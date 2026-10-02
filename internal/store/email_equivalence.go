package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/emailaddr"
)

// Email address equivalence links participants whose addresses deliver to
// one mailbox (see internal/emailaddr for the rule).
//
// Every automatic link is an ordinary participant_links edge owned by an
// accepted identity match candidate with basis email_equivalence. The edge
// is visible with its reason on the person page, and the normal unlink path
// removes it. Unlinking rejects the candidate, and a rejected candidate is
// never applied again, so an undone link stays undone. Pairs that would join
// two different Directory people, and non-Gmail addresses that differ only by
// dots, become reviewable candidates instead of links.
//
// The pass never links relay and robot mailboxes (emailaddr.IsAutomatedMailbox)
// or identities classified as not a person, and never rejoins an identity the
// user detached from a person (detachmentBlockingJoinTx).
const (
	// IdentityMatchEmailEquivalence marks a pair of addresses that deliver to
	// the same mailbox: a plus tag on any domain, or Gmail dots and
	// googlemail.com. The equivalence pass accepts and links these itself.
	IdentityMatchEmailEquivalence IdentityMatchBasis = "email_equivalence"
	// IdentityMatchEmailDotVariant marks non-Gmail addresses that are equal
	// only after removing dots. They are suggestions for review only.
	IdentityMatchEmailDotVariant IdentityMatchBasis = "email_dot_variant"

	// EmailEquivalenceSourceRef is the source_ref of every candidate the
	// equivalence pass writes.
	EmailEquivalenceSourceRef = "email_address_equivalence"

	emailEquivalenceConflictNote = "these addresses deliver to the same mailbox but " +
		"belong to two different people; accept to merge the people"

	// emailEquivalenceWatermarkKey records the rule version and the highest
	// participant ID the last complete pass covered, as "version:id".
	emailEquivalenceWatermarkKey = "email_equivalence_watermark"
	// emailEquivalenceRuleVersion forces one full pass when the rule changes.
	emailEquivalenceRuleVersion = 1
	// emailEquivalenceBatchSize bounds how many pairs one write transaction
	// applies while it holds the identity mutation lock.
	emailEquivalenceBatchSize = 250
)

// EmailEquivalenceResult reports what one equivalence pass changed.
type EmailEquivalenceResult struct {
	// Skipped is true when no participant was added since the last
	// complete pass, so nothing was scanned.
	Skipped bool `json:"skipped"`
	// Participants is the number of email participants scanned.
	Participants int `json:"participants"`
	// Linked counts new participant links.
	Linked int `json:"linked"`
	// AlreadyLinked counts equivalences recorded for pairs that were
	// already in one identity, so no new link was needed.
	AlreadyLinked int `json:"already_linked"`
	// Suggested counts new dot-variant candidates waiting in Reviews.
	Suggested int `json:"suggested"`
	// Conflicts counts equivalent addresses already bound to two different
	// people. They wait in Reviews instead of merging the people.
	Conflicts int `json:"conflicts"`
	// Suppressed counts pairs left apart because the user unlinked or
	// rejected that equivalence, or classified an address as not a person.
	Suppressed int `json:"suppressed"`
	// CacheState is set by the HTTP endpoint after it refreshes the identity
	// datasets for new links: ready, or stale when the refresh failed. It is
	// omitted when the pass linked nothing.
	CacheState string `json:"cache_state,omitempty" enum:"ready,stale"`
}

type emailEquivalencePair struct {
	basis  IdentityMatchBasis
	lo, hi int64
	key    string
}

type emailEquivalenceCandidateKey struct {
	basis  IdentityMatchBasis
	lo, hi int64
	key    string
}

type emailEquivalenceCandidateState struct {
	id      int64
	state   IdentityMatchState
	pending bool
	byUser  bool
}

// emailEquivalenceDecisions indexes the pass's candidates three ways: by exact
// key, by participant pair for rejected email_equivalence rows (so a future
// change to the mailbox key never revives an old rejection), and as the list
// of pairs the user rejected, which no join may reconnect.
type emailEquivalenceDecisions struct {
	byKey        map[emailEquivalenceCandidateKey]emailEquivalenceCandidateState
	rejectedPair map[linkEdge]struct{}
	userRejected []linkEdge
}

func newEmailEquivalenceDecisions(
	candidates map[emailEquivalenceCandidateKey]emailEquivalenceCandidateState,
) emailEquivalenceDecisions {
	decisions := emailEquivalenceDecisions{
		byKey:        candidates,
		rejectedPair: make(map[linkEdge]struct{}),
	}
	for key, state := range candidates {
		if state.state != IdentityMatchStateRejected {
			continue
		}
		pair := linkEdge{a: key.lo, b: key.hi}
		if key.basis == IdentityMatchEmailEquivalence {
			decisions.rejectedPair[pair] = struct{}{}
		}
		if state.byUser {
			decisions.userRejected = append(decisions.userRejected, pair)
		}
	}
	return decisions
}

// mailboxPair is an unordered pair of mailbox keys.
type mailboxPair [2]string

func newMailboxPair(a, b string) mailboxPair {
	if a > b {
		a, b = b, a
	}
	return mailboxPair{a, b}
}

// loadDecidedDotVariantMailboxes returns the mailbox pairs that already have
// a dot-variant candidate in any state. A suggestion is about two mailboxes,
// not two participant IDs: the anchor that represents a mailbox can change
// (a promotion, a detachment), and the user's decision must survive that.
func loadDecidedDotVariantMailboxes(
	ctx context.Context, query func(context.Context, string, ...any) (rowsScanner, error),
) (map[mailboxPair]struct{}, error) {
	rows, err := query(ctx, `SELECT left_side.email_address, right_side.email_address
		FROM identity_match_candidates c
		JOIN participants left_side ON left_side.id = c.left_id
		JOIN participants right_side ON right_side.id = c.right_id
		WHERE c.basis = ? AND c.left_kind = ? AND c.right_kind = ?
		  AND left_side.email_address IS NOT NULL
		  AND right_side.email_address IS NOT NULL`,
		IdentityMatchEmailDotVariant, IdentityMatchParticipant, IdentityMatchParticipant)
	if err != nil {
		return nil, fmt.Errorf("load dot-variant decisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	decided := make(map[mailboxPair]struct{})
	for rows.Next() {
		var left, right string
		if err := rows.Scan(&left, &right); err != nil {
			return nil, fmt.Errorf("scan dot-variant decision: %w", err)
		}
		leftMailbox, okLeft := emailaddr.Mailbox(left)
		rightMailbox, okRight := emailaddr.Mailbox(right)
		if okLeft && okRight {
			decided[newMailboxPair(leftMailbox, rightMailbox)] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dot-variant decisions: %w", err)
	}
	return decided, nil
}

// dotVariantDecided reports whether the mailboxes of a dot-variant pair
// already have a suggestion, whichever participants it named.
func dotVariantDecided(
	pair emailEquivalencePair, addresses map[int64]string, decided map[mailboxPair]struct{},
) bool {
	lo, okLo := emailaddr.Mailbox(addresses[pair.lo])
	hi, okHi := emailaddr.Mailbox(addresses[pair.hi])
	if !okLo || !okHi {
		return false
	}
	_, found := decided[newMailboxPair(lo, hi)]
	return found
}

// settled reports whether a pair already has an outcome the pass must not
// revisit: a rejection under any key, a conflict, or an applied acceptance.
func (d emailEquivalenceDecisions) settled(pair emailEquivalencePair) bool {
	if pair.basis == IdentityMatchEmailEquivalence {
		if _, rejected := d.rejectedPair[linkEdge{a: pair.lo, b: pair.hi}]; rejected {
			return true
		}
	}
	existing, found := d.byKey[emailEquivalenceCandidateKey(pair)]
	if !found {
		return false
	}
	if pair.basis == IdentityMatchEmailDotVariant {
		return true
	}
	return existing.state == IdentityMatchStateRejected ||
		existing.state == IdentityMatchStateConflict ||
		(existing.state == IdentityMatchStateAccepted && !existing.pending)
}

// LinkEquivalentEmailAddressesContext applies the mailbox rule to the whole
// archive. It is idempotent and safe to repeat: pairs that already carry a
// decision are skipped without a write, and a pass that finds no new
// participants since the last complete pass returns Skipped unless force is
// set. Work is O(participants) plus one batched write transaction per
// emailEquivalenceBatchSize pairs that still need a decision.
func (s *Store) LinkEquivalentEmailAddressesContext(
	ctx context.Context, force bool,
) (*EmailEquivalenceResult, error) {
	result := &EmailEquivalenceResult{}
	var maxID int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(id), 0) FROM participants`).Scan(&maxID); err != nil {
		return nil, fmt.Errorf("read highest participant id: %w", err)
	}
	if !force {
		covered, err := s.emailEquivalenceWatermarkContext(ctx)
		if err != nil {
			return nil, err
		}
		if covered >= maxID {
			result.Skipped = true
			return result, nil
		}
	}

	ineligible, err := s.emailEquivalenceIneligibleAnchorsContext(ctx)
	if err != nil {
		return nil, err
	}
	pairs, addresses, scanned, err := s.planEmailEquivalencePairsContext(ctx, ineligible)
	if err != nil {
		return nil, err
	}
	result.Participants = scanned
	pairs, err = s.pendingEmailEquivalencePairsContext(ctx, pairs, addresses)
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(pairs); start += emailEquivalenceBatchSize {
		end := min(start+emailEquivalenceBatchSize, len(pairs))
		if err := s.applyEmailEquivalenceBatchContext(ctx, pairs[start:end], result); err != nil {
			return result, err
		}
	}
	if err := s.withTxContext(ctx, func(tx *loggedTx) error {
		return setArchiveMetadataValueTx(ctx, tx, emailEquivalenceWatermarkKey,
			strconv.Itoa(emailEquivalenceRuleVersion)+":"+strconv.FormatInt(maxID, 10))
	}); err != nil {
		return result, err
	}
	return result, nil
}

// linkEquivalentEmailAddressesAfterSync runs the watermark-gated pass after
// a successful import so new participants join their mailbox's identity. A
// failure is logged, never returned: the import itself succeeded and the
// next completed import or an explicit run retries the pass.
func (s *Store) linkEquivalentEmailAddressesAfterSync(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	result, err := s.LinkEquivalentEmailAddressesContext(ctx, false)
	if err != nil {
		slog.WarnContext(ctx, "link equivalent email addresses after sync", "error", err)
		return
	}
	if result.Linked+result.Suggested+result.Conflicts > 0 {
		slog.InfoContext(ctx, "linked equivalent email addresses",
			"linked", result.Linked, "suggested", result.Suggested,
			"conflicts", result.Conflicts)
	}
}

// emailEquivalenceWatermarkContext returns the highest participant ID the
// last complete pass under the current rule version covered, or 0.
func (s *Store) emailEquivalenceWatermarkContext(ctx context.Context) (int64, error) {
	value, err := s.archiveMetadataValueContext(ctx, emailEquivalenceWatermarkKey)
	if err != nil || value == "" {
		return 0, err
	}
	version, covered, found := strings.Cut(value, ":")
	if !found || version != strconv.Itoa(emailEquivalenceRuleVersion) {
		return 0, nil
	}
	// An unreadable watermark only costs one full rescan.
	if id, parseErr := strconv.ParseInt(covered, 10, 64); parseErr == nil {
		return id, nil
	}
	return 0, nil
}

// emailEquivalenceIneligibleAnchorsContext returns participants that cannot
// anchor their mailbox group: identities classified as not a person (by the
// user, a rule, or Jev) and identities under an active detachment. Their own
// pairs are still planned, so the batch records or skips them as before.
func (s *Store) emailEquivalenceIneligibleAnchorsContext(
	ctx context.Context,
) (map[int64]struct{}, error) {
	ineligible := make(map[int64]struct{})
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		classified, err := s.anyNotPersonClassificationTx(ctx, tx)
		if err != nil {
			return err
		}
		if classified {
			hidden, err := s.hiddenCorrespondentParticipantsTx(ctx, tx)
			if err != nil {
				return err
			}
			for id := range hidden {
				ineligible[id] = struct{}{}
			}
		}
		rows, err := tx.QueryContext(ctx, `SELECT member.participant_id
			FROM person_participant_detachment_members member
			WHERE EXISTS (SELECT 1 FROM person_participant_detachments detachment
			              WHERE detachment.id = member.detachment_id
			                AND detachment.reattached_at IS NULL)`)
		if err != nil {
			return fmt.Errorf("load detached participants: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("scan detached participant: %w", err)
			}
			ineligible[id] = struct{}{}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return ineligible, nil
}

// planEmailEquivalencePairsContext scans every email participant once and
// groups them by mailbox. Each automatic pair joins a member to its group's
// anchor, the lowest participant ID that is not in ineligible (or the lowest
// ID when none is eligible), so a group of n addresses needs n-1 pairs and a
// skipped low ID never strands the rest. Dot-variant suggestions join the
// anchors of each mailbox that shares a dot-insensitive key.
func (s *Store) planEmailEquivalencePairsContext(
	ctx context.Context, ineligible map[int64]struct{},
) ([]emailEquivalencePair, map[int64]string, int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, email_address FROM participants
		WHERE email_address IS NOT NULL AND email_address <> ''
		ORDER BY id`)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("scan email participants: %w", err)
	}
	defer func() { _ = rows.Close() }()
	mailboxes := make(map[string][]int64)
	addresses := make(map[int64]string)
	mailboxOrder := make([]string, 0)
	dotless := make(map[string][]string)
	dotlessOrder := make([]string, 0)
	scanned := 0
	for rows.Next() {
		var id int64
		var address string
		if err := rows.Scan(&id, &address); err != nil {
			return nil, nil, 0, fmt.Errorf("scan email participant: %w", err)
		}
		scanned++
		mailbox, ok := emailaddr.Mailbox(address)
		// Relay and robot mailboxes (reply+<thread>@, bounces+<id>@) give
		// every message its own tag; their tags are not one person's aliases.
		if !ok || emailaddr.IsAutomatedMailbox(address) {
			continue
		}
		if _, seen := mailboxes[mailbox]; !seen {
			mailboxOrder = append(mailboxOrder, mailbox)
			key, _ := emailaddr.DotInsensitiveMailbox(address)
			if _, seenKey := dotless[key]; !seenKey {
				dotlessOrder = append(dotlessOrder, key)
			}
			dotless[key] = append(dotless[key], mailbox)
		}
		mailboxes[mailbox] = append(mailboxes[mailbox], id)
		addresses[id] = address
	}
	if err := rows.Err(); err != nil {
		return nil, nil, 0, fmt.Errorf("iterate email participants: %w", err)
	}

	anchors := make(map[string]int64, len(mailboxOrder))
	for _, mailbox := range mailboxOrder {
		ids := mailboxes[mailbox]
		anchor := ids[0]
		for _, id := range ids {
			if _, skip := ineligible[id]; !skip {
				anchor = id
				break
			}
		}
		anchors[mailbox] = anchor
	}
	pairs := make([]emailEquivalencePair, 0)
	for _, mailbox := range mailboxOrder {
		anchor := anchors[mailbox]
		for _, member := range mailboxes[mailbox] {
			if member == anchor {
				continue
			}
			lo, hi := normalizeEdge(anchor, member)
			pairs = append(pairs, emailEquivalencePair{
				basis: IdentityMatchEmailEquivalence, lo: lo, hi: hi, key: mailbox,
			})
		}
	}
	for _, key := range dotlessOrder {
		variants := dotless[key]
		if len(variants) < 2 {
			continue
		}
		first := anchors[variants[0]]
		for _, variant := range variants[1:] {
			lo, hi := normalizeEdge(first, anchors[variant])
			pairs = append(pairs, emailEquivalencePair{
				basis: IdentityMatchEmailDotVariant, lo: lo, hi: hi, key: key,
			})
		}
	}
	return pairs, addresses, scanned, nil
}

// loadEmailEquivalenceCandidates reads every candidate the pass may have
// written, keyed by pair. Their number is bounded by the planned pairs.
func loadEmailEquivalenceCandidates(
	ctx context.Context, query func(context.Context, string, ...any) (rowsScanner, error),
) (map[emailEquivalenceCandidateKey]emailEquivalenceCandidateState, error) {
	rows, err := query(ctx, `SELECT id, basis, left_id, right_id,
		COALESCE(normalized_value, ''), state, application_pending,
		COALESCE(decided_by, '') = 'user'
		FROM identity_match_candidates
		WHERE basis IN (?, ?) AND left_kind = ? AND right_kind = ?`,
		IdentityMatchEmailEquivalence, IdentityMatchEmailDotVariant,
		IdentityMatchParticipant, IdentityMatchParticipant)
	if err != nil {
		return nil, fmt.Errorf("load email equivalence candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	candidates := make(map[emailEquivalenceCandidateKey]emailEquivalenceCandidateState)
	for rows.Next() {
		var key emailEquivalenceCandidateKey
		var state emailEquivalenceCandidateState
		if err := rows.Scan(&state.id, &key.basis, &key.lo, &key.hi, &key.key,
			&state.state, &state.pending, &state.byUser); err != nil {
			return nil, fmt.Errorf("scan email equivalence candidate: %w", err)
		}
		candidates[key] = state
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate email equivalence candidates: %w", err)
	}
	return candidates, nil
}

// pendingEmailEquivalencePairsContext drops pairs that already carry a final
// outcome, using an unlocked snapshot. Each remaining pair is checked again
// under the identity lock before it is written.
func (s *Store) pendingEmailEquivalencePairsContext(
	ctx context.Context, pairs []emailEquivalencePair, addresses map[int64]string,
) ([]emailEquivalencePair, error) {
	if len(pairs) == 0 {
		return pairs, nil
	}
	query := func(ctx context.Context, query string, args ...any) (rowsScanner, error) {
		return s.db.QueryContext(ctx, query, args...)
	}
	candidates, err := loadEmailEquivalenceCandidates(ctx, query)
	if err != nil {
		return nil, err
	}
	dotDecided, err := loadDecidedDotVariantMailboxes(ctx, query)
	if err != nil {
		return nil, err
	}
	edges, err := s.loadLinkEdgesContext(ctx)
	if err != nil {
		return nil, err
	}
	clusters := clustersFromEdges(edges)
	clusterOf := func(id int64) int64 {
		if root, ok := clusters[id]; ok {
			return root
		}
		return id
	}
	decisions := newEmailEquivalenceDecisions(candidates)
	pending := make([]emailEquivalencePair, 0, len(pairs))
	for _, pair := range pairs {
		if decisions.settled(pair) {
			continue
		}
		if pair.basis == IdentityMatchEmailDotVariant &&
			(clusterOf(pair.lo) == clusterOf(pair.hi) ||
				dotVariantDecided(pair, addresses, dotDecided)) {
			continue
		}
		pending = append(pending, pair)
	}
	return pending, nil
}

// linkForest is a union-find view of the participant link graph for one
// write transaction. It tracks each identity's members and Directory person
// so a batch decides every pair against the links made earlier in the same
// batch, without rebuilding the graph per pair.
type linkForest struct {
	parent  map[int64]int64
	members map[int64][]int64
	person  map[int64]int64
}

// ambiguousPerson marks an identity whose members are bound to more than one
// person, which link and merge never produce. The pass treats it as a
// conflict rather than choosing a person.
const ambiguousPerson int64 = -1

func newLinkForest(edges []linkEdge) *linkForest {
	forest := &linkForest{
		parent:  make(map[int64]int64, 2*len(edges)),
		members: make(map[int64][]int64),
		person:  make(map[int64]int64),
	}
	for _, edge := range edges {
		forest.union(edge.a, edge.b)
	}
	return forest
}

func (f *linkForest) find(id int64) int64 {
	parent, ok := f.parent[id]
	if !ok {
		f.parent[id] = id
		f.members[id] = []int64{id}
		return id
	}
	if parent == id {
		return id
	}
	root := f.find(parent)
	f.parent[id] = root
	return root
}

func (f *linkForest) bind(participantID, personID int64) {
	root := f.find(participantID)
	switch current := f.person[root]; current {
	case 0:
		f.person[root] = personID
	case personID:
	default:
		f.person[root] = ambiguousPerson
	}
}

// union joins two identities, keeping the larger one's root so member lists
// are copied O(n log n) times in total.
func (f *linkForest) union(a, b int64) {
	rootA, rootB := f.find(a), f.find(b)
	if rootA == rootB {
		return
	}
	if len(f.members[rootA]) < len(f.members[rootB]) {
		rootA, rootB = rootB, rootA
	}
	f.parent[rootB] = rootA
	f.members[rootA] = append(f.members[rootA], f.members[rootB]...)
	delete(f.members, rootB)
	personA, personB := f.person[rootA], f.person[rootB]
	delete(f.person, rootB)
	switch {
	case personA == 0:
		if personB != 0 {
			f.person[rootA] = personB
		}
	case personB != 0 && personB != personA:
		f.person[rootA] = ambiguousPerson
	}
}

// applyEmailEquivalenceBatchContext decides one batch of pairs in a single
// transaction under the identity mutation lock. It reads the link graph,
// person bindings, existing decisions, and not-a-person classifications
// once, then writes candidates, edges, and person bindings for each pair.
func (s *Store) applyEmailEquivalenceBatchContext(
	ctx context.Context, pairs []emailEquivalencePair, result *EmailEquivalenceResult,
) error {
	// Count into a scratch result and publish it only after the commit, so a
	// rolled-back batch does not report work that did not happen.
	var batch EmailEquivalenceResult
	err := s.withTxContext(ctx, func(tx *loggedTx) error {
		batch = EmailEquivalenceResult{}
		if err := s.lockIdentityMutationTxContext(ctx, tx); err != nil {
			return err
		}
		addresses, err := emailEquivalenceAddressesTx(ctx, tx, pairs)
		if err != nil {
			return err
		}
		edges, err := s.loadLinkEdgesTxContext(ctx, tx)
		if err != nil {
			return err
		}
		forest := newLinkForest(edges)
		if err := loadPersonBindingsIntoForestTx(ctx, tx, forest); err != nil {
			return err
		}
		query := func(ctx context.Context, query string, args ...any) (rowsScanner, error) {
			return tx.QueryContext(ctx, query, args...)
		}
		candidates, err := loadEmailEquivalenceCandidates(ctx, query)
		if err != nil {
			return err
		}
		decisions := newEmailEquivalenceDecisions(candidates)
		dotDecided, err := loadDecidedDotVariantMailboxes(ctx, query)
		if err != nil {
			return err
		}
		// userHidden holds identities the user marked as not a person; their
		// pairs are recorded rejected and restored when the mark is cleared.
		// derivedHidden adds rule and Jev classifications (an automated
		// sender, a mailing list), whose pairs are skipped without a record
		// so a changed classification is picked up by the next pass.
		userHidden := map[int64]correspondentKindRow{}
		derivedHidden := map[int64]correspondentKindRow{}
		classified, err := s.anyNotPersonClassificationTx(ctx, tx)
		if err != nil {
			return err
		}
		if classified {
			if userHidden, err = s.userHiddenCorrespondentParticipantsTx(ctx, tx); err != nil {
				return err
			}
			if derivedHidden, err = s.hiddenCorrespondentParticipantsTx(ctx, tx); err != nil {
				return err
			}
		}
		// A user detachment must never be undone by this pass. Without an
		// active detachment there is nothing to protect, so the per-pair
		// checks below are skipped.
		detachmentActive, err := hasActivePersonDetachmentTx(ctx, tx)
		if err != nil {
			return err
		}
		// rejectForDetachment records a pair the detachment guard refused as
		// rejected for that detachment, journaled so undoing the detachment
		// restores the decision the pair had (or an open candidate).
		rejectForDetachment := func(
			pair emailEquivalencePair, existing emailEquivalenceCandidateState,
			found bool, detachmentID int64,
		) error {
			candidateID := existing.id
			if !found {
				var err error
				if candidateID, err = s.insertEmailEquivalenceCandidateTx(
					ctx, tx, pair, IdentityMatchStateCandidate, "", addresses,
				); err != nil {
					return err
				}
			}
			return s.rejectCandidateForDetachmentTx(
				ctx, tx, detachmentID, candidateID, string(ProvenanceSystem))
		}

		linkedPeople := make(map[int64]struct{})
		reboundPeople := make(map[int64]struct{})
		for _, pair := range pairs {
			if !emailEquivalencePairStillHolds(pair, addresses) {
				continue
			}
			existing, found := candidates[emailEquivalenceCandidateKey(pair)]
			_, loHidden := userHidden[pair.lo]
			_, hiHidden := userHidden[pair.hi]
			_, loDerived := derivedHidden[pair.lo]
			_, hiDerived := derivedHidden[pair.hi]
			rootLo, rootHi := forest.find(pair.lo), forest.find(pair.hi)
			personLo, personHi := forest.person[rootLo], forest.person[rootHi]

			if pair.basis == IdentityMatchEmailDotVariant {
				if found || rootLo == rootHi || (personLo != 0 && personLo == personHi) ||
					dotVariantDecided(pair, addresses, dotDecided) {
					continue
				}
				if !loHidden && !hiHidden && (loDerived || hiDerived) {
					batch.Suppressed++
					continue
				}
				if detachmentActive && !loHidden && !hiHidden {
					detachmentID, separated, err := s.activePersonDetachmentSeparatingTx(
						ctx, tx, IdentityMatchParticipant, pair.lo, IdentityMatchParticipant, pair.hi)
					if err != nil {
						return err
					}
					if separated {
						if err := rejectForDetachment(pair, existing, found, detachmentID); err != nil {
							return err
						}
						batch.Suppressed++
						continue
					}
				}
				state, notes := IdentityMatchStateCandidate, ""
				if loHidden || hiHidden {
					state, notes = IdentityMatchStateRejected, correspondentkind.NotAPersonReason
				}
				if _, err := s.insertEmailEquivalenceCandidateTx(
					ctx, tx, pair, state, notes, addresses,
				); err != nil {
					return err
				}
				if state == IdentityMatchStateRejected {
					batch.Suppressed++
				} else {
					batch.Suggested++
				}
				continue
			}

			if decisions.settled(pair) {
				continue
			}
			if rootLo != rootHi &&
				emailEquivalenceSuppressed(forest, rootLo, rootHi, decisions.userRejected) {
				batch.Suppressed++
				continue
			}
			if loHidden || hiHidden {
				if !found {
					if _, err := s.insertEmailEquivalenceCandidateTx(ctx, tx, pair,
						IdentityMatchStateRejected, correspondentkind.NotAPersonReason, addresses,
					); err != nil {
						return err
					}
				}
				batch.Suppressed++
				continue
			}
			if loDerived || hiDerived {
				batch.Suppressed++
				continue
			}
			// Joining two identities bound to different people, or to an identity
			// whose people are already ambiguous, needs a person merge decision.
			conflict := rootLo != rootHi && (personLo == ambiguousPerson ||
				personHi == ambiguousPerson ||
				(personLo != 0 && personHi != 0 && personLo != personHi))
			if detachmentActive && rootLo != rootHi {
				var detachmentID int64
				var blocked bool
				if conflict {
					// A conflict only asks the user; it is withheld, like any
					// generated suggestion, when it names a detached pair.
					detachmentID, blocked, err = s.activePersonDetachmentSeparatingTx(
						ctx, tx, IdentityMatchParticipant, pair.lo, IdentityMatchParticipant, pair.hi)
				} else {
					// A link is refused when the joined identity would hold a
					// detached participant and one still bound to its person,
					// including through a third participant.
					detachmentID, blocked, err = detachmentBlockingJoinTx(
						ctx, tx, pair.lo, pair.hi, edges)
				}
				if err != nil {
					return err
				}
				if blocked {
					if err := rejectForDetachment(pair, existing, found, detachmentID); err != nil {
						return err
					}
					batch.Suppressed++
					continue
				}
			}
			outcome := IdentityMatchStateAccepted
			if conflict {
				outcome = IdentityMatchStateConflict
			}
			candidateID := existing.id
			switch {
			case !found:
				notes := ""
				if conflict {
					notes = emailEquivalenceConflictNote
				}
				if candidateID, err = s.insertEmailEquivalenceCandidateTx(
					ctx, tx, pair, outcome, notes, addresses,
				); err != nil {
					return err
				}
			case existing.state != IdentityMatchStateAccepted || conflict:
				if err := s.decideEmailEquivalenceCandidateTx(
					ctx, tx, candidateID, outcome,
				); err != nil {
					return err
				}
			}
			if conflict {
				batch.Conflicts++
				continue
			}
			if existing.pending {
				if _, err := tx.ExecContext(ctx, `UPDATE identity_match_candidates
					SET application_pending = FALSE WHERE id = ?`, candidateID); err != nil {
					return fmt.Errorf("complete email equivalence application: %w", err)
				}
			}
			if rootLo == rootHi {
				batch.AlreadyLinked++
				continue
			}

			person := personLo
			unbound := forest.members[rootLo]
			if person == 0 {
				person = personHi
			} else {
				unbound = forest.members[rootHi]
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO participant_links
				(participant_a, participant_b, identity_match_candidate_id)
				VALUES (?, ?, ?)`, pair.lo, pair.hi, candidateID); err != nil {
				return fmt.Errorf("insert email equivalence link: %w", err)
			}
			if person != 0 {
				if err := extendActivePersonMergeLineageTx(
					ctx, tx, person, pair.lo, pair.hi, edges,
				); err != nil {
					return err
				}
				changed, err := s.bindPersonParticipantsTx(ctx, tx, person, slices.Clone(unbound))
				if err != nil {
					return err
				}
				linkedPeople[person] = struct{}{}
				if changed {
					reboundPeople[person] = struct{}{}
				}
			}
			edges = append(edges, linkEdge{a: pair.lo, b: pair.hi})
			forest.union(pair.lo, pair.hi)
			batch.Linked++
		}

		if batch.Linked == 0 {
			return nil
		}
		if len(reboundPeople) > 0 {
			people := sortedPersonSet(reboundPeople)
			if err := s.bumpPersonRevisionsTx(ctx, tx, people...); err != nil {
				return err
			}
			if err := s.invalidatePersonEnrichmentIdentitiesAfterRevisionTx(
				ctx, tx, people...,
			); err != nil {
				return err
			}
		}
		if _, err := s.bumpIdentityRevisionContext(ctx, tx); err != nil {
			return err
		}
		if len(linkedPeople) == 0 {
			return nil
		}
		return s.publishPersonIdentityScopeChangesTx(ctx, tx, sortedPersonSet(linkedPeople))
	})
	if err != nil {
		return fmt.Errorf("apply email address equivalence: %w", err)
	}
	result.Linked += batch.Linked
	result.AlreadyLinked += batch.AlreadyLinked
	result.Suggested += batch.Suggested
	result.Conflicts += batch.Conflicts
	result.Suppressed += batch.Suppressed
	return nil
}

func sortedPersonSet(set map[int64]struct{}) []int64 {
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// emailEquivalenceSuppressed reports whether linking the two identities would
// put both addresses of a pair the user rejected or unlinked into one
// identity, whatever mailbox either pair belongs to. An unlink records such a
// pair across its cut, so no later pair on any mailbox bridges the two sides
// again.
func emailEquivalenceSuppressed(
	forest *linkForest, rootLo, rootHi int64, rejected []linkEdge,
) bool {
	for _, pair := range rejected {
		a, b := forest.find(pair.a), forest.find(pair.b)
		if (a == rootLo && b == rootHi) || (a == rootHi && b == rootLo) {
			return true
		}
	}
	return false
}

// emailEquivalenceAddressesTx rereads the batch's participant addresses
// under the identity lock. A participant merged away since the plan was made
// is absent, and its pairs are skipped.
func emailEquivalenceAddressesTx(
	ctx context.Context, tx *loggedTx, pairs []emailEquivalencePair,
) (map[int64]string, error) {
	ids := make([]int64, 0, 2*len(pairs))
	for _, pair := range pairs {
		ids = append(ids, pair.lo, pair.hi)
	}
	placeholders, args := sortedIDPlaceholders(ids)
	addresses := make(map[int64]string, len(args))
	if placeholders == "" {
		return addresses, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, email_address FROM participants
		WHERE email_address IS NOT NULL AND id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("load email equivalence addresses: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var address string
		if err := rows.Scan(&id, &address); err != nil {
			return nil, fmt.Errorf("scan email equivalence address: %w", err)
		}
		addresses[id] = address
	}
	return addresses, rows.Err()
}

// emailEquivalencePairStillHolds verifies the rule against the addresses read
// under the lock, so a pair is only written for what the store holds now.
func emailEquivalencePairStillHolds(pair emailEquivalencePair, addresses map[int64]string) bool {
	lo, okLo := addresses[pair.lo]
	hi, okHi := addresses[pair.hi]
	if !okLo || !okHi {
		return false
	}
	relation := emailaddr.Compare(lo, hi)
	switch pair.basis {
	case IdentityMatchEmailEquivalence:
		mailbox, _ := emailaddr.Mailbox(lo)
		return relation == emailaddr.SameMailbox && mailbox == pair.key
	case IdentityMatchEmailDotVariant:
		key, _ := emailaddr.DotInsensitiveMailbox(lo)
		return relation == emailaddr.DotVariant && key == pair.key
	default:
		return false
	}
}

// loadPersonBindingsIntoForestTx records each identity's Directory person.
// Bindings are read in one pass; their number is bounded by participants.
func loadPersonBindingsIntoForestTx(ctx context.Context, tx *loggedTx, forest *linkForest) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT participant_id, person_id FROM person_participants`)
	if err != nil {
		return fmt.Errorf("load person bindings for email equivalence: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var participantID, personID int64
		if err := rows.Scan(&participantID, &personID); err != nil {
			return fmt.Errorf("scan person binding for email equivalence: %w", err)
		}
		forest.bind(participantID, personID)
	}
	return rows.Err()
}

// insertEmailEquivalenceCandidateTx writes a new candidate for pair in its
// decided state, with one evidence row explaining the rule. A pair touching
// an identity classified as not a person is recorded rejected with the same
// restorable snapshot generated suggestions use, so clearing the
// classification puts it back up for review.
func (s *Store) insertEmailEquivalenceCandidateTx(
	ctx context.Context,
	tx *loggedTx,
	pair emailEquivalencePair,
	state IdentityMatchState,
	notes string,
	addresses map[int64]string,
) (int64, error) {
	if err := s.lockProfileIdentityKeyTxContext(
		ctx, tx, "identity-match-candidate",
		IdentityMatchParticipant, pair.lo, IdentityMatchParticipant, pair.hi,
		pair.basis, nil, nil, nil, pair.key,
	); err != nil {
		return 0, err
	}
	suppressed := notes == correspondentkind.NotAPersonReason
	decidedBy, decidedAt := any(nil), any(nil)
	if state != IdentityMatchStateCandidate {
		decidedBy, decidedAt = string(ProvenanceSystem), time.Now().UTC()
	}
	pending := state == IdentityMatchStateCandidate
	notesValue := any(nil)
	if notes != "" {
		notesValue = notes
	}
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO identity_match_candidates (
		left_kind, left_id, right_kind, right_id, basis, normalized_value, state,
		source, source_ref, notes, decided_by, decided_at, application_pending,
		created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, `+s.dialect.Now()+`, `+s.dialect.Now()+`)
	RETURNING id`,
		IdentityMatchParticipant, pair.lo, IdentityMatchParticipant, pair.hi,
		pair.basis, pair.key, state, ProvenanceSystem, EmailEquivalenceSourceRef,
		notesValue, decidedBy, decidedAt, pending,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert email equivalence candidate: %w", err)
	}
	if suppressed {
		priorState := IdentityMatchStateCandidate
		if _, err := tx.ExecContext(ctx, `INSERT INTO correspondent_kind_candidate_snapshots (
				candidate_id, prior_state, prior_notes, prior_application_pending)
			VALUES (?, ?, NULL, TRUE)`, id, priorState); err != nil {
			return 0, fmt.Errorf("snapshot suppressed email equivalence candidate: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO identity_match_evidence (
		candidate_id, evidence_kind, detail, source) VALUES (?, ?, ?, ?)`,
		id, string(pair.basis), emailEquivalenceEvidence(pair, addresses), ProvenanceSystem,
	); err != nil {
		return 0, fmt.Errorf("add email equivalence evidence: %w", err)
	}
	return id, nil
}

// decideEmailEquivalenceCandidateTx moves an undecided or still-pending
// candidate to the pass's outcome, mirroring a system decision.
func (s *Store) decideEmailEquivalenceCandidateTx(
	ctx context.Context, tx *loggedTx, candidateID int64, state IdentityMatchState,
) error {
	notes := any(nil)
	if state == IdentityMatchStateConflict {
		notes = emailEquivalenceConflictNote
	}
	if _, err := tx.ExecContext(ctx, `UPDATE identity_match_candidates SET
		state = ?, decided_by = ?, decided_at = `+s.dialect.Now()+`,
		notes = COALESCE(?, notes), pre_conflict_state = NULL,
		application_pending = FALSE, updated_at = `+s.dialect.Now()+`
		WHERE id = ?`,
		state, string(ProvenanceSystem), notes, candidateID,
	); err != nil {
		return fmt.Errorf("decide email equivalence candidate: %w", err)
	}
	return dropCandidateDecisionSnapshotTx(ctx, tx, candidateID)
}

// emailEquivalenceEvidence explains in plain words why the pair matched.
func emailEquivalenceEvidence(pair emailEquivalencePair, addresses map[int64]string) string {
	lo, hi := addresses[pair.lo], addresses[pair.hi]
	if pair.basis == IdentityMatchEmailDotVariant {
		return fmt.Sprintf("%s and %s differ only by dots. Outside Gmail that can be "+
			"a different mailbox, so they are not linked automatically.", lo, hi)
	}
	at := strings.LastIndexByte(pair.key, '@')
	if at > 0 && emailaddr.IsGmailDomain(pair.key[at+1:]) {
		return fmt.Sprintf("%s and %s deliver to the same Gmail mailbox, %s: Gmail "+
			"ignores dots and anything after a plus sign.", lo, hi, pair.key)
	}
	return fmt.Sprintf("%s and %s deliver to the same mailbox, %s: anything after "+
		"a plus sign is a tag.", lo, hi, pair.key)
}

// emailEquivalenceUnlinkNote is the decision note on the rejection an unlink
// records for addresses of one mailbox.
const emailEquivalenceUnlinkNote = "unlinked by the user"

// rememberSameMailboxSplitTx records a user's unlink as a rejected
// email_equivalence candidate when it separates two addresses of one
// mailbox, so the equivalence pass never links them again. a and b are the
// endpoints of the removed edge; the caller holds the identity mutation lock
// and has already deleted the edge.
//
// One rejected pair across the cut is enough: the pass refuses any join that
// would put both addresses of a user-rejected pair into one identity. The
// endpoints are tried first; otherwise the first same-mailbox pair across
// the two components, in participant ID order, is recorded. Unlinks made
// before this record existed cannot be recovered.
func (s *Store) rememberSameMailboxSplitTx(ctx context.Context, tx *loggedTx, a, b int64) error {
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
	ids := make([]int64, 0, len(left)+len(right))
	for id := range left {
		ids = append(ids, id)
	}
	for id := range right {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	// A participant's own address and its email identifiers both name
	// mailboxes the unlink separated.
	addresses := make(map[int64][]string, len(ids))
	for _, query := range []string{
		`SELECT id, email_address FROM participants
		 WHERE email_address IS NOT NULL AND id IN (%s)`,
		`SELECT participant_id, identifier_value FROM participant_identifiers
		 WHERE identifier_type = 'email' AND participant_id IN (%s)`,
	} {
		if err := queryInChunksContext(ctx, tx, ids, nil, query, func(rows *loggedRows) error {
			var id int64
			var address string
			if err := rows.Scan(&id, &address); err != nil {
				return fmt.Errorf("scan unlinked address: %w", err)
			}
			addresses[id] = append(addresses[id], address)
			return nil
		}); err != nil {
			return fmt.Errorf("load unlinked addresses: %w", err)
		}
	}
	pairFor := func(x, y int64) (emailEquivalencePair, bool) {
		for _, left := range addresses[x] {
			for _, right := range addresses[y] {
				if emailaddr.Compare(left, right) != emailaddr.SameMailbox {
					continue
				}
				key, _ := emailaddr.Mailbox(left)
				lo, hi := normalizeEdge(x, y)
				return emailEquivalencePair{basis: IdentityMatchEmailEquivalence, lo: lo, hi: hi, key: key}, true
			}
		}
		return emailEquivalencePair{}, false
	}
	pair, found := pairFor(a, b)
	if !found {
		leftByMailbox := make(map[string]int64)
		for _, id := range ids {
			if _, inLeft := left[id]; !inLeft {
				continue
			}
			for _, address := range addresses[id] {
				if key, ok := emailaddr.Mailbox(address); ok {
					if _, seen := leftByMailbox[key]; !seen {
						leftByMailbox[key] = id
					}
				}
			}
		}
	search:
		for _, id := range ids {
			if _, inRight := right[id]; !inRight {
				continue
			}
			for _, address := range addresses[id] {
				key, ok := emailaddr.Mailbox(address)
				if !ok {
					continue
				}
				if partner, shared := leftByMailbox[key]; shared {
					if pair, found = pairFor(partner, id); found {
						break search
					}
				}
			}
		}
	}
	if !found {
		return nil
	}
	var candidateID int64
	var state IdentityMatchState
	err = tx.QueryRowContext(ctx, `SELECT id, state FROM identity_match_candidates
		WHERE left_kind = ? AND left_id = ? AND right_kind = ? AND right_id = ?
		  AND basis = ?
		ORDER BY CASE WHEN state = ? THEN 0 ELSE 1 END, id LIMIT 1`,
		IdentityMatchParticipant, pair.lo, IdentityMatchParticipant, pair.hi,
		IdentityMatchEmailEquivalence, IdentityMatchStateRejected,
	).Scan(&candidateID, &state)
	switch {
	case err == nil && state == IdentityMatchStateRejected:
		return nil
	case err == nil:
		if _, err := tx.ExecContext(ctx, `UPDATE identity_match_candidates SET
			state = ?, decided_by = ?, decided_at = `+s.dialect.Now()+`, notes = ?,
			pre_conflict_state = NULL, application_pending = FALSE,
			updated_at = `+s.dialect.Now()+` WHERE id = ?`,
			IdentityMatchStateRejected, string(ProvenanceUser), emailEquivalenceUnlinkNote,
			candidateID); err != nil {
			return fmt.Errorf("record unlinked mailbox pair: %w", err)
		}
		return dropCandidateDecisionSnapshotTx(ctx, tx, candidateID)
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("find unlinked mailbox pair: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO identity_match_candidates (
		left_kind, left_id, right_kind, right_id, basis, normalized_value, state,
		source, source_ref, notes, decided_by, decided_at, application_pending,
		created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, `+s.dialect.Now()+`, FALSE,
		`+s.dialect.Now()+`, `+s.dialect.Now()+`)`,
		IdentityMatchParticipant, pair.lo, IdentityMatchParticipant, pair.hi,
		IdentityMatchEmailEquivalence, pair.key, IdentityMatchStateRejected,
		ProvenanceUser, EmailEquivalenceSourceRef, emailEquivalenceUnlinkNote,
		string(ProvenanceUser),
	); err != nil {
		return fmt.Errorf("record unlinked mailbox pair: %w", err)
	}
	return nil
}
