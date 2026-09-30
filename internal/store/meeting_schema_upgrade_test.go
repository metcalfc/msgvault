package store_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

func meetingAssigneeColumns(t *testing.T, st *store.Store) map[string]bool {
	t.Helper()
	rows, err := st.DB().QueryContext(t.Context(), `SELECT name FROM pragma_table_info('meeting_action_assignees')`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	columns := map[string]bool{}
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		columns[name] = true
	}
	require.NoError(t, rows.Err())
	return columns
}

// An archive from before this branch has neither meeting table; one from an
// earlier build of it has meeting_action_assignees without the fingerprint
// and revision columns. Opening either yields the current shape.
func TestMeetingTablesUpgradeOlderArchives(t *testing.T) {
	tests := []struct {
		name  string
		setup string
	}{
		{"archive without the meeting tables", `
			DROP TABLE meeting_action_assignees;
			DROP TABLE calendar_event_kinds;`},
		{"archive with the first assignee table", `
			DROP TABLE meeting_action_assignees;
			CREATE TABLE meeting_action_assignees (
				message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
				ordinal INTEGER NOT NULL,
				action_title TEXT NOT NULL,
				choice TEXT NOT NULL,
				assignee_participant_id INTEGER REFERENCES participants(id) ON DELETE SET NULL,
				confidence REAL NOT NULL,
				probabilities_json TEXT NOT NULL DEFAULT '{}',
				provenance TEXT NOT NULL,
				model TEXT NOT NULL DEFAULT '',
				judged_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (message_id, ordinal));`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			path := filepath.Join(t.TempDir(), "archive.db")
			st, err := store.Open(path)
			require.NoError(err)
			require.NoError(st.InitSchema())
			_, err = st.DB().ExecContext(t.Context(), test.setup)
			require.NoError(err)
			require.NoError(st.Close())

			st, err = store.Open(path)
			require.NoError(err)
			t.Cleanup(func() { _ = st.Close() })
			require.NoError(st.InitSchema())
			columns := meetingAssigneeColumns(t, st)
			assert.True(columns["input_fingerprint"])
			assert.True(columns["meeting_revision"])
			_, _, err = st.MeetingActionAssigneeCandidatesContext(t.Context(), 0)
			require.NoError(err)
			_, err = st.CalendarEventKindCandidatesContext(t.Context(), 0)
			require.NoError(err)
		})
	}
}
