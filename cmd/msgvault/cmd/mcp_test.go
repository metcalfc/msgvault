package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/daemonclient"
	"go.kenn.io/msgvault/internal/deletion"
	mcpserver "go.kenn.io/msgvault/internal/mcp"
)

func TestMCPWriteHelpDisclosesMutationClassesAndProfileOptIn(t *testing.T) {
	mcpCmd := newMCPCommand()

	require.NotNil(t, mcpCmd.Flags().Lookup("allow-profile-writes"))
	var output bytes.Buffer
	mcpCmd.SetOut(&output)

	require.NoError(t, mcpCmd.Help())
	help := output.String()
	assert.Contains(t, help, "attachment exports")
	assert.Contains(t, help, "deletion manifests")
	assert.Contains(t, help, "person promotion")
	assert.Contains(t, help, "private Notes writes")
}

func TestMCPCommandUsesDaemonInsteadOfOpeningLocalDatabase(t *testing.T) {
	testCtx := withStoreResolverConfig(t, &config.Config{
		HomeDir: t.TempDir(),
		Data: config.DataConfig{
			DataDir: filepath.Join(t.TempDir(), "missing-parent", "data"),
		},
		Remote: config.RemoteConfig{
			URL:           "http://daemonclient.example:8080",
			AllowInsecure: true,
		},
	})

	ctx, cancel := context.WithCancel(testCtx)
	cancel()

	cmd := newMCPCommand()
	require.NoError(t, cmd.Flags().Set("http", "127.0.0.1:0"))
	cmd.SetContext(testCtx)
	cmd.SetContext(ctx)
	err := cmd.RunE(cmd, nil)

	require.Error(t, err, "canceled MCP serve should return")
	require.ErrorIs(t, err, context.Canceled, "error should preserve context cancellation: %v", err)
	assert.NotContains(t, err.Error(), "open database", "MCP command must not open SQLite directly")
}

func TestMCPCommandForwardsHTTPPolicy(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok", "api_schema_version": api.APISchemaVersion,
			})
			return
		}
		if r.URL.Path == "/api/v1/multimodal/status" {
			http.Error(w, `{"error":"visual_search_not_ready"}`, http.StatusServiceUnavailable)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(daemon.Close)

	home := t.TempDir()
	testCtx := withStoreResolverConfig(t, &config.Config{
		HomeDir: home,
		Data:    config.DataConfig{DataDir: t.TempDir()},
		Server:  config.ServerConfig{APIKey: "mcp-http-key"},
		Remote: config.RemoteConfig{
			URL:           daemon.URL,
			APIKey:        "daemon-key",
			AllowInsecure: true,
		},
	})

	mcpCmd := newMCPCommand()
	require.NoError(t, mcpCmd.Flags().Set("http", "0.0.0.0:8081"))
	require.NoError(t, mcpCmd.Flags().Set("http-allow-insecure", "true"))
	require.NoError(t, mcpCmd.Flags().Set("allow-profile-writes", "true"))
	savedServeHTTP := serveMCPHTTPWithOptions
	allowWritesFlag := mcpCmd.Flags().Lookup("http-allow-writes")
	require.NotNil(t, allowWritesFlag, "mcp command must define --http-allow-writes")
	require.NoError(t, allowWritesFlag.Value.Set("true"))

	t.Cleanup(func() {
		assert.NoError(t, allowWritesFlag.Value.Set("false"))

		serveMCPHTTPWithOptions = savedServeHTTP
	})

	wantErr := errors.New("stop after capture")
	var gotServeOpts mcpserver.ServeOptions
	var gotHTTPOpts mcpserver.HTTPOptions
	serveMCPHTTPWithOptions = func(_ context.Context, serveOpts mcpserver.ServeOptions, httpOpts mcpserver.HTTPOptions) error {
		gotServeOpts = serveOpts
		gotHTTPOpts = httpOpts
		return wantErr
	}

	mcpCmd.SetContext(testCtx)
	err := mcpCmd.RunE(mcpCmd, nil)

	require.ErrorIs(t, err, wantErr)
	assert.True(t, gotServeOpts.AllowProfileWrites)
	assert.Equal(t, mcpserver.HTTPOptions{
		Addr:               "0.0.0.0:8081",
		DiscoveryDirectory: filepath.Join(home, "mcp"),
		BackendURL:         daemon.URL,
		APIKey:             "mcp-http-key",
		AllowWrites:        true,
	}, gotHTTPOpts)
}

func TestDaemonMCPHybridSearcherPreservesPhaseTimings(t *testing.T) {
	client := newMCPDaemonClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/search", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"query":"semantic terms",
			"mode":"hybrid",
			"returned":0,
			"pool_saturated":false,
			"accelerator":"vec1_ivf_opq",
			"has_more":false,
			"generation":{"id":7,"model":"fake","dimension":4,"fingerprint":"fake:4","state":"active"},
			"took_ms":12,
			"timings":{"query_embedding_ms":2,"retrieval_ms":7,"hydration_ms":3},
			"results":[]
		}`))
	})

	result, err := (daemonMCPHybridSearcher{client: client}).SearchHybrid(t.Context(), mcpserver.HybridSearchRequest{
		Query: "semantic terms", Mode: "hybrid", Limit: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(12), result.TookMS)
	assert.Equal(t, "vec1_ivf_opq", result.Accelerator)
	assert.Equal(t, mcpserver.HybridSearchTimings{
		QueryEmbeddingMS: 2,
		RetrievalMS:      7,
		HydrationMS:      3,
	}, result.Timings)
}

func TestDaemonMCPServeOptionsUsesHealthForVectorTools(t *testing.T) {
	testCtx := withStoreResolverConfig(t, &config.Config{
		Data: config.DataConfig{DataDir: t.TempDir()},
	})
	_ = testCtx
	tests := []struct {
		name       string
		health     string
		wantText   bool
		wantVisual bool
	}{
		{name: "both disabled", health: `{"status":"ok","api_schema_version":"2.28.0","vector":{"status":"disabled","text_enabled":false,"visual_enabled":false}}`},
		{name: "text only", health: `{"status":"ok","api_schema_version":"2.28.0","vector":{"status":"ready","text_enabled":true,"visual_enabled":false}}`, wantText: true},
		{name: "visual only", health: `{"status":"ok","api_schema_version":"2.28.0","vector":{"status":"ready","text_enabled":false,"visual_enabled":true}}`, wantVisual: true},
		{name: "both enabled", health: `{"status":"ok","api_schema_version":"2.28.0","vector":{"status":"ready","text_enabled":true,"visual_enabled":true}}`, wantText: true, wantVisual: true},
		{name: "initializing lanes stay registered", health: `{"status":"ok","api_schema_version":"2.28.0","vector":{"status":"initializing","text_enabled":true,"visual_enabled":true}}`, wantText: true, wantVisual: true},
		{name: "failed lanes stay registered", health: `{"status":"ok","api_schema_version":"2.28.0","vector":{"status":"error","text_enabled":true,"visual_enabled":true}}`, wantText: true, wantVisual: true},
		{name: "stale lanes stay registered", health: `{"status":"ok","api_schema_version":"2.28.0","vector":{"status":"stale","text_enabled":true,"visual_enabled":true}}`, wantText: true, wantVisual: true},
		{name: "legacy health without lane fields", health: `{"status":"ok","api_schema_version":"2.26.0","vector":{"status":"ready"}}`},
		{name: "visual route predecessor", health: `{"status":"ok","api_schema_version":"2.3.0","vector":{"status":"ready","text_enabled":true,"visual_enabled":true}}`},
		{name: "lane facts predecessor", health: `{"status":"ok","api_schema_version":"2.27.0","vector":{"status":"ready","text_enabled":true,"visual_enabled":true}}`},
		{name: "missing schema", health: `{"status":"ok","vector":{"status":"ready","text_enabled":true,"visual_enabled":true}}`},
		{name: "malformed schema", health: `{"status":"ok","api_schema_version":"unknown","vector":{"status":"ready","text_enabled":true,"visual_enabled":true}}`},
		{name: "health unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var healthRequests atomic.Int32
			client := newMCPDaemonClient(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/v1/health", r.URL.Path, "startup must not request archive statistics")
				healthRequests.Add(1)
				if tt.health == "" {
					http.Error(w, `{"error":"temporarily_unavailable"}`, http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.health))
			})

			opts := daemonMCPServeOptions(t.Context(), client, invocationFromContext(t.Context()))
			assert.Equal(t, tt.wantText, opts.HybridSearcher != nil, "semantic search")
			assert.Equal(t, tt.wantText, opts.SimilarSearcher != nil, "similar messages")
			assert.Equal(t, tt.wantVisual, opts.VisualSearcher != nil, "visual search")
			assert.Equal(t, int32(1), healthRequests.Load(), "reuse the schema probe")
		})
	}
}

func TestDaemonMCPVectorReadinessIsCheckedAtRequestTime(t *testing.T) {
	testCtx := withStoreResolverConfig(t, &config.Config{
		Data: config.DataConfig{DataDir: t.TempDir()},
	})

	requests := make(chan string, 8)
	client := newMCPDaemonClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		if r.URL.Path == "/api/v1/health" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok", "api_schema_version": api.APISchemaVersion,
				"vector": map[string]any{
					"status":         "initializing",
					"text_enabled":   true,
					"visual_enabled": false,
				},
			})
			return
		}
		http.Error(w, `{"error":"vector_initializing","message":"Vector search is initializing"}`, http.StatusServiceUnavailable)
	})

	opts := daemonMCPServeOptions(testCtx, client, invocationFromContext(testCtx))
	_, err := opts.HybridSearcher.SearchHybrid(testCtx, mcpserver.HybridSearchRequest{
		Query: "term",
		Mode:  "hybrid",
	})
	var coded interface{ APIErrorCode() string }
	require.ErrorAs(t, err, &coded)
	assert.Equal(t, "vector_initializing", coded.APIErrorCode())
	path := <-requests
	assert.Equal(t, "/api/v1/health", path, "startup should only probe health")
	path = <-requests
	assert.Equal(t, "/api/v1/search", path, "vector readiness belongs to the request")
}

func TestDaemonMCPServeOptionsGatesPeopleToolsByAPISchema(t *testing.T) {
	testCtx := withStoreResolverConfig(t, &config.Config{
		Data: config.DataConfig{DataDir: t.TempDir()},
	})
	_ = testCtx
	tests := []struct {
		name           string
		schemaVersion  string
		wantPeople     bool
		wantDirectory  bool
		wantSavedViews bool
		wantMeetings   bool
		wantAgenda     bool
		wantArchiveSQL bool
	}{
		{name: "people schema", schemaVersion: "2.10.0", wantPeople: true},
		{name: "directory predecessor", schemaVersion: "2.12.9", wantPeople: true},
		{name: "directory schema", schemaVersion: "2.13.0", wantPeople: true, wantDirectory: true},
		{name: "newer schema", schemaVersion: "2.14.0", wantPeople: true, wantDirectory: true},
		{name: "schema before the Saved View run endpoint", schemaVersion: "2.20.0", wantPeople: true, wantDirectory: true},
		{name: "saved view run schema", schemaVersion: "2.21.0", wantPeople: true, wantDirectory: true, wantSavedViews: true},
		{name: "schema before the meeting endpoints", schemaVersion: "2.24.0", wantPeople: true, wantDirectory: true, wantSavedViews: true},
		{name: "meeting predecessor 2.25.x", schemaVersion: "2.25.0", wantPeople: true, wantDirectory: true, wantSavedViews: true},
		{name: "meeting predecessor 2.26.x", schemaVersion: "2.26.0", wantPeople: true, wantDirectory: true, wantSavedViews: true},
		{name: "meeting schema", schemaVersion: "2.27.0", wantPeople: true, wantDirectory: true, wantSavedViews: true, wantMeetings: true},
		{name: "person agenda predecessor", schemaVersion: "2.29.0", wantPeople: true, wantDirectory: true, wantSavedViews: true, wantMeetings: true},
		{name: "person agenda schema", schemaVersion: "2.30.0", wantPeople: true, wantDirectory: true, wantSavedViews: true, wantMeetings: true, wantAgenda: true},
		{name: "archive SQL schema", schemaVersion: "2.31.0", wantPeople: true, wantDirectory: true, wantSavedViews: true, wantMeetings: true, wantAgenda: true, wantArchiveSQL: true},
		{name: "older same-major schema", schemaVersion: "2.9.9"},
		{name: "malformed schema", schemaVersion: "not-a-version"},
		{name: "missing schema"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newMCPDaemonClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/health":
					body := map[string]any{"status": "ok"}
					if tt.schemaVersion != "" {
						body["api_schema_version"] = tt.schemaVersion
					}
					_ = json.NewEncoder(w).Encode(body)
				default:
					http.NotFound(w, r)
				}
			})

			opts := daemonMCPServeOptions(t.Context(), client, invocationFromContext(t.Context()))
			if tt.wantPeople {
				assert.NotNil(t, opts.PeopleBackend)
			} else {
				assert.Nil(t, opts.PeopleBackend)
			}
			if tt.wantSavedViews {
				assert.NotNil(t, opts.SavedViews, "Saved View tools need the daemon run endpoint")
			} else {
				assert.Nil(t, opts.SavedViews, "an older daemon cannot run Saved Views")
			}
			assert.Equal(t, tt.wantDirectory, opts.DirectoryBackend != nil)
			assert.Equal(t, tt.wantMeetings, opts.Meetings != nil)
			assert.Equal(t, tt.wantAgenda, opts.PersonAgendaBackend != nil)
			assert.Equal(t, tt.wantArchiveSQL, opts.ArchiveSQLQuerier != nil)
		})
	}
}

func TestDaemonMCPServeOptionsWarnsWhenPeopleCapabilityProbeFails(t *testing.T) {
	testCtx := withStoreResolverConfig(t, &config.Config{
		Data: config.DataConfig{DataDir: t.TempDir()},
	})
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	inv := invocationFromContext(testCtx)
	previousInvocationLogger := inv.logger
	inv.logger = logger
	t.Cleanup(func() { inv.logger = previousInvocationLogger })

	client := newMCPDaemonClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			http.Error(w, `{"error":"temporarily_unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		http.NotFound(w, r)
	})

	opts := daemonMCPServeOptions(testCtx, client, invocationFromContext(testCtx))
	assert.Nil(t, opts.PeopleBackend)
	assert.Nil(t, opts.DirectoryBackend)
	assert.Nil(t, opts.ArchiveSQLQuerier)
	assert.Contains(t, logs.String(), "people tools disabled")
}

func TestDaemonMCPServeOptionsUsesOneCapabilityProbe(t *testing.T) {
	testCtx := withStoreResolverConfig(t, &config.Config{
		Data: config.DataConfig{DataDir: t.TempDir()},
	})
	var logs bytes.Buffer
	var healthRequests atomic.Int32
	client := newMCPDaemonClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			if healthRequests.Add(1) == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": "ok", "api_schema_version": "2.28.0",
					"vector": map[string]any{
						"status":         "ready",
						"text_enabled":   true,
						"visual_enabled": true,
					},
				})
				return
			}
			http.Error(w, `{"error":"temporarily_unavailable"}`, http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	})

	opts := daemonMCPServeOptions(testCtx, client, invocationFromContext(testCtx))
	assert.NotNil(t, opts.PeopleBackend)
	assert.NotNil(t, opts.DirectoryBackend)
	assert.NotNil(t, opts.SavedViews)
	assert.NotNil(t, opts.HybridSearcher)
	assert.NotNil(t, opts.SimilarSearcher)
	assert.NotNil(t, opts.VisualSearcher)
	assert.Equal(t, int32(1), healthRequests.Load())
	assert.Empty(t, logs.String())
}

func TestDaemonMCPServeOptionsSavesDeletionManifestsThroughDaemon(t *testing.T) {
	testCtx := withStoreResolverConfig(t, &config.Config{
		Data: config.DataConfig{DataDir: t.TempDir()},
	})

	var manifestRequests atomic.Int32
	client := newMCPDaemonClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "ok", "api_schema_version": api.APISchemaVersion,
			})
		case "/api/v1/cli/deletion-manifests":
			manifestRequests.Add(1)
			assert.Equal(t, http.MethodPost, r.Method, "method")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"batch-1","message_count":1}`))
		default:
			http.NotFound(w, r)
		}
	})

	opts := daemonMCPServeOptions(testCtx, client, invocationFromContext(testCtx))
	require.NotNil(t, opts.ManifestSaver, "manifest saver")

	manifest := deletion.NewManifest("mcp test", []string{"gmail-001"})
	err := opts.ManifestSaver.SaveManifest(testCtx, manifest)
	require.NoError(t, err)
	assert.Equal(t, int32(1), manifestRequests.Load(), "manifest requests")
}

func newMCPDaemonClient(t *testing.T, handler http.HandlerFunc) *daemonclient.Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "key", r.Header.Get("X-Api-Key"), "api key")
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	client, err := daemonclient.New(daemonclient.Config{
		URL:           srv.URL,
		APIKey:        "key",
		AllowInsecure: true,
	})
	require.NoError(t, err)
	return client
}

func TestNormalizeMCPHTTPAddr(t *testing.T) {
	t.Run("bare_port_defaults_to_loopback", func(t *testing.T) {
		got, err := normalizeMCPHTTPAddr("8080", false, false)
		require.NoError(t, err)
		require.Equal(t, "127.0.0.1:8080", got)
	})

	t.Run("colon_port_defaults_to_loopback", func(t *testing.T) {
		got, err := normalizeMCPHTTPAddr(":8080", false, false)
		require.NoError(t, err)
		require.Equal(t, "127.0.0.1:8080", got)
	})

	t.Run("explicit_loopback_passes", func(t *testing.T) {
		cases := []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"}
		for _, c := range cases {
			got, err := normalizeMCPHTTPAddr(c, false, false)
			require.NoError(t, err, "%s", c)
			assert.Equal(t, c, got, "%s: should be unchanged", c)
		}
	})

	t.Run("non_loopback_rejected_without_optin", func(t *testing.T) {
		cases := []string{
			"0.0.0.0:8080",
			"192.168.1.5:8080",
			"vault.local:8080",
			// Regression: empty-bracket host parses cleanly via
			// net.SplitHostPort but binds to all interfaces. Must
			// be rejected, not silently treated as loopback.
			"[]:8080",
		}
		for _, c := range cases {
			_, err := normalizeMCPHTTPAddr(c, false, false)
			require.Error(t, err, "%s: expected refusal", c)
			assert.ErrorContains(t, err, "--http-allow-insecure", "%s: expected hint", c)
		}
	})

	t.Run("non_loopback_allowed_with_optin", func(t *testing.T) {
		got, err := normalizeMCPHTTPAddr("0.0.0.0:8080", true, false)
		require.NoError(t, err)
		require.Equal(t, "0.0.0.0:8080", got)
	})

	t.Run("non_loopback_allowed_with_api_key", func(t *testing.T) {
		got, err := normalizeMCPHTTPAddr("0.0.0.0:8080", false, true)
		require.NoError(t, err)
		require.Equal(t, "0.0.0.0:8080", got)
	})

	t.Run("empty_rejected", func(t *testing.T) {
		_, err := normalizeMCPHTTPAddr("", false, false)
		require.Error(t, err, "expected error for empty addr")
	})

	t.Run("garbage_rejected", func(t *testing.T) {
		_, err := normalizeMCPHTTPAddr("not-a-port", false, false)
		require.Error(t, err, "expected error for non-port, non-host:port")
	})
}
