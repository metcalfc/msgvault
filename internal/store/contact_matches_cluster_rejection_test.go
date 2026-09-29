package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

func TestRejectedClusterMemberSuppressesSiblingContactMatch(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	first := f.emailParticipant("uma@example.test", "Uma")
	second := f.emailParticipant("uma.work@example.test", "Uma")
	people := f.importCards(f.card("card-uma", "Uma Contact",
		[]string{"uma@example.test", "uma.work@example.test"}, nil))
	contactID := people["card-uma"]
	_, err := f.st.BuildContactMatchCandidatesContext(t.Context())
	require.NoError(err)
	candidates, err := f.st.ListContactMatchCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	require.Len(candidates, 2, "separate clusters give separate candidates")
	byParticipant := map[int64]store.IdentityMatchCandidate{}
	for _, candidate := range candidates {
		byParticipant[candidate.LeftID] = candidate
	}
	_, err = f.st.DecideIdentityMatchCandidateContext(t.Context(),
		byParticipant[first].ID, store.IdentityMatchStateRejected, "user", nil)
	require.NoError(err)

	// The two identities are linked afterwards: the sibling now proposes
	// binding a cluster that contains the rejected identity.
	_, err = f.st.LinkParticipants(first, second)
	require.NoError(err)
	_, _, err = f.st.AcceptIdentityMatchCandidateContext(t.Context(),
		byParticipant[second].ID, "user", nil)
	require.ErrorIs(err, store.ErrContactMatchRejectedInCluster)
	contact, err := f.st.GetPersonContext(t.Context(), contactID)
	require.NoError(err)
	assert.Empty(contact.ParticipantIDs)

	result, err := f.st.BuildContactMatchCandidatesContext(t.Context())
	require.NoError(err)
	assert.Equal(1, result.Retired)
	assert.Equal([]int64{byParticipant[first].ID}, contactMatchCandidateIDs(t, f.st),
		"only the rejected decision remains")
}
