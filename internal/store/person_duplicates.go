package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"go.kenn.io/msgvault/internal/correspondentkind"
)

// PersonDuplicateSourceRef marks identity match candidates proposed as
// possible duplicate people (the person_duplicates Jev judgment).
const PersonDuplicateSourceRef = "person_duplicate"

// PersonDuplicateEvidenceKind is the evidence kind of a duplicate-person
// candidate's signals and judgment.
const PersonDuplicateEvidenceKind = "person_duplicate_signal"

// PersonDuplicateSignal is why code proposed two identity clusters as one
// person.
type PersonDuplicateSignal string

const (
	// PersonDuplicateSameName: both clusters use the same display name (at
	// least two words, compared case-, punctuation-, and order-insensitively)
	// on different addresses.
	PersonDuplicateSameName PersonDuplicateSignal = "same_display_name"
	// PersonDuplicateSameLocalPart: both clusters have an address with the
	// same distinctive local part at different domains.
	PersonDuplicateSameLocalPart PersonDuplicateSignal = "same_local_part"
)

// Proposal bounds. A name or local part shared by more clusters than
// maxDuplicateGroupClusters is too common to mean one person, so it proposes
// nothing.
const (
	maxDuplicateGroupClusters = 5
	minDuplicateLocalPartLen  = 5
	maxDuplicateNames         = 3
	maxDuplicateAddresses     = 5
)

// duplicateNameStopwords are words that mark a display name as a team,
// service, or relay rather than one person's name.
var duplicateNameStopwords = map[string]struct{}{
	"team": {}, "support": {}, "service": {}, "services": {}, "notifications": {},
	"notification": {}, "noreply": {}, "info": {}, "admin": {}, "sales": {},
	"billing": {}, "help": {}, "via": {}, "newsletter": {}, "news": {},
	"updates": {}, "marketing": {}, "customer": {}, "account": {}, "accounts": {},
	"office": {}, "hr": {}, "careers": {}, "jobs": {}, "calendar": {},
}

// PersonDuplicateIdentity is one side of a proposed pair: the identity
// cluster's representative (lowest) participant, the person it is bound to
// if any, and its display names and email addresses. Phone numbers are
// never included.
type PersonDuplicateIdentity struct {
	ParticipantID int64
	PersonID      *int64
	Names         []string
	Addresses     []string
}

// PersonDuplicateProposal is two identity clusters code proposes as possibly
// one person. Left.ParticipantID < Right.ParticipantID.
type PersonDuplicateProposal struct {
	Left, Right PersonDuplicateIdentity
	Signals     []PersonDuplicateSignal
	// SharedValue is the normalized shared name, else the shared local part.
	SharedValue string
	// Fingerprint hashes both clusters' members, names, and addresses and
	// the signals; a stored judgment with the same fingerprint is not asked
	// again.
	Fingerprint string
}

// PersonDuplicateJudgment records one judged proposal. Propose writes a
// reviewable candidate with Probability as its confidence; otherwise only
// the judgment is remembered.
type PersonDuplicateJudgment struct {
	Proposal    PersonDuplicateProposal
	Probability float64
	Model       string
	Propose     bool
}

// PersonDuplicateWriteResult counts what RecordPersonDuplicateJudgments
// wrote.
type PersonDuplicateWriteResult struct {
	Recorded   int `json:"recorded"`
	Candidates int `json:"candidates"`
	// Existing counts proposals that already had a candidate row, which is
	// never changed.
	Existing int `json:"existing"`
	// Dropped counts judgments whose proposal no longer holds when written
	// (an owner identity, a non-person classification, a new candidate or
	// rejection, one person binding, or changed inputs); nothing is written
	// for them and a later run proposes them afresh if they qualify again.
	Dropped int `json:"dropped"`
}

type duplicateCluster struct {
	root      int64
	members   []int64
	names     map[string]string // normalized key -> first display form
	addresses []string
}

// PersonDuplicateProposalsContext proposes pairs of identity clusters that
// may be one person: the same display name on different addresses, or the
// same distinctive local part at different domains. Only clusters with an
// email address take part. Owner clusters, clusters classified as anything
// but a person (by any source), clusters that look like a shared mailbox,
// pairs already bound to one person, pairs with any existing
// participant-to-participant candidate (in any state), pairs where one side
// was rejected for the other side's person, and pairs already judged with
// the same inputs are left out. At most limit proposals are returned
// (0 means no cap), strongest signals first.
func (s *Store) PersonDuplicateProposalsContext(
	ctx context.Context, limit int,
) ([]PersonDuplicateProposal, error) {
	var proposals []PersonDuplicateProposal
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		var err error
		proposals, err = s.personDuplicateProposalsTx(ctx, tx)
		return err
	})
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(proposals) > limit {
		proposals = proposals[:limit]
	}
	return proposals, nil
}

func (s *Store) personDuplicateProposalsTx(
	ctx context.Context, tx *loggedTx,
) ([]PersonDuplicateProposal, error) {
	edges, err := s.loadLinkEdgesTxContext(ctx, tx)
	if err != nil {
		return nil, err
	}
	index := newClusterIndex(edges)
	owners, err := ownerParticipantIDsTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	hidden, err := s.hiddenCorrespondentParticipantsTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	excludedRoots := map[int64]struct{}{}
	for id := range owners {
		excludedRoots[index.rootOf(id)] = struct{}{}
	}
	for id := range hidden {
		excludedRoots[index.rootOf(id)] = struct{}{}
	}

	clusters := map[int64]*duplicateCluster{}
	rows, err := tx.QueryContext(ctx, `SELECT id, email_address, display_name FROM participants
		WHERE email_address IS NOT NULL AND TRIM(email_address) <> '' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load duplicate-person participants: %w", err)
	}
	for rows.Next() {
		var id int64
		var email string
		var name sql.NullString
		if err := rows.Scan(&id, &email, &name); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan duplicate-person participant: %w", err)
		}
		root := index.rootOf(id)
		if _, excluded := excludedRoots[root]; excluded {
			continue
		}
		cluster, ok := clusters[root]
		if !ok {
			cluster = &duplicateCluster{root: root, members: index.membersOf(id), names: map[string]string{}}
			clusters[root] = cluster
		}
		address := strings.ToLower(strings.TrimSpace(email))
		if !slices.Contains(cluster.addresses, address) {
			cluster.addresses = append(cluster.addresses, address)
		}
		if key, display, ok := duplicateNameKey(name.String); ok {
			if _, seen := cluster.names[key]; !seen {
				cluster.names[key] = display
			}
		}
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close duplicate-person participants: %w", err)
	}

	type pairKey struct{ left, right int64 }
	pairs := map[pairKey]map[PersonDuplicateSignal]string{}
	addGroup := func(signal PersonDuplicateSignal, value string, roots []int64) {
		slices.Sort(roots)
		roots = slices.Compact(roots)
		if len(roots) < 2 || len(roots) > maxDuplicateGroupClusters {
			return
		}
		for i := range roots {
			for j := i + 1; j < len(roots); j++ {
				key := pairKey{roots[i], roots[j]}
				if pairs[key] == nil {
					pairs[key] = map[PersonDuplicateSignal]string{}
				}
				// The smallest shared value represents the signal, so the
				// proposal does not depend on map iteration order.
				if prior, seen := pairs[key][signal]; !seen || value < prior {
					pairs[key][signal] = value
				}
			}
		}
	}
	byName := map[string][]int64{}
	byLocal := map[string]map[int64]map[string]struct{}{}
	for root, cluster := range clusters {
		for key := range cluster.names {
			byName[key] = append(byName[key], root)
		}
		for _, address := range cluster.addresses {
			local, domain, ok := duplicateLocalPart(address)
			if !ok {
				continue
			}
			if byLocal[local] == nil {
				byLocal[local] = map[int64]map[string]struct{}{}
			}
			if byLocal[local][root] == nil {
				byLocal[local][root] = map[string]struct{}{}
			}
			byLocal[local][root][domain] = struct{}{}
		}
	}
	for key, roots := range byName {
		// The same name on the same address cannot be two clusters, so every
		// pair here spans different addresses.
		addGroup(PersonDuplicateSameName, key, roots)
	}
	for local, rootDomains := range byLocal {
		roots := make([]int64, 0, len(rootDomains))
		for root := range rootDomains {
			roots = append(roots, root)
		}
		slices.Sort(roots)
		if len(roots) < 2 || len(roots) > maxDuplicateGroupClusters {
			continue
		}
		for i := range roots {
			for j := i + 1; j < len(roots); j++ {
				if !differentDomains(rootDomains[roots[i]], rootDomains[roots[j]]) {
					continue
				}
				addGroup(PersonDuplicateSameLocalPart, local, []int64{roots[i], roots[j]})
			}
		}
	}
	if len(pairs) == 0 {
		return []PersonDuplicateProposal{}, nil
	}

	involved := map[int64][]int64{}
	allMembers := []int64{}
	for key := range pairs {
		for _, root := range []int64{key.left, key.right} {
			if _, ok := involved[root]; !ok {
				involved[root] = clusters[root].members
				allMembers = append(allMembers, clusters[root].members...)
			}
		}
	}
	bindings, err := personBindingsForParticipantsTx(ctx, tx, allMembers)
	if err != nil {
		return nil, err
	}
	shared, err := s.sharedMailboxSignalsTx(ctx, tx, involved)
	if err != nil {
		return nil, err
	}
	decided, rejectedForPerson, err := existingDuplicatePairsTx(ctx, tx, index)
	if err != nil {
		return nil, err
	}
	judged, err := personDuplicateJudgmentFingerprintsTx(ctx, tx)
	if err != nil {
		return nil, err
	}

	proposals := make([]PersonDuplicateProposal, 0, len(pairs))
	for key, signals := range pairs {
		if _, ok := shared[key.left]; ok {
			continue
		}
		if _, ok := shared[key.right]; ok {
			continue
		}
		if _, ok := decided[[2]int64{key.left, key.right}]; ok {
			continue
		}
		leftPersons := personsForMembers(clusters[key.left].members, bindings)
		rightPersons := personsForMembers(clusters[key.right].members, bindings)
		if len(leftPersons) > 1 || len(rightPersons) > 1 {
			continue
		}
		if len(leftPersons) == 1 && len(rightPersons) == 1 && leftPersons[0] == rightPersons[0] {
			continue
		}
		if rejectedAcross(key.left, rightPersons, rejectedForPerson) ||
			rejectedAcross(key.right, leftPersons, rejectedForPerson) {
			continue
		}
		proposal := buildDuplicateProposal(clusters[key.left], clusters[key.right], leftPersons, rightPersons, signals)
		if fingerprint, ok := judged[[2]int64{key.left, key.right}]; ok && fingerprint == proposal.Fingerprint {
			continue
		}
		proposals = append(proposals, proposal)
	}
	slices.SortFunc(proposals, func(a, b PersonDuplicateProposal) int {
		if len(a.Signals) != len(b.Signals) {
			return len(b.Signals) - len(a.Signals)
		}
		if a.Left.ParticipantID != b.Left.ParticipantID {
			return compareInt64(a.Left.ParticipantID, b.Left.ParticipantID)
		}
		return compareInt64(a.Right.ParticipantID, b.Right.ParticipantID)
	})
	return proposals, nil
}

func rejectedAcross(root int64, persons []int64, rejected map[[2]int64]struct{}) bool {
	for _, person := range persons {
		if _, ok := rejected[[2]int64{root, person}]; ok {
			return true
		}
	}
	return false
}

func differentDomains(left, right map[string]struct{}) bool {
	for domain := range left {
		for other := range right {
			if domain != other {
				return true
			}
		}
	}
	return false
}

func buildDuplicateProposal(
	left, right *duplicateCluster, leftPersons, rightPersons []int64,
	signals map[PersonDuplicateSignal]string,
) PersonDuplicateProposal {
	proposal := PersonDuplicateProposal{
		Left:  duplicateIdentity(left, leftPersons),
		Right: duplicateIdentity(right, rightPersons),
	}
	for _, signal := range []PersonDuplicateSignal{PersonDuplicateSameName, PersonDuplicateSameLocalPart} {
		if value, ok := signals[signal]; ok {
			proposal.Signals = append(proposal.Signals, signal)
			if proposal.SharedValue == "" {
				proposal.SharedValue = value
			}
		}
	}
	hash := sha256.New()
	for _, part := range []struct {
		members []int64
		names   []string
		addrs   []string
	}{
		{left.members, proposal.Left.Names, left.addresses},
		{right.members, proposal.Right.Names, right.addresses},
	} {
		for _, member := range part.members {
			_, _ = hash.Write([]byte(strconv.FormatInt(member, 10) + ","))
		}
		_, _ = hash.Write([]byte("|" + strings.Join(part.names, "\x1f") + "|"))
		addresses := slices.Clone(part.addrs)
		slices.Sort(addresses)
		_, _ = hash.Write([]byte(strings.Join(addresses, "\x1f") + "\x00"))
	}
	for _, signal := range proposal.Signals {
		_, _ = hash.Write([]byte(string(signal) + "=" + signals[signal] + "\x00"))
	}
	proposal.Fingerprint = hex.EncodeToString(hash.Sum(nil))
	return proposal
}

func duplicateIdentity(cluster *duplicateCluster, persons []int64) PersonDuplicateIdentity {
	identity := PersonDuplicateIdentity{ParticipantID: cluster.root}
	if len(persons) == 1 {
		person := persons[0]
		identity.PersonID = &person
	}
	keys := make([]string, 0, len(cluster.names))
	for key := range cluster.names {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys[:min(len(keys), maxDuplicateNames)] {
		identity.Names = append(identity.Names, cluster.names[key])
	}
	addresses := slices.Clone(cluster.addresses)
	slices.Sort(addresses)
	identity.Addresses = addresses[:min(len(addresses), maxDuplicateAddresses)]
	return identity
}

// duplicateNameKey normalizes a display name for grouping: letters only,
// lowercased, words sorted, so "Doe, Jane" and "Jane Doe" share a key. A
// name with an address or digits, fewer than two words, fewer than five
// letters, or a team or service word proposes nothing.
func duplicateNameKey(raw string) (string, string, bool) {
	display := strings.Join(strings.Fields(raw), " ")
	if display == "" || strings.ContainsAny(display, "@0123456789") {
		return "", "", false
	}
	words := strings.FieldsFunc(strings.ToLower(display), func(r rune) bool {
		return !unicode.IsLetter(r) && r != '\'' && r != '-'
	})
	letters := 0
	kept := words[:0]
	for _, word := range words {
		word = strings.Trim(word, "'-")
		if word == "" {
			continue
		}
		if _, stop := duplicateNameStopwords[word]; stop {
			return "", "", false
		}
		for _, r := range word {
			if unicode.IsLetter(r) {
				letters++
			}
		}
		kept = append(kept, word)
	}
	if len(kept) < 2 || letters < 5 {
		return "", "", false
	}
	slices.Sort(kept)
	return strings.Join(kept, " "), display, true
}

// duplicateLocalPart returns an address's local part (without a +tag) and
// domain when the local part is distinctive: at least five characters with
// a letter, and not a role, list, or no-reply address.
func duplicateLocalPart(address string) (string, string, bool) {
	local, domain := correspondentkind.SplitEmail(address)
	if local == "" || domain == "" {
		return "", "", false
	}
	if plus := strings.IndexByte(local, '+'); plus > 0 {
		local = local[:plus]
	}
	if len(local) < minDuplicateLocalPartLen || !strings.ContainsFunc(local, unicode.IsLetter) {
		return "", "", false
	}
	if correspondentkind.IsRoleAddress(address) || correspondentkind.IsNoReplyAddress(address) {
		return "", "", false
	}
	return local, domain, true
}

// existingDuplicatePairsTx maps every participant-to-participant candidate
// (any state, any basis) to its pair of cluster roots, and every rejected
// participant-to-person candidate to its cluster root and person.
func existingDuplicatePairsTx(
	ctx context.Context, tx *loggedTx, index clusterIndex,
) (map[[2]int64]struct{}, map[[2]int64]struct{}, error) {
	decided := map[[2]int64]struct{}{}
	rejected := map[[2]int64]struct{}{}
	rows, err := tx.QueryContext(ctx, `SELECT left_kind, left_id, right_kind, right_id, state
		FROM identity_match_candidates
		WHERE (left_kind = ? AND right_kind = ?) OR (left_kind = ? AND right_kind = ? AND state = ?)`,
		IdentityMatchParticipant, IdentityMatchParticipant,
		IdentityMatchParticipant, IdentityMatchPerson, IdentityMatchStateRejected)
	if err != nil {
		return nil, nil, fmt.Errorf("load existing identity match pairs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var leftKind, rightKind, state string
		var leftID, rightID int64
		if err := rows.Scan(&leftKind, &leftID, &rightKind, &rightID, &state); err != nil {
			return nil, nil, fmt.Errorf("scan existing identity match pair: %w", err)
		}
		if IdentityMatchEndpointKind(rightKind) == IdentityMatchPerson {
			rejected[[2]int64{index.rootOf(leftID), rightID}] = struct{}{}
			continue
		}
		left, right := index.rootOf(leftID), index.rootOf(rightID)
		if left > right {
			left, right = right, left
		}
		decided[[2]int64{left, right}] = struct{}{}
	}
	return decided, rejected, rows.Err()
}

func personDuplicateJudgmentFingerprintsTx(
	ctx context.Context, tx *loggedTx,
) (map[[2]int64]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT left_participant_id, right_participant_id, inputs_fingerprint
		FROM person_duplicate_judgments`)
	if err != nil {
		return nil, fmt.Errorf("load person duplicate judgments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	judged := map[[2]int64]string{}
	for rows.Next() {
		var left, right int64
		var fingerprint string
		if err := rows.Scan(&left, &right, &fingerprint); err != nil {
			return nil, fmt.Errorf("scan person duplicate judgment: %w", err)
		}
		judged[[2]int64{left, right}] = fingerprint
	}
	return judged, rows.Err()
}

// ErrPersonDuplicateInvalid reports a malformed duplicate-person judgment.
var ErrPersonDuplicateInvalid = errors.New("invalid person duplicate judgment")

// RecordPersonDuplicateJudgmentsContext remembers each judgment so the same
// inputs are not asked again, and writes a reviewable
// participant-to-participant identity match candidate for every judgment
// with Propose: basis display_name, source system with source_ref
// person_duplicate, the probability as confidence, and the shared value as
// normalized value, with the signals and the judgment as evidence. A pair
// that already has a candidate (in any state) is left as it is. Nothing is
// ever accepted: only a user decision applies a candidate.
func (s *Store) RecordPersonDuplicateJudgmentsContext(
	ctx context.Context, judgments []PersonDuplicateJudgment,
) (PersonDuplicateWriteResult, error) {
	var result PersonDuplicateWriteResult
	for _, judgment := range judgments {
		left, right := judgment.Proposal.Left.ParticipantID, judgment.Proposal.Right.ParticipantID
		if left <= 0 || right <= 0 || left >= right || judgment.Probability < 0 || judgment.Probability > 1 ||
			judgment.Proposal.Fingerprint == "" || strings.TrimSpace(judgment.Model) == "" {
			return result, ErrPersonDuplicateInvalid
		}
	}
	if len(judgments) == 0 {
		return result, nil
	}
	err := retryBusyWriteErr(ctx, s, "record person duplicate judgments", func() error {
		result = PersonDuplicateWriteResult{}
		return s.withTxContext(ctx, func(tx *loggedTx) error {
			if err := s.lockIdentityMutationTxContext(ctx, tx); err != nil {
				return err
			}
			// Every exclusion is rechecked under the identity lock: only a
			// pair that is still proposed, with the same inputs, is written.
			current, err := s.personDuplicateProposalsTx(ctx, tx)
			if err != nil {
				return err
			}
			live := make(map[[2]int64]string, len(current))
			for _, proposal := range current {
				live[[2]int64{proposal.Left.ParticipantID, proposal.Right.ParticipantID}] = proposal.Fingerprint
			}
			for _, judgment := range judgments {
				pair := [2]int64{judgment.Proposal.Left.ParticipantID, judgment.Proposal.Right.ParticipantID}
				if fingerprint, ok := live[pair]; !ok || fingerprint != judgment.Proposal.Fingerprint {
					result.Dropped++
					continue
				}
				if err := s.recordPersonDuplicateJudgmentTx(ctx, tx, judgment, &result); err != nil {
					return err
				}
			}
			return nil
		})
	})
	return result, err
}

func (s *Store) recordPersonDuplicateJudgmentTx(
	ctx context.Context, tx *loggedTx, judgment PersonDuplicateJudgment, result *PersonDuplicateWriteResult,
) error {
	proposal := judgment.Proposal
	left, right := proposal.Left.ParticipantID, proposal.Right.ParticipantID
	for _, id := range []int64{left, right} {
		if err := validateIdentityMatchEndpointTx(ctx, tx, IdentityMatchParticipant, id); err != nil {
			if errors.Is(err, ErrIdentityMatchEndpointNotFound) {
				// A participant merged away since the proposal was built; the
				// next run proposes its survivor.
				return nil
			}
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO person_duplicate_judgments
			(left_participant_id, right_participant_id, inputs_fingerprint, probability, model, judged_at)
		VALUES (?, ?, ?, ?, ?, `+s.dialect.Now()+`)
		ON CONFLICT (left_participant_id, right_participant_id) DO UPDATE SET
			inputs_fingerprint = excluded.inputs_fingerprint, probability = excluded.probability,
			model = excluded.model, judged_at = excluded.judged_at`,
		left, right, proposal.Fingerprint, judgment.Probability, judgment.Model); err != nil {
		return fmt.Errorf("record person duplicate judgment: %w", err)
	}
	result.Recorded++
	if !judgment.Propose {
		return nil
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM identity_match_candidates
		WHERE left_kind = ? AND right_kind = ? AND left_id = ? AND right_id = ?`,
		IdentityMatchParticipant, IdentityMatchParticipant, left, right).Scan(&existing); err != nil {
		return fmt.Errorf("check existing duplicate-person candidate: %w", err)
	}
	if existing > 0 {
		result.Existing++
		return nil
	}
	confidence := judgment.Probability
	sourceRef := PersonDuplicateSourceRef
	normalized := proposal.SharedValue
	var normalizedValue *string
	if strings.TrimSpace(normalized) != "" {
		normalizedValue = &normalized
	}
	candidate, created, err := s.upsertIdentityMatchCandidateTx(ctx, tx, IdentityMatchCandidateInput{
		LeftKind: IdentityMatchParticipant, LeftID: left,
		RightKind: IdentityMatchParticipant, RightID: right,
		Basis: IdentityMatchDisplayName, NormalizedValue: normalizedValue,
		State: IdentityMatchStateCandidate, Confidence: &confidence,
		Source: ProvenanceSystem, SourceRef: &sourceRef,
	}, IdentityMatchParticipant, left, IdentityMatchParticipant, right, nil, false)
	if err != nil {
		return fmt.Errorf("write duplicate-person candidate: %w", err)
	}
	if !created {
		result.Existing++
		return nil
	}
	result.Candidates++
	details := make([]string, 0, len(proposal.Signals)+1)
	for _, signal := range proposal.Signals {
		details = append(details, string(signal))
	}
	details = append(details, fmt.Sprintf("jev same_person %.2f (%s)", judgment.Probability, judgment.Model))
	for _, detail := range details {
		if _, _, err := s.addIdentityMatchEvidenceTx(ctx, tx, candidate.ID, PersonDuplicateEvidenceKind,
			IdentityMatchEvidenceInput{Detail: &detail, Source: ProvenanceSystem}); err != nil {
			return err
		}
	}
	return nil
}

// ListPersonDuplicateCandidatesContext lists only duplicate-person
// candidates, with the same state filter and paging as
// ListIdentityMatchCandidatesContext.
func (s *Store) ListPersonDuplicateCandidatesContext(
	ctx context.Context, states []IdentityMatchState, limit, offset int,
) ([]IdentityMatchCandidate, error) {
	return s.listIdentityMatchCandidatesContext(ctx, states,
		`c.source_ref = ? AND c.left_kind = ? AND c.right_kind = ?`,
		[]any{PersonDuplicateSourceRef, IdentityMatchParticipant, IdentityMatchParticipant},
		limit, offset)
}
