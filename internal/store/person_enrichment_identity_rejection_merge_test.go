package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

func insertIdentityRejection(t *testing.T, st *store.Store, personID, attemptID int64, value string) {
	t.Helper()
	_, err := st.DB().ExecContext(t.Context(), st.Rebind(`INSERT INTO person_enrichment_identity_rejections
		(person_id, provider_namespace, key_kind, key_value, attempt_id, actor, created_at)
		VALUES (?, 'exa', 'provider_person_id', ?, ?, 'user', ?)`),
		personID, value, attemptID, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
}

func identityRejectionOwners(t *testing.T, st *store.Store, value string) []int64 {
	t.Helper()
	rows, err := st.DB().QueryContext(t.Context(), st.Rebind(`SELECT person_id
		FROM person_enrichment_identity_rejections WHERE key_value = ? ORDER BY person_id`), value)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	owners := []int64{}
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		owners = append(owners, id)
	}
	require.NoError(t, rows.Err())
	return owners
}

func TestPersonMergeMovesIdentityRejectionsAndSplitRestoresThem(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)
	survivor := mustPromotedPerson(t, st, "reject-survivor@example.test", "Survivor")
	_, survivorAttempt := storetest.UncertainEnrichmentAttempt(t, st, "reject-survivor-2@example.test", "Survivor Two")
	absorbedID, absorbedAttempt := storetest.UncertainEnrichmentAttempt(t, st, "reject-absorbed@example.test", "Absorbed")
	absorbed, err := st.GetPersonContext(ctx, absorbedID)
	require.NoError(err)
	insertIdentityRejection(t, st, absorbed.ID, absorbedAttempt, "provider-x")
	insertIdentityRejection(t, st, absorbed.ID, absorbedAttempt, "provider-shared")
	insertIdentityRejection(t, st, survivor.ID, survivorAttempt, "provider-shared")
	survivor, err = st.GetPersonContext(ctx, survivor.ID)
	require.NoError(err)

	merged, err := st.MergePersonsContext(ctx, store.PersonMergeRequest{
		SurvivorID: survivor.ID, AbsorbedID: absorbed.ID,
		ExpectedSurvivorRevision: survivor.Revision, ExpectedAbsorbedRevision: absorbed.Revision,
		IdempotencyKey: "reject-merge", Actor: "test",
	})
	require.NoError(err)
	assert.Equal([]int64{survivor.ID}, identityRejectionOwners(t, st, "provider-x"),
		"the absorbed person's negative now binds the survivor")
	assert.Equal([]int64{survivor.ID}, identityRejectionOwners(t, st, "provider-shared"),
		"a duplicate negative is kept once")

	split, err := st.SplitPersonMergeContext(ctx, store.PersonSplitRequest{
		SourcePersonID: merged.Person.ID, MergeID: merged.Merge.ID,
		ParticipantIDs: absorbed.ParticipantIDs, ExpectedSourceRevision: merged.Person.Revision,
		IdempotencyKey: "reject-split", Actor: "test",
	})
	require.NoError(err)
	assert.Equal([]int64{split.NewPerson.ID}, identityRejectionOwners(t, st, "provider-x"),
		"split returns the negative to the split-out person")
	assert.ElementsMatch([]int64{survivor.ID, split.NewPerson.ID},
		identityRejectionOwners(t, st, "provider-shared"))
}

func TestSplitRestoresAReviewedRejectionWhoseAttemptWasDeleted(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)
	absorbedID, attemptID := storetest.UncertainEnrichmentAttempt(t, st, "reviewed-a@example.test", "Reviewed A")
	decision, err := st.RejectPersonEnrichmentIdentityContext(ctx, attemptID, "user")
	require.NoError(err)
	require.Equal(1, decision.Negatives)
	var storedAttempt sql.NullInt64
	require.NoError(st.DB().QueryRowContext(ctx, st.Rebind(`SELECT attempt_id
		FROM person_enrichment_identity_rejections WHERE person_id = ?`), absorbedID).Scan(&storedAttempt))
	require.True(storedAttempt.Valid, "the review records its attempt")

	survivor := mustPromotedPerson(t, st, "reviewed-b@example.test", "Reviewed B")
	absorbed, err := st.GetPersonContext(ctx, absorbedID)
	require.NoError(err)
	merged, err := st.MergePersonsContext(ctx, store.PersonMergeRequest{
		SurvivorID: survivor.ID, AbsorbedID: absorbed.ID,
		ExpectedSurvivorRevision: survivor.Revision, ExpectedAbsorbedRevision: absorbed.Revision,
		IdempotencyKey: "reviewed-merge", Actor: "test",
	})
	require.NoError(err)

	split, err := st.SplitPersonMergeContext(ctx, store.PersonSplitRequest{
		SourcePersonID: merged.Person.ID, MergeID: merged.Merge.ID,
		ParticipantIDs: absorbed.ParticipantIDs, ExpectedSourceRevision: merged.Person.Revision,
		IdempotencyKey: "reviewed-split", Actor: "test",
	})
	require.NoError(err, "a deleted attempt must not roll the split back")

	var owner int64
	var attempt sql.NullInt64
	require.NoError(st.DB().QueryRowContext(ctx, st.Rebind(`SELECT person_id, attempt_id
		FROM person_enrichment_identity_rejections`)).Scan(&owner, &attempt))
	assert.Equal(split.NewPerson.ID, owner)
	if attempt.Valid {
		var exists bool
		require.NoError(st.DB().QueryRowContext(ctx, st.Rebind(`SELECT EXISTS (
			SELECT 1 FROM person_enrichment_attempts WHERE id = ?)`), attempt.Int64).Scan(&exists))
		assert.True(exists, "a restored attempt reference must point at an existing attempt")
	}
}
