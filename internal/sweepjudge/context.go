// Package sweepjudge holds the judgments the people sweep makes around its
// chat model: which retrieved messages bear on a fact before extraction
// (local embedding similarity against the message vector index), and
// whether each extracted claim is stated and still current (the Jev feature
// sweep_claim_grounding).
package sweepjudge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"unicode/utf8"

	"go.kenn.io/msgvault/internal/peoplesweep"
	"go.kenn.io/msgvault/internal/personfacts"
	"go.kenn.io/msgvault/internal/vector"
)

const (
	maxContextQueryRunes = 400
	// unitNormTolerance bounds how far a query vector's length may stray
	// from 1 before its scores are not read as cosine similarity.
	unitNormTolerance = 0.01
)

// QueryEmbedder embeds one query with the configured provider's query role.
// The hybrid search engine implements it.
type QueryEmbedder interface {
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

// Similarity is the message vector index the context scorer reads: the
// backend that holds the active generation's chunk vectors, the query
// embedder for the same provider, and the configured generation
// fingerprint, so a query is never compared with vectors from another model.
type Similarity struct {
	Backend     vector.ChunkScoringBackend
	Embedder    QueryEmbedder
	Fingerprint string
}

// SimilaritySource returns the message vector index when it is configured
// and initialized; ok=false means there is none and every item is kept.
type SimilaritySource func() (Similarity, bool)

// ContextScorer implements peoplesweep.ContextJudge with local embedding
// similarity. Only the target's catalog description is embedded; each
// item's message is scored from the chunk vectors the archive already
// indexed, so no excerpt is sent anywhere to score it.
type ContextScorer struct {
	source SimilaritySource
	logger *slog.Logger

	mu      sync.Mutex
	queries map[string][]float32
}

var _ peoplesweep.ContextJudge = (*ContextScorer)(nil)

// NewContextScorer wires context relevance to the message vector index.
func NewContextScorer(source SimilaritySource, logger *slog.Logger) *ContextScorer {
	if logger == nil {
		logger = slog.Default()
	}
	return &ContextScorer{source: source, logger: logger, queries: make(map[string][]float32)}
}

var errNoSimilarity = errors.New("message vector index is not available")

// JudgeContext scores each item by the best cosine similarity between the
// target's catalog description and any indexed chunk of the item's message.
// An item the index cannot score (not embedded, outside the embedding
// scope, or not message text) gets peoplesweep.ContextNotJudged and is
// kept. A missing index, a stale or building generation, a sensitive
// target, or a provider failure returns an error, and the sweep keeps every
// item.
func (s *ContextScorer) JudgeContext(
	ctx context.Context, target personfacts.TargetDescriptor, items []peoplesweep.EvidenceItem,
) ([]float64, error) {
	if s == nil || s.source == nil {
		return nil, errNoSimilarity
	}
	if target.Sensitive {
		return nil, errors.New("sensitive targets are never scored")
	}
	query := contextQuery(target)
	if query == "" {
		return nil, errors.New("target has no description")
	}
	similarity, ok := s.source()
	if !ok || similarity.Backend == nil || similarity.Embedder == nil {
		return nil, errNoSimilarity
	}
	scores, err := s.score(ctx, similarity, query, items)
	if err != nil {
		s.logger.Info("people sweep context relevance: embedding similarity skipped",
			"target", target.Key, "error", err)
		return nil, err
	}
	return scores, nil
}

func (s *ContextScorer) score(
	ctx context.Context, similarity Similarity, query string, items []peoplesweep.EvidenceItem,
) ([]float64, error) {
	active, err := vector.ResolveActiveForFingerprint(ctx, similarity.Backend, similarity.Fingerprint)
	if err != nil {
		return nil, fmt.Errorf("resolve message vector index: %w", err)
	}
	queryVec, err := s.queryVector(ctx, similarity, query)
	if err != nil {
		return nil, err
	}
	scores := make([]float64, len(items))
	for i, item := range items {
		scores[i] = peoplesweep.ContextNotJudged
		if !scoredLane(item.Ref.SourceLane) || item.Ref.MessageID <= 0 {
			continue
		}
		hits, err := similarity.Backend.ScoreMessageChunks(ctx, active.ID, item.Ref.MessageID, queryVec)
		if err != nil {
			return nil, fmt.Errorf("score message %d: %w", item.Ref.MessageID, err)
		}
		if len(hits) == 0 {
			continue
		}
		// Hits are sorted best first.
		scores[i] = cosineFromScore(hits[0].Score)
	}
	return scores, nil
}

// queryVector embeds the description once per scorer and checks that it is
// unit length, which the score conversion relies on.
func (s *ContextScorer) queryVector(ctx context.Context, similarity Similarity, query string) ([]float32, error) {
	s.mu.Lock()
	cached, ok := s.queries[query]
	s.mu.Unlock()
	if ok {
		return cached, nil
	}
	queryVec, err := similarity.Embedder.EmbedQuery(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed target description: %w", err)
	}
	var sum float64
	for _, value := range queryVec {
		sum += float64(value) * float64(value)
	}
	if norm := math.Sqrt(sum); math.Abs(norm-1) > unitNormTolerance {
		return nil, fmt.Errorf("embedding model returns vectors of length %.3f, not unit length", norm)
	}
	s.mu.Lock()
	s.queries[query] = queryVec
	s.mu.Unlock()
	return queryVec, nil
}

// scoredLane reports whether an item's message body is what the message
// vector index embedded. Attachment and document text is indexed apart from
// its message, so scoring the message would judge the wrong text.
func scoredLane(lane peoplesweep.SourceClass) bool {
	return lane == peoplesweep.SourceConversationText || lane == peoplesweep.SourceMeetingText
}

// cosineFromScore converts the backend's chunk score (1 minus Euclidean
// distance) to cosine similarity. For unit vectors the squared distance is
// 2 - 2cos, so cos = 1 - d²/2. The result is clamped to [0, 1].
func cosineFromScore(score float64) float64 {
	distance := 1 - score
	return min(1, max(0, 1-distance*distance/2))
}

// contextQuery is the target's catalog description, falling back to its slug.
func contextQuery(target personfacts.TargetDescriptor) string {
	query := strings.Join(strings.Fields(target.Description), " ")
	if query == "" {
		query = strings.ReplaceAll(strings.TrimSpace(target.Slug), "_", " ")
	}
	return truncateRunes(query, maxContextQueryRunes)
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}
