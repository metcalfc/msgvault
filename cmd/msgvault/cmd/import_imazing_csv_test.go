package cmd

import (
	"bytes"
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

func TestImportIMazingCSVRequiresMe(t *testing.T) {
	command := &cobra.Command{Use: "test"}
	command.AddCommand(newImportIMazingCSVCmd())
	command.SetArgs([]string{"import-imazing-csv", t.TempDir()})
	err := command.Execute()
	require.Error(t, err)
	assert.ErrorContains(t, err, "required flag")
}

// The export directory and --contacts file live on the caller's machine, so a
// configured remote daemon cannot read them. The command must refuse before
// proxying instead of sending client-side paths for the daemon host to fail on.
func TestImportIMazingCSVRejectsConfiguredRemoteDaemon(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	server, requests := newDaemonCLIRunnerTestServer(t, nil, `{"type":"complete"}`)
	testCtx := configureRemoteDaemonForTest(t, server.URL)
	t.Setenv(daemonCLISubprocessEnv, "")

	command := &cobra.Command{Use: "test"}
	command.SetContext(testCtx)
	command.AddCommand(newImportIMazingCSVCmd())
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{
		"import-imazing-csv", t.TempDir(), "--me", "+15550000001", "--timezone", "UTC",
	})

	err := command.Execute()

	require.ErrorContains(err, "run it on the daemon host with --local")
	assert.Equal(0, int(requests.Load()), "runner endpoint calls")
}

func TestImportIMazingCSVReportsSummary(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	markDaemonCLISubprocessForTest(t)
	home := t.TempDir()
	cfg := config.NewDefaultConfig()
	testCtx := testInvocationContext(t.Context(), cfg, invocationOptions{})
	cfg.HomeDir = home
	cfg.Data.DataDir = home
	exportDir := writeIMazingCSVCommandFixture(t)
	command := &cobra.Command{Use: "test"}
	command.SetContext(testCtx)
	command.AddCommand(newImportIMazingCSVCmd())
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{
		"import-imazing-csv", exportDir, "--me", "+15550000001", "--timezone", "UTC",
	})

	require.NoError(command.Execute())
	assert.Contains(output.String(), "Files:               1")
	assert.Contains(output.String(), "Messages:            1")
	assert.Contains(output.String(), "Attachments stored:  0")
}

// TestImportIMazingCSVRebuildsCacheAfterPartialImportFailure catches the
// failure mode where messages commit one by one but the import later fails
// (here: an unreadable --contacts file). The committed rows must still reach
// the analytics cache, not stay invisible until the next maintenance build,
// even though the command returns an error.
func TestImportIMazingCSVRebuildsCacheAfterPartialImportFailure(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	markDaemonCLISubprocessForTest(t)
	dataDir := t.TempDir()
	cfg := config.NewDefaultConfig()
	testCtx := testInvocationContext(t.Context(), cfg, invocationOptions{})
	cfg.HomeDir = dataDir
	cfg.Data.DataDir = dataDir

	exportDir := writeIMazingCSVCommandFixture(t)
	command := &cobra.Command{Use: "test"}
	command.SetContext(testCtx)
	command.AddCommand(newImportIMazingCSVCmd())
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{
		"import-imazing-csv", exportDir, "--me", "+15550000001", "--timezone", "UTC",
		"--contacts", filepath.Join(dataDir, "missing.vcf"),
	})
	err := command.Execute()
	require.Error(err)
	require.ErrorContains(err, "import iMazing CSV")
	require.ErrorContains(err, "parse iMazing contacts")

	st, err := store.OpenForTest(cfg.DatabaseDSN())
	require.NoError(err)
	var messages int
	require.NoError(st.DB().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messages))
	revision, err := st.DerivedDataRevision()
	require.NoError(err)
	require.NoError(st.Close())
	assert.Equal(1, messages, "the message committed before the import error must be stored")

	cacheState, err := query.ReadCacheSyncState(cfg.AnalyticsDir())
	require.NoError(err, "the analytics cache must exist after a partially failed import")
	assert.Equal(revision, cacheState.DerivedDataRevision,
		"the analytics cache must catch up to the partial import's committed revision")
	staleness := cacheNeedsBuild(cfg.DatabaseDSN(), cfg.AnalyticsDir())
	assert.False(staleness.NeedsBuild, "a failed import must not leave the analytics cache stale")
}

// --timezone Local names the host's own zone instead of a concrete IANA zone,
// so offset-free timestamps would decode differently on every host while the
// opaque name is persisted with the source. The command must fail before the
// importer creates a source, starts a sync, or persists a message.
func TestImportIMazingCSVRejectsLocalTimezone(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	markDaemonCLISubprocessForTest(t)
	dataDir := t.TempDir()
	cfg := config.NewDefaultConfig()
	testCtx := testInvocationContext(t.Context(), cfg, invocationOptions{})
	cfg.HomeDir = dataDir
	cfg.Data.DataDir = dataDir

	exportDir := writeIMazingCSVCommandFixture(t)
	command := &cobra.Command{Use: "test"}
	command.SetContext(testCtx)
	command.AddCommand(newImportIMazingCSVCmd())
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{
		"import-imazing-csv", exportDir, "--me", "+15550000001", "--timezone", "Local",
	})

	err := command.Execute()

	require.ErrorContains(err, "host-dependent Local")
	st, err := store.OpenForTest(cfg.DatabaseDSN())
	require.NoError(err)
	var sources, syncs, messages int
	require.NoError(st.DB().QueryRow(
		`SELECT COUNT(*) FROM sources WHERE source_type = 'imazing_csv'`).Scan(&sources))
	require.NoError(st.DB().QueryRow(`SELECT COUNT(*) FROM sync_runs`).Scan(&syncs))
	require.NoError(st.DB().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messages))
	require.NoError(st.Close())
	assert.Zero(sources, "no iMazing CSV source may be created for --timezone Local")
	assert.Zero(syncs, "no sync may be started for --timezone Local")
	assert.Zero(messages, "no message may be persisted for --timezone Local")
}

func TestImportIMazingCSVReportsLocalTimezoneResolutionFailure(t *testing.T) {
	previous := resolveLocalTimezone
	resolveLocalTimezone = func() (string, error) { return "", errors.New("unknown local zone") }
	t.Cleanup(func() { resolveLocalTimezone = previous })
	command := &cobra.Command{Use: "test"}
	command.AddCommand(newImportIMazingCSVCmd())
	command.SetArgs([]string{"import-imazing-csv", t.TempDir(), "--me", "+15550000001"})
	err := command.Execute()
	require.Error(t, err)
	assert.ErrorContains(t, err, "--timezone")
}

// A concrete TZ makes local IANA timezone resolution deterministic.
func TestResolveLocalTimezonePlatformContract(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("TZ", "Europe/Berlin")

	zone, err := ResolveLocalTimezone()

	require.NoError(err)
	assert.Equal("Europe/Berlin", zone)
}

// Omitted --timezone resolves the local IANA zone before importing.
func TestImportIMazingCSVOmittedTimezonePlatformContract(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	markDaemonCLISubprocessForTest(t)
	dataDir := t.TempDir()
	cfg := config.NewDefaultConfig()
	testCtx := testInvocationContext(t.Context(), cfg, invocationOptions{})
	cfg.HomeDir = dataDir
	cfg.Data.DataDir = dataDir

	exportDir := writeIMazingCSVCommandFixture(t)
	command := &cobra.Command{Use: "test"}
	command.SetContext(testCtx)
	command.AddCommand(newImportIMazingCSVCmd())
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"import-imazing-csv", exportDir, "--me", "+15550000001"})

	t.Setenv("TZ", "Europe/Berlin")
	require.NoError(command.Execute())
	assert.Contains(output.String(), "Import complete")
	st, err := store.OpenForTest(cfg.DatabaseDSN())
	require.NoError(err)
	source, err := st.GetSourceByTypeAndIdentifier("imazing_csv", "+15550000001")
	require.NoError(err)
	require.True(source.SyncConfig.Valid)
	assert.JSONEq(`{"ambiguous_time_policy":"earlier","timezone":"Europe/Berlin"}`, source.SyncConfig.String)
	var messages int
	require.NoError(st.DB().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messages))
	require.NoError(st.Close())
	assert.Equal(1, messages, "the import runs with the resolved zone")
}

func writeIMazingCSVCommandFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "csv"), 0o700))
	file, err := os.Create(filepath.Join(root, "csv", "messages.csv"))
	require.NoError(t, err)
	writer := csv.NewWriter(file)
	require.NoError(t, writer.Write([]string{
		"Chat Session", "Message Date", "Delivered Date", "Read Date", "Service", "Type",
		"Sender ID", "Sender Name", "Status", "Replying to", "Subject", "Text", "Attachment", "Attachment type",
	}))
	require.NoError(t, writer.Write([]string{
		"Alice", "2024-06-01 12:00:00", "", "", "iMessage", "Outgoing",
		"", "", "", "", "", "hello", "", "",
	}))
	writer.Flush()
	require.NoError(t, writer.Error())
	require.NoError(t, file.Close())
	return root
}
