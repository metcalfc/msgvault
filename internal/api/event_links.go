package api

import (
	"context"

	"go.kenn.io/msgvault/internal/gcal"
	"go.kenn.io/msgvault/internal/store"
)

// EventLinks are a calendar event's provider links. Both are absolute https
// URLs; clients decide which hosts they will open.
type EventLinks struct {
	JoinURL     string `json:"join_url,omitempty" doc:"The event's video-meeting link (Google Calendar hangoutLink)."`
	CalendarURL string `json:"calendar_url,omitempty" doc:"The event's page in the provider's calendar (Google Calendar htmlLink)."`
}

// CalendarEventLinkStore reads the provider links stored with calendar
// events. It is optional: without it, message details carry no event links.
type CalendarEventLinkStore interface {
	CalendarEventLinksContext(ctx context.Context, ids []int64) (map[int64]store.CalendarEventLinks, error)
}

// attachEventLinks adds stored provider links to the calendar-event details.
// The links are an optional enrichment, so a read failure is logged and the
// details are served without them.
func (s *Server) attachEventLinks(ctx context.Context, details []MessageDetail) {
	links, ok := s.store.(CalendarEventLinkStore)
	if !ok {
		return
	}
	ids := make([]int64, 0, len(details))
	for _, detail := range details {
		if detail.MessageType == gcal.MessageTypeCalendarEvent {
			ids = append(ids, detail.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	stored, err := links.CalendarEventLinksContext(ctx, ids)
	if err != nil {
		s.logger.Warn("calendar event link lookup failed", "error", err)
		return
	}
	for i := range details {
		if link, ok := stored[details[i].ID]; ok {
			details[i].EventLinks = &EventLinks{JoinURL: link.JoinURL, CalendarURL: link.CalendarURL}
		}
	}
}
