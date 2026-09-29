package store

import (
	"context"
	"fmt"
	"slices"
)

// contactMatchMergeActor records who performed the merge half of an accepted
// bind. Only an explicit user decision reaches it.
const contactMatchMergeActor = "user"

// acceptParticipantPersonMatchCandidateContext applies a participant-to-person
// candidate, such as a contact match, at participant-cluster level:
//
//   - bind (the cluster has no person): the cluster is promoted to a new
//     person, that person is merged into the candidate's person as survivor,
//     and the candidate is marked accepted. The survivor keeps its vCard UID
//     and CardDAV mapping, and the merge is reversible through split.
//   - linked (the cluster already belongs to the candidate's person): the
//     candidate is marked accepted; nothing else changes.
//   - merge or ambiguous (the cluster belongs to other people): a
//     PersonBindingConflictError naming every person is returned and the
//     candidate is left undecided, so the caller can offer an explicit merge
//     that lets the user pick the survivor.
//
// Everything happens in one transaction under the identity lock, so a bind
// is all or nothing: a cancellation or failure after promotion rolls the
// promotion back, and the candidate's decision is written only by the
// transaction that applied it.
func (s *Store) acceptParticipantPersonMatchCandidateContext(
	ctx context.Context, candidateID int64, decidedBy string, notes *string,
) (*IdentityMatchCandidate, int64, error) {
	if decidedBy != string(ProvenanceUser) {
		return nil, 0, ErrIdentityMatchNotAcceptable
	}
	type acceptance struct {
		candidate *IdentityMatchCandidate
		revision  int64
	}
	result, err := retryBusyWrite(ctx, s, "accept participant-to-person match",
		func() (*acceptance, error) {
			var accepted acceptance
			err := s.withTxContext(ctx, func(tx *loggedTx) error {
				var err error
				accepted.candidate, err = s.acceptParticipantPersonMatchTx(
					ctx, tx, candidateID, decidedBy, notes)
				if err != nil {
					return err
				}
				accepted.revision, err = readIdentityRevisionContext(ctx, tx)
				return err
			})
			if err != nil {
				return nil, err
			}
			return &accepted, nil
		})
	if err != nil {
		return nil, 0, err
	}
	return result.candidate, result.revision, nil
}

func (s *Store) acceptParticipantPersonMatchTx(
	ctx context.Context, tx *loggedTx, candidateID int64, decidedBy string, notes *string,
) (*IdentityMatchCandidate, error) {
	if err := s.lockIdentityMutationTxContext(ctx, tx); err != nil {
		return nil, err
	}
	candidate, err := getIdentityMatchCandidateTx(ctx, tx, candidateID)
	if err != nil {
		return nil, err
	}
	if candidate.LeftKind != IdentityMatchParticipant ||
		candidate.RightKind != IdentityMatchPerson {
		return nil, ErrIdentityMatchEndpointUnsupported
	}
	for _, endpoint := range []struct {
		kind IdentityMatchEndpointKind
		id   int64
	}{{candidate.LeftKind, candidate.LeftID}, {candidate.RightKind, candidate.RightID}} {
		if err := validateIdentityMatchEndpointTx(ctx, tx, endpoint.kind, endpoint.id); err != nil {
			return nil, err
		}
	}
	edges, err := s.loadLinkEdgesTxContext(ctx, tx)
	if err != nil {
		return nil, err
	}
	members := sortedComponentMembers(candidate.LeftID, edges)
	persons, err := personIDsForParticipantsTx(ctx, tx, members)
	if err != nil {
		return nil, err
	}
	contactPersonID := candidate.RightID
	classification := classifyContactMatch(contactPersonID, persons)
	switch classification {
	case ContactMatchMerge, ContactMatchAmbiguous:
		return nil, newPersonBindingConflict(append(slices.Clone(persons), contactPersonID))
	case ContactMatchBind:
		blocks, err := contactMatchBlockReasonsTx(ctx, tx, []int64{contactPersonID})
		if err != nil {
			return nil, err
		}
		if _, blocked := blocks[contactPersonID]; blocked {
			return nil, ErrPersonCardDAVPublished
		}
		if err := s.bindClusterIntoPersonTx(ctx, tx, candidate.ID, candidate.LeftID, contactPersonID); err != nil {
			return nil, err
		}
	case ContactMatchLinked:
	}
	if _, err := tx.ExecContext(ctx, `UPDATE identity_match_candidates SET
		state = ?, decided_by = ?, decided_at = `+s.dialect.Now()+`, notes = ?,
		pre_conflict_state = NULL, application_pending = FALSE,
		updated_at = `+s.dialect.Now()+` WHERE id = ?`,
		IdentityMatchStateAccepted, decidedBy, stringValue(notes), candidate.ID,
	); err != nil {
		return nil, fmt.Errorf("accept participant-to-person identity candidate: %w", err)
	}
	return getIdentityMatchCandidateTx(ctx, tx, candidate.ID)
}

// bindClusterIntoPersonTx promotes the participant's unbound cluster and
// merges the new profile into the survivor, inside the caller's transaction.
func (s *Store) bindClusterIntoPersonTx(
	ctx context.Context, tx *loggedTx, candidateID, participantID, survivorID int64,
) error {
	promoted, created, err := s.createPersonFromParticipantTx(ctx, tx, participantID)
	if err != nil {
		return err
	}
	if !created {
		// The cluster was checked unbound under the same lock, so this is an
		// invariant failure rather than a race.
		return newPersonBindingConflict([]int64{promoted.ID, survivorID})
	}
	if s.contactMatchBindAfterPromoteHook != nil {
		s.contactMatchBindAfterPromoteHook()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	survivor, err := s.getPersonTx(ctx, tx, survivorID)
	if err != nil {
		return err
	}
	request := PersonMergeRequest{
		SurvivorID:               survivor.ID,
		AbsorbedID:               promoted.ID,
		ExpectedSurvivorRevision: survivor.Revision,
		ExpectedAbsorbedRevision: promoted.Revision,
		IdempotencyKey:           fmt.Sprintf("identity-match-%d-bind-%d", candidateID, promoted.ID),
		Actor:                    contactMatchMergeActor,
	}
	if err := request.validate(); err != nil {
		return err
	}
	requestHash, err := personMergeRequestHash(request)
	if err != nil {
		return err
	}
	if _, err := s.mergePersonsTx(ctx, tx, request, requestHash); err != nil {
		return fmt.Errorf("merge bound cluster into person %d: %w", survivor.ID, err)
	}
	return nil
}
