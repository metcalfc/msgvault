package cmd

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/daemon"
	"go.kenn.io/msgvault/internal/config"
)

type failingAttachmentStream struct {
	sent bool
}

func (r *failingAttachmentStream) Read(p []byte) (int, error) {
	if r.sent {
		return 0, errors.New("stream failed")
	}
	r.sent = true
	return copy(p, "partial download"), errors.New("stream failed")
}

type closeFailingAttachmentStream struct {
	io.Reader

	closeErr error
}

func (r *closeFailingAttachmentStream) Close() error {
	return r.closeErr
}

func TestExportAttachmentBinaryDownloadPreservesExistingFileOnError(t *testing.T) {
	tmpDir := t.TempDir()
	outFile := filepath.Join(tmpDir, "attachment.bin")
	original := []byte("existing data")
	require.NoError(t, os.WriteFile(outFile, original, 0o600), "seed output file")

	exportAttachmentOutput := outFile

	err := exportAttachmentBinaryDownload(io.NopCloser(&failingAttachmentStream{}), exportAttachmentOutput)
	require.Error(t, err, "streaming failure should be returned")

	got, readErr := os.ReadFile(outFile)
	require.NoError(t, readErr, "read original output")
	assert.Equal(t, original, got, "pre-existing output must survive failed stream")
}

func TestExportAttachmentBinaryDownloadReplacesExistingFile(t *testing.T) {
	tmpDir := t.TempDir()
	outFile := filepath.Join(tmpDir, "attachment.bin")
	require.NoError(t, os.WriteFile(outFile, []byte("old data"), 0o600), "seed output file")

	exportAttachmentOutput := outFile

	err := exportAttachmentBinaryDownload(io.NopCloser(strings.NewReader("new data")), exportAttachmentOutput)
	require.NoError(t, err, "streaming replacement should succeed")

	got, readErr := os.ReadFile(outFile)
	require.NoError(t, readErr, "read replaced output")
	assert.Equal(t, []byte("new data"), got, "pre-existing output should be replaced")
}

func TestExportAttachmentBinaryDownloadPreservesExistingFileOnCloseError(t *testing.T) {
	tmpDir := t.TempDir()
	outFile := filepath.Join(tmpDir, "attachment.bin")
	original := []byte("existing verified data")
	require.NoError(t, os.WriteFile(outFile, original, 0o600), "seed output file")

	exportAttachmentOutput := outFile

	doneErr := captureStderr(t)
	err := exportAttachmentBinaryDownload(&closeFailingAttachmentStream{
		Reader:   strings.NewReader("unverified replacement"),
		closeErr: errors.New("verification failed during close"),
	}, exportAttachmentOutput)
	stderr := doneErr()
	require.Error(t, err, "close-time verification failure should be returned")
	require.ErrorContains(t, err, "verification failed during close")
	assert.NotContains(t, stderr, "Exported attachment", "failed export must not report success")

	got, readErr := os.ReadFile(outFile)
	require.NoError(t, readErr, "read original output")
	assert.Equal(t, original, got, "pre-existing output must survive close-time verification failure")
	temps, globErr := filepath.Glob(filepath.Join(tmpDir, ".attachment.bin.tmp-*"))
	require.NoError(t, globErr, "find staged output files")
	assert.Empty(t, temps, "failed export must remove its staged output")
}

func TestExportAttachmentUsesLocalDaemonHTTPAndPreservesFileOutput(t *testing.T) {
	dataDir := t.TempDir()
	wantData := []byte("daemon attachment content")
	contentHash := fmt.Sprintf("%x", sha256.Sum256(wantData))
	server, attachmentRequests := attachmentHTTPDaemon(t, contentHash, wantData)
	writeStatsHTTPDaemonRuntime(t, dataDir, server)

	cfg := &config.Config{
		HomeDir: dataDir,
		Data:    config.DataConfig{DataDir: dataDir},
		Remote:  config.RemoteConfig{URL: "http://configured-daemonclient.invalid"},
	}
	testCtx := testInvocationContext(t.Context(), cfg, invocationOptions{})
	invocationFromContext(testCtx).options.useLocal = true
	t.Chdir(dataDir)
	exportAttachmentOutput := "attachment.bin"
	exportAttachmentJSON := false
	exportAttachmentBase64 := false

	doneErr := captureStderr(t)
	cmd := newExportAttachmentTestCommand(t, exportAttachmentOutput, exportAttachmentJSON, exportAttachmentBase64)
	cmd.SetContext(testCtx)

	err := runExportAttachment(cmd, []string{contentHash})
	stderr := doneErr()
	require.NoError(t, err, "export-attachment")

	outputPath := filepath.Join(dataDir, exportAttachmentOutput)
	got, err := os.ReadFile(outputPath)
	require.NoError(t, err, "read output")
	assert.Equal(t, wantData, got, "output")
	assert.Equal(t, 1, int(attachmentRequests.Load()), "attachment endpoint calls")
	assert.Contains(t, stderr, "Exported attachment to: "+exportAttachmentOutput, "stderr")
	assert.Contains(t, stderr, "("+strconv.Itoa(len(wantData))+" bytes)", "stderr size")
}

func TestExportAttachmentUsesLocalDaemonHTTPAndPreservesJSONOutput(t *testing.T) {
	dataDir := t.TempDir()
	wantData := []byte("daemon attachment content")
	contentHash := fmt.Sprintf("%x", sha256.Sum256(wantData))
	server, attachmentRequests := attachmentHTTPDaemon(t, contentHash, wantData)
	writeStatsHTTPDaemonRuntime(t, dataDir, server)

	cfg := &config.Config{
		HomeDir: dataDir,
		Data:    config.DataConfig{DataDir: dataDir},
		Remote:  config.RemoteConfig{URL: "http://configured-daemonclient.invalid"},
	}
	testCtx := testInvocationContext(t.Context(), cfg, invocationOptions{})
	invocationFromContext(testCtx).options.useLocal = true
	exportAttachmentOutput := ""
	exportAttachmentJSON := true
	exportAttachmentBase64 := false

	done := captureStdout(t)
	cmd := newExportAttachmentTestCommand(t, exportAttachmentOutput, exportAttachmentJSON, exportAttachmentBase64)
	cmd.SetContext(testCtx)

	err := runExportAttachment(cmd, []string{contentHash})
	out := done()
	require.NoError(t, err, "export-attachment --json")

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &result), "decode JSON")
	assert.Equal(t, contentHash, result["content_hash"], "content_hash")
	assert.InDelta(t, float64(len(wantData)), result["size"], 0, "size")
	dataB64, ok := result["data_base64"].(string)
	require.True(t, ok, "data_base64 is string")
	got, err := base64.StdEncoding.DecodeString(dataB64)
	require.NoError(t, err, "decode base64")
	assert.Equal(t, wantData, got, "decoded data")
	assert.Equal(t, 1, int(attachmentRequests.Load()), "attachment endpoint calls")
}

func TestExportAttachmentUsesLocalDaemonHTTPAndPreservesBase64Output(t *testing.T) {
	dataDir := t.TempDir()
	wantData := []byte("daemon attachment content")
	contentHash := fmt.Sprintf("%x", sha256.Sum256(wantData))
	server, attachmentRequests := attachmentHTTPDaemon(t, contentHash, wantData)
	writeStatsHTTPDaemonRuntime(t, dataDir, server)

	cfg := &config.Config{
		HomeDir: dataDir,
		Data:    config.DataConfig{DataDir: dataDir},
		Remote:  config.RemoteConfig{URL: "http://configured-daemonclient.invalid"},
	}
	testCtx := testInvocationContext(t.Context(), cfg, invocationOptions{})
	invocationFromContext(testCtx).options.useLocal = true
	exportAttachmentOutput := ""
	exportAttachmentJSON := false
	exportAttachmentBase64 := true

	done := captureStdout(t)
	cmd := newExportAttachmentTestCommand(t, exportAttachmentOutput, exportAttachmentJSON, exportAttachmentBase64)
	cmd.SetContext(testCtx)

	err := runExportAttachment(cmd, []string{contentHash})
	out := done()
	require.NoError(t, err, "export-attachment --base64")

	assert.Equal(t, base64.StdEncoding.EncodeToString(wantData)+"\n", out, "base64 output")
	assert.Equal(t, 1, int(attachmentRequests.Load()), "attachment endpoint calls")
}

func TestExportAttachment_FlagMutualExclusivity(t *testing.T) {
	tests := []struct {
		name   string
		output string
		json   bool
		base64 bool
		errMsg string
	}{
		{"json+base64", "", true, true, "--json and --base64 are mutually exclusive"},
		{"json+output", "file.bin", true, false, "--json and --output are mutually exclusive"},
		{"base64+output", "file.bin", false, true, "--base64 and --output are mutually exclusive"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exportAttachmentOutput := tc.output
			exportAttachmentJSON := tc.json
			exportAttachmentBase64 := tc.base64

			// Use a valid hash — flag validation happens before file access
			err := runExportAttachment(newExportAttachmentTestCommand(t, exportAttachmentOutput, exportAttachmentJSON, exportAttachmentBase64), []string{
				"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			})

			require.Error(t, err, "expected error containing %q", tc.errMsg)
			assert.ErrorContains(t, err, tc.errMsg)
		})
	}
}

func attachmentHTTPDaemon(t *testing.T, contentHash string, data []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	mux := http.NewServeMux()
	mux.Handle("/api/ping", daemon.NewPingHandler(daemon.PingHandlerOptions{
		Service: daemonService,
		Version: Version,
	}))
	mux.HandleFunc("/api/v1/cli/attachment", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Query().Get("content_hash") != contentHash {
			http.Error(w, "wrong content hash", http.StatusBadRequest)
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, requests
}

func TestExportAttachment_HashValidation(t *testing.T) {
	tests := []struct {
		name string
		hash string
	}{
		{"too short", "61ccf192"},
		{"too long", "61ccf192b5bd358738802dc2676d3ceab856f47d26dd29681ac3d335bfd5bbd0aa"},
		{"invalid hex", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{"empty", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exportAttachmentOutput := ""
			exportAttachmentJSON := false
			exportAttachmentBase64 := false

			err := runExportAttachment(newExportAttachmentTestCommand(t, exportAttachmentOutput, exportAttachmentJSON, exportAttachmentBase64), []string{tc.hash})
			require.Error(t, err, "expected error for invalid hash")
			assert.ErrorContains(t, err, "invalid content hash")
		})
	}
}
