//go:build sqlite_vec

package hybrid

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/msgvault/internal/vector"
)

type recordingReranker struct {
	top     int
	scores  map[int64]float64
	err     error
	timeout time.Duration
	// release, when set, holds every judgment until it is closed.
	release chan struct{}
	// started is signalled when a judgment begins.
	started chan struct{}
	// admit, when set, decides each caller's gate.
	admit func() (string, error)
	// honorCancel makes a held judgment return when its context ends,
	// like a real provider call, and records that it did.
	honorCancel bool
	cancelled   atomic.Bool

	mu    sync.Mutex
	calls [][]int64
}

func (r *recordingReranker) Timeout() time.Duration { return r.timeout }

func (r *recordingReranker) Admit(context.Context) (string, error) {
	if r.admit != nil {
		return r.admit()
	}
	return "policy-1", nil
}

func (r *recordingReranker) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

type reasonError string

func (e reasonError) Error() string        { return "skipped: " + string(e) }
func (e reasonError) RerankReason() string { return string(e) }

func (r *recordingReranker) Top() int         { return r.top }
func (r *recordingReranker) Identity() string { return "batched\x00test-model" }

func (r *recordingReranker) Rerank(ctx context.Context, _ string, ids []int64) (RerankScores, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]int64(nil), ids...))
	r.mu.Unlock()
	if r.started != nil {
		r.started <- struct{}{}
	}
	if r.release != nil {
		if r.honorCancel {
			select {
			case <-r.release:
			case <-ctx.Done():
				r.cancelled.Store(true)
				return RerankScores{}, ctx.Err()
			}
		} else {
			<-r.release
		}
	}
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
	assert.Equal(1, reranker.callCount(), "the cached order needs no second judgment")

	_, meta, err = f.Engine.Search(t.Context(), SearchRequest{Mode: ModeHybrid, FreeText: "lunch", Limit: 4, Rerank: true})
	require.NoError(err)
	assert.False(meta.Rerank.Cached, "another query is judged afresh")
	assert.Equal(2, reranker.callCount())
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
			assert.Equal(want, reranker.callCount(), "transient failures are pinned for the next page; gate states are not")
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

func fusedFixtureHits() []vector.FusedHit {
	return []vector.FusedHit{
		{MessageID: 1, RRFScore: 0.03}, {MessageID: 2, RRFScore: 0.02}, {MessageID: 3, RRFScore: 0.01},
	}
}

func TestEngineRerankConcurrentMissesShareOneJudgment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		engine := NewEngine(nil, nil, nil, Config{})
		reranker := &recordingReranker{
			top: 3, scores: map[int64]float64{1: 0.1, 2: 0.2, 3: 0.9}, timeout: time.Minute,
			release: make(chan struct{}), started: make(chan struct{}, 2),
		}
		request := hybridRequest(5, true)
		generation := vector.Generation{ID: 1}
		results := make(chan []int64, 2)
		search := func() {
			hits := fusedFixtureHits()
			info := engine.applyRerank(t.Context(), reranker, request, generation, hits)
			assert.Equal(RerankApplied, info.Status)
			results <- hitIDs(hits)
		}
		go search()
		<-reranker.started // the slow judgment is in flight
		go search()
		synctest.Wait() // the second search is waiting on the same flight
		close(reranker.release)
		assert.Equal([]int64{3, 2, 1}, <-results)
		assert.Equal([]int64{3, 2, 1}, <-results)
		assert.Equal(1, reranker.callCount(), "concurrent misses for one key share one judgment")
	})
}

func TestEngineRerankTimeoutKeepsFusedOrderAndNeverReplacesAnOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		engine := NewEngine(nil, nil, nil, Config{})
		reranker := &recordingReranker{
			top: 3, scores: map[int64]float64{1: 0.1, 2: 0.2, 3: 0.9}, timeout: 2 * time.Second,
			release: make(chan struct{}), started: make(chan struct{}, 1),
		}
		request := hybridRequest(5, true)
		generation := vector.Generation{ID: 1}

		hits := fusedFixtureHits()
		started := time.Now()
		info := engine.applyRerank(t.Context(), reranker, request, generation, hits)
		assert.Equal(2*time.Second, time.Since(started), "the search waits exactly the bounded timeout")
		assert.Equal("timeout", info.Reason)
		assert.Equal([]int64{1, 2, 3}, hitIDs(hits), "a timed-out judgment keeps the fused order")

		// Its only waiter left, so the judgment was cancelled and nothing
		// was cached.
		<-reranker.started
		close(reranker.release)
		synctest.Wait()
		key := rerankCacheKey(request, generation, reranker.Identity()+"\x00policy-1", []int64{1, 2, 3})
		_, ok := engine.rerankCache.get(key)
		assert.False(ok, "a judgment nobody waits for is not cached")

		engine.rerankCache.put(key, RerankInfo{Status: RerankApplied, Scores: map[int64]float64{1: 0.1, 2: 0.2, 3: 0.9}, Scored: 3})
		engine.rerankCache.put(key, RerankInfo{Status: RerankSkipped, Reason: "timeout"})
		hits = fusedFixtureHits()
		info = engine.applyRerank(t.Context(), reranker, request, generation, hits)
		assert.Equal(RerankApplied, info.Status, "a failure never replaces a cached order")
		assert.True(info.Cached)
		assert.Equal([]int64{3, 2, 1}, hitIDs(hits))
		assert.Equal(1, reranker.callCount())
	})
}

func TestEngineRerankLastCallerLeavingCancelsTheJudgment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		engine := NewEngine(nil, nil, nil, Config{})
		reranker := &recordingReranker{
			top: 3, scores: map[int64]float64{1: 0.1, 2: 0.2, 3: 0.9}, timeout: time.Minute,
			release: make(chan struct{}), started: make(chan struct{}, 2), honorCancel: true,
		}
		request := hybridRequest(5, true)
		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			<-reranker.started
			cancel()
		}()
		hits := fusedFixtureHits()
		info := engine.applyRerank(ctx, reranker, request, vector.Generation{ID: 1}, hits)
		assert.Equal("timeout", info.Reason, "a caller that leaves keeps the fused order")
		synctest.Wait()
		assert.True(reranker.cancelled.Load(), "the judgment stops once its only caller left")
		assert.Equal(1, reranker.callCount(), "no further request is made for the abandoned search")

		// The abandoned judgment was not cached; the next search judges afresh.
		close(reranker.release)
		hits = fusedFixtureHits()
		info = engine.applyRerank(t.Context(), reranker, request, vector.Generation{ID: 1}, hits)
		assert.Equal(RerankApplied, info.Status)
		assert.Equal(2, reranker.callCount())
	})
}

func TestEngineRerankOneCallerLeavingKeepsTheJudgmentForOthers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		engine := NewEngine(nil, nil, nil, Config{})
		reranker := &recordingReranker{
			top: 3, scores: map[int64]float64{1: 0.1, 2: 0.2, 3: 0.9}, timeout: time.Minute,
			release: make(chan struct{}), started: make(chan struct{}, 1), honorCancel: true,
		}
		request := hybridRequest(5, true)
		stayed := make(chan *RerankInfo, 1)
		go func() {
			stayed <- engine.applyRerank(t.Context(), reranker, request, vector.Generation{ID: 1}, fusedFixtureHits())
		}()
		<-reranker.started
		ctx, cancel := context.WithCancel(t.Context())
		left := make(chan *RerankInfo, 1)
		go func() {
			left <- engine.applyRerank(ctx, reranker, request, vector.Generation{ID: 1}, fusedFixtureHits())
		}()
		synctest.Wait()
		cancel()
		assert.Equal("timeout", (<-left).Reason)
		close(reranker.release)
		assert.Equal(RerankApplied, (<-stayed).Status)
		assert.False(reranker.cancelled.Load())
		assert.Equal(1, reranker.callCount())
	})
}

func TestEngineRerankGatesEveryCallerBeforeJoining(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		engine := NewEngine(nil, nil, nil, Config{})
		var revoked atomic.Bool
		reranker := &recordingReranker{
			top: 3, scores: map[int64]float64{1: 0.1, 2: 0.2, 3: 0.9}, timeout: time.Minute,
			release: make(chan struct{}), started: make(chan struct{}, 1),
			admit: func() (string, error) {
				if revoked.Load() {
					return "", reasonError("consent_required")
				}
				return "policy-1", nil
			},
		}
		request := hybridRequest(5, true)
		first := make(chan *RerankInfo, 1)
		go func() {
			first <- engine.applyRerank(t.Context(), reranker, request, vector.Generation{ID: 1}, fusedFixtureHits())
		}()
		<-reranker.started
		revoked.Store(true) // consent revoked while the first judgment is in flight

		hits := fusedFixtureHits()
		info := engine.applyRerank(t.Context(), reranker, request, vector.Generation{ID: 1}, hits)
		assert.Equal(RerankSkipped, info.Status)
		assert.Equal("consent_required", info.Reason, "a caller whose gate fails never joins a judgment")
		assert.Equal([]int64{1, 2, 3}, hitIDs(hits))

		close(reranker.release)
		assert.Equal(RerankApplied, (<-first).Status)

		info = engine.applyRerank(t.Context(), reranker, request, vector.Generation{ID: 1}, fusedFixtureHits())
		assert.Equal("consent_required", info.Reason, "nor reads the cached order")
		assert.Equal(1, reranker.callCount())
	})
}

func TestRerankCacheKeyCoversEveryFilterDimension(t *testing.T) {
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	later := after.Add(24 * time.Hour)
	generation := vector.Generation{ID: 7, Fingerprint: "fake:4"}
	base := SearchRequest{Mode: ModeHybrid, FreeText: "budget"}
	baseKey := rerankCacheKey(base, generation, "id", []int64{1, 2})
	for name, filter := range map[string]vector.Filter{
		"account scope": {SourceIDs: []int64{3}},
		"after":         {After: &after},
		"before":        {Before: &later},
		"message types": {MessageTypes: []string{"email"}},
		"labels":        {LabelGroups: [][]int64{{9}}},
		"senders":       {SenderGroups: [][]int64{{4}}},
		"list":          {ListID: "list.example.com"},
	} {
		request := base
		request.Filter = filter
		assert.NotEqual(t, baseKey, rerankCacheKey(request, generation, "id", []int64{1, 2}), name)
	}
	assert.NotEqual(t, baseKey, rerankCacheKey(base, vector.Generation{ID: 8, Fingerprint: "fake:4"}, "id", []int64{1, 2}), "generation")
	assert.NotEqual(t, baseKey, rerankCacheKey(base, generation, "id", []int64{1, 3}), "a removed result changes the key")
	assert.Equal(t, baseKey, rerankCacheKey(base, generation, "id", []int64{1, 2}))
}
