package mcp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/query/querytest"
	"go.kenn.io/msgvault/internal/testutil"
	"go.kenn.io/msgvault/internal/vector"
	"go.kenn.io/msgvault/internal/vector/hybrid"
)

// countingReranker stands in for the daemon's Jev rerank stage and counts
// every judgment it is asked for.
type countingReranker struct{ calls atomic.Int32 }

func (r *countingReranker) Top() int         { return 30 }
func (r *countingReranker) Identity() string { return "test" }
func (r *countingReranker) Admit(context.Context) (string, error) {
	return "policy", nil
}
func (r *countingReranker) Timeout() time.Duration { return time.Minute }
func (r *countingReranker) Rerank(_ context.Context, _ string, ids []int64) (hybrid.RerankScores, error) {
	r.calls.Add(1)
	scores := make(map[int64]float64, len(ids))
	for i, id := range ids {
		scores[id] = float64(i+1) / float64(len(ids)+1)
	}
	return hybrid.RerankScores{Model: "jev-test", Scores: scores}, nil
}

func rerankMCPHandlers(rerankSearches bool) (*handlers, *countingReranker) {
	backend := &fakeBackend{
		active: vector.Generation{ID: 1, Model: "fake", Dimension: 4, Fingerprint: "fake:4", State: vector.GenerationActive},
		searchHits: []vector.Hit{
			{MessageID: 10, Score: 0.9}, {MessageID: 20, Score: 0.8}, {MessageID: 30, Score: 0.7},
		},
	}
	engine := hybrid.NewEngine(backend, nil, realEmbedder{dim: 4}, hybrid.Config{
		ExpectedFingerprint: "fake:4", RRFK: 60, KPerSignal: 10,
	})
	reranker := &countingReranker{}
	engine.SetReranker(reranker)
	return &handlers{
		engine: &querytest.MockEngine{Messages: map[int64]*query.MessageDetail{
			10: testutil.NewMessageDetail(10).WithBodyText("first").BuildPtr(),
			20: testutil.NewMessageDetail(20).WithBodyText("second").BuildPtr(),
			30: testutil.NewMessageDetail(30).WithBodyText("third").BuildPtr(),
		}},
		hybridEngine: engine, backend: backend, rerankSearches: rerankSearches,
	}, reranker
}

type rerankHybridPage struct {
	Data []struct {
		ID int64 `json:"id"`
	} `json:"data"`
	Rerank *HybridRerank `json:"rerank"`
}

func TestMCPHybridSearchMakesNoJevCallByDefault(t *testing.T) {
	assert := assert.New(t)
	h, reranker := rerankMCPHandlers(false)
	resp := runTool[rerankHybridPage](t, "semantic_search_messages", h.semanticSearchMessages, map[string]any{
		"query": "hit", "mode": "hybrid",
	})
	require.Len(t, resp.Data, 3)
	assert.Nil(resp.Rerank)
	assert.Zero(reranker.calls.Load(), "an MCP search never asks Jev unless [jev.rerank] mcp = true")
}

func TestMCPHybridSearchRerankFollowsTheMCPSwitch(t *testing.T) {
	assert := assert.New(t)
	h, reranker := rerankMCPHandlers(true)
	resp := runTool[rerankHybridPage](t, "semantic_search_messages", h.semanticSearchMessages, map[string]any{
		"query": "hit", "mode": "hybrid",
	})
	require.NotNil(t, resp.Rerank)
	assert.Equal(hybrid.RerankApplied, resp.Rerank.Status)
	assert.Equal(int32(1), reranker.calls.Load())
}

func TestMCPDaemonHybridSearchAsksForRerankOnlyWhenAllowed(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		var got HybridSearchRequest
		searcher := hybridSearcherFunc(func(_ context.Context, request HybridSearchRequest) (*HybridSearchResult, error) {
			got = request
			return &HybridSearchResult{}, nil
		})
		h := &handlers{engine: &querytest.MockEngine{}, hybridSearcher: searcher, rerankSearches: allowed}
		runTool[rerankHybridPage](t, "semantic_search_messages", h.semanticSearchMessages, map[string]any{
			"query": "hit", "mode": "hybrid",
		})
		assert.Equal(t, allowed, got.Rerank, "mcp switch %v", allowed)
	}
}
