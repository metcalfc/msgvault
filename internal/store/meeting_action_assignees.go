package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
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
	// Fingerprint hashes the judgment inputs; store it with the result.
	Fingerprint string
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
	// Revision is the meeting revision read with the candidate; store it
	// with each result.
	Revision string
}

// MeetingActionAssignee is one inferred assignee to store.
type MeetingActionAssignee struct {
	MessageID   int64
	Ordinal     int
	ActionTitle string
	// Fingerprint is the MeetingAssigneeAction fingerprint the judgment read.
	Fingerprint string
	// MeetingRevision is the MeetingAssigneeCandidate revision it was read at.
	MeetingRevision string
	Choice          string
	ParticipantID   int64
	Confidence      float64
	Probabilities   map[string]float64
	Model           string
}

// meetingRevisionSQL is a meeting's revision for assignee judging: its
// projection content hash (items, source participants) and its content
// change stamp (title and other message content). Aliases m and md.
const meetingRevisionSQL = `(COALESCE(md.content_hash, '') || '|' ||
	COALESCE(CAST(m.content_changed_at AS TEXT), ''))`

// MeetingActionAssigneeCandidatesContext lists meeting transcripts, newest
// first, with unassigned action items that were never judged or whose
// meeting changed since they were judged, with their attendees. An item
// whose meeting changed but whose own inputs (its fingerprint) did not is
// marked current instead of returned, so an unchanged archive loads no
// meetings. limit caps the meetings enumerated; zero means no cap. examined
// counts the meetings enumerated, including those found current.
func (s *Store) MeetingActionAssigneeCandidatesContext(
	ctx context.Context, limit int,
) (candidates []MeetingAssigneeCandidate, examined int, err error) {
	err = s.withTxContext(ctx, func(tx *loggedTx) error {
		query := `
			SELECT m.id, COALESCE(m.subject, ''), ` + meetingRevisionSQL + `
			FROM messages m
			LEFT JOIN meeting_details md ON md.message_id = m.id
			WHERE m.message_type = ? AND m.deleted_at IS NULL
			  AND EXISTS (
				SELECT 1 FROM meeting_action_items a
				WHERE a.message_id = m.id AND a.assignee_email = '' AND a.assignee_name = ''
				  AND NOT EXISTS (
					SELECT 1 FROM meeting_action_assignees x
					WHERE x.message_id = a.message_id AND x.ordinal = a.ordinal
					  AND (x.provenance = 'user' OR x.meeting_revision = ` + meetingRevisionSQL + `)))
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
		var meetings []MeetingAssigneeCandidate
		for rows.Next() {
			var candidate MeetingAssigneeCandidate
			if err := rows.Scan(&candidate.MessageID, &candidate.Title, &candidate.Revision); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan meeting assignee candidate: %w", err)
			}
			meetings = append(meetings, candidate)
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
		examined = len(meetings)
		for i := range meetings {
			candidate := meetings[i]
			if err := meetingAssigneeAttendeesTx(ctx, tx, &candidate, owners); err != nil {
				return err
			}
			if err := meetingAssigneeActionsTx(ctx, tx, &candidate); err != nil {
				return err
			}
			if len(candidate.Actions) > 0 {
				candidates = append(candidates, candidate)
			}
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return candidates, examined, nil
}

// meetingAssigneeActionsTx keeps the meeting's unassigned items whose inputs
// changed since they were judged, or that were never judged. It needs the
// attendees read first: they are part of every item's fingerprint.
func meetingAssigneeActionsTx(ctx context.Context, tx *loggedTx, candidate *MeetingAssigneeCandidate) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT a.ordinal, a.title, a.description, x.input_fingerprint
		FROM meeting_action_items a
		LEFT JOIN meeting_action_assignees x
		  ON x.message_id = a.message_id AND x.ordinal = a.ordinal
		WHERE a.message_id = ? AND a.assignee_email = '' AND a.assignee_name = ''
		  AND (x.provenance IS NULL OR (x.provenance <> 'user' AND x.meeting_revision <> ?))
		ORDER BY a.ordinal`, candidate.MessageID, candidate.Revision)
	if err != nil {
		return fmt.Errorf("read meeting assignee actions: %w", err)
	}
	var unchanged []int
	for rows.Next() {
		var action MeetingAssigneeAction
		var judged sql.NullString
		if err := rows.Scan(&action.Ordinal, &action.Title, &action.Description, &judged); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan meeting assignee action: %w", err)
		}
		action.Fingerprint = meetingAssigneeFingerprint(*candidate, action)
		if judged.Valid && judged.String == action.Fingerprint {
			unchanged = append(unchanged, action.Ordinal)
			continue
		}
		if len(candidate.Actions) < meetingAssigneeCandidateActions {
			candidate.Actions = append(candidate.Actions, action)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate meeting assignee actions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close meeting assignee actions: %w", err)
	}
	// The meeting changed but these items' inputs did not: their judgment
	// stands for the new revision.
	for _, ordinal := range unchanged {
		if _, err := tx.ExecContext(ctx, `
			UPDATE meeting_action_assignees SET meeting_revision = ?
			WHERE message_id = ? AND ordinal = ? AND provenance = 'inferred'`,
			candidate.Revision, candidate.MessageID, ordinal); err != nil {
			return fmt.Errorf("mark meeting assignee current: %w", err)
		}
	}
	return nil
}

// meetingAssigneeFingerprint hashes every input a judgment of the item reads:
// the meeting title, the attendees and their labels, the owner, and the item
// title and description.
func meetingAssigneeFingerprint(candidate MeetingAssigneeCandidate, action MeetingAssigneeAction) string {
	hash := sha256.New()
	write := func(parts ...string) {
		for _, part := range parts {
			_, _ = hash.Write([]byte(strconv.Itoa(len(part))))
			_, _ = hash.Write([]byte{':'})
			_, _ = hash.Write([]byte(part))
		}
	}
	write("v1", candidate.Title, strconv.FormatInt(candidate.OwnerParticipantID, 10))
	for _, attendee := range candidate.Attendees {
		write(strconv.FormatInt(attendee.ParticipantID, 10), attendee.Label)
	}
	write("item", action.Title, action.Description)
	return hex.EncodeToString(hash.Sum(nil))
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
					message_id, ordinal, action_title, input_fingerprint, meeting_revision, choice, assignee_participant_id,
					confidence, probabilities_json, provenance, model, judged_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'inferred', ?, `+s.dialect.Now()+`)
				ON CONFLICT (message_id, ordinal) DO UPDATE SET
					action_title = excluded.action_title,
					input_fingerprint = excluded.input_fingerprint,
					meeting_revision = excluded.meeting_revision,
					choice = excluded.choice,
					assignee_participant_id = excluded.assignee_participant_id,
					confidence = excluded.confidence,
					probabilities_json = excluded.probabilities_json,
					model = excluded.model,
					judged_at = excluded.judged_at
				WHERE meeting_action_assignees.provenance = 'inferred'`,
				assignee.MessageID, assignee.Ordinal, assignee.ActionTitle, assignee.Fingerprint, assignee.MeetingRevision,
				assignee.Choice, participant,
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
