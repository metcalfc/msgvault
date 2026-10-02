package store

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/meetingweight"
)

// Calendar event kind sources. Meeting weights read only Jev kinds; a rule
// kind marks a series as decided so it is not sent, and calendar sync
// clears it whenever the series' events change.
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
	// Shapes describes the invite list of every event in the series that is
	// not cancelled, for rules decided locally. It is never sent.
	Shapes []CalendarEventShape
}

// CalendarEventShape is one event's invite list, without addresses.
type CalendarEventShape struct {
	AllDay           bool
	OrganizedByOwner bool
	// OwnerInvited reports that one of your addresses is an attendee.
	OwnerInvited bool
	// Others counts attendees that are not one of your addresses.
	Others int
	// OtherNotAPerson reports that an other attendee's identity is
	// classified as a mailing list, shared mailbox, automated sender, or
	// organization.
	OtherNotAPerson bool
}

// notOnePersonKinds are correspondent kinds whose address stands for more
// than, or other than, one person.
var notOnePersonKinds = []correspondentkind.Kind{
	correspondentkind.MailingList, correspondentkind.SharedMailbox,
	correspondentkind.Automated, correspondentkind.Organization,
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
	if err := s.describeCalendarKindCandidates(ctx, candidates); err != nil {
		return nil, err
	}
	return candidates, nil
}

// describeCalendarKindCandidates fills candidates that carry only their
// conversation and occurrence count from the series' current events.
func (s *Store) describeCalendarKindCandidates(ctx context.Context, candidates []CalendarEventKindCandidate) error {
	if len(candidates) == 0 {
		return nil
	}
	owner, err := s.OwnerEmailAddressesContext(ctx)
	if err != nil {
		return err
	}
	notOnePerson, err := s.participantsByEffectiveKindContext(ctx, func(kind correspondentkind.Kind) bool {
		return slices.Contains(notOnePersonKinds, kind)
	})
	if err != nil {
		return fmt.Errorf("read correspondent kinds for calendar kinds: %w", err)
	}
	for i := range candidates {
		if err := s.describeCalendarKindCandidate(ctx, &candidates[i]); err != nil {
			return err
		}
		if candidates[i].NotAMeeting {
			continue
		}
		if err := s.shapeCalendarKindCandidate(ctx, &candidates[i], owner, notOnePerson); err != nil {
			return err
		}
	}
	return nil
}

// CalendarRuleKind is a stored rule kind with its series described from
// current data, so the rule can be checked again.
type CalendarRuleKind struct {
	Kind      string
	Candidate CalendarEventKindCandidate
}

// CalendarRuleKindsContext lists every stored rule kind with its series
// described exactly as a candidate would be.
func (s *Store) CalendarRuleKindsContext(ctx context.Context) ([]CalendarRuleKind, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT k.conversation_id, k.kind,
		       (SELECT COUNT(*) FROM messages m
		         WHERE m.conversation_id = k.conversation_id AND m.message_type = ? AND m.deleted_at IS NULL)
		FROM calendar_event_kinds k
		WHERE k.source = ?
		ORDER BY k.conversation_id`, calendarEventMessageType, CalendarEventKindSourceRule)
	if err != nil {
		return nil, fmt.Errorf("list calendar rule kinds: %w", err)
	}
	var kinds []string
	var candidates []CalendarEventKindCandidate
	for rows.Next() {
		var candidate CalendarEventKindCandidate
		var kind string
		if err := rows.Scan(&candidate.ConversationID, &kind, &candidate.Occurrences); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan calendar rule kind: %w", err)
		}
		kinds = append(kinds, kind)
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate calendar rule kinds: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close calendar rule kinds: %w", err)
	}
	if err := s.describeCalendarKindCandidates(ctx, candidates); err != nil {
		return nil, err
	}
	result := make([]CalendarRuleKind, len(candidates))
	for i := range candidates {
		result[i] = CalendarRuleKind{Kind: kinds[i], Candidate: candidates[i]}
	}
	return result, nil
}

// DeleteCalendarRuleKindsContext deletes stored rule kinds that no longer
// hold. A row is deleted only while it still has the kind that was checked,
// and a Jev kind is never deleted. It returns how many rows were deleted.
func (s *Store) DeleteCalendarRuleKindsContext(ctx context.Context, kinds []CalendarEventKind) (int, error) {
	deleted := 0
	err := s.withTxContext(ctx, func(tx *loggedTx) error {
		for _, kind := range kinds {
			result, err := tx.ExecContext(ctx, `
				DELETE FROM calendar_event_kinds
				WHERE conversation_id = ? AND kind = ? AND source = ?`,
				kind.ConversationID, kind.Kind, CalendarEventKindSourceRule)
			if err != nil {
				return fmt.Errorf("delete calendar rule kind: %w", err)
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("count calendar rule kind delete: %w", err)
			}
			deleted += int(affected)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

// shapeCalendarKindCandidate records the invite list of every event in the
// series that is not cancelled. owner holds your addresses; each event's
// calendar account address counts as yours too.
func (s *Store) shapeCalendarKindCandidate(
	ctx context.Context, candidate *CalendarEventKindCandidate,
	owner map[string]struct{}, notOnePerson map[int64]correspondentkind.Kind,
) error {
	type eventRow struct {
		shape   CalendarEventShape
		account string
	}
	events := map[int64]*eventRow{}
	var order []int64
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.metadata, m.is_from_me
		FROM messages m
		WHERE m.conversation_id = ? AND m.message_type = ? AND m.deleted_at IS NULL
		ORDER BY m.id`, candidate.ConversationID, calendarEventMessageType)
	if err != nil {
		return fmt.Errorf("read calendar kind series events: %w", err)
	}
	for rows.Next() {
		var id int64
		var metadata sql.NullString
		var fromMe bool
		if err := rows.Scan(&id, &metadata, &fromMe); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan calendar kind series event: %w", err)
		}
		var facts calendarEventFacts
		if metadata.Valid && strings.TrimSpace(metadata.String) != "" {
			if json.Unmarshal([]byte(metadata.String), &facts) != nil {
				facts = calendarEventFacts{}
			}
		}
		if strings.EqualFold(strings.TrimSpace(facts.Status), "cancelled") {
			continue
		}
		events[id] = &eventRow{
			shape: CalendarEventShape{
				AllDay: facts.AllDay, OrganizedByOwner: fromMe,
				OwnerInvited: strings.TrimSpace(facts.OwnerResponseStatus) != "",
			},
			account: strings.ToLower(strings.TrimSpace(facts.AccountEmail)),
		}
		order = append(order, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate calendar kind series events: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close calendar kind series events: %w", err)
	}
	attendees, err := s.db.QueryContext(ctx, `
		SELECT r.message_id, p.id, COALESCE(p.email_address, '')
		FROM message_recipients r
		JOIN participants p ON p.id = r.participant_id
		WHERE r.recipient_type = 'to' AND r.message_id IN (
			SELECT m.id FROM messages m
			WHERE m.conversation_id = ? AND m.message_type = ? AND m.deleted_at IS NULL
		)`, candidate.ConversationID, calendarEventMessageType)
	if err != nil {
		return fmt.Errorf("read calendar kind series attendees: %w", err)
	}
	defer func() { _ = attendees.Close() }()
	for attendees.Next() {
		var messageID, participantID int64
		var address string
		if err := attendees.Scan(&messageID, &participantID, &address); err != nil {
			return fmt.Errorf("scan calendar kind series attendee: %w", err)
		}
		event, ok := events[messageID]
		if !ok {
			continue
		}
		address = strings.ToLower(strings.TrimSpace(address))
		_, yours := owner[address]
		if yours || (address != "" && address == event.account) {
			event.shape.OwnerInvited = true
			continue
		}
		event.shape.Others++
		if _, kinded := notOnePerson[participantID]; kinded {
			event.shape.OtherNotAPerson = true
		}
	}
	if err := attendees.Err(); err != nil {
		return fmt.Errorf("iterate calendar kind series attendees: %w", err)
	}
	candidate.Shapes = make([]CalendarEventShape, 0, len(order))
	for _, id := range order {
		candidate.Shapes = append(candidate.Shapes, events[id].shape)
	}
	return nil
}

// ClearCalendarRuleKindContext forgets the rule kind of the series an event
// belongs to, so the next event kind run decides the series again from its
// current events. Calendar sync calls it whenever it writes an event. A Jev
// kind is never cleared.
func (s *Store) ClearCalendarRuleKindContext(ctx context.Context, messageID int64) error {
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM calendar_event_kinds
		WHERE source = ? AND conversation_id = (SELECT conversation_id FROM messages WHERE id = ?)`,
		CalendarEventKindSourceRule, messageID); err != nil {
		return fmt.Errorf("clear calendar rule kind: %w", err)
	}
	return nil
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
// It returns how many rows were written. A Jev kind can change meeting
// weights, so writing one requeues the series' activity and bumps the
// meeting weight revision; a rule kind changes neither.
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
	written, weighed := 0, 0
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
			if affected == 0 || kind.Source != CalendarEventKindSourceJev {
				continue
			}
			weighed++
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
		if weighed == 0 {
			return nil
		}
		return s.bumpMeetingWeightRevisionTx(ctx, tx)
	})
	if err != nil {
		return 0, err
	}
	return written, nil
}
