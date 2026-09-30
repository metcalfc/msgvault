//go:build sqlite_vec

package hybrid

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/msgvault/internal/vector"
)

type recordingReranker struct {
	top    int
	scores map[int64]float64
	err    error
	calls  [][]int64
}

type reasonError string

func (e reasonError) Error() string        { return "skipped: " + string(e) }
func (e reasonError) RerankReason() string { return string(e) }

func (r *recordingReranker) Top() int         { return r.top }
func (r *recordingReranker) Identity() string { return "batched\x00test-model" }

func (r *recordingReranker) Rerank(_ context.Context, _ string, ids []int64) (RerankScores, error) {
	r.calls = append(r.calls, append([]int64(nil), ids...))
	if r.err != nil {
		return RerankScores{}, r.err
	}
	scores := make(map[int64]float64)
	for _, id := range ids {
		if score, ok := r.scores[id]; ok {
			scores[id] = score
		}
	}
	return RerankScores{Model: "test-model", Scores: scores}, nil
}

func hitIDs(hits []vector.FusedHit) []int64 {
	ids := make([]int64, len(hits))
	for i, hit := range hits {
		ids[i] = hit.MessageID
	}
	return ids
}

func hybridRequest(limit int, rerank bool) SearchRequest {
	return SearchRequest{Mode: ModeHybrid, FreeText: "meeting", Limit: limit, Rerank: rerank}
}

func TestEngineRerankReordersLeadingHitsAndKeepsTheTail(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newEngineFixture(t)
	fused, meta, err := f.Engine.Search(t.Context(), hybridRequest(5, true))
	require.NoError(err)
	assert.Nil(meta.Rerank, "without a reranker the stage does not run")
	require.Equal([]int64{1, 2, 3}, hitIDs(fused), "fused order of the fixture")

	reranker := &recordingReranker{top: 2, scores: map[int64]float64{1: 0.1, 2: 0.8}}
	f.Engine.SetReranker(reranker)

	hits, meta, err := f.Engine.Search(t.Context(), hybridRequest(5, false))
	require.NoError(err)
	assert.Equal([]int64{1, 2, 3}, hitIDs(hits), "a search that does not ask keeps the fused order")
	assert.Nil(meta.Rerank)
	assert.Empty(reranker.calls)

	hits, meta, err = f.Engine.Search(t.Context(), hybridRequest(5, true))
	require.NoError(err)
	assert.Equal([]int64{2, 1, 3}, hitIDs(hits), "only the leading Top hits move")
	require.NotNil(meta.Rerank)
	assert.Equal(RerankApplied, meta.Rerank.Status)
	assert.Equal("test-model", meta.Rerank.Model)
	assert.Equal(2, meta.Rerank.Scored)
	assert.False(meta.Rerank.Cached)
	assert.InDelta(0.8, meta.Rerank.Scores[2], 1e-12)
	assert.Equal([][]int64{{1, 2}}, reranker.calls)

	vectorHits, meta, err := f.Engine.Search(t.Context(), SearchRequest{Mode: ModeVector, FreeText: "meeting", Limit: 5, Rerank: true})
	require.NoError(err)
	assert.Nil(meta.Rerank, "only hybrid searches are reranked")
	assert.Equal(int64(1), vectorHits[0].MessageID)
}

func TestEngineRerankPagesShareOneCachedOrder(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newEngineFixture(t)
	reranker := &recordingReranker{top: 3, scores: map[int64]float64{1: 0.2, 2: 0.3, 3: 0.9}}
	f.Engine.SetReranker(reranker)

	first, meta, err := f.Engine.Search(t.Context(), hybridRequest(2, true))
	require.NoError(err)
	assert.Equal([]int64{3, 2}, hitIDs(first), "a page smaller than Top still reranks all Top hits")
	assert.True(meta.PoolSaturated, "more reranked hits remain beyond the page")
	assert.Equal(2, meta.ReturnedCount)

	second, meta, err := f.Engine.Search(t.Context(), hybridRequest(4, true))
	require.NoError(err)
	assert.Equal([]int64{3, 2, 1}, hitIDs(second), "the next page reads the same order")
	require.NotNil(meta.Rerank)
	assert.True(meta.Rerank.Cached)
	assert.Len(reranker.calls, 1, "the cached order needs no second judgment")

	_, meta, err = f.Engine.Search(t.Context(), SearchRequest{Mode: ModeHybrid, FreeText: "lunch", Limit: 4, Rerank: true})
	require.NoError(err)
	assert.False(meta.Rerank.Cached, "another query is judged afresh")
	assert.Len(reranker.calls, 2)
}

func TestEngineRerankLeavesUnscoredHitsInPlace(t *testing.T) {
	f := newEngineFixture(t)
	f.Engine.SetReranker(&recordingReranker{top: 3, scores: map[int64]float64{1: 0.1, 3: 0.7}})
	hits, meta, err := f.Engine.Search(t.Context(), hybridRequest(5, true))
	require.NoError(t, err)
	assert.Equal(t, []int64{3, 2, 1}, hitIDs(hits), "message 2 was not sent, so it keeps its slot")
	assert.Equal(t, 2, meta.Rerank.Scored)
}

func TestEngineRerankFailureKeepsFusedOrder(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		reason string
		cached bool
	}{
		{name: "consent missing", err: reasonError("consent_required"), reason: "consent_required"},
		{name: "provider failure", err: reasonError("provider_error"), reason: "provider_error", cached: true},
		{name: "deadline", err: context.DeadlineExceeded, reason: "timeout", cached: true},
		{name: "unclassified", err: errors.New("boom"), reason: "provider_error", cached: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			f := newEngineFixture(t)
			reranker := &recordingReranker{top: 3, err: tc.err}
			f.Engine.SetReranker(reranker)
			for range 2 {
				hits, meta, err := f.Engine.Search(t.Context(), hybridRequest(5, true))
				require.NoError(err, "a rerank failure never fails the search")
				assert.Equal([]int64{1, 2, 3}, hitIDs(hits))
				require.NotNil(meta.Rerank)
				assert.Equal(RerankSkipped, meta.Rerank.Status)
				assert.Equal(tc.reason, meta.Rerank.Reason)
			}
			want := 2
			if tc.cached {
				want = 1
			}
			assert.Len(reranker.calls, want, "transient failures are pinned for the next page; gate states are not")
		})
	}
}

func TestEngineRerankRejectsInvalidScores(t *testing.T) {
	f := newEngineFixture(t)
	f.Engine.SetReranker(&recordingReranker{top: 3, scores: map[int64]float64{1: math.NaN(), 2: 0.5}})
	hits, meta, err := f.Engine.Search(t.Context(), hybridRequest(5, true))
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2, 3}, hitIDs(hits))
	assert.Equal(t, "invalid_response", meta.Rerank.Reason)
}

func TestReorderByScoresBreaksTiesByRRFThenID(t *testing.T) {
	prefix := []vector.FusedHit{
		{MessageID: 9, RRFScore: 0.01},
		{MessageID: 4, RRFScore: 0.03},
		{MessageID: 7, RRFScore: 0.03},
		{MessageID: 5, RRFScore: math.NaN()},
	}
	reorderByScores(prefix, map[int64]float64{9: 0.5, 4: 0.5, 7: 0.5, 5: 0.5})
	assert.Equal(t, []int64{4, 7, 9, 5}, hitIDs(prefix))
}
