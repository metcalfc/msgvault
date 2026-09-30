package store

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type labelEmojiRevisions struct {
	participantNames, personNames, derivedData int64
}

func readLabelEmojiRevisions(t *testing.T, st *Store) labelEmojiRevisions {
	t.Helper()
	participantNames, err := st.ParticipantDisplayNameRevisionContext(t.Context())
	require.NoError(t, err)
	personNames, err := st.PersonDisplayNameRevisionContext(t.Context())
	require.NoError(t, err)
	derivedData, err := st.DerivedDataRevisionContext(t.Context())
	require.NoError(t, err)
	return labelEmojiRevisions{participantNames, personNames, derivedData}
}

// Labels stored before ingest removed emoji are cleaned once on upgrade. A
// value the user typed is kept, and the cleanup is not dated as a rename.
func TestStripLabelEmojiMigrationCleansDerivedLabelsOnce(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	path := filepath.Join(t.TempDir(), "archive.db")
	st, err := Open(path)
	require.NoError(err)
	require.NoError(st.InitSchema())
	for _, statement := range []string{
		`INSERT INTO sources (id, source_type, identifier) VALUES (1, 'gmail', 'owner@example.com')`,
		`INSERT INTO conversations (id, source_id, conversation_type) VALUES (1, 1, 'email_thread')`,
		`INSERT INTO messages (id, conversation_id, source_id, message_type, source_message_id)
			VALUES (1, 1, 1, 'email', 'm1')`,
		`INSERT INTO participants (id, email_address, display_name) VALUES
			(1, 'ana@example.com', '🎉 Ana Example'),
			(2, 'bea@example.com', 'Bea Example'),
			(3, 'cam@example.com', 'Cam Example')`,
		`INSERT INTO participants (id, display_name) VALUES (4, '🦄')`,
		`INSERT INTO message_recipients (message_id, participant_id, recipient_type, display_name)
			VALUES (1, 1, 'from', '🎉 Ana Example'), (1, 2, 'to', 'Bea Example')`,
		`INSERT INTO persons (id, vcard_uid, display_name, display_name_changed_at) VALUES
			(1, 'uid-ana', '🎉 Ana Example', '2020-01-02 03:04:05'),
			(2, 'uid-bea', 'Bea ✨ Example', '2020-01-02 03:04:05')`,
		`INSERT INTO person_participants (person_id, participant_id) VALUES (1, 1), (2, 2)`,
		`INSERT INTO person_names (person_id, name_kind, formatted, original_value, source) VALUES
			(1, 'formatted', '🎉 Ana Example', '🎉 Ana Example', 'carddav_import'),
			(2, 'formatted', 'Bea ✨ Example', 'Bea ✨ Example', 'user')`,
	} {
		_, err := st.db.Exec(statement)
		require.NoError(err, statement)
	}
	_, err = st.db.Exec(`DELETE FROM applied_migrations WHERE name = ?`, migrationStripLabelEmoji)
	require.NoError(err)
	before := readLabelEmojiRevisions(t, st)
	var personRevision int64
	require.NoError(st.db.QueryRow(`SELECT revision FROM persons WHERE id = 1`).Scan(&personRevision))
	require.NoError(st.Close())

	reopened, err := Open(path)
	require.NoError(err)
	require.NoError(reopened.InitSchema())
	text := func(query string) string {
		var value string
		require.NoError(reopened.db.QueryRow(query).Scan(&value), query)
		return value
	}
	assert.Equal("Ana Example", text(`SELECT display_name FROM participants WHERE id = 1`))
	assert.Equal("🦄", text(`SELECT display_name FROM participants WHERE id = 4`),
		"an identifier-only participant keeps its only label")
	assert.Equal("Ana Example", text(`SELECT display_name FROM message_recipients WHERE participant_id = 1`))
	assert.Equal("Ana Example", text(`SELECT display_name FROM persons WHERE id = 1`))
	assert.Equal("Bea ✨ Example", text(`SELECT display_name FROM persons WHERE id = 2`),
		"a display name with no derived source is the user's rename")
	assert.Equal("Ana Example", text(`SELECT formatted FROM person_names WHERE person_id = 1`))
	assert.Equal("🎉 Ana Example", text(`SELECT original_value FROM person_names WHERE person_id = 1`))
	assert.Equal("Bea ✨ Example", text(`SELECT formatted FROM person_names WHERE person_id = 2`))
	assert.Contains(text(`SELECT display_name_changed_at FROM persons WHERE id = 1`), "2020-01-02")
	var revisionAfter int64
	require.NoError(reopened.db.QueryRow(`SELECT revision FROM persons WHERE id = 1`).Scan(&revisionAfter))
	assert.Greater(revisionAfter, personRevision)

	after := readLabelEmojiRevisions(t, reopened)
	assert.Equal(before.participantNames+1, after.participantNames)
	assert.Equal(before.personNames+1, after.personNames)
	assert.Equal(before.derivedData+1, after.derivedData)
	require.NoError(reopened.Close())

	again, err := Open(path)
	require.NoError(err)
	t.Cleanup(func() { _ = again.Close() })
	require.NoError(again.InitSchema())
	assert.Equal(after, readLabelEmojiRevisions(t, again))
}
