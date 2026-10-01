package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentityMutationCancellationWhileWaitingForDatabase(t *testing.T) {
	for _, operation := range []string{"links", "unlinks"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			srv, st := newIdentityLinkTestServer(t)
			a := st.mustParticipant(t, "first@example.test", "First", "example.test")
			b := st.mustParticipant(t, "second@example.test", "Second", "example.test")
			if operation == "unlinks" {
				_, err := st.LinkParticipantsContext(t.Context(), a, b)
				require.NoError(t, err)
			}
			db := st.DB()
			db.SetMaxOpenConns(1)
			conn, err := db.Conn(t.Context())
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })
			initialWaits := db.Stats().WaitCount
			raw, err := json.Marshal(IdentityLinkRequest{ParticipantA: a, ParticipantB: b})
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/identity/"+operation, bytes.NewReader(raw)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				srv.Router().ServeHTTP(response, req)
			}()
			require.Eventually(t, func() bool { return db.Stats().WaitCount > initialWaits }, 2*time.Second, 10*time.Millisecond,
				"identity mutation must reach the held database connection")
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				require.NoError(t, conn.Close())
				<-done
				require.FailNow(t, "cancelled identity mutation remained blocked on the database connection")
			}
			require.NoError(t, conn.Close())
			assert.Equal(t, http.StatusServiceUnavailable, response.Code)
			var body ErrorResponse
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
			assert.Equal(t, "query_canceled", body.Error)
			assert.Zero(t, st.refreshCalls, "cancelled mutations must not refresh the cache")
			edges, err := st.ClusterEdges(a)
			require.NoError(t, err)
			if operation == "unlinks" {
				assert.Len(t, edges, 1, "cancelled unlink must preserve the existing edge")
			} else {
				assert.Empty(t, edges, "cancelled link must not persist an edge")
			}
		})
	}
}
