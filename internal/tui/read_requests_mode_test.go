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
