package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"strings"

	"go.kenn.io/msgvault/internal/mime"

	"go.kenn.io/msgvault/internal/textutil"
)

// EnsureParticipant gets or creates a participant by email. INSERT ... ON
// CONFLICT ... DO NOTHING followed by an in-transaction lookup lets concurrent
// callers converge on the same row without a unique-constraint error. Display
// name and domain are left untouched on conflict to preserve hand-edited values.
func (s *Store) EnsureParticipant(email, displayName, domain string) (int64, error) {
	return s.EnsureParticipantContext(context.Background(), email, displayName, domain)
}

// EnsureParticipantContext is the request-aware form of EnsureParticipant.
func (s *Store) EnsureParticipantContext(
	ctx context.Context,
	email,
	displayName,
	domain string,
) (int64, error) {
	var id int64
	err := s.withTxContext(ctx, func(tx *loggedTx) error {
		var err error
		id, err = ensureParticipantWith(
			boundQuerier{ctx: ctx, q: tx},
			s.dialect,
			email,
			displayName,
			domain,
			func() error {
				return s.bumpParticipantDisplayNameRevisionContext(ctx, tx)
			},
		)
		return err
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

func ensureParticipantWith(
	q querier,
	dialect Dialect,
	email,
	displayName,
	domain string,
	onInsert func() error,
) (int64, error) {
	displayName = textutil.StripLabelEmoji(displayName)
	// ON CONFLICT must mirror the partial unique index on
	// participants(email_address) WHERE email_address IS NOT NULL. SQLite
	// requires the WHERE clause on the conflict target to
	// match the partial index exactly. INSERT ... DO NOTHING lets us use
	// RowsAffected to distinguish an actual insert from an idempotent retry.
	for range 3 {
		result, err := q.Exec(fmt.Sprintf(`
			INSERT INTO participants (email_address, display_name, domain, created_at, updated_at)
			VALUES (?, ?, ?, %s, %s)
			ON CONFLICT (email_address) WHERE email_address IS NOT NULL
				DO NOTHING
		`, dialect.Now(), dialect.Now()), email, displayName, domain)
		if err != nil {
			return 0, err
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("check participant insert: %w", err)
		}
		if inserted > 0 && onInsert != nil {
			if err := onInsert(); err != nil {
				return 0, err
			}
		}
		var id int64
		err = q.QueryRow("SELECT id FROM participants WHERE email_address = ?", email).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		// Retry if the expected participant row is absent after the insert.
	}
	return 0, fmt.Errorf("ensure participant %q after concurrent deletion", email)
}

// EnsureParticipantsBatch gets or creates participants in batch.
// Returns a map of email -> participant ID.
func (s *Store) EnsureParticipantsBatch(addresses []mime.Address) (map[string]int64, error) {
	if len(addresses) == 0 {
		return make(map[string]int64), nil
	}

	result := make(map[string]int64)
	unique := make(map[string]mime.Address, len(addresses))
	for _, addr := range addresses {
		if addr.Email == "" {
			continue
		}
		if _, exists := unique[addr.Email]; !exists {
			unique[addr.Email] = addr
		}
	}
	if len(unique) == 0 {
		return result, nil
	}
	emails := make([]string, 0, len(unique))
	for email := range unique {
		emails = append(emails, email)
	}
	sort.Strings(emails)

	err := s.withTx(func(tx *loggedTx) error {
		if err := s.lockParticipantDirectoryMutationTxContext(
			context.Background(), tx,
		); err != nil {
			return err
		}
		inserted := false
		for _, email := range emails {
			addr := unique[email]
			id, err := ensureParticipantWith(
				tx, s.dialect, addr.Email, addr.Name, addr.Domain,
				func() error {
					inserted = true
					return nil
				},
			)
			if err != nil {
				return err
			}
			result[email] = id
		}
		if inserted {
			return s.bumpParticipantDisplayNameRevision(tx)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ReplaceMessageRecipients replaces all recipients for a message atomically.
func (s *Store) ReplaceMessageRecipients(messageID int64, recipientType string, participantIDs []int64, displayNames []string) error {
	return s.ReplaceMessageRecipientsContext(context.Background(), messageID, recipientType, participantIDs, displayNames)
}

// ReplaceMessageRecipientsContext is the request-aware form of ReplaceMessageRecipients.
func (s *Store) ReplaceMessageRecipientsContext(ctx context.Context, messageID int64, recipientType string, participantIDs []int64, displayNames []string) error {
	return s.withTxContext(ctx, func(tx *loggedTx) error {
		q := boundQuerier{ctx: ctx, q: tx}
		if err := s.lockMessageForRecipientWrite(q, messageID); err != nil {
			return err
		}
		if err := s.requireSyncMessageSourceTx(q, messageID); err != nil {
			return err
		}
		if err := replaceMessageRecipientsTx(q, messageID, RecipientSet{
			Type:           recipientType,
			ParticipantIDs: participantIDs,
			DisplayNames:   displayNames,
		}); err != nil {
			return err
		}
		if recipientType != "from" {
			return nil
		}
		// 'from' rows are attribution input: the message upsert's CTE could not
		// see the envelope rows this call just replaced, and importers on this
		// granular path never reach persistMessageWith's final recompute.
		return refreshMessageAttributionWith(q, messageID)
	})
}

func (s *Store) lockMessageForRecipientWrite(q querier, messageID int64) error {
	if lockSQL := s.dialect.RowWriterLockSQL("messages", "sender_id"); lockSQL != "" {
		if _, err := q.Exec(lockSQL, messageID); err != nil {
			return fmt.Errorf("lock message %d for recipient write: %w", messageID, err)
		}
	}
	var lockedID int64
	if err := q.QueryRow(`
		SELECT id FROM messages WHERE id = ?
	`, messageID).Scan(&lockedID); err != nil {
		return fmt.Errorf("lock message %d for recipient write: %w", messageID, err)
	}
	return nil
}

func replaceMessageRecipientsTx(tx querier, messageID int64, rs RecipientSet) error {
	_, err := tx.Exec(`
		DELETE FROM message_recipients WHERE message_id = ? AND recipient_type = ?
	`, messageID, rs.Type)
	if err != nil {
		return err
	}

	if len(rs.ParticipantIDs) == 0 {
		return nil
	}

	// Collapse duplicates within this set. The table holds at most one row
	// per (message_id, participant_id, recipient_type, normalized envelope
	// address) — idx_message_recipients_envelope — so an exact repeat in one
	// call (a calendar event listing the same attendee twice) is redundant
	// and would otherwise trip the unique index and abort the entire write,
	// while the same participant under two envelope aliases keeps one row
	// per alias. The first occurrence's display name wins per row.
	type recipientRowKey struct {
		participantID int64
		email         string
	}
	seen := make(map[recipientRowKey]struct{}, len(rs.ParticipantIDs))
	ids := make([]int64, 0, len(rs.ParticipantIDs))
	names := make([]string, 0, len(rs.ParticipantIDs))
	emails := make([]string, 0, len(rs.ParticipantIDs))
	for i, pid := range rs.ParticipantIDs {
		email := ""
		if i < len(rs.EmailAddresses) {
			email = rs.EmailAddresses[i]
		}
		key := recipientRowKey{participantID: pid, email: strings.ToLower(email)}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		ids = append(ids, pid)
		name := ""
		if i < len(rs.DisplayNames) {
			name = textutil.StripLabelEmoji(rs.DisplayNames[i])
		}
		names = append(names, name)
		emails = append(emails, email)
	}

	return insertInChunks(tx, chunkInsert{
		totalRows:    len(ids),
		valuesPerRow: 5,
		prefix:       "INSERT INTO message_recipients (message_id, participant_id, recipient_type, display_name, email_address) VALUES ",
	}, func(start, end int) ([]string, []any) {
		values := make([]string, end-start)
		args := make([]any, 0, (end-start)*5)
		for i := start; i < end; i++ {
			values[i-start] = "(?, ?, ?, ?, ?)"
			args = append(args, messageID, ids[i], rs.Type, names[i], nullIfEmpty(emails[i]))
		}
		return values, args
	})
}

// Label represents a Gmail label.
type Label struct {
	ID            int64
	SourceID      sql.NullInt64
	SourceLabelID sql.NullString
	Name          string
	LabelType     sql.NullString
	SystemRole    sql.NullString
}

// LabelSystemRoleSent identifies a label whose provider metadata confirms it
// represents sent mail. It is deliberately independent of the display name.
const LabelSystemRoleSent = "sent"
