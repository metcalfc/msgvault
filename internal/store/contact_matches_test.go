package store_test

import (
	"errors"
	"strings"
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
	var body strings.Builder
	body.WriteString("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:" + uid + "\r\nFN:" + name + "\r\n")
	for _, email := range emails {
		body.WriteString("EMAIL:" + email + "\r\n")
	}
	for _, phone := range phones {
		body.WriteString("TEL:" + phone + "\r\n")
	}
	body.WriteString("END:VCARD\r\n")
	return store.CardDAVRemoteResource{
		Href: f.book.CanonicalURL + uid + ".vcf", RemoteUID: uid, RemoteETag: `"` + uid + `"`,
		RemoteBody: []byte(body.String()), SemanticHash: "semantic-" + uid, DisplayName: name,
		Emails: emails, Phones: phones,
	}
}

func (f *contactMatchFixture) emailParticipant(email, name string) int64 {
	f.t.Helper()
	id, err := f.st.EnsureParticipant(email, name, "example.test")
	require.NoError(f.t, err)
	return id
}

// phoneParticipant creates a chat identity known only by its phone number.
// A phone match is never decided automatically, so tests of the manual
// accept path use one.
func (f *contactMatchFixture) phoneParticipant(phone, name string) int64 {
	f.t.Helper()
	id, err := f.st.EnsureParticipantByPhone(phone, name, "whatsapp")
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

func TestBuildContactMatchCandidatesWritesIdempotentSystemRowsWithEvidence(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	participant := f.emailParticipant("cy@example.test", "Cy")
	people := f.importCards(f.card("card-cy", "Cy Contact", []string{"cy@example.test"}, nil))

	first, err := f.st.BuildContactMatchCandidatesContext(t.Context())
	require.NoError(err)
	assert.Equal(store.ContactMatchBuildResult{
		Matches: 1, Created: 1, EvidenceAdded: 1, Bind: 1, AutoBound: 1,
	}, *first, "an exact email to an unbound identity is linked without review")

	candidates, err := f.st.ListIdentityMatchCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	require.Len(candidates, 1)
	candidate := candidates[0]
	assert.Equal(store.IdentityMatchParticipant, candidate.LeftKind)
	assert.Equal(participant, candidate.LeftID)
	assert.Equal(store.IdentityMatchPerson, candidate.RightKind)
	assert.Equal(people["card-cy"], candidate.RightID)
	assert.Equal(store.IdentityMatchEmail, candidate.Basis)
	require.NotNil(candidate.NormalizedValue)
	assert.Equal("cy@example.test", *candidate.NormalizedValue)
	assert.Equal(store.ProvenanceSystem, candidate.Source)
	require.NotNil(candidate.Confidence)
	assert.InDelta(1.0, *candidate.Confidence, 0)
	require.NotNil(candidate.SourceRef)
	assert.Equal(store.ContactMatchSourceRef, *candidate.SourceRef)
	assert.Equal(store.IdentityMatchStateAccepted, candidate.State)
	require.NotNil(candidate.DecidedBy)
	assert.Equal(store.ContactMatchAutoActor, *candidate.DecidedBy)
	require.NotNil(candidate.Notes)
	assert.Equal("linked automatically: same email cy@example.test", *candidate.Notes)
	require.Len(candidate.Evidence, 1)
	assert.Equal(store.ContactMatchEvidenceKind, candidate.Evidence[0].EvidenceKind)
	require.NotNil(candidate.Evidence[0].EvidenceRef)
	assert.Contains(*candidate.Evidence[0].EvidenceRef, "person_contact_point:")
	require.NotNil(candidate.Evidence[0].Detail)
	assert.Contains(*candidate.Evidence[0].Detail, "participants.email_address=cy@example.test")

	// The contact profile now has the archive identity, so a rerun finds
	// nothing to match and changes nothing.
	second, err := f.st.BuildContactMatchCandidatesContext(t.Context())
	require.NoError(err)
	assert.Equal(store.ContactMatchBuildResult{}, *second)
	contact, err := f.st.GetPersonContext(t.Context(), people["card-cy"])
	require.NoError(err)
	assert.Equal([]int64{participant}, contact.ParticipantIDs)
	again, err := f.st.ListIdentityMatchCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	require.Len(again, 1)
	assert.Len(again[0].Evidence, 1)
}

func (f *contactMatchFixture) publish(personID int64, card store.CardDAVRemoteResource) {
	f.t.Helper()
	snapshot, err := f.st.LoadPersonVCardSnapshotContext(f.t.Context(), personID)
	require.NoError(f.t, err)
	_, err = f.st.PrepareCardDAVPublicationContext(f.t.Context(), store.CardDAVPublicationPlan{
		PersonID: personID, Desired: true, AddressBookID: f.book.ID, Href: card.Href,
		OutgoingBody: card.RemoteBody, OutgoingSemanticHash: card.SemanticHash,
		LocalHash: snapshot.Fingerprint,
	})
	require.NoError(f.t, err)
}

func (f *contactMatchFixture) buildCandidate(personID int64) store.IdentityMatchCandidate {
	f.t.Helper()
	_, err := f.st.BuildContactMatchCandidatesContext(f.t.Context())
	require.NoError(f.t, err)
	candidates, err := f.st.ListIdentityMatchCandidatesContext(f.t.Context(), nil, 500, 0)
	require.NoError(f.t, err)
	for _, candidate := range candidates {
		if candidate.RightKind == store.IdentityMatchPerson && candidate.RightID == personID {
			return candidate
		}
	}
	require.FailNow(f.t, "no contact match candidate for person")
	return store.IdentityMatchCandidate{}
}

func TestAcceptContactMatchBindPromotesClusterIntoContactProfile(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	first := f.emailParticipant("di@example.test", "Di")
	second := f.phoneParticipant("+15550100131", "Di")
	_, err := f.st.LinkParticipants(first, second)
	require.NoError(err)
	card := f.card("card-di", "Di Contact", nil, []string{"+1 555 010 0131"})
	people := f.importCards(card)
	contactID := people["card-di"]
	contactBefore, err := f.st.GetPersonContext(t.Context(), contactID)
	require.NoError(err)
	candidate := f.buildCandidate(contactID)

	accepted, revision, err := f.st.AcceptIdentityMatchCandidateContext(
		t.Context(), candidate.ID, "user", nil)
	require.NoError(err)
	assert.Positive(revision)
	assert.Equal(store.IdentityMatchStateAccepted, accepted.State)
	require.NotNil(accepted.DecidedBy)
	assert.Equal("user", *accepted.DecidedBy)

	contactAfter, err := f.st.GetPersonContext(t.Context(), contactID)
	require.NoError(err)
	assert.Equal(contactBefore.VCardUID, contactAfter.VCardUID, "the contact profile survives")
	assert.ElementsMatch([]int64{first, second}, contactAfter.ParticipantIDs)
	resource, err := f.st.GetCardDAVResourceContext(t.Context(), f.book.ID, card.Href)
	require.NoError(err)
	require.NotNil(resource.PersonID)
	assert.Equal(contactID, *resource.PersonID, "the card mapping stays on the survivor")

	merges, err := f.st.ListPersonMergesContext(t.Context(), contactID)
	require.NoError(err)
	require.Len(merges, 1)
	assert.Equal(contactID, merges[0].Merge.SurvivorPersonID)

	again, _, err := f.st.AcceptIdentityMatchCandidateContext(t.Context(), candidate.ID, "user", nil)
	require.NoError(err, "re-accepting a linked match is idempotent")
	assert.Equal(store.IdentityMatchStateAccepted, again.State)
	merges, err = f.st.ListPersonMergesContext(t.Context(), contactID)
	require.NoError(err)
	assert.Len(merges, 1)
}

func TestAcceptContactMatchMergeRequiresExplicitSurvivorChoice(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	participant := f.phoneParticipant("+15550100132", "Eve")
	existing, _, err := f.st.CreatePersonFromParticipantContext(t.Context(), participant)
	require.NoError(err)
	people := f.importCards(f.card("card-eve", "Eve Contact", nil, []string{"+1 555 010 0132"}))
	contactID := people["card-eve"]
	candidate := f.buildCandidate(contactID)

	_, _, err = f.st.AcceptIdentityMatchCandidateContext(t.Context(), candidate.ID, "user", nil)
	conflict, ok := errors.AsType[*store.PersonBindingConflictError](err)
	require.True(ok, "a merge candidate must return a person binding conflict: %v", err)
	assert.ElementsMatch([]int64{existing.ID, contactID}, conflict.PersonIDs)

	unchanged, err := f.st.GetIdentityMatchCandidateContext(t.Context(), candidate.ID)
	require.NoError(err)
	assert.Equal(store.IdentityMatchStateCandidate, unchanged.State, "the refusal must not consume the candidate")

	// The user merges, choosing the existing profile as survivor; the
	// candidate is then linked and accepting it records the decision.
	survivor, err := f.st.GetPersonContext(t.Context(), existing.ID)
	require.NoError(err)
	absorbed, err := f.st.GetPersonContext(t.Context(), contactID)
	require.NoError(err)
	_, err = f.st.MergePersonsContext(t.Context(), store.PersonMergeRequest{
		SurvivorID: survivor.ID, AbsorbedID: absorbed.ID,
		ExpectedSurvivorRevision: survivor.Revision, ExpectedAbsorbedRevision: absorbed.Revision,
		IdempotencyKey: "merge-eve", Actor: "user",
	})
	require.NoError(err)
	retargeted, err := f.st.GetIdentityMatchCandidateContext(t.Context(), candidate.ID)
	require.NoError(err)
	assert.Equal(existing.ID, retargeted.RightID, "the absorbed side is retargeted to the survivor")
	accepted, _, err := f.st.AcceptIdentityMatchCandidateContext(t.Context(), candidate.ID, "user", nil)
	require.NoError(err)
	assert.Equal(store.IdentityMatchStateAccepted, accepted.State)
}

func TestAcceptContactMatchBindRefusesPublishedContactProfile(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	f.emailParticipant("fay@example.test", "Fay")
	card := f.card("card-fay", "Fay Contact", []string{"fay@example.test"}, nil)
	people := f.importCards(card)
	contactID := people["card-fay"]
	f.publish(contactID, card)
	candidate := f.buildCandidate(contactID)

	matches, err := f.st.FindContactMatchesContext(t.Context())
	require.NoError(err)
	require.Len(matches, 1)
	require.NotNil(matches[0].BlockedReason)
	assert.Equal(store.ContactMatchBlockedPublished, *matches[0].BlockedReason)

	_, _, err = f.st.AcceptIdentityMatchCandidateContext(t.Context(), candidate.ID, "user", nil)
	require.ErrorIs(err, store.ErrPersonCardDAVPublished)
	unchanged, err := f.st.GetIdentityMatchCandidateContext(t.Context(), candidate.ID)
	require.NoError(err)
	assert.Equal(store.IdentityMatchStateCandidate, unchanged.State)
	contact, err := f.st.GetPersonContext(t.Context(), contactID)
	require.NoError(err)
	assert.Empty(contact.ParticipantIDs, "a refused bind promotes nothing")
}

func TestAcceptContactMatchRefusesSystemDecision(t *testing.T) {
	require := require.New(t)
	f := newContactMatchFixture(t)

	f.emailParticipant("gus@example.test", "Gus")
	people := f.importCards(f.card("card-gus", "Gus Contact", []string{"gus@example.test"}, nil))
	candidate := f.buildCandidate(people["card-gus"])

	_, _, err := f.st.AcceptIdentityMatchCandidateContext(t.Context(), candidate.ID, "system", nil)
	require.ErrorIs(err, store.ErrIdentityMatchNotAcceptable)
}
