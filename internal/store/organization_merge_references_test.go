package store

import (
	"errors"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/personfacts"
)

func TestEveryOrganizationReferenceHasAMergePolicy(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st, err := OpenForTest(filepath.Join(t.TempDir(), "organization-inventory.db"))
	require.NoError(err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(st.InitSchema())

	// A column references an organization when it has a foreign key to
	// organizations or its name says so (some references carry no key).
	rows, err := st.db.QueryContext(t.Context(), `
		SELECT m.name, f."from" FROM sqlite_master m, pragma_foreign_key_list(m.name) f
		WHERE m.type = 'table' AND f."table" = 'organizations'
		UNION
		SELECT m.name, c.name FROM sqlite_master m, pragma_table_info(m.name) c
		WHERE m.type = 'table'
		  AND (c.name = 'organization_id' OR c.name LIKE '%\_organization\_id' ESCAPE '\')`)
	require.NoError(err)
	defer func() { _ = rows.Close() }()
	var found []string
	for rows.Next() {
		var table, column string
		require.NoError(rows.Scan(&table, &column))
		found = append(found, table+"."+column)
	}
	require.NoError(rows.Err())
	sort.Strings(found)

	classified := make([]string, 0, len(organizationMergePolicy))
	for reference := range organizationMergePolicy {
		classified = append(classified, reference)
	}
	sort.Strings(classified)
	assert.Equal(classified, found,
		"every organization reference needs an entry in organizationMergePolicy saying what a merge does to it")
}

func newOrganizationMergeStore(t *testing.T) *Store {
	t.Helper()
	st, _ := newPersonFactLedgerStore(t)
	return st
}

func mergeOrganizationFixture(t *testing.T, st *Store, survivor, losing *Organization) {
	t.Helper()
	survivor, err := st.GetOrganizationContext(t.Context(), survivor.ID)
	require.NoError(t, err)
	losing, err = st.GetOrganizationContext(t.Context(), losing.ID)
	require.NoError(t, err)
	_, err = st.MergeOrganizationsContext(t.Context(), survivor.ID, survivor.Revision, losing.ID, losing.Revision)
	require.NoError(t, err)
}

func organizationShortlistRef(name string) personfacts.OrganizationReference {
	return personfacts.OrganizationReference{Name: name}
}

func recordTitleAliasFixture(t *testing.T, st *Store, organizationID int64, title, canonical string) {
	t.Helper()
	_, err := st.RecordEmploymentTitleAliasContext(t.Context(), EmploymentTitleAliasInput{
		OrganizationID: organizationID, Title: title, CanonicalTitle: canonical,
		Model: "jev-1.13.0", Confidence: 0.9,
	})
	require.NoError(t, err)
}

func TestAcceptOrganizationMatchCommitsTheMergeOnlyWithItsDecision(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := newOrganizationMergeStore(t)
	labs := createPersonFactOrganization(t, st, "Example Labs", "")
	created := createPersonFactOrganization(t, st, "Example Labs Europe", "")
	_, err := st.RecordOrganizationMatchReviewContext(t.Context(), OrganizationMatchReviewInput{
		OrganizationID: labs.ID, Name: "Example Labs Europe", Model: "jev-1.13.0", Probability: 0.7,
	})
	require.NoError(err)
	reviews, err := st.ListOrganizationMatchReviewsContext(t.Context(), 10)
	require.NoError(err)
	require.Len(reviews, 1)
	reviewID := reviews[0].ID

	stopped := errors.New("stopped after the merge")
	failing := withOrganizationMatchAcceptFailpoint(t.Context(), func(stage string) error {
		if stage == "merged" {
			return stopped
		}
		return nil
	})
	_, err = st.AcceptOrganizationMatchReviewContext(failing, reviewID, "user")
	require.ErrorIs(err, stopped)
	unmerged, err := st.GetOrganizationContext(t.Context(), created.ID)
	require.NoError(err)
	assert.Nil(unmerged.MergedIntoID, "a decision that does not commit takes the merge with it")
	reviews, err = st.ListOrganizationMatchReviewsContext(t.Context(), 10)
	require.NoError(err)
	assert.Len(reviews, 1, "the review is still pending")

	_, err = st.RejectOrganizationMatchReviewContext(t.Context(), reviewID, "user")
	require.NoError(err)
	_, err = st.AcceptOrganizationMatchReviewContext(t.Context(), reviewID, "user")
	require.ErrorIs(err, ErrOrganizationMatchReviewStateChanged)
	unmerged, err = st.GetOrganizationContext(t.Context(), created.ID)
	require.NoError(err)
	assert.Nil(unmerged.MergedIntoID, "an accept that loses to a reject merges nothing")
}

func TestOrganizationMergeCarriesReviewsAndRejectionsToTheSurvivor(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := newOrganizationMergeStore(t)
	survivor := createPersonFactOrganization(t, st, "Northwind Traders", "")
	losing := createPersonFactOrganization(t, st, "Northwind Holdings", "")
	for _, input := range []OrganizationMatchReviewInput{
		{OrganizationID: losing.ID, Name: "Northwind Trading", Model: "jev-1.13.0", Probability: 0.6},
		{OrganizationID: survivor.ID, Name: "Northwind Trading", Model: "jev-1.13.0", Probability: 0.55},
		{OrganizationID: losing.ID, Name: "Northwind Logistics", Model: "jev-1.13.0", Probability: 0.6},
	} {
		_, err := st.RecordOrganizationMatchReviewContext(t.Context(), input)
		require.NoError(err)
	}
	reviews, err := st.ListOrganizationMatchReviewsContext(t.Context(), 10)
	require.NoError(err)
	for _, review := range reviews {
		if review.OrganizationID == losing.ID && review.ProposedName == "Northwind Trading" {
			_, err = st.RejectOrganizationMatchReviewContext(t.Context(), review.ID, "user")
			require.NoError(err)
		}
	}

	mergeOrganizationFixture(t, st, survivor, losing)

	reviews, err = st.ListOrganizationMatchReviewsContext(t.Context(), 10)
	require.NoError(err)
	require.Len(reviews, 1, "the losing side's pending review moves to the survivor")
	assert.Equal(survivor.ID, reviews[0].OrganizationID)
	assert.Equal("Northwind Logistics", reviews[0].ProposedName)
	shortlist, err := st.OrganizationShortlistContext(t.Context(), organizationShortlistRef("Northwind Trading"))
	require.NoError(err)
	for _, candidate := range shortlist.Candidates {
		assert.NotEqual(survivor.ID, candidate.OrganizationID,
			"a rejection made before the merge still keeps the organization off the shortlist")
	}
}

func TestTitleAliasesSurviveChainedMergesAndCollapse(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := newOrganizationMergeStore(t)
	first := createPersonFactOrganization(t, st, "Example Labs One", "")
	second := createPersonFactOrganization(t, st, "Example Labs Two", "")
	third := createPersonFactOrganization(t, st, "Example Labs Three", "")
	recordTitleAliasFixture(t, st, first.ID, "GP", "Partner")
	recordTitleAliasFixture(t, st, second.ID, "Partner", "Managing Partner")

	mergeOrganizationFixture(t, st, second, first)
	mergeOrganizationFixture(t, st, third, second)

	canonical, err := st.EmploymentTitleCanonicalContext(t.Context(), third.ID, []string{"GP", "Partner", "Associate"})
	require.NoError(err)
	assert.Equal(map[string]string{"gp": "Managing Partner", "partner": "Managing Partner"}, canonical,
		"aliases from two merges back still apply, collapsed to one canonical title")

	var rows int
	require.NoError(st.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM organization_title_aliases
		WHERE organization_id = ? AND canonical_title_normalized = 'managing partner'`, third.ID).Scan(&rows))
	assert.Equal(2, rows, "stored chains are collapsed, not only resolved on read")
}

func TestLaterTitleAliasOnTheSurvivorReachesInheritedAliases(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := newOrganizationMergeStore(t)
	survivor := createPersonFactOrganization(t, st, "Example Capital", "")
	losing := createPersonFactOrganization(t, st, "Example Capital Partners", "")
	recordTitleAliasFixture(t, st, losing.ID, "GP", "Partner")
	mergeOrganizationFixture(t, st, survivor, losing)
	recordTitleAliasFixture(t, st, survivor.ID, "Partner", "Managing Partner")

	canonical, err := st.EmploymentTitleCanonicalContext(t.Context(), survivor.ID, []string{"GP", "Partner"})
	require.NoError(err)
	assert.Equal(map[string]string{"gp": "Managing Partner", "partner": "Managing Partner"}, canonical)
}
