package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/textimport"
)

// revalidatePersonDuplicateTx rechecks one judged proposal under the
// identity lock, reading only the two clusters: their links and members,
// owner status, correspondent kinds, shared-mailbox signals, person
// bindings, candidates and rejections between them, and the names and
// addresses behind the proposal's signals. It reports whether the pair
// still qualifies with exactly the judged inputs. How many other clusters
// share the name or local part is not rechecked; that only decided which
// pairs were worth asking.
func (s *Store) revalidatePersonDuplicateTx(
	ctx context.Context, tx *loggedTx, proposal PersonDuplicateProposal,
) (bool, error) {
	left, right := proposal.Left.ParticipantID, proposal.Right.ParticipantID
	components, err := linkComponentsFromTx(ctx, tx, []int64{left, right})
	if err != nil {
		return false, err
	}
	if components[left] != left || components[right] != right {
		// Linked together, or no longer the lowest member of their clusters.
		return false, nil
	}
	leftMembers, rightMembers, all := []int64{}, []int64{}, []int64{}
	for member, root := range components {
		switch root {
		case left:
			leftMembers = append(leftMembers, member)
		case right:
			rightMembers = append(rightMembers, member)
		}
		all = append(all, member)
	}
	slices.Sort(leftMembers)
	slices.Sort(rightMembers)
	slices.Sort(all)
	if s.personDuplicateRevalidateHook != nil {
		s.personDuplicateRevalidateHook(slices.Clone(all))
	}

	owner, err := anyOwnerParticipantTx(ctx, tx, all)
	if err != nil || owner {
		return false, err
	}
	rows, err := loadCorrespondentKindRowsForTx(ctx, tx, all)
	if err != nil {
		return false, err
	}
	effective := map[int64]correspondentKindRow{}
	for _, row := range rows {
		root := components[row.participantID]
		if current, ok := effective[root]; !ok || row.rowWins(current) {
			effective[root] = row
		}
	}
	overrides := map[int64]struct{}{}
	for root, row := range effective {
		if !row.kind.IsPerson() {
			return false, nil
		}
		if row.source == correspondentkind.SourceUser {
			for member, memberRoot := range components {
				if memberRoot == root {
					overrides[member] = struct{}{}
				}
			}
		}
	}
	shared, err := s.sharedMailboxSignalsWithOverridesTx(ctx, tx,
		map[int64][]int64{left: leftMembers, right: rightMembers}, overrides)
	if err != nil || len(shared) > 0 {
		return false, err
	}
	bindings, err := personBindingsForParticipantsTx(ctx, tx, all)
	if err != nil {
		return false, err
	}
	leftPersons := personsForMembers(leftMembers, bindings)
	rightPersons := personsForMembers(rightMembers, bindings)
	if len(leftPersons) > 1 || len(rightPersons) > 1 ||
		len(leftPersons) == 1 && len(rightPersons) == 1 && leftPersons[0] == rightPersons[0] {
		return false, nil
	}
	decided, err := duplicatePairDecidedTx(ctx, tx, components, left, right, leftPersons, rightPersons)
	if err != nil || decided {
		return false, err
	}

	clusters := map[int64]*duplicateCluster{
		left:  {root: left, members: leftMembers, names: map[string]string{}},
		right: {root: right, members: rightMembers, names: map[string]string{}},
	}
	if err := queryInChunksContext(ctx, tx, all, nil, `
		SELECT id, email_address, display_name FROM participants
		WHERE id IN (%s) AND email_address IS NOT NULL AND TRIM(email_address) <> ''
		ORDER BY id`, func(rows *loggedRows) error {
		var id int64
		var email string
		var name sql.NullString
		if err := rows.Scan(&id, &email, &name); err != nil {
			return fmt.Errorf("scan duplicate-person participant: %w", err)
		}
		clusters[components[id]].add(email, name.String)
		return nil
	}); err != nil {
		return false, fmt.Errorf("load duplicate-person participants: %w", err)
	}
	signals := pairDuplicateSignals(clusters[left], clusters[right])
	judged := map[PersonDuplicateSignal]string{}
	for _, signal := range proposal.Signals {
		value, ok := signals[signal]
		if !ok {
			return false, nil
		}
		judged[signal] = value
	}
	current := buildDuplicateProposal(clusters[left], clusters[right], leftPersons, rightPersons, judged)
	return current.Fingerprint == proposal.Fingerprint, nil
}

// add records one email-bearing participant's address and display name.
func (c *duplicateCluster) add(email, name string) {
	address := strings.ToLower(strings.TrimSpace(email))
	if !slices.Contains(c.addresses, address) {
		c.addresses = append(c.addresses, address)
	}
	if key, display, ok := duplicateNameKey(name); ok {
		if _, seen := c.names[key]; !seen {
			c.names[key] = display
		}
	}
}

// pairDuplicateSignals computes the signals two clusters share, each
// represented by its smallest shared value, exactly as the proposal builder
// does for one pair.
func pairDuplicateSignals(left, right *duplicateCluster) map[PersonDuplicateSignal]string {
	signals := map[PersonDuplicateSignal]string{}
	keep := func(signal PersonDuplicateSignal, value string) {
		if prior, ok := signals[signal]; !ok || value < prior {
			signals[signal] = value
		}
	}
	for key := range left.names {
		if _, ok := right.names[key]; ok {
			keep(PersonDuplicateSameName, key)
		}
	}
	domains := func(cluster *duplicateCluster) map[string]map[string]struct{} {
		result := map[string]map[string]struct{}{}
		for _, address := range cluster.addresses {
			local, domain, ok := duplicateLocalPart(address)
			if !ok {
				continue
			}
			if result[local] == nil {
				result[local] = map[string]struct{}{}
			}
			result[local][domain] = struct{}{}
		}
		return result
	}
	rightDomains := domains(right)
	for local, leftDomains := range domains(left) {
		if other, ok := rightDomains[local]; ok && differentDomains(leftDomains, other) {
			keep(PersonDuplicateSameLocalPart, local)
		}
	}
	return signals
}

// duplicatePairDecidedTx reports whether any participant-to-participant
// candidate joins the two clusters, or either cluster was rejected for the
// other's person.
func duplicatePairDecidedTx(
	ctx context.Context, tx *loggedTx, components map[int64]int64,
	left, right int64, leftPersons, rightPersons []int64,
) (bool, error) {
	members := make([]int64, 0, len(components))
	for member := range components {
		members = append(members, member)
	}
	slices.Sort(members)
	decided := false
	if err := queryInChunksContext(ctx, tx, members,
		[]any{IdentityMatchParticipant, IdentityMatchParticipant, IdentityMatchPerson, IdentityMatchStateRejected},
		`SELECT left_id, right_kind, right_id FROM identity_match_candidates
		WHERE left_kind = ? AND (right_kind = ? OR (right_kind = ? AND state = ?)) AND left_id IN (%s)`,
		func(rows *loggedRows) error {
			var leftID, rightID int64
			var rightKind string
			if err := rows.Scan(&leftID, &rightKind, &rightID); err != nil {
				return fmt.Errorf("scan duplicate-person decision: %w", err)
			}
			from := components[leftID]
			if IdentityMatchEndpointKind(rightKind) == IdentityMatchPerson {
				other := rightPersons
				if from == right {
					other = leftPersons
				}
				if slices.Contains(other, rightID) {
					decided = true
				}
				return nil
			}
			if to, ok := components[rightID]; ok && to != from && (to == left || to == right) {
				decided = true
			}
			return nil
		}); err != nil {
		return false, fmt.Errorf("load duplicate-person decisions: %w", err)
	}
	return decided, nil
}

// anyOwnerParticipantTx reports whether any of the participants is one of
// the archive owner's identities, by the rules ownerParticipantIDsTx
// applies to the whole archive, reading only these participants.
func anyOwnerParticipantTx(ctx context.Context, tx *loggedTx, participantIDs []int64) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT address FROM account_identities`)
	if err != nil {
		return false, fmt.Errorf("load owner identities: %w", err)
	}
	lower, exact, phones := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			_ = rows.Close()
			return false, fmt.Errorf("scan owner identity: %w", err)
		}
		lower[strings.ToLower(address)] = struct{}{}
		exact[address] = struct{}{}
		if phoneShaped(address) {
			if normalized, err := textimport.NormalizePhone(address); err == nil {
				phones[normalized] = struct{}{}
			}
		}
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close owner identities: %w", err)
	}
	if len(exact) == 0 {
		return false, nil
	}
	isPhone := func(raw string) bool {
		if strings.TrimSpace(raw) == "" {
			return false
		}
		normalized, err := textimport.NormalizePhone(raw)
		if err != nil {
			return false
		}
		_, ok := phones[normalized]
		return ok
	}
	owner := false
	hasEmail := map[int64]bool{}
	if err := queryInChunksContext(ctx, tx, participantIDs, nil,
		`SELECT id, COALESCE(email_address, ''), COALESCE(phone_number, '') FROM participants WHERE id IN (%s)`,
		func(rows *loggedRows) error {
			var id int64
			var email, phone string
			if err := rows.Scan(&id, &email, &phone); err != nil {
				return fmt.Errorf("scan owner candidate participant: %w", err)
			}
			if strings.TrimSpace(email) != "" {
				hasEmail[id] = true
				if _, ok := lower[strings.ToLower(email)]; ok {
					owner = true
				}
			}
			if isPhone(phone) {
				owner = true
			}
			return nil
		}); err != nil {
		return false, err
	}
	if err := queryInChunksContext(ctx, tx, participantIDs, nil,
		`SELECT participant_id, identifier_type, identifier_value FROM participant_identifiers
		WHERE participant_id IN (%s)`,
		func(rows *loggedRows) error {
			var id int64
			var kind, value string
			if err := rows.Scan(&id, &kind, &value); err != nil {
				return fmt.Errorf("scan owner candidate identifier: %w", err)
			}
			switch {
			case kind == "email":
				if _, ok := lower[strings.ToLower(value)]; ok && !hasEmail[id] {
					owner = true
				}
			default:
				if _, ok := exact[value]; ok {
					owner = true
				}
				if kind == "phone" && isPhone(value) {
					owner = true
				}
			}
			return nil
		}); err != nil {
		return false, err
	}
	return owner, nil
}
