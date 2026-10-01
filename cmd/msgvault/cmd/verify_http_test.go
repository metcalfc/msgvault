package cmd

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyUsesConfiguredRemoteHTTPAndPreservesOutput(t *testing.T) {
	assert := assert.New(t)

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(http.MethodPost, r.Method, "method")
		assert.Equal("/api/v1/cli/verify", r.URL.Path, "path")
		assert.Equal("alice@example.com", r.URL.Query().Get("email"), "email query")
		assert.Equal("25", r.URL.Query().Get("sample"), "sample query")
		assert.Equal("true", r.URL.Query().Get("skip_db_check"), "skip_db_check query")
		assert.Equal("true", r.URL.Query().Get("json"), "json query")
		requests.Add(1)

		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"type":"stdout","data":"{\"email\":\"alice@example.com\"}\n"}` + "\n"))
		_, _ = w.Write([]byte(`{"type":"stderr","data":"verify warning\n"}` + "\n"))
		_, _ = w.Write([]byte(`{"type":"complete"}` + "\n"))
	}))
	t.Cleanup(server.Close)

	testCtx := configureRemoteSyncTest(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := newVerifyCmd()
	cmd.SetContext(testCtx)
	cmd.SetArgs([]string{"alice@example.com", "--sample", "25", "--skip-db-check", "--json"})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	require.NoError(t, err, "verify command")
	assert.Equal(int32(1), requests.Load(), "HTTP requests")
	assert.JSONEq("{\"email\":\"alice@example.com\"}\n", stdout.String())
	assert.Equal("verify warning\n", stderr.String())
}
