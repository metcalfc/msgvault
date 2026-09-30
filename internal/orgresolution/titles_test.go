package orgresolution_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/orgresolution"
	"go.kenn.io/msgvault/internal/personfacts"
)

func TestSameRoleTitlesCorroborateAtOneOrganization(t *testing.T) {
	tests := []struct {
		name      string
		titleSame float64
		applied   bool
	}{
		{"judged the same role", 0.93, true},
		{"judged different roles", 0.2, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			fake := newFakeJev(t, nil, test.titleSame)
			f := newFixture(t, fake)
			labs := f.organization(t, "Example Labs", "")
			claims := []personfacts.ProposedClaim{
				f.weakClaim(`{"name":"Example Labs"}`, "Partner", "sweep"),
				f.weakClaim(`{"name":"Example Labs"}`, "General Partner", "exa"),
			}

			results, err := f.preparer().Prepare(t.Context(), f.personID, claims)
			require.NoError(err)
			require.Len(results, 1)
			assert.Equal(orgresolution.OutcomeExact, results[0].Outcome)
			requests := fake.requests()
			require.Len(requests, 1)
			assert.Equal(map[string]any{"title_pairs": map[string]any{"pair_1": map[string]any{
				"organization": "Example Labs", "title": "General Partner", "other_title": "Partner",
			}}}, requests[0]["state"], "an exact organization match asks only about titles")
			questions, ok := requests[0]["questions"].(map[string]any)
			require.True(ok)
			assert.Equal([]string{"title_same_role_1"}, keys(questions))

			result := f.apply(t, "titles", claims...)
			employments := f.currentEmployments(t)
			if !test.applied {
				assert.Empty(employments, "each title alone stays below the apply threshold")
				for _, decision := range result.Decisions {
					assert.Equal(personfacts.ReasonBelowThreshold, decision.Reason)
				}
				return
			}
			require.Len(employments, 1, "two titles for one role add up to one employment")
			assert.Equal(labs.ID, employments[0].OrganizationID)
			require.NotNil(employments[0].Title)
			assert.Equal("Partner", *employments[0].Title)
			var applied, superseded int
			for _, decision := range result.Decisions {
				if decision.Action == personfacts.DecisionApplied {
					applied++
					assert.Equal(790, decision.Score.Total, "740 plus one corroborating source")
				}
				if decision.Action == personfacts.DecisionSuperseded {
					superseded++
				}
			}
			assert.Equal(1, applied)
			assert.Equal(1, superseded)
		})
	}
}

func TestSameRoleTitleMatchesTheExistingEmployment(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fake := newFakeJev(t, nil, 0.95)
	f := newFixture(t, fake)
	labs := f.organization(t, "Example Labs", "")
	f.apply(t, "first", f.claim(`{"name":"Example Labs"}`, "Partner", "first"))
	require.Len(f.currentEmployments(t), 1)

	later := []personfacts.ProposedClaim{f.claim(`{"name":"Example Labs"}`, "General Partner", "later")}
	results, err := f.preparer().Prepare(t.Context(), f.personID, later)
	require.NoError(err)
	require.Len(results, 1)
	assert.Equal(1, results[0].TitleAliases)
	f.apply(t, "later", later...)

	employments := f.currentEmployments(t)
	require.Len(employments, 1, "the same role is one employment, not a second current job")
	assert.Equal(labs.ID, employments[0].OrganizationID)
	require.NotNil(employments[0].Title)
	assert.Equal("Partner", *employments[0].Title)

	again, err := f.preparer().Prepare(t.Context(), f.personID, later)
	require.NoError(err)
	require.Len(again, 1)
	assert.False(again[0].Asked, "a title already mapped is never asked about again")
	assert.Len(fake.requests(), 1)
}

func keys(values map[string]any) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return out
}
