package meetingjudge_test

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/meetingcontent"
	"go.kenn.io/msgvault/internal/meetingjudge"
	"go.kenn.io/msgvault/internal/store"
)

type meetingArchive struct {
	st     *store.Store
	owner  int64
	casey  int64
	jordan int64
}

// newMeetingArchive stores one imported meeting through the real persist
// and projection path: the owner organized it, Casey and Jordan attended,
// and three of its four action items have no source assignee.
func newMeetingArchive(t *testing.T) (*meetingArchive, int64) {
	t.Helper()
	st := newStore(t)
	source, err := st.GetOrCreateSource("meeting_import", ownerAddress)
	require.NoError(t, err)
	require.NoError(t, st.AddAccountIdentity(source.ID, ownerAddress, "manual"))
	owner, err := st.EnsureParticipant(ownerAddress, "Owner Example", "example.com")
	require.NoError(t, err)
	casey, err := st.EnsureParticipant("casey@example.com", "Casey Example", "example.com")
	require.NoError(t, err)
	jordan, err := st.EnsureParticipant("jordan.lee@example.net", "", "example.net")
	require.NoError(t, err)
	archive := &meetingArchive{st: st, owner: owner, casey: casey, jordan: jordan}
	raw := `{"summary_text":"Budget planning","action_items":[` +
		`{"source_id":"a0","title":"Casey to draft the budget","status":"open"},` +
		`{"source_id":"a1","title":"I will book the room","status":"open"},` +
		`{"source_id":"a2","title":"Somebody should pick a date","status":"open"},` +
		`{"source_id":"a3","title":"Send notes","assignee_name":"Jordan","assignee_email":"jordan.lee@example.net","status":"open"}]}`
	id, err := st.PersistMessage(&store.MessagePersistData{
		Message: &store.Message{
			SourceID: source.ID, SourceMessageID: "planning", MessageType: "meeting_transcript",
			SentAt:   sql.NullTime{Time: time.Date(2026, 5, 4, 15, 0, 0, 0, time.UTC), Valid: true},
			SenderID: sql.NullInt64{Int64: owner, Valid: true},
			Subject:  sql.NullString{String: "Budget planning", Valid: true},
		},
		Conversation: &store.ConversationPersistData{
			SourceConversationID: "planning", ConversationType: "meeting", Title: "Budget planning",
		},
		Recipients: []store.RecipientSet{{
			Type: "from", ParticipantIDs: []int64{owner}, DisplayNames: []string{"Owner Example"},
			EmailAddresses: []string{ownerAddress},
		}, {
			Type: "to", ParticipantIDs: []int64{casey, jordan, owner}, DisplayNames: []string{"", "", ""},
			EmailAddresses: []string{"casey@example.com", "jordan.lee@example.net", ownerAddress},
		}},
		RawMIME: []byte(raw), RawFormat: "meeting_json",
	})
	require.NoError(t, err)
	return archive, id
}

// assigneeByItem answers from each item's title, finding Casey's option
// among the attendee labels the request offered.
func assigneeByItem(questionID string, state map[string]any) (string, map[string]float64) {
	typed := asState[meetingjudge.AssigneeState](state)
	item := typed.ActionItems["item_"+strings.TrimPrefix(questionID, "assignee_")]
	caseyKey := ""
	for key, attendee := range typed.Attendees {
		if attendee.Label == "Casey Example" {
			caseyKey = key
		}
	}
	switch item.Title {
	case "Casey to draft the budget":
		return caseyKey, map[string]float64{caseyKey: 0.93, meetingjudge.OptionOwner: 0.04}
	case "I will book the room":
		return meetingjudge.OptionOwner, map[string]float64{meetingjudge.OptionOwner: 0.88}
	default:
		return caseyKey, map[string]float64{caseyKey: 0.55, meetingjudge.OptionNoneOrUnclear: 0.45}
	}
}

func assigneeService(t *testing.T, endpoint string, st *store.Store) *jev.Service {
	t.Helper()
	cfg := jev.DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = endpoint
	cfg.MeetingActionAssignee = jev.FeatureConfig{Enabled: true}
	service, err := jev.NewService(jev.ServiceOptions{
		Config:     func() (jev.Config, error) { return cfg, nil },
		Consents:   st,
		Ledger:     st,
		Credential: func(string, string) (string, bool, error) { return "fake-key", true, nil },
	})
	require.NoError(t, err)
	grantConsent(t, st, cfg, meetingjudge.AssigneeFeature())
	return service
}

func actionsByTitle(t *testing.T, st *store.Store, query store.MeetingActionsQuery) map[string]meetingcontent.ActionRow {
	t.Helper()
	page, err := st.ListMeetingActionsContext(t.Context(), query)
	require.NoError(t, err)
	rows := map[string]meetingcontent.ActionRow{}
	for _, row := range page.Rows {
		rows[row.Action.Title] = row
	}
	return rows
}

func TestAssigneesInferOwnersAndFilterByPerson(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	archive, meetingID := newMeetingArchive(t)
	st := archive.st
	fake := &fakeJev{answer: assigneeByItem}
	server := fake.server(t)
	service := assigneeService(t, server.URL, st)

	report, err := meetingjudge.RunAssignees(t.Context(), st, meetingjudge.AssigneeOptions{Judge: service})
	require.NoError(err)
	assert.Equal(1, report.Meetings)
	assert.Equal(3, report.Items, "the item with a source assignee is never asked about")
	assert.Equal(1, report.Requests)
	assert.Equal(1, report.Attendees)
	assert.Equal(1, report.Owner)
	assert.Equal(1, report.Unclear)
	assert.Empty(report.Skipped)

	requests := fake.requests()
	require.Len(requests, 1)
	state := asState[meetingjudge.AssigneeState](requests[0]["state"])
	assert.Equal(meetingjudge.MeetingState{Title: "Budget planning"}, state.Meeting)
	labels := []string{}
	for _, attendee := range state.Attendees {
		labels = append(labels, attendee.Label)
	}
	assert.ElementsMatch([]string{"Casey Example", "jordan.lee"}, labels,
		"attendees are offered by label; the owner is the owner option, never an attendee")
	assert.Len(state.ActionItems, 3)
	assert.Len(requests[0]["questions"], 3, "only the questions for the items sent")
	raw := fake.rawRequests()[0]
	assert.NotContains(raw, "@", "no address leaves the machine")
	assert.NotContains(raw, "Owner Example", "the owner's name never leaves the machine")
	assert.NotContains(raw, "Send notes")

	rows := actionsByTitle(t, st, store.MeetingActionsQuery{})
	casey := rows["Casey to draft the budget"].InferredAssignee
	require.NotNil(casey)
	require.NotNil(casey.ParticipantID)
	assert.Equal(archive.casey, *casey.ParticipantID)
	assert.Equal("Casey Example", casey.Label)
	assert.InDelta(0.93, casey.Confidence, 1e-9)
	assert.Equal("inferred", casey.Provenance)
	assert.False(casey.IsOwner)
	owner := rows["I will book the room"].InferredAssignee
	require.NotNil(owner)
	assert.True(owner.IsOwner)
	require.NotNil(owner.ParticipantID)
	assert.Equal(archive.owner, *owner.ParticipantID)
	assert.Nil(rows["Somebody should pick a date"].InferredAssignee, "below 0.80 nothing is inferred")
	assert.Nil(rows["Send notes"].InferredAssignee)

	caseyPerson, _, err := st.CreatePersonFromParticipantContext(t.Context(), archive.casey)
	require.NoError(err)
	jordanPerson, _, err := st.CreatePersonFromParticipantContext(t.Context(), archive.jordan)
	require.NoError(err)
	byCasey := actionsByTitle(t, st, store.MeetingActionsQuery{AssigneePersonID: caseyPerson.ID})
	assert.Len(byCasey, 1)
	require.Contains(byCasey, "Casey to draft the budget")
	require.NotNil(byCasey["Casey to draft the budget"].InferredAssignee.PersonID)
	assert.Equal(caseyPerson.ID, *byCasey["Casey to draft the budget"].InferredAssignee.PersonID)
	byJordan := actionsByTitle(t, st, store.MeetingActionsQuery{AssigneePersonID: jordanPerson.ID})
	assert.Len(byJordan, 1)
	assert.Contains(byJordan, "Send notes", "the source assignee address matches the person")

	written, err := st.WriteInferredMeetingActionAssigneesContext(t.Context(), []store.MeetingActionAssignee{{
		MessageID: meetingID, Ordinal: 3, ActionTitle: "Send notes",
		Choice: store.MeetingAssigneeChoiceAttendee, ParticipantID: archive.casey, Confidence: 0.99,
	}})
	require.NoError(err)
	assert.Zero(written, "a source assignee is never replaced")

	again, err := meetingjudge.RunAssignees(t.Context(), st, meetingjudge.AssigneeOptions{Judge: service})
	require.NoError(err)
	assert.Zero(again.Meetings, "judged items are not asked again")
	assert.Len(fake.requests(), 1)
}

func TestAssigneesNeverReplaceAUserAssignee(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	archive, meetingID := newMeetingArchive(t)
	st := archive.st
	_, err := st.DB().ExecContext(t.Context(), st.Rebind(`
		INSERT INTO meeting_action_assignees
			(message_id, ordinal, action_title, choice, assignee_participant_id, confidence, provenance)
		VALUES (?, 0, 'Casey to draft the budget', 'attendee', ?, 1, 'user')`), meetingID, archive.jordan)
	require.NoError(err)

	written, err := st.WriteInferredMeetingActionAssigneesContext(t.Context(), []store.MeetingActionAssignee{{
		MessageID: meetingID, Ordinal: 0, ActionTitle: "Casey to draft the budget",
		Choice: store.MeetingAssigneeChoiceAttendee, ParticipantID: archive.casey, Confidence: 0.95,
	}})
	require.NoError(err)
	assert.Zero(written)
	candidates, err := st.MeetingActionAssigneeCandidatesContext(t.Context(), 0)
	require.NoError(err)
	require.Len(candidates, 1)
	for _, action := range candidates[0].Actions {
		assert.NotEqual(0, action.Ordinal, "an item with a user assignee is not a candidate")
	}
	row := actionsByTitle(t, st, store.MeetingActionsQuery{})["Casey to draft the budget"]
	require.NotNil(row.InferredAssignee)
	assert.Equal("user", row.InferredAssignee.Provenance)
	require.NotNil(row.InferredAssignee.ParticipantID)
	assert.Equal(archive.jordan, *row.InferredAssignee.ParticipantID)
}

func TestAssigneesWithoutConsentSendNothing(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	archive, _ := newMeetingArchive(t)
	fake := &fakeJev{answer: assigneeByItem}
	server := fake.server(t)
	cfg := jev.DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = server.URL
	cfg.MeetingActionAssignee = jev.FeatureConfig{Enabled: true}
	service, err := jev.NewService(jev.ServiceOptions{
		Config:     func() (jev.Config, error) { return cfg, nil },
		Consents:   archive.st,
		Credential: func(string, string) (string, bool, error) { return "fake-key", true, nil },
	})
	require.NoError(err)

	report, err := meetingjudge.RunAssignees(t.Context(), archive.st, meetingjudge.AssigneeOptions{Judge: service})
	require.NoError(err)
	assert.Equal("consent_required", report.Skipped)
	assert.Empty(fake.requests())
	off, err := meetingjudge.RunAssignees(t.Context(), archive.st, meetingjudge.AssigneeOptions{})
	require.NoError(err)
	assert.Zero(off.Meetings, "without Jev nothing is read or written")
}
