package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/vector"
	"go.kenn.io/msgvault/internal/vector/hybrid"
)

// fusingFakeBackend answers hybrid searches with a fixed fused ranking.
type fusingFakeBackend struct {
	*fakeVectorBackend
	fused  []vector.FusedHit
	limits []int
}

func (b *fusingFakeBackend) FusedSearch(_ context.Context, req vector.FusedRequest) ([]vector.FusedHit, vector.SearchMetadata, error) {
	b.limits = append(b.limits, req.Limit)
	hits := append([]vector.FusedHit(nil), b.fused...)
	if len(hits) > req.Limit {
		hits = hits[:req.Limit]
	}
	return hits, vector.SearchMetadata{PoolSaturated: len(b.fused) > req.Limit}, nil
}

type fixedReranker struct {
	scores map[int64]float64
	calls  int
}

func (r *fixedReranker) Top() int         { return 30 }
func (r *fixedReranker) Identity() string { return "test" }
func (r *fixedReranker) Rerank(_ context.Context, _ string, ids []int64) (hybrid.RerankScores, error) {
	r.calls++
	scores := make(map[int64]float64)
	for _, id := range ids {
		scores[id] = r.scores[id]
	}
	return hybrid.RerankScores{Model: "jev-1.13.0", Scores: scores}, nil
}

func newRerankSearchServer(t *testing.T, reranker hybrid.Reranker) (*Server, *fusingFakeBackend) {
	t.Helper()
	sent := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	store := &mockStore{messages: []APIMessage{
		{ID: 1, Subject: "Budget draft", FromEmail: "casey@example.com", SentAt: sent},
		{ID: 2, Subject: "Lunch", FromEmail: "robin@example.com", SentAt: sent},
		{ID: 3, Subject: "Signed budget", FromEmail: "casey@example.com", SentAt: sent},
	}}
	backend := &fusingFakeBackend{
		fakeVectorBackend: &fakeVectorBackend{active: &vector.Generation{
			ID: 1, Model: "fake", Dimension: 4, Fingerprint: "fake:4", State: vector.GenerationActive,
		}},
		fused: []vector.FusedHit{
			{MessageID: 1, RRFScore: 0.03, BM25Score: 1.2, VectorScore: 0.9},
			{MessageID: 2, RRFScore: 0.02, BM25Score: math.NaN(), VectorScore: 0.8},
			{MessageID: 3, RRFScore: 0.01, BM25Score: 0.4, VectorScore: math.NaN()},
		},
	}
	engine := hybrid.NewEngine(backend, nil, realEmbedder{dim: 4}, hybrid.Config{
		ExpectedFingerprint: "fake:4", RRFK: 60, KPerSignal: 10,
	})
	if reranker != nil {
		engine.SetReranker(reranker)
	}
	srv := NewServerWithOptions(ServerOptions{
		Config:       &config.Config{Server: config.ServerConfig{APIPort: 8080}},
		Store:        store,
		HybridEngine: engine,
		Backend:      backend,
		Logger:       testLogger(),
	})
	return srv, backend
}

type rerankSearchResponse struct {
	Rerank *struct {
		Status string `json:"status"`
		Model  string `json:"model"`
		Scored int    `json:"scored"`
		Cached bool   `json:"cached"`
	} `json:"rerank"`
	HasMore bool `json:"has_more"`
	Results []struct {
		ID    int64 `json:"id"`
		Score *struct {
			RRF    *float64 `json:"rrf"`
			Rerank *float64 `json:"rerank"`
		} `json:"score"`
	} `json:"results"`
}

func getRerankSearch(t *testing.T, srv *Server, query string) rerankSearchResponse {
	t.Helper()
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/search?"+query, nil))
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var resp rerankSearchResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	return resp
}

func resultIDs(resp rerankSearchResponse) []int64 {
	ids := make([]int64, len(resp.Results))
	for i, result := range resp.Results {
		ids[i] = result.ID
	}
	return ids
}

func TestHybridSearchReportsRerankAndExplainScore(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	reranker := &fixedReranker{scores: map[int64]float64{1: 0.2, 2: 0.1, 3: 0.95}}
	srv, backend := newRerankSearchServer(t, reranker)

	resp := getRerankSearch(t, srv, "q=signed+budget&mode=hybrid&explain=1&page_size=2")
	assert.Equal([]int64{3, 1}, resultIDs(resp))
	assert.True(resp.HasMore)
	require.NotNil(resp.Rerank)
	assert.Equal("applied", resp.Rerank.Status)
	assert.Equal("jev-1.13.0", resp.Rerank.Model)
	assert.Equal(3, resp.Rerank.Scored)
	require.NotNil(resp.Results[0].Score)
	require.NotNil(resp.Results[0].Score.Rerank)
	assert.InDelta(0.95, *resp.Results[0].Score.Rerank, 1e-12)
	assert.Equal(30, backend.limits[0], "a reranked search retrieves the leading 30 results")

	second := getRerankSearch(t, srv, "q=signed+budget&mode=hybrid&offset=2&page_size=2")
	assert.Equal([]int64{2}, resultIDs(second), "page two continues the reranked order")
	assert.True(second.Rerank.Cached)
	assert.Equal(1, reranker.calls)
	assert.Nil(second.Results[0].Score, "no explain, no score")

	vectorResp := getRerankSearch(t, srv, "q=signed+budget&mode=vector")
	assert.Nil(vectorResp.Rerank, "vector searches are never reranked")
}

func TestHybridSearchWithoutRerankerOmitsRerank(t *testing.T) {
	srv, backend := newRerankSearchServer(t, nil)
	assert := assert.New(t)
	resp := getRerankSearch(t, srv, "q=signed+budget&mode=hybrid&explain=1&page_size=2")
	assert.Equal([]int64{1, 2}, resultIDs(resp))
	assert.Nil(resp.Rerank)
	assert.Nil(resp.Results[0].Score.Rerank)
	assert.Equal(3, backend.limits[0], "the fetch limit is unchanged when reranking is off")
}
