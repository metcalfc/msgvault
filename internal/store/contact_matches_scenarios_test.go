package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

func TestFindContactMatchesMarksClusterSpanningPeopleAmbiguous(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	first := f.emailParticipant("hal@example.test", "Hal")
	second := f.emailParticipant("hal.work@example.test", "Hal")
	firstPerson, _, err := f.st.CreatePersonFromParticipantContext(t.Context(), first)
	require.NoError(err)
	secondPerson, _, err := f.st.CreatePersonFromParticipantContext(t.Context(), second)
	require.NoError(err)
	// Legacy archives can hold a cluster whose members are bound to two
	// people; the link API refuses to create one, so the state is written
	// directly to exercise the classifier.
	_, err = f.st.DB().Exec(`INSERT INTO participant_links (participant_a, participant_b) VALUES (?, ?)`,
		min(first, second), max(first, second))
	require.NoError(err)
	people := f.importCards(f.card("card-hal", "Hal Contact", []string{"hal@example.test"}, nil))

	matches, err := f.st.FindContactMatchesContext(t.Context())
	require.NoError(err)
	require.Len(matches, 1)
	assert.Equal(people["card-hal"], matches[0].ContactPersonID)
	assert.Equal(store.ContactMatchAmbiguous, matches[0].Classification)
	assert.ElementsMatch([]int64{firstPerson.ID, secondPerson.ID}, matches[0].ClusterPersonIDs)

	candidate := f.buildCandidate(people["card-hal"])
	_, _, err = f.st.AcceptIdentityMatchCandidateContext(t.Context(), candidate.ID, "user", nil)
	require.ErrorIs(err, store.ErrPersonBindingConflict)
	unchanged, err := f.st.GetIdentityMatchCandidateContext(t.Context(), candidate.ID)
	require.NoError(err)
	assert.Equal(store.IdentityMatchStateCandidate, unchanged.State)
}

func TestFindContactMatchesExcludesOwnerClusters(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)
	source, err := f.st.GetOrCreateSource("gmail", "owner@example.test")
	require.NoError(err)

	// Owner by primary email, and a second identity linked into the owner's
	// cluster: neither may be proposed for the owner's own card.
	owner := f.emailParticipant("owner@example.test", "Owner")
	alias := f.emailParticipant("owner.alias@example.test", "Owner")
	_, err = f.st.LinkParticipants(owner, alias)
	require.NoError(err)
	require.NoError(f.st.AddAccountIdentityContext(t.Context(), source.ID, "owner@example.test", "manual"))

	// Owner by a chat phone identifier stored in a different form than the
	// card's phone.
	_, err = f.st.EnsureParticipantByPhone("+15550100999", "Owner Phone", "imessage")
	require.NoError(err)
	require.NoError(f.st.AddAccountIdentityContext(t.Context(), source.ID, "+15550100999", "manual"))

	// An ordinary correspondent still matches.
	other := f.emailParticipant("ivy@example.test", "Ivy")

	people := f.importCards(
		f.card("card-owner", "Owner Card", []string{"owner.alias@example.test"}, []string{"(555) 010-0999"}),
		f.card("card-ivy", "Ivy Contact", []string{"ivy@example.test"}, nil),
	)

	matches, err := f.st.FindContactMatchesContext(t.Context())
	require.NoError(err)
	require.Len(matches, 1, "owner matches by email and phone must be excluded")
	assert.Equal(people["card-ivy"], matches[0].ContactPersonID)
	assert.Equal(other, matches[0].ParticipantID)
}

func TestFindContactMatchesNormalizesNonCanonicalParticipantPhones(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	// A Beeper-style participant phone with punctuation, and a bare-digit
	// phone identifier with no leading plus.
	formatted, err := f.st.EnsureParticipantByPhone("+1 (555) 010-0142", "Jo Chat", "whatsapp")
	require.NoError(err)
	bare, err := f.st.EnsureParticipantByIdentifier("phone", "15550100143", "Kit Chat")
	require.NoError(err)
	people := f.importCards(
		f.card("card-jo", "Jo Contact", nil, []string{"555-010-0142"}),
		f.card("card-kit", "Kit Contact", nil, []string{"+1 555 010 0143"}),
	)

	matches, err := f.st.FindContactMatchesContext(t.Context())
	require.NoError(err)
	grouped := matchesByPerson(matches)
	require.Len(grouped, 2)
	jo := grouped[people["card-jo"]]
	require.Len(jo, 1)
	assert.Equal(formatted, jo[0].ParticipantID)
	require.Len(jo[0].Identifiers, 1)
	assert.Equal(store.IdentityMatchPhone, jo[0].Identifiers[0].Basis)
	assert.Equal("+15550100142", jo[0].Identifiers[0].NormalizedValue)
	assert.Equal("participants.phone_number", jo[0].Identifiers[0].MatchedField)
	kit := grouped[people["card-kit"]]
	require.Len(kit, 1)
	assert.Equal(bare, kit[0].ParticipantID)
	assert.Equal("participant_identifiers.phone", kit[0].Identifiers[0].MatchedField)
	assert.Equal("+15550100143", kit[0].Identifiers[0].NormalizedValue)
}

func TestRejectedContactMatchSurvivesRerunsAndCardReimport(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	f.emailParticipant("lu@example.test", "Lu")
	card := f.card("card-lu", "Lu Contact", []string{"lu@example.test"}, nil)
	people := f.importCards(card)
	candidate := f.buildCandidate(people["card-lu"])
	_, err := f.st.DecideIdentityMatchCandidateContext(
		t.Context(), candidate.ID, store.IdentityMatchStateRejected, "user", nil)
	require.NoError(err)

	for range 2 {
		result, err := f.st.BuildContactMatchCandidatesContext(t.Context())
		require.NoError(err)
		assert.Equal(store.ContactMatchBuildResult{}, *result, "a rejected pair is never re-proposed")
	}

	// The rejection is user-owned state, so removing the card keeps the
	// profile and its decision instead of deleting it as untouched. The
	// re-imported card binds back to the same profile by its address, and
	// the rejection still suppresses the pair.
	f.importCards()
	_, err = f.st.GetPersonContext(t.Context(), people["card-lu"])
	require.NoError(err, "a profile with a user decision survives card removal")
	reimported := f.importCards(card)
	assert.Equal(people["card-lu"], reimported["card-lu"])
	result, err := f.st.BuildContactMatchCandidatesContext(t.Context())
	require.NoError(err)
	assert.Equal(store.ContactMatchBuildResult{}, *result)
	rejected, err := f.st.GetIdentityMatchCandidateContext(t.Context(), candidate.ID)
	require.NoError(err)
	assert.Equal(store.IdentityMatchStateRejected, rejected.State)
}

func TestContactMatchBlockedByUnresolvedCardDAVConflict(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st, conflict := approvedStandaloneConflict(t)
	_, err := st.PrepareCardDAVConflictLocalContext(t.Context(), store.CardDAVConflictLocalPlan{
		ConflictID: conflict.ID, ExpectedMappingRevision: conflict.MappingRevision,
		RemoteETag: conflict.RemoteETag, OutgoingSemanticHash: "approved",
	})
	require.NoError(err)
	source, err := st.LoadCardDAVConflictReviewSourceContext(t.Context(), conflict.ID)
	require.NoError(err)
	personID := source.Person.ID
	var pointID int64
	require.NoError(st.DB().QueryRow(`INSERT INTO person_contact_points (
		person_id, address_kind, original_value, normalized_value, normalization, source
	) VALUES (?, 'email', 'standalone@example.test', 'standalone@example.test', 'email', 'carddav_import')
	RETURNING id`, personID).Scan(&pointID))
	_, err = st.EnsureParticipant("standalone@example.test", "Standalone Sender", "example.test")
	require.NoError(err)

	matches, err := st.FindContactMatchesContext(t.Context())
	require.NoError(err)
	require.Len(matches, 1)
	require.NotNil(matches[0].BlockedReason)
	assert.Equal(store.ContactMatchBlockedCardDAVConflict, *matches[0].BlockedReason)

	result, err := st.BuildContactMatchCandidatesContext(t.Context())
	require.NoError(err)
	assert.Equal(1, result.Blocked)
	candidates, err := st.ListContactMatchCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	require.Len(candidates, 1)
	_, _, err = st.AcceptIdentityMatchCandidateContext(t.Context(), candidates[0].ID, "user", nil)
	require.ErrorIs(err, store.ErrPersonCardDAVPublished)
}

func TestAcceptedContactBindSurvivesRemoteUpdateDeletionAndSplit(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	participant := f.emailParticipant("max@example.test", "Max Sender")
	card := f.card("card-max", "Max Contact", []string{"max@example.test"}, nil)
	people := f.importCards(card)
	contactID := people["card-max"]
	candidate := f.buildCandidate(contactID)
	_, _, err := f.st.AcceptIdentityMatchCandidateContext(t.Context(), candidate.ID, "user", nil)
	require.NoError(err)
	contact, err := f.st.GetPersonContext(t.Context(), contactID)
	require.NoError(err)
	uid := contact.VCardUID

	// A remote edit to the absorbed card lands on the surviving profile.
	updated := f.card("card-max", "Max Renamed", []string{"max@example.test"}, nil)
	updated.RemoteETag = `"card-max-2"`
	updated.SemanticHash = "semantic-card-max-2"
	f.importCards(updated)
	resource, err := f.st.GetCardDAVResourceContext(t.Context(), f.book.ID, card.Href)
	require.NoError(err)
	require.NotNil(resource.PersonID)
	assert.Equal(contactID, *resource.PersonID)
	assert.Equal(`"card-max-2"`, resource.RemoteETag)
	contact, err = f.st.GetPersonContext(t.Context(), contactID)
	require.NoError(err)
	assert.Equal([]int64{participant}, contact.ParticipantIDs)
	assert.Equal(uid, contact.VCardUID)

	// Split reverses the bind: the archive identity leaves on a new
	// profile and the contact profile keeps its card mapping and UID.
	merges, err := f.st.ListPersonMergesContext(t.Context(), contactID)
	require.NoError(err)
	require.Len(merges, 1)
	split, err := f.st.SplitPersonMergeContext(t.Context(), store.PersonSplitRequest{
		SourcePersonID: contactID, MergeID: merges[0].Merge.ID,
		ParticipantIDs: []int64{participant}, ExpectedSourceRevision: contact.Revision,
		IdempotencyKey: "split-max", Actor: "user",
	})
	require.NoError(err)
	assert.Equal([]int64{participant}, split.NewPerson.ParticipantIDs)
	assert.Empty(split.SourcePerson.ParticipantIDs)
	assert.Equal(uid, split.SourcePerson.VCardUID)
	resource, err = f.st.GetCardDAVResourceContext(t.Context(), f.book.ID, card.Href)
	require.NoError(err)
	require.NotNil(resource.PersonID)
	assert.Equal(contactID, *resource.PersonID, "split keeps the card on the contact profile")

	// The split left the contact profile unbound again; the accepted
	// decision is retained and nothing is re-proposed.
	result, err := f.st.BuildContactMatchCandidatesContext(t.Context())
	require.NoError(err)
	assert.Equal(0, result.Created)

	// A remote deletion of the card no longer deletes the profile, which
	// has user history now.
	f.importCards()
	_, err = f.st.GetCardDAVResourceContext(t.Context(), f.book.ID, card.Href)
	require.ErrorIs(err, store.ErrCardDAVResourceNotFound)
	_, err = f.st.GetPersonContext(t.Context(), contactID)
	require.NoError(err, "a profile touched by a bind survives remote deletion of its card")
}

func TestAcceptedContactBindSurvivesRemoteDeletion(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	participant := f.emailParticipant("ned@example.test", "Ned Sender")
	card := f.card("card-ned", "Ned Contact", []string{"ned@example.test"}, nil)
	people := f.importCards(card)
	candidate := f.buildCandidate(people["card-ned"])
	_, _, err := f.st.AcceptIdentityMatchCandidateContext(t.Context(), candidate.ID, "user", nil)
	require.NoError(err)

	f.importCards()
	person, err := f.st.GetPersonContext(t.Context(), people["card-ned"])
	require.NoError(err, "the bound profile outlives its remote card")
	assert.Equal([]int64{participant}, person.ParticipantIDs)
}
