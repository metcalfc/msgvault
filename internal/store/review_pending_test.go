package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/correspondentkind"
)

func newPendingReviewStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenForTest(filepath.Join(t.TempDir(), "pending-reviews.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	require.NoError(t, st.InitSchema())
	return st
}

func pendingKinds(t *testing.T, st *Store) []PendingReviewKind {
	t.Helper()
	kinds, err := st.PendingReviewKindsContext(t.Context())
	require.NoError(t, err)
	return kinds
}

func TestPendingReviewKindsFollowEachQueue(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := newPendingReviewStore(t)
	assert.Empty(pendingKinds(t, st))

	// Identity: an open candidate waits; a decided one does not.
	left, err := st.EnsureParticipant("avery@example.test", "Avery Example", "example.test")
	require.NoError(err)
	right, err := st.EnsureParticipant("avery.e@example.org", "Avery Example", "example.org")
	require.NoError(err)
	value := "avery example"
	candidate, _, err := st.UpsertIdentityMatchCandidateContext(t.Context(), IdentityMatchCandidateInput{
		LeftKind: IdentityMatchParticipant, LeftID: left, RightKind: IdentityMatchParticipant, RightID: right,
		Basis: IdentityMatchDisplayName, NormalizedValue: &value,
		State: IdentityMatchStateCandidate, Source: ProvenanceSystem,
	})
	require.NoError(err)
	assert.Equal([]PendingReviewKind{PendingReviewIdentity}, pendingKinds(t, st))
	_, err = st.db.ExecContext(t.Context(), st.Rebind(
		`UPDATE identity_match_candidates SET state = 'rejected' WHERE id = ?`), candidate.ID)
	require.NoError(err)
	assert.Empty(pendingKinds(t, st))

	// Organization: a pending review waits until it is decided.
	organization, err := st.CreateOrganizationContext(t.Context(), OrganizationInput{
		Name: "Example Labs", Kind: OrganizationKindCompany,
	})
	require.NoError(err)
	_, err = st.RecordOrganizationMatchReviewContext(t.Context(), OrganizationMatchReviewInput{
		OrganizationID: organization.ID, Name: "Example Labs Europe", Model: "synthetic", Probability: 0.6,
	})
	require.NoError(err)
	assert.Equal([]PendingReviewKind{PendingReviewOrganization}, pendingKinds(t, st))
	_, err = st.db.ExecContext(t.Context(), `UPDATE organization_match_reviews
		SET status = 'rejected', decided_at = CURRENT_TIMESTAMP`)
	require.NoError(err)
	assert.Empty(pendingKinds(t, st))

	// Unclear correspondent: a Jev judgment waits until the user decides.
	desk, err := st.EnsureParticipant("desk@example.test", "Front Desk", "example.test")
	require.NoError(err)
	_, err = st.WriteDerivedCorrespondentKindsContext(t.Context(), []DerivedCorrespondentKind{{
		ParticipantID: desk, Source: correspondentkind.SourceJev, Kind: correspondentkind.Unclear,
	}})
	require.NoError(err)
	assert.Equal([]PendingReviewKind{PendingReviewCorrespondent}, pendingKinds(t, st))
	_, err = st.SetCorrespondentKindContext(t.Context(), SetCorrespondentKindInput{
		ParticipantID: desk, Kind: correspondentkind.Person,
	})
	require.NoError(err)
	assert.Empty(pendingKinds(t, st))

	// Imported relationship: a pending review waits until it is decided.
	owner, _, err := st.CreatePersonFromParticipant(left)
	require.NoError(err)
	_, err = st.db.ExecContext(t.Context(), st.Rebind(`INSERT INTO person_relationship_reviews
		(person_id, raw_related_value, raw_related_type, value_kind, source)
		VALUES (?, 'Casey Example', 'friend', 'text', 'vcard_import')`), owner.ID)
	require.NoError(err)
	assert.Equal([]PendingReviewKind{PendingReviewRelationship}, pendingKinds(t, st))
	_, err = st.db.ExecContext(t.Context(), `UPDATE person_relationship_reviews SET status = 'rejected'`)
	require.NoError(err)
	assert.Empty(pendingKinds(t, st))
}

// The Unclear correspondents queue resolves clusters, so a user decision on
// one linked member settles a Jev judgment on another. The dot agrees.
func TestPendingUnclearCorrespondentFollowsTheClusterDecision(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := newPendingReviewStore(t)
	desk, err := st.EnsureParticipant("desk@example.test", "Front Desk", "example.test")
	require.NoError(err)
	deskAlias, err := st.EnsureParticipant("frontdesk@example.test", "Front Desk", "example.test")
	require.NoError(err)
	_, err = st.LinkParticipants(desk, deskAlias)
	require.NoError(err)
	_, err = st.WriteDerivedCorrespondentKindsContext(t.Context(), []DerivedCorrespondentKind{{
		ParticipantID: deskAlias, Source: correspondentkind.SourceJev, Kind: correspondentkind.Unclear,
	}})
	require.NoError(err)
	assert.Equal([]PendingReviewKind{PendingReviewCorrespondent}, pendingKinds(t, st))

	// The decision lands on the other member of the cluster.
	_, err = st.db.ExecContext(t.Context(), st.Rebind(`INSERT INTO correspondent_kinds
		(participant_id, source, kind, actor) VALUES (?, 'user', 'shared_mailbox', 'user')`), desk)
	require.NoError(err)
	assert.Empty(pendingKinds(t, st))
	unclear, err := st.ListCorrespondentKindsContext(t.Context(), CorrespondentKindListFilter{Kind: correspondentkind.Unclear})
	require.NoError(err)
	assert.Empty(unclear, "the queue and the dot agree")
}

func TestPendingReviewKindsReportsUncertainEnrichment(t *testing.T) {
	f := uncertainEnrichmentFixture(t)
	assert.Equal(t, []PendingReviewKind{PendingReviewEnrichment}, pendingKinds(t, f.store))
}

// Each probe must seek an index rather than walk its table: the check runs
// on every page load and after every review decision.
func TestPendingReviewProbesUseIndexesSQLite(t *testing.T) {
	st := newPendingReviewStore(t)
	wantIndex := map[PendingReviewKind]string{
		PendingReviewIdentity:      "idx_identity_match_candidates_state",
		PendingReviewEnrichment:    "person_enrichment_attempts_next_action",
		PendingReviewOrganization:  "idx_organization_match_reviews_pending",
		PendingReviewCorrespondent: "idx_correspondent_kinds_kind",
		PendingReviewRelationship:  "idx_person_relationship_reviews_status",
	}
	for _, probe := range pendingReviewQueries() {
		t.Run(string(probe.kind), func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			rows, err := st.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+probe.query, probe.args...)
			require.NoError(err)
			defer func() { require.NoError(rows.Close()) }()
			details := []string{}
			for rows.Next() {
				var id, parent, unused int
				var detail string
				require.NoError(rows.Scan(&id, &parent, &unused, &detail))
				details = append(details, detail)
			}
			require.NoError(rows.Err())
			plan := strings.Join(details, "\n")
			for _, detail := range details {
				assert.False(strings.HasPrefix(detail, "SCAN "), "full scan in plan:\n%s", plan)
			}
			assert.Contains(plan, wantIndex[probe.kind])
		})
	}
}
