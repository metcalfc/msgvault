package sqliteutil_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/sqliteutil"
	"go.kenn.io/msgvault/internal/store"
)

func TestServerDSNsAreRejectedBeforeFilesystemAccess(t *testing.T) {
	t.Chdir(t.TempDir())
	openers := map[string]func(string) (*store.Store, error){
		"writable": store.Open,
		"test":     store.OpenForTest,
		"readonly": store.OpenReadOnly,
		"context":  func(dsn string) (*store.Store, error) { return store.OpenContext(context.Background(), dsn) },
	}
	for _, dsn := range []string{
		"postgres://alice:secret@example.test/archive",
		"postgresql://alice:secret@example.test/archive",
		" POSTGRESQL://alice:secret@example.test/archive ",
		"postgres:archive",
		"host=example.test dbname=archive password=secret",
		"host = example.test user = alice password = secret",
		"connect_timeout=5",
		"application_name=msgvault search_path=archive",
		"mysql://alice:secret@example.test/archive",
	} {
		for name, open := range openers {
			t.Run(name+"/"+dsn, func(t *testing.T) {
				requirements := require.New(t)
				assertions := assert.New(t)
				st, err := open(dsn)
				requirements.Error(err)
				assertions.Nil(st)
				assertions.Contains(err.Error(), "SQLite")
				assertions.NotContains(err.Error(), "secret")
				entries, readErr := os.ReadDir(".")
				requirements.NoError(readErr)
				assertions.Empty(entries)
			})
		}
	}
}

func TestSQLiteDSNsRemainAccepted(t *testing.T) {
	for _, dsn := range []string{":memory:", "archive.db", "dir/archive.db", "file:archive.db?mode=ro", "file::memory:?cache=shared", `C:\archive\messages.db`, "file:host=notes.db"} {
		t.Run(dsn, func(t *testing.T) {
			require.NoError(t, sqliteutil.ValidateDSN(dsn))
		})
	}
	st, err := store.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	require.NoError(t, st.InitSchema())
}
