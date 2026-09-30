package store

import (
	"context"
	"database/sql"
	"fmt"

	"go.kenn.io/msgvault/internal/meetingweight"
)

// meetingWeightRevisionKey tracks changes to what calendar event weights
// read besides the messages themselves (event kind judgments), so the
// analytics cache can tell when its exported weights are stale.
const meetingWeightRevisionKey = "meeting_weight_revision"

// MeetingWeightExportRow is one calendar event whose meeting weight differs
// from 1. Events not listed weigh 1.
type MeetingWeightExportRow struct {
	MessageID int64
	Weight    float64
}

// MeetingWeightRevisionContext reads the meeting weight revision.
func (s *Store) MeetingWeightRevisionContext(ctx context.Context) (int64, error) {
	return readArchiveMetadataRevisionContext(ctx, s.db, meetingWeightRevisionKey, "meeting weight")
}

func (s *Store) bumpMeetingWeightRevisionTx(ctx context.Context, tx *loggedTx) error {
	if _, err := tx.ExecContext(ctx, s.dialect.InsertOrIgnore(
		`INSERT OR IGNORE INTO archive_metadata (key, value) VALUES (?, '0')`),
		meetingWeightRevisionKey); err != nil {
		return fmt.Errorf("seed meeting weight revision: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE archive_metadata SET value = CAST(CAST(value AS INTEGER) + 1 AS TEXT)
		 WHERE key = ?`, meetingWeightRevisionKey); err != nil {
		return fmt.Errorf("bump meeting weight revision: %w", err)
	}
	return nil
}

// MeetingWeightExportRowsContext computes the meeting weight of every live
// calendar event (see meetingweight.Weight) and returns those that differ
// from 1. The attendee count comes from the event metadata, or from the
// event's attendee recipients for events synced before sync recorded it.
// Its series' Jev event kind, when there is one, is the judgment.
func (s *Store) MeetingWeightExportRowsContext(ctx context.Context) ([]MeetingWeightExportRow, error) {
	result := []MeetingWeightExportRow{}
	// An archive without calendar events has nothing to weigh; checking
	// first also keeps minimal legacy schemas without event columns working.
	var events int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE message_type = ?`,
		calendarEventMessageType).Scan(&events); err != nil {
		return nil, fmt.Errorf("count calendar events: %w", err)
	}
	if events == 0 {
		return result, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.metadata,
		       (SELECT COUNT(*) FROM message_recipients r
		         WHERE r.message_id = m.id AND r.recipient_type = 'to'),
		       k.kind, k.confidence
		FROM messages m
		LEFT JOIN calendar_event_kinds k
		  ON k.conversation_id = m.conversation_id AND k.source = 'jev'
		WHERE m.message_type = ? AND m.deleted_at IS NULL
		ORDER BY m.id`, calendarEventMessageType)
	if err != nil {
		return nil, fmt.Errorf("query meeting weights: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, recipients int64
		var metadata, kind sql.NullString
		var confidence sql.NullFloat64
		if err := rows.Scan(&id, &metadata, &recipients, &kind, &confidence); err != nil {
			return nil, fmt.Errorf("scan meeting weight: %w", err)
		}
		parsed := meetingweight.ParseMetadata(metadata.String)
		attendees := parsed.AttendeeCount
		if attendees <= 0 {
			attendees = int(recipients)
		}
		var judgment *meetingweight.Judgment
		if kind.Valid && confidence.Valid {
			judgment = &meetingweight.Judgment{Kind: meetingweight.Kind(kind.String), Confidence: confidence.Float64}
		}
		if weight := meetingweight.Weight(parsed, attendees, judgment); weight != 1 {
			result = append(result, MeetingWeightExportRow{MessageID: id, Weight: weight})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate meeting weights: %w", err)
	}
	return result, nil
}
