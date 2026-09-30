package rerank

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"go.kenn.io/msgvault/internal/jev"
)

// Request shapes. The eval flags and this package spell per-candidate with a
// hyphen; [jev.rerank] spells it per_candidate (see ShapeFromConfig).
const (
	ShapeBatched      = "batched"
	ShapePerCandidate = "per-candidate"
)

const perCandidateQuestionID = "matches"

func batchedQuestionID(i int) string { return fmt.Sprintf("candidate_%d", i) }

// BatchedQuestionID is the batched shape's question about candidates[i].
func BatchedQuestionID(i int) string { return batchedQuestionID(i) }

// BatchedQuestions returns the batched shape's MaxCandidates questions,
// worded exactly as search reranking sends them over a state of `query` and
// `candidates`. Another feature asks the same Noul under its own consent
// by listing these in its own policy.
func BatchedQuestions() []jev.Question {
	questions := make([]jev.Question, MaxCandidates)
	for i := range questions {
		questions[i] = rankingQuestion(batchedQuestionID(i), fmt.Sprintf("candidates[%d]", i))
	}
	return questions
}

const (
	JevEndpoint       = jev.DefaultEndpoint
	JevModel          = jev.DefaultModel
	MaxCandidates     = 30
	MaxCandidateBytes = 2048
	typesafeMaxQuery  = 4096
)

// Failure categories are the shared client's so callers can match either name.
var (
	ErrRequestLimit    = jev.ErrRequestLimit
	ErrCostStop        = jev.ErrCostStop
	ErrUsageUnknown    = jev.ErrUsageUnknown
	ErrRequestBounds   = jev.ErrRequestBounds
	ErrInvalidResponse = jev.ErrInvalidResponse
)

// Budget shares request and cost limits across Jev scorers. Set its limits before use.
type Budget = jev.Budget

// Jev scores message candidates with the TypeSafe API.
type Jev struct {
	shape  string
	client *jev.Client
}

// NewJev creates a scorer. A nil transport uses the default HTTP transport.
func NewJev(shape, key string, budget *Budget, transport http.RoundTripper) (*Jev, error) {
	if shape != ShapePerCandidate && shape != ShapeBatched {
		return nil, fmt.Errorf("unknown Jev request shape %q", shape)
	}
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("TYPESAFE_API_KEY is required")
	}
	if budget == nil {
		return nil, errors.New("reranker budget is required")
	}
	// Reranking runs are bounded evaluations: --rerank-cost-stop-usd caps
	// the whole run, not one UTC day, and a provider failure or unknowable
	// usage stops the run rather than pausing for a cool-down.
	budget.PerRun = true
	client, err := jev.NewClient(jev.Options{APIKey: key, Budget: budget, Transport: transport})
	if err != nil {
		return nil, err
	}
	return &Jev{shape: shape, client: client}, nil
}

type jevPerCandidateState struct {
	Query     string `json:"query"`
	Candidate string `json:"candidate"`
}

type jevBatchedState struct {
	Query      string   `json:"query"`
	Candidates []string `json:"candidates"`
}

func rankingQuestion(id, name string) jev.Question {
	return jev.Question{
		ID:           id,
		Type:         jev.QuestionNoul,
		Instructions: fmt.Sprintf("Could `%s` be the best answer to `query`?", name),
		Criteria: jev.NoulCriteria{
			True:  fmt.Sprintf("The %s contains the specific information needed to answer the query.", name),
			False: fmt.Sprintf("The %s is only topically similar or does not contain the needed evidence.", name),
		},
	}
}

func encodeJevCalls(query string, candidates []string, shape string) ([]jev.Request, error) {
	if strings.TrimSpace(query) == "" || !utf8.ValidString(query) || len([]byte(query)) > typesafeMaxQuery {
		return nil, fmt.Errorf("%w: query exceeds the 4096-byte Jev limit or is empty", ErrRequestBounds)
	}
	if len(candidates) == 0 || len(candidates) > MaxCandidates {
		return nil, fmt.Errorf("%w: candidate count must be between 1 and %d", ErrRequestBounds, MaxCandidates)
	}
	for i, candidate := range candidates {
		if !utf8.ValidString(candidate) || len([]byte(candidate)) > MaxCandidateBytes {
			return nil, fmt.Errorf("%w: candidate %d exceeds the 2048-byte Jev limit", ErrRequestBounds, i)
		}
	}
	switch shape {
	case ShapePerCandidate:
		requests := make([]jev.Request, len(candidates))
		for i, candidate := range candidates {
			requests[i] = jev.Request{
				State:     jevPerCandidateState{Query: query, Candidate: candidate},
				Questions: []jev.Question{rankingQuestion(perCandidateQuestionID, "candidate")},
			}
		}
		return requests, nil
	case ShapeBatched:
		questions := make([]jev.Question, len(candidates))
		for i := range candidates {
			questions[i] = rankingQuestion(batchedQuestionID(i), fmt.Sprintf("candidates[%d]", i))
		}
		return []jev.Request{{
			State:     jevBatchedState{Query: query, Candidates: slices.Clone(candidates)},
			Questions: questions,
		}}, nil
	default:
		return nil, fmt.Errorf("unknown Jev request shape %q", shape)
	}
}

// Rerank scores every candidate against the query. On failure the returned
// usage still counts every attempted request so callers can account for it.
func (j *Jev) Rerank(ctx context.Context, request Request) (Result, error) {
	calls, err := encodeJevCalls(request.Query, request.Candidates, j.shape)
	if err != nil {
		return emptyJevResult(), err
	}
	batch, err := j.client.AskAll(ctx, calls)
	return resultFromBatch(batch, err, j.shape, len(request.Candidates))
}

// resultFromBatch reads one Noul per candidate out of the responses to
// encodeJevCalls' requests. Usage is kept on every path.
func resultFromBatch(batch jev.BatchResult, err error, shape string, candidates int) (Result, error) {
	result := Result{Scores: make([]float64, candidates), Usage: batch.Usage}
	if err != nil {
		return result, fmt.Errorf("rerank requests failed: %w", err)
	}
	if len(batch.Responses) == 0 {
		return result, fmt.Errorf("%w: missing response", ErrInvalidResponse)
	}
	for i, response := range batch.Responses {
		if response == nil {
			return result, fmt.Errorf("%w: missing response", ErrInvalidResponse)
		}
		if shape == ShapeBatched {
			for k := range candidates {
				answer, ok := response.Answers[batchedQuestionID(k)]
				if !ok {
					return result, fmt.Errorf("%w: missing answer", ErrInvalidResponse)
				}
				result.Scores[k] = answer.Noul
			}
			continue
		}
		answer, ok := response.Answers[perCandidateQuestionID]
		if !ok {
			return result, fmt.Errorf("%w: missing answer", ErrInvalidResponse)
		}
		result.Scores[i] = answer.Noul
	}
	return result, nil
}

func emptyJevResult() Result {
	input, output := int64(0), int64(0)
	return Result{Usage: Usage{InputTokens: &input, OutputTokens: &output, Complete: true}}
}

// SafeFailure reports a known error category without including queries, message
// text, credentials, or provider response bodies.
func SafeFailure(err error) string {
	return jev.SafeFailure(err)
}
