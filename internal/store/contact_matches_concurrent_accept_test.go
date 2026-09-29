package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

func TestConcurrentContactMatchAcceptsBindOnceAndStayAccepted(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	participant := f.emailParticipant("sol@example.test", "Sol")
	people := f.importCards(f.card("card-sol", "Sol Contact", []string{"sol@example.test"}, nil))
	candidate := f.buildCandidate(people["card-sol"])

	before := personCount(t, f.st)

	// One of the concurrent accepts fails partway: whichever request
	// reaches the bind first cancels it. A failed accept must never revert
	// the decision another request committed.
	failing, cancel := context.WithCancel(t.Context())
	defer cancel()
	restore := f.st.SetContactMatchBindAfterPromoteHookForTest(cancel)
	defer restore()
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		ctx := t.Context()
		if i == 0 {
			ctx = failing
		}
		wg.Go(func() {
			_, _, errs[i] = f.st.AcceptIdentityMatchCandidateContext(ctx, candidate.ID, "user", nil)
		})
	}
	wg.Wait()
	succeeded := 0
	for _, err := range errs[1:] {
		if err == nil {
			succeeded++
		}
	}
	assert.Equal(3, succeeded, "every uncancelled accept succeeds: %v", errs)
	assert.Equal(before, personCount(t, f.st), "no orphan promoted person")

	final, err := f.st.GetIdentityMatchCandidateContext(t.Context(), candidate.ID)
	require.NoError(err)
	assert.Equal(store.IdentityMatchStateAccepted, final.State, "a failed accept never reverts a successful one")
	merges, err := f.st.ListPersonMergesContext(t.Context(), people["card-sol"])
	require.NoError(err)
	assert.Len(merges, 1)
	contact, err := f.st.GetPersonContext(t.Context(), people["card-sol"])
	require.NoError(err)
	assert.Equal([]int64{participant}, contact.ParticipantIDs)
}
