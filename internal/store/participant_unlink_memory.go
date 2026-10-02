package store

import (
	"context"
	"fmt"
	"slices"
)

// participantUnlinkSourceRef marks a rejected participant-to-participant
// candidate that records a user's decision to separate two identities: an
// unlink, or a person split.
const participantUnlinkSourceRef = "participant_unlink"

// participantUnlinkInheritedSourceRef marks a rejection copied from an
// earlier unlink or split onto the other half of a later split. It is as
// durable as the original but was not itself an action the user took.
const participantUnlinkInheritedSourceRef = "participant_unlink_inherited"

// personSplitNote is the decision note on the rejection a person split
// records between its two halves.
const personSplitNote = "split apart by the user"

// inheritedSeparationNote is the decision note on an inherited rejection.
const inheritedSeparationNote = "carried from an earlier unlink"

// durableUserRejectionSQL selects rejected participant pairs that stand for
// a user's own decision and are never restored by the system: it leaves out
// rejections a not-a-person classification made (restored when the
// classification is cleared) and rejections a detachment made (restored
// when it is undone). The alias is "tombstone".
const durableUserRejectionSQL = `tombstone.state = 'rejected'
	AND tombstone.left_kind = 'participant' AND tombstone.right_kind = 'participant'
	AND tombstone.decided_by = 'user'
	AND NOT EXISTS (SELECT 1 FROM person_participant_detachment_candidates detachment
	                WHERE detachment.candidate_id = tombstone.id)
	AND ` + notRestorableNotAPersonSQL

// rememberUserUnlinkTx records a user's unlink of the edge (a, b).
func (s *Store) rememberUserUnlinkTx(ctx context.Context, tx *loggedTx, a, b int64) error {
	return s.rememberUserSeparationTx(ctx, tx, a, b, emailEquivalenceUnlinkNote)
}

// rememberUserSeparationTx records that the user separated the identities
// on either side of the removed edge (a, b), so duplicate detection never
// proposes them again, whether by name or by a shared mailbox, phone number,
// or provider account. It covers links made by hand, which have no
// candidate to reject. The caller holds the identity mutation lock and has
// already removed the edge.
//
// Two things are written, both as durable user rejections:
//
//   - Earlier decisions survive the split. A user rejection between an
//     identity outside the split cluster and a member of one half is
//     copied to the other half, so after A|BC and then B|C, A stays apart
//     from C as well as B.
//   - The separation itself, between a and b, unless a durable user
//     rejection already crosses the cut. Restorable system rejections do
//     not count: they can come back as open candidates.
func (s *Store) rememberUserSeparationTx(
	ctx context.Context, tx *loggedTx, a, b int64, note string,
) error {
	edges, err := s.loadLinkEdgesTxContext(ctx, tx)
	if err != nil {
		return err
	}
	adjacency := buildAdjacency(edges)
	left := componentOfAdj(a, adjacency)
	if _, connected := left[b]; connected {
		return nil
	}
	right := componentOfAdj(b, adjacency)
	sideOf := func(id int64) int {
		if _, ok := left[id]; ok {
			return 1
		}
		if _, ok := right[id]; ok {
			return 2
		}
		return 0
	}
	rows, err := tx.QueryContext(ctx, `SELECT tombstone.left_id, tombstone.right_id
		FROM identity_match_candidates tombstone WHERE `+durableUserRejectionSQL)
	if err != nil {
		return fmt.Errorf("load user rejections for unlink: %w", err)
	}
	crossing := false
	// outside maps an identity outside the split cluster to the halves it
	// is already rejected against.
	outside := map[int64]map[int]bool{}
	for rows.Next() {
		var leftID, rightID int64
		if err := rows.Scan(&leftID, &rightID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan user rejection for unlink: %w", err)
		}
		leftSide, rightSide := sideOf(leftID), sideOf(rightID)
		switch {
		case leftSide != 0 && rightSide != 0 && leftSide != rightSide:
			crossing = true
		case leftSide == 0 && rightSide != 0:
			if outside[leftID] == nil {
				outside[leftID] = map[int]bool{}
			}
			outside[leftID][rightSide] = true
		case rightSide == 0 && leftSide != 0:
			if outside[rightID] == nil {
				outside[rightID] = map[int]bool{}
			}
			outside[rightID][leftSide] = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate user rejections for unlink: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close user rejections for unlink: %w", err)
	}

	others := make([]int64, 0, len(outside))
	for id := range outside {
		others = append(others, id)
	}
	slices.Sort(others)
	representative := map[int]int64{1: minMember(left), 2: minMember(right)}
	for _, other := range others {
		for _, side := range []int{1, 2} {
			if outside[other][side] {
				continue
			}
			if err := s.insertUserSeparationTx(ctx, tx, other, representative[side],
				participantUnlinkInheritedSourceRef, inheritedSeparationNote); err != nil {
				return err
			}
		}
	}
	if crossing {
		return nil
	}
	return s.insertUserSeparationTx(ctx, tx, a, b, participantUnlinkSourceRef, note)
}

func minMember(members map[int64]struct{}) int64 {
	var lowest int64
	for id := range members {
		if lowest == 0 || id < lowest {
			lowest = id
		}
	}
	return lowest
}

// insertUserSeparationTx records a durable user rejection between a and b.
// An open candidate for the same pair is rejected in place rather than left
// open beside a new row.
func (s *Store) insertUserSeparationTx(
	ctx context.Context, tx *loggedTx, a, b int64, sourceRef, note string,
) error {
	lo, hi := normalizeEdge(a, b)
	result, err := tx.ExecContext(ctx, `UPDATE identity_match_candidates SET
		state = ?, decided_by = ?, decided_at = `+s.dialect.Now()+`, notes = ?,
		pre_conflict_state = NULL, application_pending = FALSE,
		updated_at = `+s.dialect.Now()+`
		WHERE left_kind = ? AND left_id = ? AND right_kind = ? AND right_id = ? AND state = ?`,
		IdentityMatchStateRejected, string(ProvenanceUser), note,
		IdentityMatchParticipant, lo, IdentityMatchParticipant, hi, IdentityMatchStateCandidate)
	if err != nil {
		return fmt.Errorf("reject open candidate for separated pair: %w", err)
	}
	if resolved, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("count rejected open candidates: %w", err)
	} else if resolved > 0 {
		return nil
	}
	basis, normalized, err := participantTombstoneBasisTx(ctx, tx, lo)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO identity_match_candidates (
		left_kind, left_id, right_kind, right_id, basis, normalized_value, state,
		source, source_ref, notes, decided_by, decided_at, application_pending,
		created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, `+s.dialect.Now()+`, FALSE,
		`+s.dialect.Now()+`, `+s.dialect.Now()+`)`,
		IdentityMatchParticipant, lo, IdentityMatchParticipant, hi,
		basis, normalized, IdentityMatchStateRejected,
		ProvenanceUser, sourceRef, note,
		string(ProvenanceUser),
	); err != nil {
		return fmt.Errorf("record separated identity pair: %w", err)
	}
	return nil
}

// clearUserSeparationsTx deletes the unlink and split records (direct and
// inherited) between two sets of participants that the user has just joined
// by hand. Other rejections stay as they are.
func (s *Store) clearUserSeparationsTx(
	ctx context.Context, tx *loggedTx, left, right map[int64]struct{},
) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, left_id, right_id FROM identity_match_candidates
		WHERE left_kind = ? AND right_kind = ? AND source_ref IN (?, ?)`,
		IdentityMatchParticipant, IdentityMatchParticipant,
		participantUnlinkSourceRef, participantUnlinkInheritedSourceRef)
	if err != nil {
		return fmt.Errorf("load identity separations: %w", err)
	}
	stale := []int64{}
	for rows.Next() {
		var id, leftID, rightID int64
		if err := rows.Scan(&id, &leftID, &rightID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan identity separation: %w", err)
		}
		_, leftInLeft := left[leftID]
		_, leftInRight := right[leftID]
		_, rightInLeft := left[rightID]
		_, rightInRight := right[rightID]
		if leftInLeft && rightInRight || leftInRight && rightInLeft {
			stale = append(stale, id)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate identity separations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close identity separations: %w", err)
	}
	for _, id := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM identity_match_candidates WHERE id = ?`, id); err != nil {
			return fmt.Errorf("clear identity separation %d: %w", id, err)
		}
	}
	return nil
}

// clearPersonSeparationsTx clears unlink and split records between the
// participants of two people the user is merging.
func (s *Store) clearPersonSeparationsTx(ctx context.Context, tx *loggedTx, survivorID, absorbedID int64) error {
	sides := map[int64]map[int64]struct{}{survivorID: {}, absorbedID: {}}
	rows, err := tx.QueryContext(ctx, `SELECT person_id, participant_id FROM person_participants
		WHERE person_id IN (?, ?)`, survivorID, absorbedID)
	if err != nil {
		return fmt.Errorf("load merged person participants: %w", err)
	}
	for rows.Next() {
		var personID, participantID int64
		if err := rows.Scan(&personID, &participantID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan merged person participant: %w", err)
		}
		sides[personID][participantID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate merged person participants: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close merged person participants: %w", err)
	}
	return s.clearUserSeparationsTx(ctx, tx, sides[survivorID], sides[absorbedID])
}
