package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/personfacts"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestOrganizationResolutionAliasMakesTheExactLookupResolveAndIsIdempotent(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	labs := createShortlistOrganization(t, st, "Example Labs", "")
	ref := personfacts.OrganizationReference{Name: "Example Labs, Inc.", Domain: "examplelabs.example"}

	before, err := st.OrganizationShortlistContext(t.Context(), ref)
	require.NoError(err)
	require.Equal(store.OrganizationCreated, before.Status)

	input := store.OrganizationAliasInput{
		OrganizationID: labs.ID, Name: ref.Name, Domain: ref.Domain, Model: "jev-1.13.0", Confidence: 0.93,
	}
	added, err := st.RecordOrganizationResolutionAliasContext(t.Context(), input)
	require.NoError(err)
	assert.Equal(store.OrganizationAliasResult{NameAdded: true, DomainAdded: true}, added)

	after, err := st.OrganizationShortlistContext(t.Context(), ref)
	require.NoError(err)
	assert.Equal(store.OrganizationReused, after.Status)
	assert.Equal([]int64{labs.ID}, after.MatchedIDs)

	again, err := st.RecordOrganizationResolutionAliasContext(t.Context(), input)
	require.NoError(err)
	assert.Equal(store.OrganizationAliasResult{}, again, "the same alias twice writes nothing")

	profile, err := st.GetOrganizationProfileContext(t.Context(), labs.ID, false)
	require.NoError(err)
	require.Len(profile.Names, 1)
	name := profile.Names[0]
	assert.Equal("Example Labs, Inc.", name.Name)
	assert.Equal(store.OrganizationNameKindAlias, name.NameKind)
	assert.Equal(store.ProvenanceSystem, name.Envelope.Source)
	require.NotNil(name.Envelope.SourceRef)
	assert.Equal("jev:organization_resolution:jev-1.13.0", *name.Envelope.SourceRef)
	require.NotNil(name.Envelope.Confidence)
	assert.InDelta(0.93, *name.Envelope.Confidence, 1e-9)
	require.Len(profile.Identifiers, 1)
	assert.Equal("examplelabs.example", profile.Identifiers[0].NormalizedValue)

	reloaded, err := st.GetOrganizationContext(t.Context(), labs.ID)
	require.NoError(err)
	assert.Equal(labs.Revision+1, reloaded.Revision, "only the write that added keys bumps the revision")

	merged := createShortlistOrganization(t, st, "Example Labs Old", "")
	_, err = st.MergeOrganizationsContext(t.Context(), labs.ID, reloaded.Revision, merged.ID, merged.Revision)
	require.NoError(err)
	input.OrganizationID = merged.ID
	input.Name = "Example Labs Old Co"
	_, err = st.RecordOrganizationResolutionAliasContext(t.Context(), input)
	require.NoError(err)
	redirected, err := st.OrganizationShortlistContext(t.Context(),
		personfacts.OrganizationReference{Name: "Example Labs Old Co"})
	require.NoError(err)
	assert.Equal([]int64{labs.ID}, redirected.MatchedIDs, "an alias aimed at a merged redirect lands on the survivor")
}

func TestEmploymentTitleAliasesResolveToOneCanonicalTitle(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	labs := createShortlistOrganization(t, st, "Example Labs", "")
	record := func(title, canonical string) bool {
		t.Helper()
		written, err := st.RecordEmploymentTitleAliasContext(t.Context(), store.EmploymentTitleAliasInput{
			OrganizationID: labs.ID, Title: title, CanonicalTitle: canonical,
			Model: "jev-1.13.0", Confidence: 0.9,
		})
		require.NoError(err)
		return written
	}

	assert.True(record("General Partner", "Partner"))
	assert.False(record("general   partner", "Partner"), "an alias is written once")
	assert.False(record("Partner", "Partner"), "a title is never its own alias")
	assert.True(record("GP", "General Partner"), "the canonical side is resolved first")
	assert.True(record("Partner", "Managing Partner"))

	canonical, err := st.EmploymentTitleCanonicalContext(t.Context(), labs.ID,
		[]string{"GP", "General Partner", "Partner", "Managing Partner", "Associate"})
	require.NoError(err)
	assert.Equal(map[string]string{
		"gp": "Managing Partner", "general partner": "Managing Partner", "partner": "Managing Partner",
	}, canonical, "earlier aliases follow the title that became an alias itself")
}

func TestPersonOrganizationTitlesListsKnownTitlesOnce(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	labs := createShortlistOrganization(t, st, "Example Labs", "")
	other := createShortlistOrganization(t, st, "Northwind Traders", "")
	participantID, err := st.EnsureParticipant("rowan@example.test", "Rowan Example", "example.test")
	require.NoError(err)
	person, _, err := st.CreatePersonFromParticipant(participantID)
	require.NoError(err)
	for _, employment := range []struct {
		organization int64
		title        string
	}{{labs.ID, "Partner"}, {labs.ID, "Venture Partner"}, {other.ID, "Advisor"}} {
		title := employment.title
		_, err := st.AddEmploymentContext(t.Context(), store.EmploymentInput{
			PersonID: person.ID, OrganizationID: employment.organization, Title: &title,
			IsCurrent: new(true), Source: store.ProvenanceUser,
		})
		require.NoError(err)
	}

	titles, err := st.PersonOrganizationTitlesContext(t.Context(), person.ID, labs.ID)
	require.NoError(err)
	assert.Equal([]string{"Partner", "Venture Partner"}, titles)

	_, err = st.RecordEmploymentTitleAliasContext(t.Context(), store.EmploymentTitleAliasInput{
		OrganizationID: labs.ID, Title: "Venture Partner", CanonicalTitle: "Partner",
		Model: "jev-1.13.0", Confidence: 0.9,
	})
	require.NoError(err)
	titles, err = st.PersonOrganizationTitlesContext(t.Context(), person.ID, labs.ID)
	require.NoError(err)
	assert.Equal([]string{"Partner"}, titles, "titles that are one role count once")
}

func TestOrganizationMatchReviewAcceptMergesAndRejectKeepsTheOrganizationOffTheShortlist(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	labs := createShortlistOrganization(t, st, "Example Labs", "")
	created := createShortlistOrganization(t, st, "Example Labs Europe", "eu.examplelabs.example")
	northwind := createShortlistOrganization(t, st, "Northwind Traders", "")

	recorded, err := st.RecordOrganizationMatchReviewContext(t.Context(), store.OrganizationMatchReviewInput{
		OrganizationID: labs.ID, Name: "Example Labs Europe", Domain: "eu.examplelabs.example",
		Model: "jev-1.13.0", Probability: 0.7,
	})
	require.NoError(err)
	assert.True(recorded)
	recorded, err = st.RecordOrganizationMatchReviewContext(t.Context(), store.OrganizationMatchReviewInput{
		OrganizationID: labs.ID, Name: "example labs  europe", Domain: "eu.examplelabs.example",
		Model: "jev-1.13.0", Probability: 0.6,
	})
	require.NoError(err)
	assert.False(recorded, "one review per organization, name, and domain")
	_, err = st.RecordOrganizationMatchReviewContext(t.Context(), store.OrganizationMatchReviewInput{
		OrganizationID: northwind.ID, Name: "Northwind Trading", Model: "jev-1.13.0", Probability: 0.55,
	})
	require.NoError(err)

	reviews, err := st.ListOrganizationMatchReviewsContext(t.Context(), 10)
	require.NoError(err)
	require.Len(reviews, 2)
	byOrganization := map[int64]store.OrganizationMatchReview{}
	for _, review := range reviews {
		byOrganization[review.OrganizationID] = review
	}
	accepting := byOrganization[labs.ID]
	assert.Equal("Example Labs", accepting.OrganizationName)
	assert.Equal("Example Labs Europe", accepting.ProposedName)
	require.NotNil(accepting.ProposedDomain)
	assert.Equal("eu.examplelabs.example", *accepting.ProposedDomain)
	require.NotNil(accepting.ProposedOrganizationID)
	assert.Equal(created.ID, *accepting.ProposedOrganizationID)
	assert.InDelta(0.7, accepting.Probability, 1e-9)
	assert.Equal("jev-1.13.0", accepting.Model)
	rejecting := byOrganization[northwind.ID]
	assert.Nil(rejecting.ProposedOrganizationID)

	decision, err := st.AcceptOrganizationMatchReviewContext(t.Context(), accepting.ID, "user")
	require.NoError(err)
	assert.Equal(store.OrganizationMatchAccepted, decision.Decision)
	assert.Equal(labs.ID, decision.OrganizationID)
	require.NotNil(decision.MergedOrganizationID)
	assert.Equal(created.ID, *decision.MergedOrganizationID)
	shortlist, err := st.OrganizationShortlistContext(t.Context(),
		personfacts.OrganizationReference{Name: "Example Labs Europe", Domain: "eu.examplelabs.example"})
	require.NoError(err)
	assert.Equal(store.OrganizationReused, shortlist.Status, "the accepted name and domain now resolve")
	assert.Equal([]int64{labs.ID}, shortlist.MatchedIDs)

	_, err = st.AcceptOrganizationMatchReviewContext(t.Context(), accepting.ID, "user")
	require.ErrorIs(err, store.ErrOrganizationMatchReviewStateChanged)

	shortlist, err = st.OrganizationShortlistContext(t.Context(),
		personfacts.OrganizationReference{Name: "Northwind Trading"})
	require.NoError(err)
	assert.Contains(shortlistIDs(shortlist), northwind.ID)
	decision, err = st.RejectOrganizationMatchReviewContext(t.Context(), rejecting.ID, "user")
	require.NoError(err)
	assert.Equal(store.OrganizationMatchRejected, decision.Decision)
	shortlist, err = st.OrganizationShortlistContext(t.Context(),
		personfacts.OrganizationReference{Name: "Northwind Trading"})
	require.NoError(err)
	assert.NotContains(shortlistIDs(shortlist), northwind.ID, "a rejected pair is never proposed again")

	_, err = st.RejectOrganizationMatchReviewContext(t.Context(), 999999, "user")
	require.ErrorIs(err, store.ErrOrganizationMatchReviewNotFound)
	reviews, err = st.ListOrganizationMatchReviewsContext(t.Context(), 10)
	require.NoError(err)
	assert.Empty(reviews)
}

func TestOrganizationDomainSettlement(t *testing.T) {
	tests := []struct {
		name               string
		organizationName   string
		organizationDomain string
		ref                personfacts.OrganizationReference
		settles            bool
	}{
		{"same name, subdomain", "Example Labs", "example.com",
			personfacts.OrganizationReference{Name: "Example Labs", Domain: "mail.example.com"}, true},
		{"legal suffix and punctuation", "Example Labs", "example.com",
			personfacts.OrganizationReference{Name: "EXAMPLE LABS, LLC", Domain: "example.com"}, true},
		{"multi-label public suffix", "Example Labs", "example.co.uk",
			personfacts.OrganizationReference{Name: "Example Labs", Domain: "mail.example.co.uk"}, true},
		{"different name", "Example Labs", "example.com",
			personfacts.OrganizationReference{Name: "Example Labs Europe", Domain: "eu.example.com"}, false},
		{"only the public suffix is shared", "Example Labs", "other.co.uk",
			personfacts.OrganizationReference{Name: "Example Labs", Domain: "example.co.uk"}, false},
		{"no registrable domain", "Example Labs", "co.uk",
			personfacts.OrganizationReference{Name: "Example Labs", Domain: "co.uk"}, false},
		{"consumer mail", "Example Labs", "gmx.de",
			personfacts.OrganizationReference{Name: "Example Labs", Domain: "gmx.de"}, false},
		{"platform", "Example Labs", "linkedin.com",
			personfacts.OrganizationReference{Name: "Example Labs", Domain: "https://www.linkedin.com/company/example"}, false},
		{"platform public suffix", "Example Labs", "example.github.io",
			personfacts.OrganizationReference{Name: "Example Labs", Domain: "example.github.io"}, false},
		{"no domain", "Example Labs", "example.com",
			personfacts.OrganizationReference{Name: "Example Labs"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			st := testutil.NewTestStore(t)
			labs := createShortlistOrganization(t, st, test.organizationName, test.organizationDomain)

			id, ok, err := st.OrganizationDomainSettlementContext(t.Context(), test.ref)
			require.NoError(err)
			assert.Equal(test.settles, ok)
			if test.settles {
				assert.Equal(labs.ID, id)
			}
		})
	}
}

func TestOrganizationDomainAliasIsARuleAliasThatSurvivesMerges(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	labs := createShortlistOrganization(t, st, "Example Labs", "example.com")
	ref := personfacts.OrganizationReference{Name: "Example Labs, Inc.", Domain: "eu.example.com"}

	input := store.OrganizationDomainAliasInput{OrganizationID: labs.ID, Name: ref.Name, Domain: ref.Domain}
	added, err := st.RecordOrganizationDomainAliasContext(t.Context(), input)
	require.NoError(err)
	assert.Equal(store.OrganizationAliasResult{NameAdded: true, DomainAdded: true}, added)
	again, err := st.RecordOrganizationDomainAliasContext(t.Context(), input)
	require.NoError(err)
	assert.Equal(store.OrganizationAliasResult{}, again, "the same alias twice writes nothing")

	lookup, err := st.OrganizationShortlistContext(t.Context(), ref)
	require.NoError(err)
	assert.Equal(store.OrganizationReused, lookup.Status)
	assert.Equal([]int64{labs.ID}, lookup.MatchedIDs)

	profile, err := st.GetOrganizationProfileContext(t.Context(), labs.ID, false)
	require.NoError(err)
	require.Len(profile.Names, 1)
	assert.Equal(store.ProvenanceSystem, profile.Names[0].Envelope.Source)
	require.NotNil(profile.Names[0].Envelope.SourceRef)
	assert.Equal(store.OrganizationDomainRuleSourceRef, *profile.Names[0].Envelope.SourceRef)
	assert.Nil(profile.Names[0].Envelope.Confidence, "a rule has no probability")

	survivor := createShortlistOrganization(t, st, "Example Holdings", "")
	reloaded, err := st.GetOrganizationContext(t.Context(), labs.ID)
	require.NoError(err)
	_, err = st.MergeOrganizationsContext(t.Context(), survivor.ID, survivor.Revision, reloaded.ID, reloaded.Revision)
	require.NoError(err)
	merged, err := st.OrganizationShortlistContext(t.Context(), ref)
	require.NoError(err)
	assert.Equal([]int64{survivor.ID}, merged.MatchedIDs, "the rule alias is carried to the survivor")
}

func TestOrganizationDomainAliasRefusesWhatTheRuleDoesNotSettle(t *testing.T) {
	tests := []struct {
		name               string
		organizationDomain string
		aliasName          string
		domain             string
	}{
		{"different registrable domain", "example.com", "Example Labs", "example.org"},
		{"different name", "example.com", "Example Labs Europe", "eu.example.com"},
		{"only the public suffix is shared", "other.co.uk", "Example Labs", "example.co.uk"},
		{"consumer mail domain", "gmail.com", "Example Labs", "gmail.com"},
		{"platform subdomain", "other.medium.com", "Example Labs", "example.medium.com"},
		{"organization without a domain", "", "Example Labs", "example.com"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			st := testutil.NewTestStore(t)
			labs := createShortlistOrganization(t, st, "Example Labs", test.organizationDomain)

			_, err := st.RecordOrganizationDomainAliasContext(t.Context(), store.OrganizationDomainAliasInput{
				OrganizationID: labs.ID, Name: test.aliasName, Domain: test.domain,
			})
			require.ErrorIs(err, store.ErrOrganizationInvalid)
			profile, err := st.GetOrganizationProfileContext(t.Context(), labs.ID, false)
			require.NoError(err)
			assert.Empty(profile.Names, "nothing is written")
			assert.Empty(profile.Identifiers, "nothing is written")
		})
	}
}

func TestOrganizationDomainSettlementRespectsARejectionInAnySpelling(t *testing.T) {
	for _, settling := range []string{"Acme Inc", "Acme, Inc.", "ACME"} {
		t.Run(settling, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			st := testutil.NewTestStore(t)
			acme := createShortlistOrganization(t, st, "Acme", "acme.example")
			ref := personfacts.OrganizationReference{Name: settling, Domain: "eu.acme.example"}
			_, ok, err := st.OrganizationDomainSettlementContext(t.Context(), ref)
			require.NoError(err)
			require.True(ok, "settles before the rejection")

			_, err = st.RecordOrganizationMatchReviewContext(t.Context(), store.OrganizationMatchReviewInput{
				OrganizationID: acme.ID, Name: "Acme Inc", Domain: "acme.example",
				Model: "fixture", Probability: 0.6,
			})
			require.NoError(err)
			reviews, err := st.ListOrganizationMatchReviewsContext(t.Context(), 10)
			require.NoError(err)
			require.Len(reviews, 1)
			_, err = st.RejectOrganizationMatchReviewContext(t.Context(), reviews[0].ID, "user")
			require.NoError(err)

			_, ok, err = st.OrganizationDomainSettlementContext(t.Context(), ref)
			require.NoError(err)
			assert.False(ok, "rejecting \"Acme Inc\" refuses every spelling of that name")
			_, err = st.RecordOrganizationDomainAliasContext(t.Context(), store.OrganizationDomainAliasInput{
				OrganizationID: acme.ID, Name: ref.Name, Domain: ref.Domain,
			})
			require.ErrorIs(err, store.ErrOrganizationInvalid)
		})
	}
}

func TestOrganizationDomainSettlementCountsOnlyActiveCompanies(t *testing.T) {
	tests := []struct {
		name   string
		retire func(*testing.T, *store.Store, *store.Organization)
	}{
		{"retired sharer", func(t *testing.T, st *store.Store, other *store.Organization) {
			t.Helper()
			domain := "ventures.example.com"
			_, err := st.ReplaceOrganizationContext(t.Context(), other.ID, other.Revision, store.OrganizationInput{
				Name: other.Name, Kind: store.OrganizationKindCompany, PrimaryDomain: &domain,
			}, true)
			require.NoError(t, err)
		}},
		{"merged sharer", func(t *testing.T, st *store.Store, other *store.Organization) {
			t.Helper()
			survivor := createShortlistOrganization(t, st, "Northwind Traders", "northwind.example")
			_, err := st.MergeOrganizationsContext(t.Context(), survivor.ID, survivor.Revision, other.ID, other.Revision)
			require.NoError(t, err)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			st := testutil.NewTestStore(t)
			labs := createShortlistOrganization(t, st, "Example Labs", "example.com")
			other := createShortlistOrganization(t, st, "Example Ventures", "ventures.example.com")
			ref := personfacts.OrganizationReference{Name: "Example Labs", Domain: "eu.example.com"}
			_, ok, err := st.OrganizationDomainSettlementContext(t.Context(), ref)
			require.NoError(err)
			require.False(ok, "two active companies share the domain")

			test.retire(t, st, other)
			id, ok, err := st.OrganizationDomainSettlementContext(t.Context(), ref)
			require.NoError(err)
			assert.True(ok, "only active companies count")
			assert.Equal(labs.ID, id)
		})
	}
}
