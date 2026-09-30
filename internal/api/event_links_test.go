package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
)

func TestCalendarEventDetailsCarryStoredProviderLinks(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	st, conversationID, ids := seedConversationStore(t, "calendar_event", 2)
	require.NoError(st.SetMessageMetadata(ids[0], sql.NullString{Valid: true,
		String: `{"html_link":"https://www.google.com/calendar/event?eid=abc",` +
			`"hangout_link":"https://meet.google.com/abc-defg-hij"}`}))
	srv := NewServer(&config.Config{Server: config.ServerConfig{APIPort: 8080}}, st, nil, testLogger())
	want := &EventLinks{
		JoinURL:     "https://meet.google.com/abc-defg-hij",
		CalendarURL: "https://www.google.com/calendar/event?eid=abc",
	}

	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/conversations/%d?anchor=%d&before=1&after=1", conversationID, ids[0]), nil))
	require.Equal(http.StatusOK, w.Code, "body: %s", w.Body.String())
	var conversation ConversationResponse
	require.NoError(json.NewDecoder(w.Body).Decode(&conversation))
	require.Len(conversation.Messages, 2)
	assert.Equal(want, conversation.Messages[0].EventLinks)
	assert.Nil(conversation.Messages[1].EventLinks, "an event without stored links has none")

	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/messages/%d", ids[0]), nil))
	require.Equal(http.StatusOK, w.Code, "body: %s", w.Body.String())
	var detail MessageDetail
	require.NoError(json.NewDecoder(w.Body).Decode(&detail))
	assert.Equal(want, detail.EventLinks)
}

func TestEmailDetailsNeverCarryEventLinks(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	st, _, ids := seedConversationStore(t, "email", 1)
	require.NoError(st.SetMessageMetadata(ids[0], sql.NullString{Valid: true,
		String: `{"hangout_link":"https://meet.google.com/abc-defg-hij"}`}))
	srv := NewServer(&config.Config{Server: config.ServerConfig{APIPort: 8080}}, st, nil, testLogger())

	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/messages/%d", ids[0]), nil))
	require.Equal(http.StatusOK, w.Code, "body: %s", w.Body.String())
	var raw map[string]any
	require.NoError(json.NewDecoder(w.Body).Decode(&raw))
	assert.NotContains(raw, "event_links")
}
