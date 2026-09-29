package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

type contactMatchFixture struct {
	t       *testing.T
	st      *store.Store
	account store.CardDAVAccount
	book    store.CardDAVAddressBook
}

func newContactMatchFixture(t *testing.T) *contactMatchFixture {
	t.Helper()
	st, account, book := newCardDAVResourceStore(t)
	return &contactMatchFixture{t: t, st: st, account: account, book: book}
}

// importCards replaces the address book with cards through the production
// CardDAV apply path, returning the person each card created or bound.
func (f *contactMatchFixture) importCards(cards ...store.CardDAVRemoteResource) map[string]int64 {
	f.t.Helper()
	books, err := f.st.ListCardDAVAddressBooksContext(f.t.Context())
	require.NoError(f.t, err)
	var revision int64
	for _, book := range books {
		if book.ID == f.book.ID {
			revision = book.SyncRevision
		}
	}
	_, err = f.st.ApplyCardDAVSyncPlanContext(f.t.Context(), store.CardDAVSyncPlan{
		AddressBookID: f.book.ID, ConnectionGeneration: f.account.ConnectionGeneration,
		SyncRevision: revision, ReplaceAll: true, Upserts: cards,
	})
	require.NoError(f.t, err)
	people := map[string]int64{}
	for _, card := range cards {
		resource, err := f.st.GetCardDAVResourceContext(f.t.Context(), f.book.ID, card.Href)
		require.NoError(f.t, err)
		require.NotNil(f.t, resource.PersonID)
		people[card.RemoteUID] = *resource.PersonID
	}
	return people
}

func (f *contactMatchFixture) card(uid, name string, emails, phones []string) store.CardDAVRemoteResource {
	body := "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:" + uid + "\r\nFN:" + name + "\r\n"
	for _, email := range emails {
		body += "EMAIL:" + email + "\r\n"
	}
	for _, phone := range phones {
		body += "TEL:" + phone + "\r\n"
	}
	body += "END:VCARD\r\n"
	return store.CardDAVRemoteResource{
		Href: f.book.CanonicalURL + uid + ".vcf", RemoteUID: uid, RemoteETag: `"` + uid + `"`,
		RemoteBody: []byte(body), SemanticHash: "semantic-" + uid, DisplayName: name,
		Emails: emails, Phones: phones,
	}
}

func (f *contactMatchFixture) emailParticipant(email, name string) int64 {
	f.t.Helper()
	id, err := f.st.EnsureParticipant(email, name, "example.test")
	require.NoError(f.t, err)
	return id
}

func matchesByPerson(matches []store.ContactMatch) map[int64][]store.ContactMatch {
	grouped := map[int64][]store.ContactMatch{}
	for _, match := range matches {
		grouped[match.ContactPersonID] = append(grouped[match.ContactPersonID], match)
	}
	return grouped
}

func TestFindContactMatchesClassifiesBindAndMergeAtClusterLevel(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	// An unbound two-member cluster matched through its second member.
	unboundA := f.emailParticipant("ada.alt@example.test", "Ada")
	unboundB := f.emailParticipant("ada@example.test", "Ada")
	_, err := f.st.LinkParticipants(unboundA, unboundB)
	require.NoError(err)

	// A cluster already promoted to a person.
	bound := f.emailParticipant("bo@example.test", "Bo")
	existing, _, err := f.st.CreatePersonFromParticipantContext(t.Context(), bound)
	require.NoError(err)

	// A participant nobody's card mentions.
	f.emailParticipant("unrelated@example.test", "Unrelated")

	people := f.importCards(
		f.card("card-ada", "Ada Contact", []string{"ADA@example.test"}, nil),
		f.card("card-bo", "Bo Contact", []string{"bo@example.test"}, nil),
		f.card("card-none", "Nobody", []string{"nobody@example.test"}, nil),
	)
	require.NotEqual(existing.ID, people["card-bo"], "cards must not bind to people by participant")

	matches, err := f.st.FindContactMatchesContext(t.Context())
	require.NoError(err)
	grouped := matchesByPerson(matches)
	require.Len(grouped, 2)

	ada := grouped[people["card-ada"]]
	require.Len(ada, 1)
	assert.Equal(store.ContactMatchBind, ada[0].Classification)
	assert.Equal(unboundB, ada[0].ParticipantID)
	assert.ElementsMatch([]int64{unboundA, unboundB}, ada[0].ClusterMembers)
	assert.Empty(ada[0].ClusterPersonIDs)
	require.Len(ada[0].Identifiers, 1)
	assert.Equal(store.IdentityMatchEmail, ada[0].Identifiers[0].Basis)
	assert.Equal("ada@example.test", ada[0].Identifiers[0].NormalizedValue)
	assert.Equal("participants.email_address", ada[0].Identifiers[0].MatchedField)
	assert.Nil(ada[0].BlockedReason)

	bo := grouped[people["card-bo"]]
	require.Len(bo, 1)
	assert.Equal(store.ContactMatchMerge, bo[0].Classification)
	assert.Equal([]int64{existing.ID}, bo[0].ClusterPersonIDs)
}
