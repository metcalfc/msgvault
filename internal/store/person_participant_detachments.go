package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/correspondentkind"
)

// MaxPersonParticipantDetachIDs bounds one detach request so its IN lists
// stay well under SQLite bind-parameter limits.
const MaxPersonParticipantDetachIDs = 200

// PersonDetachmentNote is the decision note on every identity match
// candidate a detachment rejects or creates. Reviews shows it as the reason.
const PersonDetachmentNote = "detached from this person"

// notRestorableNotAPersonSQL excludes a rejection the not-a-person
// classification made and will undo when the classification is cleared:
// that rejection is not the user's statement about this person, so it is
// never a detachment tombstone. The alias is "tombstone".
const notRestorableNotAPersonSQL = `NOT (tombstone.notes = '` + correspondentkind.NotAPersonReason + `'
	AND EXISTS (SELECT 1 FROM correspondent_kind_candidate_snapshots snapshot
	            WHERE snapshot.candidate_id = tombstone.id))`

// personDetachmentSourceRef marks the participant-to-person tombstone a
// detachment creates when no candidate for the pair existed yet.
const personDetachmentSourceRef = "person_detach"

var (
	ErrPersonParticipantNotBound  = errors.New("participant is not bound to this person")
	ErrPersonDetachmentNotFound   = errors.New("person participant detachment not found")
	ErrPersonDetachmentReattached = errors.New("person participant detachment was already undone")
)

// PersonParticipantDetachRequest removes archive identities from a person.
type PersonParticipantDetachRequest struct {
	PersonID         int64
	ParticipantIDs   []int64
	ExpectedRevision int64
	Actor            string
}

// PersonParticipantReattachRequest reverses one active detachment.
type PersonParticipantReattachRequest struct {
	PersonID         int64
	DetachmentID     int64
	ExpectedRevision int64
	Actor            string
}

// PersonParticipantDetachment is the durable record of one detachment.
type PersonParticipantDetachment struct {
	ID             int64      `json:"id"`
	PersonID       int64      `json:"person_id"`
	ParticipantIDs []int64    `json:"participant_ids"`
	Actor          string     `json:"actor"`
	CreatedAt      time.Time  `json:"created_at"`
	ReattachedAt   *time.Time `json:"reattached_at,omitempty"`
	ReattachedBy   *string    `json:"reattached_by,omitzero" nullable:"false"`
}

// PersonParticipantDetachResult is returned by detach and reattach.
type PersonParticipantDetachResult struct {
	Detachment       PersonParticipantDetachment `json:"detachment"`
	Person           Person                      `json:"person"`
	IdentityRevision int64                       `json:"identity_revision"`
	CacheState       string                      `json:"cache_state" enum:"ready,stale"`
}

func (r PersonParticipantDetachRequest) canonicalParticipantIDs() ([]int64, error) {
	if r.PersonID <= 0 || r.ExpectedRevision <= 0 {
		return nil, fmt.Errorf("detach person participants: person and revision must be positive: %w",
			ErrInvalidParticipantID)
	}
	if len(r.ParticipantIDs) == 0 || len(r.ParticipantIDs) > MaxPersonParticipantDetachIDs {
		return nil, fmt.Errorf("detach person participants: name 1 to %d participants: %w",
			MaxPersonParticipantDetachIDs, ErrInvalidParticipantID)
	}
	ids := slices.Clone(r.ParticipantIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if ids[0] <= 0 {
		return nil, fmt.Errorf("detach person participants: ids must be positive: %w",
			ErrInvalidParticipantID)
	}
	return ids, nil
}

// DetachPersonParticipantsContext states that the named archive identities
// are not this person. In one transaction it:
//
//   - deletes the person_participants bindings for those participants;
//   - cuts every participant_links edge between them and any other
//     participant, recording each cut edge;
//   - rejects, as a user decision, every accepted, pending, or conflicting
//     participant-to-participant identity match candidate that crosses the
//     cut, and every non-rejected participant-to-person candidate pairing a
//     detached participant with this person;
//   - creates a rejected participant-to-person candidate (the standard
//     tombstone contact matching and duplicate detection honor) for each
//     detached participant that had none;
//   - bumps the identity and person revisions and invalidates enrichment,
//     activity, and sweep projections like other identity mutations.
//
// Messages are never touched. Detaching every participant is allowed: a
// person with no bound participants is a valid contact-only profile.
func (s *Store) DetachPersonParticipantsContext(
	ctx context.Context, request PersonParticipantDetachRequest,
) (*PersonParticipantDetachResult, error) {
	ids, err := request.canonicalParticipantIDs()
	if err != nil {
		return nil, err
	}
	request.ParticipantIDs = ids
	request.Actor = strings.TrimSpace(request.Actor)
	if request.Actor == "" {
		request.Actor = string(ProvenanceUser)
	}
	return retryBusyWrite(ctx, s, "detach person participants",
		func() (*PersonParticipantDetachResult, error) {
			return s.detachPersonParticipantsOnce(ctx, request)
		})
}

func (s *Store) lockPersonRevisionTx(
	ctx context.Context, tx *loggedTx, personID, expected int64,
) error {
	var revision int64
	err := tx.QueryRowContext(ctx, "SELECT revision FROM persons WHERE id = ?", personID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPersonNotFound
	}
	if err != nil {
		return fmt.Errorf("lock person %d: %w", personID, err)
	}
	if revision != expected {
		return ErrPersonRevisionConflict
	}
	return nil
}

func (s *Store) detachPersonParticipantsOnce(
	ctx context.Context, request PersonParticipantDetachRequest,
) (*PersonParticipantDetachResult, error) {
	var result *PersonParticipantDetachResult
	err := s.withTxContext(ctx, func(tx *loggedTx) error {
		if err := s.lockIdentityMutationTxContext(ctx, tx); err != nil {
			return err
		}
		personID := request.PersonID
		if err := s.lockPersonRevisionTx(ctx, tx, personID, request.ExpectedRevision); err != nil {
			return err
		}
		detached := request.ParticipantIDs
		detachedSet := make(map[int64]struct{}, len(detached))
		for _, id := range detached {
			detachedSet[id] = struct{}{}
		}
		bound, err := personBoundParticipantIDsTx(ctx, tx, personID)
		if err != nil {
			return err
		}
		boundSet := make(map[int64]struct{}, len(bound))
		for _, id := range bound {
			boundSet[id] = struct{}{}
		}
		for _, id := range detached {
			if _, ok := boundSet[id]; !ok {
				return fmt.Errorf("participant %d: %w", id, ErrPersonParticipantNotBound)
			}
		}
		inferenceBefore, err := s.captureInferenceExportPeopleTx(ctx, tx, personID)
		if err != nil {
			return err
		}
		trackedPeople, err := s.trackedPersonIDsForParticipantsTx(ctx, tx, detached)
		if err != nil {
			return err
		}
		edges, err := s.loadLinkEdgesTxContext(ctx, tx)
		if err != nil {
			return err
		}
		// Candidates are rejected when they pair a detached participant with
		// anyone it was connected to or anyone still bound to the person.
		related := make(map[int64]struct{}, len(bound))
		for _, id := range bound {
			related[id] = struct{}{}
		}
		adjacency := buildAdjacency(edges)
		for _, id := range detached {
			for member := range componentOfAdj(id, adjacency) {
				related[member] = struct{}{}
			}
		}

		var detachmentID int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO person_participant_detachments
			(person_id, actor, person_revision_before) VALUES (?, ?, ?) RETURNING id`,
			personID, request.Actor, request.ExpectedRevision,
		).Scan(&detachmentID); err != nil {
			return fmt.Errorf("record person participant detachment: %w", err)
		}
		for _, id := range detached {
			if _, err := tx.ExecContext(ctx, `INSERT INTO person_participant_detachment_members
				(detachment_id, participant_id) VALUES (?, ?)`, detachmentID, id); err != nil {
				return fmt.Errorf("record detached participant %d: %w", id, err)
			}
		}
		if err := s.cutDetachedParticipantLinksTx(ctx, tx, detachmentID, detachedSet); err != nil {
			return err
		}
		if err := s.rejectDetachedParticipantCandidatesTx(
			ctx, tx, detachmentID, personID, request.Actor, detachedSet, related,
		); err != nil {
			return err
		}
		if err := s.tombstoneDetachedParticipantsTx(
			ctx, tx, detachmentID, personID, request.Actor, detached,
		); err != nil {
			return err
		}

		idPlaceholders, idArgs := sortedIDPlaceholders(detached)
		deleted, err := tx.ExecContext(ctx, `DELETE FROM person_participants
			WHERE person_id = ? AND participant_id IN (`+idPlaceholders+`)`,
			append([]any{personID}, idArgs...)...)
		if err != nil {
			return fmt.Errorf("delete detached person bindings: %w", err)
		}
		if n, err := deleted.RowsAffected(); err != nil {
			return fmt.Errorf("count detached person bindings: %w", err)
		} else if n != int64(len(detached)) {
			return fmt.Errorf("%w: binding changed during detach", ErrPersonParticipantNotBound)
		}

		identityRevision, err := s.finishPersonBindingChangeTx(
			ctx, tx, personID, trackedPeople, inferenceBefore)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE person_participant_detachments
			SET identity_revision = ? WHERE id = ?`, identityRevision, detachmentID); err != nil {
			return fmt.Errorf("record detachment identity revision: %w", err)
		}
		result, err = s.personDetachResultTx(ctx, tx, detachmentID, personID, identityRevision)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// finishPersonBindingChangeTx runs the derived-state work every binding
// change needs: identity revision, activity and contact state, sweep scope,
// person revision, enrichment, and inference export invalidation.
func (s *Store) finishPersonBindingChangeTx(
	ctx context.Context,
	tx *loggedTx,
	personID int64,
	trackedPeople []int64,
	inferenceBefore map[int64]personInferenceExportProjection,
) (int64, error) {
	identityRevision, err := s.bumpIdentityRevisionContext(ctx, tx)
	if err != nil {
		return 0, err
	}
	accountRevision, err := readAccountIdentityRevision(tx)
	if err != nil {
		return 0, err
	}
	revisions := ContactRevisions{
		IdentityRevision: identityRevision, AccountIdentityRevision: accountRevision,
	}
	contactIDs, err := s.reclassifyPersonActivityTx(ctx, tx, []int64{personID}, revisions)
	if err != nil {
		return 0, err
	}
	for _, contactID := range contactIDs {
		if err := s.recomputeContactStateTx(ctx, tx, contactID, revisions, true); err != nil {
			return 0, err
		}
	}
	if err := s.publishPersonIdentityScopeChangesTx(
		ctx, tx, trackedPeople,
	); err != nil {
		return 0, err
	}
	if err := s.bumpPersonRevisionsTx(ctx, tx, personID); err != nil {
		return 0, err
	}
	if err := s.invalidatePersonEnrichmentIdentitiesAfterRevisionTx(ctx, tx, personID); err != nil {
		return 0, err
	}
	if err := s.invalidateInferenceExportChangesTx(ctx, tx, inferenceBefore); err != nil {
		return 0, err
	}
	return identityRevision, nil
}

func personBoundParticipantIDsTx(
	ctx context.Context, tx *loggedTx, personID int64,
) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT participant_id FROM person_participants
		WHERE person_id = ? ORDER BY participant_id`, personID)
	if err != nil {
		return nil, fmt.Errorf("load person %d bindings: %w", personID, err)
	}
	defer func() { _ = rows.Close() }()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan person %d binding: %w", personID, err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// cutDetachedParticipantLinksTx deletes every edge with exactly one
// endpoint in detached, recording the edge and its owning candidate first.
func (s *Store) cutDetachedParticipantLinksTx(
	ctx context.Context, tx *loggedTx, detachmentID int64, detached map[int64]struct{},
) error {
	ids := make([]int64, 0, len(detached))
	for id := range detached {
		ids = append(ids, id)
	}
	idPlaceholders, idArgs := sortedIDPlaceholders(ids)
	rows, err := tx.QueryContext(ctx, `SELECT participant_a, participant_b, identity_match_candidate_id
		FROM participant_links
		WHERE participant_a IN (`+idPlaceholders+`) OR participant_b IN (`+idPlaceholders+`)
		ORDER BY participant_a, participant_b`, append(slices.Clone(idArgs), idArgs...)...)
	if err != nil {
		return fmt.Errorf("load detached participant links: %w", err)
	}
	type cutEdge struct {
		a, b  int64
		owner sql.NullInt64
	}
	cuts := []cutEdge{}
	for rows.Next() {
		var edge cutEdge
		if err := rows.Scan(&edge.a, &edge.b, &edge.owner); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan detached participant link: %w", err)
		}
		_, aDetached := detached[edge.a]
		_, bDetached := detached[edge.b]
		if aDetached != bDetached {
			cuts = append(cuts, edge)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate detached participant links: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close detached participant links: %w", err)
	}
	for _, edge := range cuts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO person_participant_detachment_links
			(detachment_id, participant_a, participant_b, identity_match_candidate_id)
			VALUES (?, ?, ?, ?)`, detachmentID, edge.a, edge.b, edge.owner); err != nil {
			return fmt.Errorf("record cut participant link: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM participant_links
			WHERE participant_a = ? AND participant_b = ?`, edge.a, edge.b); err != nil {
			return fmt.Errorf("cut detached participant link: %w", err)
		}
	}
	return nil
}

// recordDetachmentCandidatePriorTx copies a candidate's decision fields into
// the detachment journal before the detachment overwrites them.
func recordDetachmentCandidatePriorTx(
	ctx context.Context, tx *loggedTx, detachmentID, candidateID int64,
) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO person_participant_detachment_candidates (
			detachment_id, candidate_id, created_by_detachment, prior_state,
			prior_decided_by, prior_decided_at, prior_notes,
			prior_application_pending, prior_pre_conflict_state)
		SELECT CAST(? AS BIGINT), id, FALSE, state, decided_by, decided_at, notes,
			application_pending, pre_conflict_state
		FROM identity_match_candidates WHERE id = ?`, detachmentID, candidateID); err != nil {
		return fmt.Errorf("record detached candidate %d: %w", candidateID, err)
	}
	return nil
}

func (s *Store) rejectCandidateForDetachmentTx(
	ctx context.Context, tx *loggedTx, detachmentID, candidateID int64, actor string,
) error {
	if err := recordDetachmentCandidatePriorTx(ctx, tx, detachmentID, candidateID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE identity_match_candidates SET
			state = ?, decided_by = ?, decided_at = `+s.dialect.Now()+`, notes = ?,
			pre_conflict_state = NULL, application_pending = FALSE,
			updated_at = `+s.dialect.Now()+`
		WHERE id = ?`,
		IdentityMatchStateRejected, actor, PersonDetachmentNote, candidateID,
	); err != nil {
		return fmt.Errorf("reject identity match %d for detachment: %w", candidateID, err)
	}
	return nil
}

// rejectDetachedParticipantCandidatesTx rejects every live candidate that
// would reconnect a detached participant to the person: participant pairs
// crossing the cut, and participant-to-person pairs naming the person.
func (s *Store) rejectDetachedParticipantCandidatesTx(
	ctx context.Context,
	tx *loggedTx,
	detachmentID, personID int64,
	actor string,
	detached, related map[int64]struct{},
) error {
	// The scan mirrors rejectAcceptedIdentityMatchesAcrossUnlinkTx: no IN
	// list over the cluster, so a large cluster cannot exceed bind limits.
	rows, err := tx.QueryContext(ctx, `SELECT id, left_kind, left_id, right_kind, right_id
		FROM identity_match_candidates
		WHERE state <> ? AND left_kind = ?
		  AND (right_kind = ? OR (right_kind = ? AND right_id = ?))
		ORDER BY id`,
		IdentityMatchStateRejected, IdentityMatchParticipant,
		IdentityMatchParticipant, IdentityMatchPerson, personID)
	if err != nil {
		return fmt.Errorf("find candidates crossing detachment: %w", err)
	}
	candidateIDs := []int64{}
	for rows.Next() {
		var id, leftID, rightID int64
		var leftKind, rightKind string
		if err := rows.Scan(&id, &leftKind, &leftID, &rightKind, &rightID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan candidate crossing detachment: %w", err)
		}
		_, leftDetached := detached[leftID]
		if IdentityMatchEndpointKind(rightKind) == IdentityMatchPerson {
			if leftDetached {
				candidateIDs = append(candidateIDs, id)
			}
			continue
		}
		_, rightDetached := detached[rightID]
		if leftDetached == rightDetached {
			continue
		}
		other := rightID
		if rightDetached {
			other = leftID
		}
		if _, ok := related[other]; ok {
			candidateIDs = append(candidateIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate candidates crossing detachment: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close candidates crossing detachment: %w", err)
	}
	for _, id := range candidateIDs {
		if err := s.rejectCandidateForDetachmentTx(ctx, tx, detachmentID, id, actor); err != nil {
			return err
		}
	}
	return nil
}

// tombstoneDetachedParticipantsTx makes sure each detached participant has
// a rejected participant-to-person candidate for the person. That pair is
// the suppression record contact matching and duplicate detection already
// honor for the participant's whole cluster.
func (s *Store) tombstoneDetachedParticipantsTx(
	ctx context.Context, tx *loggedTx, detachmentID, personID int64, actor string, detached []int64,
) error {
	for _, participantID := range detached {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
				SELECT 1 FROM identity_match_candidates tombstone
				WHERE tombstone.left_kind = ? AND tombstone.left_id = ?
				  AND tombstone.right_kind = ? AND tombstone.right_id = ?
				  AND tombstone.state = ? AND `+notRestorableNotAPersonSQL+`)`,
			IdentityMatchParticipant, participantID, IdentityMatchPerson, personID,
			IdentityMatchStateRejected,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check detachment tombstone for participant %d: %w", participantID, err)
		}
		if exists {
			continue
		}
		basis, normalized, err := participantTombstoneBasisTx(ctx, tx, participantID)
		if err != nil {
			return err
		}
		var candidateID int64
		err = tx.QueryRowContext(ctx, `INSERT INTO identity_match_candidates (
				left_kind, left_id, right_kind, right_id, basis, normalized_value,
				state, source, source_ref, notes, decided_by, decided_at,
				application_pending, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, `+s.dialect.Now()+`, FALSE,
				`+s.dialect.Now()+`, `+s.dialect.Now()+`) RETURNING id`,
			IdentityMatchParticipant, participantID, IdentityMatchPerson, personID,
			basis, normalized, IdentityMatchStateRejected, ProvenanceUser,
			personDetachmentSourceRef, PersonDetachmentNote, actor,
		).Scan(&candidateID)
		if err != nil {
			return fmt.Errorf("create detachment tombstone for participant %d: %w", participantID, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO person_participant_detachment_candidates
			(detachment_id, candidate_id, created_by_detachment) VALUES (?, ?, TRUE)`,
			detachmentID, candidateID); err != nil {
			return fmt.Errorf("record detachment tombstone %d: %w", candidateID, err)
		}
	}
	return nil
}

// participantTombstoneBasisTx names the participant's own address as the
// tombstone's basis, so the rejection reads naturally in Reviews history.
// The pair, not the basis, is what matchers honor.
func participantTombstoneBasisTx(
	ctx context.Context, tx *loggedTx, participantID int64,
) (IdentityMatchBasis, any, error) {
	var email, phone sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT NULLIF(TRIM(email_address), ''),
			NULLIF(TRIM(phone_number), '')
		FROM participants WHERE id = ?`, participantID).Scan(&email, &phone); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, fmt.Errorf("participant %d: %w", participantID, ErrParticipantNotFound)
		}
		return "", nil, fmt.Errorf("read participant %d address: %w", participantID, err)
	}
	switch {
	case email.Valid:
		return IdentityMatchEmail, strings.ToLower(email.String), nil
	case phone.Valid:
		return IdentityMatchPhone, phone.String, nil
	default:
		return IdentityMatchStableProviderID, nil, nil
	}
}

// ReattachPersonParticipantsContext reverses an active detachment. It is the
// explicit user action that clears that detachment's tombstones: bindings
// return, cut edges are re-inserted where the identity forest still allows
// them, rejected candidates regain their prior decisions, and tombstones the
// detachment created are deleted. A participant bound to another person since
// the detachment fails the whole undo with PersonBindingConflictError.
func (s *Store) ReattachPersonParticipantsContext(
	ctx context.Context, request PersonParticipantReattachRequest,
) (*PersonParticipantDetachResult, error) {
	if request.PersonID <= 0 || request.DetachmentID <= 0 || request.ExpectedRevision <= 0 {
		return nil, fmt.Errorf("reattach person participants: ids and revision must be positive: %w",
			ErrInvalidParticipantID)
	}
	request.Actor = strings.TrimSpace(request.Actor)
	if request.Actor == "" {
		request.Actor = string(ProvenanceUser)
	}
	return retryBusyWrite(ctx, s, "reattach person participants",
		func() (*PersonParticipantDetachResult, error) {
			return s.reattachPersonParticipantsOnce(ctx, request)
		})
}

func (s *Store) reattachPersonParticipantsOnce(
	ctx context.Context, request PersonParticipantReattachRequest,
) (*PersonParticipantDetachResult, error) {
	var result *PersonParticipantDetachResult
	err := s.withTxContext(ctx, func(tx *loggedTx) error {
		if err := s.lockIdentityMutationTxContext(ctx, tx); err != nil {
			return err
		}
		personID := request.PersonID
		if err := s.lockPersonRevisionTx(ctx, tx, personID, request.ExpectedRevision); err != nil {
			return err
		}
		var reattachedAt sql.NullTime
		err := tx.QueryRowContext(ctx, `SELECT reattached_at FROM person_participant_detachments
			WHERE id = ? AND person_id = ?`, request.DetachmentID, personID).Scan(&reattachedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPersonDetachmentNotFound
		}
		if err != nil {
			return fmt.Errorf("lock person participant detachment: %w", err)
		}
		if reattachedAt.Valid {
			return ErrPersonDetachmentReattached
		}
		members, err := detachmentMemberIDsTx(ctx, tx, request.DetachmentID)
		if err != nil {
			return err
		}
		inferenceBefore, err := s.captureInferenceExportPeopleTx(ctx, tx, personID)
		if err != nil {
			return err
		}
		edges, err := s.restoreDetachmentLinksTx(ctx, tx, request.DetachmentID)
		if err != nil {
			return err
		}
		// Bind the detached participants and whatever the restored links
		// connect them to, the same fill the link path performs.
		adjacency := buildAdjacency(edges)
		union := map[int64]struct{}{}
		for _, id := range members {
			for member := range componentOfAdj(id, adjacency) {
				union[member] = struct{}{}
			}
		}
		unionIDs := make([]int64, 0, len(union))
		for id := range union {
			unionIDs = append(unionIDs, id)
		}
		slices.Sort(unionIDs)
		personIDs, err := personIDsForParticipantsTx(ctx, tx, unionIDs)
		if err != nil {
			return err
		}
		for _, other := range personIDs {
			if other != personID {
				return newPersonBindingConflict([]int64{personID, other})
			}
		}
		if _, err := s.bindPersonParticipantsTx(ctx, tx, personID, unionIDs); err != nil {
			return err
		}
		if err := s.restoreDetachmentCandidatesTx(ctx, tx, request.DetachmentID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE person_participant_detachments
			SET reattached_at = `+s.dialect.Now()+`, reattached_by = ? WHERE id = ?`,
			request.Actor, request.DetachmentID); err != nil {
			return fmt.Errorf("mark detachment undone: %w", err)
		}
		trackedPeople, err := s.trackedPersonIDsForParticipantsTx(ctx, tx, unionIDs)
		if err != nil {
			return err
		}
		identityRevision, err := s.finishPersonBindingChangeTx(
			ctx, tx, personID, trackedPeople, inferenceBefore)
		if err != nil {
			return err
		}
		result, err = s.personDetachResultTx(
			ctx, tx, request.DetachmentID, personID, identityRevision)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func detachmentMemberIDsTx(
	ctx context.Context, tx *loggedTx, detachmentID int64,
) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT participant_id
		FROM person_participant_detachment_members
		WHERE detachment_id = ? ORDER BY participant_id`, detachmentID)
	if err != nil {
		return nil, fmt.Errorf("load detached participants: %w", err)
	}
	defer func() { _ = rows.Close() }()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan detached participant: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// restoreDetachmentLinksTx re-inserts each recorded edge unless its
// endpoints are already connected (another path, or a link the user made
// since), which keeps the identity graph a forest. It returns the edge set
// after restoration.
func (s *Store) restoreDetachmentLinksTx(
	ctx context.Context, tx *loggedTx, detachmentID int64,
) ([]linkEdge, error) {
	rows, err := tx.QueryContext(ctx, `SELECT recorded.participant_a, recorded.participant_b,
			CASE WHEN EXISTS (
				SELECT 1 FROM identity_match_candidates candidate
				WHERE candidate.id = recorded.identity_match_candidate_id
			) THEN recorded.identity_match_candidate_id END
		FROM person_participant_detachment_links recorded
		WHERE recorded.detachment_id = ?
		ORDER BY recorded.participant_a, recorded.participant_b`, detachmentID)
	if err != nil {
		return nil, fmt.Errorf("load detachment links: %w", err)
	}
	type recordedEdge struct {
		edge  linkEdge
		owner sql.NullInt64
	}
	recorded := []recordedEdge{}
	for rows.Next() {
		var item recordedEdge
		if err := rows.Scan(&item.edge.a, &item.edge.b, &item.owner); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan detachment link: %w", err)
		}
		recorded = append(recorded, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate detachment links: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close detachment links: %w", err)
	}
	edges, err := s.loadLinkEdgesTxContext(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, item := range recorded {
		if _, connected := componentOf(item.edge.a, edges)[item.edge.b]; connected {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO participant_links
			(participant_a, participant_b, identity_match_candidate_id) VALUES (?, ?, ?)`,
			item.edge.a, item.edge.b, item.owner); err != nil {
			return nil, fmt.Errorf("restore detached participant link: %w", err)
		}
		edges = append(edges, item.edge)
	}
	return edges, nil
}

// restoreDetachmentCandidatesTx returns each candidate the detachment
// rejected to its recorded decision and deletes the tombstones it created.
// A candidate someone decided again since the detachment is left alone.
func (s *Store) restoreDetachmentCandidatesTx(
	ctx context.Context, tx *loggedTx, detachmentID int64,
) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM identity_match_candidates
		WHERE state = ? AND notes = ? AND EXISTS (
			SELECT 1 FROM person_participant_detachment_candidates journal
			WHERE journal.detachment_id = ? AND journal.candidate_id = identity_match_candidates.id
			  AND journal.created_by_detachment
		)`, IdentityMatchStateRejected, PersonDetachmentNote, detachmentID); err != nil {
		return fmt.Errorf("delete detachment tombstones: %w", err)
	}
	restore := func(column string) string {
		return `(SELECT journal.` + column + ` FROM person_participant_detachment_candidates journal
			WHERE journal.detachment_id = ? AND journal.candidate_id = identity_match_candidates.id)`
	}
	args := make([]any, 0, 9)
	for range 6 {
		args = append(args, detachmentID)
	}
	args = append(args, IdentityMatchStateRejected, PersonDetachmentNote, detachmentID)
	if _, err := tx.ExecContext(ctx, `UPDATE identity_match_candidates SET
			state = `+restore("prior_state")+`,
			decided_by = `+restore("prior_decided_by")+`,
			decided_at = `+restore("prior_decided_at")+`,
			notes = `+restore("prior_notes")+`,
			application_pending = `+restore("prior_application_pending")+`,
			pre_conflict_state = `+restore("prior_pre_conflict_state")+`,
			updated_at = `+s.dialect.Now()+`
		WHERE state = ? AND notes = ? AND EXISTS (
			SELECT 1 FROM person_participant_detachment_candidates journal
			WHERE journal.detachment_id = ? AND journal.candidate_id = identity_match_candidates.id
			  AND NOT journal.created_by_detachment
		)`, args...); err != nil {
		return fmt.Errorf("restore detached candidates: %w", err)
	}
	return nil
}

func (s *Store) personDetachResultTx(
	ctx context.Context, tx *loggedTx, detachmentID, personID, identityRevision int64,
) (*PersonParticipantDetachResult, error) {
	detachment, err := getPersonParticipantDetachmentTx(ctx, tx, detachmentID)
	if err != nil {
		return nil, err
	}
	person, err := s.getPersonTx(ctx, tx, personID)
	if err != nil {
		return nil, err
	}
	return &PersonParticipantDetachResult{
		Detachment: *detachment, Person: *person, IdentityRevision: identityRevision,
	}, nil
}

func getPersonParticipantDetachmentTx(
	ctx context.Context, tx *loggedTx, detachmentID int64,
) (*PersonParticipantDetachment, error) {
	var detachment PersonParticipantDetachment
	var reattachedAt sql.NullTime
	var reattachedBy sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id, person_id, actor, created_at,
			reattached_at, reattached_by
		FROM person_participant_detachments WHERE id = ?`, detachmentID).Scan(
		&detachment.ID, &detachment.PersonID, &detachment.Actor, &detachment.CreatedAt,
		&reattachedAt, &reattachedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPersonDetachmentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get person participant detachment %d: %w", detachmentID, err)
	}
	if reattachedAt.Valid {
		at := reattachedAt.Time
		detachment.ReattachedAt = &at
	}
	if reattachedBy.Valid {
		by := reattachedBy.String
		detachment.ReattachedBy = &by
	}
	detachment.ParticipantIDs, err = detachmentMemberIDsTx(ctx, tx, detachmentID)
	if err != nil {
		return nil, err
	}
	return &detachment, nil
}

// activePersonDetachmentSeparatingTx reports the active detachment, if any,
// that separates a new system candidate's endpoints: one endpoint is a
// participant the user detached from a person, that person still holds the
// detachment's rejected participant-to-person tombstone for it, and the
// other endpoint is that person or a participant bound to that person. The
// tombstone, not the detachment's person ID, names the person, so the check
// follows the tombstone when a person merge rewrites it to the survivor.
func (s *Store) activePersonDetachmentSeparatingTx(
	ctx context.Context,
	tx *loggedTx,
	leftKind IdentityMatchEndpointKind, leftID int64,
	rightKind IdentityMatchEndpointKind, rightID int64,
) (int64, bool, error) {
	type pair struct {
		detached  int64
		otherKind IdentityMatchEndpointKind
		otherID   int64
	}
	pairs := []pair{}
	if leftKind == IdentityMatchParticipant {
		pairs = append(pairs, pair{leftID, rightKind, rightID})
	}
	if rightKind == IdentityMatchParticipant {
		pairs = append(pairs, pair{rightID, leftKind, leftID})
	}
	for _, candidate := range pairs {
		var personFilter string
		var personArgs []any
		switch candidate.otherKind {
		case IdentityMatchPerson:
			personFilter = `tombstone.right_id = ?`
			personArgs = []any{candidate.otherID}
		case IdentityMatchParticipant:
			personFilter = `EXISTS (SELECT 1 FROM person_participants other
				WHERE other.participant_id = ? AND other.person_id = tombstone.right_id)`
			personArgs = []any{candidate.otherID}
		default:
			continue
		}
		args := []any{candidate.detached, IdentityMatchParticipant, IdentityMatchPerson,
			IdentityMatchStateRejected}
		args = append(args, personArgs...)
		var detachmentID int64
		err := tx.QueryRowContext(ctx, `SELECT member.detachment_id
			FROM person_participant_detachment_members member
			WHERE member.participant_id = ?
			  AND EXISTS (SELECT 1 FROM person_participant_detachments detachment
			              WHERE detachment.id = member.detachment_id
			                AND detachment.reattached_at IS NULL)
			  AND EXISTS (SELECT 1 FROM identity_match_candidates tombstone
			              WHERE tombstone.left_kind = ? AND tombstone.left_id = member.participant_id
			                AND tombstone.right_kind = ? AND tombstone.state = ?
			                AND `+notRestorableNotAPersonSQL+`
			                AND `+personFilter+`
			                AND NOT EXISTS (SELECT 1 FROM person_participants rebound
			                                WHERE rebound.participant_id = member.participant_id
			                                  AND rebound.person_id = tombstone.right_id))
			ORDER BY member.detachment_id DESC LIMIT 1`, args...).Scan(&detachmentID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, false, fmt.Errorf("check person detachment for new candidate: %w", err)
		}
		return detachmentID, true, nil
	}
	return 0, false, nil
}

// ListPersonParticipantDetachmentsContext returns a person's detachments,
// newest first, for history and audit.
func (s *Store) ListPersonParticipantDetachmentsContext(
	ctx context.Context, personID int64,
) ([]PersonParticipantDetachment, error) {
	var detachments []PersonParticipantDetachment
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM person_participant_detachments
			WHERE person_id = ? ORDER BY id DESC`, personID)
		if err != nil {
			return fmt.Errorf("list person participant detachments: %w", err)
		}
		ids := []int64{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan person participant detachment: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate person participant detachments: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close person participant detachments: %w", err)
		}
		detachments = make([]PersonParticipantDetachment, 0, len(ids))
		for _, id := range ids {
			detachment, err := getPersonParticipantDetachmentTx(ctx, tx, id)
			if err != nil {
				return err
			}
			detachments = append(detachments, *detachment)
		}
		return nil
	})
	return detachments, err
}

// adoptNotAPersonCandidateForDetachmentTx moves a candidate whose
// not-a-person rejection is being cleared under an active detachment that
// separates its endpoints: instead of returning to its prior decision, it
// stays rejected for the detachment, and the decision the not-a-person
// snapshot held moves into the detachment journal, so undoing the
// detachment restores it.
func (s *Store) adoptNotAPersonCandidateForDetachmentTx(
	ctx context.Context, tx *loggedTx, detachmentID, candidateID int64,
) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO person_participant_detachment_candidates (
			detachment_id, candidate_id, created_by_detachment, prior_state,
			prior_decided_by, prior_decided_at, prior_notes,
			prior_application_pending, prior_pre_conflict_state)
		SELECT CAST(? AS BIGINT), candidate_id, FALSE, prior_state, prior_decided_by,
			prior_decided_at, prior_notes, prior_application_pending, prior_pre_conflict_state
		FROM correspondent_kind_candidate_snapshots WHERE candidate_id = ?
		ON CONFLICT (detachment_id, candidate_id) DO NOTHING`,
		detachmentID, candidateID); err != nil {
		return fmt.Errorf("journal not-a-person candidate %d for detachment: %w", candidateID, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE identity_match_candidates SET
			decided_by = ?, notes = ?, updated_at = `+s.dialect.Now()+`
		WHERE id = ?`, string(ProvenanceSystem), PersonDetachmentNote, candidateID); err != nil {
		return fmt.Errorf("hand candidate %d to detachment: %w", candidateID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM correspondent_kind_candidate_snapshots
		WHERE candidate_id = ?`, candidateID); err != nil {
		return fmt.Errorf("drop not-a-person snapshot for candidate %d: %w", candidateID, err)
	}
	return nil
}

// PersonDetachmentJoinError reports that a system identity match would join
// a participant's cluster with a person the user detached that participant
// from. The link is refused and the candidate is rejected for the
// detachment; an explicit user link or a user acceptance is not refused.
type PersonDetachmentJoinError struct {
	DetachmentID int64
}

func (e *PersonDetachmentJoinError) Error() string {
	return fmt.Sprintf("%s: detachment %d", ErrPersonDetachmentJoin, e.DetachmentID)
}

func (e *PersonDetachmentJoinError) Unwrap() error { return ErrPersonDetachmentJoin }

// ErrPersonDetachmentJoin is the sentinel PersonDetachmentJoinError wraps.
var ErrPersonDetachmentJoin = errors.New(
	"identity match would rejoin an identity detached from its person")

// hasActivePersonDetachmentTx reports whether any detachment is still in
// effect, so callers can skip guards that only matter while one is.
func hasActivePersonDetachmentTx(ctx context.Context, tx *loggedTx) (bool, error) {
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM person_participant_detachments WHERE reattached_at IS NULL)`).Scan(&active); err != nil {
		return false, fmt.Errorf("check active person detachments: %w", err)
	}
	return active, nil
}

// detachmentBlockingJoinTx reports the active detachment, if any, that a
// new edge between a and b would defeat: after the join, one cluster would
// hold a participant the user detached from a person (with that
// detachment's tombstone still live, and the participant not bound to the
// person again) together with a participant bound to that person. This
// catches a re-attach through a third participant, which an endpoint-only
// check cannot see.
func detachmentBlockingJoinTx(
	ctx context.Context, tx *loggedTx, a, b int64, edges []linkEdge,
) (int64, bool, error) {
	adjacency := buildAdjacency(edges)
	union := componentOfAdj(a, adjacency)
	for member := range componentOfAdj(b, adjacency) {
		union[member] = struct{}{}
	}
	members := make([]int64, 0, len(union))
	for member := range union {
		members = append(members, member)
	}
	slices.Sort(members)
	type detached struct{ participantID, detachmentID, personID int64 }
	found := []detached{}
	if err := queryInChunksContext(ctx, tx, members, []any{
		IdentityMatchParticipant, IdentityMatchPerson, IdentityMatchStateRejected,
	}, `SELECT member.participant_id, member.detachment_id, tombstone.right_id
		FROM person_participant_detachment_members member
		JOIN person_participant_detachments detachment
		  ON detachment.id = member.detachment_id AND detachment.reattached_at IS NULL
		JOIN identity_match_candidates tombstone
		  ON tombstone.left_kind = ? AND tombstone.left_id = member.participant_id
		 AND tombstone.right_kind = ? AND tombstone.state = ?
		 AND `+notRestorableNotAPersonSQL+`
		WHERE member.participant_id IN (%s)
		ORDER BY member.detachment_id DESC`, func(rows *loggedRows) error {
		var row detached
		if err := rows.Scan(&row.participantID, &row.detachmentID, &row.personID); err != nil {
			return fmt.Errorf("scan detached cluster member: %w", err)
		}
		found = append(found, row)
		return nil
	}); err != nil {
		return 0, false, fmt.Errorf("load detached cluster members: %w", err)
	}
	if len(found) == 0 {
		return 0, false, nil
	}
	bindings := map[int64]int64{}
	if err := queryInChunksContext(ctx, tx, members, nil,
		`SELECT participant_id, person_id FROM person_participants WHERE participant_id IN (%s)`,
		func(rows *loggedRows) error {
			var participantID, personID int64
			if err := rows.Scan(&participantID, &personID); err != nil {
				return fmt.Errorf("scan cluster binding: %w", err)
			}
			bindings[participantID] = personID
			return nil
		}); err != nil {
		return 0, false, fmt.Errorf("load cluster bindings: %w", err)
	}
	for _, row := range found {
		if bindings[row.participantID] == row.personID {
			continue
		}
		for participantID, personID := range bindings {
			if personID == row.personID && participantID != row.participantID {
				return row.detachmentID, true, nil
			}
		}
	}
	return 0, false, nil
}

// rejectCandidateBlockedByDetachmentContext records a system match the
// detachment guard refused: the candidate becomes rejected for that
// detachment, journaled so undoing the detachment restores it.
func (s *Store) rejectCandidateBlockedByDetachmentContext(
	ctx context.Context, detachmentID, candidateID int64,
) error {
	return s.withTxContext(ctx, func(tx *loggedTx) error {
		if err := s.lockIdentityMutationTxContext(ctx, tx); err != nil {
			return err
		}
		var state IdentityMatchState
		err := tx.QueryRowContext(ctx, `SELECT state FROM identity_match_candidates WHERE id = ?`,
			candidateID).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load candidate %d blocked by detachment: %w", candidateID, err)
		}
		if state == IdentityMatchStateRejected {
			return nil
		}
		return s.rejectCandidateForDetachmentTx(
			ctx, tx, detachmentID, candidateID, string(ProvenanceSystem))
	})
}
