package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleaseOwnedNoOpSyncExecutionLockPreservesOtherSource(t *testing.T) {
	requirements := require.New(t)
	st, err := OpenForTest(":memory:")
	requirements.NoError(err)
	t.Cleanup(func() { _ = st.Close() })

	firstLock, err := st.acquireBackendSyncExecutionLock(t.Context(), 1)
	requirements.NoError(err)
	secondLock, err := st.acquireBackendSyncExecutionLock(t.Context(), 2)
	requirements.NoError(err)

	st.syncExecutionLocks.bySource[1] = firstLock
	st.syncExecutionLocks.bySource[2] = secondLock
	st.registerSyncExecutionLock(1, 101, firstLock, false)
	st.registerSyncExecutionLock(2, 202, secondLock, false)

	requirements.NoError(st.releaseOwnedSyncExecutionLock(1, firstLock))

	st.syncExecutionLocks.mu.Lock()
	defer st.syncExecutionLocks.mu.Unlock()
	_, firstSourceExists := st.syncExecutionLocks.bySource[1]
	_, secondSourceExists := st.syncExecutionLocks.bySource[2]
	_, firstRunExists := st.syncExecutionLocks.byRun[101]
	_, secondRunExists := st.syncExecutionLocks.byRun[202]
	requirements.False(firstSourceExists)
	requirements.True(secondSourceExists)
	requirements.False(firstRunExists)
	requirements.True(secondRunExists)
}

func TestAcquireSyncExecutionCanceledDoesNotRetainOwnership(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	st, err := OpenForTest(":memory:")
	requirements.NoError(err)
	t.Cleanup(func() { _ = st.Close() })
	requirements.NoError(st.InitSchema())
	source, err := st.GetOrCreateSource("gmail", "sync-cancellation@example.com")
	requirements.NoError(err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	execution, err := st.AcquireSyncExecutionContext(ctx, source.ID)
	requirements.ErrorIs(err, context.Canceled)
	assertions.Nil(execution)
	execution, err = st.AcquireSyncExecutionContext(t.Context(), source.ID)
	requirements.NoError(err)
	requirements.NoError(execution.Release())
}
