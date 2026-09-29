package rerank

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	_, err := encodeJevCalls(strings.Repeat("q", 4096), []string{"candidate"}, "batched")
	require.NoError(err)
	_, err = encodeJevCalls(strings.Repeat("q", 4097), []string{"candidate"}, "batched")
	require.ErrorIs(err, ErrRequestBounds)
	_, err = encodeJevCalls("query", []string{strings.Repeat("x", 2049)}, "batched")
	require.ErrorIs(err, ErrRequestBounds)
	_, err = encodeJevCalls("query", make([]string, MaxCandidates+1), "batched")
	require.ErrorIs(err, ErrRequestBounds)

	maxCandidates := make([]string, MaxCandidates)
	for i := range maxCandidates {
		maxCandidates[i] = strings.Repeat("x", MaxCandidateBytes)
	}
	scorer, err := NewJev("batched", "secret", &Budget{MaxRequests: 10, StopUSD: 1}, nil)
	require.NoError(err)
	requests, err := encodeJevCalls(strings.Repeat("q", typesafeMaxQuery), maxCandidates, "batched")
	require.NoError(err)
	require.Len(requests, 1)
	body, err := scorer.client.Encode(requests[0])
	require.NoError(err)
	assert.LessOrEqual(len(body), 128<<10, "the largest reranking request stays under the shared request cap")

	budget := &Budget{MaxRequests: 0, StopUSD: 1}
	limited, err := NewJev("batched", "secret", budget, testTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: http.NoBody}, nil
	}))
	require.NoError(err)
	_, err = limited.Rerank(context.Background(), Request{Query: "query", Candidates: []string{"a", "b"}})
	require.ErrorIs(err, ErrRequestLimit)
	assert.Equal(0, budget.Attempts(), "the request limit is checked before egress")
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
