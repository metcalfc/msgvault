package cmd

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/daemon"
	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

// TestStatsCommand_AccountAndCollectionMutuallyExclusive confirms that passing
// both --account and --collection to the stats command is rejected by cobra.
func TestStatsCommand_AccountAndCollectionMutuallyExclusive(t *testing.T) {
	cmd := newStatsCommand()
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"--account", "foo@example.com", "--collection", "bar"})

	err := cmd.Execute()
	require.Error(t, err, "expected error when both --account and --collection are set")
	msg := err.Error()
	assert.Contains(t, msg, "account", "error should mention account flag name")
	assert.Contains(t, msg, "collection", "error should mention collection flag name")
}

// TestStatsCommand_EmptyCollectionRejected verifies that
// `stats --collection <name>` errors out when the named collection
// has zero member sources, instead of silently falling through to
// archive-wide stats. Regression test for iter13 codex Medium:
// previously, an empty collection produced a non-IsEmpty Scope but
// SourceIDs() returned an empty slice, and GetStatsForScope treats
// an empty slice as unscoped/global.
func TestStatsCommand_EmptyCollectionRejected(t *testing.T) {
	dataDir := t.TempDir()
	st := testutil.NewTestStore(t)
	src, err := st.GetOrCreateSource("gmail", "alice@example.com")
	require.NoError(t, err, "create source")
	_, err = st.CreateCollection("empty", "test", []int64{src.ID})
	require.NoError(t, err, "create collection")
	require.NoError(t, st.RemoveSourcesFromCollection("empty", []int64{src.ID}), "remove source from collection")
	startStoreAPIDaemon(t, dataDir, st, nil)

	cfg := &config.Config{
		HomeDir: dataDir,
		Data:    config.DataConfig{DataDir: dataDir},
		Remote:  config.RemoteConfig{URL: "http://configured-daemonclient.invalid"},
	}
	testCtx := testInvocationContext(t.Context(), cfg, invocationOptions{useLocal: true})

	testCmd := newStatsCommand()
	root := newTestRootCmd()
	root.SetContext(testCtx)
	root.AddCommand(testCmd)
	root.SetArgs([]string{"stats", "--collection", "empty"})

	err = root.Execute()
	require.Error(t, err, "expected error for empty collection")
	assert.Contains(t, err.Error(), "no member accounts")
}

func TestStatsCommand_ScopedUsesLocalDaemonHTTPAndPreservesLocalOutput(t *testing.T) {
	dataDir := t.TempDir()
	testCfg := &config.Config{
		HomeDir: dataDir,
		Data:    config.DataConfig{DataDir: dataDir},
	}
	server, statsRequests := statsHTTPDaemon(t)
	writeStatsHTTPDaemonRuntime(t, dataDir, server)

	testCtx := testInvocationContext(t.Context(), testCfg, invocationOptions{useLocal: true})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := newStatsCommand()
	cmd.SetArgs([]string{"--collection", "Important"})
	cmd.SetContext(testCtx)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	require.NoError(t, err, "stats command")

	assert.Equal(t, int32(1), statsRequests.Load(), "exactly one CLI stats request")
	assert.Empty(t, stderr.String(), "stderr")
	assert.Equal(t, `Stats for collection "Important" (2 accounts):
  Messages:    8
  Threads:     6
  Attachments: 3
  Labels:      9
  Accounts:    2
  Size:        2.00 MB

Note: Size is global (not scoped).
`, stdout.String())
}

func TestStatsCommand_UnscopedUsesLocalDaemonHTTPAndPreservesLocalOutput(t *testing.T) {
	dataDir := t.TempDir()
	testCfg := &config.Config{
		HomeDir: dataDir,
		Data:    config.DataConfig{DataDir: dataDir},
	}
	server, statsRequests := statsHTTPDaemon(t)
	writeStatsHTTPDaemonRuntime(t, dataDir, server)

	testCtx := testInvocationContext(t.Context(), testCfg, invocationOptions{useLocal: true})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := newStatsCommand()
	cmd.SetArgs([]string{})
	cmd.SetContext(testCtx)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	require.NoError(t, err, "stats command")

	assert.Equal(t, int32(1), statsRequests.Load(), "exactly one CLI stats request")
	assert.Empty(t, stderr.String(), "stderr")
	assert.Equal(t, "Database: "+testCfg.DatabaseDSN()+`
  Messages:    3
  Threads:     2
  Attachments: 5
  Labels:      4
  Accounts:    1
  Size:        1.00 MB
`, stdout.String())
}

func statsHTTPDaemon(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	statsRequests := &atomic.Int32{}
	mux := http.NewServeMux()
	mux.Handle("/api/ping", daemon.NewPingHandler(daemon.PingHandlerOptions{
		Service: daemonService,
		Version: Version,
	}))
	mux.HandleFunc("/api/v1/stats", func(w http.ResponseWriter, _ *http.Request) {
		assert.Fail(t, "stats command must not use the general statistics route")
		http.Error(w, "use /api/v1/cli/stats", http.StatusInternalServerError)
	})
	mux.HandleFunc("/api/v1/cli/stats", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		statsRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("collection") == "Important" {
			_, _ = w.Write([]byte(`{
				"stats": {
					"total_messages": 8,
					"total_threads": 6,
					"total_accounts": 2,
					"total_labels": 9,
					"total_attachments": 3,
					"database_size_bytes": 2097152
				},
				"scope_label": "Important",
				"scope_source_count": 2
			}`))
			return
		}
		if r.URL.Query().Get("account") != "" || r.URL.Query().Get("collection") != "" {
			http.Error(w, "unexpected scope", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{
			"stats": {
				"total_messages": 3,
				"total_threads": 2,
				"total_accounts": 1,
				"total_labels": 4,
				"total_attachments": 5,
				"database_size_bytes": 1048576
			}
		}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, statsRequests
}

func registerStatsProbeHandler(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}

func writeStatsHTTPDaemonRuntime(t *testing.T, dataDir string, server *httptest.Server) {
	t.Helper()
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err, "split listener address")
	_, err = strconv.Atoi(portText)
	require.NoError(t, err, "parse listener port")

	_, err = daemonRuntimeStore(dataDir).Write(daemon.RuntimeRecord{
		PID:     os.Getpid(),
		Network: daemon.NetworkTCP,
		Address: net.JoinHostPort(host, portText),
		Service: daemonService,
		Version: Version,
		Metadata: map[string]string{
			runtimeHost:             host,
			runtimePort:             portText,
			runtimeAPIVersion:       strconv.Itoa(daemonAPIVersion),
			runtimeAPISchemaVersion: api.APISchemaVersion,
			runtimeAuthFingerprint:  daemonAPIKeyFingerprint(""),
			runtimeCreateTime:       matchingProcessCreateTime(t),
		},
	})
	require.NoError(t, err, "write daemon runtime")
}

// TestPrintStats_ThousandsGroupingUniform verifies every count in the stats
// output uses the same thousands-grouping so Messages/Threads/Attachments/
// Labels/Accounts are formatted consistently.
func TestPrintStats_ThousandsGroupingUniform(t *testing.T) {
	var out bytes.Buffer
	printStats(&out, &store.Stats{
		MessageCount:    2470176,
		ThreadCount:     561070,
		AttachmentCount: 202662,
		LabelCount:      1183,
		SourceCount:     12345,
		DatabaseSize:    1024 * 1024,
	})
	got := out.String()
	assert.Contains(t, got, "Messages:    2,470,176", "messages grouped")
	assert.Contains(t, got, "Threads:     561,070", "threads grouped")
	assert.Contains(t, got, "Attachments: 202,662", "attachments grouped")
	assert.Contains(t, got, "Labels:      1,183", "labels grouped")
	assert.Contains(t, got, "Accounts:    12,345", "accounts grouped")
	assert.NotContains(t, got, "561070", "no bare thread count")
	assert.NotContains(t, got, "202662", "no bare attachment count")
}
