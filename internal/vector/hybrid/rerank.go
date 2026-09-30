package hybrid

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"

	"go.kenn.io/msgvault/internal/vector"
)

// Rerank statuses reported in ResultMeta.Rerank.
const (
	RerankApplied = "applied"
	RerankSkipped = "skipped"
)

// RerankReasonTooFewCandidates is the skip reason when fewer than two of the
// leading results may be reranked.
const RerankReasonTooFewCandidates = "too_few_candidates"

// DefaultRerankTimeout bounds a judgment whose reranker names no timeout.
const DefaultRerankTimeout = 10 * time.Second

const (
	rerankCacheTTL        = 10 * time.Minute
	rerankCacheMaxEntries = 256
)

// Reranker scores the leading hybrid results against the query. The engine
// owns which results are offered, how scores reorder them, and caching; the
// reranker owns loading, what leaves the machine, and the gate.
type Reranker interface {
	// Top is the most leading results one search may rerank.
	Top() int
	// Identity names everything besides the candidates that changes scores
	// (for example the request shape and model). It is part of the cache key.
	Identity() string
	// Admit runs the gate for one caller (configuration, credential,
	// consent, budget) without sending anything, and returns a token naming
	// the admitted policy. An error carries a safe skip reason.
	Admit(ctx context.Context) (string, error)
	// Timeout bounds one judgment; zero means DefaultRerankTimeout. A search
	// never waits longer and keeps its fused order when it runs out.
	Timeout() time.Duration
	// Rerank scores some or all of messageIDs. A message it leaves out (for
	// example an excluded message type) keeps its fused position. Scores
	// must be finite probabilities.
	Rerank(ctx context.Context, query string, messageIDs []int64) (RerankScores, error)
}

// RerankScores is one reranking result.
type RerankScores struct {
	Model  string
	Scores map[int64]float64
}

// RerankReasoner is implemented by reranker errors that carry a safe skip
// category (for example consent_required or timeout).
type RerankReasoner interface {
	RerankReason() string
}

// RerankInfo describes what the rerank stage did for one search.
type RerankInfo struct {
	// Status is RerankApplied or RerankSkipped.
	Status string
	// Reason is the safe skip category when Status is RerankSkipped.
	Reason string
	// Model is the judging model when Status is RerankApplied.
	Model string
	// Scored counts the results that received a score.
	Scored int
	// Cached reports that the order came from an earlier identical search.
	Cached bool
	// Scores maps each scored message to its score, for explain output.
	Scores map[int64]float64
}

type rerankCacheEntry struct {
	info    RerankInfo
	expires time.Time
	added   time.Time
}

type rerankCache struct {
	mu      sync.Mutex
	entries map[string]rerankCacheEntry
	now     func() time.Time
}

func (c *rerankCache) get(key string) (RerankInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return RerankInfo{}, false
	}
	if !c.now().Before(entry.expires) {
		delete(c.entries, key)
		return RerankInfo{}, false
	}
	return entry.info, true
}

func (c *rerankCache) put(key string, info RerankInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]rerankCacheEntry)
	}
	now := c.now()
	for existing, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, existing)
		}
	}
	if current, ok := c.entries[key]; ok && current.info.Status == RerankApplied && info.Status != RerankApplied {
		// A skip or failure never replaces an order already served.
		return
	}
	if len(c.entries) >= rerankCacheMaxEntries {
		oldestKey, oldest := "", now
		for existing, entry := range c.entries {
			if oldestKey == "" || entry.added.Before(oldest) {
				oldestKey, oldest = existing, entry.added
			}
		}
		delete(c.entries, oldestKey)
	}
	c.entries[key] = rerankCacheEntry{info: info, expires: now.Add(rerankCacheTTL), added: now}
}

// SetReranker installs the optional rerank stage. Nil removes it. Only
// hybrid searches that ask for it (SearchRequest.Rerank) use it.
func (e *Engine) SetReranker(reranker Reranker) {
	e.rerankMu.Lock()
	defer e.rerankMu.Unlock()
	e.reranker = reranker
}

func (e *Engine) currentReranker() Reranker {
	e.rerankMu.Lock()
	defer e.rerankMu.Unlock()
	return e.reranker
}

// rerankFetchLimit is how many fused results a reranked search retrieves:
// at least the reranker's Top, so every page of one query offers the same
// leading results and therefore reuses one cached order.
func rerankFetchLimit(limit int, reranker Reranker) int {
	return max(limit, reranker.Top())
}

// applyRerank reorders the leading min(Top, n) hits by score. Scored hits
// fill the positions scored hits held, ordered by score, then RRF score,
// then message ID; unscored hits in the prefix and every hit after it keep
// their place. It never fails the search: any error leaves the fused order
// and reports a skip.
func (e *Engine) applyRerank(
	ctx context.Context, reranker Reranker, req SearchRequest, generation vector.Generation, hits []vector.FusedHit,
) *RerankInfo {
	top := min(reranker.Top(), len(hits))
	if top < 2 {
		return &RerankInfo{Status: RerankSkipped, Reason: RerankReasonTooFewCandidates}
	}
	ids := make([]int64, top)
	for i := range top {
		ids[i] = hits[i].MessageID
	}
	// Every caller passes the gate itself (configuration, credential,
	// consent, budget) before it may read a cached order or join a judgment
	// in flight. The admitted policy is part of the key, so a caller never
	// reuses work admitted under another policy.
	admission, err := reranker.Admit(ctx)
	if err != nil {
		return &RerankInfo{Status: RerankSkipped, Reason: rerankReason(err)}
	}
	key := rerankCacheKey(req, generation, reranker.Identity()+"\x00"+admission, ids)
	if info, ok := e.rerankCache.get(key); ok {
		info.Cached = true
		reorderByScores(hits[:top], info.Scores)
		return &info
	}
	timeout := reranker.Timeout()
	if timeout <= 0 {
		timeout = DefaultRerankTimeout
	}
	flight := e.joinRerankFlight(ctx, key, timeout, func(flightCtx context.Context) RerankInfo {
		return e.judgeRerank(flightCtx, reranker, req.FreeText, ids, key)
	})
	wait := time.NewTimer(timeout)
	defer wait.Stop()
	select {
	case <-flight.done:
		info := flight.info
		if info.Status == RerankApplied {
			reorderByScores(hits[:top], info.Scores)
		}
		return &info
	case <-ctx.Done():
	case <-wait.C:
	}
	e.leaveRerankFlight(key, flight)
	return &RerankInfo{Status: RerankSkipped, Reason: "timeout"}
}

// rerankFlight is one judgment shared by every concurrent caller with the
// same key. It runs on its own context, bounded by the reranker's timeout,
// and is cancelled as soon as its last waiter leaves, so no request is sent
// for a search nobody is waiting for.
type rerankFlight struct {
	cancel  context.CancelFunc
	waiters int
	done    chan struct{}
	info    RerankInfo
}

func (e *Engine) joinRerankFlight(
	ctx context.Context, key string, timeout time.Duration, judge func(context.Context) RerankInfo,
) *rerankFlight {
	e.rerankFlightsMu.Lock()
	defer e.rerankFlightsMu.Unlock()
	if flight, ok := e.rerankFlights[key]; ok {
		flight.waiters++
		return flight
	}
	flightCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	flight := &rerankFlight{cancel: cancel, waiters: 1, done: make(chan struct{})}
	if e.rerankFlights == nil {
		e.rerankFlights = make(map[string]*rerankFlight)
	}
	e.rerankFlights[key] = flight
	go func() {
		defer cancel()
		info := judge(flightCtx)
		e.rerankFlightsMu.Lock()
		flight.info = info
		if e.rerankFlights[key] == flight {
			delete(e.rerankFlights, key)
		}
		e.rerankFlightsMu.Unlock()
		close(flight.done)
	}()
	return flight
}

// leaveRerankFlight drops one waiter and cancels the judgment when none is
// left.
func (e *Engine) leaveRerankFlight(key string, flight *rerankFlight) {
	e.rerankFlightsMu.Lock()
	defer e.rerankFlightsMu.Unlock()
	flight.waiters--
	if flight.waiters > 0 {
		return
	}
	flight.cancel()
	if e.rerankFlights[key] == flight {
		// A later caller starts a fresh judgment instead of joining a
		// cancelled one.
		delete(e.rerankFlights, key)
	}
}

// judgeRerank asks the reranker once and caches the outcome: an order, or a
// transient failure so the next page keeps the fused order. Gate states and
// too-few-candidate skips are not cached.
func (e *Engine) judgeRerank(ctx context.Context, reranker Reranker, query string, ids []int64, key string) RerankInfo {
	scores, err := reranker.Rerank(ctx, query, ids)
	if ctx.Err() != nil {
		// Cancelled because every caller left, or out of time: nothing is
		// cached, and the next search judges afresh.
		return RerankInfo{Status: RerankSkipped, Reason: "timeout"}
	}
	if err == nil {
		err = validateRerankScores(scores.Scores, ids)
	}
	if err != nil {
		info := RerankInfo{Status: RerankSkipped, Reason: rerankReason(err)}
		if cacheableRerankSkip(info.Reason) {
			e.rerankCache.put(key, info)
		}
		return info
	}
	info := RerankInfo{Model: scores.Model, Scores: scores.Scores, Scored: len(scores.Scores)}
	if info.Scored < 2 {
		return RerankInfo{Status: RerankSkipped, Reason: RerankReasonTooFewCandidates, Model: scores.Model}
	}
	info.Status = RerankApplied
	e.rerankCache.put(key, info)
	return info
}

// reorderByScores sorts the scored hits of prefix among the positions they
// occupy. Nil scores leave the prefix unchanged.
func reorderByScores(prefix []vector.FusedHit, scores map[int64]float64) {
	if len(scores) == 0 {
		return
	}
	positions := make([]int, 0, len(scores))
	scored := make([]vector.FusedHit, 0, len(scores))
	for i, hit := range prefix {
		if _, ok := scores[hit.MessageID]; ok {
			positions = append(positions, i)
			scored = append(scored, hit)
		}
	}
	slices.SortStableFunc(scored, func(a, b vector.FusedHit) int {
		sa, sb := scores[a.MessageID], scores[b.MessageID]
		switch {
		case sa > sb:
			return -1
		case sa < sb:
			return 1
		}
		ra, rb := rrfForTie(a.RRFScore), rrfForTie(b.RRFScore)
		switch {
		case ra > rb:
			return -1
		case ra < rb:
			return 1
		}
		switch {
		case a.MessageID < b.MessageID:
			return -1
		case a.MessageID > b.MessageID:
			return 1
		}
		return 0
	})
	for i, position := range positions {
		prefix[position] = scored[i]
	}
}

func rrfForTie(score float64) float64 {
	if math.IsNaN(score) {
		return math.Inf(-1)
	}
	return score
}

var errInvalidRerankScores = errors.New("reranker returned invalid scores")

func validateRerankScores(scores map[int64]float64, offered []int64) error {
	allowed := make(map[int64]struct{}, len(offered))
	for _, id := range offered {
		allowed[id] = struct{}{}
	}
	for id, score := range scores {
		if _, ok := allowed[id]; !ok {
			return errInvalidRerankScores
		}
		if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 1 {
			return errInvalidRerankScores
		}
	}
	return nil
}

func rerankReason(err error) string {
	var reasoner RerankReasoner
	if errors.As(err, &reasoner) {
		if reason := reasoner.RerankReason(); reason != "" {
			return reason
		}
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	case errors.Is(err, errInvalidRerankScores):
		return "invalid_response"
	default:
		return "provider_error"
	}
}

// cacheableRerankSkip reports whether a skip is a transient provider
// outcome worth pinning for the next page. Configuration and consent states
// are not cached so a change takes effect on the next search.
func cacheableRerankSkip(reason string) bool {
	switch reason {
	case "disabled", "feature_disabled", "manual_only", "consent_required", "credential_missing",
		"policy_unavailable", RerankReasonTooFewCandidates:
		return false
	default:
		return true
	}
}

// rerankCacheKey hashes (query, filter, generation, reranker identity,
// offered IDs in fused order). Equal keys mean an identical rerank input.
func rerankCacheKey(req SearchRequest, generation vector.Generation, identity string, ids []int64) string {
	hash := sha256.New()
	write := func(value string) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		hash.Write(size[:])
		hash.Write([]byte(value))
	}
	write(string(req.Mode))
	write(req.FreeText)
	write(req.FTSQuery)
	// The filter's exported field names are a stable hash input here, not a
	// wire format.
	filter, err := json.Marshal(req.Filter) //nolint:musttag // hash input only.
	if err != nil {
		filter = nil
	}
	write(string(filter))
	write(strconv.FormatInt(int64(generation.ID), 10))
	write(generation.Fingerprint)
	write(identity)
	for _, id := range ids {
		write(strconv.FormatInt(id, 10))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
