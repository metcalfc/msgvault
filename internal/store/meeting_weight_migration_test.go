package store

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An archive whose cache was built under the old social weight must see a
// meeting weight revision change exactly once, so the derived refresh
// recomputes weights without rebuilding on every open.
func TestTwoPersonSocialMigrationBumpsMeetingWeightRevisionOnce(t *testing.T) {
	require := require.New(t)
	path := filepath.Join(t.TempDir(), "archive.db")
	st, err := Open(path)
	require.NoError(err)
	require.NoError(st.InitSchema())
	_, err = st.db.Exec(`DELETE FROM applied_migrations WHERE name = ?`, migrationMeetingWeightTwoPersonSocial)
	require.NoError(err)
	before, err := st.MeetingWeightRevisionContext(t.Context())
	require.NoError(err)
	require.NoError(st.Close())

	reopened, err := Open(path)
	require.NoError(err)
	require.NoError(reopened.InitSchema())
	after, err := reopened.MeetingWeightRevisionContext(t.Context())
	require.NoError(err)
	assert.Equal(t, before+1, after)
	require.NoError(reopened.Close())

	again, err := Open(path)
	require.NoError(err)
	t.Cleanup(func() { _ = again.Close() })
	require.NoError(again.InitSchema())
	unchanged, err := again.MeetingWeightRevisionContext(t.Context())
	require.NoError(err)
	assert.Equal(t, after, unchanged)
}
