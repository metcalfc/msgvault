package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/query"
)

// This catches a message detail that drops the stored conversation type, which
// the Web UI needs to key fallback-typed chat messages ("chat", "text", "") by
// their conversation. Both the query-engine and the store detail paths serve it.
func TestGetMessageReturnsConversationType(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		messageType      string
		conversationType string
	}{
		{messageType: "email", conversationType: "email_thread"},
		{messageType: "chat", conversationType: "direct_chat"},
	} {
		st, _, ids := seedConversationStore(t, tc.messageType, 1)
		cfg := &config.Config{Server: config.ServerConfig{APIPort: 8080}}
		storeServer := NewServer(cfg, st, nil, testLogger())
		engineServer := NewServerWithOptions(ServerOptions{
			Config: cfg, Store: st, Engine: query.NewSQLiteEngine(st.DB()), Logger: testLogger(),
		})
		for name, srv := range map[string]*Server{"store": storeServer, "engine": engineServer} {
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", ids[0]), nil)
			w := httptest.NewRecorder()
			srv.Router().ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code, "%s %s: %s", name, tc.messageType, w.Body.String())
			var detail MessageDetail
			require.NoError(t, json.NewDecoder(w.Body).Decode(&detail))
			assert.Equal(t, tc.messageType, detail.MessageType, "%s path", name)
			assert.Equal(t, tc.conversationType, detail.ConversationType, "%s path", name)
		}
	}
}
