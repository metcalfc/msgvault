package store

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
)

// Meeting action assignee choices and provenances.
const (
	MeetingAssigneeChoiceAttendee   = "attendee"
	MeetingAssigneeChoiceOwner      = "owner"
	MeetingAssigneeChoiceNone       = "none_or_unclear"
	MeetingAssigneeProvenanceInfer  = "inferred"
	MeetingAssigneeProvenanceUser   = "user"
	meetingTranscriptMessageType    = "meeting_transcript"
	meetingAssigneeCandidateActions = 64
)

// ErrMeetingActionAssigneeInvalid reports an assignee row with an unknown
// choice, an out-of-range confidence, or an attendee choice without one.
var ErrMeetingActionAssigneeInvalid = errors.New("invalid meeting action assignee")

// MeetingAssigneeAttendee is one attendee with an address on a meeting,
// never one of the owner's identities.
type MeetingAssigneeAttendee struct {
	ParticipantID int64
	// Label is the display name, or the address's local part when no name
	// is known. The address itself is never exposed.
	Label string
}

// MeetingAssigneeAction is one action item the meeting tool left without an
// assignee name or address and that has no current assignee row.
type MeetingAssigneeAction struct {
	Ordinal     int
	Title       string
	Description string
}

// MeetingAssigneeCandidate is one meeting with unassigned action items.
type MeetingAssigneeCandidate struct {
	MessageID int64
	Title     string
	Attendees []MeetingAssigneeAttendee
	// OwnerParticipantID is the owner's participant on the meeting, 0 when
	// the owner is not among its recorded participants.
	OwnerParticipantID int64
	Actions            []MeetingAssigneeAction
}

// MeetingActionAssignee is one inferred assignee to store.
type MeetingActionAssignee struct {
	MessageID     int64
	Ordinal       int
	ActionTitle   string
	Choice        string
	ParticipantID int64
	Confidence    float64
	Probabilities map[string]float64
	Model         string
}

// MeetingActionAssigneeCandidatesContext lists meeting transcripts, newest
// first, that have action items with no source assignee and no current
// assignee row, with their attendees. limit caps the meetings; zero means
// no cap.
func (s *Store) MeetingActionAssigneeCandidatesContext(
	ctx context.Context, limit int,
) ([]MeetingAssigneeCandidate, error) {
	var candidates []MeetingAssigneeCandidate
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		query := `
			SELECT m.id, COALESCE(m.subject, '')
			FROM messages m
			WHERE m.message_type = ? AND m.deleted_at IS NULL
			  AND EXISTS (
				SELECT 1 FROM meeting_action_items a
				WHERE a.message_id = m.id AND a.assignee_email = '' AND a.assignee_name = ''
				  AND NOT EXISTS (
					SELECT 1 FROM meeting_action_assignees x
					WHERE x.message_id = a.message_id AND x.ordinal = a.ordinal
					  AND (x.action_title = a.title OR x.provenance = 'user')))
			ORDER BY m.sent_at DESC, m.id DESC`
		args := []any{meetingTranscriptMessageType}
		if limit > 0 {
			query += ` LIMIT ?`
			args = append(args, limit)
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("list meeting assignee candidates: %w", err)
		}
		for rows.Next() {
			var candidate MeetingAssigneeCandidate
			if err := rows.Scan(&candidate.MessageID, &candidate.Title); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan meeting assignee candidate: %w", err)
			}
			candidates = append(candidates, candidate)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate meeting assignee candidates: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close meeting assignee candidates: %w", err)
		}
		owners, err := ownerParticipantIDsTx(ctx, tx)
		if err != nil {
			return err
		}
		for i := range candidates {
			if err := meetingAssigneeActionsTx(ctx, tx, &candidates[i]); err != nil {
				return err
			}
			if err := meetingAssigneeAttendeesTx(ctx, tx, &candidates[i], owners); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

func meetingAssigneeActionsTx(ctx context.Context, tx *loggedTx, candidate *MeetingAssigneeCandidate) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT a.ordinal, a.title, a.description
		FROM meeting_action_items a
		WHERE a.message_id = ? AND a.assignee_email = '' AND a.assignee_name = ''
		  AND NOT EXISTS (
			SELECT 1 FROM meeting_action_assignees x
			WHERE x.message_id = a.message_id AND x.ordinal = a.ordinal
			  AND (x.action_title = a.title OR x.provenance = 'user'))
		ORDER BY a.ordinal
		LIMIT ?`, candidate.MessageID, meetingAssigneeCandidateActions)
	if err != nil {
		return fmt.Errorf("read meeting assignee actions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var action MeetingAssigneeAction
		if err := rows.Scan(&action.Ordinal, &action.Title, &action.Description); err != nil {
			return fmt.Errorf("scan meeting assignee action: %w", err)
		}
		candidate.Actions = append(candidate.Actions, action)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate meeting assignee actions: %w", err)
	}
	return nil
}

// meetingAssigneeAttendeesTx reads the meeting's participants with an
// address: its sender and every recipient, once each, in participant order.
// The owner's identities become OwnerParticipantID, never an attendee.
func meetingAssigneeAttendeesTx(
	ctx context.Context, tx *loggedTx, candidate *MeetingAssigneeCandidate, owners map[int64]struct{},
) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT p.id, COALESCE(p.display_name, ''), COALESCE(r.display_name, ''), p.email_address
		FROM message_recipients r
		JOIN participants p ON p.id = r.participant_id
		WHERE r.message_id = ? AND p.email_address IS NOT NULL AND TRIM(p.email_address) <> ''
		UNION ALL
		SELECT p.id, COALESCE(p.display_name, ''), '', p.email_address
		FROM messages m
		JOIN participants p ON p.id = m.sender_id
		WHERE m.id = ? AND p.email_address IS NOT NULL AND TRIM(p.email_address) <> ''
		ORDER BY 1`, candidate.MessageID, candidate.MessageID)
	if err != nil {
		return fmt.Errorf("read meeting assignee attendees: %w", err)
	}
	defer func() { _ = rows.Close() }()
	seen := map[int64]int{}
	for rows.Next() {
		var id int64
		var name, recipientName, address string
		if err := rows.Scan(&id, &name, &recipientName, &address); err != nil {
			return fmt.Errorf("scan meeting assignee attendee: %w", err)
		}
		if _, owner := owners[id]; owner {
			if candidate.OwnerParticipantID == 0 {
				candidate.OwnerParticipantID = id
			}
			continue
		}
		label := strings.Join(strings.Fields(name), " ")
		if label == "" {
			label = strings.Join(strings.Fields(recipientName), " ")
		}
		if label == "" {
			label, _, _ = strings.Cut(strings.TrimSpace(address), "@")
		}
		if index, duplicate := seen[id]; duplicate {
			if candidate.Attendees[index].Label == "" {
				candidate.Attendees[index].Label = label
			}
			continue
		}
		seen[id] = len(candidate.Attendees)
		candidate.Attendees = append(candidate.Attendees, MeetingAssigneeAttendee{ParticipantID: id, Label: label})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate meeting assignee attendees: %w", err)
	}
	return nil
}

// WriteInferredMeetingActionAssigneesContext stores inferred assignees. A
// row replaces an earlier inferred row for the same action item, never a
// 'user' row, and is skipped when the action item changed or gained a
// source assignee since it was read. It returns how many rows were written.
func (s *Store) WriteInferredMeetingActionAssigneesContext(
	ctx context.Context, assignees []MeetingActionAssignee,
) (int, error) {
	for _, assignee := range assignees {
		if err := validateMeetingActionAssignee(assignee); err != nil {
			return 0, err
		}
	}
	if len(assignees) == 0 {
		return 0, nil
	}
	written := 0
	err := s.withTxContext(ctx, func(tx *loggedTx) error {
		for _, assignee := range assignees {
			var current int
			err := tx.QueryRowContext(ctx, `
				SELECT 1 FROM meeting_action_items
				WHERE message_id = ? AND ordinal = ? AND title = ?
				  AND assignee_email = '' AND assignee_name = ''`,
				assignee.MessageID, assignee.Ordinal, assignee.ActionTitle).Scan(&current)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return fmt.Errorf("check meeting action item: %w", err)
			}
			var participant sql.NullInt64
			if assignee.ParticipantID > 0 {
				var exists int
				err := tx.QueryRowContext(ctx, `SELECT 1 FROM participants WHERE id = ?`,
					assignee.ParticipantID).Scan(&exists)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("check meeting assignee participant: %w", err)
				}
				participant = sql.NullInt64{Int64: assignee.ParticipantID, Valid: err == nil}
			}
			probabilities := assignee.Probabilities
			if probabilities == nil {
				probabilities = map[string]float64{}
			}
			encoded, err := json.Marshal(probabilities, json.Deterministic(true))
			if err != nil {
				return fmt.Errorf("encode meeting assignee probabilities: %w", err)
			}
			result, err := tx.ExecContext(ctx, `
				INSERT INTO meeting_action_assignees (
					message_id, ordinal, action_title, choice, assignee_participant_id,
					confidence, probabilities_json, provenance, model, judged_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, 'inferred', ?, `+s.dialect.Now()+`)
				ON CONFLICT (message_id, ordinal) DO UPDATE SET
					action_title = excluded.action_title,
					choice = excluded.choice,
					assignee_participant_id = excluded.assignee_participant_id,
					confidence = excluded.confidence,
					probabilities_json = excluded.probabilities_json,
					model = excluded.model,
					judged_at = excluded.judged_at
				WHERE meeting_action_assignees.provenance = 'inferred'`,
				assignee.MessageID, assignee.Ordinal, assignee.ActionTitle, assignee.Choice, participant,
				assignee.Confidence, string(encoded), assignee.Model)
			if err != nil {
				return fmt.Errorf("write meeting action assignee: %w", err)
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("count meeting action assignee write: %w", err)
			}
			written += int(affected)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return written, nil
}

func validateMeetingActionAssignee(assignee MeetingActionAssignee) error {
	switch {
	case assignee.MessageID <= 0 || assignee.Ordinal < 0:
		return fmt.Errorf("%w: action item is required", ErrMeetingActionAssigneeInvalid)
	case assignee.Confidence < 0 || assignee.Confidence > 1:
		return fmt.Errorf("%w: confidence must be between 0 and 1", ErrMeetingActionAssigneeInvalid)
	case assignee.Choice == MeetingAssigneeChoiceAttendee && assignee.ParticipantID <= 0:
		return fmt.Errorf("%w: an attendee choice needs a participant", ErrMeetingActionAssigneeInvalid)
	case assignee.Choice != MeetingAssigneeChoiceAttendee && assignee.Choice != MeetingAssigneeChoiceOwner &&
		assignee.Choice != MeetingAssigneeChoiceNone:
		return fmt.Errorf("%w: unknown choice %q", ErrMeetingActionAssigneeInvalid, assignee.Choice)
	case assignee.Choice == MeetingAssigneeChoiceNone && assignee.ParticipantID != 0:
		return fmt.Errorf("%w: no assignee names no participant", ErrMeetingActionAssigneeInvalid)
	default:
		return nil
	}
}
