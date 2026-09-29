package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

func TestContactMatchWhoseAddressChangedIsRetiredAndRefused(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	f.emailParticipant("tam@example.test", "Tam")
	card := f.card("card-tam", "Tam Contact", []string{"tam@example.test"}, nil)
	people := f.importCards(card)
	candidate := f.buildCandidate(people["card-tam"])

	// The address is corrected remotely; the card no longer matches.
	corrected := f.card("card-tam", "Tam Contact", []string{"tam.new@example.test"}, nil)
	corrected.RemoteETag = `"card-tam-2"`
	corrected.SemanticHash = "semantic-card-tam-2"
	f.importCards(corrected)

	_, _, err := f.st.AcceptIdentityMatchCandidateContext(t.Context(), candidate.ID, "user", nil)
	require.ErrorIs(err, store.ErrContactMatchStale)
	contact, err := f.st.GetPersonContext(t.Context(), people["card-tam"])
	require.NoError(err)
	assert.Empty(contact.ParticipantIDs)

	result, err := f.st.BuildContactMatchCandidatesContext(t.Context())
	require.NoError(err)
	assert.Equal(1, result.Retired)
	assert.Empty(contactMatchCandidateIDs(t, f.st))
}
