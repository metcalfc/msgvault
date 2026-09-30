package query

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
)

// Interactive results are bounded separately from DuckDB's execution memory.
// The byte budget counts JSON-encoded columns and rows; rows also have a count
// bound so tiny values cannot create an arbitrarily large Go object graph.
const (
	SQLResultMaxRows  = 10_000
	SQLResultMaxBytes = 16 << 20
)

var ErrSQLResultLimit = errors.New("SQL result exceeds interactive limit")

func sqlResultLimitError(kind string, limit int) error {
	return fmt.Errorf("%w: %s budget is %d; narrow the SELECT or add a LIMIT (the CLI can export everything with msgvault query --stream)", ErrSQLResultLimit, kind, limit)
}

type sqlResultBudget struct{ bytes int }

func (b *sqlResultBudget) add(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode SQL result: %w", err)
	}
	if len(encoded) > SQLResultMaxBytes-b.bytes {
		return sqlResultLimitError("encoded byte", SQLResultMaxBytes)
	}
	b.bytes += len(encoded)
	return nil
}

// SQLRowConsumer receives columns with a nil row once, then each non-nil row.
// Row values are only valid during the call; consumers must not retain them.
type SQLRowConsumer func(columns []string, row []any) error

// SQLStreamer is the explicit export capability, without an aggregate row or
// byte limit. Implementations retain query cancellation and resource controls.
type SQLStreamer interface {
	StreamSQL(ctx context.Context, sql string, consume SQLRowConsumer) (*QueryResult, error)
}
