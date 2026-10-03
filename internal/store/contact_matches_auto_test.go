package store_test

import (
	"errors"
	"fmt"
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
	require.Len(result.Actions, 1)
	assert.Equal(store.ContactMatchBuildResult{
		Matches: 1, Created: 1, EvidenceAdded: 1, Merge: 1, AutoMerged: 1,
		Actions: []store.ContactMatchAutoAction{{
			Action: store.ContactMatchAutoActionMerge, CandidateID: result.Actions[0].CandidateID,
			ContactPersonID: contactID, PersonID: archiveID, ParticipantID: participant,
		}},
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
			name: "family cards sharing one email", pending: true,
			setup: func(f *contactMatchFixture) (int64, int64) {
				_, archiveID := f.archivePerson("robin.lee@example.test", "Robin Lee")
				people := f.importCards(
					f.card("card-robin", "Robin Lee", []string{"robin.lee@example.test"}, nil),
					f.card("card-sam", "Sam Lee", []string{"robin.lee@example.test"}, nil),
				)
				require.Equal(f.t, people["card-robin"], people["card-sam"],
					"import binds the second card to the profile that has the address")
				return archiveID, people["card-robin"]
			},
		},
		{
			name: "contact names two different people", pending: true,
			setup: func(f *contactMatchFixture) (int64, int64) {
				_, archiveID := f.archivePerson("jo.ng@example.test", "Jo Ng")
				people := f.importCards(f.card("card-jo", "Jo Ng", []string{"jo.ng@example.test"}, nil))
				other := "Kai Ng"
				_, err := f.st.AddPersonNameContext(f.t.Context(), people["card-jo"], store.PersonNameInput{
					NameKind: store.PersonNameFormatted, Formatted: &other, OriginalValue: other,
					Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceUser},
				})
				require.NoError(f.t, err)
				return archiveID, people["card-jo"]
			},
		},
		{
			name: "split before contact matching remembered splits", pending: true,
			setup: func(f *contactMatchFixture) (int64, int64) {
				f.st.SetContactMatchAutoResolve(false)
				_, archiveID := f.archivePerson("lee@example.test", "Lee")
				people := f.importCards(f.card("card-lee", "Lee", []string{"lee@example.test"}, nil))
				f.mergeInto(archiveID, people["card-lee"])
				archive, err := f.st.GetPersonContext(f.t.Context(), archiveID)
				require.NoError(f.t, err)
				merges, err := f.st.ListPersonMergesContext(f.t.Context(), archiveID)
				require.NoError(f.t, err)
				require.Len(f.t, merges, 1)
				split, err := f.st.SplitPersonMergeContext(f.t.Context(), store.PersonSplitRequest{
					SourcePersonID: archiveID, MergeID: merges[0].Merge.ID,
					ExpectedSourceRevision: archive.Revision, IdempotencyKey: "old-split", Actor: "user",
				})
				require.NoError(f.t, err)
				// An older release recorded no contact-match rejection.
				_, err = f.st.DB().Exec(`DELETE FROM identity_match_candidates
					WHERE left_kind = 'participant' AND right_kind = 'person'`)
				require.NoError(f.t, err)
				f.st.SetContactMatchAutoResolve(true)
				return archiveID, split.NewPerson.ID
			},
		},
		{
			name: "contact email also hits an owner identity", pending: true,
			setup: func(f *contactMatchFixture) (int64, int64) {
				source, err := f.st.GetOrCreateSource("gmail", "own@example.test")
				require.NoError(f.t, err)
				f.emailParticipant("own@example.test", "Me")
				require.NoError(f.t, f.st.AddAccountIdentityContext(f.t.Context(), source.ID, "own@example.test", "manual"))
				_, archiveID := f.archivePerson("val@example.test", "Val")
				people := f.importCards(f.card("card-val", "Val",
					[]string{"val@example.test", "own@example.test"}, nil))
				return archiveID, people["card-val"]
			},
		},
		{
			name: "contact phone belongs to another person", pending: true,
			setup: func(f *contactMatchFixture) (int64, int64) {
				_, archiveID := f.archivePerson("kim@example.test", "Kim")
				partner := f.phoneParticipant("+15550100161", "Pat")
				_, _, err := f.st.CreatePersonFromParticipantContext(f.t.Context(), partner)
				require.NoError(f.t, err)
				people := f.importCards(f.card("card-kim", "Kim",
					[]string{"kim@example.test"}, []string{"+1 555 010 0161"}))
				return archiveID, people["card-kim"]
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

func TestBuildDryRunReportsDecisionsWithoutWriting(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	participant, archiveID := f.archivePerson("yan@example.test", "Yan")
	people := f.importCards(f.card("card-yan", "Yan", []string{"yan@example.test"}, nil))
	f.phoneParticipant("+15550100162", "Zed")
	f.importCards(
		f.card("card-yan", "Yan", []string{"yan@example.test"}, nil),
		f.card("card-zed", "Zed", nil, []string{"+1 555 010 0162"}),
	)

	planned, err := f.st.BuildContactMatchCandidatesWithOptionsContext(t.Context(),
		store.ContactMatchBuildOptions{DryRun: true})
	require.NoError(err)
	assert.True(planned.DryRun)
	assert.Equal(1, planned.AutoMerged)
	assert.Equal(1, planned.LeftForReview)
	assert.Equal([]store.ContactMatchAutoAction{{
		Action: store.ContactMatchAutoActionMerge, ContactPersonID: people["card-yan"],
		PersonID: archiveID, ParticipantID: participant,
	}}, planned.Actions)
	candidates, err := f.st.ListContactMatchCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	assert.Empty(candidates, "a dry run writes no candidate")
	assert.True(f.personExists(people["card-yan"]), "a dry run merges nothing")

	applied := f.build()
	assert.Equal(planned.AutoMerged, applied.AutoMerged)
	assert.Equal(planned.LeftForReview, applied.LeftForReview)
	assert.False(f.personExists(people["card-yan"]))
}

func TestBuildOnlyProposesWhenAutoResolveIsOff(t *testing.T) {
	assert := assert.New(t)
	f := newContactMatchFixture(t)
	f.st.SetContactMatchAutoResolve(false)

	_, archiveID := f.archivePerson("amy@example.test", "Amy")
	people := f.importCards(f.card("card-amy", "Amy", []string{"amy@example.test"}, nil))

	result := f.build()
	assert.Zero(result.AutoMerged)
	assert.Equal(1, result.LeftForReview)
	assert.True(f.personExists(people["card-amy"]))
	assert.True(f.personExists(archiveID))
}

func TestBuildResolvesManyMatchesInBatches(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	cards := []store.CardDAVRemoteResource{}
	archives := []int64{}
	for i := range 30 {
		email := fmt.Sprintf("batch%02d@example.test", i)
		_, archiveID := f.archivePerson(email, fmt.Sprintf("Batch %02d", i))
		archives = append(archives, archiveID)
		cards = append(cards, f.card(fmt.Sprintf("card-batch-%02d", i), fmt.Sprintf("Batch %02d", i),
			[]string{email}, nil))
	}
	f.importCards(cards...)

	result := f.build()
	assert.Equal(30, result.AutoMerged, "more than one batch is applied")
	assert.Zero(result.LeftForReview)
	for _, archiveID := range archives {
		merges, err := f.st.ListPersonMergesContext(t.Context(), archiveID)
		require.NoError(err)
		assert.Len(merges, 1)
	}
}

// seededArchivePerson promotes a cluster of two identities whose headers
// use different names, so promotion records the name it chose as the seed.
func (f *contactMatchFixture) seededArchivePerson(email, name, otherEmail, otherName string) int64 {
	f.t.Helper()
	first := f.emailParticipant(email, name)
	second := f.emailParticipant(otherEmail, otherName)
	_, err := f.st.LinkParticipants(first, second)
	require.NoError(f.t, err)
	person, _, err := f.st.CreatePersonFromParticipantContext(f.t.Context(), first)
	require.NoError(f.t, err)
	return person.ID
}

func (f *contactMatchFixture) displayName(personID int64) string {
	f.t.Helper()
	person, err := f.st.GetPersonContext(f.t.Context(), personID)
	require.NoError(f.t, err)
	require.NotNil(f.t, person.DisplayName)
	return *person.DisplayName
}

func TestAutomaticMergeNamesSurvivorAfterTheCard(t *testing.T) {
	t.Run("name promotion chose", func(t *testing.T) {
		f := newContactMatchFixture(t)
		archiveID := f.seededArchivePerson("m.orsolini@example.test", "M. Orsolini",
			"matt.o@example.test", "Matt O.")
		f.importCards(f.card("card-mo", "Matt Orsolini", []string{"m.orsolini@example.test"}, nil))
		require.Equal(t, 1, f.build().AutoMerged)
		assert.Equal(t, "Matt Orsolini", f.displayName(archiveID))
	})

	t.Run("header variant the user picked", func(t *testing.T) {
		f := newContactMatchFixture(t)
		archiveID := f.seededArchivePerson("ann@example.test", "Ann Lee",
			"ann.lee@example.test", "Annie Lee")
		archive, err := f.st.GetPersonContext(t.Context(), archiveID)
		require.NoError(t, err)
		require.NotNil(t, archive.DisplayName)
		picked := "Ann Lee"
		if *archive.DisplayName == picked {
			picked = "Annie Lee"
		}
		_, err = f.st.UpdatePersonDisplayNameContext(t.Context(), archiveID, archive.Revision, &picked)
		require.NoError(t, err)
		f.importCards(f.card("card-ann", "Ann B. Lee", []string{"ann@example.test"}, nil))
		require.Equal(t, 1, f.build().AutoMerged)
		assert.Equal(t, picked, f.displayName(archiveID), "a header name the user picked is kept")
	})

	t.Run("user-set name", func(t *testing.T) {
		f := newContactMatchFixture(t)
		_, archiveID := f.archivePerson("n.orsolini@example.test", "N. Orsolini")
		archive, err := f.st.GetPersonContext(t.Context(), archiveID)
		require.NoError(t, err)
		chosen := "Nico"
		_, err = f.st.UpdatePersonDisplayNameContext(t.Context(), archiveID, archive.Revision, &chosen)
		require.NoError(t, err)
		f.importCards(f.card("card-no", "Nicola Orsolini", []string{"n.orsolini@example.test"}, nil))
		require.Equal(t, 1, f.build().AutoMerged)
		assert.Equal(t, "Nico", f.displayName(archiveID), "a name the user set is kept")
	})

	t.Run("single header name", func(t *testing.T) {
		f := newContactMatchFixture(t)
		_, archiveID := f.archivePerson("p.orsolini@example.test", "P. Orsolini")
		f.importCards(f.card("card-po", "Paola Orsolini", []string{"p.orsolini@example.test"}, nil))
		require.Equal(t, 1, f.build().AutoMerged)
		assert.Equal(t, "P. Orsolini", f.displayName(archiveID),
			"without a promotion seed nothing proves the user never chose the name")
	})
}

func TestBuildComparesCardAndArchiveNames(t *testing.T) {
	tests := []struct {
		name        string
		archiveName string
		cardName    string
		// bind leaves the identity without a person, so the card is
		// compared with its header name only.
		bind   bool
		merged bool
	}{
		{name: "same name", archiveName: "Matt Orsolini", cardName: "Matt Orsolini", merged: true},
		{name: "short form and order", archiveName: "Bob Smith", cardName: "Smith, Bob", merged: true},
		{name: "first name only", archiveName: "Bob Smith", cardName: "Bob", merged: true},
		{name: "nickname that is not a prefix", archiveName: "Robert Smith", cardName: "Bob Smith"},
		{name: "different person", archiveName: "Jane Doe", cardName: "John Doe"},
		{name: "bind same name", archiveName: "Matt Orsolini", cardName: "Matt Orsolini", bind: true, merged: true},
		{name: "bind different person", archiveName: "Jane Doe", cardName: "John Doe", bind: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newContactMatchFixture(t)
			const email = "p1@example.test"
			if tt.bind {
				f.emailParticipant(email, tt.archiveName)
			} else {
				f.archivePerson(email, tt.archiveName)
			}
			people := f.importCards(f.card("card-p1", tt.cardName, []string{email}, nil))

			result := f.build()
			decided := result.AutoMerged + result.AutoBound
			if tt.merged {
				assert.Equal(t, 1, decided)
				assert.Zero(t, result.LeftForReview)
				return
			}
			assert.Zero(t, decided)
			assert.Equal(t, 1, result.LeftForReview, "the match waits for a decision")
			assert.True(t, f.personExists(people["card-p1"]))
		})
	}
}

func TestBuildLeavesCardNamingAnotherPersonForReview(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	// Jane's profile holds her own address and the family address; John's
	// card lists only the family address.
	jane := f.emailParticipant("jane@example.test", "Jane Doe")
	family := f.emailParticipant("family@example.test", "")
	_, err := f.st.LinkParticipants(jane, family)
	require.NoError(err)
	archive, _, err := f.st.CreatePersonFromParticipantContext(t.Context(), jane)
	require.NoError(err)
	people := f.importCards(f.card("card-john", "John Doe", []string{"family@example.test"}, nil))

	result := f.build()
	assert.Zero(result.AutoMerged)
	assert.Equal(1, result.LeftForReview)
	assert.True(f.personExists(people["card-john"]), "John's card is not merged into Jane")
	assert.Equal("Jane Doe", f.displayName(archive.ID), "Jane keeps her name")
}

func TestApplyRecountsMatchesChangedAfterPlanning(t *testing.T) {
	tests := []struct {
		name string
		// change runs between planning and applying.
		change func(f *contactMatchFixture, contactID, otherID int64)
	}{
		{
			name: "a second contact lists the address",
			change: func(f *contactMatchFixture, _, otherID int64) {
				_, err := f.st.AddPersonContactPointContext(f.t.Context(), otherID, store.PersonContactPointInput{
					AddressKind: store.ContactAddressEmail, OriginalValue: "kim@example.test",
					Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceUser},
				})
				require.NoError(f.t, err)
			},
		},
		{
			name: "the contact gains another person's phone",
			change: func(f *contactMatchFixture, contactID, _ int64) {
				_, err := f.st.AddPersonContactPointContext(f.t.Context(), contactID, store.PersonContactPointInput{
					AddressKind: store.ContactAddressPhone, OriginalValue: "+1 555 010 0181",
					Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceUser},
				})
				require.NoError(f.t, err)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			f := newContactMatchFixture(t)
			_, archiveID := f.archivePerson("kim@example.test", "Kim Park")
			partner := f.phoneParticipant("+15550100181", "Pat Park")
			_, _, err := f.st.CreatePersonFromParticipantContext(t.Context(), partner)
			require.NoError(t, err)
			people := f.importCards(
				f.card("card-kim", "Kim Park", []string{"kim@example.test"}, nil),
				f.card("card-kim-old", "Kim Park", []string{"kim.old@example.test"}, nil),
			)
			changed := false
			restore := f.st.SetContactMatchAutoBeforeApplyHookForTest(func() {
				changed = true
				tt.change(f, people["card-kim"], people["card-kim-old"])
			})
			defer restore()

			result := f.build()
			require.True(t, changed, "the change lands between planning and applying")
			assert.Zero(result.AutoMerged)
			assert.Positive(result.LeftForReview)
			assert.True(f.personExists(people["card-kim"]), "the contact profile is not absorbed")
			assert.True(f.personExists(archiveID))
		})
	}
}

func TestBuildDryRunMatchesTheRealRun(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	// A phone match waits for review, then the user merges the two by hand,
	// so the refresh closes it as linked.
	vic := f.phoneParticipant("+15550100190", "Vic Ray")
	vicPerson, _, err := f.st.CreatePersonFromParticipantContext(t.Context(), vic)
	require.NoError(err)
	// A second phone match waits for review, then its identity becomes one
	// of the owner's, so the refresh retires it.
	own := f.emailParticipant("own@example.test", "Me")
	_, err = f.st.LinkParticipants(own, f.phoneParticipant("+15550100191", "Me"))
	require.NoError(err)
	cards := []store.CardDAVRemoteResource{
		f.card("card-vic", "Vic Ray", nil, []string{"+1 555 010 0190"}),
		f.card("card-me", "Me", nil, []string{"+1 555 010 0191"}),
	}
	people := f.importCards(cards...)
	require.Equal(2, f.build().LeftForReview)
	f.mergeInto(vicPerson.ID, people["card-vic"])
	source, err := f.st.GetOrCreateSource("gmail", "own@example.test")
	require.NoError(err)
	require.NoError(f.st.AddAccountIdentityContext(t.Context(), source.ID, "own@example.test", "manual"))
	// New exact matches: one merges, one names another person.
	_, yanID := f.archivePerson("yan@example.test", "Yan Wu")
	f.archivePerson("zoe@example.test", "Zoe Li")
	people = f.importCards(append(cards,
		f.card("card-yan", "Yan Wu", []string{"yan@example.test"}, nil),
		f.card("card-zoe", "Zara Li", []string{"zoe@example.test"}, nil),
	)...)

	before, err := f.st.ListContactMatchCandidatesContext(t.Context(), nil, 500, 0)
	require.NoError(err)
	existing := map[int64]bool{}
	for _, candidate := range before {
		existing[candidate.ID] = true
	}
	planned, err := f.st.BuildContactMatchCandidatesWithOptionsContext(t.Context(),
		store.ContactMatchBuildOptions{DryRun: true})
	require.NoError(err)
	after, err := f.st.ListContactMatchCandidatesContext(t.Context(), nil, 500, 0)
	require.NoError(err)
	assert.Equal(before, after, "a dry run writes nothing")
	assert.True(f.personExists(people["card-yan"]), "a dry run merges nothing")

	applied := f.build()
	require.Equal(1, applied.Retired)
	require.Equal(1, applied.LinkedClosed)
	require.Equal(1, applied.AutoMerged)
	require.Equal(1, applied.LeftForReview)
	assert.False(f.personExists(people["card-yan"]))
	assert.True(f.personExists(yanID))

	want := applied
	want.DryRun = true
	want.Actions = []store.ContactMatchAutoAction{}
	for _, action := range applied.Actions {
		if !existing[action.CandidateID] {
			action.CandidateID = 0
		}
		want.Actions = append(want.Actions, action)
	}
	assert.Equal(want, *planned)
}

func TestAutoResolveRefusalLeavesReviewAndFaultFailsBuild(t *testing.T) {
	t.Run("expected refusal", func(t *testing.T) {
		assert := assert.New(t)
		f := newContactMatchFixture(t)
		_, archiveID := f.archivePerson("ren@example.test", "Ren")
		people := f.importCards(f.card("card-ren", "Ren", []string{"ren@example.test"}, nil))
		restore := f.st.SetContactMatchAutoResolveHookForTest(func() error {
			return store.ErrPersonRevisionConflict
		})
		defer restore()

		result := f.build()
		assert.Zero(result.AutoMerged)
		assert.Equal(1, result.LeftForReview)
		assert.True(f.personExists(people["card-ren"]))
		assert.True(f.personExists(archiveID))
	})

	t.Run("enrichment dispatch in progress", func(t *testing.T) {
		assert := assert.New(t)
		f := newContactMatchFixture(t)
		_, archiveID := f.archivePerson("tam@example.test", "Tam")
		people := f.importCards(f.card("card-tam", "Tam", []string{"tam@example.test"}, nil))
		restore := f.st.SetContactMatchAutoResolveHookForTest(func() error {
			return fmt.Errorf("merge: %w", store.ErrPersonEnrichmentDispatchInProgress)
		})

		result := f.build()
		restore()
		assert.Zero(result.AutoMerged)
		assert.Equal(1, result.LeftForReview, "the match waits for the next run")
		assert.True(f.personExists(people["card-tam"]))

		assert.Equal(1, f.build().AutoMerged, "the next run merges once the dispatch ends")
		assert.False(f.personExists(people["card-tam"]))
		assert.True(f.personExists(archiveID))
	})

	t.Run("fault", func(t *testing.T) {
		assert := assert.New(t)
		f := newContactMatchFixture(t)
		f.archivePerson("sol@example.test", "Sol")
		people := f.importCards(f.card("card-sol", "Sol", []string{"sol@example.test"}, nil))
		fault := errors.New("synthetic storage fault")
		restore := f.st.SetContactMatchAutoResolveHookForTest(func() error { return fault })
		defer restore()

		_, err := f.st.BuildContactMatchCandidatesContext(t.Context())
		require.ErrorIs(t, err, fault)
		assert.True(f.personExists(people["card-sol"]))
	})
}
