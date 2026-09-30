package daemonclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopySQLResultJSON(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		valid       bool
	}{
		{"complete", `{"columns":["id"],"rows":[[9007199254740993],[null]],"row_count":2,"cache":{"generation":"example"}}`, true},
		{"empty", `{"columns":["id"],"rows":[],"row_count":0}`, true},
		{"legacy empty", `{"columns":["id"],"rows":null,"row_count":0}`, true},
		{"partial", `{"columns":["id"],"rows":[[1]`, false},
		{"missing count", `{"columns":["id"],"rows":[[1]]}`, false},
		{"wrong count", `{"columns":["id"],"rows":[[1]],"row_count":2}`, false},
		{"multiple objects", `{"columns":[],"rows":[],"row_count":0}{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertions := assert.New(t)
			requirements := require.New(t)
			var output bytes.Buffer
			err := copySQLResultJSON(&output, strings.NewReader(tc.input))
			if tc.valid {
				if !assertions.NoError(err) {
					return
				}
				assertions.JSONEq(tc.input, output.String())
				if strings.Contains(tc.input, "9007199254740993") {
					assertions.Contains(output.String(), "9007199254740993")
				}
			} else {
				requirements.Error(err)
			}
		})
	}
}

type failedSQLWriter struct{}

func (failedSQLWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestStreamSQLQuery(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		failed     bool
	}{
		{"complete", `{"columns":["id"],"rows":[[1]],"row_count":1}`, 200, false},
		{"partial", `{"columns":["id"],"rows":[[1]`, 200, true},
		{"accepted", `{"status":"queued","job_id":"synthetic-job"}`, 202, false},
		{"error", `{"error":{"code":"query_error","message":"invalid query"}}`, 400, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertions := assert.New(t)
			requirements := require.New(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertions.Equal("/api/v1/query", r.URL.Path)
				body, err := io.ReadAll(r.Body)
				if !assertions.NoError(err) {
					return
				}
				assertions.JSONEq(`{"sql":"SELECT 1","fresh":false,"stream":true}`, string(body))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client, err := New(Config{URL: server.URL, AllowInsecure: true})
			requirements.NoError(err)
			var output bytes.Buffer
			accepted, err := client.StreamSQLQuery(context.Background(), "SELECT 1", false, &output)
			if tc.failed {
				requirements.Error(err)
				if tc.status == 200 {
					assertions.Contains(err.Error(), "discard partial output")
				}
				return
			}
			requirements.NoError(err)
			if tc.status == 202 {
				requirements.NotNil(accepted)
				assertions.Equal("synthetic-job", accepted.JobID)
				assertions.Empty(output.String())
			} else {
				assertions.Nil(accepted)
				assertions.JSONEq(tc.body, output.String())
			}
		})
	}
}

func TestCopySQLResultJSONWriterFailure(t *testing.T) {
	requirements := require.New(t)
	err := copySQLResultJSON(failedSQLWriter{}, strings.NewReader(`{"columns":[],"rows":[],"row_count":0}`))
	requirements.ErrorContains(err, "disk full")
}
