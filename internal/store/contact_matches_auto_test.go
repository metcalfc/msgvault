package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/store"
)

// archivePerson promotes an email identity to a person, the way automatic
// promotion does for frequent correspondents.
func (f *contactMatchFixture) archivePerson(email, name string) (int64, int64) {
	f.t.Helper()
	participant := f.emailParticipant(email, name)
	person, _, err := f.st.CreatePersonFromParticipantContext(f.t.Context(), participant)
	require.NoError(f.t, err)
	return participant, person.ID
}

func (f *contactMatchFixture) build() store.ContactMatchBuildResult {
	f.t.Helper()
	result, err := f.st.BuildContactMatchCandidatesContext(f.t.Context())
	require.NoError(f.t, err)
	return *result
}

func (f *contactMatchFixture) personExists(personID int64) bool {
	f.t.Helper()
	_, err := f.st.GetPersonContext(f.t.Context(), personID)
	if err == nil {
		return true
	}
	require.ErrorIs(f.t, err, store.ErrPersonNotFound)
	return false
}

func (f *contactMatchFixture) candidatesFor(participantID int64) []store.IdentityMatchCandidate {
	f.t.Helper()
	candidates, err := f.st.ListContactMatchCandidatesContext(f.t.Context(), nil, 500, 0)
	require.NoError(f.t, err)
	found := []store.IdentityMatchCandidate{}
	for _, candidate := range candidates {
		if candidate.LeftID == participantID {
			found = append(found, candidate)
		}
	}
	return found
}

func TestBuildMergesExactEmailContactProfileIntoArchivePerson(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	participant, archiveID := f.archivePerson("matt@example.test", "Matt Example")
	card := f.card("card-matt", "Matt Example", []string{"Matt@Example.test"}, []string{"+1 555 010 0150"})
	people := f.importCards(card)
	contactID := people["card-matt"]
	require.NotEqual(archiveID, contactID, "the import creates a contact profile first")

	result := f.build()
	assert.Equal(store.ContactMatchBuildResult{
		Matches: 1, Created: 1, EvidenceAdded: 1, Merge: 1, AutoMerged: 1,
	}, result)

	// The archive person survives and absorbs the contact profile.
	assert.False(f.personExists(contactID), "the contact profile is absorbed")
	archive, err := f.st.GetPersonContext(t.Context(), archiveID)
	require.NoError(err)
	assert.Equal([]int64{participant}, archive.ParticipantIDs)
	points, err := f.st.ListPersonContactPointsContext(t.Context(), archiveID, true)
	require.NoError(err)
	values := []string{}
	for _, point := range points {
		values = append(values, point.NormalizedValue)
	}
	assert.Subset(values, []string{"matt@example.test", "+15550100150"},
		"the card's contact points move to the survivor")
	resource, err := f.st.GetCardDAVResourceContext(t.Context(), f.book.ID, card.Href)
	require.NoError(err)
	require.NotNil(resource.PersonID)
	assert.Equal(archiveID, *resource.PersonID, "the card binding follows the survivor")

	// Merge history names the rule and the evidence.
	merges, err := f.st.ListPersonMergesContext(t.Context(), archiveID)
	require.NoError(err)
	require.Len(merges, 1)
	assert.Equal(archiveID, merges[0].Merge.SurvivorPersonID)
	assert.Equal(contactID, merges[0].Merge.AbsorbedPersonID)
	assert.Equal("rule:contact_match:email:matt@example.test", merges[0].Merge.Actor)

	candidates := f.candidatesFor(participant)
	require.Len(candidates, 1)
	assert.Equal(store.IdentityMatchStateAccepted, candidates[0].State)
	assert.Equal(archiveID, candidates[0].RightID, "the decision follows the survivor")
	require.NotNil(candidates[0].DecidedBy)
	assert.Equal(store.ContactMatchAutoActor, *candidates[0].DecidedBy)
	require.NotNil(candidates[0].Notes)
	assert.Equal("merged automatically: same email matt@example.test", *candidates[0].Notes)

	// Rerunning finds nothing left to decide and merges nothing again.
	assert.Equal(store.ContactMatchBuildResult{}, f.build())
	merges, err = f.st.ListPersonMergesContext(t.Context(), archiveID)
	require.NoError(err)
	assert.Len(merges, 1)

	// A later sync of the same card stays on the survivor.
	f.importCards(card)
	resource, err = f.st.GetCardDAVResourceContext(t.Context(), f.book.ID, card.Href)
	require.NoError(err)
	require.NotNil(resource.PersonID)
	assert.Equal(archiveID, *resource.PersonID)
	assert.Equal(store.ContactMatchBuildResult{}, f.build())
}

func TestBuildLinksExactEmailIdentityToContactProfile(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	first := f.emailParticipant("nia@example.test", "Nia")
	second := f.emailParticipant("nia.work@example.test", "Nia")
	_, err := f.st.LinkParticipants(first, second)
	require.NoError(err)
	people := f.importCards(f.card("card-nia", "Nia Contact", []string{"nia.work@example.test"}, nil))
	contactID := people["card-nia"]

	result := f.build()
	assert.Equal(1, result.AutoBound)
	assert.Equal(0, result.LeftForReview)
	contact, err := f.st.GetPersonContext(t.Context(), contactID)
	require.NoError(err)
	assert.ElementsMatch([]int64{first, second}, contact.ParticipantIDs,
		"the whole identity cluster joins the contact profile")
	merges, err := f.st.ListPersonMergesContext(t.Context(), contactID)
	require.NoError(err)
	require.Len(merges, 1)
	assert.Equal(contactID, merges[0].Merge.SurvivorPersonID, "the contact profile keeps its ID and UID")
	assert.Equal("rule:contact_match:email:nia.work@example.test", merges[0].Merge.Actor)
	assert.Equal(store.ContactMatchBuildResult{}, f.build())
}

func TestBuildLeavesJudgmentsForReview(t *testing.T) {
	type scenario struct {
		name string
		// setup arranges the archive and cards and returns the archive
		// person (zero for none) and the contact profile under test.
		setup func(f *contactMatchFixture) (archiveID, contactID int64)
		// pending reports whether the match should wait in the queue, as
		// opposed to never being proposed at all.
		pending bool
	}
	scenarios := []scenario{
		{
			name: "shared mailbox", pending: true,
			setup: func(f *contactMatchFixture) (int64, int64) {
				desk, archiveID := f.archivePerson("desk@example.test", "Avery Stone")
				f.sendAs(desk, "Avery Stone", "Blake Rivera", "avery stone via Example Desk")
				people := f.importCards(f.card("card-desk", "Avery Stone", []string{"desk@example.test"}, nil))
				return archiveID, people["card-desk"]
			},
		},
		{
			name: "owner identity",
			setup: func(f *contactMatchFixture) (int64, int64) {
				source, err := f.st.GetOrCreateSource("gmail", "me@example.test")
				require.NoError(f.t, err)
				_, archiveID := f.archivePerson("me@example.test", "Me")
				require.NoError(f.t, f.st.AddAccountIdentityContext(f.t.Context(), source.ID, "me@example.test", "manual"))
				people := f.importCards(f.card("card-me", "Me", []string{"me@example.test"}, nil))
				return archiveID, people["card-me"]
			},
		},
		{
			name: "user said not a person",
			setup: func(f *contactMatchFixture) (int64, int64) {
				participant, archiveID := f.archivePerson("alerts@example.test", "Alerts")
				_, err := f.st.SetCorrespondentKindContext(f.t.Context(), store.SetCorrespondentKindInput{
					ParticipantID: participant, Kind: correspondentkind.Automated, Actor: "user"})
				require.NoError(f.t, err)
				people := f.importCards(f.card("card-alerts", "Alerts", []string{"alerts@example.test"}, nil))
				return archiveID, people["card-alerts"]
			},
		},
		{
			name: "a rule says not a person", pending: true,
			setup: func(f *contactMatchFixture) (int64, int64) {
				participant, archiveID := f.archivePerson("news@example.test", "News")
				_, err := f.st.WriteDerivedCorrespondentKindsContext(f.t.Context(), []store.DerivedCorrespondentKind{{
					ParticipantID: participant, Source: correspondentkind.SourceRule, Kind: correspondentkind.Automated,
				}})
				require.NoError(f.t, err)
				people := f.importCards(f.card("card-news", "News", []string{"news@example.test"}, nil))
				return archiveID, people["card-news"]
			},
		},
		{
			name: "earlier rejection",
			setup: func(f *contactMatchFixture) (int64, int64) {
				participant, archiveID := f.archivePerson("ola@example.test", "Ola")
				people := f.importCards(f.card("card-ola", "Ola", []string{"ola@example.test"}, nil))
				normalized := "ola@example.test"
				sourceRef := store.ContactMatchSourceRef
				candidate, _, err := f.st.UpsertIdentityMatchCandidateContext(f.t.Context(), store.IdentityMatchCandidateInput{
					LeftKind: store.IdentityMatchParticipant, LeftID: participant,
					RightKind: store.IdentityMatchPerson, RightID: people["card-ola"],
					Basis: store.IdentityMatchEmail, NormalizedValue: &normalized,
					State: store.IdentityMatchStateCandidate, Source: store.ProvenanceSystem, SourceRef: &sourceRef,
				})
				require.NoError(f.t, err)
				_, err = f.st.DecideIdentityMatchCandidateContext(f.t.Context(), candidate.ID,
					store.IdentityMatchStateRejected, "user", nil)
				require.NoError(f.t, err)
				return archiveID, people["card-ola"]
			},
		},
		{
			name: "contact profile already has its own identity",
			setup: func(f *contactMatchFixture) (int64, int64) {
				_, archiveID := f.archivePerson("pia@example.test", "Pia")
				people := f.importCards(f.card("card-pia", "Pia", []string{"pia@example.test"}, nil))
				_, otherID := f.archivePerson("pia.chat@example.test", "Pia")
				f.mergeInto(people["card-pia"], otherID)
				return archiveID, people["card-pia"]
			},
		},
		{
			name: "contact matches two identity clusters", pending: true,
			setup: func(f *contactMatchFixture) (int64, int64) {
				_, archiveID := f.archivePerson("ray@example.test", "Ray")
				f.emailParticipant("ray.home@example.test", "Ray")
				people := f.importCards(f.card("card-ray", "Ray",
					[]string{"ray@example.test", "ray.home@example.test"}, nil))
				return archiveID, people["card-ray"]
			},
		},
		{
			name: "two contacts match one identity cluster", pending: true,
			setup: func(f *contactMatchFixture) (int64, int64) {
				_, archiveID := f.archivePerson("sky@example.test", "Sky")
				people := f.importCards(
					f.card("card-sky", "Sky", []string{"sky@example.test"}, nil),
					f.card("card-sky-old", "Sky Old", []string{"sky.old@example.test"}, nil),
				)
				// A second card would bind to the first profile by its
				// address, so the older profile gains it by hand.
				_, err := f.st.AddPersonContactPointContext(f.t.Context(), people["card-sky-old"],
					store.PersonContactPointInput{
						AddressKind: store.ContactAddressEmail, OriginalValue: "sky@example.test",
						Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceUser},
					})
				require.NoError(f.t, err)
				return archiveID, people["card-sky"]
			},
		},
		{
			name: "phone only", pending: true,
			setup: func(f *contactMatchFixture) (int64, int64) {
				participant := f.phoneParticipant("+15550100160", "Tia")
				archive, _, err := f.st.CreatePersonFromParticipantContext(f.t.Context(), participant)
				require.NoError(f.t, err)
				people := f.importCards(f.card("card-tia", "Tia", nil, []string{"+1 555 010 0160"}))
				return archive.ID, people["card-tia"]
			},
		},
		{
			name: "published contact profile", pending: true,
			setup: func(f *contactMatchFixture) (int64, int64) {
				_, archiveID := f.archivePerson("uli@example.test", "Uli")
				card := f.card("card-uli", "Uli", []string{"uli@example.test"}, nil)
				people := f.importCards(card)
				f.publish(people["card-uli"], card)
				return archiveID, people["card-uli"]
			},
		},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			assert := assert.New(t)
			f := newContactMatchFixture(t)
			archiveID, contactID := sc.setup(f)

			result := f.build()
			assert.Zero(result.AutoMerged)
			assert.Zero(result.AutoBound)
			assert.True(f.personExists(contactID), "the contact profile is not absorbed")
			assert.True(f.personExists(archiveID), "the archive person is unchanged")
			merges, err := f.st.ListPersonMergesContext(t.Context(), archiveID)
			require.NoError(t, err)
			for _, merge := range merges {
				assert.NotContains(merge.Merge.Actor, store.ContactMatchAutoActor)
			}
			if sc.pending {
				assert.Positive(result.LeftForReview, "the match waits for a decision")
			} else {
				assert.Zero(result.LeftForReview)
			}
		})
	}
}

// mergeInto merges absorbedID into survivorID by hand.
func (f *contactMatchFixture) mergeInto(survivorID, absorbedID int64) {
	f.t.Helper()
	survivor, err := f.st.GetPersonContext(f.t.Context(), survivorID)
	require.NoError(f.t, err)
	absorbed, err := f.st.GetPersonContext(f.t.Context(), absorbedID)
	require.NoError(f.t, err)
	_, err = f.st.MergePersonsContext(f.t.Context(), store.PersonMergeRequest{
		SurvivorID: survivorID, AbsorbedID: absorbedID,
		ExpectedSurvivorRevision: survivor.Revision, ExpectedAbsorbedRevision: absorbed.Revision,
		IdempotencyKey: "manual-merge", Actor: "user",
	})
	require.NoError(f.t, err)
}

func TestBuildClosesPendingMatchThatIsAlreadyLinked(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	// A phone match waits for review; the user then merges the two people
	// by hand instead of deciding the candidate.
	participant := f.phoneParticipant("+15550100170", "Vic")
	archive, _, err := f.st.CreatePersonFromParticipantContext(t.Context(), participant)
	require.NoError(err)
	people := f.importCards(f.card("card-vic", "Vic", nil, []string{"+1 555 010 0170"}))
	first := f.build()
	require.Equal(1, first.LeftForReview)
	f.mergeInto(archive.ID, people["card-vic"])

	result := f.build()
	assert.Equal(1, result.LinkedClosed)
	assert.Equal(0, result.LeftForReview)
	candidates := f.candidatesFor(participant)
	require.Len(candidates, 1)
	assert.Equal(store.IdentityMatchStateAccepted, candidates[0].State)
	require.NotNil(candidates[0].DecidedBy)
	assert.Equal(store.ContactMatchAutoActor, *candidates[0].DecidedBy)
	require.NotNil(candidates[0].Notes)
	assert.Equal("already linked", *candidates[0].Notes)
	assert.Equal(store.ContactMatchBuildResult{}, f.build())
}

func TestSplitUndoesAutomaticMergeAndIsRemembered(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	participant, archiveID := f.archivePerson("wes@example.test", "Wes")
	card := f.card("card-wes", "Wes Contact", []string{"wes@example.test"}, nil)
	people := f.importCards(card)
	contact, err := f.st.GetPersonContext(t.Context(), people["card-wes"])
	require.NoError(err)
	require.Equal(1, f.build().AutoMerged)

	archive, err := f.st.GetPersonContext(t.Context(), archiveID)
	require.NoError(err)
	merges, err := f.st.ListPersonMergesContext(t.Context(), archiveID)
	require.NoError(err)
	require.Len(merges, 1)
	split, err := f.st.SplitPersonMergeContext(t.Context(), store.PersonSplitRequest{
		SourcePersonID: archiveID, MergeID: merges[0].Merge.ID,
		ExpectedSourceRevision: archive.Revision, IdempotencyKey: "split-wes", Actor: "user",
	})
	require.NoError(err)
	assert.Equal([]int64{participant}, split.SourcePerson.ParticipantIDs)
	assert.Empty(split.NewPerson.ParticipantIDs, "the contact profile comes back on its own")
	resource, err := f.st.GetCardDAVResourceContext(t.Context(), f.book.ID, card.Href)
	require.NoError(err)
	require.NotNil(resource.PersonID)
	assert.Equal(split.NewPerson.ID, *resource.PersonID, "the card follows the restored profile")
	assert.Equal("retired_uid_alias_retargeted", split.UIDAliasDisposition,
		"the contact profile's original UID %s resolves to the restored profile", contact.VCardUID)

	// The split is the user's decision: the rule never merges the two
	// again, and the queue does not ask.
	for range 2 {
		result := f.build()
		assert.Zero(result.AutoMerged)
		assert.Zero(result.LeftForReview)
	}
	assert.True(f.personExists(split.NewPerson.ID))
	candidates := f.candidatesFor(participant)
	rejected := false
	for _, candidate := range candidates {
		if candidate.RightID == split.NewPerson.ID {
			assert.Equal(store.IdentityMatchStateRejected, candidate.State)
			rejected = true
		}
	}
	assert.True(rejected, "the split is recorded against the restored profile")

	// A re-sync of the card stays on the restored profile.
	f.importCards(card)
	resource, err = f.st.GetCardDAVResourceContext(t.Context(), f.book.ID, card.Href)
	require.NoError(err)
	require.NotNil(resource.PersonID)
	assert.Equal(split.NewPerson.ID, *resource.PersonID)
	assert.Zero(f.build().AutoMerged)
}

func TestSplitUndoesAutomaticBindAndIsRemembered(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	participant := f.emailParticipant("xan@example.test", "Xan")
	people := f.importCards(f.card("card-xan", "Xan Contact", []string{"xan@example.test"}, nil))
	contactID := people["card-xan"]
	require.Equal(1, f.build().AutoBound)

	contact, err := f.st.GetPersonContext(t.Context(), contactID)
	require.NoError(err)
	merges, err := f.st.ListPersonMergesContext(t.Context(), contactID)
	require.NoError(err)
	require.Len(merges, 1)
	split, err := f.st.SplitPersonMergeContext(t.Context(), store.PersonSplitRequest{
		SourcePersonID: contactID, MergeID: merges[0].Merge.ID, ParticipantIDs: []int64{participant},
		ExpectedSourceRevision: contact.Revision, IdempotencyKey: "split-xan", Actor: "user",
	})
	require.NoError(err)
	assert.Empty(split.SourcePerson.ParticipantIDs)

	result := f.build()
	assert.Zero(result.AutoBound)
	assert.Zero(result.AutoMerged, "the identity's new profile is not merged back either")
	assert.Zero(result.LeftForReview)
	contact, err = f.st.GetPersonContext(t.Context(), contactID)
	require.NoError(err)
	assert.Empty(contact.ParticipantIDs)
}
