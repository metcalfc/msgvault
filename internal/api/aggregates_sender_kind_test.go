package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

func TestAggregatesFilterSendersByCorrespondentKind(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	f := storetest.New(t)
	st := f.Store
	alerts := f.EnsureParticipant("alerts@example.com", "Example Alerts", "example.com")
	casey := f.EnsureParticipant("casey@example.com", "Casey Example", "example.com")
	for i, sender := range []int64{alerts, alerts, casey} {
		id, err := st.UpsertMessage(&store.Message{
			ConversationID: f.ConvID, SourceID: f.Source.ID, SourceMessageID: fmt.Sprintf("agg-%d", i),
			MessageType: "email", SenderID: sql.NullInt64{Int64: sender, Valid: true},
			SentAt: sql.NullTime{Time: time.Date(2026, 5, 1+i, 9, 0, 0, 0, time.UTC), Valid: true},
		})
		require.NoError(err)
		require.NoError(st.ReplaceMessageRecipients(id, "from", []int64{sender}, []string{""}))
	}
	_, err := st.WriteDerivedCorrespondentKindsContext(t.Context(), []store.DerivedCorrespondentKind{{
		ParticipantID: alerts, Source: correspondentkind.SourceRule, Kind: correspondentkind.Automated,
	}})
	require.NoError(err)
	srv := NewServerWithOptions(ServerOptions{
		Config: &config.Config{Server: config.ServerConfig{APIPort: 8080}},
		Store:  st, Engine: query.NewSQLiteEngine(st.DB()), Logger: testLogger(),
	})
	get := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/aggregates?"+query, nil))
		return w
	}
	keys := func(w *httptest.ResponseRecorder) []string {
		require.Equal(http.StatusOK, w.Code, w.Body.String())
		var response AggregateResponse
		require.NoError(json.NewDecoder(w.Body).Decode(&response))
		result := []string{}
		for _, row := range response.Rows {
			result = append(result, row.Key)
		}
		return result
	}

	assert.ElementsMatch([]string{"alerts@example.com", "casey@example.com"}, keys(get("view_type=senders")))
	assert.Equal([]string{"alerts@example.com"}, keys(get("view_type=senders&sender_kind=automated")))
	assert.Empty(keys(get("view_type=senders&sender_kind=mailing_list")))
	for _, bad := range []string{"view_type=senders&sender_kind=person", "view_type=senders&sender_kind=robot",
		"view_type=domains&sender_kind=automated"} {
		w := get(bad)
		assert.Equal(http.StatusBadRequest, w.Code, bad)
		assert.Contains(w.Body.String(), "invalid_sender_kind", bad)
	}
}
