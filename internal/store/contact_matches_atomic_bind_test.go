package store_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

func contactMatchCandidateIDs(t *testing.T, st *store.Store) []int64 {
	t.Helper()
	candidates, err := st.ListContactMatchCandidatesContext(t.Context(), nil, 500, 0)
	require.NoError(t, err)
	ids := make([]int64, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.ID)
	}
	return ids
}

func personCount(t *testing.T, st *store.Store) int {
	t.Helper()
	var count int
	require.NoError(t, st.DB().QueryRow(`SELECT COUNT(*) FROM persons`).Scan(&count))
	return count
}

func TestContactMatchBindIsAllOrNothingWhenCancelledMidway(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	participant := f.phoneParticipant("+15550100133", "Rae")
	people := f.importCards(f.card("card-rae", "Rae Contact", nil, []string{"+1 555 010 0133"}))
	candidate := f.buildCandidate(people["card-rae"])
	before := personCount(t, f.st)

	ctx, cancel := context.WithCancel(t.Context())
	restore := f.st.SetContactMatchBindAfterPromoteHookForTest(cancel)
	_, _, err := f.st.AcceptIdentityMatchCandidateContext(ctx, candidate.ID, "user", nil)
	restore()
	require.ErrorIs(err, context.Canceled)

	unchanged, err := f.st.GetIdentityMatchCandidateContext(t.Context(), candidate.ID)
	require.NoError(err)
	assert.Equal(store.IdentityMatchStateCandidate, unchanged.State)
	assert.Equal(before, personCount(t, f.st), "no orphan promoted person is left behind")
	contact, err := f.st.GetPersonContext(t.Context(), people["card-rae"])
	require.NoError(err)
	assert.Empty(contact.ParticipantIDs)

	accepted, _, err := f.st.AcceptIdentityMatchCandidateContext(t.Context(), candidate.ID, "user", nil)
	require.NoError(err, "a later accept completes the bind")
	assert.Equal(store.IdentityMatchStateAccepted, accepted.State)
	contact, err = f.st.GetPersonContext(t.Context(), people["card-rae"])
	require.NoError(err)
	assert.Equal([]int64{participant}, contact.ParticipantIDs)
}
