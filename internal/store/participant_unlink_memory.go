package store

import (
	"context"
	"fmt"
)

// participantUnlinkSourceRef marks the rejected participant-to-participant
// candidate an unlink records when nothing else already says the two
// identities are not one person.
const participantUnlinkSourceRef = "participant_unlink"

// rememberUserUnlinkTx records a user's unlink as a rejected
// participant-to-participant candidate between the endpoints of the removed
// edge, so duplicate detection never proposes the two identities again,
// whether by name or by a shared mailbox, phone number, or provider account.
// It covers links made by hand, which have no candidate to reject. Nothing
// is written when the identities are still connected, or when a rejected
// participant pair already crosses the cut (an accepted match the unlink
// rejected, or the same-mailbox record). The caller holds the identity
// mutation lock and has already deleted the edge.
func (s *Store) rememberUserUnlinkTx(ctx context.Context, tx *loggedTx, a, b int64) error {
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
	rows, err := tx.QueryContext(ctx, `SELECT left_id, right_id FROM identity_match_candidates
		WHERE left_kind = ? AND right_kind = ? AND state = ?`,
		IdentityMatchParticipant, IdentityMatchParticipant, IdentityMatchStateRejected)
	if err != nil {
		return fmt.Errorf("load rejected identity pairs for unlink: %w", err)
	}
	crossing := false
	for rows.Next() {
		var leftID, rightID int64
		if err := rows.Scan(&leftID, &rightID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan rejected identity pair for unlink: %w", err)
		}
		_, leftInLeft := left[leftID]
		_, leftInRight := right[leftID]
		_, rightInLeft := left[rightID]
		_, rightInRight := right[rightID]
		if leftInLeft && rightInRight || leftInRight && rightInLeft {
			crossing = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate rejected identity pairs for unlink: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close rejected identity pairs for unlink: %w", err)
	}
	if crossing {
		return nil
	}
	lo, hi := normalizeEdge(a, b)
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
		ProvenanceUser, participantUnlinkSourceRef, emailEquivalenceUnlinkNote,
		string(ProvenanceUser),
	); err != nil {
		return fmt.Errorf("record unlinked identity pair: %w", err)
	}
	return nil
}
