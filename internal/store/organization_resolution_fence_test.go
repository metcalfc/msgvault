package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/personfacts"
)

func TestOrganizationMatchReviewsMergeTheSameWayInEitherDirection(t *testing.T) {
	earlier := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Hour)
	type side struct {
		status    string
		decidedAt time.Time
	}
	tests := []struct {
		name     string
		left     side
		right    side
		want     string
		resolves bool
	}{
		{"accepted beats pending", side{OrganizationMatchAccepted, earlier}, side{status: OrganizationMatchPending},
			OrganizationMatchAccepted, true},
		{"rejected beats pending", side{OrganizationMatchRejected, earlier}, side{status: OrganizationMatchPending},
			OrganizationMatchRejected, false},
		{"a later rejection beats an acceptance", side{OrganizationMatchAccepted, earlier},
			side{OrganizationMatchRejected, later}, OrganizationMatchRejected, true},
		{"a later acceptance beats a rejection", side{OrganizationMatchRejected, earlier},
			side{OrganizationMatchAccepted, later}, OrganizationMatchAccepted, true},
	}
	for _, test := range tests {
		for _, leftSurvives := range []bool{true, false} {
			name := test.name + " / left org survives"
			if !leftSurvives {
				name = test.name + " / right org survives"
			}
			t.Run(name, func(t *testing.T) {
				assert := assert.New(t)
				require := require.New(t)
				st := newOrganizationMergeStore(t)
				left := createPersonFactOrganization(t, st, "Northwind Traders", "")
				right := createPersonFactOrganization(t, st, "Northwind Holdings", "")
				decide := func(organization *Organization, wanted side) {
					t.Helper()
					_, err := st.RecordOrganizationMatchReviewContext(t.Context(), OrganizationMatchReviewInput{
						OrganizationID: organization.ID, Name: "Northwind Trading", Model: "jev-1.13.0", Probability: 0.6,
					})
					require.NoError(err)
					var id int64
					require.NoError(st.db.QueryRowContext(t.Context(), `
						SELECT id FROM organization_match_reviews WHERE organization_id = ?`,
						organization.ID).Scan(&id))
					switch wanted.status {
					case OrganizationMatchAccepted:
						_, err = st.AcceptOrganizationMatchReviewContext(t.Context(), id, "user")
					case OrganizationMatchRejected:
						_, err = st.RejectOrganizationMatchReviewContext(t.Context(), id, "user")
					default:
						return
					}
					require.NoError(err)
					_, err = st.db.ExecContext(t.Context(),
						`UPDATE organization_match_reviews SET decided_at = ? WHERE id = ?`, wanted.decidedAt, id)
					require.NoError(err)
				}
				decide(left, test.left)
				decide(right, test.right)
				survivor, losing := left, right
				if !leftSurvives {
					survivor, losing = right, left
				}
				mergeOrganizationFixture(t, st, survivor, losing)

				var statuses []string
				rows, err := st.db.QueryContext(t.Context(), `
					SELECT status FROM organization_match_reviews WHERE organization_id = ?`, survivor.ID)
				require.NoError(err)
				for rows.Next() {
					var status string
					require.NoError(rows.Scan(&status))
					statuses = append(statuses, status)
				}
				require.NoError(rows.Err())
				require.NoError(rows.Close())
				assert.Equal([]string{test.want}, statuses, "one review, the same answer either way")

				shortlist, err := st.OrganizationShortlistContext(t.Context(), organizationShortlistRef("Northwind Trading"))
				require.NoError(err)
				if test.resolves {
					assert.Equal(OrganizationReused, shortlist.Status, "an accepted review's alias survives the merge")
					assert.Equal([]int64{survivor.ID}, shortlist.MatchedIDs)
				} else {
					assert.Equal(OrganizationCreated, shortlist.Status)
					for _, candidate := range shortlist.Candidates {
						assert.NotEqual(survivor.ID, candidate.OrganizationID, "the rejection still blocks the shortlist")
					}
				}
			})
		}
	}
}

func TestOppositeTitleMappingsStayOneRoleAfterAMerge(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := newOrganizationMergeStore(t)
	survivor := createPersonFactOrganization(t, st, "Example Capital", "")
	losing := createPersonFactOrganization(t, st, "Example Capital Partners", "")
	recordTitleAliasFixture(t, st, survivor.ID, "GP", "Partner")
	recordTitleAliasFixture(t, st, survivor.ID, "Associate", "Analyst")
	recordTitleAliasFixture(t, st, losing.ID, "Partner", "GP")
	recordTitleAliasFixture(t, st, losing.ID, "Analyst", "Investment Analyst")

	mergeOrganizationFixture(t, st, survivor, losing)

	canonical, err := st.EmploymentTitleCanonicalContext(t.Context(), survivor.ID,
		[]string{"GP", "Partner", "Associate", "Analyst", "Investment Analyst"})
	require.NoError(err)
	assert.Equal(map[string]string{
		"gp": "Partner", "associate": "Analyst", "investment analyst": "Analyst",
	}, canonical, "opposite mappings keep the survivor's canonical title, and no equivalence is dropped")
}

func TestResolutionWritesFollowAMergeThatLandedFirst(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := newOrganizationMergeStore(t)
	survivor := createPersonFactOrganization(t, st, "Example Labs", "")
	merged := createPersonFactOrganization(t, st, "Example Labs Old", "")
	mergeOrganizationFixture(t, st, survivor, merged)

	recordTitleAliasFixture(t, st, merged.ID, "General Partner", "Partner")
	canonical, err := st.EmploymentTitleCanonicalContext(t.Context(), survivor.ID, []string{"General Partner"})
	require.NoError(err)
	assert.Equal(map[string]string{"general partner": "Partner"}, canonical)

	_, err = st.RecordOrganizationResolutionAliasContext(t.Context(), OrganizationAliasInput{
		OrganizationID: merged.ID, Name: "Example Labs, Inc.", Model: "jev-1.13.0", Confidence: 0.9,
	})
	require.NoError(err)
	shortlist, err := st.OrganizationShortlistContext(t.Context(), organizationShortlistRef("Example Labs, Inc."))
	require.NoError(err)
	assert.Equal([]int64{survivor.ID}, shortlist.MatchedIDs)

	_, err = st.RecordOrganizationMatchReviewContext(t.Context(), OrganizationMatchReviewInput{
		OrganizationID: merged.ID, Name: "Example Labs Europe", Model: "jev-1.13.0", Probability: 0.6,
	})
	require.NoError(err)
	reviews, err := st.ListOrganizationMatchReviewsContext(t.Context(), 10)
	require.NoError(err)
	require.Len(reviews, 1)
	assert.Equal(survivor.ID, reviews[0].OrganizationID, "the review lands where it can be seen")
}

func TestSweepFencedOrganizationWritesNeedAnUnexpiredLease(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st, personID := newPersonFactLedgerStore(t)
	labs := createPersonFactOrganization(t, st, "Example Labs", "")
	leaseUntil, leaseArg := personSweepLeaseExpiration(10 * time.Minute)
	_, err := st.db.ExecContext(t.Context(), st.Rebind(`
		INSERT INTO person_sweep_work (
			person_id, dirty_through_sequence, available_at, attempt_count,
			lease_owner, lease_until, lease_fence
		) VALUES (?, 0, CURRENT_TIMESTAMP, 0, 'sweep-worker', `+leaseUntil+`, 3)`), personID, leaseArg)
	require.NoError(err)
	fence := &personfacts.WriteFence{
		Kind: personfacts.FencePersonSweep, PersonID: personID, Owner: "sweep-worker", Fence: 3,
	}
	write := func(name string, fence *personfacts.WriteFence) error {
		t.Helper()
		_, err := st.RecordOrganizationResolutionAliasContext(t.Context(), OrganizationAliasInput{
			OrganizationID: labs.ID, Name: name, Model: "jev-1.13.0", Confidence: 0.9, Fence: fence,
		})
		return err
	}

	require.NoError(write("Example Labs, Inc.", fence), "a held lease lets the write through")
	stale := *fence
	stale.Fence = 2
	require.ErrorIs(write("Example Labs GmbH", &stale), ErrOrganizationWriteFenced, "a superseded fence is refused")

	_, err = st.db.ExecContext(t.Context(), `UPDATE person_sweep_work SET lease_until = created_at WHERE person_id = ?`, personID)
	require.NoError(err)
	require.ErrorIs(write("Example Labs LLC", fence), ErrOrganizationWriteFenced, "an expired lease blocks the write")
	_, err = st.RecordOrganizationMatchReviewContext(t.Context(), OrganizationMatchReviewInput{
		OrganizationID: labs.ID, Name: "Example Labs Europe", Model: "jev-1.13.0", Probability: 0.6, Fence: fence,
	})
	require.ErrorIs(err, ErrOrganizationWriteFenced)
	_, err = st.RecordEmploymentTitleAliasContext(t.Context(), EmploymentTitleAliasInput{
		OrganizationID: labs.ID, Title: "GP", CanonicalTitle: "Partner", Model: "jev-1.13.0",
		Confidence: 0.9, Fence: fence,
	})
	require.ErrorIs(err, ErrOrganizationWriteFenced)

	profile, err := st.GetOrganizationProfileContext(t.Context(), labs.ID, false)
	require.NoError(err)
	require.Len(profile.Names, 1)
	assert.Equal("Example Labs, Inc.", profile.Names[0].Name, "only the fenced-in write landed")
}

func TestEnrichmentFencedOrganizationWritesNeedAnUnexpiredLease(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := newEnrichmentResultFixture(t)
	labs := createPersonFactOrganization(t, f.store, "Example Labs", "")
	token := f.attempt.Token
	fence := &personfacts.WriteFence{
		Kind: personfacts.FencePersonEnrichment, PersonID: token.WorkPersonID, Owner: token.Owner,
		Fence: token.Fence, RunID: token.RunID, ProfileFingerprint: token.ProfileFingerprint,
		AttemptID: token.AttemptID,
	}
	_, err := f.store.RecordEmploymentTitleAliasContext(t.Context(), EmploymentTitleAliasInput{
		OrganizationID: labs.ID, Title: "GP", CanonicalTitle: "Partner", Model: "jev-1.13.0",
		Confidence: 0.9, Fence: fence,
	})
	require.NoError(err, "a held lease lets the write through")

	expired := f.now.Add(48 * time.Hour)
	SetPersonEnrichmentClockForTest(f.store, func() time.Time { return expired })
	_, err = f.store.RecordEmploymentTitleAliasContext(t.Context(), EmploymentTitleAliasInput{
		OrganizationID: labs.ID, Title: "MP", CanonicalTitle: "Partner", Model: "jev-1.13.0",
		Confidence: 0.9, Fence: fence,
	})
	require.ErrorIs(err, ErrOrganizationWriteFenced, "an expired lease blocks the write")
	err = f.store.RenewLease(t.Context(), token, expired.Add(time.Minute))
	require.ErrorIs(err, ErrStaleLease, "renewal never revives an expired lease")

	canonical, err := f.store.EmploymentTitleCanonicalContext(t.Context(), labs.ID, []string{"GP", "MP"})
	require.NoError(err)
	assert.Equal(map[string]string{"gp": "Partner"}, canonical)
}
