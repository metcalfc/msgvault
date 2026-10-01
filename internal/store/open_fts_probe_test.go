package store

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenReportsFTSAvailableForInitializedSQLiteDB pins an invariant the
// read-only open paths already hold: a Store knows whether full-text search is
// available in the database it just opened. Until Open probes for it, a store
// opened against an already-initialized database reports FTS as unavailable
// until something calls InitSchema again, and every search it serves silently
// falls back to the slow path.
func TestOpenReportsFTSAvailableForInitializedSQLiteDB(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dbPath := filepath.Join(t.TempDir(), "test.db")

	first, err := Open(dbPath)
	require.NoError(err, "open store")
	require.NoError(first.InitSchema(), "init schema")
	require.True(first.FTS5Available(), "FTS is available once the schema is initialized")
	require.NoError(first.Close(), "close store")

	second, err := Open(dbPath)
	require.NoError(err, "reopen store")
	t.Cleanup(func() { _ = second.Close() })

	assert.True(second.FTS5Available(), "reopening an initialized database reports FTS as available")
}
