package api

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/query"
)

func TestSQLStreamingHTTPContract(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		fail, accepted, archive bool
	}{
		{name: "complete"}, {name: "partial", fail: true}, {name: "accepted", accepted: true}, {name: "archive", archive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertions := assert.New(t)
			requirements := require.New(t)
			srv := NewServerWithOptions(ServerOptions{Config: &config.Config{}, Logger: testLogger(), SQLQueryStreamRunner: func(ctx context.Context, sql string, fresh, archive bool, consume query.SQLRowConsumer) (*query.QueryResult, *CacheBuildAccepted, error) {
				assertions.Equal("SELECT 1", sql)
				assertions.True(fresh)
				assertions.Equal(tc.archive, archive)
				if tc.accepted {
					return nil, &CacheBuildAccepted{JobID: "synthetic-job", Status: CacheBuildQueued}, nil
				}
				requirements.NoError(consume([]string{"id"}, nil))
				requirements.NoError(consume([]string{"id"}, []any{1}))
				if tc.fail {
					return nil, nil, errors.New("query interrupted")
				}
				return &query.QueryResult{RowCount: 1}, nil, nil
			}})
			path := "/api/v1/query"
			if tc.archive {
				path += "/archive"
			}
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"sql":"SELECT 1","stream":true,"fresh":true}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.Router().ServeHTTP(w, req)
			if tc.accepted {
				assertions.Equal(202, w.Code)
				assertions.Contains(w.Body.String(), "synthetic-job")
				return
			}
			requirements.Equal(200, w.Code, w.Body.String())
			var result query.QueryResult
			err := json.Unmarshal(w.Body.Bytes(), &result)
			if tc.fail {
				requirements.Error(err)
				assertions.NotContains(w.Body.String(), "row_count")
			} else {
				requirements.NoError(err)
				assertions.Equal(1, result.RowCount)
				assertions.Equal([]string{"id"}, result.Columns)
				assertions.Len(result.Rows, 1)
			}
		})
	}
}

func TestSQLInteractiveLimitHTTPError(t *testing.T) {
	assertions := assert.New(t)
	srv := NewServerWithOptions(ServerOptions{Config: &config.Config{}, Logger: testLogger(), SQLQueryRunner: func(context.Context, string, bool) (*query.QueryResult, *CacheBuildAccepted, error) {
		return nil, nil, query.ErrSQLResultLimit
	}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/query", strings.NewReader(`{"sql":"SELECT 1"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	assertions.Equal(400, w.Code)
	assertions.Contains(w.Body.String(), "result_too_large")
}
