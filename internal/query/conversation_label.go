package query

import (
	"context"
	"fmt"
	"strings"

	"go.kenn.io/msgvault/internal/correspondentkind"
)

// maxConversationLabelNames bounds how many participant names one untitled
// conversation's label lists before summarizing the rest as "+N".
const maxConversationLabelNames = 3

// sqlStoreBoundPersonNameExpr renders the curated name of the durable
// person one participants row (alias) is bound to, or NULL. It leads a
// participant's label, following the display-label policy in
// person_label.go, unless the participant is marked as not a person.
func sqlStoreBoundPersonNameExpr(alias string) string {
	return `(SELECT NULLIF(TRIM(lp.display_name), '')
		 FROM person_participants lb
		 JOIN persons lp ON lp.id = lb.person_id
		 WHERE lb.participant_id = ` + alias + `.id
		   AND NULLIF(TRIM(lp.display_name), '') IS NOT NULL
		 ORDER BY lb.person_id LIMIT 1)`
}

// sqlStoreOwnParticipantLabelExpr renders one participants row's (alias)
// own label from the live archive tables: its observed name, then the
// identifier chain (phone → email → stored identifier evidence). Unlike the
// analytics fallback it yields NULL instead of "Unknown person", so an
// unnamed participant contributes nothing to a conversation label.
func sqlStoreOwnParticipantLabelExpr(alias string) string {
	return `COALESCE(
		NULLIF(TRIM(` + alias + `.display_name), ''),
		NULLIF(TRIM(` + alias + `.phone_number), ''),
		NULLIF(TRIM(` + alias + `.email_address), ''),
		(SELECT COALESCE(NULLIF(TRIM(pi.display_value), ''), NULLIF(TRIM(pi.identifier_value), ''))
		 FROM participant_identifiers pi
		 WHERE pi.participant_id = ` + alias + `.id
		   AND COALESCE(NULLIF(TRIM(pi.display_value), ''), NULLIF(TRIM(pi.identifier_value), '')) IS NOT NULL
		 ORDER BY pi.is_primary DESC, pi.identifier_type, pi.identifier_value LIMIT 1))`
}

type notPersonParticipantsKey struct{}

// WithNotPersonParticipants tells conversation labels which participants
// are in a cluster marked as not a person (the store's
// NotPersonParticipantsContext). Such a participant is named by its own
// name or address, never by the person it may still be bound to. Without
// it, every participant counts as a person.
func WithNotPersonParticipants(
	ctx context.Context, participants map[int64]correspondentkind.Kind,
) context.Context {
	return context.WithValue(ctx, notPersonParticipantsKey{}, participants)
}

func notPersonParticipants(ctx context.Context) map[int64]correspondentkind.Kind {
	participants, _ := ctx.Value(notPersonParticipantsKey{}).(map[int64]correspondentkind.Kind)
	return participants
}

// conversationLabelRecentMessages bounds how many of a conversation's most
// recent messages the label query reads: they supply senders for a
// conversation without membership rows and the owner's own sends.
const conversationLabelRecentMessages = 50

// sqlConversationLabelQuery returns the label query for n page
// conversation IDs (bound once, in order). Every step is bounded by the
// page and index-backed:
//
//   - cutoffs finds each conversation's 50th most recent sent_at (or its
//     oldest, when it has fewer) with idx_messages_conversation
//     (conversation_id, sent_at DESC) probes, and recent reads only messages
//     at or after it as an index range scan. Messages without sent_at are
//     outside the window; the cutoff lookup skips them explicitly so the
//     window does not depend on where a database sorts NULLs (PostgreSQL
//     puts them first in DESC order). This path runs only on SQLite today:
//     pgEngine does not expose the SQLite text engine.
//   - The sets are MATERIALIZED so the planner evaluates each once instead
//     of re-probing messages per member.
//   - members reads conversation_participants by its primary key; only a
//     conversation with no membership rows falls back to recent senders.
//   - from_me is the set of recent senders marked as the owner's.
//   - owners are members matching any account identity (account_identities,
//     the rows the identity index turns into owner_participants), by the
//     same rules: the participant's own email, else an email identifier
//     case-insensitively, else a non-email identifier exactly. Owner
//     identities are excluded across sources, as the explore counterpart
//     column does.
func sqlConversationLabelQuery(n int) string {
	in := strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
	return fmt.Sprintf(`
		WITH page AS (
			SELECT c.id FROM conversations c WHERE c.id IN (%s)
		), cutoffs AS (
			SELECT page.id,
			       COALESCE(
			           (SELECT lm.sent_at FROM messages lm
			            WHERE lm.conversation_id = page.id AND lm.sent_at IS NOT NULL
			            ORDER BY lm.sent_at DESC LIMIT 1 OFFSET %d),
			           (SELECT MIN(lm.sent_at) FROM messages lm
			            WHERE lm.conversation_id = page.id)) AS cutoff
			FROM page
		), recent AS MATERIALIZED (
			SELECT m.conversation_id, m.sender_id, m.is_from_me, m.identity_is_from_me
			FROM cutoffs co
			JOIN messages m ON m.conversation_id = co.id AND m.sent_at >= co.cutoff
		), members AS MATERIALIZED (
			SELECT cp.conversation_id, cp.participant_id
			FROM page
			JOIN conversation_participants cp ON cp.conversation_id = page.id
			UNION
			SELECT rc.conversation_id, rc.sender_id
			FROM recent rc
			WHERE rc.sender_id IS NOT NULL
			  AND NOT EXISTS (
				SELECT 1 FROM conversation_participants cpx
				WHERE cpx.conversation_id = rc.conversation_id)
		), from_me AS MATERIALIZED (
			SELECT rc.conversation_id, rc.sender_id
			FROM recent rc
			WHERE rc.sender_id IS NOT NULL AND (rc.is_from_me OR rc.identity_is_from_me)
		), owners AS MATERIALIZED (
			SELECT p.id
			FROM participants p
			WHERE p.id IN (SELECT participant_id FROM members)
			  AND EXISTS (
				SELECT 1 FROM account_identities ai
				WHERE (NULLIF(TRIM(p.email_address), '') IS NOT NULL
				       AND LOWER(p.email_address) = LOWER(ai.address))
				   OR EXISTS (
					SELECT 1 FROM participant_identifiers pi
					WHERE pi.participant_id = p.id
					  AND ((pi.identifier_type = 'email'
					        AND NULLIF(TRIM(p.email_address), '') IS NULL
					        AND LOWER(pi.identifier_value) = LOWER(ai.address))
					       OR (pi.identifier_type <> 'email'
					        AND pi.identifier_value = ai.address))))
		)
		SELECT mb.conversation_id, p.id, %s, %s
		FROM members mb
		JOIN participants p ON p.id = mb.participant_id
		WHERE NOT EXISTS (
			SELECT 1 FROM from_me f
			WHERE f.conversation_id = mb.conversation_id AND f.sender_id = mb.participant_id)
		  AND NOT EXISTS (SELECT 1 FROM owners o WHERE o.id = mb.participant_id)
		ORDER BY mb.conversation_id, p.id`,
		in, conversationLabelRecentMessages-1,
		sqlStoreBoundPersonNameExpr("p"), sqlStoreOwnParticipantLabelExpr("p"))
}

// fillConversationParticipantLabels names each untitled conversation on a
// page by its other participants, so no surface has to fall back to the
// conversation ID. It runs one query bounded by the page (see
// sqlConversationLabelQuery); the owner is never part of the label.
func (e *SQLiteEngine) fillConversationParticipantLabels(
	ctx context.Context, rows []ConversationRow,
) error {
	positions := make(map[int64][]int)
	ids := make([]any, 0, len(rows))
	for i, row := range rows {
		if strings.TrimSpace(row.Title) != "" {
			continue
		}
		if _, seen := positions[row.ConversationID]; !seen {
			ids = append(ids, row.ConversationID)
		}
		positions[row.ConversationID] = append(positions[row.ConversationID], i)
	}
	if len(ids) == 0 {
		return nil
	}
	query := sqlConversationLabelQuery(len(ids))
	args := ids
	result, err := e.db.QueryContext(ctx, e.dialect.Rebind(query), args...)
	if err != nil {
		return fmt.Errorf("label untitled conversations: %w", err)
	}
	defer func() { _ = result.Close() }()
	notPeople := notPersonParticipants(ctx)
	names := make(map[int64][]string, len(ids))
	for result.Next() {
		var conversationID, participantID int64
		var boundName, ownLabel *string
		if err := result.Scan(&conversationID, &participantID, &boundName, &ownLabel); err != nil {
			return fmt.Errorf("scan conversation label: %w", err)
		}
		label := ownLabel
		if _, notPerson := notPeople[participantID]; !notPerson && boundName != nil {
			label = boundName
		}
		if label != nil && strings.TrimSpace(*label) != "" {
			names[conversationID] = append(names[conversationID], strings.TrimSpace(*label))
		}
	}
	if err := result.Err(); err != nil {
		return fmt.Errorf("iterate conversation labels: %w", err)
	}
	for conversationID, indexes := range positions {
		label := conversationParticipantLabel(names[conversationID])
		for _, i := range indexes {
			rows[i].ParticipantLabel = label
		}
	}
	return nil
}

// conversationParticipantLabel joins up to maxConversationLabelNames
// distinct names and counts the rest, e.g. "Avery, Blake, Casey +2".
func conversationParticipantLabel(names []string) string {
	seen := make(map[string]bool, len(names))
	unique := make([]string, 0, len(names))
	for _, name := range names {
		if !seen[name] {
			seen[name] = true
			unique = append(unique, name)
		}
	}
	if len(unique) <= maxConversationLabelNames {
		return strings.Join(unique, ", ")
	}
	return fmt.Sprintf("%s +%d", strings.Join(unique[:maxConversationLabelNames], ", "),
		len(unique)-maxConversationLabelNames)
}
