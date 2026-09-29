package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// contactMatchMergeActor records who performed the merge half of an accepted
// bind. Only an explicit user decision reaches it.
const contactMatchMergeActor = "user"

// acceptParticipantPersonMatchCandidateContext applies a participant-to-person
// candidate, such as a contact match, at participant-cluster level:
//
//   - bind (the cluster has no person): the candidate is marked accepted, the
//     cluster is promoted to a new person, and that person is merged into the
//     candidate's person as survivor. The survivor keeps its vCard UID and
//     CardDAV mapping, and the merge is reversible through split.
//   - linked (the cluster already belongs to the candidate's person): the
//     candidate is marked accepted; nothing else changes.
//   - merge or ambiguous (the cluster belongs to other people): a
//     PersonBindingConflictError naming every person is returned and the
//     candidate is left undecided, so the caller can offer an explicit merge
//     that lets the user pick the survivor.
//
// Promotion and merge are separate transactions. A failure after promotion
// restores the candidate's previous decision and leaves the promoted person
// in place; the candidate then reads as a merge, which the user resolves
// through the normal merge flow.
func (s *Store) acceptParticipantPersonMatchCandidateContext(
	ctx context.Context, candidateID int64, decidedBy string, notes *string,
) (*IdentityMatchCandidate, int64, error) {
	if decidedBy != string(ProvenanceUser) {
		return nil, 0, ErrIdentityMatchNotAcceptable
	}
	var before *IdentityMatchCandidate
	var classification ContactMatchClassification
	err := s.withTxContext(ctx, func(tx *loggedTx) error {
		if err := s.lockIdentityMutationTxContext(ctx, tx); err != nil {
			return err
		}
		candidate, err := getIdentityMatchCandidateTx(ctx, tx, candidateID)
		if err != nil {
			return err
		}
		if candidate.LeftKind != IdentityMatchParticipant ||
			candidate.RightKind != IdentityMatchPerson {
			return ErrIdentityMatchEndpointUnsupported
		}
		for _, endpoint := range []struct {
			kind IdentityMatchEndpointKind
			id   int64
		}{{candidate.LeftKind, candidate.LeftID}, {candidate.RightKind, candidate.RightID}} {
			if err := validateIdentityMatchEndpointTx(ctx, tx, endpoint.kind, endpoint.id); err != nil {
				return err
			}
		}
		edges, err := s.loadLinkEdgesTxContext(ctx, tx)
		if err != nil {
			return err
		}
		members := sortedComponentMembers(candidate.LeftID, edges)
		persons, err := personIDsForParticipantsTx(ctx, tx, members)
		if err != nil {
			return err
		}
		classification = classifyContactMatch(candidate.RightID, persons)
		switch classification {
		case ContactMatchMerge, ContactMatchAmbiguous:
			return newPersonBindingConflict(append(slices.Clone(persons), candidate.RightID))
		case ContactMatchBind:
			blocks, err := contactMatchBlockReasonsTx(ctx, tx, []int64{candidate.RightID})
			if err != nil {
				return err
			}
			if _, blocked := blocks[candidate.RightID]; blocked {
				return ErrPersonCardDAVPublished
			}
		case ContactMatchLinked:
		}
		before = candidate
		// The bind is marked accepted before the merge; the pending flag
		// stays set until the merge commits.
		_, err = tx.ExecContext(ctx, `UPDATE identity_match_candidates SET
			state = ?, decided_by = ?, decided_at = `+s.dialect.Now()+`, notes = ?,
			pre_conflict_state = NULL, application_pending = ?,
			updated_at = `+s.dialect.Now()+` WHERE id = ?`,
			IdentityMatchStateAccepted, decidedBy, stringValue(notes),
			classification == ContactMatchBind, candidate.ID)
		if err != nil {
			return fmt.Errorf("accept participant-to-person identity candidate: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	if classification == ContactMatchLinked {
		return s.acceptedParticipantPersonCandidateContext(ctx, candidateID)
	}

	contactPersonID := before.RightID
	promoted, created, err := s.CreatePersonFromParticipantContext(ctx, before.LeftID)
	if err == nil && !created && promoted.ID != contactPersonID {
		// Another writer bound the cluster between the decision and the
		// promotion. Merging it now would choose a survivor the user never
		// saw, so hand the conflict back instead.
		err = newPersonBindingConflict([]int64{promoted.ID, contactPersonID})
	}
	if err == nil && promoted.ID != contactPersonID {
		err = s.mergeContactMatchBindContext(ctx, candidateID, contactPersonID, promoted)
	}
	if err != nil {
		if restoreErr := s.restoreParticipantPersonDecisionContext(ctx, before); restoreErr != nil {
			return nil, 0, errors.Join(err, restoreErr)
		}
		return nil, 0, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE identity_match_candidates
		SET application_pending = FALSE WHERE id = ? AND state = ?`,
		candidateID, IdentityMatchStateAccepted); err != nil {
		return nil, 0, fmt.Errorf("complete participant-to-person identity candidate: %w", err)
	}
	return s.acceptedParticipantPersonCandidateContext(ctx, candidateID)
}

func (s *Store) mergeContactMatchBindContext(
	ctx context.Context, candidateID, contactPersonID int64, promoted *Person,
) error {
	contact, err := s.GetPersonContext(ctx, contactPersonID)
	if err != nil {
		return err
	}
	_, err = s.MergePersonsContext(ctx, PersonMergeRequest{
		SurvivorID:               contact.ID,
		AbsorbedID:               promoted.ID,
		ExpectedSurvivorRevision: contact.Revision,
		ExpectedAbsorbedRevision: promoted.Revision,
		IdempotencyKey:           fmt.Sprintf("identity-match-%d-bind-%d", candidateID, promoted.ID),
		Actor:                    contactMatchMergeActor,
	})
	if err != nil {
		return fmt.Errorf("merge bound cluster into person %d: %w", contact.ID, err)
	}
	return nil
}

func (s *Store) acceptedParticipantPersonCandidateContext(
	ctx context.Context, candidateID int64,
) (*IdentityMatchCandidate, int64, error) {
	candidate, err := s.GetIdentityMatchCandidateContext(ctx, candidateID)
	if err != nil {
		return nil, 0, err
	}
	revision, err := readIdentityRevisionContext(ctx, s.db)
	if err != nil {
		return nil, 0, err
	}
	return candidate, revision, nil
}

// restoreParticipantPersonDecisionContext puts back the decision fields a
// failed bind overwrote, so a refused or failed accept does not consume the
// candidate.
func (s *Store) restoreParticipantPersonDecisionContext(
	ctx context.Context, before *IdentityMatchCandidate,
) error {
	return s.withTxContext(ctx, func(tx *loggedTx) error {
		if err := s.lockIdentityMutationTxContext(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE identity_match_candidates SET
			state = ?, decided_by = ?, decided_at = ?, notes = ?,
			application_pending = ?, pre_conflict_state = ?, updated_at = ?
			WHERE id = ? AND state = ?`,
			before.State, before.DecidedBy, before.DecidedAt, before.Notes,
			before.applicationPending, before.conflictState.preConflictState,
			before.UpdatedAt, before.ID, IdentityMatchStateAccepted,
		); err != nil {
			return fmt.Errorf("restore participant-to-person identity decision: %w", err)
		}
		return nil
	})
}
