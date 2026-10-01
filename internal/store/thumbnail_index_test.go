package store_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestInitSchema_SQLiteCreatesThumbnailPathIndex(t *testing.T) {
	require := require.New(t)

	st := testutil.NewTestStore(t)
	var n int
	require.NoError(st.DB().QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_attachments_thumbnail_path'`).Scan(&n))
	require.Equal(1, n, "thumbnail_path lookup must use a real schema index")
}

func TestInitSchema_SQLiteCreatesAttachmentHashExpressionIndexes(t *testing.T) {
	require := require.New(t)

	st := testutil.NewTestStore(t)
	names := []string{
		"idx_attachments_content_hash_lower",
		"idx_attachments_thumbnail_hash_lower",
	}
	probe := func(name string) int {
		var n int
		require.NoError(st.DB().QueryRow(`
			SELECT COUNT(*) FROM sqlite_master
			WHERE type = 'index' AND name = ?`, name).Scan(&n))
		return n
	}
	for _, name := range names {
		require.Equal(1, probe(name), "%s must exist after fresh InitSchema", name)
	}

	for _, name := range names {
		_, err := st.DB().Exec(`DROP INDEX ` + name)
		require.NoError(err, "drop %s", name)
	}
	require.NoError(st.InitSchema(), "InitSchema must add expression indexes to upgraded databases")
	for _, name := range names {
		require.Equal(1, probe(name), "%s must exist after upgraded InitSchema", name)
	}
}
