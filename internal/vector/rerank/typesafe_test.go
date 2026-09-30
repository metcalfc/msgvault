package rerank

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/jev"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type wireCapture struct {
	Request  map[string]any  `json:"request"`
	Response json.RawMessage `json:"response"`
}

func readCapture(t *testing.T, name string) wireCapture {
	t.Helper()
	data, err := os.ReadFile("testdata/typesafe/" + name)
	require.NoError(t, err)
	var capture wireCapture
	require.NoError(t, json.Unmarshal(data, &capture))
	return capture
}

func captureTransport(response json.RawMessage, requests *[][]byte) testTransport {
	return func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if requests != nil {
			*requests = append(*requests, body)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(response)))}, nil
	}
}

func TestJevWireCaptures(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	per := readCapture(t, "capture_per_candidate.json")
	batch := readCapture(t, "capture_batched.json")

	var perBodies [][]byte
	perScorer, err := NewJev("per-candidate", "secret", &Budget{MaxRequests: 10, StopUSD: 1}, captureTransport(per.Response, &perBodies))
	require.NoError(err)
	perResult, err := perScorer.Rerank(context.Background(), Request{Query: "synthetic question", Candidates: []string{"synthetic candidate"}})
	require.NoError(err)
	require.Len(perBodies, 1)
	var got map[string]any
	require.NoError(json.Unmarshal(perBodies[0], &got))
	assert.Equal(per.Request, got, "per-candidate request shape matches the recorded wire capture")
	assert.Equal([]float64{0.15}, perResult.Scores)
	assert.Equal(int64(342), *perResult.Usage.InputTokens)
	assert.True(perResult.Usage.Complete)

	var batchBodies [][]byte
	batchScorer, err := NewJev("batched", "secret", &Budget{MaxRequests: 10, StopUSD: 1}, captureTransport(batch.Response, &batchBodies))
	require.NoError(err)
	batchResult, err := batchScorer.Rerank(context.Background(), Request{Query: "synthetic question", Candidates: []string{"synthetic first", "synthetic second"}})
	require.NoError(err)
	require.Len(batchBodies, 1)
	require.NoError(json.Unmarshal(batchBodies[0], &got))
	assert.Equal(batch.Request, got, "batched request shape matches the recorded wire capture")
	assert.Equal([]float64{0.37, 0.34}, batchResult.Scores)
	assert.Equal(1, batchResult.Usage.Requests)
	assert.Equal(int64(438), *batchResult.Usage.InputTokens)
}

func TestJevBounds(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Log("candidate=2048 query=4096 request=131072 response=65536 max_requests=1000")
	_, err := planJevCalls(strings.Repeat("q", 4096), []string{"candidate"}, "batched", jev.MaxStateTokens)
	require.NoError(err)
	_, err = planJevCalls(strings.Repeat("q", 4097), []string{"candidate"}, "batched", jev.MaxStateTokens)
	require.ErrorIs(err, ErrRequestBounds)
	_, err = planJevCalls("query", []string{strings.Repeat("x", 2049)}, "batched", jev.MaxStateTokens)
	require.ErrorIs(err, ErrRequestBounds)
	_, err = planJevCalls("query", make([]string, MaxCandidates+1), "batched", jev.MaxStateTokens)
	require.ErrorIs(err, ErrRequestBounds)

	maxCandidates := make([]string, MaxCandidates)
	for i := range maxCandidates {
		maxCandidates[i] = strings.Repeat("x", MaxCandidateBytes)
	}
	scorer, err := NewJev("batched", "secret", &Budget{MaxRequests: 10, StopUSD: 1}, nil)
	require.NoError(err)
	requests, err := planJevCalls(strings.Repeat("q", typesafeMaxQuery), maxCandidates, "batched", jev.MaxStateTokens)
	require.NoError(err)
	require.Len(requests, 1)
	body, err := scorer.client.Encode(requests[0].request)
	require.NoError(err)
	assert.LessOrEqual(len(body), 128<<10, "the largest reranking request stays under the shared request cap")

	budget := &Budget{MaxRequests: 0, StopUSD: 1}
	var calls atomic.Int32
	limited, err := NewJev("batched", "secret", budget, testTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: http.NoBody}, nil
	}))
	require.NoError(err)
	_, err = limited.Rerank(context.Background(), Request{Query: "query", Candidates: []string{"a", "b"}})
	require.ErrorIs(err, ErrRequestLimit)
	assert.Zero(calls.Load(), "the request limit is checked before egress")
}

func TestJevRerankFailureKeepsAttemptedUsageAndRedactsBodies(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var requests atomic.Int32
	scorer, err := NewJev("batched", "secret-key", &Budget{MaxRequests: 10, StopUSD: 1}, testTransport(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("candidate secret body"))}, nil
	}))
	require.NoError(err)
	result, err := scorer.Rerank(context.Background(), Request{Query: "query", Candidates: []string{"candidate"}})
	require.Error(err)
	assert.Equal(1, result.Usage.Requests)
	assert.False(result.Usage.Complete)
	assert.Equal("provider returned HTTP 502", SafeFailure(err))
	assert.NotContains(err.Error(), "candidate secret body")
	assert.NotContains(err.Error(), "secret")
	assert.Equal("provider request failed", SafeFailure(errors.New("provider returned HTTP 503 secret")))
}

func TestJevRerankBudgetAccountsPerRun(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	budget := &Budget{MaxRequests: 10, StopUSD: 1}
	_, err := NewJev("batched", "secret", budget, nil)
	require.NoError(err)
	assert.True(budget.PerRun, "reranking budgets cap the run, not the UTC day")
}

func TestNewJevRejectsInvalidInputs(t *testing.T) {
	require := require.New(t)
	_, err := NewJev("weird", "secret", &Budget{}, nil)
	require.ErrorContains(err, "unknown Jev request shape")
	_, err = NewJev("batched", " ", &Budget{}, nil)
	require.ErrorContains(err, "TYPESAFE_API_KEY")
	_, err = NewJev("batched", "secret", nil, nil)
	require.ErrorContains(err, "budget")
}

// denseCandidate is a synthetic order email: numbers, SKUs, and URLs that
// tokenize poorly. Its id prefix lets the fake provider score it.
func denseCandidate(id int) string {
	text := fmt.Sprintf("c%02d|", id)
	for len(text) < MaxCandidateBytes-80 {
		text += fmt.Sprintf("SKU-%d-%04d $%d.99 https://shop.example.com/o/%d?q=%d ", id, len(text), id, len(text), id)
	}
	return text
}

// candidateScore is the fake provider's score for a candidate: its id / 100.
func candidateScore(text string) float64 {
	var id int
	if _, err := fmt.Sscanf(text, "c%02d|", &id); err != nil {
		return 0
	}
	return float64(id) / 100
}

type recordedRequest struct {
	State struct {
		Query      string   `json:"query"`
		Candidate  string   `json:"candidate,omitempty"`
		Candidates []string `json:"candidates,omitempty"`
	} `json:"state"`
	Questions map[string]json.RawMessage `json:"questions"`
}

// providerTransport stands in for the TypeSafe API, an external contract
// the tests cannot reach: it answers every Noul with candidateScore, unless
// tooLarge says to answer 400 max_tokens_exceeded as the provider documents.
func providerTransport(t *testing.T, mu *sync.Mutex, seen *[]recordedRequest, tooLarge func(recordedRequest) bool) testTransport {
	t.Helper()
	return func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var request recordedRequest
		require.NoError(t, json.Unmarshal(raw, &request))
		mu.Lock()
		*seen = append(*seen, request)
		mu.Unlock()
		header := http.Header{"Content-Type": []string{"application/json"}}
		if tooLarge != nil && tooLarge(request) {
			return &http.Response{StatusCode: http.StatusBadRequest, Header: header, Body: io.NopCloser(strings.NewReader(
				`{"detail":{"error_type":"max_tokens_exceeded","message":"state echo: ` + request.State.Candidate + `"}}`))}, nil
		}
		answers := map[string]any{}
		for id := range request.Questions {
			text := request.State.Candidate
			if id != perCandidateQuestionID {
				var index int
				_, err := fmt.Sscanf(id, "candidate_%d", &index)
				require.NoError(t, err)
				require.Less(t, index, len(request.State.Candidates), "question ids number the request's own candidates")
				text = request.State.Candidates[index]
			}
			answers[id] = map[string]any{"type": "noul", "noul": candidateScore(text)}
		}
		body, err := json.Marshal(map[string]any{
			"model": jev.DefaultModel, "answers": answers,
			"usage": map[string]any{"input_tokens": 100, "output_tokens": 5},
		})
		require.NoError(t, err)
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	}
}

func expectedScores(candidates []string) []float64 {
	scores := make([]float64, len(candidates))
	for i, candidate := range candidates {
		scores[i] = candidateScore(candidate)
	}
	return scores
}

func TestJevBatchedSplitsDenseCandidatesUnderTheTokenBudget(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	candidates := make([]string, MaxCandidates)
	for i := range candidates {
		candidates[i] = denseCandidate(i + 1)
	}
	var mu sync.Mutex
	var seen []recordedRequest
	scorer, err := NewJev(ShapeBatched, "secret", &Budget{MaxRequests: 100, StopUSD: 1},
		providerTransport(t, &mu, &seen, nil))
	require.NoError(err)
	result, err := scorer.Rerank(context.Background(), Request{Query: "order total", Candidates: candidates})
	require.NoError(err)
	assert.Equal(expectedScores(candidates), result.Scores, "every candidate keeps its own score and position")
	require.Greater(len(seen), 1, "30 dense candidates are split across requests")
	assert.Equal(len(seen), result.Usage.Requests)
	sent := 0
	for _, request := range seen {
		assert.Len(request.Questions, len(request.State.Candidates))
		tokens, err := jev.EstimateStateTokens(request.State, BatchedQuestions())
		require.NoError(err)
		assert.LessOrEqual(tokens, jev.MaxStateTokens)
		for _, candidate := range request.State.Candidates {
			assert.Contains(candidates, candidate, "no candidate is trimmed")
		}
		sent += len(request.State.Candidates)
	}
	assert.Equal(MaxCandidates, sent, "no candidate is dropped")
}

func TestJevRetriesOnlyTheOversizeRequestAndKeepsItsSiblings(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	// Thirty dense candidates split into several requests, the last one a
	// small trailing batch.
	candidates := make([]string, MaxCandidates)
	for i := range candidates {
		candidates[i] = denseCandidate(i + 1)
	}
	var mu sync.Mutex
	var seen []recordedRequest
	var rejected atomic.Bool
	scorer, err := NewJev(ShapeBatched, "secret", &Budget{MaxRequests: 100, StopUSD: 1},
		providerTransport(t, &mu, &seen, func(request recordedRequest) bool {
			// The provider counts more tokens than the estimate for the
			// first request that holds the first candidate.
			return candidateScore(request.State.Candidates[0]) == 0.01 && rejected.CompareAndSwap(false, true)
		}))
	require.NoError(err)
	result, err := scorer.Rerank(context.Background(), Request{Query: "order total", Candidates: candidates})
	require.NoError(err)
	assert.Equal(expectedScores(candidates), result.Scores, "scores stay aligned with their candidates")
	sends := map[string]int{}
	firstRequest := 0
	for _, request := range seen {
		for _, candidate := range request.State.Candidates {
			sends[candidate]++
		}
		if firstRequest == 0 && candidateScore(request.State.Candidates[0]) == 0.01 {
			firstRequest = len(request.State.Candidates)
		}
	}
	require.Positive(firstRequest)
	require.Less(firstRequest, MaxCandidates, "the plan has siblings, including a trailing batch")
	for i, candidate := range candidates {
		want := 1
		if i < firstRequest {
			want = 2
		}
		assert.Equal(want, sends[candidate], "candidate %d: siblings are sent once, the oversize request is resent split", i)
	}
	assert.Equal(len(seen), result.Usage.Requests)
	assert.False(result.Usage.Complete, "the rejected request reported no usage")
	assert.Equal(int64(100*(len(seen)-1)), *result.Usage.InputTokens, "every answered request's usage is recorded")
}

func TestJevRetriesOnceAtHalfSizeWhenTheProviderSaysMaxTokens(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	candidates := make([]string, 12)
	for i := range candidates {
		candidates[i] = fmt.Sprintf("c%02d|Receipt for your order, thank you for shopping.", i+1)
	}
	var mu sync.Mutex
	var seen []recordedRequest
	budget := &Budget{MaxRequests: 100, StopUSD: 1}
	scorer, err := NewJev(ShapeBatched, "secret", budget, providerTransport(t, &mu, &seen, func(request recordedRequest) bool {
		// The provider counts more tokens than the estimate for the full batch.
		return len(request.State.Candidates) == len(candidates)
	}))
	require.NoError(err)
	result, err := scorer.Rerank(context.Background(), Request{Query: "receipt", Candidates: candidates})
	require.NoError(err)
	assert.Equal(expectedScores(candidates), result.Scores)
	require.Greater(len(seen), 2, "the rejected batch is resent as smaller requests")
	assert.Len(seen[0].State.Candidates, len(candidates))
	assert.Equal(len(seen), result.Usage.Requests)
	assert.False(result.Usage.Complete, "the rejected attempt has no usage")
	_, err = scorer.Rerank(context.Background(), Request{Query: "receipt", Candidates: candidates[:2]})
	require.NoError(err, "an oversize answer does not halt the run")

	// A second oversize answer is reported with its error type and no content.
	seen = nil
	always, err := NewJev(ShapePerCandidate, "secret", &Budget{MaxRequests: 100, StopUSD: 1},
		providerTransport(t, &mu, &seen, func(recordedRequest) bool { return true }))
	require.NoError(err)
	result, err = always.Rerank(context.Background(), Request{Query: "receipt", Candidates: []string{"c01|secret order detail " + strings.Repeat("line item and shipping ", 60)}})
	require.Error(err)
	require.ErrorIs(err, jev.ErrStateTooLarge)
	assert.Equal("provider returned HTTP 400 (max_tokens_exceeded)", SafeFailure(err))
	assert.NotContains(err.Error(), "secret order detail")
	assert.Equal(2, result.Usage.Requests, "one retry, then the failure")
	require.Len(seen, 2)
	assert.Less(len(seen[1].State.Candidate), len(seen[0].State.Candidate), "the retry cuts a lone candidate")
}
