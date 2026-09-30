package jev

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEstimateTokensIsPessimisticForDenseText(t *testing.T) {
	assert := assert.New(t)
	prose := strings.Repeat("The meeting moved to the larger room after lunch. ", 40)
	dense := strings.Repeat("Order #4417-2291 SKU A7X-99Q2 $1,249.00 https://example.com/t?id=8812ab ", 28)
	// Prose sits near a typical tokenizer's four bytes per token.
	proseRatio := float64(len(prose)) / float64(EstimateTokens(prose))
	assert.Greater(proseRatio, 3.0)
	assert.Less(proseRatio, 5.0)
	// Dense order text is charged at under two bytes per token, where real
	// tokenizers are least efficient.
	assert.Less(float64(len(dense))/float64(EstimateTokens(dense)), 2.0)
	// Never below one token per four bytes, and non-ASCII costs more.
	assert.Equal(25, EstimateTokens(strings.Repeat("a", 100)))
	assert.Equal(300, EstimateTokens(strings.Repeat("🧾", 100)))
	assert.Equal(0, EstimateTokens(""))
}

func TestEncodeRefusesStateOverTheTokenBudgetBeforeSending(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var calls atomic.Int32
	client := newTestClient(t, &Budget{MaxRequests: 10}, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected send")
	})
	// 80 KiB of digits is under the byte cap but far over the token budget.
	request := noulRequest("fits")
	request.State = map[string]any{"candidate": strings.Repeat("7", 80<<10)}
	_, err := client.Encode(request)
	require.Error(err)
	require.ErrorIs(err, ErrStateTooLarge)
	assert.ErrorIs(err, ErrRequestBounds)
	assert.True(Oversize(err))
	assert.Equal("state_too_large", Skipped(err))
	assert.Equal(ErrStateTooLarge.Error(), SafeFailure(err))
	_, err = client.Ask(context.Background(), request)
	assert.ErrorIs(err, ErrStateTooLarge)
	assert.Zero(calls.Load())
}

func statusResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestProviderErrorSurfacesOnlyTheErrorType(t *testing.T) {
	secret := "echoed candidate text from Alex at alex@example.com"
	tests := []struct {
		name      string
		status    int
		body      string
		errorType string
		failure   string
		tooLarge  bool
	}{
		{
			name: "detail error_type", status: 400,
			body:      `{"detail":{"error_type":"max_tokens_exceeded","message":"` + secret + `"}}`,
			errorType: "max_tokens_exceeded", failure: "provider returned HTTP 400 (max_tokens_exceeded)", tooLarge: true,
		},
		{
			name: "top-level error type", status: 429,
			body:      `{"error":{"type":"rate_limited","message":"` + secret + `"}}`,
			errorType: "rate_limited", failure: "provider returned HTTP 429 (rate_limited)",
		},
		{
			name: "prose in the token slot is dropped", status: 400,
			body:    `{"detail":{"error_type":"` + secret + `"}}`,
			failure: "provider returned HTTP 400",
		},
		{
			name: "string detail", status: 422,
			body:    `{"detail":"` + secret + `"}`,
			failure: "provider returned HTTP 422",
		},
		{
			name: "body past the read bound", status: 400,
			body:    `{"pad":"` + strings.Repeat("x", maxErrorBodyBytes) + `","detail":{"error_type":"max_tokens_exceeded"}}`,
			failure: "provider returned HTTP 400",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			client := newTestClient(t, &Budget{MaxRequests: 1}, func(*http.Request) (*http.Response, error) {
				return statusResponse(tt.status, tt.body), nil
			})
			_, err := client.Ask(context.Background(), noulRequest("q"))
			require.Error(t, err)
			assert.Equal(tt.errorType, ProviderErrorType(err))
			assert.Equal(tt.failure, SafeFailure(err))
			assert.Equal(tt.tooLarge, errors.Is(err, ErrStateTooLarge))
			assert.NotContains(err.Error(), "example.com")
			assert.NotContains(fmt.Sprintf("%+v", err), "Alex")
		})
	}
}

func TestMaxTokensExceededDoesNotHaltAPerRunBudget(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	budget := &Budget{MaxRequests: 10, PerRun: true}
	var calls atomic.Int32
	client := newTestClient(t, budget, func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return statusResponse(400, `{"detail":{"error_type":"max_tokens_exceeded"}}`), nil
		}
		return jsonResponse(`{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":0.4}},
			"usage":{"input_tokens":10,"output_tokens":1}}`), nil
	})
	_, err := client.Ask(context.Background(), noulRequest("q"))
	require.ErrorIs(err, ErrStateTooLarge)
	assert.Zero(budget.State().ConsecutiveFailures, "an oversize request says nothing about provider health")
	response, err := client.Ask(context.Background(), noulRequest("q"))
	require.NoError(err)
	assert.InDelta(0.4, response.Answers["q"].Noul, 1e-9)

	// Any other provider failure still halts the run.
	halting := newTestClient(t, &Budget{MaxRequests: 10, PerRun: true}, func(*http.Request) (*http.Response, error) {
		return statusResponse(400, `{"detail":{"error_type":"invalid_request"}}`), nil
	})
	_, err = halting.Ask(context.Background(), noulRequest("q"))
	require.Error(err)
	_, err = halting.Ask(context.Background(), noulRequest("q"))
	assert.ErrorIs(err, ErrRunHalted)
}

type denseState struct {
	Items []string `json:"items"`
}

func denseItems(n, bytes int) []string {
	items := make([]string, n)
	for i := range items {
		items[i] = strings.Repeat(strconv.Itoa(i%10), bytes)
	}
	return items
}

func TestPackSpansKeepsEveryStateWithinBudget(t *testing.T) {
	assert := assert.New(t)
	items := denseItems(30, 2000)
	questions := []Question{noulRequest("q").Questions[0]}
	build := func(start, end int) any { return denseState{Items: items[start:end]} }
	spans := PackSpans(len(items), 30, MaxStateTokens, questions, build)
	assert.Greater(len(spans), 1, "30 dense items do not fit one request")
	next := 0
	for _, span := range spans {
		assert.Equal(next, span.Start, "spans are contiguous and ordered")
		next = span.End
		assert.LessOrEqual(span.Len(), 30)
		assert.True(FitsStateBudget(build(span.Start, span.End), questions, MaxStateTokens))
	}
	assert.Equal(len(items), next)
	// Light items still pack up to maxItems.
	light := PackSpans(25, 10, MaxStateTokens, questions, func(start, end int) any {
		return denseState{Items: make([]string, end-start)}
	})
	assert.Equal([]Span{{0, 10}, {10, 20}, {20, 25}}, light)
}

func TestJudgeSpansRetriesAProviderOversizeOnceAtHalfSize(t *testing.T) {
	assert := assert.New(t)
	items := denseItems(8, 500)
	questions := []Question{noulRequest("q").Questions[0]}
	build := func(start, end int) any { return denseState{Items: items[start:end]} }
	var judged []Span
	err := JudgeSpans(len(items), 8, questions, build, func(span Span) error {
		judged = append(judged, span)
		if span.Len() == len(items) {
			return httpStatusError{status: 400, errorType: providerMaxTokensExceeded}
		}
		return nil
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(judged), 3)
	assert.Equal(Span{0, 8}, judged[0])
	next := 0
	for _, piece := range judged[1:] {
		assert.Equal(next, piece.Start, "the retry covers the failed span in order")
		assert.LessOrEqual(piece.Len(), 4, "the retry packs to half the failed estimate")
		next = piece.End
	}
	assert.Equal(len(items), next)

	// A second oversize answer is returned rather than retried again.
	calls := 0
	err = JudgeSpans(len(items), 8, questions, build, func(Span) error {
		calls++
		return httpStatusError{status: 400, errorType: providerMaxTokensExceeded}
	})
	require.ErrorIs(t, err, ErrStateTooLarge)
	assert.Equal(2, calls)

	// Other errors stop at once.
	calls = 0
	err = JudgeSpans(len(items), 8, questions, build, func(Span) error {
		calls++
		return ErrBreakerOpen
	})
	require.ErrorIs(t, err, ErrBreakerOpen)
	assert.Equal(1, calls)
}
