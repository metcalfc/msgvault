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
// in clusters (keyed by any caller-chosen key), from the cluster's email
// addresses and two groups of names seen on it: the participant display
// names and the names messages carried for its members, and separately the
// saved people bound to it, the contact profiles with open matches to it,
// and extraNames. A cluster the
// user said is a person never fires. Clusters that do not fire are absent.
func (s *Store) sharedMailboxSignalsTx(
	ctx context.Context, tx *loggedTx,
	clusters map[int64][]int64, extraNames map[int64][]string,
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

	personOverride := map[int64]struct{}{}
	if err := queryInChunksContext(ctx, tx, members,
		[]any{correspondentkind.SourceUser, correspondentkind.Person}, `
		SELECT participant_id FROM correspondent_kinds
		WHERE source = ? AND kind = ? AND participant_id IN (%s)`,
		func(rows *loggedRows) error {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("scan person override: %w", err)
			}
			personOverride[id] = struct{}{}
			return nil
		}); err != nil {
		return nil, fmt.Errorf("load person overrides: %w", err)
	}

	emails := map[int64][]string{}
	// Names messages carried and names of saved or contact profiles are two
	// groups judged separately: a card may call someone by a nickname.
	names := map[int64][]string{}
	profileNames := map[int64][]string{}
	addTo := func(target map[int64][]string, id int64, value sql.NullString) {
		if text := strings.TrimSpace(value.String); value.Valid && text != "" {
			target[id] = append(target[id], text)
		}
	}
	addName := func(id int64, value sql.NullString) { addTo(names, id, value) }
	addProfileName := func(id int64, value sql.NullString) { addTo(profileNames, id, value) }
	if err := queryInChunksContext(ctx, tx, members, nil, `
		SELECT id, email_address, display_name FROM participants WHERE id IN (%s)`,
		func(rows *loggedRows) error {
			var id int64
			var email, name sql.NullString
			if err := rows.Scan(&id, &email, &name); err != nil {
				return fmt.Errorf("scan cluster participant: %w", err)
			}
			if text := strings.TrimSpace(email.String); email.Valid && text != "" {
				emails[id] = append(emails[id], text)
			}
			addName(id, name)
			return nil
		}); err != nil {
		return nil, fmt.Errorf("load cluster participants: %w", err)
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
			if text := strings.TrimSpace(email); text != "" {
				emails[id] = append(emails[id], text)
			}
			return nil
		}); err != nil {
		return nil, fmt.Errorf("load cluster email identifiers: %w", err)
	}
	if err := queryInChunksContext(ctx, tx, members, nil, `
		SELECT participant_id, display_name FROM message_recipients
		WHERE participant_id IN (%s) AND display_name IS NOT NULL AND TRIM(display_name) <> ''
		GROUP BY participant_id, display_name`,
		func(rows *loggedRows) error {
			var id int64
			var name sql.NullString
			if err := rows.Scan(&id, &name); err != nil {
				return fmt.Errorf("scan message display name: %w", err)
			}
			addName(id, name)
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
			addProfileName(id, name)
			return nil
		}); err != nil {
		return nil, fmt.Errorf("load bound person names: %w", err)
	}
	if err := queryInChunksContext(ctx, tx, members, []any{
		IdentityMatchParticipant, IdentityMatchPerson, ContactMatchSourceRef,
		IdentityMatchStateCandidate, IdentityMatchStateConflict,
	}, `
		SELECT c.left_id, p.display_name FROM identity_match_candidates c
		JOIN persons p ON p.id = c.right_id
		WHERE c.left_kind = ? AND c.right_kind = ? AND c.source_ref = ?
		  AND c.state IN (?, ?) AND c.left_id IN (%s)`,
		func(rows *loggedRows) error {
			var id int64
			var name sql.NullString
			if err := rows.Scan(&id, &name); err != nil {
				return fmt.Errorf("scan matched contact name: %w", err)
			}
			addProfileName(id, name)
			return nil
		}); err != nil {
		return nil, fmt.Errorf("load matched contact names: %w", err)
	}

	for key, cluster := range clusters {
		if slices.ContainsFunc(cluster, func(id int64) bool {
			_, overridden := personOverride[id]
			return overridden
		}) {
			continue
		}
		clusterProfiles := slices.Clone(extraNames[key])
		clusterNames := []string{}
		clusterEmails := []string{}
		for _, id := range cluster {
			clusterNames = append(clusterNames, names[id]...)
			clusterProfiles = append(clusterProfiles, profileNames[id]...)
			clusterEmails = append(clusterEmails, emails[id]...)
		}
		for _, email := range clusterEmails {
			if signal := correspondentkind.DetectSharedMailbox(email, clusterNames, clusterProfiles); signal.Fires() {
				result[key] = signal
				break
			}
		}
	}
	return result, nil
}
