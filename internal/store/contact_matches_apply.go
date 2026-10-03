package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.kenn.io/msgvault/internal/textimport"
)

// ErrContactMatchOwnerIdentity reports that the matched participant cluster
// contains one of the owner's own identities, so it must not be bound to a
// contact profile.
var ErrContactMatchOwnerIdentity = errors.New(
	"the matched archive identity belongs to the archive owner")

// ErrContactMatchStale reports that the profile's current email and phone
// addresses no longer exactly match any identity in the cluster.
var ErrContactMatchStale = errors.New(
	"the profile's addresses no longer match this archive identity")

// ErrContactMatchRejectedInCluster reports that the user already rejected
// another identity in the same cluster for this person, so binding the
// cluster would bind the rejected identity too.
var ErrContactMatchRejectedInCluster = errors.New(
	"another identity in this cluster was rejected for this profile")

// ErrContactMatchNotAPerson reports that the user classified the matched
// cluster as an organization, a shared mailbox, or ignored, so it is never
// bound to or merged with a person.
var ErrContactMatchNotAPerson = errors.New(
	"the matched archive identity is not a person")

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
	if err := s.contactMatchAcceptGuardsTx(ctx, tx, *candidate, members); err != nil {
		return nil, err
	}
	persons, err := personIDsForParticipantsTx(ctx, tx, members)
	if err != nil {
		return nil, err
	}
	contactPersonID := candidate.RightID
	classification := classifyContactMatch(contactPersonID, persons)
	if classification != ContactMatchLinked {
		signals, err := s.sharedMailboxSignalsTx(ctx, tx, map[int64][]int64{0: members})
		if err != nil {
			return nil, err
		}
		if _, shared := signals[0]; shared {
			return nil, ErrContactMatchSharedMailbox
		}
	}
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
		if err := s.bindClusterIntoPersonTx(
			ctx, tx, candidate.ID, candidate.LeftID, contactPersonID, contactMatchMergeActor,
		); err != nil {
			return nil, err
		}
	case ContactMatchLinked:
	case ContactMatchSharedMailbox:
		// classifyContactMatch never returns it; the signal is checked above.
		return nil, ErrContactMatchSharedMailbox
	}
	if _, err := tx.ExecContext(ctx, `UPDATE identity_match_candidates SET
		state = ?, decided_by = ?, decided_at = `+s.dialect.Now()+`, notes = ?,
		pre_conflict_state = NULL, application_pending = FALSE,
		updated_at = `+s.dialect.Now()+` WHERE id = ?`,
		IdentityMatchStateAccepted, decidedBy, stringValue(notes), candidate.ID,
	); err != nil {
		return nil, fmt.Errorf("accept participant-to-person identity candidate: %w", err)
	}
	if err := dropCandidateDecisionSnapshotTx(ctx, tx, candidate.ID); err != nil {
		return nil, err
	}
	return getIdentityMatchCandidateTx(ctx, tx, candidate.ID)
}

// contactMatchAcceptGuardsTx re-checks, under the identity lock, the rules
// that decided the candidate when it was built, because the archive may have
// changed since: the cluster must not contain an owner identity, no member
// may have a rejected match with the person, and one of the person's current
// addresses must still exactly match a cluster member.
func (s *Store) contactMatchAcceptGuardsTx(
	ctx context.Context, tx *loggedTx, candidate IdentityMatchCandidate, members []int64,
) error {
	owners, err := ownerParticipantIDsTx(ctx, tx)
	if err != nil {
		return err
	}
	for _, member := range members {
		if _, owner := owners[member]; owner {
			return ErrContactMatchOwnerIdentity
		}
	}
	notAPerson, err := s.participantsClassifiedNotPersonTx(ctx, tx, members)
	if err != nil {
		return err
	}
	if notAPerson {
		return ErrContactMatchNotAPerson
	}
	rejected, err := clusterHasRejectedPersonMatchTx(ctx, tx, candidate, members)
	if err != nil {
		return err
	}
	if rejected {
		return ErrContactMatchRejectedInCluster
	}
	matched, err := clusterMatchesPersonAddressesTx(ctx, tx, candidate.RightID, members)
	if err != nil {
		return err
	}
	if !matched {
		return ErrContactMatchStale
	}
	return nil
}

// clusterHasRejectedPersonMatchTx reports whether any other
// participant-to-person candidate between a cluster member and the person
// was rejected.
func clusterHasRejectedPersonMatchTx(
	ctx context.Context, tx *loggedTx, candidate IdentityMatchCandidate, members []int64,
) (bool, error) {
	found := false
	if err := queryInChunksContext(ctx, tx, members,
		[]any{candidate.ID, IdentityMatchStateRejected, IdentityMatchParticipant,
			IdentityMatchPerson, candidate.RightID}, `
		SELECT id FROM identity_match_candidates
		WHERE id <> ? AND state = ? AND left_kind = ? AND right_kind = ? AND right_id = ?
		  AND left_id IN (%s)`, func(rows *loggedRows) error {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("scan rejected cluster match: %w", err)
			}
			found = true
			return nil
		}); err != nil {
		return false, fmt.Errorf("check rejected cluster matches: %w", err)
	}
	return found, nil
}

// clusterMatchesPersonAddressesTx reports whether any active email or phone
// contact point of the person exactly equals an email or phone of a cluster
// member, with the same normalization the finder uses.
func clusterMatchesPersonAddressesTx(
	ctx context.Context, tx *loggedTx, personID int64, members []int64,
) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT address_kind, normalized_value
		FROM person_contact_points
		WHERE person_id = ? AND address_kind IN (?, ?)
		  AND active_until IS NULL AND superseded_at IS NULL`,
		personID, ContactAddressEmail, ContactAddressPhone)
	if err != nil {
		return false, fmt.Errorf("load contact match person addresses: %w", err)
	}
	want := map[string]struct{}{}
	for rows.Next() {
		var kind ContactAddressKind
		var value string
		if err := rows.Scan(&kind, &value); err != nil {
			_ = rows.Close()
			return false, fmt.Errorf("scan contact match person address: %w", err)
		}
		if key, ok := contactMatchAddressKey(string(kind), value); ok {
			want[key] = struct{}{}
		}
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close contact match person addresses: %w", err)
	}
	if len(want) == 0 {
		return false, nil
	}
	found := false
	check := func(kind, value string) {
		if key, ok := contactMatchAddressKey(kind, value); ok {
			if _, hit := want[key]; hit {
				found = true
			}
		}
	}
	if err := queryInChunksContext(ctx, tx, members, nil, `
		SELECT email_address, phone_number FROM participants WHERE id IN (%s)`,
		func(rows *loggedRows) error {
			var email, phone sql.NullString
			if err := rows.Scan(&email, &phone); err != nil {
				return fmt.Errorf("scan contact match cluster member: %w", err)
			}
			check(string(ContactAddressEmail), email.String)
			check(string(ContactAddressPhone), phone.String)
			return nil
		}); err != nil {
		return false, err
	}
	if err := queryInChunksContext(ctx, tx, members, nil, `
		SELECT identifier_type, identifier_value FROM participant_identifiers
		WHERE identifier_type IN ('email', 'phone') AND participant_id IN (%s)`,
		func(rows *loggedRows) error {
			var kind, value string
			if err := rows.Scan(&kind, &value); err != nil {
				return fmt.Errorf("scan contact match cluster identifier: %w", err)
			}
			check(kind, value)
			return nil
		}); err != nil {
		return false, err
	}
	return found, nil
}

// contactMatchAddressKey normalizes an email or phone the way the finder
// compares them.
func contactMatchAddressKey(kind, value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	switch kind {
	case string(ContactAddressEmail):
		return "email:" + strings.ToLower(value), true
	case string(ContactAddressPhone):
		normalized, err := textimport.NormalizePhone(value)
		if err != nil {
			return "", false
		}
		return "phone:" + normalized, true
	default:
		return "", false
	}
}

// bindClusterIntoPersonTx promotes the participant's unbound cluster and
// merges the new profile into the survivor, inside the caller's transaction.
// The actor is recorded on the merge: the user for an explicit accept, the
// exact contact match rule for an automatic one.
func (s *Store) bindClusterIntoPersonTx(
	ctx context.Context, tx *loggedTx, candidateID, participantID, survivorID int64, actor string,
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
		Actor:                    actor,
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
