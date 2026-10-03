package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

// Exact contact matches are decided in code. A contact profile whose exact
// email is an archive identity of one person, with nothing else pointing
// anywhere else, is the same human: the build merges or binds it without
// asking. Everything that needs judgment (a shared mailbox, a phone number,
// several people or several profiles, a blocked merge, or an earlier user
// decision) stays in the review queue.

// ContactMatchAutoActor records decisions the exact contact match rule makes:
// the decided_by of an automatically resolved candidate and the prefix of
// the actor of the person merge it performs.
const ContactMatchAutoActor = "rule:contact_match"

// contactMatchAutoMergeActorPrefix starts the actor of an automatic merge or
// bind; the matched email follows it, so merge history names the evidence.
const contactMatchAutoMergeActorPrefix = ContactMatchAutoActor + ":email:"

// ContactMatchAutoMergeActor is the person merge actor for an automatic
// contact match on the given normalized email.
func ContactMatchAutoMergeActor(normalizedEmail string) string {
	return contactMatchAutoMergeActorPrefix + normalizedEmail
}

// contactMatchAutoNote is the decision note on an automatically resolved
// candidate.
func contactMatchAutoNote(classification ContactMatchClassification, normalizedEmail string) string {
	if classification == ContactMatchBind {
		return "linked automatically: same email " + normalizedEmail
	}
	return "merged automatically: same email " + normalizedEmail
}

// contactMatchLinkedNote closes a pending candidate whose archive identity
// already belongs to the contact profile.
const contactMatchLinkedNote = "already linked"

type writtenContactMatch struct {
	match       ContactMatch
	candidateID int64
}

// contactMatchAutoEligible applies the exact-match rule to one match. The
// finder has already left out owner identities, user rejections, and
// clusters the user said are not a person; notPeople adds clusters any
// classifier (a rule or Jev) considers something other than a person.
func contactMatchAutoEligible(
	match ContactMatch, state IdentityMatchState,
	emailClustersPerContact, emailContactsPerCluster map[int64]int,
	notPeople map[int64]correspondentKindRow,
) bool {
	if state != IdentityMatchStateCandidate || match.BlockedReason != nil {
		return false
	}
	switch match.Classification {
	case ContactMatchMerge:
		if len(match.ClusterPersonIDs) != 1 || match.ClusterPersonIDs[0] == match.ContactPersonID {
			return false
		}
	case ContactMatchBind:
		if len(match.ClusterPersonIDs) != 0 {
			return false
		}
	case ContactMatchAmbiguous, ContactMatchLinked, ContactMatchSharedMailbox:
		return false
	default:
		return false
	}
	if _, ok := contactMatchAutoEmail(match); !ok {
		return false
	}
	if emailClustersPerContact[match.ContactPersonID] != 1 ||
		emailContactsPerCluster[match.ClusterMembers[0]] != 1 {
		return false
	}
	return !slices.ContainsFunc(match.ClusterMembers, func(id int64) bool {
		_, hidden := notPeople[id]
		return hidden
	})
}

// contactMatchAutoEmail returns the exact email that supports a match. A
// phone alone never qualifies: households and offices share numbers.
func contactMatchAutoEmail(match ContactMatch) (string, bool) {
	for _, identifier := range match.Identifiers {
		if identifier.Basis == IdentityMatchEmail && strings.TrimSpace(identifier.NormalizedValue) != "" {
			return identifier.NormalizedValue, true
		}
	}
	return "", false
}

// autoResolveContactMatchesTx merges or binds every written match the exact
// rule decides. Each resolution runs in its own savepoint: a refusal leaves
// that candidate for review without undoing the rest of the build.
func (s *Store) autoResolveContactMatchesTx(
	ctx context.Context, tx *loggedTx, written []writtenContactMatch, result *ContactMatchBuildResult,
) error {
	if len(written) == 0 {
		return nil
	}
	emailClustersPerContact := map[int64]int{}
	emailContactsPerCluster := map[int64]int{}
	for _, item := range written {
		if _, ok := contactMatchAutoEmail(item.match); ok {
			emailClustersPerContact[item.match.ContactPersonID]++
			emailContactsPerCluster[item.match.ClusterMembers[0]]++
		}
	}
	notPeople, err := s.hiddenCorrespondentParticipantsTx(ctx, tx)
	if err != nil {
		return err
	}
	states, err := contactMatchCandidateStatesTx(ctx, tx, written)
	if err != nil {
		return err
	}
	for _, item := range written {
		if !contactMatchAutoEligible(item.match, states[item.candidateID],
			emailClustersPerContact, emailContactsPerCluster, notPeople) {
			continue
		}
		resolved, err := s.autoResolveContactMatchSavepointTx(ctx, tx, item)
		if err != nil {
			return err
		}
		if !resolved {
			continue
		}
		if item.match.Classification == ContactMatchBind {
			result.AutoBound++
		} else {
			result.AutoMerged++
		}
	}
	return nil
}

func contactMatchCandidateStatesTx(
	ctx context.Context, tx *loggedTx, written []writtenContactMatch,
) (map[int64]IdentityMatchState, error) {
	ids := make([]int64, 0, len(written))
	for _, item := range written {
		ids = append(ids, item.candidateID)
	}
	states := map[int64]IdentityMatchState{}
	if err := queryInChunksContext(ctx, tx, ids, nil, `
		SELECT id, state FROM identity_match_candidates WHERE id IN (%s)`,
		func(rows *loggedRows) error {
			var id int64
			var state IdentityMatchState
			if err := rows.Scan(&id, &state); err != nil {
				return fmt.Errorf("scan contact match candidate state: %w", err)
			}
			states[id] = state
			return nil
		}); err != nil {
		return nil, fmt.Errorf("load contact match candidate states: %w", err)
	}
	return states, nil
}

// autoResolveContactMatchSavepointTx applies one automatic resolution inside
// a savepoint. It reports false, with the savepoint rolled back, when the
// merge path refused it; cancellation still fails the build.
func (s *Store) autoResolveContactMatchSavepointTx(
	ctx context.Context, tx *loggedTx, item writtenContactMatch,
) (bool, error) {
	const savepoint = "contact_match_auto"
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+savepoint); err != nil {
		return false, fmt.Errorf("create contact match savepoint: %w", err)
	}
	applyErr := s.autoResolveContactMatchTx(ctx, tx, item)
	if applyErr == nil {
		if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+savepoint); err != nil {
			return false, fmt.Errorf("release contact match savepoint: %w", err)
		}
		return true, nil
	}
	if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+savepoint); err != nil {
		return false, errors.Join(applyErr, fmt.Errorf("roll back contact match savepoint: %w", err))
	}
	if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+savepoint); err != nil {
		return false, errors.Join(applyErr, fmt.Errorf("release contact match savepoint: %w", err))
	}
	if ctx.Err() != nil {
		return false, applyErr
	}
	slog.WarnContext(ctx, "exact contact match left for review",
		"candidate_id", item.candidateID, "classification", item.match.Classification,
		"error", applyErr)
	return false, nil
}

// autoResolveContactMatchTx merges the contact profile into the archive
// person (merge) or binds the unbound cluster to the contact profile (bind)
// through the same merge path a manual decision uses, then records the
// candidate as accepted by the rule. The candidate is decided before the
// merge so the merge snapshot, and therefore a later split, carries it.
func (s *Store) autoResolveContactMatchTx(
	ctx context.Context, tx *loggedTx, item writtenContactMatch,
) error {
	email, _ := contactMatchAutoEmail(item.match)
	if _, err := tx.ExecContext(ctx, `UPDATE identity_match_candidates SET
		state = ?, decided_by = ?, decided_at = `+s.dialect.Now()+`, notes = ?,
		pre_conflict_state = NULL, application_pending = FALSE,
		updated_at = `+s.dialect.Now()+` WHERE id = ? AND state = ?`,
		IdentityMatchStateAccepted, ContactMatchAutoActor,
		contactMatchAutoNote(item.match.Classification, email),
		item.candidateID, IdentityMatchStateCandidate,
	); err != nil {
		return fmt.Errorf("accept exact contact match %d: %w", item.candidateID, err)
	}
	if err := dropCandidateDecisionSnapshotTx(ctx, tx, item.candidateID); err != nil {
		return err
	}
	actor := ContactMatchAutoMergeActor(email)
	if item.match.Classification == ContactMatchBind {
		return s.bindClusterIntoPersonTx(
			ctx, tx, item.candidateID, item.match.ParticipantID, item.match.ContactPersonID, actor)
	}
	survivor, err := s.getPersonTx(ctx, tx, item.match.ClusterPersonIDs[0])
	if err != nil {
		return err
	}
	absorbed, err := s.getPersonTx(ctx, tx, item.match.ContactPersonID)
	if err != nil {
		return err
	}
	request := PersonMergeRequest{
		SurvivorID:               survivor.ID,
		AbsorbedID:               absorbed.ID,
		ExpectedSurvivorRevision: survivor.Revision,
		ExpectedAbsorbedRevision: absorbed.Revision,
		IdempotencyKey: fmt.Sprintf("contact-match-%d-merge-%d.%d-%d.%d", item.candidateID,
			survivor.ID, survivor.Revision, absorbed.ID, absorbed.Revision),
		Actor: actor,
	}
	if err := request.validate(); err != nil {
		return err
	}
	requestHash, err := personMergeRequestHash(request)
	if err != nil {
		return err
	}
	if _, err := s.mergePersonsTx(ctx, tx, request, requestHash); err != nil {
		return fmt.Errorf("merge contact profile %d into person %d: %w", absorbed.ID, survivor.ID, err)
	}
	return nil
}

// closeLinkedContactMatchCandidatesTx accepts pending contact-match
// candidates whose archive identity already belongs to the contact profile,
// for example because the two were merged by hand. They are already true.
func (s *Store) closeLinkedContactMatchCandidatesTx(ctx context.Context, tx *loggedTx) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, left_id, right_id
		FROM identity_match_candidates
		WHERE source_ref = ? AND left_kind = ? AND right_kind = ? AND state = ?
		ORDER BY id`,
		ContactMatchSourceRef, IdentityMatchParticipant, IdentityMatchPerson,
		IdentityMatchStateCandidate)
	if err != nil {
		return 0, fmt.Errorf("load pending contact match candidates: %w", err)
	}
	type pendingRow struct{ id, participantID, personID int64 }
	pending := []pendingRow{}
	for rows.Next() {
		var row pendingRow
		if err := rows.Scan(&row.id, &row.participantID, &row.personID); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan pending contact match candidate: %w", err)
		}
		pending = append(pending, row)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close pending contact match candidates: %w", err)
	}
	if len(pending) == 0 {
		return 0, nil
	}
	edges, err := s.loadLinkEdgesTxContext(ctx, tx)
	if err != nil {
		return 0, err
	}
	closed := 0
	for _, row := range pending {
		members := sortedComponentMembers(row.participantID, edges)
		persons, err := personIDsForParticipantsTx(ctx, tx, members)
		if err != nil {
			return closed, err
		}
		if classifyContactMatch(row.personID, persons) != ContactMatchLinked {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE identity_match_candidates SET
			state = ?, decided_by = ?, decided_at = `+s.dialect.Now()+`, notes = ?,
			pre_conflict_state = NULL, application_pending = FALSE,
			updated_at = `+s.dialect.Now()+` WHERE id = ? AND state = ?`,
			IdentityMatchStateAccepted, ContactMatchAutoActor, contactMatchLinkedNote,
			row.id, IdentityMatchStateCandidate,
		); err != nil {
			return closed, fmt.Errorf("close linked contact match %d: %w", row.id, err)
		}
		if err := dropCandidateDecisionSnapshotTx(ctx, tx, row.id); err != nil {
			return closed, err
		}
		closed++
	}
	return closed, nil
}

// pendingContactMatchCountTx counts contact-match candidates still waiting
// for a decision.
func pendingContactMatchCountTx(ctx context.Context, tx *loggedTx) (int, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM identity_match_candidates
		WHERE source_ref = ? AND left_kind = ? AND right_kind = ? AND state = ?`,
		ContactMatchSourceRef, IdentityMatchParticipant, IdentityMatchPerson,
		IdentityMatchStateCandidate).Scan(&count); err != nil {
		return 0, fmt.Errorf("count pending contact matches: %w", err)
	}
	return count, nil
}

// rememberContactProfileSplitTx keeps a split from being undone by contact
// matching. When one side of a split is left with no archive identity, it is
// a contact-only profile again, and its exact email would match the other
// side's identities: the rule would merge it straight back, or the queue
// would ask again. Every contact-match candidate between the two sides is
// rejected, and each identity cluster of the other side gets a rejection
// against the contact-only side if it has none, as the user's decision.
func (s *Store) rememberContactProfileSplitTx(
	ctx context.Context, tx *loggedTx, firstID, secondID int64, actor string,
) error {
	firstParticipants, err := personParticipantIDsTx(ctx, tx, firstID)
	if err != nil {
		return err
	}
	secondParticipants, err := personParticipantIDsTx(ctx, tx, secondID)
	if err != nil {
		return err
	}
	var contactID int64
	var participants []int64
	switch {
	case len(firstParticipants) == 0 && len(secondParticipants) > 0:
		contactID, participants = firstID, secondParticipants
	case len(secondParticipants) == 0 && len(firstParticipants) > 0:
		contactID, participants = secondID, firstParticipants
	default:
		return nil
	}
	edges, err := s.loadLinkEdgesTxContext(ctx, tx)
	if err != nil {
		return err
	}
	roots := clustersFromEdges(edges)
	clusters := map[int64][]int64{}
	for _, id := range participants {
		root, ok := roots[id]
		if !ok {
			root = id
		}
		clusters[root] = append(clusters[root], id)
	}
	for _, members := range clusters {
		slices.Sort(members)
		if err := s.rejectClusterForContactProfileTx(ctx, tx, members, contactID, actor); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) rejectClusterForContactProfileTx(
	ctx context.Context, tx *loggedTx, members []int64, personID int64, actor string,
) error {
	if err := execInChunksContext(ctx, tx, members,
		[]any{IdentityMatchStateRejected, actor, personSplitNote,
			IdentityMatchParticipant, IdentityMatchPerson, personID, IdentityMatchStateRejected},
		`UPDATE identity_match_candidates SET
			state = ?, decided_by = ?, decided_at = `+s.dialect.Now()+`, notes = ?,
			pre_conflict_state = NULL, application_pending = FALSE,
			updated_at = `+s.dialect.Now()+`
		WHERE left_kind = ? AND right_kind = ? AND right_id = ? AND state <> ?
		  AND left_id IN (%s)`); err != nil {
		return fmt.Errorf("reject split contact matches: %w", err)
	}
	rejected := false
	if err := queryInChunksContext(ctx, tx, members,
		[]any{IdentityMatchParticipant, IdentityMatchPerson, personID, IdentityMatchStateRejected}, `
		SELECT id FROM identity_match_candidates
		WHERE left_kind = ? AND right_kind = ? AND right_id = ? AND state = ?
		  AND left_id IN (%s)`, func(rows *loggedRows) error {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("scan split contact match rejection: %w", err)
			}
			rejected = true
			return nil
		}); err != nil {
		return fmt.Errorf("check split contact match rejections: %w", err)
	}
	if rejected {
		return nil
	}
	representative := members[0]
	basis, normalized, err := participantTombstoneBasisTx(ctx, tx, representative)
	if err != nil {
		return err
	}
	sourceRef := ContactMatchSourceRef
	if _, err := tx.ExecContext(ctx, `INSERT INTO identity_match_candidates (
		left_kind, left_id, right_kind, right_id, basis, normalized_value, state,
		source, source_ref, notes, decided_by, decided_at, application_pending,
		created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, `+s.dialect.Now()+`, FALSE,
		`+s.dialect.Now()+`, `+s.dialect.Now()+`)`,
		IdentityMatchParticipant, representative, IdentityMatchPerson, personID,
		basis, normalized, IdentityMatchStateRejected,
		ProvenanceUser, sourceRef, personSplitNote, actor,
	); err != nil {
		return fmt.Errorf("record split contact profile rejection: %w", err)
	}
	return nil
}

func personParticipantIDsTx(ctx context.Context, tx *loggedTx, personID int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT participant_id FROM person_participants
		WHERE person_id = ? ORDER BY participant_id`, personID)
	if err != nil {
		return nil, fmt.Errorf("load person %d participants: %w", personID, err)
	}
	defer func() { _ = rows.Close() }()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan person %d participant: %w", personID, err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
