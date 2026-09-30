package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sqlResultTestEngine(t *testing.T) *DuckDBEngine {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return &DuckDBEngine{db: db}
}

func TestSQLInteractiveResultBudgets(t *testing.T) {
	engine := sqlResultTestEngine(t)
	t.Run("small contract", func(t *testing.T) {
		assertions := assert.New(t)
		requirements := require.New(t)
		result, err := engine.QuerySQL(t.Context(), "SELECT 7 AS id, 'example' AS name, NULL AS absent")
		requirements.NoError(err)
		assertions.Equal([]string{"id", "name", "absent"}, result.Columns)
		assertions.Equal([][]any{{int32(7), "example", nil}}, result.Rows)
		assertions.Equal(1, result.RowCount)
	})
	t.Run("row boundary", func(t *testing.T) {
		assertions := assert.New(t)
		requirements := require.New(t)
		result, err := engine.QuerySQL(t.Context(), fmt.Sprintf("SELECT * FROM range(%d)", SQLResultMaxRows))
		requirements.NoError(err)
		assertions.Len(result.Rows, SQLResultMaxRows)
		result, err = engine.QuerySQL(t.Context(), fmt.Sprintf("SELECT * FROM range(%d)", SQLResultMaxRows+1))
		requirements.ErrorIs(err, ErrSQLResultLimit)
		assertions.Nil(result, "never return a silently truncated result")
		assertions.Contains(err.Error(), "--stream")
	})
	t.Run("byte budget", func(t *testing.T) {
		assertions := assert.New(t)
		requirements := require.New(t)
		result, err := engine.QuerySQL(t.Context(), fmt.Sprintf("SELECT repeat('x', %d) FROM range(2)", SQLResultMaxBytes/2))
		requirements.ErrorIs(err, ErrSQLResultLimit)
		assertions.Nil(result)
		assertions.Contains(err.Error(), "byte")
	})
	t.Run("cancelled", func(t *testing.T) {
		requirements := require.New(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := engine.QuerySQL(ctx, "SELECT 1")
		requirements.ErrorIs(err, context.Canceled)
	})
}

func TestSQLStreamDoesNotRetainOrLimitCompleteResult(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	engine := sqlResultTestEngine(t)
	count, bytes := 0, 0
	result, err := engine.StreamSQL(t.Context(), fmt.Sprintf("SELECT repeat('x', 2048) AS value FROM range(%d)", SQLResultMaxRows+1), func(columns []string, row []any) error {
		assertions.Equal([]string{"value"}, columns)
		if row != nil {
			count++
			value, ok := row[0].(string)
			requirements.True(ok)
			bytes += len(value)
		}
		return nil
	})
	requirements.NoError(err)
	assertions.Equal(SQLResultMaxRows+1, count)
	assertions.Greater(bytes, SQLResultMaxBytes)
	assertions.Nil(result.Rows)
	assertions.Equal(count, result.RowCount)
}

func TestSQLStreamStopsOnCancellationAndConsumerFailure(t *testing.T) {
	engine := sqlResultTestEngine(t)
	for _, cancelled := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelled), func(t *testing.T) {
			assertions := assert.New(t)
			requirements := require.New(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("export writer failed")
			rows := 0
			_, err := engine.StreamSQL(ctx, "SELECT * FROM range(1)", func(_ []string, row []any) error {
				if row == nil {
					return nil
				}
				rows++
				if cancelled {
					cancel()
					return nil
				}
				return failure
			})
			if cancelled {
				requirements.ErrorIs(err, context.Canceled)
			} else {
				requirements.ErrorIs(err, failure)
			}
			assertions.Equal(1, rows)
			_, err = engine.QuerySQL(t.Context(), "SELECT 1")
			requirements.NoError(err, "the failed export releases its query resources")
		})
	}
}
