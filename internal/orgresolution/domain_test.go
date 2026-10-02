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
		{"same name, fact on a subdomain", "example.com",
			`{"name":"Example Labs","domain":"mail.example.com"}`, false},
		{"legal suffix and punctuation, same domain", "example.com",
			`{"name":"Example Labs, Inc.","domain":"example.com"}`, false},
		{"organization on a subdomain", "eu.example.com",
			`{"name":"example labs","domain":"example.com"}`, false},
		{"under a multi-label public suffix", "example.co.uk",
			`{"name":"Example Labs Ltd","domain":"mail.example.co.uk"}`, false},
		{"with Jev off", "example.com",
			`{"name":"Example Labs","domain":"mail.example.com"}`, true},
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
			var sources []string
			for _, name := range profile.Names {
				require.NotNil(name.Envelope.SourceRef)
				sources = append(sources, *name.Envelope.SourceRef)
			}
			for _, identifier := range profile.Identifiers {
				require.NotNil(identifier.Envelope.SourceRef)
				sources = append(sources, *identifier.Envelope.SourceRef)
			}
			require.NotEmpty(sources, "the name or the domain became a lookup key")
			for _, source := range sources {
				assert.Equal(store.OrganizationDomainRuleSourceRef, source)
			}
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
		f.claim(`{"name":"Example Labs","domain":"eu.example.com"}`, "General Partner", "titles"),
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
		{"shared domain under a different name",
			[][2]string{{"Example Labs", "example.com"}},
			`{"name":"Example Labs Europe","domain":"eu.example.com"}`, 1},
		{"name match without a shared domain",
			[][2]string{{"Example Labs", "example.com"}},
			`{"name":"Example Labs Inc","domain":"examplelabs.example"}`, 1},
		{"only the public suffix is shared",
			[][2]string{{"Example Labs", "other.co.uk"}},
			`{"name":"Example Labs","domain":"example.co.uk"}`, 1},
		{"profile URL on a platform under another name",
			[][2]string{{"LinkedIn", "linkedin.com"}, {"Acme Bakers", ""}},
			`{"name":"Acme Bakery","domain":"https://www.linkedin.com/company/acme"}`, 2},
		{"profile URL on a platform under the same name",
			[][2]string{{"Acme Bakery", "linkedin.com"}},
			`{"name":"Acme Bakery","domain":"https://uk.linkedin.com/company/acme"}`, 1},
		{"platform subdomain",
			[][2]string{{"Acme Studio", "other.medium.com"}},
			`{"name":"Acme Studio","domain":"acme.medium.com"}`, 1},
		{"site under a platform public suffix",
			[][2]string{{"Acme Labs", "acme.github.io"}},
			`{"name":"Acme Labs","domain":"docs.acme.github.io"}`, 1},
		{"regional consumer mail domain",
			[][2]string{{"Example Labs", "yahoo.co.uk"}},
			`{"name":"Example Labs","domain":"mail.yahoo.co.uk"}`, 1},
		{"several organizations share the domain",
			[][2]string{{"Example Labs", "example.com"}, {"Example Ventures", "ventures.example.com"}},
			`{"name":"Example Labs","domain":"eu.example.com"}`, 2},
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
			assert.Len(candidatesOf(t, requests[0]), test.candidates, "the whole shortlist is asked about")
			questions, ok := requests[0]["questions"].(map[string]any)
			require.True(ok)
			assert.Contains(questions, orgresolution.QuestionOrgRef)
		})
	}
}

func TestPreparerCountsDomainSharersBeyondTheShortlist(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fake := newFakeJev(t, map[string]float64{"candidate_1": 0.95}, 0)
	f := newFixture(t, fake)
	labs := f.organization(t, "Example Labs", "example.com")
	// Northwind's sixth domain is on example.com: past the five domains a
	// shortlist candidate carries, and its name shares nothing.
	northwind := f.organization(t, "Northwind Traders", "northwind.example")
	for _, domain := range []string{"a.northwind.example", "b.northwind.example", "c.northwind.example",
		"d.northwind.example", "ops.example.com"} {
		_, err := f.store.RecordOrganizationResolutionAliasContext(t.Context(), store.OrganizationAliasInput{
			OrganizationID: northwind.ID, Name: "Northwind Traders", Domain: domain,
			Model: "fixture", Confidence: 0.9,
		})
		require.NoError(err)
	}
	shortlist, err := f.store.OrganizationShortlistContext(t.Context(),
		personfacts.OrganizationReference{Name: "Example Labs", Domain: "eu.example.com"})
	require.NoError(err)
	require.Len(shortlist.Candidates, 1, "the shortlist alone sees one sharer")
	assert.Equal(labs.ID, shortlist.Candidates[0].OrganizationID)

	results, err := f.preparer().Prepare(t.Context(), f.personID, []personfacts.ProposedClaim{
		f.claim(`{"name":"Example Labs","domain":"eu.example.com"}`, "Engineer", "uncapped"),
	}, nil)
	require.NoError(err)
	require.Len(results, 1)
	assert.Equal(orgresolution.OutcomeAlias, results[0].Outcome, "two organizations on the domain: Jev decides")
	assert.Len(fake.requests(), 1)
}

func TestPreparerCountsARejectedSharerAsAmbiguity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fake := newFakeJev(t, map[string]float64{"candidate_1": 0.95}, 0)
	f := newFixture(t, fake)
	f.organization(t, "Example Labs", "example.com")
	ventures := f.organization(t, "Example Ventures", "ventures.example.com")
	_, err := f.store.RecordOrganizationMatchReviewContext(t.Context(), store.OrganizationMatchReviewInput{
		OrganizationID: ventures.ID, Name: "Example Labs", Domain: "eu.example.com",
		Model: "fixture", Probability: 0.6,
	})
	require.NoError(err)
	reviews, err := f.store.ListOrganizationMatchReviewsContext(t.Context(), 10)
	require.NoError(err)
	require.Len(reviews, 1)
	_, err = f.store.RejectOrganizationMatchReviewContext(t.Context(), reviews[0].ID, "user")
	require.NoError(err)

	results, err := f.preparer().Prepare(t.Context(), f.personID, []personfacts.ProposedClaim{
		f.claim(`{"name":"Example Labs","domain":"eu.example.com"}`, "Engineer", "rejected"),
	}, nil)
	require.NoError(err)
	require.Len(results, 1)
	assert.Equal(orgresolution.OutcomeAlias, results[0].Outcome, "a rejection does not make the domain unique")
	requests := fake.requests()
	require.Len(requests, 1)
	assert.Len(candidatesOf(t, requests[0]), 1, "the rejected organization stays off the shortlist")
}

func candidatesOf(t *testing.T, request map[string]any) map[string]any {
	t.Helper()
	state, ok := request["state"].(map[string]any)
	require.True(t, ok)
	candidates, ok := state["candidates"].(map[string]any)
	require.True(t, ok)
	return candidates
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
		f.claim(`{"name":"Example Labs","domain":"eu.example.com"}`, "Engineer", "lost"),
	}, lost)
	require.ErrorIs(err, store.ErrOrganizationWriteFenced)
	assert.Empty(fake.requests())
	profile, err := f.store.GetOrganizationProfileContext(t.Context(), labs.ID, false)
	require.NoError(err)
	assert.Empty(profile.Names, "no alias for a lost lease")
}
