package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.kenn.io/msgvault/internal/correspondentkind"
)

// ErrContactMatchSharedMailbox reports that the matched address looks like a
// mailbox several people write from, so accepting the match would bind or
// merge people through it. The user classifies the address as not a person,
// or says it is a person, before a match through it can be accepted.
var ErrContactMatchSharedMailbox = errors.New(
	"the matched address looks like a shared mailbox")

// sharedMailboxSignalsTx computes the shared-mailbox signal for each cluster
// in clusters (keyed by any caller-chosen key). Each email address in the
// cluster is judged only by the names seen on that address, in two groups
// judged separately: the names messages carried for it (and the display
// name of the participant whose address it is), and the names of saved or
// contact profiles that list it or are bound to that participant. Names seen
// on another address or a phone number in the same cluster never count, so
// a shared household phone cannot make a personal email look shared. A
// cluster whose effective kind is a user's "this is a person" never fires.
// Clusters that do not fire are absent.
func (s *Store) sharedMailboxSignalsTx(
	ctx context.Context, tx *loggedTx, clusters map[int64][]int64,
) (map[int64]correspondentkind.SharedMailboxSignal, error) {
	if len(clusters) == 0 {
		return map[int64]correspondentkind.SharedMailboxSignal{}, nil
	}
	personOverride, err := s.userPersonOverridesTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	return s.sharedMailboxSignalsWithOverridesTx(ctx, tx, clusters, personOverride)
}

// sharedMailboxSignalsWithOverridesTx is sharedMailboxSignalsTx with the
// user's "this is a person" members supplied by the caller, so a caller
// that resolved the clusters' kinds itself reads nothing outside them.
func (s *Store) sharedMailboxSignalsWithOverridesTx(
	ctx context.Context, tx *loggedTx, clusters map[int64][]int64, personOverride map[int64]struct{},
) (map[int64]correspondentkind.SharedMailboxSignal, error) {
	result := map[int64]correspondentkind.SharedMailboxSignal{}
	if len(clusters) == 0 {
		return result, nil
	}
	members := []int64{}
	for _, cluster := range clusters {
		members = append(members, cluster...)
	}
	slices.Sort(members)
	members = slices.Compact(members)

	// Each participant's email addresses, lowercased; the primary one first.
	emails := map[int64][]string{}
	primary := map[int64]string{}
	addEmail := func(id int64, value string) {
		address := strings.ToLower(strings.TrimSpace(value))
		if address == "" || slices.Contains(emails[id], address) {
			return
		}
		emails[id] = append(emails[id], address)
	}
	messageNames := map[string][]string{}
	profileNames := map[string][]string{}
	addName := func(target map[string][]string, address string, value sql.NullString) {
		address = strings.ToLower(strings.TrimSpace(address))
		if text := strings.TrimSpace(value.String); value.Valid && text != "" && address != "" {
			target[address] = append(target[address], text)
		}
	}
	participantNames := map[int64]sql.NullString{}
	if err := queryInChunksContext(ctx, tx, members, nil, `
		SELECT id, email_address, display_name FROM participants WHERE id IN (%s)`,
		func(rows *loggedRows) error {
			var id int64
			var email, name sql.NullString
			if err := rows.Scan(&id, &email, &name); err != nil {
				return fmt.Errorf("scan cluster participant: %w", err)
			}
			if email.Valid {
				addEmail(id, email.String)
				primary[id] = strings.ToLower(strings.TrimSpace(email.String))
			}
			participantNames[id] = name
			return nil
		}); err != nil {
		return nil, fmt.Errorf("load cluster participants: %w", err)
	}
	for id, name := range participantNames {
		addName(messageNames, primary[id], name)
	}
	if err := queryInChunksContext(ctx, tx, members, nil, `
		SELECT participant_id, identifier_value FROM participant_identifiers
		WHERE identifier_type = 'email' AND participant_id IN (%s)`,
		func(rows *loggedRows) error {
			var id int64
			var email string
			if err := rows.Scan(&id, &email); err != nil {
				return fmt.Errorf("scan cluster email identifier: %w", err)
			}
			addEmail(id, email)
			return nil
		}); err != nil {
		return nil, fmt.Errorf("load cluster email identifiers: %w", err)
	}
	// A message row names the address it was sent from or to: its envelope
	// address, or else its participant's primary address.
	if err := queryInChunksContext(ctx, tx, members, nil, `
		SELECT mr.participant_id, mr.email_address, mr.display_name FROM message_recipients mr
		WHERE mr.participant_id IN (%s) AND mr.display_name IS NOT NULL AND TRIM(mr.display_name) <> ''
		GROUP BY mr.participant_id, mr.email_address, mr.display_name`,
		func(rows *loggedRows) error {
			var id int64
			var envelope, name sql.NullString
			if err := rows.Scan(&id, &envelope, &name); err != nil {
				return fmt.Errorf("scan message display name: %w", err)
			}
			address := envelope.String
			if strings.TrimSpace(address) == "" {
				address = primary[id]
			}
			addName(messageNames, address, name)
			return nil
		}); err != nil {
		return nil, fmt.Errorf("load message display names: %w", err)
	}
	if err := queryInChunksContext(ctx, tx, members, nil, `
		SELECT pp.participant_id, p.display_name FROM person_participants pp
		JOIN persons p ON p.id = pp.person_id
		WHERE pp.participant_id IN (%s)`,
		func(rows *loggedRows) error {
			var id int64
			var name sql.NullString
			if err := rows.Scan(&id, &name); err != nil {
				return fmt.Errorf("scan bound person name: %w", err)
			}
			addName(profileNames, primary[id], name)
			return nil
		}); err != nil {
		return nil, fmt.Errorf("load bound person names: %w", err)
	}
	addresses := []string{}
	for _, list := range emails {
		for _, address := range list {
			if !slices.Contains(addresses, address) {
				addresses = append(addresses, address)
			}
		}
	}
	slices.Sort(addresses)
	if err := queryInChunksContext(ctx, tx, addresses, []any{ContactAddressEmail}, `
		SELECT LOWER(cp.normalized_value), p.display_name FROM person_contact_points cp
		JOIN persons p ON p.id = cp.person_id
		WHERE cp.address_kind = ? AND cp.active_until IS NULL AND cp.superseded_at IS NULL
		  AND LOWER(cp.normalized_value) IN (%s)`,
		func(rows *loggedRows) error {
			var address string
			var name sql.NullString
			if err := rows.Scan(&address, &name); err != nil {
				return fmt.Errorf("scan profile listing the address: %w", err)
			}
			addName(profileNames, address, name)
			return nil
		}); err != nil {
		return nil, fmt.Errorf("load profiles listing the addresses: %w", err)
	}

	for key, cluster := range clusters {
		if slices.ContainsFunc(cluster, func(id int64) bool {
			_, overridden := personOverride[id]
			return overridden
		}) {
			continue
		}
		clusterEmails := []string{}
		for _, id := range cluster {
			for _, address := range emails[id] {
				if !slices.Contains(clusterEmails, address) {
					clusterEmails = append(clusterEmails, address)
				}
			}
		}
		for _, email := range clusterEmails {
			signal := correspondentkind.DetectSharedMailbox(email, messageNames[email], profileNames[email])
			if signal.Fires() {
				result[key] = signal
				break
			}
		}
	}
	return result, nil
}

// userPersonOverridesTx maps every member of a cluster whose effective
// classification is a user's explicit "this is a person".
func (s *Store) userPersonOverridesTx(ctx context.Context, tx *loggedTx) (map[int64]struct{}, error) {
	clusters, err := s.correspondentKindClustersTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	overrides := map[int64]struct{}{}
	for _, cluster := range clusters {
		if cluster.effective.kind != correspondentkind.Person ||
			cluster.effective.source != correspondentkind.SourceUser {
			continue
		}
		for _, member := range cluster.members {
			overrides[member] = struct{}{}
		}
	}
	return overrides, nil
}
