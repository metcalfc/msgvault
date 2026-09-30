package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/store"
)

func candidateState(t *testing.T, st *store.Store, id int64) (store.IdentityMatchState, *string) {
	t.Helper()
	candidate, err := st.GetIdentityMatchCandidateContext(t.Context(), id)
	require.NoError(t, err)
	return candidate.State, candidate.Notes
}

func TestSetCorrespondentKindClassifiesTheWholeCluster(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	first := f.emailParticipant("desk@example.test", "Help Desk")
	second := f.emailParticipant("desk-alias@example.test", "")
	_, err := f.st.LinkParticipants(first, second)
	require.NoError(err)

	result, err := f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: second, Kind: correspondentkind.Ignored,
	})
	require.NoError(err)
	assert.Equal(correspondentkind.Ignored, result.Record.Kind)
	require.NotNil(result.Record.Source)
	assert.Equal(correspondentkind.SourceUser, *result.Record.Source)
	assert.Equal(first, result.Record.CanonicalID)
	assert.Equal([]int64{first, second}, result.Record.MemberIDs)
	require.NotNil(result.Record.DisplayName)
	assert.Equal("Help Desk", *result.Record.DisplayName)
	assert.Equal([]string{"desk@example.test", "desk-alias@example.test"}, result.Record.Addresses)

	// A participant linked into the cluster later is covered without a row.
	third := f.emailParticipant("desk-third@example.test", "")
	_, err = f.st.LinkParticipants(second, third)
	require.NoError(err)
	hidden, err := f.st.NotPersonParticipantsContext(t.Context())
	require.NoError(err)
	assert.Equal(map[int64]correspondentkind.Kind{
		first: correspondentkind.Ignored, second: correspondentkind.Ignored, third: correspondentkind.Ignored,
	}, hidden)

	kinds, err := f.st.CorrespondentKindsForParticipantsContext(t.Context(), []int64{third})
	require.NoError(err)
	assert.Equal(correspondentkind.Ignored, kinds[third].Kind)

	records, err := f.st.ListCorrespondentKindsContext(t.Context(), store.CorrespondentKindListFilter{})
	require.NoError(err)
	require.Len(records, 1)
	assert.Equal([]int64{first, second, third}, records[0].MemberIDs)

	// "This is a person" clears the classification for every member.
	result, err = f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: third, Kind: correspondentkind.Person,
	})
	require.NoError(err)
	assert.Equal(correspondentkind.Person, result.Record.Kind)
	require.NotNil(result.Record.Source, "an explicit person override keeps its provenance")

	unclassified := f.emailParticipant("new@example.test", "New Person")
	record, err := f.st.GetCorrespondentKindContext(t.Context(), unclassified)
	require.NoError(err)
	assert.Equal(correspondentkind.Person, record.Kind)
	assert.Nil(record.Source)
	assert.Nil(record.ClassifiedAt)
	hidden, err = f.st.NotPersonParticipantsContext(t.Context())
	require.NoError(err)
	assert.Empty(hidden)
	records, err = f.st.ListCorrespondentKindsContext(t.Context(), store.CorrespondentKindListFilter{})
	require.NoError(err)
	assert.Empty(records)
}

func TestSetCorrespondentKindResolvesAndRestoresOpenCandidates(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	participant := f.emailParticipant("help@example.test", "Avery Example")
	other := f.emailParticipant("avery@example.test", "Avery Example")
	people := f.importCards(f.card("card-avery", "Avery Example", []string{"help@example.test"}, nil))
	averyCandidate := f.buildCandidate(people["card-avery"])
	name := "avery example"
	blakeCandidate, _, err := f.st.UpsertIdentityMatchCandidateContext(t.Context(),
		store.IdentityMatchCandidateInput{
			LeftKind: store.IdentityMatchParticipant, LeftID: participant,
			RightKind: store.IdentityMatchParticipant, RightID: other,
			Basis: store.IdentityMatchDisplayName, NormalizedValue: &name,
			State: store.IdentityMatchStateCandidate, Source: store.ProvenanceSystem,
		})
	require.NoError(err)

	result, err := f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: participant, Kind: correspondentkind.SharedMailbox,
	})
	require.NoError(err)
	assert.Equal(2, result.ResolvedCandidates)
	for _, id := range []int64{averyCandidate.ID, blakeCandidate.ID} {
		state, notes := candidateState(t, f.st, id)
		assert.Equal(store.IdentityMatchStateRejected, state)
		require.NotNil(notes)
		assert.Equal(correspondentkind.NotAPersonReason, *notes)
	}

	// A rebuild does not propose the pair again.
	_, err = f.st.BuildContactMatchCandidatesContext(t.Context())
	require.NoError(err)
	open, err := f.st.ListContactMatchCandidatesContext(t.Context(),
		[]store.IdentityMatchState{store.IdentityMatchStateCandidate}, 500, 0)
	require.NoError(err)
	assert.Empty(open)

	result, err = f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: participant, Kind: correspondentkind.Person,
	})
	require.NoError(err)
	assert.Equal(2, result.RestoredCandidates)
	state, notes := candidateState(t, f.st, averyCandidate.ID)
	assert.Equal(store.IdentityMatchStateCandidate, state)
	assert.Nil(notes)
}

func TestGeneratedCandidateForClassifiedIdentityIsRecordedResolved(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	left := f.emailParticipant("left@example.test", "Left")
	right := f.emailParticipant("right@example.test", "Right")
	_, err := f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: right, Kind: correspondentkind.Ignored,
	})
	require.NoError(err)

	value := "shared-name"
	candidate, created, err := f.st.UpsertIdentityMatchCandidateContext(t.Context(),
		store.IdentityMatchCandidateInput{
			LeftKind: store.IdentityMatchParticipant, LeftID: left,
			RightKind: store.IdentityMatchParticipant, RightID: right,
			Basis: store.IdentityMatchDisplayName, NormalizedValue: &value,
			State: store.IdentityMatchStateCandidate, Source: store.ProvenanceSystem,
		})
	require.NoError(err)
	assert.True(created)
	assert.Equal(store.IdentityMatchStateRejected, candidate.State)
	require.NotNil(candidate.Notes)
	assert.Equal(correspondentkind.NotAPersonReason, *candidate.Notes)

	// Clearing the classification returns it to review.
	_, err = f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: right, Kind: correspondentkind.Person,
	})
	require.NoError(err)
	state, _ := candidateState(t, f.st, candidate.ID)
	assert.Equal(store.IdentityMatchStateCandidate, state)
}

func TestClearingACorrespondentKindReturnsConflictsToConflict(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	left := f.emailParticipant("left-conflict@example.test", "Left")
	right := f.emailParticipant("right-conflict@example.test", "Right")
	value := "shared-handle"
	conflict, _, err := f.st.UpsertIdentityMatchCandidateContext(t.Context(),
		store.IdentityMatchCandidateInput{
			LeftKind: store.IdentityMatchParticipant, LeftID: left,
			RightKind: store.IdentityMatchParticipant, RightID: right,
			Basis: store.IdentityMatchDisplayName, NormalizedValue: &value,
			State: store.IdentityMatchStateConflict, Source: store.ProvenanceSystem,
		})
	require.NoError(err)
	require.Equal(store.IdentityMatchStateConflict, conflict.State)

	_, err = f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: left, Kind: correspondentkind.Ignored,
	})
	require.NoError(err)
	state, notes := candidateState(t, f.st, conflict.ID)
	assert.Equal(store.IdentityMatchStateRejected, state)
	require.NotNil(notes)
	assert.Equal(correspondentkind.NotAPersonConflictReason, *notes)

	_, err = f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: left, Kind: correspondentkind.Person,
	})
	require.NoError(err)
	state, _ = candidateState(t, f.st, conflict.ID)
	assert.Equal(store.IdentityMatchStateConflict, state)
}

func TestSetCorrespondentKindOrganizationFindsOrCreatesTheOrganization(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	orders := f.emailParticipant("orders@shop.example.test", "Example Shop")
	result, err := f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: orders, Kind: correspondentkind.Organization,
	})
	require.NoError(err)
	assert.True(result.OrganizationCreated)
	require.NotNil(result.Record.OrganizationID)
	require.NotNil(result.Record.OrganizationName)
	assert.Equal("Example Shop", *result.Record.OrganizationName)
	organization, err := f.st.GetOrganizationContext(t.Context(), *result.Record.OrganizationID)
	require.NoError(err)
	require.NotNil(organization.PrimaryDomain)
	assert.Equal("shop.example.test", *organization.PrimaryDomain)
	profile, err := f.st.GetOrganizationProfileContext(t.Context(), organization.ID, false)
	require.NoError(err)
	require.Len(profile.ContactPoints, 1)
	assert.Equal("orders@shop.example.test", profile.ContactPoints[0].NormalizedValue)
	assert.Equal(store.ProvenanceUser, profile.ContactPoints[0].Envelope.Source)

	// A second address named for the same organization joins it.
	billing := f.emailParticipant("billing@shop.example.test", "")
	name := "example shop"
	result, err = f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: billing, Kind: correspondentkind.Organization, OrganizationName: &name,
	})
	require.NoError(err)
	assert.False(result.OrganizationCreated)
	assert.Equal(organization.ID, *result.Record.OrganizationID)
	grouped, err := f.st.ListCorrespondentKindsContext(t.Context(),
		store.CorrespondentKindListFilter{OrganizationID: &organization.ID})
	require.NoError(err)
	assert.Len(grouped, 2)

	// Clearing withdraws the address the classification attached.
	_, err = f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: orders, Kind: correspondentkind.Person,
	})
	require.NoError(err)
	profile, err = f.st.GetOrganizationProfileContext(t.Context(), organization.ID, false)
	require.NoError(err)
	require.Len(profile.ContactPoints, 1)
	assert.Equal("billing@shop.example.test", profile.ContactPoints[0].NormalizedValue)
}

func TestSetCorrespondentKindNamesButKeepsAProfileOnlyForTheCluster(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	participant := f.emailParticipant("news@example.test", "Example News")
	person, _, err := f.st.CreatePersonFromParticipantContext(t.Context(), participant)
	require.NoError(err)

	result, err := f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: participant, Kind: correspondentkind.Organization,
	})
	require.NoError(err)
	require.NotNil(result.Record.Person)
	assert.Equal(person.ID, result.Record.Person.ID)
	assert.True(result.Record.Person.OnlyThisCluster)
	_, err = f.st.GetPersonContext(t.Context(), person.ID)
	require.NoError(err, "classification never deletes a saved person")
}

func TestSetCorrespondentKindRejectsInvalidRequests(t *testing.T) {
	f := newContactMatchFixture(t)
	source, err := f.st.GetOrCreateSource("gmail", "owner@example.test")
	require.NoError(t, err)
	participant := f.emailParticipant("someone@example.test", "Someone")
	owner := f.emailParticipant("owner@example.test", "Owner")
	require.NoError(t, f.st.AddAccountIdentityContext(t.Context(), source.ID, "owner@example.test", "manual"))
	name := "Example"
	id := int64(1)

	tests := []struct {
		name  string
		input store.SetCorrespondentKindInput
		want  error
	}{
		{"unknown kind", store.SetCorrespondentKindInput{ParticipantID: participant, Kind: "robot"},
			store.ErrCorrespondentKindInvalid},
		{"organization on another kind", store.SetCorrespondentKindInput{
			ParticipantID: participant, Kind: correspondentkind.Ignored, OrganizationName: &name,
		}, store.ErrCorrespondentKindInvalid},
		{"id and name", store.SetCorrespondentKindInput{
			ParticipantID: participant, Kind: correspondentkind.Organization,
			OrganizationName: &name, OrganizationID: &id,
		}, store.ErrCorrespondentKindInvalid},
		{"missing participant", store.SetCorrespondentKindInput{
			ParticipantID: 999999, Kind: correspondentkind.Ignored,
		}, store.ErrParticipantNotFound},
		{"owner identity", store.SetCorrespondentKindInput{
			ParticipantID: owner, Kind: correspondentkind.Ignored,
		}, store.ErrCorrespondentKindOwner},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := f.st.SetCorrespondentKindContext(t.Context(), test.input)
			assert.ErrorIs(t, err, test.want)
		})
	}
}

func TestParticipantMergeKeepsTheClassification(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	absorbed := f.emailParticipant("old-desk@example.test", "Desk")
	survivor := f.emailParticipant("desk@example.test", "Desk")
	_, err := f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: absorbed, Kind: correspondentkind.SharedMailbox,
	})
	require.NoError(err)
	require.NoError(f.st.MergeParticipants(absorbed, survivor))

	record, err := f.st.GetCorrespondentKindContext(t.Context(), survivor)
	require.NoError(err)
	assert.Equal(correspondentkind.SharedMailbox, record.Kind)
}

func TestParticipantMergeKeepsTheNewerClassification(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)

	absorbed := f.emailParticipant("older-desk@example.test", "Desk")
	survivor := f.emailParticipant("newer-desk@example.test", "Desk")
	_, err := f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: absorbed, Kind: correspondentkind.Ignored,
	})
	require.NoError(err)
	_, err = f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: survivor, Kind: correspondentkind.Person,
	})
	require.NoError(err)
	require.NoError(f.st.MergeParticipants(absorbed, survivor))

	record, err := f.st.GetCorrespondentKindContext(t.Context(), survivor)
	require.NoError(err)
	assert.Equal(correspondentkind.Person, record.Kind, "the survivor's newer person override wins")
}

func TestOwnerIdentitiesAlwaysResolveAsAPerson(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)
	source, err := f.st.GetOrCreateSource("gmail", "me@example.test")
	require.NoError(err)

	alias := f.emailParticipant("old-alias@example.test", "Old Alias")
	owner := f.emailParticipant("me@example.test", "Me")
	require.NoError(f.st.AddAccountIdentityContext(t.Context(), source.ID, "me@example.test", "manual"))
	_, err = f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: alias, Kind: correspondentkind.Ignored,
	})
	require.NoError(err)

	// Linking the ignored alias into the owner's cluster must never make the
	// owner "not a person".
	_, err = f.st.LinkParticipants(alias, owner)
	require.NoError(err)
	hidden, err := f.st.NotPersonParticipantsContext(t.Context())
	require.NoError(err)
	assert.Empty(hidden)
	record, err := f.st.GetCorrespondentKindContext(t.Context(), owner)
	require.NoError(err)
	assert.Equal(correspondentkind.Person, record.Kind)

	// The alias's classification was dropped with a record of why.
	var kind, actor string
	require.NoError(f.st.DB().QueryRow(f.st.Rebind(`SELECT kind, actor FROM correspondent_kinds
		WHERE participant_id = ? AND source = 'user'`), alias).Scan(&kind, &actor))
	assert.Equal(string(correspondentkind.Person), kind)
	assert.Equal("system:owner_identity", actor)
	records, err := f.st.ListCorrespondentKindsContext(t.Context(), store.CorrespondentKindListFilter{})
	require.NoError(err)
	assert.Empty(records)
}

func TestOwnerIdentityAddedLaterStillResolvesAsAPerson(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)
	source, err := f.st.GetOrCreateSource("gmail", "me@example.test")
	require.NoError(err)

	mine := f.emailParticipant("second-me@example.test", "Me")
	_, err = f.st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: mine, Kind: correspondentkind.Ignored,
	})
	require.NoError(err)
	// Confirming the address as the owner's own afterwards overrides the row
	// at read time, even before anything rewrites it.
	_, err = f.st.DB().ExecContext(t.Context(), f.st.Rebind(
		`INSERT INTO account_identities (source_id, address, source_signal) VALUES (?, ?, 'manual')`),
		source.ID, "second-me@example.test")
	require.NoError(err)
	hidden, err := f.st.NotPersonParticipantsContext(t.Context())
	require.NoError(err)
	assert.Empty(hidden)
}
