// Package sqlitetest provides production-initialized SQLite fixtures without query dependencies.
package sqlitetest

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

// New clones an initialized SQLite template into the test directory.
func New(tb testing.TB) *store.Store {
	tb.Helper()

	template, err := sqliteTemplateBytes()
	require.NoError(tb, err, "build sqlite template")

	dbPath := filepath.Join(tb.TempDir(), "test.db")
	require.NoError(tb, os.WriteFile(dbPath, template, 0o600), "clone sqlite template")

	st, err := store.OpenForTest(dbPath)
	require.NoError(tb, err, "open store")

	tb.Cleanup(func() {
		_ = st.Close()
	})
	assignFreshArchiveUID(tb, st)

	return st
}

// The key is the store's own ("archive_uid", see store/archive_identity.go),
// written here because a store never reassigns an identity in production.
func assignFreshArchiveUID(tb testing.TB, st *store.Store) {
	tb.Helper()

	random := make([]byte, 32)
	_, err := rand.Read(random)
	require.NoError(tb, err, "random archive UID")

	statement := "UPDATE archive_metadata SET value = ? WHERE key = 'archive_uid'"

	result, err := st.DB().Exec(statement, hex.EncodeToString(random))
	require.NoError(tb, err, "assign archive UID")
	updated, err := result.RowsAffected()
	require.NoError(tb, err, "assigned archive UID rows")
	require.Equal(tb, int64(1), updated, "the template carried exactly one archive UID to replace")
}
