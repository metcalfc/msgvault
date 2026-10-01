//go:build fts5

package beeper

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestImportIndexesFTS verifies imported messages (including voice-note
// transcriptions) land in the SQLite FTS5 index. Gated on the fts5 build tag.
func TestImportIndexesFTS(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	f := newFakeBeeper(t)
	f.addChat(e2eChat())
	imp, st, done := newTestImporter(t, f)
	defer done()

	_, err := imp.Import(context.Background(), ImportOptions{AccountID: "signal"})
	require.NoError(err)
	require.True(st.FTS5Available(), "fts5 build tag set but FTS5 unavailable")

	var ftsHits int
	require.NoError(st.DB().QueryRow(
		`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'tomorrow'`).Scan(&ftsHits))
	assert.Equal(1, ftsHits, "voice transcription must be FTS-indexed")

	require.NoError(st.DB().QueryRow(
		`SELECT COUNT(*) FROM messages_fts`).Scan(&ftsHits))
	assert.Equal(43, ftsHits, "every persisted message must be FTS-indexed")
}
