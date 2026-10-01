package testutil

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

// NewTestStore creates a temporary SQLite database and cleans it up after the test.
func NewTestStore(t *testing.T) *store.Store { t.Helper(); return NewSQLiteTestStore(t) }

// NewSQLiteTestStore clones an initialized SQLite template into the test directory.
func NewSQLiteTestStore(t *testing.T) *store.Store {
	t.Helper()

	template, err := sqliteTemplateBytes()
	require.NoError(t, err, "build sqlite template")

	dbPath := filepath.Join(t.TempDir(), "test.db")
	require.NoError(t, os.WriteFile(dbPath, template, 0o600), "clone sqlite template")

	st, err := store.OpenForTest(dbPath)
	require.NoError(t, err, "open store")

	t.Cleanup(func() {
		_ = st.Close()
	})
	assignFreshArchiveUID(t, st)

	return st
}

// The key is the store's own ("archive_uid", see store/archive_identity.go),
// written here because a store never reassigns an identity in production.
func assignFreshArchiveUID(t *testing.T, st *store.Store) {
	t.Helper()

	random := make([]byte, 32)
	_, err := rand.Read(random)
	require.NoError(t, err, "random archive UID")

	statement := "UPDATE archive_metadata SET value = ? WHERE key = 'archive_uid'"

	result, err := st.DB().Exec(statement, hex.EncodeToString(random))
	require.NoError(t, err, "assign archive UID")
	updated, err := result.RowsAffected()
	require.NoError(t, err, "assigned archive UID rows")
	require.Equal(t, int64(1), updated, "the template carried exactly one archive UID to replace")
}
