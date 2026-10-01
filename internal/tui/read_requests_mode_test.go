package tui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/query"
)

func TestEmailDetailSurvivesModeRoundTrip(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	engine := newMockEngine(MockConfig{})
	engine.GetMessageFunc = func(ctx context.Context, id int64) (*query.MessageDetail, error) {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return &query.MessageDetail{ID: id, Subject: "hello"}, nil
		}
	}
	model := New(engine, Options{Context: t.Context()})
	defer model.Close()
	model.width, model.height = 100, 40
	model.level = levelMessageList
	model.messages = []query.MessageSummary{{ID: 7, Subject: "hello"}}
	model, cmd := sendKey(t, model, keyEnter())
	require.NotNil(t, cmd)
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	<-started
	model, _ = sendKey(t, model, key('m'))
	close(release)
	model = sendMsg(t, model, <-result)
	model, _ = sendKey(t, model, key('m'))
	assert.Equal(t, modeEmail, model.mode)
	assert.Equal(t, levelMessageDetail, model.level)
	require.NotNil(t, model.messageDetail, "detail requested before the mode round trip is lost")
}
func TestMeetingDetailSurvivesModeRoundTrip(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	engine := newMockEngine(MockConfig{})
	engine.GetMessageFunc = func(ctx context.Context, id int64) (*query.MessageDetail, error) {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return &query.MessageDetail{ID: id, Subject: "standup"}, nil
		}
	}
	model := New(engine, Options{Context: t.Context()})
	defer model.Close()
	model.width, model.height = 100, 40
	model.mode = modeMeetings
	model.meetingState.initialized = true
	model.meetingState.messages = []query.MessageSummary{{ID: 9, Subject: "standup"}}
	model, cmd := sendKey(t, model, keyEnter())
	require.NotNil(t, cmd)
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "detail never started")
	}
	model, _ = sendKey(t, model, key('m'))
	close(release)
	model = sendMsg(t, model, <-result)
	model, _ = sendKey(t, model, key('m'))
	assert.Equal(t, modeMeetings, model.mode)
	assert.Equal(t, meetingLevelDetail, model.meetingState.level)
	require.NotNil(t, model.meetingState.detail, "meeting detail requested before the mode round trip is lost")
}

func TestMeetingReadsRetainWorkspaceOwnerWhilePeopleActive(t *testing.T) {
	for _, kind := range []string{"list", "search", "detail"} {
		t.Run(kind, func(t *testing.T) {
			messages := []query.MessageSummary{{ID: 9, Subject: "Example meeting"}}
			detail := &query.MessageDetail{ID: 9, Subject: "Example meeting", BodyText: "meeting needle"}
			model := New(newMockEngine(MockConfig{Messages: messages, Detail: detail}), Options{
				Context: t.Context(), PeopleBackend: &fakePeopleBackend{},
			})
			defer model.Close()
			model.width, model.height = 100, 40
			model.mode = modeMeetings
			model.meetingState.initialized = true
			model.meetingState.requestID = 1
			model.meetingState.searchRequestID = 1
			model.meetingState.detailRequestID = 1
			var cmd tea.Cmd
			switch kind {
			case "list":
				model.meetingState.listLoading = true
				cmd = model.loadMeetingMessages()
			case "search":
				model.meetingState.searchLoading = true
				cmd = model.loadMeetingSearch("meeting", 0, false)
			case "detail":
				model.meetingState.level = meetingLevelDetail
				model.meetingState.detailLoading = true
				model.meetingState.detailSearchQuery = "needle"
				cmd = model.loadMeetingDetail(9)
			}
			model, _ = sendKey(t, model, key('m'))
			require.Equal(t, modePeople, model.mode)
			// Matching request IDs must not cause a completion to overwrite
			// the contact reader while the Meetings workspace is parked.
			model.meetingState.requestID = 1
			model.meetingState.searchRequestID = 1
			model.meetingState.detailRequestID = 1
			model.meetingState.detail = &query.MessageDetail{ID: 42, Subject: "Contact meeting"}
			peopleReader := model.meetingState
			model = sendMsg(t, model, cmd())
			assert.Equal(t, peopleReader, model.meetingState)
			model = cycleToMode(t, model, modeMeetings)
			if kind == "detail" {
				require.NotNil(t, model.meetingState.detail)
				assert.Equal(t, int64(9), model.meetingState.detail.ID)
				assert.NotEmpty(t, model.meetingState.detailSearchMatches)
			} else {
				assert.Equal(t, messages, model.meetingState.messages)
			}
			assert.False(t, model.meetingState.listLoading)
			assert.False(t, model.meetingState.searchLoading)
			assert.False(t, model.meetingState.detailLoading)
		})
	}
}
