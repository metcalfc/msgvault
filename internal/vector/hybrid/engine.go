package hybrid

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/search"
	"go.kenn.io/msgvault/internal/vector"
)

// Mode selects which signal(s) the engine runs.
type Mode string

const (
	// ModeFTS is the legacy FTS-only path. The engine rejects it because
	// the existing search path handles FTS directly.
	ModeFTS Mode = "fts"
	// ModeVector runs pure ANN search against the active generation.
	ModeVector Mode = "vector"
	// ModeHybrid runs fused BM25 + ANN via the FusingBackend capability.
	ModeHybrid Mode = "hybrid"
)

// SearchRequest is the caller-facing input to Engine.Search.
type SearchRequest struct {
	Mode         Mode
	FreeText     string
	FTSQuery     string // optional override; defaults to FreeText
	Filter       vector.Filter
	Limit        int
	SubjectTerms []string // lowercased terms for subject-boost check
	Explain      bool     // reserved for future use; no-op in this task
	// Rerank asks for the optional rerank stage on a hybrid search. Only a
	// person's own interactive search sets it; automatic and background
	// searches never do. It has no effect without an installed Reranker.
	Rerank bool
	// AnyTermFallback allows one retry of the BM25 leg matching any content
	// word when requiring every term matched nothing. Callers set it only
	// for plain bag-of-words queries (see PlainQuery): a quoted phrase or
	// an operator must never be split into loose words.
	AnyTermFallback bool
}

// PlainQuery reports whether a raw query is a plain bag of words: no
// quotes, no operators (from:, message_type=), and no negated or required
// terms. Only such a query may use the any-term BM25 fallback.
func PlainQuery(raw string) bool {
	for field := range strings.FieldsSeq(raw) {
		if strings.ContainsAny(field, "\"\u201c\u201d:=") || strings.HasPrefix(field, "'") || strings.HasSuffix(field, "'") ||
			strings.HasPrefix(field, "-") || strings.HasPrefix(field, "+") {
			return false
		}
	}
	return true
}

// ResultMeta returns engine-level metadata alongside the hit list.
type ResultMeta struct {
	Generation    vector.Generation
	PoolSaturated bool
	Accelerator   string
	ReturnedCount int
	// QueryVector is the embedding of FreeText used for this search.
	// Callers use it to score within-message chunks without re-embedding.
	QueryVector []float32
	// QueryEmbeddingDuration and RetrievalDuration are bounded phase timings
	// for structured diagnostics; they contain no query or vector data.
	QueryEmbeddingDuration time.Duration
	RetrievalDuration      time.Duration
	// Rerank reports the rerank stage when the request asked for it and a
	// reranker is installed; nil otherwise.
	Rerank *RerankInfo
	// RerankDuration is the time spent in the rerank stage.
	RerankDuration time.Duration
	// LexicalMatchAny reports that the BM25 leg matched any content word
	// because requiring every term found nothing.
	LexicalMatchAny bool
}

// EmbeddingClient embeds free-text queries. The engine uses it once per
// Search call.
type EmbeddingClient interface {
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

type legacyEmbeddingClient interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}

type legacyQueryAdapter struct{ client legacyEmbeddingClient }

func (a legacyQueryAdapter) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vectors, err := a.client.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("embedder returned %d vectors, want 1", len(vectors))
	}
	return vectors[0], nil
}

// Config captures engine tuning knobs.
type Config struct {
	// ExpectedFingerprint is the "model:dimension" string the engine
	// checks against the active generation. If empty, the check is
	// skipped.
	ExpectedFingerprint string
	RRFK                int
	KPerSignal          int
	SubjectBoost        float64
	// Rebind transforms the participant/label lookup queries run by
	// BuildFilter against mainDB. Nil or SQLiteDialect.Rebind preserves
	// SQLite's native ? placeholders.
	Rebind     func(string) string
	BuildScope vector.BuildScope
}

// Engine orchestrates the generation check, query embedding, and fusion
// call for vector/hybrid search requests.
type Engine struct {
	backend vector.Backend
	mainDB  *sql.DB
	client  EmbeddingClient
	cfg     Config

	rerankMu        sync.Mutex
	reranker        Reranker
	rerankCache     rerankCache
	rerankFlightsMu sync.Mutex
	rerankFlights   map[string]*rerankFlight
	// rerankBeforePublish, set only by tests, runs after a judgment returns
	// and before the flight publishes it.
	rerankBeforePublish func()
}

// NewEngine wires a backend, main DB handle, embedding client, and
// configuration into an Engine.
func NewEngine(backend vector.Backend, mainDB *sql.DB, client any, cfg Config) *Engine {
	queryClient, ok := client.(EmbeddingClient)
	if !ok {
		if legacy, legacyOK := client.(legacyEmbeddingClient); legacyOK {
			queryClient = legacyQueryAdapter{client: legacy}
		}
	}
	return &Engine{
		backend: backend, mainDB: mainDB, client: queryClient, cfg: cfg,
		rerankCache: rerankCache{now: time.Now},
	}
}

// EmbedQuery embeds free text for within-message chunk scoring.
func (e *Engine) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("empty query")
	}
	vec, err := e.client.EmbedQuery(ctx, text)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("embed query: %w: %w", vector.ErrEmbeddingTimeout, err)
		}
		return nil, fmt.Errorf("embed query: %w", err)
	}
	return vec, nil
}

// BuildFilter resolves a parsed Gmail-syntax query into a vector.Filter
// against the engine's main DB. Convenience wrapper around the
// package-level BuildFilter so callers that already hold an *Engine
// don't need to plumb a *sql.DB separately.
func (e *Engine) BuildFilter(
	ctx context.Context,
	q *search.Query,
	structured ...query.MessageFilter,
) (vector.Filter, error) {
	filter, err := BuildFilter(ctx, e.mainDB, e.cfg.Rebind, q)
	if err != nil {
		return vector.Filter{}, err
	}
	for _, exact := range structured {
		if err := ApplyMessageFilter(ctx, e.mainDB, e.cfg.Rebind, &filter, exact); err != nil {
			return vector.Filter{}, err
		}
	}
	return filter, nil
}

// Search runs hybrid or vector mode. Resolves the active generation
// via vector.ResolveActiveForFingerprint, so callers get the full
// family of sentinel errors:
//
//   - ErrIndexStale: an active generation exists but its fingerprint
//     differs from the configured embedding settings.
//   - ErrIndexBuilding: no active yet, but a build is in progress.
//   - ErrNotEnabled: no generation at all (vector search unused).
//
// mode=fts is rejected with a clear error (legacy path handles it).
func (e *Engine) Search(ctx context.Context, req SearchRequest) ([]vector.FusedHit, ResultMeta, error) {
	if req.Mode == ModeFTS {
		return nil, ResultMeta{}, errors.New("mode=fts should be handled by the legacy engine")
	}
	if req.Mode != ModeVector && req.Mode != ModeHybrid {
		return nil, ResultMeta{}, fmt.Errorf("unknown mode %q", req.Mode)
	}
	if err := vector.ValidateFilter(req.Filter); err != nil {
		return nil, ResultMeta{}, err
	}

	active, err := vector.ResolveActiveForFingerprint(ctx, e.backend, e.cfg.ExpectedFingerprint)
	if err != nil {
		return nil, ResultMeta{}, err
	}
	if err := e.validateBuildScope(req.Filter); err != nil {
		return nil, ResultMeta{}, err
	}

	if req.FreeText == "" {
		return nil, ResultMeta{}, errors.New("empty query")
	}

	embeddingStarted := time.Now()
	queryVec, err := e.client.EmbedQuery(ctx, req.FreeText)
	embeddingDuration := time.Since(embeddingStarted)
	if err != nil {
		// Surface deadline-exceeded distinctly so HTTP/MCP can map it
		// to a transient 503 instead of a generic 500. The handler
		// timeout (default 60s) often fires before a cold local
		// embedding endpoint responds, and "deadline exceeded" wrapped
		// inside an opaque error gives clients no way to know whether
		// to retry.
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, ResultMeta{}, fmt.Errorf("embed query: %w: %w", vector.ErrEmbeddingTimeout, err)
		}
		return nil, ResultMeta{}, fmt.Errorf("embed query: %w", err)
	}
	if req.Mode == ModeVector {
		retrievalStarted := time.Now()
		var hits []vector.Hit
		var searchMeta vector.SearchMetadata
		if backend, ok := e.backend.(vector.MetadataSearchingBackend); ok {
			hits, searchMeta, err = backend.SearchWithMetadata(ctx, active.ID, queryVec, req.Limit, req.Filter)
		} else {
			hits, err = e.backend.Search(ctx, active.ID, queryVec, req.Limit, req.Filter)
			searchMeta.PoolSaturated = len(hits) >= req.Limit
		}
		if err != nil {
			return nil, ResultMeta{}, fmt.Errorf("vector search: %w", err)
		}
		fused := vectorHitsToFused(hits)
		return fused, ResultMeta{
			Generation:             active,
			ReturnedCount:          len(fused),
			PoolSaturated:          searchMeta.PoolSaturated,
			Accelerator:            searchMeta.Accelerator,
			QueryVector:            queryVec,
			QueryEmbeddingDuration: embeddingDuration,
			RetrievalDuration:      time.Since(retrievalStarted),
		}, nil
	}

	// ModeHybrid: prefer FusingBackend.
	fb, ok := e.backend.(vector.FusingBackend)
	if !ok {
		return nil, ResultMeta{}, errors.New("hybrid mode requires a FusingBackend; non-fusing fallback not wired in MVP")
	}
	// FusedRequest.FTSTerms carries tokenized, punctuation-filtered terms.
	// The backend escapes them with SQLiteQueryDialect.BuildFTSTerm for
	// FTS5 prefix matching. An empty term set skips BM25 (vector-only).
	// An explicit FTSQuery override is also tokenized, never used verbatim.
	// Filtering punctuation-only terms and escaping the remaining terms
	// prevents FTS5 metacharacters from becoming query syntax (issue #366).
	// The --mode fts path sanitizes via Store.SearchMessages; --mode vector
	// has no BM25 branch.
	terms := ftsTerms(req.FreeText)
	if req.FTSQuery != "" {
		terms = ftsTerms(req.FTSQuery)
	}
	var reranker Reranker
	limit := req.Limit
	if req.Rerank {
		reranker = e.currentReranker()
		if reranker != nil {
			limit = rerankFetchLimit(limit, reranker)
		}
	}
	fReq := vector.FusedRequest{
		FTSTerms:     terms,
		QueryVec:     queryVec,
		Generation:   active.ID,
		KPerSignal:   e.cfg.KPerSignal,
		Limit:        limit,
		RRFK:         e.cfg.RRFK,
		SubjectBoost: e.cfg.SubjectBoost,
		SubjectTerms: req.SubjectTerms,
		Filter:       req.Filter,
	}
	retrievalStarted := time.Now()
	hits, searchMeta, err := fb.FusedSearch(ctx, fReq)
	if err != nil {
		return nil, ResultMeta{}, fmt.Errorf("fused search: %w", err)
	}
	lexicalAny := false
	if anyTerms := lexicalFallbackTerms(req, terms, hits, searchMeta, limit); anyTerms != nil {
		// Every term must match for the BM25 leg, so a long natural
		// query often finds nothing lexically. Ask again for messages
		// matching any content word; the vector leg is unchanged.
		fReq.FTSTerms = anyTerms
		fReq.FTSMatchAny = true
		fallbackHits, fallbackMeta, fallbackErr := fb.FusedSearch(ctx, fReq)
		if fallbackErr != nil {
			return nil, ResultMeta{}, fmt.Errorf("fused search (any term): %w", fallbackErr)
		}
		hits, searchMeta, lexicalAny = fallbackHits, fallbackMeta, true
	}
	meta := ResultMeta{
		LexicalMatchAny:        lexicalAny,
		Generation:             active,
		PoolSaturated:          searchMeta.PoolSaturated,
		Accelerator:            searchMeta.Accelerator,
		QueryVector:            queryVec,
		QueryEmbeddingDuration: embeddingDuration,
		RetrievalDuration:      time.Since(retrievalStarted),
	}
	if reranker != nil {
		rerankStarted := time.Now()
		meta.Rerank = e.applyRerank(ctx, reranker, req, active, hits)
		meta.RerankDuration = time.Since(rerankStarted)
		if req.Limit > 0 && len(hits) > req.Limit {
			hits = hits[:req.Limit]
			meta.PoolSaturated = true
		}
	}
	meta.ReturnedCount = len(hits)
	return hits, meta, nil
}

func (e *Engine) validateBuildScope(filter vector.Filter) error {
	return ValidateBuildScope(e.cfg.BuildScope, filter)
}

// ValidateBuildScope rejects filters that cannot safely answer from a scoped
// embedding index. A non-empty build scope only covers those message types, so
// callers must make the query scope explicit and compatible before running ANN.
func ValidateBuildScope(buildScope vector.BuildScope, filter vector.Filter) error {
	// Only the message-type dimension is validated. A source-scoped index
	// simply has no vectors for out-of-scope accounts and hybrid search
	// degrades to the BM25 signal for them — rejecting those queries would
	// break ordinary unfiltered search against a partially-embedded corpus.
	scope := vector.NewBuildScope(buildScope.MessageTypes, nil)
	if scope.IsEmpty() {
		return nil
	}
	if len(filter.MessageTypes) == 0 {
		return fmt.Errorf("%w: index is scoped to message_type=%s; add a matching message_type filter",
			vector.ErrIndexScopeMismatch, strings.Join(scope.MessageTypes, ","))
	}
	if !scope.AllowsMessageTypes(filter.MessageTypes) {
		return fmt.Errorf("%w: index is scoped to message_type=%s, query requested message_type=%s",
			vector.ErrIndexScopeMismatch,
			strings.Join(scope.MessageTypes, ","),
			strings.Join(vector.NewBuildScope(filter.MessageTypes, nil).MessageTypes, ","))
	}
	return nil
}

// vectorHitsToFused wraps pure-vector hits in the FusedHit schema.
// BM25Score and RRFScore are both set to math.NaN(): "not present in
// this signal." Pure vector mode never applies Reciprocal Rank Fusion
// (there's only one signal to fuse), so reporting an RRF score would
// be a lie. Renderers and explain output already treat NaN as "skip
// this column," so the breakdown will show vector_score only.
func vectorHitsToFused(hits []vector.Hit) []vector.FusedHit {
	out := make([]vector.FusedHit, len(hits))
	for i, h := range hits {
		out[i] = vector.FusedHit{
			MessageID:   h.MessageID,
			BM25Score:   math.NaN(),
			VectorScore: h.Score,
			RRFScore:    math.NaN(),
		}
	}
	return out
}

// ftsTerms splits a free-text query into terms for hybrid BM25 search,
// mirroring Store.SearchMessages. It keeps words verbatim and drops
// punctuation-only terms with no FTS5 tokens. A nil result skips BM25.
// SQLiteQueryDialect.BuildFTSTerm escapes the retained terms before
// they reach the FTS5 parser (#366).
func ftsTerms(freeText string) []string {
	terms := strings.Fields(freeText)
	kept := terms[:0]
	for _, t := range terms {
		if hasFTSToken(t) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// lexicalFallbackTerms returns the terms for an any-term BM25 retry, or nil
// when none is warranted: the caller did not allow it (the query is not a
// plain bag of words), it overrides the BM25 text, the query has one term,
// the BM25 leg matched something, or no content word (non-stopword)
// remains. Whether the leg matched is the backend's own count, taken
// before subject boosting or trimming. A backend that does not count falls
// back to looking for a BM25 score among the hits; with a limit of one
// that cannot tell an empty leg from an outranked hit, so it never retries.
func lexicalFallbackTerms(
	req SearchRequest, terms []string, hits []vector.FusedHit, meta vector.SearchMetadata, limit int,
) []string {
	if !req.AnyTermFallback || req.FTSQuery != "" || len(terms) < 2 {
		return nil
	}
	if meta.LexicalCounted {
		if meta.LexicalHits > 0 {
			return nil
		}
	} else {
		if limit == 1 {
			return nil
		}
		for _, hit := range hits {
			if !math.IsNaN(hit.BM25Score) {
				return nil
			}
		}
	}
	content := vector.ContentTerms(terms)
	if len(content) == 0 {
		return nil
	}
	return content
}

// hasFTSToken reports whether s contains a rune the default FTS5
// tokenizer (unicode61) emits as part of a token — a Unicode letter or
// digit. Punctuation-only terms tokenize to nothing, so a MATCH built
// from them is a syntax error; callers drop them. Mirrors the helper of
// the same name in internal/store (the two packages keep parallel
// minimal abstractions rather than share a dependency).
func hasFTSToken(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
