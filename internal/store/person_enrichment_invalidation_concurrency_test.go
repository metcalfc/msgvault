package store

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/personenrichment"
)

// A committed enrichment result must not clear the identity work queued by a
// participant merge that follows it through the person gate.
func TestPersonEnrichmentResultAndInvalidationSharePersonFirstLockOrder(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := newEnrichmentResultFixture(t)
	prepared, err := f.store.preparePersonEnrichmentCommit(t.Context(), f.commit)
	require.NoError(err)
	participantID := enrichmentInvalidationParticipant(t, f.store, f.person.ID)
	survivorID, err := f.store.EnsureParticipant(
		"race-survivor@example.test", "Race Survivor", "example.test")
	require.NoError(err)
	resultBeforeLock := make(chan struct{}, 1)
	resultLocked := make(chan struct{}, 1)
	releaseResult := make(chan struct{})
	resumeResult := sync.OnceFunc(func() { close(releaseResult) })
	defer resumeResult()
	invalidationBeforeLock := make(chan struct{}, 1)
	invalidationLocked := make(chan struct{}, 1)
	SetPersonEnrichmentTxBarrierForTest(f.store, func(phase string) {
		switch phase {
		case "result_before_person_lock":
			resultBeforeLock <- struct{}{}
		case "result_person_locked":
			resultLocked <- struct{}{}
			select {
			case <-releaseResult:
			case <-t.Context().Done():
			}
		case "invalidation_before_person_lock":
			invalidationBeforeLock <- struct{}{}
		case "invalidation_person_locked":
			invalidationLocked <- struct{}{}
		}
	})
	type commitResult struct {
		outcome *personenrichment.ClaimOutcome
		err     error
	}
	resultDone := make(chan commitResult, 1)
	go func() {
		outcome, commitErr := f.store.commitPreparedPersonEnrichmentResult(t.Context(), prepared)
		resultDone <- commitResult{outcome: outcome, err: commitErr}
	}()
	requireReceiveEnrichmentBarrier(t, resultBeforeLock, "result did not reach person gate")
	select {
	case <-resultLocked:
	case result := <-resultDone:
		require.Fail("result bypassed person gate", "result: %+v", result)
	case <-time.After(5 * time.Second):
		require.Fail("result did not acquire person gate")
	}
	mergeDone := make(chan error, 1)
	mergeStarted := make(chan struct{})
	go func() {
		close(mergeStarted)
		mergeDone <- f.store.MergeParticipants(participantID, survivorID)
	}()
	requireReceiveEnrichmentBarrier(t, mergeStarted, "participant merge did not start")
	resumeResult()
	var result commitResult
	select {
	case result = <-resultDone:
	case <-time.After(5 * time.Second):
		require.FailNow("enrichment result did not finish after release")
	}
	require.NoError(result.err)
	require.NotNil(result.outcome)
	assert.Equal(personenrichment.ClaimApplied, result.outcome.Status)
	select {
	case err = <-mergeDone:
		require.NoError(err)
	case <-time.After(5 * time.Second):
		require.FailNow("participant merge did not finish after result release")
	}
	requireReceiveEnrichmentBarrier(t, invalidationBeforeLock, "invalidation did not reach person gate")
	requireReceiveEnrichmentBarrier(t, invalidationLocked, "invalidation never acquired released person gate")
	rows, err := f.store.ListPersonEnrichmentWorkContext(t.Context(), PersonEnrichmentWorkFilter{
		PersonID: f.person.ID, ProfileFingerprint: f.profile.Fingerprint, Limit: 10,
	})
	require.NoError(err)
	require.Len(rows, 1)
	assert.Equal(personEnrichmentTriggerBit(personenrichment.TriggerIdentity), rows[0].TriggerMask)
}
