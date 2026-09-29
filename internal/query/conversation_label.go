package query

import (
	"context"
	"fmt"
	"strings"
)

// maxConversationLabelNames bounds how many participant names one untitled
// conversation's label lists before summarizing the rest as "+N".
const maxConversationLabelNames = 3

// sqlStoreParticipantLabelExpr renders one participants row's (alias) label
// from the live archive tables, following the display-label policy in
// person_label.go: the curated name of the durable person the participant
// is bound to, then the participant's own observed name, then the
// identifier chain (phone → email → stored identifier evidence). Unlike the
// analytics fallback it yields NULL instead of "Unknown person", so an
// unnamed participant contributes nothing to a conversation label.
func sqlStoreParticipantLabelExpr(alias string) string {
	return `COALESCE(
		(SELECT NULLIF(TRIM(lp.display_name), '')
		 FROM person_participants lb
		 JOIN persons lp ON lp.id = lb.person_id
		 WHERE lb.participant_id = ` + alias + `.id
		   AND NULLIF(TRIM(lp.display_name), '') IS NOT NULL
		 ORDER BY lb.person_id LIMIT 1),
		NULLIF(TRIM(` + alias + `.display_name), ''),
		NULLIF(TRIM(` + alias + `.phone_number), ''),
		NULLIF(TRIM(` + alias + `.email_address), ''),
		(SELECT COALESCE(NULLIF(TRIM(pi.display_value), ''), NULLIF(TRIM(pi.identifier_value), ''))
		 FROM participant_identifiers pi
		 WHERE pi.participant_id = ` + alias + `.id
		   AND COALESCE(NULLIF(TRIM(pi.display_value), ''), NULLIF(TRIM(pi.identifier_value), '')) IS NOT NULL
		 ORDER BY pi.is_primary DESC, pi.identifier_type, pi.identifier_value LIMIT 1))`
}

// fillConversationParticipantLabels names each untitled conversation on a
// page by its other participants, so no surface has to fall back to the
// conversation ID. It runs one query bounded by the page: members come from
// conversation_participants, or from message senders for a conversation
// with no membership rows, and participants who sent a message marked as
// the owner's are excluded.
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
	in := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
	query := `
		WITH members AS (
			SELECT cp.conversation_id, cp.participant_id
			FROM conversation_participants cp
			WHERE cp.conversation_id IN (` + in + `)
			UNION
			SELECT m.conversation_id, m.sender_id
			FROM messages m
			WHERE m.conversation_id IN (` + in + `)
			  AND m.sender_id IS NOT NULL
			  AND NOT EXISTS (
				SELECT 1 FROM conversation_participants cpx
				WHERE cpx.conversation_id = m.conversation_id)
		)
		SELECT mb.conversation_id, ` + sqlStoreParticipantLabelExpr("p") + `
		FROM members mb
		JOIN participants p ON p.id = mb.participant_id
		WHERE NOT EXISTS (
			SELECT 1 FROM messages mine
			WHERE mine.conversation_id = mb.conversation_id
			  AND mine.sender_id = mb.participant_id
			  AND (mine.is_from_me OR mine.identity_is_from_me))
		ORDER BY mb.conversation_id, p.id`
	args := append(append([]any{}, ids...), ids...)
	result, err := e.db.QueryContext(ctx, e.dialect.Rebind(query), args...)
	if err != nil {
		return fmt.Errorf("label untitled conversations: %w", err)
	}
	defer func() { _ = result.Close() }()
	names := make(map[int64][]string, len(ids))
	for result.Next() {
		var conversationID int64
		var label *string
		if err := result.Scan(&conversationID, &label); err != nil {
			return fmt.Errorf("scan conversation label: %w", err)
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
