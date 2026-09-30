package store_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/personfacts"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func createShortlistOrganization(t *testing.T, st *store.Store, name, domain string) *store.Organization {
	t.Helper()
	input := store.OrganizationInput{Name: name, Kind: store.OrganizationKindCompany}
	if domain != "" {
		input.PrimaryDomain = &domain
	}
	organization, err := st.CreateOrganizationContext(t.Context(), input)
	require.NoError(t, err)
	return organization
}

func shortlistIDs(shortlist *store.OrganizationShortlist) []int64 {
	ids := make([]int64, len(shortlist.Candidates))
	for i, candidate := range shortlist.Candidates {
		ids[i] = candidate.OrganizationID
	}
	return ids
}

func TestOrganizationShortlistExactLookupDecidesWithoutCandidates(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	labs := createShortlistOrganization(t, st, "Example Labs", "")

	shortlist, err := st.OrganizationShortlistContext(t.Context(),
		personfacts.OrganizationReference{Name: "  example   LABS "})
	require.NoError(err)
	assert.Equal(store.OrganizationReused, shortlist.Status)
	assert.Equal([]int64{labs.ID}, shortlist.MatchedIDs)
	assert.Empty(shortlist.Candidates)
}

func TestOrganizationShortlistMissFindsNearNames(t *testing.T) {
	st := testutil.NewTestStore(t)
	labs := createShortlistOrganization(t, st, "Example Labs", "")
	unrelated := createShortlistOrganization(t, st, "Northwind Traders", "")

	tests := []struct {
		name    string
		ref     personfacts.OrganizationReference
		signals []string
	}{
		{"legal suffix", personfacts.OrganizationReference{Name: "Example Labs, Inc."},
			[]string{store.OrganizationSignalPrefix, store.OrganizationSignalTokenOverlap, store.OrganizationSignalTrigram}},
		{"accelerator batch tag", personfacts.OrganizationReference{Name: "Example Labs (YC W21)"},
			[]string{store.OrganizationSignalPrefix, store.OrganizationSignalTokenOverlap, store.OrganizationSignalTrigram}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			shortlist, err := st.OrganizationShortlistContext(t.Context(), test.ref)
			require.NoError(err)
			assert.Equal(store.OrganizationCreated, shortlist.Status)
			assert.Empty(shortlist.MatchedIDs)
			require.Equal([]int64{labs.ID}, shortlistIDs(shortlist))
			assert.NotContains(shortlistIDs(shortlist), unrelated.ID)
			assert.Equal("Example Labs", shortlist.Candidates[0].Name)
			assert.Equal(test.signals, shortlist.Candidates[0].Signals)
		})
	}
}

func TestOrganizationShortlistMatchesAlternateNamesAndDomainSiblings(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	renamed := createShortlistOrganization(t, st, "Contoso Group", "")
	_, err := st.ReplaceOrganizationProfileContext(t.Context(), renamed.ID, renamed.Revision,
		store.OrganizationProfileInput{Names: []store.OrganizationNameInput{{
			Name: "Fabrikam Robotics", NameKind: store.OrganizationNameKindFormer,
			Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceUser},
		}}})
	require.NoError(err)
	sibling := createShortlistOrganization(t, st, "Tailspin", "tailspin.example")

	shortlist, err := st.OrganizationShortlistContext(t.Context(),
		personfacts.OrganizationReference{Name: "Fabrikam Robotics Ltd"})
	require.NoError(err)
	require.Equal([]int64{renamed.ID}, shortlistIDs(shortlist))
	assert.Equal([]string{"Fabrikam Robotics"}, shortlist.Candidates[0].OtherNames)

	shortlist, err = st.OrganizationShortlistContext(t.Context(),
		personfacts.OrganizationReference{Name: "Tailspin Europe", Domain: "eu.tailspin.example"})
	require.NoError(err)
	assert.Equal(store.OrganizationCreated, shortlist.Status)
	require.Equal([]int64{sibling.ID}, shortlistIDs(shortlist))
	assert.Contains(shortlist.Candidates[0].Signals, store.OrganizationSignalDomainSibling)
	assert.Equal([]string{"tailspin.example"}, shortlist.Candidates[0].Domains)
}

func TestOrganizationShortlistIsCappedAndOrderedByStrength(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	exact := createShortlistOrganization(t, st, "Example Labs", "")
	for i := range 11 {
		createShortlistOrganization(t, st, fmt.Sprintf("Example Labs Division %d", i), "")
	}

	shortlist, err := st.OrganizationShortlistContext(t.Context(),
		personfacts.OrganizationReference{Name: "Example Labs GmbH"})
	require.NoError(err)
	require.Len(shortlist.Candidates, store.MaxOrganizationShortlist)
	assert.Equal(exact.ID, shortlist.Candidates[0].OrganizationID, "the closest name leads")
}

func TestOrganizationShortlistLeavesUnrelatedNamesOut(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	createShortlistOrganization(t, st, "Example Labs", "labs.example")

	shortlist, err := st.OrganizationShortlistContext(t.Context(),
		personfacts.OrganizationReference{Name: "Northwind Traders", Domain: "northwind.example"})
	require.NoError(err)
	assert.Equal(store.OrganizationCreated, shortlist.Status)
	assert.Empty(shortlist.Candidates)
}
