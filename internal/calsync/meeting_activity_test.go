package calsync

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/msgvault/internal/gcal"
)

func TestFull_SkipsResourceAttendeesAndRecordsOwnerResponse(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	m := gcal.NewMockAPI()
	m.Calendars = []gcal.Calendar{{ID: "primary", Summary: "Work", AccessRole: "owner", Primary: true, TimeZone: "UTC"}}
	ev := timedEvent("e1", "Design review",
		gcal.Attendee{Email: testAccount, DisplayName: "Alice", ResponseStatus: "declined", Self: true},
		gcal.Attendee{Email: "bob@example.com", DisplayName: "Bob", ResponseStatus: "accepted"},
		gcal.Attendee{Email: "room-4a@example.com", DisplayName: "Room 4A", Resource: true},
		gcal.Attendee{Email: "c_1234@resource.calendar.google.com", DisplayName: "Projector"})
	m.FullEvents["primary"] = [][]gcal.Event{{ev}}
	s, st := newSyncer(t, m, Options{})

	_, err := s.Full(t.Context())
	require.NoError(err)
	src := primarySource(t, st)
	require.NotNil(src)
	row, ok := getMsg(t, st, src.ID, "e1")
	require.True(ok)

	assert.ElementsMatch([]string{testAccount, "bob@example.com"}, recipientEmails(t, st, row.id, "to"),
		"rooms and equipment never become attendees")
	body := bodyText(t, st, row.id)
	assert.NotContains(body, "Room 4A")
	assert.NotContains(body, "Projector")
	meta := parseMeta(t, row)
	assert.Equal("declined", meta["owner_response_status"])
	assert.InDelta(2.0, meta["attendee_count"], 1e-9)
}

func TestFull_MeetingWeightsLeaveOutEventsThatAreNotMeetings(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	bob := gcal.Attendee{Email: "bob@example.com", DisplayName: "Bob", ResponseStatus: "accepted"}
	declined := timedEvent("declined", "Vendor pitch",
		gcal.Attendee{Email: testAccount, ResponseStatus: "declined", Self: true}, bob)
	outOfOffice := timedEvent("ooo", "Out of office", bob)
	outOfOffice.EventType = "outOfOffice"
	focus := timedEvent("focus", "Focus time", bob)
	focus.EventType = "focusTime"
	working := timedEvent("working", "Home", bob)
	working.EventType = "workingLocation"
	free := timedEvent("free", "Optional hold", bob)
	free.Transparency = "transparent"
	ordinary := timedEvent("ordinary", "One on one", bob)
	large := timedEvent("large", "All hands")
	for i := range 20 {
		large.Attendees = append(large.Attendees, gcal.Attendee{Email: fmt.Sprintf("person%02d@example.com", i)})
	}

	m := gcal.NewMockAPI()
	m.Calendars = []gcal.Calendar{{ID: "primary", Summary: "Work", AccessRole: "owner", Primary: true, TimeZone: "UTC"}}
	m.FullEvents["primary"] = [][]gcal.Event{{declined, outOfOffice, focus, working, free, ordinary, large}}
	s, st := newSyncer(t, m, Options{})
	_, err := s.Full(t.Context())
	require.NoError(err)
	src := primarySource(t, st)
	require.NotNil(src)

	ids := map[string]int64{}
	for _, smid := range []string{"declined", "ooo", "focus", "working", "free", "ordinary", "large"} {
		row, ok := getMsg(t, st, src.ID, smid)
		require.True(ok, smid)
		ids[smid] = row.id
	}
	rows, err := st.MeetingWeightExportRowsContext(t.Context())
	require.NoError(err)
	weights := map[int64]float64{}
	for _, row := range rows {
		weights[row.MessageID] = row.Weight
	}
	for _, smid := range []string{"declined", "ooo", "focus", "working", "free"} {
		weight, listed := weights[ids[smid]]
		assert.True(listed, "%s is exported", smid)
		assert.InDelta(0.0, weight, 1e-9, "%s is not a meeting", smid)
	}
	_, listed := weights[ids["ordinary"]]
	assert.False(listed, "an ordinary meeting weighs 1 and is not exported")
	assert.InDelta(0.5, weights[ids["large"]], 1e-9, "twenty attendees count for half a meeting")
	assert.Len(rows, 6)
}
