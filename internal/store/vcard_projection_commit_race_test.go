package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
	"go.kenn.io/msgvault/internal/vcard"
)

// TestVCardSemanticCommitRejectsProjectionChangedByEmploymentWrite covers the
// half of the read set that no person-scoped row belongs to. An employment
// write touches neither the person record nor any profile component, so before
// the projection revision existed there was nothing for a commit to serialize
// against except the fingerprint itself.
func TestVCardSemanticCommitRejectsProjectionChangedByEmploymentWrite(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newVCardProjectionFixture(t)
	ctx := t.Context()
	raw := []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Alice\r\nEND:VCARD\r\n")
	created, snapshot, prepared := renderVCardEnvelope(t, fixture.store, fixture.person.ID,
		raw, "employment-conflict", "Rendered Before The Promotion")
	_, err := fixture.store.UpdateEmploymentContext(
		ctx, fixture.employment.ID, fixture.employment.Revision,
		store.EmploymentInput{
			PersonID: fixture.person.ID, OrganizationID: fixture.organization.ID,
			Title: new("Staff Engineer"), Source: store.ProvenanceUser,
		})
	require.NoError(err)

	_, err = fixture.store.CommitVCardResourceEnvelopeContext(
		ctx, "book", "employment-conflict", created.Revision,
		snapshot.Fingerprint, prepared,
	)
	require.ErrorIs(err, store.ErrVCardProjectionConflict)

	loaded, err := fixture.store.GetVCardResourceEnvelopeContext(
		ctx, "book", "employment-conflict")
	require.NoError(err)
	assert.Equal(created.Revision, loaded.Revision)
	assert.Equal(raw, loaded.StoredBody)
}

// TestVCardSemanticCommitLeavesProjectionRevisionAlone is what keeps the
// mechanism from chasing its own tail. The commit takes the projection row's
// lock but must not move the row: a commit that bumped it would invalidate the
// snapshot every render was made from, so the next render would conflict
// against the commit before it, forever.
func TestVCardSemanticCommitLeavesProjectionRevisionAlone(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	ctx := t.Context()
	person := createEnvelopePerson(t, st, "alice@example.com")
	raw := []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Alice\r\nEND:VCARD\r\n")
	created, snapshot, prepared := renderVCardEnvelope(t, st, person.ID,
		raw, "idempotent-commit", "Alice Example")
	committed, err := st.CommitVCardResourceEnvelopeContext(
		ctx, "book", "idempotent-commit", created.Revision,
		snapshot.Fingerprint, prepared,
	)
	require.NoError(err)
	assert.Equal(created.Revision+1, committed.Revision)

	afterCommit, err := st.LoadPersonVCardSnapshotContext(ctx, person.ID)
	require.NoError(err)
	assert.Equal(snapshot.ProjectionRevision, afterCommit.ProjectionRevision)
	assert.Equal(snapshot.Fingerprint, afterCommit.Fingerprint)

	// Re-committing the same render against the same unchanged projection is a
	// no-op, not a conflict and not a new revision.
	repeated, err := st.CommitVCardResourceEnvelopeContext(
		ctx, "book", "idempotent-commit", committed.Revision,
		afterCommit.Fingerprint, prepared,
	)
	require.NoError(err)
	assert.Equal(committed.Revision, repeated.Revision)
	assert.Equal(committed.UpdatedAt, repeated.UpdatedAt)

	settled, err := st.LoadPersonVCardSnapshotContext(ctx, person.ID)
	require.NoError(err)
	assert.Equal(snapshot.ProjectionRevision, settled.ProjectionRevision)
}

// renderVCardEnvelope stores raw as person's first envelope under sourceUID,
// snapshots the projection, and prepares a render of that envelope with its
// FN replaced — the state every commit-path test starts from.
func renderVCardEnvelope(
	t *testing.T, st *store.Store, personID int64, raw []byte,
	sourceUID, formattedName string,
) (*store.VCardResourceEnvelopeRecord, *store.PersonVCardSnapshot, vcard.ResourceEnvelope) {
	t.Helper()
	created, err := st.PutVCardResourceEnvelopeContext(
		t.Context(), store.VCardResourceEnvelopeInput{
			PersonID: personID,
			Envelope: parseStoreEnvelope(t, raw, "book", sourceUID),
		})
	require.NoError(t, err)
	snapshot, err := st.LoadPersonVCardSnapshotContext(t.Context(), personID)
	require.NoError(t, err)
	return created, snapshot, replaceStoreFormattedName(t, created.ResourceEnvelope, formattedName)
}
