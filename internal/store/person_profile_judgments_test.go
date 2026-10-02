package store_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func addProfileEmployment(
	t *testing.T, st *store.Store, personID int64, organization, title string, source store.Provenance,
) *store.Employment {
	t.Helper()
	org := mustOrganization(t, st, organization)
	year := 2020
	employment, err := st.AddEmploymentContext(t.Context(), store.EmploymentInput{
		PersonID: personID, OrganizationID: org.ID, Title: &title,
		StartDate: &store.PartialDate{Year: &year}, Source: source,
	})
	require.NoError(t, err)
	return employment
}

func primaryEmploymentID(t *testing.T, st *store.Store, personID int64) int64 {
	t.Helper()
	rows, err := st.ListEmploymentsContext(t.Context(), store.EmploymentFilter{PersonID: personID, CurrentOnly: true})
	require.NoError(t, err)
	for _, row := range rows {
		if row.IsPrimary {
			return row.ID
		}
	}
	return 0
}

func TestPrimaryRoleJudgmentChoosesAmongSystemSetRolesOnly(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	person := mustPromotedPerson(t, st, "robin@example.com", "Robin Example")
	board := addProfileEmployment(t, st, person.ID, "Example Foundation", "Board Member", store.ProvenanceExtraction)
	ceo := addProfileEmployment(t, st, person.ID, "Example Labs", "CEO", store.ProvenanceEnrichment)
	require.Equal(board.ID, primaryEmploymentID(t, st, person.ID), "the first role found is primary by rule")

	declared := mustPromotedPerson(t, st, "casey@example.com", "Casey Example")
	addProfileEmployment(t, st, declared.ID, "Example Co", "Engineer", store.ProvenanceExtraction)
	addProfileEmployment(t, st, declared.ID, "Example Studio", "Advisor", store.ProvenanceUser)

	single := mustPromotedPerson(t, st, "sam@example.com", "Sam Example")
	addProfileEmployment(t, st, single.ID, "Example Works", "Designer", store.ProvenanceExtraction)

	candidates, err := st.PrimaryRoleCandidatesContext(t.Context(), 0)
	require.NoError(err)
	require.Len(candidates, 1, "a user-declared role or a single role is never judged")
	candidate := candidates[0]
	assert.Equal(person.ID, candidate.PersonID)
	require.Len(candidate.Roles, 2)
	assert.Equal("Example Foundation", candidate.Roles[0].Organization)
	assert.Equal("Board Member", candidate.Roles[0].Title)
	assert.Equal("2020", candidate.Roles[0].Start)
	assert.True(candidate.Roles[0].IsPrimary)

	changed, err := st.ApplyPrimaryRoleJudgmentContext(t.Context(), store.PrimaryRoleJudgment{
		PersonID: person.ID, Fingerprint: candidate.Fingerprint, EmploymentID: &ceo.ID,
		Confidence: 0.91, Probabilities: map[string]float64{"role_2": 0.91}, Model: "jev-test",
	})
	require.NoError(err)
	assert.True(changed)
	assert.Equal(ceo.ID, primaryEmploymentID(t, st, person.ID))

	again, err := st.PrimaryRoleCandidatesContext(t.Context(), 0)
	require.NoError(err)
	assert.Empty(again, "an unchanged set of roles is not judged again")
}

func TestPrimaryRoleJudgmentNeverOverridesAUserChoice(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	person := mustPromotedPerson(t, st, "robin@example.com", "Robin Example")
	board := addProfileEmployment(t, st, person.ID, "Example Foundation", "Board Member", store.ProvenanceExtraction)
	ceo := addProfileEmployment(t, st, person.ID, "Example Labs", "CEO", store.ProvenanceExtraction)
	candidates, err := st.PrimaryRoleCandidatesContext(t.Context(), 0)
	require.NoError(err)
	require.Len(candidates, 1)

	// The user chooses the primary role after the candidate was listed.
	current, err := st.GetEmploymentContext(t.Context(), board.ID)
	require.NoError(err)
	_, err = st.SetPrimaryEmploymentContext(t.Context(), board.ID, current.Revision)
	require.NoError(err)

	changed, err := st.ApplyPrimaryRoleJudgmentContext(t.Context(), store.PrimaryRoleJudgment{
		PersonID: person.ID, Fingerprint: candidates[0].Fingerprint, EmploymentID: &ceo.ID,
		Confidence: 0.95, Model: "jev-test",
	})
	require.NoError(err)
	assert.False(changed, "the user's pin wins")
	assert.Equal(board.ID, primaryEmploymentID(t, st, person.ID))
	later, err := st.PrimaryRoleCandidatesContext(t.Context(), 0)
	require.NoError(err)
	assert.Empty(later, "a pinned employment is never offered")
}

func TestDisplayNameJudgmentReplacesOnlyTheRulesChoice(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	first, err := st.EnsureParticipant("jdoe@example.com", "jdoe", "example.com")
	require.NoError(err)
	second, err := st.EnsureParticipant("jane@example.org", "Jane Doe", "example.org")
	require.NoError(err)
	_, err = st.LinkParticipants(first, second)
	require.NoError(err)
	person, created, err := st.CreatePersonFromParticipantContext(t.Context(), first)
	require.NoError(err)
	require.True(created)
	require.NotNil(person.DisplayName)
	require.Equal("jdoe", *person.DisplayName)

	single := mustPromotedPerson(t, st, "sam@example.com", "Sam Example")
	require.NotNil(single)

	candidates, err := st.DisplayNameCandidatesContext(t.Context(), 0)
	require.NoError(err)
	require.Len(candidates, 1, "a person promoted from one name is never judged")
	assert.Equal(person.ID, candidates[0].PersonID)
	assert.Equal("jdoe", candidates[0].Current)
	assert.Equal([]string{"jdoe", "Jane Doe"}, candidates[0].Names)

	name := "Jane Doe"
	changed, err := st.ApplyDisplayNameJudgmentContext(t.Context(), store.DisplayNameJudgment{
		PersonID: person.ID, Fingerprint: candidates[0].Fingerprint, Name: &name,
		Confidence: 0.9, Model: "jev-test",
	})
	require.NoError(err)
	assert.True(changed)
	renamed, err := st.GetPersonContext(t.Context(), person.ID)
	require.NoError(err)
	require.NotNil(renamed.DisplayName)
	assert.Equal("Jane Doe", *renamed.DisplayName)
	after, err := st.DisplayNameCandidatesContext(t.Context(), 0)
	require.NoError(err)
	assert.Empty(after, "once renamed the person is no longer the rule's choice")
}

func TestDisplayNameJudgmentNeverOverridesAUserRename(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	first, err := st.EnsureParticipant("jdoe@example.com", "jdoe", "example.com")
	require.NoError(err)
	second, err := st.EnsureParticipant("jane@example.org", "Jane Doe", "example.org")
	require.NoError(err)
	_, err = st.LinkParticipants(first, second)
	require.NoError(err)
	person, _, err := st.CreatePersonFromParticipantContext(t.Context(), first)
	require.NoError(err)
	candidates, err := st.DisplayNameCandidatesContext(t.Context(), 0)
	require.NoError(err)
	require.Len(candidates, 1)

	chosen := "J. Doe (personal)"
	renamed, err := st.UpdatePersonDisplayNameContext(t.Context(), person.ID, person.Revision, &chosen)
	require.NoError(err)
	// Renamed back to the rule's name: still the user's choice.
	chosen = "jdoe"
	_, err = st.UpdatePersonDisplayNameContext(t.Context(), person.ID, renamed.Revision, &chosen)
	require.NoError(err)
	later, err := st.DisplayNameCandidatesContext(t.Context(), 0)
	require.NoError(err)
	assert.Empty(later, "a renamed person is never offered again")

	name := "Jane Doe"
	changed, err := st.ApplyDisplayNameJudgmentContext(t.Context(), store.DisplayNameJudgment{
		PersonID: person.ID, Fingerprint: candidates[0].Fingerprint, Name: &name,
		Confidence: 0.99, Model: "jev-test",
	})
	require.NoError(err)
	assert.False(changed)
	current, err := st.GetPersonContext(t.Context(), person.ID)
	require.NoError(err)
	require.NotNil(current.DisplayName)
	assert.Equal(chosen, *current.DisplayName)
}

func mergedWithConflict(
	t *testing.T, st *store.Store, key, survivorValue, absorbedValue string, absorbedSource store.Provenance,
) (*store.PersonMergeResult, *store.Person) {
	t.Helper()
	return mergedWithSources(t, st, key, survivorValue, absorbedValue, store.ProvenanceExtraction, absorbedSource)
}

func mergedWithSources(
	t *testing.T, st *store.Store, key, survivorValue, absorbedValue string,
	survivorSource, absorbedSource store.Provenance,
) (*store.PersonMergeResult, *store.Person) {
	t.Helper()
	require := require.New(t)
	ctx := context.Background()
	survivor := mustPromotedPerson(t, st, key+"-survivor@example.com", "Survivor "+key)
	absorbed := mustPromotedPerson(t, st, key+"-absorbed@example.com", "Absorbed "+key)
	_, err := st.SetPersonAttributeValueContext(ctx, store.PersonAttributeValueInput{
		PersonID: survivor.ID, DefinitionSlug: store.AttributeSlugLocation,
		Value:  store.AttributeValue{Type: store.AttributeValueText, Text: &survivorValue},
		Source: survivorSource,
	})
	require.NoError(err)
	_, err = st.SetPersonAttributeValueContext(ctx, store.PersonAttributeValueInput{
		PersonID: absorbed.ID, DefinitionSlug: store.AttributeSlugLocation,
		Value:  store.AttributeValue{Type: store.AttributeValueText, Text: &absorbedValue},
		Source: absorbedSource,
	})
	require.NoError(err)
	survivor, err = st.GetPersonContext(ctx, survivor.ID)
	require.NoError(err)
	absorbed, err = st.GetPersonContext(ctx, absorbed.ID)
	require.NoError(err)
	merged, err := st.MergePersonsContext(ctx, store.PersonMergeRequest{
		SurvivorID: survivor.ID, AbsorbedID: absorbed.ID,
		ExpectedSurvivorRevision: survivor.Revision, ExpectedAbsorbedRevision: absorbed.Revision,
		IdempotencyKey: key + "-merge", Actor: "test",
	})
	require.NoError(err)
	require.Len(merged.ReviewCandidates, 1)
	return merged, &merged.Person
}

func TestMergeConflictJudgmentKeepsTheSurvivorForTheSameFact(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	same, samePerson := mergedWithConflict(t, st, "same", "San Francisco, CA", "San Francisco", store.ProvenanceExtraction)
	different, _ := mergedWithConflict(t, st, "different", "Lisbon", "Porto", store.ProvenanceExtraction)
	declared, _ := mergedWithConflict(t, st, "declared", "Oslo", "Oslo, Norway", store.ProvenanceUser)

	candidates, err := st.MergeConflictCandidatesContext(t.Context(), 0)
	require.NoError(err)
	require.Len(candidates, 3)
	byID := map[int64]store.MergeConflictCandidate{}
	for _, candidate := range candidates {
		byID[candidate.CandidateID] = candidate
	}
	sameCandidate := byID[same.ReviewCandidates[0].ID]
	assert.Equal("San Francisco, CA", sameCandidate.Survivor)
	assert.Equal("San Francisco", sameCandidate.Absorbed)
	assert.NotEmpty(sameCandidate.Field)
	assert.Equal(store.AttributeValueText, sameCandidate.ValueType)
	assert.Equal(store.AttributeFieldText, sameCandidate.FieldType)
	assert.Equal(store.ProvenanceExtraction, sameCandidate.SurvivorSource)
	declaredCandidate := byID[declared.ReviewCandidates[0].ID]
	assert.Equal(store.ProvenanceUser, declaredCandidate.AbsorbedSource, "listed with its source for code to compare")

	kept, err := st.ApplyMergeConflictJudgmentContext(t.Context(), store.MergeConflictJudgment{
		CandidateID: declaredCandidate.CandidateID, PersonID: declaredCandidate.PersonID,
		Probability: 0.99, Model: "jev-test", Resolve: true,
	})
	require.NoError(err)
	assert.False(kept, "a judgment never rejects a user-declared absorbed value")

	resolved, err := st.ApplyMergeConflictJudgmentContext(t.Context(), store.MergeConflictJudgment{
		CandidateID: sameCandidate.CandidateID, PersonID: sameCandidate.PersonID,
		Probability: 0.97, Model: "jev-test", Resolve: true,
	})
	require.NoError(err)
	assert.True(resolved)
	resolvedAgain, err := st.ApplyMergeConflictJudgmentContext(t.Context(), store.MergeConflictJudgment{
		CandidateID: different.ReviewCandidates[0].ID, PersonID: different.Person.ID,
		Probability: 0.2, Model: "jev-test",
	})
	require.NoError(err)
	assert.False(resolvedAgain)

	detail, err := st.ListPersonMergesContext(t.Context(), samePerson.ID)
	require.NoError(err)
	require.Len(detail, 1)
	assert.Zero(detail[0].PendingCandidateCount)
	values, err := st.ListPersonAttributeValuesContext(t.Context(), samePerson.ID, store.PersonAttributeQuery{})
	require.NoError(err)
	for _, value := range values {
		if value.DefinitionSlug == store.AttributeSlugLocation {
			require.NotNil(value.Value.Text)
			assert.Equal("San Francisco, CA", *value.Value.Text, "the survivor's value stays")
		}
	}
	remaining, err := st.MergeConflictCandidatesContext(t.Context(), 0)
	require.NoError(err)
	assert.Empty(remaining, "judged conflicts are not asked again")
}

func TestSettleEqualMergeConflictKeepsTheUsersValue(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	userAbsorbed, person := mergedWithConflict(t, st, "absorbed", "oslo", "Oslo", store.ProvenanceUser)
	systemBoth, _ := mergedWithConflict(t, st, "system", "Oslo", "oslo", store.ProvenanceExtraction)

	settled, err := st.SettleEqualMergeConflictContext(t.Context(), store.MergeConflictSettlement{
		CandidateID: userAbsorbed.ReviewCandidates[0].ID, PersonID: person.ID, KeepAbsorbed: true,
	})
	require.NoError(err)
	assert.True(settled)
	values, err := st.ListPersonAttributeValuesContext(t.Context(), person.ID,
		store.PersonAttributeQuery{DefinitionSlug: store.AttributeSlugLocation})
	require.NoError(err)
	require.Len(values, 1)
	require.NotNil(values[0].Value.Text)
	assert.Equal("Oslo", *values[0].Value.Text)
	assert.Equal(store.ProvenanceUser, values[0].Source)

	settled, err = st.SettleEqualMergeConflictContext(t.Context(), store.MergeConflictSettlement{
		CandidateID: systemBoth.ReviewCandidates[0].ID, PersonID: systemBoth.Person.ID,
	})
	require.NoError(err)
	assert.True(settled)
	detail, err := st.GetPersonMergeContext(t.Context(), systemBoth.Merge.ID)
	require.NoError(err)
	require.Len(detail.ReviewCandidates, 1)
	assert.Equal("rejected", detail.ReviewCandidates[0].State)
	require.NotNil(detail.ReviewCandidates[0].ReviewedBy)
	assert.Equal(store.PersonMergeConflictNormalizedActor, *detail.ReviewCandidates[0].ReviewedBy)

	remaining, err := st.MergeConflictCandidatesContext(t.Context(), 0)
	require.NoError(err)
	assert.Empty(remaining, "settled conflicts are not listed again")
}

func TestSettleEqualMergeConflictNeverReplacesADeclaredSurvivorValue(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	merged, person := mergedWithSources(t, st, "declared", "Oslo", "oslo",
		store.ProvenanceUser, store.ProvenanceVCardImport)

	settled, err := st.SettleEqualMergeConflictContext(t.Context(), store.MergeConflictSettlement{
		CandidateID: merged.ReviewCandidates[0].ID, PersonID: person.ID, KeepAbsorbed: true,
	})
	require.ErrorIs(err, store.ErrPersonProfileJudgmentInvalid)
	assert.False(settled)
	detail, err := st.GetPersonMergeContext(t.Context(), merged.Merge.ID)
	require.NoError(err)
	assert.Equal("pending", detail.ReviewCandidates[0].State)
}
