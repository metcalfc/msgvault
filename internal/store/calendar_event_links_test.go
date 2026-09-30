package store_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

func TestCalendarEventLinksReadStoredProviderLinks(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := storetest.New(t)

	event := func(sourceMessageID, metadata string) int64 {
		message := f.NewMessage().WithSourceMessageID(sourceMessageID).Build()
		message.MessageType = "calendar_event"
		id, err := f.Store.UpsertMessage(message)
		require.NoError(err)
		if metadata != "" {
			require.NoError(f.Store.SetMessageMetadata(id, sql.NullString{String: metadata, Valid: true}))
		}
		return id
	}
	both := event("event-both", `{"all_day":false,"html_link":"https://www.google.com/calendar/event?eid=abc",`+
		`"hangout_link":"https://meet.google.com/abc-defg-hij"}`)
	calendarOnly := event("event-calendar-only", `{"html_link":"https://www.google.com/calendar/event?eid=def"}`)
	unsafe := event("event-unsafe", `{"html_link":"javascript:alert(1)",`+
		`"hangout_link":"https://user:secret@meet.example.com/x"}`)
	noMetadata := event("event-none", "")
	email := f.NewMessage().WithSourceMessageID("email-with-links").Create(t, f.Store)
	require.NoError(f.Store.SetMessageMetadata(email, sql.NullString{
		String: `{"hangout_link":"https://meet.google.com/zzz"}`, Valid: true,
	}))

	links, err := f.Store.CalendarEventLinksContext(t.Context(),
		[]int64{both, calendarOnly, unsafe, noMetadata, email, 0})
	require.NoError(err)
	assert.Equal(map[int64]store.CalendarEventLinks{
		both: {
			JoinURL:     "https://meet.google.com/abc-defg-hij",
			CalendarURL: "https://www.google.com/calendar/event?eid=abc",
		},
		calendarOnly: {CalendarURL: "https://www.google.com/calendar/event?eid=def"},
	}, links)

	empty, err := f.Store.CalendarEventLinksContext(t.Context(), nil)
	require.NoError(err)
	assert.Empty(empty)
}
