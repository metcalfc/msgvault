package store

import (
	"context"
	"fmt"
	"slices"
)

// RetireStalePersonDuplicateCandidatesContext removes pending Jev-judged
// duplicate-person candidates that the current signal rules would no longer
// propose: the two identity clusters no longer share a qualifying display
// name or address name (for example a bare first name such as "michael", or
// an address name now used by three or more clusters). Only undecided rows
// (state candidate, no decider) with basis display_name are considered, so
// accepted, rejected, conflict, and user-decided rows, and candidates
// decided in code from a shared mailbox, phone number, or provider account,
// are never touched. A candidate whose clusters were linked, or whose side
// left the eligible set (an owner identity, a non-person classification, no
// email address), is left for review as it is. A retired row is deleted,
// not rejected, because the user never decided it; its judgment is
// forgotten too, so the pair is asked afresh if it qualifies again. It
// returns how many candidates were retired.
func (s *Store) RetireStalePersonDuplicateCandidatesContext(ctx context.Context) (int, error) {
	retired := 0
	err := retryBusyWriteErr(ctx, s, "retire stale person duplicate candidates", func() error {
		retired = 0
		return s.withTxContext(ctx, func(tx *loggedTx) error {
			if err := s.lockIdentityMutationTxContext(ctx, tx); err != nil {
				return err
			}
			var err error
			retired, err = s.retireStalePersonDuplicateCandidatesTx(ctx, tx)
			return err
		})
	})
	return retired, err
}

func (s *Store) retireStalePersonDuplicateCandidatesTx(ctx context.Context, tx *loggedTx) (int, error) {
	type pending struct{ id, left, right int64 }
	rows, err := tx.QueryContext(ctx, `SELECT id, left_id, right_id FROM identity_match_candidates
		WHERE source_ref = ? AND left_kind = ? AND right_kind = ? AND basis = ?
		  AND state = ? AND decided_by IS NULL
		ORDER BY id`,
		PersonDuplicateSourceRef, IdentityMatchParticipant, IdentityMatchParticipant,
		IdentityMatchDisplayName, IdentityMatchStateCandidate)
	if err != nil {
		return 0, fmt.Errorf("load pending duplicate-person candidates: %w", err)
	}
	var candidates []pending
	for rows.Next() {
		var row pending
		if err := rows.Scan(&row.id, &row.left, &row.right); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan pending duplicate-person candidate: %w", err)
		}
		candidates = append(candidates, row)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close pending duplicate-person candidates: %w", err)
	}
	if len(candidates) == 0 {
		return 0, nil
	}
	scan, err := s.duplicateSignalPairsTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	retired := 0
	for _, candidate := range candidates {
		left, right := scan.index.rootOf(candidate.left), scan.index.rootOf(candidate.right)
		if left == right {
			continue
		}
		if left > right {
			left, right = right, left
		}
		_, leftEligible := scan.clusters[left]
		_, rightEligible := scan.clusters[right]
		if !leftEligible || !rightEligible {
			continue
		}
		signals := scan.pairs[duplicatePairKey{left, right}]
		if slices.ContainsFunc([]PersonDuplicateSignal{PersonDuplicateSameName, PersonDuplicateSameLocalPart},
			func(signal PersonDuplicateSignal) bool { _, ok := signals[signal]; return ok }) {
			continue
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM identity_match_candidates
			WHERE id = ? AND state = ? AND decided_by IS NULL`, candidate.id, IdentityMatchStateCandidate)
		if err != nil {
			return retired, fmt.Errorf("retire duplicate-person candidate %d: %w", candidate.id, err)
		}
		if changed, err := result.RowsAffected(); err != nil {
			return retired, fmt.Errorf("count retired duplicate-person candidate: %w", err)
		} else if changed == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM person_duplicate_judgments
			WHERE left_participant_id = ? AND right_participant_id = ?`,
			min(candidate.left, candidate.right), max(candidate.left, candidate.right)); err != nil {
			return retired, fmt.Errorf("forget retired duplicate-person judgment: %w", err)
		}
		retired++
	}
	return retired, nil
}
