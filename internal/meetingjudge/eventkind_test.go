package meetingjudge_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/gcal"
	"go.kenn.io/msgvault/internal/meetingjudge"
	"go.kenn.io/msgvault/internal/meetingweight"
	"go.kenn.io/msgvault/internal/store"
)

// eventKindByTitle answers by the title of the series each question asks
// about.
func eventKindByTitle(questionID string, state map[string]any) (string, map[string]float64) {
	events := state["events"].([]any)
	index := strings.TrimPrefix(questionID, "event_kind_")
	for i, event := range events {
		if meetingjudge.EventKindQuestionID(i) != "event_kind_"+index {
			continue
		}
		switch event.(map[string]any)["title"] {
		case "Weekly sync":
			return string(meetingweight.KindOneOnOne), map[string]float64{"one_on_one": 0.91, "small_working_meeting": 0.09}
		case "Company all hands":
			return string(meetingweight.KindLargeGroupOrAllHands),
				map[string]float64{"large_group_or_all_hands": 0.55, "small_working_meeting": 0.45}
		case "Vendor webinar":
			return string(meetingweight.KindExternalWebinar), map[string]float64{"external_webinar_or_marketing": 0.8}
		}
	}
	return string(meetingweight.KindSmallWorkingMeeting), map[string]float64{"small_working_meeting": 0.7}
}

func TestEventKindsAskEachSeriesOnceAndWeighMeetings(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := newStore(t)
	weekly := calendarEvent("weekly", "Weekly sync", 30, "casey@example.com")
	weekly.Recurrence = []string{"RRULE:FREQ=WEEKLY"}
	moved := calendarEvent("weekly_20260511", "Weekly sync", 30, "casey@example.com")
	moved.RecurringEventID = "weekly"
	moved.OriginalStartTime = gcal.EventDateTime{DateTime: eventStart.AddDate(0, 0, 7)}
	moved.Start = gcal.EventDateTime{DateTime: eventStart.AddDate(0, 0, 8)}
	moved.End = gcal.EventDateTime{DateTime: eventStart.AddDate(0, 0, 8).Add(30 * time.Minute)}
	allHands := calendarEvent("allhands", "Company all hands", 60, manyAttendees(20, "example.com")...)
	webinar := calendarEvent("webinar", "Vendor webinar", 45, "host@example.org", "casey@example.com")
	outOfOffice := calendarEvent("ooo", "Out of office", 480, "casey@example.com")
	outOfOffice.EventType = "outOfOffice"
	syncCalendar(t, st, weekly, moved, allHands, webinar, outOfOffice)

	fake := &fakeJev{answer: eventKindByTitle}
	server := fake.server(t)
	service, cfg := jevService(t, server.URL, st)
	grantConsent(t, st, cfg, meetingjudge.EventKindFeature())
	before, err := st.MeetingWeightRevisionContext(t.Context())
	require.NoError(err)

	report, err := meetingjudge.RunEventKinds(t.Context(), st, meetingjudge.EventKindOptions{Judge: service})
	require.NoError(err)
	assert.Equal(4, report.Candidates, "the recurring series is one candidate")
	assert.Equal(1, report.NotMeetings, "the out-of-office block is recorded without asking")
	assert.Equal(1, report.Requests)
	assert.Equal(3, report.Judged)
	assert.Equal(2, report.Confident)
	assert.Empty(report.Skipped)

	requests := fake.requests()
	require.Len(requests, 1)
	events := requests[0]["state"].(map[string]any)["events"].([]any)
	require.Len(events, 3)
	titles := []string{}
	allowed := map[string]bool{}
	for _, field := range meetingjudge.EventKindFeature().StateFields {
		allowed[strings.TrimPrefix(field, "events[].")] = true
	}
	for _, event := range events {
		fields := event.(map[string]any)
		titles = append(titles, fields["title"].(string))
		for key := range fields {
			assert.True(allowed[key], "state field %s is disclosed", key)
		}
		if fields["title"] == "Weekly sync" {
			assert.Equal(true, fields["recurring"])
			assert.InDelta(2.0, fields["occurrences"], 1e-9)
			assert.InDelta(30.0, fields["duration_minutes"], 1e-9)
			assert.Equal(true, fields["organized_by_owner"])
		}
		if fields["title"] == "Vendor webinar" {
			assert.InDelta(3.0, fields["attendee_count"], 1e-9)
			assert.InDelta(1.0, fields["external_attendee_count"], 1e-9)
		}
	}
	assert.ElementsMatch([]string{"Weekly sync", "Company all hands", "Vendor webinar"}, titles)
	raw := fake.rawRequests()[0]
	assert.NotContains(raw, "@", "no address leaves the machine")
	assert.NotContains(raw, "Owner Example", "no owner name leaves the machine")
	assert.NotContains(raw, "private notes", "no description leaves the machine")

	after, err := st.MeetingWeightRevisionContext(t.Context())
	require.NoError(err)
	assert.Greater(after, before, "new kinds move the meeting weight revision")

	weights := map[string]float64{}
	rows, err := st.MeetingWeightExportRowsContext(t.Context())
	require.NoError(err)
	subjects := subjectsByID(t, st)
	for _, row := range rows {
		weights[subjects[row.MessageID]] = row.Weight
	}
	_, weeklyListed := weights["Weekly sync"]
	assert.False(weeklyListed, "a confident one-on-one weighs 1")
	assert.InDelta(0.0, weights["Vendor webinar"], 1e-9, "a confident webinar is not a meeting")
	assert.InDelta(10.0/21.0, weights["Company all hands"], 1e-9,
		"below 0.60 the attendee-count weight (the owner and 20 others) stays")
	assert.InDelta(0.0, weights["Out of office"], 1e-9)

	again, err := meetingjudge.RunEventKinds(t.Context(), st, meetingjudge.EventKindOptions{Judge: service})
	require.NoError(err)
	assert.Zero(again.Candidates, "every series is asked once")
	assert.Len(fake.requests(), 1)
}

func TestEventKindsWithoutConsentSendNothing(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := newStore(t)
	syncCalendar(t, st, calendarEvent("sync", "Planning", 30, "casey@example.com"))
	fake := &fakeJev{answer: eventKindByTitle}
	server := fake.server(t)
	service, _ := jevService(t, server.URL, st)

	report, err := meetingjudge.RunEventKinds(t.Context(), st, meetingjudge.EventKindOptions{Judge: service})
	require.NoError(err)
	assert.Equal("consent_required", report.Skipped)
	assert.Zero(report.Judged)
	assert.Empty(fake.requests())
	candidates, err := st.CalendarEventKindCandidatesContext(t.Context(), 0)
	require.NoError(err)
	assert.Len(candidates, 1, "an unjudged series is asked again once consent is given")
}

func TestEventKindsWithoutJevRecordOnlyNonMeetings(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := newStore(t)
	focus := calendarEvent("focus", "Focus", 120)
	focus.EventType = "focusTime"
	syncCalendar(t, st, focus, calendarEvent("sync", "Planning", 30, "casey@example.com"))

	report, err := meetingjudge.RunEventKinds(t.Context(), st, meetingjudge.EventKindOptions{})
	require.NoError(err)
	assert.Equal(2, report.Candidates)
	assert.Equal(1, report.NotMeetings)
	assert.Zero(report.Requests)
	written, err := st.WriteCalendarEventKindsContext(t.Context(), []store.CalendarEventKind{{
		ConversationID: 1, Kind: "one_on_one", Source: "user", Confidence: 1,
	}})
	require.ErrorIs(err, store.ErrCalendarEventKindInvalid)
	assert.Zero(written)
}

func subjectsByID(t *testing.T, st *store.Store) map[int64]string {
	t.Helper()
	rows, err := st.DB().QueryContext(t.Context(), `SELECT id, COALESCE(subject, '') FROM messages`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	subjects := map[int64]string{}
	for rows.Next() {
		var id int64
		var subject string
		require.NoError(t, rows.Scan(&id, &subject))
		subjects[id] = subject
	}
	require.NoError(t, rows.Err())
	return subjects
}
