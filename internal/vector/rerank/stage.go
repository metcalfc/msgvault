package rerank

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/vector/embed"
	"go.kenn.io/msgvault/internal/vector/hybrid"
)

// MessageLoader reads the reranked messages by primary key in one batch.
// *store.Store implements it.
type MessageLoader interface {
	GetMessagesWithBodiesByIDsContext(ctx context.Context, ids []int64) ([]store.APIMessage, error)
}

// MessageFromAPI takes the candidate fields of a loaded message.
func MessageFromAPI(message *store.APIMessage) Message {
	return Message{
		Subject: message.Subject, FromName: message.FromName, FromEmail: message.FromEmail,
		SentAt: message.SentAt, MessageType: message.MessageType,
		BodyText: message.BodyText, BodyHTML: message.BodyHTML,
	}
}

// Scorer scores rendered candidates. *ServiceScorer and *Jev implement it.
type Scorer interface {
	Rerank(ctx context.Context, request Request) (Result, error)
}

// StageOptions configure the hybrid search rerank stage.
type StageOptions struct {
	Scorer Scorer
	Loader MessageLoader
	// Shape and Model identify the scorer for the engine's order cache.
	Shape string
	Model string
	// Top is how many leading results are offered, 2 to MaxCandidates.
	Top int
	// Excludes reports message types whose text is never sent.
	Excludes func(messageType string) bool
	// Preprocess is the embedding pipeline's body cleaning.
	Preprocess embed.PreprocessConfig
	// Timeout bounds the whole stage; zero leaves the caller's deadline.
	Timeout time.Duration
}

// Stage is the hybrid engine's rerank stage (hybrid.Reranker): it loads the
// offered messages in one batched lookup, drops excluded message types,
// renders each as a bounded candidate, and scores them through Scorer.
type Stage struct {
	options StageOptions
}

var _ hybrid.Reranker = (*Stage)(nil)

// NewStage validates the options.
func NewStage(options StageOptions) (*Stage, error) {
	if options.Scorer == nil || options.Loader == nil {
		return nil, errors.New("rerank stage requires a scorer and a message loader")
	}
	if options.Top < 2 || options.Top > MaxCandidates {
		return nil, fmt.Errorf("rerank top must be between 2 and %d", MaxCandidates)
	}
	if options.Excludes == nil {
		options.Excludes = func(string) bool { return false }
	}
	return &Stage{options: options}, nil
}

// Timeout implements hybrid.Reranker.
func (s *Stage) Timeout() time.Duration { return s.options.Timeout }

// Top implements hybrid.Reranker.
func (s *Stage) Top() int { return s.options.Top }

// Identity implements hybrid.Reranker.
func (s *Stage) Identity() string {
	return s.options.Shape + "\x00" + s.options.Model + "\x00" + strconv.Itoa(s.options.Top)
}

// skipError carries a safe skip category for the engine's report.
type skipError struct {
	reason string
	err    error
}

func (e *skipError) Error() string        { return "rerank skipped: " + e.reason }
func (e *skipError) Unwrap() error        { return e.err }
func (e *skipError) RerankReason() string { return e.reason }

// Rerank implements hybrid.Reranker.
func (s *Stage) Rerank(ctx context.Context, query string, messageIDs []int64) (hybrid.RerankScores, error) {
	if s.options.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.options.Timeout)
		defer cancel()
	}
	messages, err := s.options.Loader.GetMessagesWithBodiesByIDsContext(ctx, messageIDs)
	if err != nil {
		return hybrid.RerankScores{}, &skipError{reason: "load_failed", err: err}
	}
	ids := make([]int64, 0, len(messages))
	candidates := make([]string, 0, len(messages))
	for i := range messages {
		message := &messages[i]
		if s.options.Excludes(message.MessageType) {
			continue
		}
		ids = append(ids, message.ID)
		candidates = append(candidates, Candidate(MessageFromAPI(message), s.options.Preprocess))
	}
	if len(candidates) < 2 {
		return hybrid.RerankScores{}, &skipError{reason: hybrid.RerankReasonTooFewCandidates}
	}
	result, err := s.options.Scorer.Rerank(ctx, Request{Query: query, Candidates: candidates})
	if err != nil {
		return hybrid.RerankScores{}, &skipError{reason: jev.Skipped(err), err: err}
	}
	if len(result.Scores) != len(ids) {
		return hybrid.RerankScores{}, &skipError{reason: "invalid_response", err: ErrInvalidResponse}
	}
	scores := make(map[int64]float64, len(ids))
	for i, id := range ids {
		scores[id] = result.Scores[i]
	}
	return hybrid.RerankScores{Model: s.options.Model, Scores: scores}, nil
}
