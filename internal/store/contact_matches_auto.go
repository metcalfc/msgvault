package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"go.kenn.io/msgvault/internal/correspondentkind"
)

// Exact contact matches are decided in code. A contact profile whose exact
// email is an archive identity of one person, with nothing else pointing
// anywhere else, is the same human: the build merges or binds it without
// asking. Everything that needs judgment (a shared mailbox, a phone number,
// several people or several profiles, a profile that holds several cards or
// names, a blocked merge, or an earlier user decision) stays in the review
// queue.

// ContactMatchAutoActor records decisions the exact contact match rule makes:
// the decided_by of an automatically resolved candidate and the prefix of
// the actor of the person merge it performs.
const ContactMatchAutoActor = "rule:contact_match"

// contactMatchAutoMergeActorPrefix starts the actor of an automatic merge or
// bind; the matched email follows it, so merge history names the evidence.
const contactMatchAutoMergeActorPrefix = ContactMatchAutoActor + ":email:"

// contactMatchAutoBatchSize bounds the resolutions applied in one
// transaction, so a large first run never holds the write lock for long.
const contactMatchAutoBatchSize = 25

// ContactMatchAutoMergeActor is the person merge actor for an automatic
// contact match on the given normalized email.
func ContactMatchAutoMergeActor(normalizedEmail string) string {
	return contactMatchAutoMergeActorPrefix + normalizedEmail
}

// Actions the exact contact match rule takes.
const (
	ContactMatchAutoActionMerge       = "merge"
	ContactMatchAutoActionBind        = "bind"
	ContactMatchAutoActionCloseLinked = "close_linked"
)

// ContactMatchAutoAction is one decision the exact contact match rule made,
// or would make in a dry run. It carries IDs only, never names.
type ContactMatchAutoAction struct {
	Action string `json:"action" enum:"merge,bind,close_linked"`
	// CandidateID is zero in a dry run for a match not written yet.
	CandidateID     int64 `json:"candidate_id"`
	ContactPersonID int64 `json:"contact_person_id"`
	// PersonID is the surviving archive person of a merge.
	PersonID      int64 `json:"person_id,omitzero"`
	ParticipantID int64 `json:"participant_id"`
}

// ContactMatchBuildOptions controls one contact-match refresh.
type ContactMatchBuildOptions struct {
	// DryRun reports what the refresh would decide without writing.
	DryRun bool
}

// SetContactMatchAutoResolve turns the exact contact match rule on or off.
// It is on unless configuration turns it off; when off, a refresh only
// proposes candidates, as before the rule existed.
func (s *Store) SetContactMatchAutoResolve(enabled bool) {
	s.contactMatchAutoDisabled.Store(!enabled)
}

func (s *Store) contactMatchAutoResolveEnabled() bool {
	return !s.contactMatchAutoDisabled.Load()
}

// SetIdentityDatasetsRefresher installs the refresh of identity-derived
// analytics that background identity changes run, such as automatic contact
// merges after a CardDAV sync.
func (s *Store) SetIdentityDatasetsRefresher(refresh func(context.Context) error) {
	if refresh == nil {
		s.identityDatasetsRefresher.Store(nil)
		return
	}
	s.identityDatasetsRefresher.Store(&refresh)
}

// RefreshIdentityDatasetsAfterChange runs the installed identity dataset
// refresh. Without one (a CLI store or a test) it does nothing.
func (s *Store) RefreshIdentityDatasetsAfterChange(ctx context.Context) error {
	refresh := s.identityDatasetsRefresher.Load()
	if refresh == nil {
		return nil
	}
	return (*refresh)(ctx)
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

// contactMatchAutoPlan is one resolution the rule decided, applied later in
// its own batch after re-validation.
type contactMatchAutoPlan struct {
	classification  ContactMatchClassification
	candidateID     int64
	contactPersonID int64
	survivorID      int64
	participantID   int64
	email           string
}

func (p contactMatchAutoPlan) action() ContactMatchAutoAction {
	action := ContactMatchAutoAction{
		Action: ContactMatchAutoActionMerge, CandidateID: p.candidateID,
		ContactPersonID: p.contactPersonID, PersonID: p.survivorID, ParticipantID: p.participantID,
	}
	if p.classification == ContactMatchBind {
		action.Action = ContactMatchAutoActionBind
	}
	return action
}

// contactMatchAutoEligible applies the parts of the exact-match rule that
// the finder's match carries. The finder has already left out owner
// identities, user rejections, and clusters the user said are not a person;
// notPeople adds clusters any classifier (a rule or Jev) considers something
// other than a person.
func contactMatchAutoEligible(
	match ContactMatch, state IdentityMatchState, notPeople map[int64]correspondentKindRow,
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
	// One contact, one cluster, counted before any filter dropped a pair,
	// and no phone of the contact pointing at somebody else.
	if match.autoEmailClusters != 1 || match.autoEmailContacts != 1 || match.autoPhoneConflict {
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

// contactMatchNamesSeveralPeopleTx reports whether a match may join
// different humans. CardDAV import binds every card that lists an address
// the profile already has, so a family's cards sharing one email become one
// profile: several bound cards leave the match for review. So do names that
// describe more than one person across both sides: the contact profile's
// display name and current names, the surviving person's display name
// (merge only), and the names the identities carry in message headers (the
// matched cluster and, for a merge, every identity of the survivor). Names
// that differ only by case, order, initials, or a short form ("Bob Smith",
// "Bob", "Smith, Bob") are one person; a nickname that is not a prefix
// ("Bob" for "Robert") is not, so it waits for review.
func contactMatchNamesSeveralPeopleTx(
	ctx context.Context, tx *loggedTx, contactPersonID, survivorID int64, members []int64, email string,
) (bool, error) {
	var cards int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM carddav_resources WHERE person_id = ?`,
		contactPersonID).Scan(&cards); err != nil {
		return false, fmt.Errorf("count contact profile cards: %w", err)
	}
	if cards > 1 {
		return true, nil
	}
	names := []string{}
	rows, err := tx.QueryContext(ctx, `SELECT display_name FROM persons
		WHERE id IN (?, ?) AND display_name IS NOT NULL
		UNION ALL
		SELECT COALESCE(NULLIF(TRIM(formatted), ''), original_value) FROM person_names
		WHERE person_id = ? AND active_until IS NULL AND superseded_at IS NULL`,
		contactPersonID, survivorID, contactPersonID)
	if err != nil {
		return false, fmt.Errorf("load contact match names: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, fmt.Errorf("scan contact match name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate contact match names: %w", err)
	}
	identities := slices.Clone(members)
	if survivorID != 0 {
		own, err := personParticipantIDsTx(ctx, tx, survivorID)
		if err != nil {
			return false, err
		}
		identities = append(identities, own...)
		slices.Sort(identities)
		identities = slices.Compact(identities)
	}
	headerNames, err := clusterDistinctDisplayNamesTx(ctx, tx, identities)
	if err != nil {
		return false, err
	}
	names = append(names, headerNames...)
	return len(correspondentkind.DistinctPersonNames(email, names)) > 1, nil
}

// planContactMatchAutoTx decides which written matches the rule resolves.
func (s *Store) planContactMatchAutoTx(
	ctx context.Context, tx *loggedTx, written []writtenContactMatch,
) ([]contactMatchAutoPlan, error) {
	if len(written) == 0 || !s.contactMatchAutoResolveEnabled() {
		return nil, nil
	}
	notPeople, err := s.hiddenCorrespondentParticipantsTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	states, err := contactMatchCandidateStatesTx(ctx, tx, written)
	if err != nil {
		return nil, err
	}
	separations, err := loadPersonSeparationsTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	plan := []contactMatchAutoPlan{}
	for _, item := range written {
		state := IdentityMatchStateCandidate
		if item.candidateID != 0 {
			state = states[item.candidateID]
		}
		if !contactMatchAutoEligible(item.match, state, notPeople) {
			continue
		}
		email, _ := contactMatchAutoEmail(item.match)
		entry := contactMatchAutoPlan{
			classification: item.match.Classification, candidateID: item.candidateID,
			contactPersonID: item.match.ContactPersonID, participantID: item.match.ParticipantID,
			email: email,
		}
		if entry.classification == ContactMatchMerge {
			entry.survivorID = item.match.ClusterPersonIDs[0]
			if separations.separated(entry.contactPersonID, entry.survivorID) {
				continue
			}
		}
		several, err := contactMatchNamesSeveralPeopleTx(ctx, tx,
			entry.contactPersonID, entry.survivorID, item.match.ClusterMembers, email)
		if err != nil {
			return nil, err
		}
		if several {
			continue
		}
		plan = append(plan, entry)
	}
	return plan, nil
}

func contactMatchCandidateStatesTx(
	ctx context.Context, tx *loggedTx, written []writtenContactMatch,
) (map[int64]IdentityMatchState, error) {
	ids := make([]int64, 0, len(written))
	for _, item := range written {
		if item.candidateID != 0 {
			ids = append(ids, item.candidateID)
		}
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

// applyContactMatchAutoPlanContext applies the plan in bounded batches,
// each in its own transaction under the identity lock, re-validating every
// item against the archive as it is then.
func (s *Store) applyContactMatchAutoPlanContext(
	ctx context.Context, plan []contactMatchAutoPlan, result *ContactMatchBuildResult,
) error {
	for start := 0; start < len(plan); start += contactMatchAutoBatchSize {
		batch := plan[start:min(start+contactMatchAutoBatchSize, len(plan))]
		var applied []contactMatchAutoPlan
		err := s.withTxContext(ctx, func(tx *loggedTx) error {
			var err error
			applied, err = s.applyContactMatchAutoBatchTx(ctx, tx, batch)
			return err
		})
		if err != nil {
			return err
		}
		recordContactMatchAutoApplied(result, applied)
	}
	return nil
}

// applyContactMatchAutoBatchTx re-validates and applies one batch under the
// identity lock and returns the resolutions it applied.
func (s *Store) applyContactMatchAutoBatchTx(
	ctx context.Context, tx *loggedTx, batch []contactMatchAutoPlan,
) ([]contactMatchAutoPlan, error) {
	if err := s.lockIdentityMutationTxContext(ctx, tx); err != nil {
		return nil, err
	}
	checks, err := s.loadContactMatchAutoChecksTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	applied := []contactMatchAutoPlan{}
	for _, item := range batch {
		eligible, err := s.contactMatchAutoStillEligibleTx(ctx, tx, item, checks)
		if err != nil {
			return nil, err
		}
		if !eligible {
			continue
		}
		resolved, err := s.autoResolveContactMatchSavepointTx(ctx, tx, item)
		if err != nil {
			return nil, err
		}
		if resolved {
			applied = append(applied, item)
		}
	}
	return applied, nil
}

func recordContactMatchAutoApplied(result *ContactMatchBuildResult, applied []contactMatchAutoPlan) {
	for _, item := range applied {
		if item.classification == ContactMatchBind {
			result.AutoBound++
		} else {
			result.AutoMerged++
		}
		result.Actions = append(result.Actions, item.action())
	}
}

// contactMatchAutoChecks is archive-wide state re-validation reads once per
// batch.
type contactMatchAutoChecks struct {
	edges       []linkEdge
	notPeople   map[int64]correspondentKindRow
	separations *personSeparations
}

func (s *Store) loadContactMatchAutoChecksTx(
	ctx context.Context, tx *loggedTx,
) (*contactMatchAutoChecks, error) {
	edges, err := s.loadLinkEdgesTxContext(ctx, tx)
	if err != nil {
		return nil, err
	}
	notPeople, err := s.hiddenCorrespondentParticipantsTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	separations, err := loadPersonSeparationsTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	return &contactMatchAutoChecks{edges: edges, notPeople: notPeople, separations: separations}, nil
}

// contactMatchAutoStillEligibleTx re-checks one planned resolution under
// the identity lock: the candidate is still pending, the contact profile
// still has no archive identity, and no accept guard fails. The match is
// then found again from the archive as it is now, with its one-to-one email
// counts, phone conflict, cluster persons, shared mailbox signal, CardDAV
// block, and not-a-person classifications recomputed, and must pass the
// same rule as at planning; so must the split and name checks.
func (s *Store) contactMatchAutoStillEligibleTx(
	ctx context.Context, tx *loggedTx, item contactMatchAutoPlan, checks *contactMatchAutoChecks,
) (bool, error) {
	candidate, err := getIdentityMatchCandidateTx(ctx, tx, item.candidateID)
	if errors.Is(err, ErrIdentityMatchNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if candidate.State != IdentityMatchStateCandidate ||
		candidate.LeftKind != IdentityMatchParticipant || candidate.LeftID != item.participantID ||
		candidate.RightKind != IdentityMatchPerson || candidate.RightID != item.contactPersonID {
		return false, nil
	}
	own, err := personParticipantIDsTx(ctx, tx, item.contactPersonID)
	if err != nil {
		return false, err
	}
	if len(own) > 0 {
		return false, nil
	}
	members := sortedComponentMembers(item.participantID, checks.edges)
	if err := s.contactMatchAcceptGuardsTx(ctx, tx, *candidate, members); err != nil {
		if isContactMatchRetirement(err) {
			return false, nil
		}
		return false, err
	}
	match, found, err := s.currentContactMatchTx(ctx, tx, item.contactPersonID, item.participantID, members)
	if err != nil || !found {
		return false, err
	}
	if match.Classification != item.classification ||
		!contactMatchAutoEligible(match, candidate.State, checks.notPeople) {
		return false, nil
	}
	if item.classification == ContactMatchMerge {
		if match.ClusterPersonIDs[0] != item.survivorID ||
			checks.separations.separated(item.contactPersonID, item.survivorID) {
			return false, nil
		}
	}
	several, err := contactMatchNamesSeveralPeopleTx(ctx, tx,
		item.contactPersonID, item.survivorID, members, item.email)
	if err != nil {
		return false, err
	}
	return !several, nil
}

// currentContactMatchTx finds the match between a contact profile and the
// identity cluster holding participantID as the archive stands now,
// counting every contact point that bears on the pair.
func (s *Store) currentContactMatchTx(
	ctx context.Context, tx *loggedTx, contactPersonID, participantID int64, members []int64,
) (ContactMatch, bool, error) {
	points, err := contactMatchPointsForPairTx(ctx, tx, contactPersonID, members)
	if err != nil {
		return ContactMatch{}, false, err
	}
	matches, err := s.findContactMatchesForPointsTx(ctx, tx, points)
	if err != nil {
		return ContactMatch{}, false, err
	}
	for _, match := range matches {
		if match.ContactPersonID == contactPersonID && slices.Contains(match.ClusterMembers, participantID) {
			return match, true, nil
		}
	}
	return ContactMatch{}, false, nil
}

// isExpectedContactMatchRefusal reports whether a failed resolution is one
// of the refusals the merge path is designed to return for a state change,
// which leaves the candidate for review. Anything else is a fault.
func isExpectedContactMatchRefusal(err error) bool {
	return errors.Is(err, ErrPersonCardDAVPublished) ||
		errors.Is(err, ErrPersonEnrichmentDispatchInProgress) ||
		errors.Is(err, ErrPersonRevisionConflict) ||
		errors.Is(err, ErrPersonBindingConflict) ||
		errors.Is(err, ErrPersonMergeLineageConflict) ||
		errors.Is(err, ErrPersonNotFound) ||
		errors.Is(err, ErrIdentityMatchNotFound) ||
		errors.Is(err, ErrContactMatchSharedMailbox) ||
		isContactMatchRetirement(err)
}

// autoResolveContactMatchSavepointTx applies one automatic resolution inside
// a savepoint. An expected refusal rolls the savepoint back and reports
// false, leaving the candidate for review; any other error fails the build.
func (s *Store) autoResolveContactMatchSavepointTx(
	ctx context.Context, tx *loggedTx, item contactMatchAutoPlan,
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
	if ctx.Err() != nil || !isExpectedContactMatchRefusal(applyErr) {
		return false, fmt.Errorf("resolve exact contact match %d: %w", item.candidateID, applyErr)
	}
	slog.WarnContext(ctx, "exact contact match left for review",
		"candidate_id", item.candidateID, "classification", item.classification, "error", applyErr)
	return false, nil
}

// autoResolveContactMatchTx merges the contact profile into the archive
// person (merge) or binds the unbound cluster to the contact profile (bind)
// through the same merge path a manual decision uses, then records the
// candidate as accepted by the rule. The candidate is decided before the
// merge so the merge snapshot, and therefore a later split, carries it.
func (s *Store) autoResolveContactMatchTx(
	ctx context.Context, tx *loggedTx, item contactMatchAutoPlan,
) error {
	if s.contactMatchAutoResolveHook != nil {
		if err := s.contactMatchAutoResolveHook(); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE identity_match_candidates SET
		state = ?, decided_by = ?, decided_at = `+s.dialect.Now()+`, notes = ?,
		pre_conflict_state = NULL, application_pending = FALSE,
		updated_at = `+s.dialect.Now()+` WHERE id = ? AND state = ?`,
		IdentityMatchStateAccepted, ContactMatchAutoActor,
		contactMatchAutoNote(item.classification, item.email),
		item.candidateID, IdentityMatchStateCandidate,
	); err != nil {
		return fmt.Errorf("accept exact contact match %d: %w", item.candidateID, err)
	}
	if err := dropCandidateDecisionSnapshotTx(ctx, tx, item.candidateID); err != nil {
		return err
	}
	actor := ContactMatchAutoMergeActor(item.email)
	if item.classification == ContactMatchBind {
		return s.bindClusterIntoPersonTx(
			ctx, tx, item.candidateID, item.participantID, item.contactPersonID, actor)
	}
	survivor, err := s.getPersonTx(ctx, tx, item.survivorID)
	if err != nil {
		return err
	}
	absorbed, err := s.getPersonTx(ctx, tx, item.contactPersonID)
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
	return s.adoptContactDisplayNameTx(ctx, tx, survivor.ID, absorbed.DisplayName)
}

// adoptContactDisplayNameTx names the survivor of an automatic merge after
// the contact card, which the user curates, when the survivor's display
// name is provably still the archive's: none, or the name promotion chose
// from several header names (its seed, which every rename deletes). A name
// equal to a header name may be one the user picked, so it is kept, as is
// every other name.
func (s *Store) adoptContactDisplayNameTx(
	ctx context.Context, tx *loggedTx, personID int64, contactName *string,
) error {
	contactName = normalizePersonDisplayName(contactName)
	if contactName == nil {
		return nil
	}
	var current, seeded *string
	if err := tx.QueryRowContext(ctx, `SELECT p.display_name, s.seeded_name FROM persons p
		LEFT JOIN person_display_name_seeds s ON s.person_id = p.id
		WHERE p.id = ?`, personID).Scan(&current, &seeded); err != nil {
		return fmt.Errorf("read survivor display name: %w", err)
	}
	if current != nil && *current == *contactName {
		return nil
	}
	derived := current == nil || strings.TrimSpace(*current) == "" ||
		(seeded != nil && *seeded == *current)
	if !derived {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE persons SET display_name = ?,
		display_name_changed_at = `+s.dialect.Now()+` WHERE id = ?`,
		*contactName, personID); err != nil {
		return fmt.Errorf("name survivor after contact card: %w", err)
	}
	if err := dropDisplayNameSeedTx(ctx, tx, personID); err != nil {
		return err
	}
	if err := s.bumpPersonDisplayNameRevisionContext(ctx, tx); err != nil {
		return err
	}
	if err := s.bumpDisplayNameCounterpartVCardProjectionsTx(ctx, tx, personID); err != nil {
		return err
	}
	if err := s.bumpPersonRevisionsTx(ctx, tx, personID); err != nil {
		return err
	}
	return s.invalidatePersonEnrichmentIdentitiesAfterRevisionTx(ctx, tx, personID)
}

// linkedContactMatchCandidatesTx lists pending contact-match candidates
// whose archive identity already belongs to the contact profile, for
// example because the two were merged by hand. They are already true.
func (s *Store) linkedContactMatchCandidatesTx(
	ctx context.Context, tx *loggedTx,
) ([]ContactMatchAutoAction, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, left_id, right_id
		FROM identity_match_candidates
		WHERE source_ref = ? AND left_kind = ? AND right_kind = ? AND state = ?
		ORDER BY id`,
		ContactMatchSourceRef, IdentityMatchParticipant, IdentityMatchPerson,
		IdentityMatchStateCandidate)
	if err != nil {
		return nil, fmt.Errorf("load pending contact match candidates: %w", err)
	}
	pending := []ContactMatchAutoAction{}
	for rows.Next() {
		action := ContactMatchAutoAction{Action: ContactMatchAutoActionCloseLinked}
		if err := rows.Scan(&action.CandidateID, &action.ParticipantID, &action.ContactPersonID); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan pending contact match candidate: %w", err)
		}
		pending = append(pending, action)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close pending contact match candidates: %w", err)
	}
	if len(pending) == 0 {
		return pending, nil
	}
	edges, err := s.loadLinkEdgesTxContext(ctx, tx)
	if err != nil {
		return nil, err
	}
	linked := []ContactMatchAutoAction{}
	for _, action := range pending {
		members := sortedComponentMembers(action.ParticipantID, edges)
		persons, err := personIDsForParticipantsTx(ctx, tx, members)
		if err != nil {
			return nil, err
		}
		if classifyContactMatch(action.ContactPersonID, persons) == ContactMatchLinked {
			linked = append(linked, action)
		}
	}
	return linked, nil
}

// closeLinkedContactMatchCandidatesTx accepts the pending candidates that
// are already linked.
func (s *Store) closeLinkedContactMatchCandidatesTx(
	ctx context.Context, tx *loggedTx,
) ([]ContactMatchAutoAction, error) {
	if !s.contactMatchAutoResolveEnabled() {
		return nil, nil
	}
	linked, err := s.linkedContactMatchCandidatesTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, action := range linked {
		if _, err := tx.ExecContext(ctx, `UPDATE identity_match_candidates SET
			state = ?, decided_by = ?, decided_at = `+s.dialect.Now()+`, notes = ?,
			pre_conflict_state = NULL, application_pending = FALSE,
			updated_at = `+s.dialect.Now()+` WHERE id = ? AND state = ?`,
			IdentityMatchStateAccepted, ContactMatchAutoActor, contactMatchLinkedNote,
			action.CandidateID, IdentityMatchStateCandidate,
		); err != nil {
			return nil, fmt.Errorf("close linked contact match %d: %w", action.CandidateID, err)
		}
		if err := dropCandidateDecisionSnapshotTx(ctx, tx, action.CandidateID); err != nil {
			return nil, err
		}
	}
	return linked, nil
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

// errContactMatchDryRun rolls back a dry-run refresh.
var errContactMatchDryRun = errors.New("contact match dry run")

// dryRunContactMatchBuildContext runs the whole refresh (retirement,
// linked closing, candidate writes, planning, and every batch with its
// re-validation and merge guards) in one transaction that is rolled back, so
// it reports exactly what a refresh would do now. Candidates the refresh
// would create are reported with candidate ID zero.
func (s *Store) dryRunContactMatchBuildContext(ctx context.Context) (*ContactMatchBuildResult, error) {
	result := &ContactMatchBuildResult{}
	err := s.withTxContext(ctx, func(tx *loggedTx) error {
		*result = ContactMatchBuildResult{}
		plan, created, err := s.writeContactMatchCandidatesTx(ctx, tx, result)
		if err != nil {
			return err
		}
		for start := 0; start < len(plan); start += contactMatchAutoBatchSize {
			batch := plan[start:min(start+contactMatchAutoBatchSize, len(plan))]
			applied, err := s.applyContactMatchAutoBatchTx(ctx, tx, batch)
			if err != nil {
				return err
			}
			recordContactMatchAutoApplied(result, applied)
		}
		result.LeftForReview, err = pendingContactMatchCountTx(ctx, tx)
		if err != nil {
			return err
		}
		for i := range result.Actions {
			if _, isNew := created[result.Actions[i].CandidateID]; isNew {
				result.Actions[i].CandidateID = 0
			}
		}
		return errContactMatchDryRun
	})
	if !errors.Is(err, errContactMatchDryRun) {
		if err == nil {
			err = errors.New("contact match dry run was not rolled back")
		}
		return nil, err
	}
	result.DryRun = true
	return result, nil
}
