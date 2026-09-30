package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestFinalizeSyncFailure(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("granola", "sync-failure@example.test")
	require.NoError(err)
	runID, err := st.StartSync(source.ID, "incremental")
	require.NoError(err)
	require.NoError(st.FinalizeSyncFailure(runID, nil, nil))
	active, err := st.GetActiveSyncReadOnly(t.Context(), source.ID)
	require.NoError(err)
	require.NotNil(active, "successful work must not be marked failed")

	checkpoint := &store.Checkpoint{MessagesProcessed: 5, MessagesAdded: 3, MessagesUpdated: 1, ErrorsCount: 1, PageToken: "covered-page"}
	cause := context.Canceled
	require.ErrorIs(st.FinalizeSyncFailure(runID, cause, checkpoint), cause)
	run, err := st.GetLatestSync(source.ID)
	require.NoError(err)
	require.NotNil(run)
	assert.Equal("failed", run.Status)
	assert.Equal(checkpoint.MessagesProcessed, run.MessagesProcessed)
	assert.Equal(checkpoint.MessagesAdded, run.MessagesAdded)
	assert.Equal(checkpoint.MessagesUpdated, run.MessagesUpdated)
	assert.Equal(checkpoint.ErrorsCount, run.ErrorsCount)
	assert.Equal(checkpoint.PageToken, run.CursorBefore.String)
	assert.Equal(cause.Error(), run.ErrorMessage.String)
	active, err = st.GetActiveSyncReadOnly(t.Context(), source.ID)
	require.ErrorIs(err, store.ErrSyncRunNotFound)
	assert.Nil(active)
}

func TestFinalizeSyncFailurePreservesBothErrors(t *testing.T) {
	require := require.New(t)
	testutil.SkipIfPostgres(t, "uses a SQLite trigger to reject the terminal sync write")
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("granola", "failed-finalization@example.test")
	require.NoError(err)
	runID, err := st.StartSync(source.ID, "incremental")
	require.NoError(err)
	_, err = st.DB().Exec(`CREATE TRIGGER reject_failed_sync BEFORE UPDATE ON sync_runs
 WHEN NEW.status = 'failed' BEGIN SELECT RAISE(ABORT, 'synthetic terminal write failure'); END`)
	require.NoError(err)
	cause := errors.New("synthetic provider failure")
	got := st.FinalizeSyncFailure(runID, cause, &store.Checkpoint{MessagesProcessed: 2})
	require.ErrorIs(got, cause)
	require.ErrorContains(got, "record failed sync")
	require.ErrorContains(got, "synthetic terminal write failure")
}
