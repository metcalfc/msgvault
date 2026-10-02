package profilejudge_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/profilejudge"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
	"go.kenn.io/msgvault/internal/testutil/jevtest"
)

func promoted(t *testing.T, st *store.Store, email, name string) *store.Person {
	t.Helper()
	participant, err := st.EnsureParticipant(email, name, "example.com")
	require.NoError(t, err)
	person, _, err := st.CreatePersonFromParticipantContext(t.Context(), participant)
	require.NoError(t, err)
	return person
}

func employment(t *testing.T, st *store.Store, personID int64, organization, title string) int64 {
	t.Helper()
	org, err := st.CreateOrganizationContext(t.Context(), store.OrganizationInput{
		Name: organization, Kind: store.OrganizationKindCompany,
	})
	require.NoError(t, err)
	row, err := st.AddEmploymentContext(t.Context(), store.EmploymentInput{
		PersonID: personID, OrganizationID: org.ID, Title: &title, Source: store.ProvenanceExtraction,
	})
	require.NoError(t, err)
	return row.ID
}

func primaryID(t *testing.T, st *store.Store, personID int64) int64 {
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

// answerByContent picks the CEO role, the name with a space, and calls two
// values the same when one contains the other.
func answerByContent(questionID string, _ map[string]any, state map[string]any) map[string]any {
	switch questionID {
	case profilejudge.PrimaryRoleQuestionID:
		roles, _ := state["roles"].(map[string]any)
		for key, role := range roles {
			if title, _ := role.(map[string]any)["title"].(string); title == "CEO" {
				return jevtest.Choice(key, map[string]float64{key: 0.9, profilejudge.OptionUnclear: 0.1})
			}
		}
		return jevtest.Choice(profilejudge.OptionUnclear, map[string]float64{profilejudge.OptionUnclear: 1})
	case profilejudge.DisplayNameQuestionID:
		names, _ := state["names"].(map[string]any)
		for key, name := range names {
			if text, _ := name.(string); strings.Contains(text, " ") {
				return jevtest.Choice(key, map[string]float64{key: 0.85, profilejudge.OptionUnclear: 0.15})
			}
		}
		return jevtest.Choice(profilejudge.OptionUnclear, map[string]float64{profilejudge.OptionUnclear: 1})
	}
	conflicts, _ := state["conflicts"].(map[string]any)
	conflict, _ := conflicts["conflict_"+questionID[strings.LastIndexByte(questionID, '_')+1:]].(map[string]any)
	first, _ := conflict["first"].(string)
	second, _ := conflict["second"].(string)
	if strings.Contains(first, second) || strings.Contains(second, first) {
		return jevtest.Noul(0.97)
	}
	return jevtest.Noul(0.9)
}

func enabled(cfg *jev.Config) { cfg.PersonProfileChoices = jev.FeatureConfig{Enabled: true} }

func mergeWithLocations(t *testing.T, st *store.Store, key, survivorValue, absorbedValue string) *store.PersonMergeResult {
	t.Helper()
	return mergeWithValues(t, st, store.AttributeSlugLocation, key, survivorValue, absorbedValue)
}

// longTextSlug defines a single-value text field without a length limit.
func longTextSlug(t *testing.T, st *store.Store) string {
	t.Helper()
	_, err := st.CreateAttributeDefinitionContext(t.Context(), store.AttributeDefinitionInput{
		UniversalID: "test-home-notes", ObjectType: store.AttributeObjectPerson, Slug: "home_notes",
		Label: "Home notes", ValueType: store.AttributeValueText, FieldType: store.AttributeFieldText,
		Cardinality: store.AttributeCardinalitySingle, Ownership: store.AttributeOwnershipUser,
		UICreatable: true, UIEditable: true, APIMutable: true, IsAudited: true, IsDeletable: true,
	})
	require.NoError(t, err)
	return "home_notes"
}

func mergeWithValues(
	t *testing.T, st *store.Store, slug, key, survivorValue, absorbedValue string,
) *store.PersonMergeResult {
	t.Helper()
	return mergeTyped(t, st, slug, key, textValue(survivorValue), textValue(absorbedValue),
		store.ProvenanceExtraction, store.ProvenanceExtraction)
}

func textValue(text string) store.AttributeValue {
	return store.AttributeValue{Type: store.AttributeValueText, Text: &text}
}

// mergeTyped gives a survivor and an absorbed person one value each of the
// slug's attribute, from the given sources, and merges them.
func mergeTyped(
	t *testing.T, st *store.Store, slug, key string, survivorValue, absorbedValue store.AttributeValue,
	survivorSource, absorbedSource store.Provenance,
) *store.PersonMergeResult {
	t.Helper()
	require := require.New(t)
	ctx := context.Background()
	survivor := promoted(t, st, key+"-survivor@example.com", "Survivor "+key)
	absorbed := promoted(t, st, key+"-absorbed@example.com", "Absorbed "+key)
	for _, entry := range []struct {
		personID int64
		value    store.AttributeValue
		source   store.Provenance
	}{{survivor.ID, survivorValue, survivorSource}, {absorbed.ID, absorbedValue, absorbedSource}} {
		_, err := st.SetPersonAttributeValueContext(ctx, store.PersonAttributeValueInput{
			PersonID: entry.personID, DefinitionSlug: slug, Value: entry.value, Source: entry.source,
		})
		require.NoError(err)
	}
	survivor, err := st.GetPersonContext(ctx, survivor.ID)
	require.NoError(err)
	absorbed, err = st.GetPersonContext(ctx, absorbed.ID)
	require.NoError(err)
	merged, err := st.MergePersonsContext(ctx, store.PersonMergeRequest{
		SurvivorID: survivor.ID, AbsorbedID: absorbed.ID,
		ExpectedSurvivorRevision: survivor.Revision, ExpectedAbsorbedRevision: absorbed.Revision,
		IdempotencyKey: key + "-merge", Actor: "test",
	})
	require.NoError(err)
	require.Len(merged.ReviewCandidates, 1, "the merge leaves one conflict")
	return merged
}

func TestRunSettlesRolesNamesAndSameValueConflicts(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	robin := promoted(t, st, "robin@example.com", "Robin Example")
	employment(t, st, robin.ID, "Example Foundation", "Board Member")
	ceo := employment(t, st, robin.ID, "Example Labs", "CEO")

	first, err := st.EnsureParticipant("jdoe@example.com", "jdoe", "example.com")
	require.NoError(err)
	second, err := st.EnsureParticipant("jane@example.org", "Jane Doe", "example.org")
	require.NoError(err)
	_, err = st.LinkParticipants(first, second)
	require.NoError(err)
	jane, _, err := st.CreatePersonFromParticipantContext(t.Context(), first)
	require.NoError(err)

	sameMerge := mergeWithLocations(t, st, "same", "San Francisco, CA", "San Francisco")
	closeMerge := mergeWithLocations(t, st, "close", "Lisbon", "Porto")

	server := jevtest.NewServer(t, answerByContent)
	service, cfg := server.Service(t, st, enabled)
	jevtest.GrantConsent(t, st, cfg, profilejudge.Feature())

	report, err := profilejudge.Run(t.Context(), st, profilejudge.Options{Judge: service})
	require.NoError(err)
	assert.Equal(profilejudge.Report{
		Requests: 3, PrimaryRoles: 1, PrimaryRolesSet: 1, DisplayNames: 1, DisplayNamesSet: 1,
		MergeConflicts: 2, ConflictsSettled: 1,
	}, report)
	assert.Equal(ceo, primaryID(t, st, robin.ID))
	renamed, err := st.GetPersonContext(t.Context(), jane.ID)
	require.NoError(err)
	require.NotNil(renamed.DisplayName)
	assert.Equal("Jane Doe", *renamed.DisplayName)

	merges, err := st.ListPersonMergesContext(t.Context(), sameMerge.Person.ID)
	require.NoError(err)
	require.Len(merges, 1)
	assert.Zero(merges[0].PendingCandidateCount, "0.97 keeps the survivor's value")
	merges, err = st.ListPersonMergesContext(t.Context(), closeMerge.Person.ID)
	require.NoError(err)
	require.Len(merges, 1)
	assert.Equal(1, merges[0].PendingCandidateCount, "0.90 is below the 0.95 threshold")

	requests := server.Requests()
	require.Len(requests, 3)
	roleState, ok := requests[0]["state"].(map[string]any)
	require.True(ok)
	assert.Equal(map[string]any{
		"role_1": map[string]any{"organization": "Example Foundation", "title": "Board Member", "start": ""},
		"role_2": map[string]any{"organization": "Example Labs", "title": "CEO", "start": ""},
	}, roleState["roles"])
	assert.Len(roleState, 1, "only the roles leave for the primary role question")
	nameState, ok := requests[1]["state"].(map[string]any)
	require.True(ok)
	assert.Equal(map[string]any{"names": map[string]any{"name_1": "jdoe", "name_2": "Jane Doe"}}, nameState)
	raw := fmt.Sprint(requests)
	assert.NotContains(raw, "@example.", "no address leaves")

	again, err := profilejudge.Run(t.Context(), st, profilejudge.Options{Judge: service})
	require.NoError(err)
	assert.Equal(profilejudge.Report{}, again, "nothing is asked twice")
}

func TestRunSendsNothingWithoutConsentAndKeepsLowConfidenceChoices(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	robin := promoted(t, st, "robin@example.com", "Robin Example")
	board := employment(t, st, robin.ID, "Example Foundation", "Board Member")
	employment(t, st, robin.ID, "Example Labs", "Chair")

	server := jevtest.NewServer(t, answerByContent)
	service, cfg := server.Service(t, st, enabled)
	report, err := profilejudge.Run(t.Context(), st, profilejudge.Options{Judge: service})
	require.NoError(err)
	assert.Equal("consent_required", report.Skipped)
	assert.Empty(server.Requests())

	jevtest.GrantConsent(t, st, cfg, profilejudge.Feature())
	report, err = profilejudge.Run(t.Context(), st, profilejudge.Options{Judge: service})
	require.NoError(err)
	assert.Equal(1, report.PrimaryRoles)
	assert.Zero(report.PrimaryRolesSet, "unclear keeps the rule's primary role")
	assert.Equal(board, primaryID(t, st, robin.ID))
}

func TestRunNeverSettlesAConflictWhoseValuesWouldBeCut(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	prefix := strings.Repeat("Lives near the old harbor district ", 10)
	merged := mergeWithValues(t, st, longTextSlug(t, st), "long", prefix+"in the north", prefix+"in the south")

	alwaysSame := func(string, map[string]any, map[string]any) map[string]any { return jevtest.Noul(0.99) }
	server := jevtest.NewServer(t, alwaysSame)
	service, cfg := server.Service(t, st, enabled)
	jevtest.GrantConsent(t, st, cfg, profilejudge.Feature())

	report, err := profilejudge.Run(t.Context(), st, profilejudge.Options{Judge: service})
	require.NoError(err)
	assert.Zero(report.ConflictsSettled)
	assert.Empty(server.Requests(), "a conflict with a value over 300 characters is never sent")
	merges, err := st.ListPersonMergesContext(t.Context(), merged.Person.ID)
	require.NoError(err)
	require.Len(merges, 1)
	assert.Equal(1, merges[0].PendingCandidateCount, "the conflict stays with the user")
	remaining, err := st.MergeConflictCandidatesContext(t.Context(), 0)
	require.NoError(err)
	assert.Empty(remaining, "it is recorded so it is not listed again")
}
