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

func TestSharesRegistrableDomain(t *testing.T) {
	tests := []struct {
		name   string
		domain string
		others []string
		want   bool
	}{
		{"same host", "example.com", []string{"example.com"}, true},
		{"subdomain of the other", "mail.example.com", []string{"example.com"}, true},
		{"sibling subdomains", "eu.example.com", []string{"us.example.com"}, true},
		{"any of several", "example.com", []string{"other.example", "www.example.com"}, true},
		{"different registrable domain", "example.com", []string{"example.org"}, false},
		{"public suffix subdomain", "mail.example.co.uk", []string{"example.co.uk"}, true},
		{"only the public suffix is shared", "example.co.uk", []string{"other.co.uk"}, false},
		{"consumer mail domain", "gmail.com", []string{"gmail.com"}, false},
		{"no domain", "", []string{"example.com"}, false},
		{"no others", "example.com", nil, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, store.SharesRegistrableDomain(test.domain, test.others))
		})
	}
}

func TestOrganizationDomainAliasIsARuleAliasThatSurvivesMerges(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	labs := createShortlistOrganization(t, st, "Example Labs", "example.com")
	ref := personfacts.OrganizationReference{Name: "Example Labs Europe", Domain: "eu.example.com"}

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

func TestOrganizationDomainAliasRefusesADomainTheOrganizationDoesNotShare(t *testing.T) {
	tests := []struct {
		name               string
		organizationDomain string
		domain             string
	}{
		{"different registrable domain", "example.com", "example.org"},
		{"only the public suffix is shared", "other.co.uk", "example.co.uk"},
		{"consumer mail domain", "gmail.com", "gmail.com"},
		{"organization without a domain", "", "example.com"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			st := testutil.NewTestStore(t)
			labs := createShortlistOrganization(t, st, "Example Labs", test.organizationDomain)

			_, err := st.RecordOrganizationDomainAliasContext(t.Context(), store.OrganizationDomainAliasInput{
				OrganizationID: labs.ID, Name: "Example Labs Europe", Domain: test.domain,
			})
			require.ErrorIs(err, store.ErrOrganizationInvalid)
			profile, err := st.GetOrganizationProfileContext(t.Context(), labs.ID, false)
			require.NoError(err)
			assert.Empty(profile.Names, "nothing is written")
		})
	}
}
