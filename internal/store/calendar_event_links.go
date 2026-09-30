package store

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/url"
	"strings"
)

// calendarEventMessageType mirrors gcal.MessageTypeCalendarEvent; the store
// does not import the provider package.
const calendarEventMessageType = "calendar_event"

// CalendarEventLinks are the provider links stored with a calendar event:
// the video-meeting join link and the event's page in the provider's
// calendar. Each is an absolute https URL without credentials, or empty.
type CalendarEventLinks struct {
	JoinURL     string
	CalendarURL string
}

// CalendarEventLinksContext returns the stored links of the calendar-event
// messages among ids, keyed by message ID. Messages that are not calendar
// events, or that carry no usable link, are absent from the result.
func (s *Store) CalendarEventLinksContext(
	ctx context.Context, ids []int64,
) (map[int64]CalendarEventLinks, error) {
	result := make(map[int64]CalendarEventLinks)
	const chunkSize = 300 // below SQLite's older 999-placeholder limit
	for start := 0; start < len(ids); start += chunkSize {
		chunk := ids[start:min(start+chunkSize, len(ids))]
		args := make([]any, 0, len(chunk)+1)
		args = append(args, calendarEventMessageType)
		for _, id := range chunk {
			args = append(args, id)
		}
		rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT id,
			COALESCE(CAST(metadata AS TEXT), '')
			FROM messages WHERE message_type = ? AND id IN (%s)`,
			placeholders(len(chunk))), args...)
		if err != nil {
			return nil, fmt.Errorf("read calendar event links: %w", err)
		}
		for rows.Next() {
			var id int64
			var metadata string
			if err := rows.Scan(&id, &metadata); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan calendar event links: %w", err)
			}
			if links, ok := calendarEventLinksFromMetadata(metadata); ok {
				result[id] = links
			}
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("read calendar event links: %w", err)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("read calendar event links: %w", err)
		}
	}
	return result, nil
}

func calendarEventLinksFromMetadata(metadata string) (CalendarEventLinks, bool) {
	if strings.TrimSpace(metadata) == "" {
		return CalendarEventLinks{}, false
	}
	var stored struct {
		HTMLLink    string `json:"html_link"`
		HangoutLink string `json:"hangout_link"`
	}
	if err := json.Unmarshal([]byte(metadata), &stored); err != nil {
		return CalendarEventLinks{}, false
	}
	links := CalendarEventLinks{
		JoinURL:     httpsURL(stored.HangoutLink),
		CalendarURL: httpsURL(stored.HTMLLink),
	}
	return links, links.JoinURL != "" || links.CalendarURL != ""
}

// httpsURL returns raw when it is an absolute https URL with a host and no
// credentials, and "" otherwise. Clients still apply their own host rules.
func httpsURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	return trimmed
}
