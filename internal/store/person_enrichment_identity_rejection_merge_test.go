package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func insertIdentityRejection(t *testing.T, st *store.Store, personID int64, value string) {
	t.Helper()
	_, err := st.DB().ExecContext(t.Context(), st.Rebind(`INSERT INTO person_enrichment_identity_rejections
		(person_id, provider_namespace, key_kind, key_value, actor, created_at)
		VALUES (?, 'exa', 'provider_person_id', ?, 'user', ?)`),
		personID, value, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
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
	absorbed := mustPromotedPerson(t, st, "reject-absorbed@example.test", "Absorbed")
	insertIdentityRejection(t, st, absorbed.ID, "provider-x")
	insertIdentityRejection(t, st, absorbed.ID, "provider-shared")
	insertIdentityRejection(t, st, survivor.ID, "provider-shared")

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
