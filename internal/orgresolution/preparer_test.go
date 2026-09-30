package orgresolution_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/orgresolution"
	"go.kenn.io/msgvault/internal/personfacts"
	"go.kenn.io/msgvault/internal/store"
)

func TestFeaturePolicyDisclosesOnlyNamesDomainsAndTitles(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	spec := orgresolution.Feature()
	require.NoError(spec.Validate())
	assert.Equal(jev.FeatureOrganizationResolution, spec.Name)
	assert.Equal([]string{
		"reference.name", "reference.domain",
		"candidates.candidate_N.name", "candidates.candidate_N.domains[]",
		"candidates.candidate_N.other_names[]",
		"title_pairs.pair_N.organization", "title_pairs.pair_N.title", "title_pairs.pair_N.other_title",
	}, spec.StateFields)
	ids := make([]string, len(spec.Questions))
	for i, question := range spec.Questions {
		ids[i] = question.ID
	}
	assert.Equal([]string{
		"org_ref", "title_same_role_1", "title_same_role_2", "title_same_role_3", "title_same_role_4",
	}, ids)
	criteria, ok := spec.Questions[0].Criteria.(map[string]string)
	require.True(ok)
	assert.Len(criteria, store.MaxOrganizationShortlist+1, "one option per shortlist slot plus new_organization")
	assert.Contains(criteria, orgresolution.OptionNewOrganization)
}

func TestPreparerAliasesNearNamesToTheExistingOrganization(t *testing.T) {
	for _, name := range []string{"Example Labs, Inc.", "Example Labs (YC W21)"} {
		t.Run(name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			fake := newFakeJev(t, map[string]float64{"candidate_1": 0.94}, 0)
			f := newFixture(t, fake)
			labs := f.organization(t, "Example Labs", "")
			f.organization(t, "Northwind Traders", "")
			claims := []personfacts.ProposedClaim{f.claim(`{"name":"`+name+`"}`, "Engineer", "alias")}

			results, err := f.preparer().Prepare(t.Context(), f.personID, claims)
			require.NoError(err)
			require.Len(results, 1)
			assert.Equal(orgresolution.OutcomeAlias, results[0].Outcome)
			assert.Equal(labs.ID, results[0].OrganizationID)
			assert.InDelta(0.94, results[0].Probability, 1e-9)

			requests := fake.requests()
			require.Len(requests, 1)
			assert.Equal(map[string]any{
				"reference":  map[string]any{"name": name},
				"candidates": map[string]any{"candidate_1": map[string]any{"name": "Example Labs"}},
			}, requests[0]["state"], "only the names leave; the unrelated organization is not shortlisted")
			questions, ok := requests[0]["questions"].(map[string]any)
			require.True(ok)
			assert.Len(questions, 1, "no title pair without a known title")
			assert.Contains(questions, orgresolution.QuestionOrgRef)

			f.apply(t, "alias", claims...)
			employments := f.currentEmployments(t)
			require.Len(employments, 1)
			assert.Equal(labs.ID, employments[0].OrganizationID, "the deterministic lookup reuses the organization")
			organizations, err := f.store.ListOrganizationsContext(t.Context(), store.OrganizationFilter{})
			require.NoError(err)
			assert.Len(organizations, 2, "no new organization")
		})
	}
}

func TestPreparerCreatesUnrelatedSimilarNamesAsBefore(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fake := newFakeJev(t, map[string]float64{"candidate_1": 0.08}, 0)
	f := newFixture(t, fake)
	labs := f.organization(t, "Example Labs", "")
	claims := []personfacts.ProposedClaim{f.claim(`{"name":"Example Logistics"}`, "Engineer", "new")}

	results, err := f.preparer().Prepare(t.Context(), f.personID, claims)
	require.NoError(err)
	require.Len(results, 1)
	assert.Equal(orgresolution.OutcomeNew, results[0].Outcome)
	assert.True(results[0].Asked)

	f.apply(t, "new", claims...)
	employments := f.currentEmployments(t)
	require.Len(employments, 1)
	assert.NotEqual(labs.ID, employments[0].OrganizationID)
	created, err := f.store.GetOrganizationContext(t.Context(), employments[0].OrganizationID)
	require.NoError(err)
	assert.Equal("Example Logistics", created.Name)
	reviews, err := f.store.ListOrganizationMatchReviewsContext(t.Context(), 10)
	require.NoError(err)
	assert.Empty(reviews)
}

func TestPreparerSendsMidConfidenceMatchesToReview(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fake := newFakeJev(t, map[string]float64{"candidate_1": 0.7}, 0)
	f := newFixture(t, fake)
	labs := f.organization(t, "Example Labs", "")
	claims := []personfacts.ProposedClaim{
		f.claim(`{"name":"Example Labs Europe","domain":"eu.examplelabs.example"}`, "Engineer", "review"),
	}

	results, err := f.preparer().Prepare(t.Context(), f.personID, claims)
	require.NoError(err)
	require.Len(results, 1)
	assert.Equal(orgresolution.OutcomeReview, results[0].Outcome)

	f.apply(t, "review", claims...)
	employments := f.currentEmployments(t)
	require.Len(employments, 1)
	assert.NotEqual(labs.ID, employments[0].OrganizationID, "an unsure match still creates as before")

	reviews, err := f.store.ListOrganizationMatchReviewsContext(t.Context(), 10)
	require.NoError(err)
	require.Len(reviews, 1)
	assert.Equal(labs.ID, reviews[0].OrganizationID)
	assert.Equal("Example Labs Europe", reviews[0].ProposedName)
	require.NotNil(reviews[0].ProposedOrganizationID)
	assert.Equal(employments[0].OrganizationID, *reviews[0].ProposedOrganizationID)
	assert.InDelta(0.7, reviews[0].Probability, 1e-9)
	assert.Equal(jev.DefaultModel, reviews[0].Model)

	decision, err := f.store.AcceptOrganizationMatchReviewContext(t.Context(), reviews[0].ID, "user")
	require.NoError(err)
	require.NotNil(decision.MergedOrganizationID)
	employments = f.currentEmployments(t)
	require.Len(employments, 1)
	assert.Equal(labs.ID, employments[0].OrganizationID, "accepting merges the created organization")
}

func TestPreparerAliasWriteIsIdempotentAndLaterLookupsNeedNoJudgment(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fake := newFakeJev(t, map[string]float64{"candidate_1": 0.97}, 0)
	f := newFixture(t, fake)
	labs := f.organization(t, "Example Labs", "labs.example")
	claims := []personfacts.ProposedClaim{
		f.claim(`{"name":"Example Labs, Inc.","domain":"labs.example"}`, "Engineer", "first"),
	}

	first, err := f.preparer().Prepare(t.Context(), f.personID, claims)
	require.NoError(err)
	require.Len(first, 1)
	assert.Equal(orgresolution.OutcomeAlias, first[0].Outcome)
	second, err := f.preparer().Prepare(t.Context(), f.personID, claims)
	require.NoError(err)
	require.Len(second, 1)
	assert.Equal(orgresolution.OutcomeExact, second[0].Outcome, "the stored alias answers the second time")
	assert.False(second[0].Asked)
	assert.Len(fake.requests(), 1, "one judgment, then the alias")

	profile, err := f.store.GetOrganizationProfileContext(t.Context(), labs.ID, false)
	require.NoError(err)
	assert.Len(profile.Names, 1)
	assert.Empty(profile.Identifiers, "the domain already matched the primary domain")
}
