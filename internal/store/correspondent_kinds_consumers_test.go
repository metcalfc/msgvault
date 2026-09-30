package store_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/personenrichment"
	"go.kenn.io/msgvault/internal/store"
)

func TestContactMatchesSkipClustersThatAreNotAPerson(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	desk := f.emailParticipant("desk@example.test", "Example Desk")
	alias := f.emailParticipant("desk-team@example.test", "")
	_, err := f.st.LinkParticipants(desk, alias)
	require.NoError(err)
	_, err = f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: desk, Kind: correspondentkind.Ignored,
	})
	require.NoError(err)
	// A member linked after classification is covered by the cluster.
	late := f.emailParticipant("desk-late@example.test", "")
	_, err = f.st.LinkParticipants(alias, late)
	require.NoError(err)
	f.importCards(f.card("card-desk", "Desk Contact", []string{"desk-late@example.test"}, nil))

	matches, err := f.st.FindContactMatchesContext(t.Context())
	require.NoError(err)
	assert.Empty(matches)
	result, err := f.st.BuildContactMatchCandidatesContext(t.Context())
	require.NoError(err)
	assert.Equal(0, result.Created)
}

func TestAcceptContactMatchRefusesAClusterMarkedNotAPerson(t *testing.T) {
	require := require.New(t)
	f := newContactMatchFixture(t)

	participant := f.emailParticipant("sam@example.test", "Sam")
	people := f.importCards(f.card("card-sam", "Sam Contact", []string{"sam@example.test"}, nil))
	candidate := f.buildCandidate(people["card-sam"])
	// Classify through a raw row so the open candidate survives, as it would
	// for a member linked in after the classification.
	_, err := f.st.DB().ExecContext(t.Context(), f.st.Rebind(`INSERT INTO correspondent_kinds
		(participant_id, source, kind) VALUES (?, 'user', 'ignored')`), participant)
	require.NoError(err)

	_, _, err = f.st.AcceptIdentityMatchCandidateContext(t.Context(), candidate.ID, "user", nil)
	require.ErrorIs(err, store.ErrContactMatchNotAPerson)
}

func TestDirectoryHidesPeopleWhoseIdentitiesAreNotPeople(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	newPerson := func(email, name string) (int64, int64) {
		participant := f.emailParticipant(email, name)
		person, _, err := f.st.CreatePersonFromParticipantContext(t.Context(), participant)
		require.NoError(err)
		return participant, person.ID
	}
	shopParticipant, shop := newPerson("orders@shop.example.test", "Example Shop")
	deskParticipant, desk := newPerson("desk@example.test", "Example Desk")
	_, human := newPerson("casey@example.test", "Casey Example")
	for participant, kind := range map[int64]correspondentkind.Kind{
		shopParticipant: correspondentkind.Organization,
		deskParticipant: correspondentkind.SharedMailbox,
	} {
		_, err := f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
			ParticipantID: participant, Kind: kind,
		})
		require.NoError(err)
	}

	ids := func(mode string) []int64 {
		page, err := f.st.DirectoryPeoplePageContext(t.Context(), store.DirectoryPeopleQuery{NotPeople: mode})
		require.NoError(err)
		result := []int64{}
		for _, person := range page.People {
			result = append(result, person.ID)
		}
		return result
	}
	assert.ElementsMatch([]int64{desk, human}, ids(""), "a shared mailbox's profile stays listed")
	assert.ElementsMatch([]int64{shop}, ids(store.DirectoryNotPeopleOnly))
	assert.ElementsMatch([]int64{shop, desk, human}, ids(store.DirectoryNotPeopleInclude))

	_, err := f.st.DirectoryPeoplePageContext(t.Context(), store.DirectoryPeopleQuery{NotPeople: "all"})
	require.ErrorIs(err, store.ErrInvalidDirectoryQuery)
}

func TestEnrichmentLeavesOutIdentitiesThatAreNotAPerson(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newEnrichmentWorkFixture(t)

	// The profile also lists a support desk address that other people use.
	_, err := f.store.AddPersonContactPointContext(t.Context(), f.person.ID, store.PersonContactPointInput{
		AddressKind: store.ContactAddressEmail, OriginalValue: "desk@example.com",
		Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceUser},
	})
	require.NoError(err)
	desk, err := f.store.EnsureParticipant("desk@example.com", "Example Desk", "example.com")
	require.NoError(err)
	_, err = f.store.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: desk, Kind: correspondentkind.SharedMailbox,
	})
	require.NoError(err)

	lease := personenrichment.WorkLease{
		PersonID: f.person.ID,
		Trigger:  personenrichment.Trigger{Kind: personenrichment.TriggerManual, Generation: "manual:kinds"},
	}
	input, err := f.store.LoadRequestInput(t.Context(), lease)
	require.NoError(err)
	assert.False(input.NotAPerson)
	emails := []string{}
	for _, email := range input.Emails {
		emails = append(emails, email.Value)
	}
	assert.Contains(emails, "work-person@example.com")
	assert.NotContains(emails, "desk@example.com")

	// Once the profile's own identity is ignored, the work is skipped.
	_, err = f.store.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: f.person.ParticipantIDs[0], Kind: correspondentkind.Ignored,
	})
	require.NoError(err)
	run := f.startRun(t, "not-a-person")
	f.enqueue(t)
	claimed := f.claim(t, run.ID, "worker")
	input, err = f.store.LoadRequestInput(t.Context(), *claimed)
	require.NoError(err)
	assert.True(input.NotAPerson)
	assert.Empty(input.Emails)
	require.NoError(f.store.ReleaseWork(t.Context(), claimed.Token, personenrichment.WorkRelease{
		Outcome: personenrichment.WorkOutcomeNotAPerson, PersonRevision: input.PersonRevision,
	}))
	assert.Empty(f.work(t))
	attempts, err := f.store.ListPersonEnrichmentAttemptsContext(t.Context(), store.PersonEnrichmentAttemptFilter{
		PersonID: f.person.ID, Limit: 10,
	})
	require.NoError(err)
	assert.Empty(attempts, "skipping records no attempt")
}

func TestDirectoryCursorRestartsWhenItsAnchorBecomesNotAPerson(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	participants := map[string]int64{}
	for _, name := range []string{"Aria Example", "Bram Example", "Cleo Example"} {
		participant := f.emailParticipant(strings.ToLower(strings.Fields(name)[0])+"@example.test", name)
		_, _, err := f.st.CreatePersonFromParticipantContext(t.Context(), participant)
		require.NoError(err)
		participants[name] = participant
	}
	first, err := f.st.DirectoryPeoplePageContext(t.Context(), store.DirectoryPeopleQuery{Sort: "name", Limit: 1})
	require.NoError(err)
	require.Len(first.People, 1)
	require.NotEmpty(first.NextCursor)
	require.Equal("Aria Example", *first.People[0].DisplayName)

	// Marking a person after the anchor only removes them from later pages.
	_, err = f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: participants["Bram Example"], Kind: correspondentkind.Organization,
	})
	require.NoError(err)
	second, err := f.st.DirectoryPeoplePageContext(t.Context(), store.DirectoryPeopleQuery{
		Sort: "name", Limit: 1, Cursor: first.NextCursor,
	})
	require.NoError(err)
	require.Len(second.People, 1)
	assert.Equal("Cleo Example", *second.People[0].DisplayName)

	// Marking the cursor's own anchor makes the cursor invalid rather than
	// silently skipping or repeating people; the client restarts.
	_, err = f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: participants["Aria Example"], Kind: correspondentkind.Ignored,
	})
	require.NoError(err)
	_, err = f.st.DirectoryPeoplePageContext(t.Context(), store.DirectoryPeopleQuery{
		Sort: "name", Limit: 1, Cursor: first.NextCursor,
	})
	assert.ErrorIs(err, store.ErrInvalidDirectoryCursor)
}

func TestUnlinkingAClassifiedClusterKeepsEachHalfClassified(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	first := f.emailParticipant("desk-one@example.test", "Desk")
	second := f.emailParticipant("desk-two@example.test", "Desk")
	_, err := f.st.LinkParticipants(first, second)
	require.NoError(err)
	_, err = f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: first, Kind: correspondentkind.Ignored,
	})
	require.NoError(err)
	_, err = f.st.UnlinkParticipants(first, second)
	require.NoError(err)

	// Both halves were classified as one record; each keeps the choice.
	hidden, err := f.st.NotPersonParticipantsContext(t.Context())
	require.NoError(err)
	assert.Equal(map[int64]correspondentkind.Kind{
		first: correspondentkind.Ignored, second: correspondentkind.Ignored,
	}, hidden)
	records, err := f.st.ListCorrespondentKindsContext(t.Context(), store.CorrespondentKindListFilter{})
	require.NoError(err)
	assert.Len(records, 2, "each half is listed and can be restored on its own")

	// Restoring one half leaves the other alone.
	_, err = f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: second, Kind: correspondentkind.Person,
	})
	require.NoError(err)
	hidden, err = f.st.NotPersonParticipantsContext(t.Context())
	require.NoError(err)
	assert.Equal(map[int64]correspondentkind.Kind{first: correspondentkind.Ignored}, hidden)
}
