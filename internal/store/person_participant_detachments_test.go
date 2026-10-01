package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

// participantPersonCandidates lists every participant-to-person candidate
// naming the pair, in any state.
func participantPersonCandidates(
	t *testing.T, st *store.Store, participantID, personID int64,
) []store.IdentityMatchCandidate {
	t.Helper()
	candidates, err := st.ListIdentityMatchCandidatesContext(t.Context(), nil, 500, 0)
	require.NoError(t, err)
	matching := []store.IdentityMatchCandidate{}
	for _, candidate := range candidates {
		if candidate.LeftKind == store.IdentityMatchParticipant && candidate.LeftID == participantID &&
			candidate.RightKind == store.IdentityMatchPerson && candidate.RightID == personID {
			matching = append(matching, candidate)
		}
	}
	return matching
}

func messageSender(t *testing.T, st *store.Store, messageID int64) int64 {
	t.Helper()
	var sender sql.NullInt64
	require.NoError(t, st.DB().QueryRow(st.Rebind(
		`SELECT sender_id FROM messages WHERE id = ?`), messageID).Scan(&sender))
	require.True(t, sender.Valid)
	return sender.Int64
}

func TestDetachPersonParticipantsRemovesIdentityAndKeepsMessages(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	f := storetest.New(t)
	st := f.Store
	owner := f.EnsureParticipant("ana@example.com", "Ana Example", "example.com")
	robot := f.EnsureParticipant("notifications@example.com", "Ana Example", "example.com")
	_, err := st.LinkParticipants(owner, robot)
	require.NoError(err)
	person, _, err := st.CreatePersonFromParticipant(owner)
	require.NoError(err)
	require.Equal([]int64{owner, robot}, person.ParticipantIDs)

	message := f.NewMessage().WithSubject("Build failed").Build()
	message.SenderID = sql.NullInt64{Int64: robot, Valid: true}
	messageID, err := st.UpsertMessage(message)
	require.NoError(err)
	identityBefore, err := st.IdentityRevision()
	require.NoError(err)

	result, err := st.DetachPersonParticipantsContext(ctx, store.PersonParticipantDetachRequest{
		PersonID: person.ID, ParticipantIDs: []int64{robot}, ExpectedRevision: person.Revision,
		Actor: "user",
	})
	require.NoError(err)

	assert.Equal([]int64{owner}, result.Person.ParticipantIDs)
	assert.Equal(person.Revision+1, result.Person.Revision)
	assert.Greater(result.IdentityRevision, identityBefore)
	assert.Equal(person.ID, result.Detachment.PersonID)
	assert.Equal([]int64{robot}, result.Detachment.ParticipantIDs)
	assert.Nil(result.Detachment.ReattachedAt)
	assert.False(linkedPair(t, st, owner, robot), "the identity edge is cut")
	owning, err := st.PersonForParticipantsContext(ctx, []int64{robot})
	require.NoError(err)
	assert.Nil(owning, "the robot address belongs to no one")

	assert.Equal(robot, messageSender(t, st, messageID), "messages are never touched")
	var messages int
	require.NoError(st.DB().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messages))
	assert.Equal(1, messages)

	tombstones := participantPersonCandidates(t, st, robot, person.ID)
	require.Len(tombstones, 1)
	assert.Equal(store.IdentityMatchStateRejected, tombstones[0].State)
	require.NotNil(tombstones[0].DecidedBy)
	assert.Equal("user", *tombstones[0].DecidedBy)
	assert.Equal(store.IdentityMatchEmail, tombstones[0].Basis)

	history, err := st.ListPersonParticipantDetachmentsContext(ctx, person.ID)
	require.NoError(err)
	require.Len(history, 1)
	assert.Equal(result.Detachment.ID, history[0].ID)
}

func TestDetachPersonParticipantsValidatesAndAllowsContactOnlyProfile(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	f := storetest.New(t)
	st := f.Store
	solo := f.EnsureParticipant("solo@example.com", "Solo Example", "example.com")
	stranger := f.EnsureParticipant("stranger@example.com", "Stranger", "example.com")
	person, _, err := st.CreatePersonFromParticipant(solo)
	require.NoError(err)

	_, err = st.DetachPersonParticipantsContext(ctx, store.PersonParticipantDetachRequest{
		PersonID: person.ID, ParticipantIDs: []int64{stranger}, ExpectedRevision: person.Revision,
	})
	require.ErrorIs(err, store.ErrPersonParticipantNotBound)
	_, err = st.DetachPersonParticipantsContext(ctx, store.PersonParticipantDetachRequest{
		PersonID: person.ID, ParticipantIDs: []int64{solo}, ExpectedRevision: person.Revision + 7,
	})
	require.ErrorIs(err, store.ErrPersonRevisionConflict)
	_, err = st.DetachPersonParticipantsContext(ctx, store.PersonParticipantDetachRequest{
		PersonID: person.ID, ExpectedRevision: person.Revision,
	})
	require.ErrorIs(err, store.ErrInvalidParticipantID)

	result, err := st.DetachPersonParticipantsContext(ctx, store.PersonParticipantDetachRequest{
		PersonID: person.ID, ParticipantIDs: []int64{solo}, ExpectedRevision: person.Revision,
	})
	require.NoError(err, "a person with no archive identity is a valid contact-only profile")
	assert.Empty(result.Person.ParticipantIDs)
	kept, err := st.GetPersonContext(ctx, person.ID)
	require.NoError(err)
	assert.Equal(person.VCardUID, kept.VCardUID)
}

// detachReplayFixture is a person whose two identities were joined by a
// system-accepted match, with a second, still-pending match for the pair.
type detachReplayFixture struct {
	st                *store.Store
	left, right       int64
	accepted, pending *store.IdentityMatchCandidate
	person            *store.Person
}

func newDetachReplayFixture(t *testing.T) detachReplayFixture {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	st := storetest.New(t).Store
	left, err := st.EnsureParticipantByIdentifier("beeper", "@detach-left:beeper.local", "Ana Example")
	require.NoError(err)
	right, err := st.EnsureParticipantByIdentifier("beeper", "@detach-right:beeper.local", "Ana Example")
	require.NoError(err)
	accepted := upsertPairCandidate(t, st, left, right, store.IdentityMatchStableProviderID)
	_, _, err = st.AcceptIdentityMatchCandidateContext(ctx, accepted.ID, "system", nil)
	require.NoError(err)
	pending, created, err := st.UpsertIdentityMatchCandidateContext(ctx, store.IdentityMatchCandidateInput{
		LeftKind: store.IdentityMatchParticipant, LeftID: left,
		RightKind: store.IdentityMatchParticipant, RightID: right,
		Basis: store.IdentityMatchEmail, NormalizedValue: new("ana@example.com"),
		State: store.IdentityMatchStateCandidate, Source: store.ProvenanceArchiveObservation,
	})
	require.NoError(err)
	require.True(created)
	person, _, err := st.CreatePersonFromParticipant(left)
	require.NoError(err)
	require.Equal([]int64{left, right}, person.ParticipantIDs)
	return detachReplayFixture{
		st: st, left: left, right: right, accepted: accepted, pending: pending, person: person,
	}
}

func detachCandidate(t *testing.T, st *store.Store, id int64) *store.IdentityMatchCandidate {
	t.Helper()
	candidate, err := st.GetIdentityMatchCandidateContext(context.Background(), id)
	require.NoError(t, err)
	return candidate
}

func TestDetachPersonParticipantsSuppressesSystemMatching(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	f := newDetachReplayFixture(t)
	st := f.st

	_, err := st.DetachPersonParticipantsContext(ctx, store.PersonParticipantDetachRequest{
		PersonID: f.person.ID, ParticipantIDs: []int64{f.right}, ExpectedRevision: f.person.Revision,
	})
	require.NoError(err)

	for _, id := range []int64{f.accepted.ID, f.pending.ID} {
		rejected := detachCandidate(t, st, id)
		assert.Equal(store.IdentityMatchStateRejected, rejected.State, "candidate %d", id)
		require.NotNil(rejected.DecidedBy)
		assert.Equal("user", *rejected.DecidedBy, "candidate %d", id)
	}
	applied, err := st.ApplyAcceptedIdentityMatchesContext(ctx, 0)
	require.NoError(err)
	assert.Equal(0, applied, "accepted-match replay cannot recreate the cut edge")
	assert.False(linkedPair(t, st, f.left, f.right))

	// A matcher that finds the pair again by a basis nobody rejected yet
	// still records its suggestion already rejected.
	fresh, created, err := st.UpsertIdentityMatchCandidateContext(ctx, store.IdentityMatchCandidateInput{
		LeftKind: store.IdentityMatchParticipant, LeftID: f.left,
		RightKind: store.IdentityMatchParticipant, RightID: f.right,
		Basis: store.IdentityMatchDisplayName, NormalizedValue: new("ana example"),
		State: store.IdentityMatchStateCandidate, Source: store.ProvenanceSystem,
	})
	require.NoError(err)
	require.True(created)
	assert.Equal(store.IdentityMatchStateRejected, fresh.State)
	require.NotNil(fresh.Notes)
	assert.Equal(store.PersonDetachmentNote, *fresh.Notes)

	// The same suggestion for an unrelated pair is unaffected.
	other, err := st.EnsureParticipantByIdentifier("beeper", "@detach-other:beeper.local", "Ana Example")
	require.NoError(err)
	unrelated, _, err := st.UpsertIdentityMatchCandidateContext(ctx, store.IdentityMatchCandidateInput{
		LeftKind: store.IdentityMatchParticipant, LeftID: f.right,
		RightKind: store.IdentityMatchParticipant, RightID: other,
		Basis: store.IdentityMatchDisplayName, NormalizedValue: new("ana example"),
		State: store.IdentityMatchStateCandidate, Source: store.ProvenanceSystem,
	})
	require.NoError(err)
	assert.Equal(store.IdentityMatchStateCandidate, unrelated.State)
}

func TestReattachPersonParticipantsRestoresDetachment(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	f := newDetachReplayFixture(t)
	st := f.st

	detached, err := st.DetachPersonParticipantsContext(ctx, store.PersonParticipantDetachRequest{
		PersonID: f.person.ID, ParticipantIDs: []int64{f.right}, ExpectedRevision: f.person.Revision,
	})
	require.NoError(err)
	suppressed, _, err := st.UpsertIdentityMatchCandidateContext(ctx, store.IdentityMatchCandidateInput{
		LeftKind: store.IdentityMatchParticipant, LeftID: f.left,
		RightKind: store.IdentityMatchParticipant, RightID: f.right,
		Basis: store.IdentityMatchDisplayName, NormalizedValue: new("ana example"),
		State: store.IdentityMatchStateCandidate, Source: store.ProvenanceSystem,
	})
	require.NoError(err)
	require.Equal(store.IdentityMatchStateRejected, suppressed.State)

	_, err = st.ReattachPersonParticipantsContext(ctx, store.PersonParticipantReattachRequest{
		PersonID: f.person.ID, DetachmentID: detached.Detachment.ID,
		ExpectedRevision: f.person.Revision,
	})
	require.ErrorIs(err, store.ErrPersonRevisionConflict, "undo uses the detach result's revision")

	restored, err := st.ReattachPersonParticipantsContext(ctx, store.PersonParticipantReattachRequest{
		PersonID: f.person.ID, DetachmentID: detached.Detachment.ID,
		ExpectedRevision: detached.Person.Revision, Actor: "user",
	})
	require.NoError(err)
	assert.Equal([]int64{f.left, f.right}, restored.Person.ParticipantIDs)
	assert.Equal(detached.Person.Revision+1, restored.Person.Revision)
	assert.Greater(restored.IdentityRevision, detached.IdentityRevision)
	require.NotNil(restored.Detachment.ReattachedAt)
	require.NotNil(restored.Detachment.ReattachedBy)
	assert.Equal("user", *restored.Detachment.ReattachedBy)
	assert.True(linkedPair(t, st, f.left, f.right), "the cut edge returns")

	accepted := detachCandidate(t, st, f.accepted.ID)
	assert.Equal(store.IdentityMatchStateAccepted, accepted.State)
	require.NotNil(accepted.DecidedBy)
	assert.Equal("system", *accepted.DecidedBy)
	assert.Equal(store.IdentityMatchStateCandidate, detachCandidate(t, st, f.pending.ID).State)
	assert.Equal(store.IdentityMatchStateCandidate, detachCandidate(t, st, suppressed.ID).State,
		"a suggestion suppressed while detached regains its own state")
	assert.Empty(participantPersonCandidates(t, st, f.right, f.person.ID),
		"the tombstone the detachment created is cleared")
	applied, err := st.ApplyAcceptedIdentityMatchesContext(ctx, 0)
	require.NoError(err)
	assert.Equal(0, applied, "the restored edge already satisfies the accepted match")

	_, err = st.ReattachPersonParticipantsContext(ctx, store.PersonParticipantReattachRequest{
		PersonID: f.person.ID, DetachmentID: detached.Detachment.ID,
		ExpectedRevision: restored.Person.Revision,
	})
	require.ErrorIs(err, store.ErrPersonDetachmentReattached)
}

func TestReattachPersonParticipantsRefusesIdentityOwnedElsewhere(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	f := storetest.New(t)
	st := f.Store
	owner := f.EnsureParticipant("bea@example.com", "Bea Example", "example.com")
	shared := f.EnsureParticipant("team@example.com", "Team", "example.com")
	_, err := st.LinkParticipants(owner, shared)
	require.NoError(err)
	person, _, err := st.CreatePersonFromParticipant(owner)
	require.NoError(err)
	detached, err := st.DetachPersonParticipantsContext(ctx, store.PersonParticipantDetachRequest{
		PersonID: person.ID, ParticipantIDs: []int64{shared}, ExpectedRevision: person.Revision,
	})
	require.NoError(err)
	_, _, err = st.CreatePersonFromParticipant(shared)
	require.NoError(err)

	_, err = st.ReattachPersonParticipantsContext(ctx, store.PersonParticipantReattachRequest{
		PersonID: person.ID, DetachmentID: detached.Detachment.ID,
		ExpectedRevision: detached.Person.Revision,
	})
	require.ErrorIs(err, store.ErrPersonBindingConflict)
}

func TestDetachedParticipantStaysOutOfContactMatching(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	f := newContactMatchFixture(t)
	first := f.emailParticipant("eve@example.test", "Eve")
	robot := f.emailParticipant("eve.alerts@example.test", "Eve")
	_, err := f.st.LinkParticipants(first, robot)
	require.NoError(err)
	people := f.importCards(f.card("card-eve", "Eve Contact", []string{"eve.alerts@example.test"}, nil))
	contactID := people["card-eve"]
	candidate := f.buildCandidate(contactID)
	_, _, err = f.st.AcceptIdentityMatchCandidateContext(ctx, candidate.ID, "user", nil)
	require.NoError(err)
	contact, err := f.st.GetPersonContext(ctx, contactID)
	require.NoError(err)
	require.ElementsMatch([]int64{first, robot}, contact.ParticipantIDs)

	_, err = f.st.DetachPersonParticipantsContext(ctx, store.PersonParticipantDetachRequest{
		PersonID: contactID, ParticipantIDs: []int64{robot}, ExpectedRevision: contact.Revision,
	})
	require.NoError(err)
	assert.Equal(store.IdentityMatchStateRejected, detachCandidate(t, f.st, candidate.ID).State)

	_, err = f.st.BuildContactMatchCandidatesContext(ctx)
	require.NoError(err)
	for _, match := range participantPersonCandidates(t, f.st, robot, contactID) {
		assert.Equal(store.IdentityMatchStateRejected, match.State,
			"contact matching must not suggest the detached address again")
	}
	after, err := f.st.GetPersonContext(ctx, contactID)
	require.NoError(err)
	assert.Equal([]int64{first}, after.ParticipantIDs)
}
