package store

import (
	"context"
	"fmt"
	"strings"
)

// rejectPersonDuplicateGroupTx rejects, for the user, the other pending
// duplicate-person candidates that repeat a rejected one: the same shared
// display name or address name (basis display_name, same normalized value)
// with an endpoint in either of the rejected candidate's identity clusters.
// The user said the two sides are different people, so asking about the
// same name again for either side is the same review. Each sibling is
// decided by the user with a note naming the rejected candidate. Only
// undecided rows change; accepted and decided rows, and candidates on other
// bases or from other sources, are left as they are. The caller holds the
// identity lock. It returns the rejected siblings' IDs in ascending order.
func (s *Store) rejectPersonDuplicateGroupTx(
	ctx context.Context, tx *loggedTx, rejected *IdentityMatchCandidate,
) ([]int64, error) {
	if rejected.SourceRef == nil || *rejected.SourceRef != PersonDuplicateSourceRef ||
		rejected.LeftKind != IdentityMatchParticipant || rejected.RightKind != IdentityMatchParticipant ||
		rejected.Basis != IdentityMatchDisplayName ||
		rejected.NormalizedValue == nil || strings.TrimSpace(*rejected.NormalizedValue) == "" {
		return nil, nil
	}
	components, err := linkComponentsFromTx(ctx, tx, []int64{rejected.LeftID, rejected.RightID})
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, left_id, right_id FROM identity_match_candidates
		WHERE basis = ? AND normalized_value = ? AND id <> ?
		  AND source_ref = ? AND left_kind = ? AND right_kind = ?
		  AND state = ? AND decided_by IS NULL
		ORDER BY id`,
		IdentityMatchDisplayName, *rejected.NormalizedValue, rejected.ID,
		PersonDuplicateSourceRef, IdentityMatchParticipant, IdentityMatchParticipant,
		IdentityMatchStateCandidate)
	if err != nil {
		return nil, fmt.Errorf("load duplicate-person candidates sharing a rejected name: %w", err)
	}
	var siblings []int64
	for rows.Next() {
		var id, left, right int64
		if err := rows.Scan(&id, &left, &right); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan duplicate-person candidate sharing a rejected name: %w", err)
		}
		_, leftInGroup := components[left]
		_, rightInGroup := components[right]
		if leftInGroup || rightInGroup {
			siblings = append(siblings, id)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close duplicate-person candidates sharing a rejected name: %w", err)
	}
	note := fmt.Sprintf("Rejected with candidate %d, which you rejected for the same shared name", rejected.ID)
	for _, id := range siblings {
		if _, err := tx.ExecContext(ctx, `UPDATE identity_match_candidates SET
			state = ?, decided_by = ?, decided_at = `+s.dialect.Now()+`,
			notes = ?, pre_conflict_state = NULL, application_pending = FALSE,
			updated_at = `+s.dialect.Now()+`
			WHERE id = ? AND state = ? AND decided_by IS NULL`,
			IdentityMatchStateRejected, ProvenanceUser, note, id, IdentityMatchStateCandidate); err != nil {
			return nil, fmt.Errorf("reject duplicate-person candidate sharing a rejected name: %w", err)
		}
		if err := dropCandidateDecisionSnapshotTx(ctx, tx, id); err != nil {
			return nil, err
		}
	}
	return siblings, nil
}
