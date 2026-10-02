package orgresolution_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/orgresolution"
	"go.kenn.io/msgvault/internal/personfacts"
	"go.kenn.io/msgvault/internal/store"
)

func TestPreparerSettlesASharedRegistrableDomainWithoutJev(t *testing.T) {
	tests := []struct {
		name               string
		organizationDomain string
		reference          string
		jevOff             bool
	}{
		{"fact on a subdomain", "example.com",
			`{"name":"Example Labs Europe","domain":"mail.example.com"}`, false},
		{"organization on a subdomain", "eu.example.com",
			`{"name":"Example Labs Group","domain":"example.com"}`, false},
		{"same domain, another name", "example.com",
			`{"name":"Northwind Research","domain":"example.com"}`, false},
		{"under a multi-label public suffix", "example.co.uk",
			`{"name":"Example Labs UK","domain":"mail.example.co.uk"}`, false},
		{"with Jev off", "example.com",
			`{"name":"Example Labs Europe","domain":"mail.example.com"}`, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			// Were Jev asked, it would say "a different organization".
			fake := newFakeJev(t, map[string]float64{"candidate_1": 0.01}, 0)
			f := newFixture(t, fake)
			labs := f.organization(t, "Example Labs", test.organizationDomain)
			f.organization(t, "Unrelated Traders", "traders.example")
			if test.jevOff {
				f.config.Enabled = false
			}
			claims := []personfacts.ProposedClaim{f.claim(test.reference, "Engineer", "domain")}

			results, err := f.preparer().Prepare(t.Context(), f.personID, claims, nil)
			require.NoError(err)
			require.Len(results, 1)
			assert.Equal(orgresolution.OutcomeDomain, results[0].Outcome)
			assert.Equal(labs.ID, results[0].OrganizationID)
			assert.False(results[0].Asked)
			assert.Empty(results[0].Skipped)
			assert.Empty(fake.requests(), "a domain-settled reference never reaches Jev")

			f.apply(t, "domain", claims...)
			employments := f.currentEmployments(t)
			require.Len(employments, 1)
			assert.Equal(labs.ID, employments[0].OrganizationID, "the exact lookup now reuses the organization")
			profile, err := f.store.GetOrganizationProfileContext(t.Context(), labs.ID, false)
			require.NoError(err)
			require.Len(profile.Names, 1)
			require.NotNil(profile.Names[0].Envelope.SourceRef)
			assert.Equal(store.OrganizationDomainRuleSourceRef, *profile.Names[0].Envelope.SourceRef)
		})
	}
}

func TestPreparerAsksOnlyAboutTitlesAtADomainSettledOrganization(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fake := newFakeJev(t, map[string]float64{"candidate_1": 0.01}, 0.93)
	f := newFixture(t, fake)
	labs := f.organization(t, "Example Labs", "example.com")
	title := "Partner"
	_, err := f.store.AddEmploymentContext(t.Context(), store.EmploymentInput{
		PersonID: f.personID, OrganizationID: labs.ID, Title: &title,
		IsCurrent: new(true), Source: store.ProvenanceUser,
	})
	require.NoError(err)

	results, err := f.preparer().Prepare(t.Context(), f.personID, []personfacts.ProposedClaim{
		f.claim(`{"name":"Example Labs Europe","domain":"eu.example.com"}`, "General Partner", "titles"),
	}, nil)
	require.NoError(err)
	require.Len(results, 1)
	assert.Equal(orgresolution.OutcomeDomain, results[0].Outcome)
	assert.True(results[0].Asked)
	assert.Equal(1, results[0].TitleAliases)
	requests := fake.requests()
	require.Len(requests, 1)
	assert.Equal(map[string]any{"title_pairs": map[string]any{"pair_1": map[string]any{
		"organization": "Example Labs", "title": "General Partner", "other_title": "Partner",
	}}}, requests[0]["state"], "no reference or candidates: the domain already decided the organization")
	questions, ok := requests[0]["questions"].(map[string]any)
	require.True(ok)
	assert.Equal([]string{"title_same_role_1"}, keys(questions))
}

func TestPreparerAsksJevWhenTheDomainDoesNotSettleTheReference(t *testing.T) {
	tests := []struct {
		name          string
		organizations [][2]string
		reference     string
		candidates    int
	}{
		{"name match without a shared domain",
			[][2]string{{"Example Labs", "example.com"}},
			`{"name":"Example Labs Europe","domain":"examplelabs.example"}`, 1},
		{"only the public suffix is shared",
			[][2]string{{"Example Labs", "other.co.uk"}},
			`{"name":"Example Labs Europe","domain":"example.co.uk"}`, 1},
		{"consumer mail domain",
			[][2]string{{"Example Labs", "gmail.com"}},
			`{"name":"Example Labs Europe","domain":"gmail.com"}`, 1},
		{"several organizations share the domain",
			[][2]string{{"Example Labs", "example.com"}, {"Example Ventures", "ventures.example.com"}},
			`{"name":"Example Group","domain":"eu.example.com"}`, 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			fake := newFakeJev(t, map[string]float64{"candidate_1": 0.95}, 0)
			f := newFixture(t, fake)
			for _, organization := range test.organizations {
				f.organization(t, organization[0], organization[1])
			}

			results, err := f.preparer().Prepare(t.Context(), f.personID,
				[]personfacts.ProposedClaim{f.claim(test.reference, "Engineer", "residual")}, nil)
			require.NoError(err)
			require.Len(results, 1)
			assert.Equal(orgresolution.OutcomeAlias, results[0].Outcome, "Jev's answer decides")
			assert.True(results[0].Asked)
			requests := fake.requests()
			require.Len(requests, 1)
			state, ok := requests[0]["state"].(map[string]any)
			require.True(ok)
			candidates, ok := state["candidates"].(map[string]any)
			require.True(ok)
			assert.Len(candidates, test.candidates, "the whole shortlist is asked about")
			questions, ok := requests[0]["questions"].(map[string]any)
			require.True(ok)
			assert.Contains(questions, orgresolution.QuestionOrgRef)
		})
	}
}

func TestPreparerWritesNoDomainAliasOnceTheLeaseIsLost(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fake := newFakeJev(t, nil, 0)
	f := newFixture(t, fake)
	labs := f.organization(t, "Example Labs", "example.com")
	lost := &personfacts.WriteFence{
		Kind: personfacts.FencePersonSweep, PersonID: f.personID, Owner: "sweep-worker", Fence: 1,
	}

	_, err := f.preparer().Prepare(t.Context(), f.personID, []personfacts.ProposedClaim{
		f.claim(`{"name":"Example Labs Europe","domain":"eu.example.com"}`, "Engineer", "lost"),
	}, lost)
	require.ErrorIs(err, store.ErrOrganizationWriteFenced)
	assert.Empty(fake.requests())
	profile, err := f.store.GetOrganizationProfileContext(t.Context(), labs.ID, false)
	require.NoError(err)
	assert.Empty(profile.Names, "no alias for a lost lease")
}
