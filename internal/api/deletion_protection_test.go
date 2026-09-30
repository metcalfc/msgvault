package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/deletion"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/query/querytest"
	"go.kenn.io/msgvault/internal/store"
)

// protectionStore answers deletion protection from a fixed table, the
// contract the daemon adapter fulfills with store.DeletionProtectionsContext.
type protectionStore struct {
	deletionMockStore

	protections map[int64]store.DeletionProtection
	blocked     map[int64]bool
	keepQuery   []int64
	keepMin     float64
}

func (s *protectionStore) DeletionProtectionsContext(
	_ context.Context, ids []int64,
) (map[int64]store.DeletionProtection, error) {
	result := map[int64]store.DeletionProtection{}
	for _, id := range ids {
		if protection, ok := s.protections[id]; ok {
			result[id] = protection
		}
	}
	return result, nil
}

func (s *protectionStore) MessageRemoteImagesBlockedContext(_ context.Context, id int64) (bool, error) {
	blocked, ok := s.blocked[id]
	if !ok {
		return false, sql.ErrNoRows
	}
	return blocked, nil
}

func (s *protectionStore) KeepCandidatesForSourceMessagesContext(
	_ context.Context, sourceID int64, ids []string, minKeep float64,
) ([]store.CleanupSuggestionRow, error) {
	rows := []store.CleanupSuggestionRow{}
	for i := range 60 {
		rows = append(rows, store.CleanupSuggestionRow{
			MessageID: int64(100 + i), KeepProbability: 0.9 - float64(i)/100,
			SourceMessageID: ids[0], Subject: "Photos", FromName: "Casey Example", FromEmail: "casey@example.net",
		})
	}
	s.keepQuery = append(s.keepQuery, sourceID)
	s.keepMin = minKeep
	return rows, nil
}

func TestGetDeletionListsPossiblyWorthKeepingMessages(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	st := &protectionStore{}
	st.getStatus = "pending"
	st.getManifest = &deletion.Manifest{
		ID: "batch-7", GmailIDs: []string{"gm-1", "gm-2"},
		Source: &deletion.SourceReference{ID: 42, Type: "gmail", Identifier: "user@example.com"},
	}
	srv := newDeletionTestServer(t, st, &querytest.MockEngine{})

	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/deletions/batch-7", nil))

	require.Equal(http.StatusOK, w.Code, w.Body.String())
	var detail DeletionManifestDetail
	require.NoError(json.Unmarshal(w.Body.Bytes(), &detail))
	assert.Equal([]int64{42}, st.keepQuery)
	assert.InDelta(0.50, st.keepMin, 1e-9)
	assert.Equal(60, detail.PossiblyWorthKeepingCount)
	require.Len(detail.PossiblyWorthKeeping, 50)
	assert.Equal(deletion.KeepCandidate{
		MessageID: 100, SourceMessageID: "gm-1", From: "Casey Example <casey@example.net>",
		Subject: "Photos", KeepProbability: 0.9,
	}, detail.PossiblyWorthKeeping[0])

	st.getManifest.Source = nil
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/deletions/batch-7", nil))
	require.Equal(http.StatusOK, w.Code, w.Body.String())
	assert.NotContains(w.Body.String(), "possibly_worth_keeping", "a manifest without a source reference cannot be matched")
}

func protectionTargets() *querytest.MockEngine {
	return &querytest.MockEngine{
		GetDeletionTargetsByMessageIDsFunc: func(_ context.Context, ids []int64) ([]query.DeletionTarget, error) {
			targets := make([]query.DeletionTarget, 0, len(ids))
			for _, id := range ids {
				targets = append(targets, query.DeletionTarget{
					MessageID: id, SourceID: 9, SourceType: "gmail",
					SourceIdentifier: "user@example.com", SourceMessageID: "gm-" + string(rune('0'+id)),
				})
			}
			return targets, nil
		},
	}
}

func TestStageDeletionWarnsAboutProtectedMessagesAndProtectLeavesThemOut(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	st := &protectionStore{protections: map[int64]store.DeletionProtection{
		2: {MessageID: 2, Starred: true},
		3: {MessageID: 3, OwnerSent: true, PersonSender: true},
	}}
	srv := newDeletionTestServer(t, st, protectionTargets())

	w := postDeletions(t, srv, `{"message_ids": [1, 2, 3, 4]}`)
	require.Equal(http.StatusCreated, w.Code, w.Body.String())
	var warned StageDeletionResponse
	require.NoError(json.Unmarshal(w.Body.Bytes(), &warned))
	assert.Equal(4, warned.MessageCount, "without protect every message is staged")
	require.NotNil(warned.Protection)
	assert.Equal(DeletionProtectionSummary{
		ProtectedCount: 2, Starred: 1, OwnerSent: 1, PersonSender: 1, SampleMessageIDs: []int64{2, 3},
	}, *warned.Protection)

	w = postDeletions(t, srv, `{"message_ids": [1, 2, 3, 4], "protect": true}`)
	require.Equal(http.StatusCreated, w.Code, w.Body.String())
	var protected StageDeletionResponse
	require.NoError(json.Unmarshal(w.Body.Bytes(), &protected))
	assert.Equal(2, protected.MessageCount)
	require.NotNil(protected.Protection)
	assert.True(protected.Protection.Skipped)
	require.Len(st.saved, 2)
	assert.Equal([]string{"gm-1", "gm-4"}, st.saved[1].GmailIDs)

	w = postDeletions(t, srv, `{"message_ids": [2, 3], "protect": true}`)
	assert.Equal(http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(w.Body.String(), "all_messages_protected")
	assert.Len(st.saved, 2, "nothing is staged when protect leaves nothing")
}

func TestStageDeletionRefusesProtectWithoutAProtectionCheck(t *testing.T) {
	t.Parallel()
	st := &deletionMockStore{}
	srv := newDeletionTestServer(t, st, protectionTargets())

	w := postDeletions(t, srv, `{"message_ids": [1], "protect": true}`)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "protection_unavailable")
	assert.Empty(t, st.saved)
}

func TestRemoteImageRefusesSpamAndTrashMessagesBeforeAnyNetworkUse(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(fakePNG)
	}))
	defer upstream.Close()
	seams := &remoteImageSeams{
		answers:  map[string][]netip.Addr{"images.example": {netip.MustParseAddr("203.0.113.7")}},
		upstream: upstream.Listener.Addr().String(),
	}
	srv := newRemoteImageTestServer(t, seams)
	srv.store = &protectionStore{blocked: map[int64]bool{10: false, 11: true}}
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, remoteImagePath, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		srv.Router().ServeHTTP(resp, req)
		return resp
	}

	blocked := post(`{"url": "http://images.example/pixel.png", "message_id": 11}`)
	assert.Equal(http.StatusForbidden, blocked.Code, blocked.Body.String())
	assert.Contains(blocked.Body.String(), "remote_images_blocked")
	missing := post(`{"url": "http://images.example/pixel.png", "message_id": 12}`)
	assert.Equal(http.StatusNotFound, missing.Code, missing.Body.String())
	assert.Zero(seams.dialCount(), "a refused message never reaches the network")

	allowed := post(`{"url": "http://images.example/pixel.png", "message_id": 10}`)
	require.Equal(http.StatusOK, allowed.Code, allowed.Body.String())
	assert.Equal(fakePNG, allowed.Body.Bytes())
}
