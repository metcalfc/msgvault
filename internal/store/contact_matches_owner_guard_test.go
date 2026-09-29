package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

func TestAcceptContactMatchRefusesAClusterThatBecameTheOwner(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)
	source, err := f.st.GetOrCreateSource("gmail", "owner@example.test")
	require.NoError(err)

	participant := f.emailParticipant("pat@example.test", "Pat")
	people := f.importCards(f.card("card-pat", "Pat Contact", []string{"pat@example.test"}, nil))
	candidate := f.buildCandidate(people["card-pat"])
	before := personCount(t, f.st)

	// The address is confirmed as one of the owner's identities after the
	// candidate was built.
	require.NoError(f.st.AddAccountIdentityContext(t.Context(), source.ID, "pat@example.test", "manual"))
	_, _, err = f.st.AcceptIdentityMatchCandidateContext(t.Context(), candidate.ID, "user", nil)
	require.ErrorIs(err, store.ErrContactMatchOwnerIdentity)
	assert.Equal(before, personCount(t, f.st), "nothing is promoted")
	contact, err := f.st.GetPersonContext(t.Context(), people["card-pat"])
	require.NoError(err)
	assert.Empty(contact.ParticipantIDs)

	result, err := f.st.BuildContactMatchCandidatesContext(t.Context())
	require.NoError(err)
	assert.Equal(1, result.Retired)
	assert.Empty(contactMatchCandidateIDs(t, f.st), "rebuild retires the owner cluster's candidate")
	_ = participant
}

func TestAcceptContactMatchRefusesAClusterLinkedToAnOwnerPhone(t *testing.T) {
	require := require.New(t)
	f := newContactMatchFixture(t)
	source, err := f.st.GetOrCreateSource("gmail", "owner@example.test")
	require.NoError(err)

	participant := f.emailParticipant("quinn@example.test", "Quinn")
	people := f.importCards(f.card("card-quinn", "Quinn Contact", []string{"quinn@example.test"}, nil))
	candidate := f.buildCandidate(people["card-quinn"])

	ownerPhone, err := f.st.EnsureParticipantByPhone("+1 (555) 010-0177", "Owner Phone", "whatsapp")
	require.NoError(err)
	require.NoError(f.st.AddAccountIdentityContext(t.Context(), source.ID, "+15550100177", "manual"))
	_, err = f.st.LinkParticipants(participant, ownerPhone)
	require.NoError(err)

	_, _, err = f.st.AcceptIdentityMatchCandidateContext(t.Context(), candidate.ID, "user", nil)
	require.ErrorIs(err, store.ErrContactMatchOwnerIdentity)
}
