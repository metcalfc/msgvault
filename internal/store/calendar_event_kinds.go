package store

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/meetingweight"
)

// Calendar event kind sources.
const (
	CalendarEventKindSourceJev  = "jev"
	CalendarEventKindSourceRule = "rule"
	// CalendarEventKindNotAMeeting is the rule kind of a series none of whose
	// events is a meeting.
	CalendarEventKindNotAMeeting = "not_a_meeting"
)

// calendarKindCandidateEvents bounds how many of a series' newest events are
// read to find one that is a meeting.
const calendarKindCandidateEvents = 20

// CalendarEventKindCandidate is one calendar conversation (a recurring
// series or a standalone event) with no kind yet, described by its newest
// event that is a meeting. NotAMeeting is set when none of the events read
// is one; the other fields are then empty.
type CalendarEventKindCandidate struct {
	ConversationID        int64
	NotAMeeting           bool
	Title                 string
	AllDay                bool
	DurationMinutes       int
	Recurring             bool
	Occurrences           int
	AttendeeCount         int
	ExternalAttendeeCount int
	OrganizedByOwner      bool
	// OwnerInvited reports that you are on the attendee list. It decides
	// structural rules locally and is never sent.
	OwnerInvited bool
}

// CalendarEventKind is one decided kind to store.
type CalendarEventKind struct {
	ConversationID int64
	Kind           string
	Source         string
	Confidence     float64
	Probabilities  map[string]float64
	Model          string
}

// ErrCalendarEventKindInvalid reports a kind row with an unknown source or
// an out-of-range confidence.
var ErrCalendarEventKindInvalid = errors.New("invalid calendar event kind")

// CalendarEventKindCandidatesContext lists calendar conversations with no
// stored kind, most recently active first.
func (s *Store) CalendarEventKindCandidatesContext(ctx context.Context, limit int) ([]CalendarEventKindCandidate, error) {
	query := `
		SELECT m.conversation_id, COUNT(*)
		FROM messages m
		WHERE m.message_type = ? AND m.deleted_at IS NULL AND m.conversation_id IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM calendar_event_kinds k WHERE k.conversation_id = m.conversation_id)
		GROUP BY m.conversation_id
		ORDER BY MAX(m.sent_at) DESC, m.conversation_id DESC`
	args := []any{calendarEventMessageType}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list calendar kind candidates: %w", err)
	}
	var candidates []CalendarEventKindCandidate
	for rows.Next() {
		var candidate CalendarEventKindCandidate
		if err := rows.Scan(&candidate.ConversationID, &candidate.Occurrences); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan calendar kind candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate calendar kind candidates: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close calendar kind candidates: %w", err)
	}
	for i := range candidates {
		if err := s.describeCalendarKindCandidate(ctx, &candidates[i]); err != nil {
			return nil, err
		}
	}
	return candidates, nil
}

type calendarEventFacts struct {
	meetingweight.Metadata

	AllDay           bool     `json:"all_day"`
	Start            string   `json:"start"`
	End              string   `json:"end"`
	Recurrence       []string `json:"recurrence"`
	RecurringEventID string   `json:"recurring_event_id"`
	AccountEmail     string   `json:"account_email"`
}

// describeCalendarKindCandidate fills a candidate from its newest event
// that is a meeting.
func (s *Store) describeCalendarKindCandidate(ctx context.Context, candidate *CalendarEventKindCandidate) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, COALESCE(m.subject, ''), m.metadata, m.is_from_me
		FROM messages m
		WHERE m.conversation_id = ? AND m.message_type = ? AND m.deleted_at IS NULL
		ORDER BY m.sent_at DESC, m.id DESC
		LIMIT ?`, candidate.ConversationID, calendarEventMessageType, calendarKindCandidateEvents)
	if err != nil {
		return fmt.Errorf("read calendar kind candidate events: %w", err)
	}
	var messageID int64
	var facts calendarEventFacts
	found := false
	for !found && rows.Next() {
		var id int64
		var subject string
		var metadata sql.NullString
		var fromMe bool
		if err := rows.Scan(&id, &subject, &metadata, &fromMe); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan calendar kind candidate event: %w", err)
		}
		var parsed calendarEventFacts
		if metadata.Valid && strings.TrimSpace(metadata.String) != "" {
			if json.Unmarshal([]byte(metadata.String), &parsed) != nil {
				parsed = calendarEventFacts{}
			}
		}
		if parsed.Exclusion() != "" {
			continue
		}
		found, messageID, facts = true, id, parsed
		candidate.Title = subject
		candidate.OrganizedByOwner = fromMe
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate calendar kind candidate events: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close calendar kind candidate events: %w", err)
	}
	if !found {
		*candidate = CalendarEventKindCandidate{ConversationID: candidate.ConversationID, NotAMeeting: true}
		return nil
	}
	candidate.AllDay = facts.AllDay
	candidate.OwnerInvited = strings.TrimSpace(facts.OwnerResponseStatus) != ""
	candidate.Recurring = len(facts.Recurrence) > 0 || facts.RecurringEventID != "" || candidate.Occurrences > 1
	if !facts.AllDay {
		start, startErr := time.Parse(time.RFC3339, facts.Start)
		end, endErr := time.Parse(time.RFC3339, facts.End)
		if startErr == nil && endErr == nil && end.After(start) {
			candidate.DurationMinutes = int(end.Sub(start).Minutes())
		}
	}
	return s.countCalendarKindAttendees(ctx, candidate, messageID, facts.AccountEmail)
}

// countCalendarKindAttendees counts an event's attendees and those outside
// the calendar account's domain. Addresses never leave this function.
func (s *Store) countCalendarKindAttendees(
	ctx context.Context, candidate *CalendarEventKindCandidate, messageID int64, accountEmail string,
) error {
	_, ownerDomain, _ := strings.Cut(strings.ToLower(strings.TrimSpace(accountEmail)), "@")
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(p.email_address, '')
		FROM message_recipients r
		JOIN participants p ON p.id = r.participant_id
		WHERE r.message_id = ? AND r.recipient_type = 'to'`, messageID)
	if err != nil {
		return fmt.Errorf("read calendar kind attendees: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			return fmt.Errorf("scan calendar kind attendee: %w", err)
		}
		candidate.AttendeeCount++
		_, domain, _ := strings.Cut(strings.ToLower(strings.TrimSpace(address)), "@")
		if ownerDomain != "" && domain != "" && domain != ownerDomain {
			candidate.ExternalAttendeeCount++
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate calendar kind attendees: %w", err)
	}
	return nil
}

// WriteCalendarEventKindsContext stores kinds for conversations that have
// none. A conversation is decided once: an existing row is never replaced.
// It returns how many rows were written and bumps the meeting weight
// revision when any were.
func (s *Store) WriteCalendarEventKindsContext(ctx context.Context, kinds []CalendarEventKind) (int, error) {
	for _, kind := range kinds {
		if (kind.Source != CalendarEventKindSourceJev && kind.Source != CalendarEventKindSourceRule) ||
			strings.TrimSpace(kind.Kind) == "" || kind.Confidence < 0 || kind.Confidence > 1 {
			return 0, fmt.Errorf("%w: conversation %d", ErrCalendarEventKindInvalid, kind.ConversationID)
		}
	}
	if len(kinds) == 0 {
		return 0, nil
	}
	written := 0
	err := s.withTxContext(ctx, func(tx *loggedTx) error {
		for _, kind := range kinds {
			probabilities := kind.Probabilities
			if probabilities == nil {
				probabilities = map[string]float64{}
			}
			encoded, err := json.Marshal(probabilities, json.Deterministic(true))
			if err != nil {
				return fmt.Errorf("encode calendar kind probabilities: %w", err)
			}
			var exists int
			err = tx.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE id = ?`, kind.ConversationID).Scan(&exists)
			if errors.Is(err, sql.ErrNoRows) {
				continue // the conversation was removed since it was read
			}
			if err != nil {
				return fmt.Errorf("check calendar kind conversation: %w", err)
			}
			result, err := tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO calendar_event_kinds
					(conversation_id, kind, source, confidence, probabilities_json, model)
				VALUES (?, ?, ?, ?, ?, ?)`,
				kind.ConversationID, kind.Kind, kind.Source, kind.Confidence, string(encoded), kind.Model)
			if err != nil {
				return fmt.Errorf("write calendar event kind: %w", err)
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("count calendar event kind write: %w", err)
			}
			written += int(affected)
			if affected == 0 {
				continue
			}
			// The kind can make the series' events no contact at all, so
			// requeue them for the activity projection.
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO activity_projection_queue (message_id, revision, queued_at)
				SELECT id, 1, CURRENT_TIMESTAMP FROM messages WHERE conversation_id = ?
				ON CONFLICT (message_id) DO UPDATE SET
					revision = activity_projection_queue.revision + 1,
					queued_at = CURRENT_TIMESTAMP`, kind.ConversationID); err != nil {
				return fmt.Errorf("requeue calendar series activity: %w", err)
			}
		}
		if written == 0 {
			return nil
		}
		return s.bumpMeetingWeightRevisionTx(ctx, tx)
	})
	if err != nil {
		return 0, err
	}
	return written, nil
}
