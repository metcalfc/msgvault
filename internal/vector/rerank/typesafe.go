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

// jevCall is one request and the candidates [Start, End) it scores. A
// batched request asks candidate_0..candidate_{n-1} about its own
// candidates[0..n-1]; its answers map back through the span.
type jevCall struct {
	request jev.Request
	span    jev.Span
}

func validateJevInput(query string, candidates []string) error {
	if strings.TrimSpace(query) == "" || !utf8.ValidString(query) || len([]byte(query)) > typesafeMaxQuery {
		return fmt.Errorf("%w: query exceeds the 4096-byte Jev limit or is empty", ErrRequestBounds)
	}
	if len(candidates) == 0 || len(candidates) > MaxCandidates {
		return fmt.Errorf("%w: candidate count must be between 1 and %d", ErrRequestBounds, MaxCandidates)
	}
	for i, candidate := range candidates {
		if !utf8.ValidString(candidate) || len([]byte(candidate)) > MaxCandidateBytes {
			return fmt.Errorf("%w: candidate %d exceeds the 2048-byte Jev limit", ErrRequestBounds, i)
		}
	}
	return nil
}

// planJevCalls turns one rerank into requests that each fit within budget
// estimated tokens (see jev.EstimateTokens). The per-candidate shape sends
// one request per candidate. The batched shape packs as many consecutive
// candidates into one request as fit, up to MaxCandidates, so dense
// candidates split into several requests instead of overrunning the
// provider. A candidate too large to fit even alone is cut to the longest
// prefix that fits.
func planJevCalls(query string, candidates []string, shape string, budget int) ([]jevCall, error) {
	if err := validateJevInput(query, candidates); err != nil {
		return nil, err
	}
	switch shape {
	case ShapePerCandidate:
		questions := []jev.Question{rankingQuestion(perCandidateQuestionID, "candidate")}
		calls := make([]jevCall, len(candidates))
		for i, candidate := range candidates {
			fitted, err := fitCandidate(candidate, budget, questions, func(text string) any {
				return jevPerCandidateState{Query: query, Candidate: text}
			})
			if err != nil {
				return nil, err
			}
			calls[i] = jevCall{
				request: jev.Request{State: jevPerCandidateState{Query: query, Candidate: fitted}, Questions: questions},
				span:    jev.Span{Start: i, End: i + 1},
			}
		}
		return calls, nil
	case ShapeBatched:
		// The longest batched question bounds every subset a request asks.
		all := BatchedQuestions()
		spans := jev.PackSpans(len(candidates), MaxCandidates, budget, all, func(start, end int) any {
			return jevBatchedState{Query: query, Candidates: candidates[start:end]}
		})
		calls := make([]jevCall, 0, len(spans))
		for _, span := range spans {
			chunk := slices.Clone(candidates[span.Start:span.End])
			if len(chunk) == 1 {
				fitted, err := fitCandidate(chunk[0], budget, all, func(text string) any {
					return jevBatchedState{Query: query, Candidates: []string{text}}
				})
				if err != nil {
					return nil, err
				}
				chunk[0] = fitted
			}
			questions := make([]jev.Question, len(chunk))
			for i := range chunk {
				questions[i] = rankingQuestion(batchedQuestionID(i), fmt.Sprintf("candidates[%d]", i))
			}
			calls = append(calls, jevCall{
				request: jev.Request{State: jevBatchedState{Query: query, Candidates: chunk}, Questions: questions},
				span:    span,
			})
		}
		return calls, nil
	default:
		return nil, fmt.Errorf("unknown Jev request shape %q", shape)
	}
}

// fitCandidate returns candidate, or its longest prefix whose state fits
// budget when the whole one does not.
func fitCandidate(candidate string, budget int, questions []jev.Question, state func(string) any) (string, error) {
	if jev.FitsStateBudget(state(candidate), questions, budget) {
		return candidate, nil
	}
	if !jev.FitsStateBudget(state(""), questions, budget) {
		return "", fmt.Errorf("%w: %w: the query alone exceeds the token budget", ErrRequestBounds, jev.ErrStateTooLarge)
	}
	fits, over := 0, len(candidate)
	for over-fits > 1 {
		mid := fits + (over-fits)/2
		if jev.FitsStateBudget(state(TruncateUTF8Bytes(candidate, mid)), questions, budget) {
			fits = mid
		} else {
			over = mid
		}
	}
	return TruncateUTF8Bytes(candidate, fits), nil
}

// askFunc sends independent requests and reports each one's response, as
// jev.Client.AskAll does.
type askFunc func(ctx context.Context, requests []jev.Request) (jev.BatchResult, error)

// scoreCandidates plans the rerank within jev.MaxStateTokens, sends it, and
// reads one Noul per candidate. If the provider still answers
// max_tokens_exceeded, every unanswered request is replanned once at half
// its own estimated size (split, or cut for a single candidate) and resent;
// a second failure is returned. Usage covers every attempt on every path.
func scoreCandidates(ctx context.Context, request Request, shape string, ask askFunc) (Result, error) {
	pending, err := planJevCalls(request.Query, request.Candidates, shape, jev.MaxStateTokens)
	if err != nil {
		return emptyJevResult(), err
	}
	result := emptyJevResult()
	result.Scores = make([]float64, len(request.Candidates))
	for retried := false; ; retried = true {
		requests := make([]jev.Request, len(pending))
		for i, call := range pending {
			requests[i] = call.request
		}
		batch, askErr := ask(ctx, requests)
		addUsage(&result.Usage, batch.Usage)
		var unanswered []jevCall
		for i, call := range pending {
			if i >= len(batch.Responses) || batch.Responses[i] == nil {
				unanswered = append(unanswered, call)
				continue
			}
			if err := readScores(*batch.Responses[i], call, shape, result.Scores); err != nil {
				return result, err
			}
		}
		if askErr == nil {
			if len(unanswered) > 0 {
				return result, fmt.Errorf("%w: missing response", ErrInvalidResponse)
			}
			return result, nil
		}
		if retried || !errors.Is(askErr, jev.ErrStateTooLarge) || ctx.Err() != nil {
			return result, fmt.Errorf("rerank requests failed: %w", askErr)
		}
		pending, err = replanSmaller(request, shape, unanswered)
		if err != nil {
			return result, fmt.Errorf("rerank requests failed: %w", askErr)
		}
	}
}

// replanSmaller replans each unanswered call at half its own estimated size,
// keeping every piece's span in the original candidate order.
func replanSmaller(request Request, shape string, unanswered []jevCall) ([]jevCall, error) {
	var pending []jevCall
	for _, call := range unanswered {
		estimate, err := jev.EstimateStateTokens(call.request.State, call.request.Questions)
		if err != nil {
			return nil, err
		}
		smaller, err := planJevCalls(request.Query, request.Candidates[call.span.Start:call.span.End],
			shape, min(jev.MaxStateTokens, estimate)/2)
		if err != nil {
			return nil, err
		}
		for _, piece := range smaller {
			piece.span = jev.Span{Start: call.span.Start + piece.span.Start, End: call.span.Start + piece.span.End}
			pending = append(pending, piece)
		}
	}
	return pending, nil
}

// readScores copies one response's Nouls into scores through its span.
func readScores(response jev.Response, call jevCall, shape string, scores []float64) error {
	if shape == ShapeBatched {
		for k := range call.span.Len() {
			answer, ok := response.Answers[batchedQuestionID(k)]
			if !ok {
				return fmt.Errorf("%w: missing answer", ErrInvalidResponse)
			}
			scores[call.span.Start+k] = answer.Noul
		}
		return nil
	}
	answer, ok := response.Answers[perCandidateQuestionID]
	if !ok {
		return fmt.Errorf("%w: missing answer", ErrInvalidResponse)
	}
	scores[call.span.Start] = answer.Noul
	return nil
}

// addUsage folds one attempt's usage into a running total. A missing token
// count, on either side, makes the total incomplete.
func addUsage(total *Usage, usage Usage) {
	total.Requests += usage.Requests
	if !usage.Complete || usage.InputTokens == nil || usage.OutputTokens == nil {
		total.Complete = false
	}
	if usage.InputTokens != nil && total.InputTokens != nil {
		sum := *total.InputTokens + *usage.InputTokens
		total.InputTokens = &sum
	}
	if usage.OutputTokens != nil && total.OutputTokens != nil {
		sum := *total.OutputTokens + *usage.OutputTokens
		total.OutputTokens = &sum
	}
}

// Rerank scores every candidate against the query. On failure the returned
// usage still counts every attempted request so callers can account for it.
func (j *Jev) Rerank(ctx context.Context, request Request) (Result, error) {
	return scoreCandidates(ctx, request, j.shape, j.client.AskAll)
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
