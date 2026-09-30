package store_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/store"
)

// sendAs records one message from participant under each display name.
func (f *contactMatchFixture) sendAs(participant int64, names ...string) {
	f.t.Helper()
	source, err := f.st.GetOrCreateSource("gmail", "archive@example.test")
	require.NoError(f.t, err)
	conversation, err := f.st.EnsureConversation(source.ID, "shared-thread", "Shared thread")
	require.NoError(f.t, err)
	for i, name := range names {
		id, err := f.st.UpsertMessage(&store.Message{
			ConversationID: conversation, SourceID: source.ID, MessageType: "email", SizeEstimate: 100,
			SourceMessageID: fmt.Sprintf("shared-%d-%d", participant, i),
		})
		require.NoError(f.t, err)
		require.NoError(f.t, f.st.ReplaceMessageRecipients(id, "from", []int64{participant}, []string{name}))
	}
}

func TestContactMatchesFlagASharedMailboxInsteadOfBinding(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	desk := f.emailParticipant("desk@example.test", "Avery Stone")
	f.sendAs(desk, "Avery Stone", "Blake Rivera", "avery stone via Example Desk")
	people := f.importCards(f.card("card-avery", "Avery Stone", []string{"desk@example.test"}, nil))

	matches, err := f.st.FindContactMatchesContext(t.Context())
	require.NoError(err)
	require.Len(matches, 1)
	assert.Equal(store.ContactMatchSharedMailbox, matches[0].Classification)
	require.NotNil(matches[0].SharedMailbox)
	assert.Equal([]correspondentkind.SharedMailboxReason{correspondentkind.ReasonSeveralNames},
		matches[0].SharedMailbox.Reasons)
	assert.Equal([]string{"Avery Stone", "Blake Rivera"}, matches[0].SharedMailbox.Names)

	candidate := f.buildCandidate(people["card-avery"])
	_, statuses, err := f.st.DescribeIdentityMatchCandidatesContext(t.Context(),
		[]store.IdentityMatchCandidate{candidate})
	require.NoError(err)
	require.Len(statuses, 1)
	assert.Equal(store.ContactMatchSharedMailbox, statuses[0].Classification)
	require.NotNil(statuses[0].SharedMailbox)
	assert.Equal("desk@example.test", statuses[0].SharedMailbox.Address)

	before := personCount(t, f.st)
	_, _, err = f.st.AcceptIdentityMatchCandidateContext(t.Context(), candidate.ID, "user", nil)
	require.ErrorIs(err, store.ErrContactMatchSharedMailbox)
	assert.Equal(before, personCount(t, f.st), "nothing is bound through a shared mailbox")

	// "This is a person" overrides the signal, and the match binds.
	_, err = f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: desk, Kind: correspondentkind.Person,
	})
	require.NoError(err)
	_, statuses, err = f.st.DescribeIdentityMatchCandidatesContext(t.Context(),
		[]store.IdentityMatchCandidate{candidate})
	require.NoError(err)
	assert.Equal(store.ContactMatchBind, statuses[0].Classification)
	_, _, err = f.st.AcceptIdentityMatchCandidateContext(t.Context(), candidate.ID, "user", nil)
	require.NoError(err)
}

func TestContactMatchesFlagRoleAddressesAndCompetingProfiles(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	f.emailParticipant("billing@shop.example.test", "Example Shop")
	f.importCards(f.card("card-shop", "Shop Billing", []string{"billing@shop.example.test"}, nil))

	// Two saved profiles that both list one address, the way two support
	// agents' cards can, flag it even though every message used one name.
	queue := f.emailParticipant("queue@example.test", "Kai Mercer")
	f.sendAs(queue, "Kai Mercer")
	for _, name := range []string{"Kai Mercer", "Lena Ortiz"} {
		var personID int64
		require.NoError(f.st.DB().QueryRow(f.st.Rebind(
			`INSERT INTO persons (vcard_uid, display_name) VALUES (?, ?) RETURNING id`),
			"uid-"+name, name).Scan(&personID))
		_, err := f.st.AddPersonContactPointContext(t.Context(), personID, store.PersonContactPointInput{
			AddressKind: store.ContactAddressEmail, OriginalValue: "queue@example.test",
			Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceCardDAVImport},
		})
		require.NoError(err)
	}

	result, err := f.st.BuildContactMatchCandidatesContext(t.Context())
	require.NoError(err)
	assert.Equal(3, result.SharedMailbox)
	assert.Equal(0, result.Bind)
	matches, err := f.st.FindContactMatchesContext(t.Context())
	require.NoError(err)
	reasons := map[string][]correspondentkind.SharedMailboxReason{}
	for _, match := range matches {
		require.NotNil(match.SharedMailbox)
		reasons[match.SharedMailbox.Address] = match.SharedMailbox.Reasons
	}
	assert.Equal(map[string][]correspondentkind.SharedMailboxReason{
		"billing@shop.example.test": {correspondentkind.ReasonRoleAddress},
		"queue@example.test":        {correspondentkind.ReasonSeveralNames},
	}, reasons)
}

func TestContactMatchesKeepBindingOnePersonsAddress(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	// A card may name someone differently from their messages.
	nia := f.emailParticipant("nia@example.test", "Nia Okafor")
	f.sendAs(nia, "Nia Okafor", "Nia O.")
	f.importCards(f.card("card-nia", "Auntie Nia", []string{"nia@example.test"}, nil))

	matches, err := f.st.FindContactMatchesContext(t.Context())
	require.NoError(err)
	require.Len(matches, 1)
	assert.Equal(store.ContactMatchBind, matches[0].Classification)
	assert.Nil(matches[0].SharedMailbox)
}
