package jev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newTestClient(t *testing.T, budget *Budget, transport testTransport) *Client {
	t.Helper()
	client, err := NewClient(Options{APIKey: "secret-key", Budget: budget, Transport: transport})
	require.NoError(t, err)
	return client
}

func noulRequest(id string) Request {
	return Request{
		State: map[string]any{"query": "synthetic question", "candidate": "synthetic candidate"},
		Questions: []Question{{
			ID: id, Type: QuestionNoul,
			Instructions: "Could `candidate` be the best answer to `query`?",
			Criteria:     NoulCriteria{True: "It answers the query.", False: "It does not."},
		}},
	}
}

func TestClientEncodesEveryQuestionTypeOnTheDocumentedWire(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	client := newTestClient(t, &Budget{MaxRequests: 1, StopUSD: 1}, nil)
	body, err := client.Encode(Request{
		State: map[string]any{"ticket": "Help! My payouts have been failing for 3 days."},
		Questions: []Question{
			{ID: "is_urgent", Type: QuestionNoul, Instructions: "Does `ticket` convey urgency?"},
			{ID: "department", Type: QuestionChoice, Instructions: "Which team should handle `ticket`?",
				Criteria: map[string]any{"billing": "Payments", "technical": nil}},
			{ID: "frustration", Type: QuestionScore, Instructions: "How frustrated is the customer?",
				Criteria: []string{"Calm", "Frustrated", "Very angry"}},
		},
	})
	require.NoError(err)
	var got map[string]any
	require.NoError(json.Unmarshal(body, &got))
	assert.Equal(DefaultModel, got["model"])
	assert.Equal(map[string]any{"ticket": "Help! My payouts have been failing for 3 days."}, got["state"])
	questions, ok := got["questions"].(map[string]any)
	require.True(ok)
	assert.Equal(map[string]any{"type": "noul", "instructions": "Does `ticket` convey urgency?"}, questions["is_urgent"])
	assert.Equal(map[string]any{
		"type": "choice", "instructions": "Which team should handle `ticket`?",
		"criteria": map[string]any{"billing": "Payments", "technical": nil},
	}, questions["department"])
	assert.Equal(map[string]any{
		"type": "score", "instructions": "How frustrated is the customer?",
		"criteria": []any{"Calm", "Frustrated", "Very angry"},
	}, questions["frustration"])
}

func TestClientEncodeRejectsMalformedQuestions(t *testing.T) {
	client := newTestClient(t, &Budget{MaxRequests: 1, StopUSD: 1}, nil)
	cases := map[string]Request{
		"no state":          {Questions: []Question{{ID: "q", Type: QuestionNoul, Instructions: "?"}}},
		"no questions":      {State: "x"},
		"blank id":          {State: "x", Questions: []Question{{ID: " ", Type: QuestionNoul, Instructions: "?"}}},
		"duplicate id":      {State: "x", Questions: []Question{{ID: "q", Type: QuestionNoul, Instructions: "?"}, {ID: "q", Type: QuestionNoul, Instructions: "?"}}},
		"no instructions":   {State: "x", Questions: []Question{{ID: "q", Type: QuestionNoul}}},
		"choice criteria":   {State: "x", Questions: []Question{{ID: "q", Type: QuestionChoice, Instructions: "?"}}},
		"score criteria":    {State: "x", Questions: []Question{{ID: "q", Type: QuestionScore, Instructions: "?"}}},
		"unknown type":      {State: "x", Questions: []Question{{ID: "q", Type: "guess", Instructions: "?"}}},
		"oversized request": {State: strings.Repeat("x", DefaultMaxRequestBytes), Questions: []Question{{ID: "q", Type: QuestionNoul, Instructions: "?"}}},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := client.Encode(request)
			assert.ErrorIs(t, err, ErrRequestBounds)
		})
	}
}

func TestClientDecodesChoiceScoreAndNoulAnswers(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	client := newTestClient(t, &Budget{MaxRequests: 1, StopUSD: 1}, nil)
	response, err := client.Decode([]byte(`{
		"model":"jev-1.13.0",
		"answers":{
			"is_urgent":{"type":"noul","noul":0.95},
			"department":{"type":"choice","choice":"billing","probabilities":{"billing":0.88,"technical":0.12},"confidence":0.81},
			"frustration":{"type":"score","score":1.05,"legend":{"0":"Calm","1":"Frustrated"},"probabilities":{"0":0.0,"1":0.95,"2":0.05},"confidence":0.92}
		},
		"usage":{"input_tokens":296,"output_tokens":20}}`), []Question{
		{ID: "is_urgent", Type: QuestionNoul}, {ID: "department", Type: QuestionChoice}, {ID: "frustration", Type: QuestionScore},
	})
	require.NoError(err)
	assert.Equal(Answer{Type: QuestionNoul, Noul: 0.95}, response.Answers["is_urgent"])
	assert.Equal(Answer{
		Type: QuestionChoice, Choice: "billing",
		Probabilities: map[string]float64{"billing": 0.88, "technical": 0.12}, Confidence: 0.81,
	}, response.Answers["department"])
	assert.Equal(Answer{
		Type: QuestionScore, Score: 1.05,
		Probabilities: map[string]float64{"0": 0, "1": 0.95, "2": 0.05}, Confidence: 0.92,
	}, response.Answers["frustration"])
	assert.Equal(int64(296), *response.Usage.InputTokens)
	assert.True(response.Usage.Complete)
}

func TestClientDecodeRejectsInvalidAnswers(t *testing.T) {
	client := newTestClient(t, &Budget{MaxRequests: 1, StopUSD: 1}, nil)
	noul := []Question{{ID: "q", Type: QuestionNoul}}
	choice := []Question{{ID: "q", Type: QuestionChoice}}
	score := []Question{{ID: "q", Type: QuestionScore}}
	cases := map[string]struct {
		body      string
		questions []Question
	}{
		"malformed":            {`{`, noul},
		"other model":          {`{"model":"jev-2.0.0","answers":{"q":{"type":"noul","noul":0.5}}}`, noul},
		"missing answer":       {`{"model":"jev-1.13.0","answers":{"other":{"type":"noul","noul":0.5}}}`, noul},
		"extra answer":         {`{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":0.5},"x":{"type":"noul","noul":0.5}}}`, noul},
		"noul above one":       {`{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":1.5}}}`, noul},
		"choice not option":    {`{"model":"jev-1.13.0","answers":{"q":{"type":"choice","choice":"z","probabilities":{"a":1},"confidence":1}}}`, choice},
		"choice no confidence": {`{"model":"jev-1.13.0","answers":{"q":{"type":"choice","choice":"a","probabilities":{"a":1}}}}`, choice},
		"score negative":       {`{"model":"jev-1.13.0","answers":{"q":{"type":"score","score":-1,"probabilities":{"0":1},"confidence":1}}}`, score},
		"negative usage":       {`{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":0.5}},"usage":{"input_tokens":-1,"output_tokens":1}}`, noul},
		"unknown type":         {`{"model":"jev-1.13.0","answers":{"q":{"type":"guess"}}}`, noul},
		"score answer to a noul question": {
			`{"model":"jev-1.13.0","answers":{"q":{"type":"score","score":1,"probabilities":{"0":0,"1":1},"confidence":1}}}`, noul,
		},
		"choice answer to a noul question": {
			`{"model":"jev-1.13.0","answers":{"q":{"type":"choice","choice":"a","probabilities":{"a":1},"confidence":1}}}`, noul,
		},
		"noul answer to a choice question": {`{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":0.5}}}`, choice},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := client.Decode([]byte(tc.body), tc.questions)
			assert.ErrorIs(t, err, ErrInvalidResponse)
		})
	}
}

func TestClientBounds(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	budget := &Budget{MaxRequests: 0, StopUSD: 1}
	client := newTestClient(t, budget, func(*http.Request) (*http.Response, error) {
		return jsonResponse(""), nil
	})
	_, err := client.Ask(context.Background(), noulRequest("matches"))
	require.ErrorIs(err, ErrRequestLimit)
	assert.Zero(budget.State().Attempts, "the request limit is checked before egress")

	preflightBudget := &Budget{MaxRequests: 2, StopUSD: 1, attempts: 1}
	var preflightCalls atomic.Int32
	preflightClient := newTestClient(t, preflightBudget, func(*http.Request) (*http.Response, error) {
		preflightCalls.Add(1)
		return nil, errors.New("unexpected provider call")
	})
	_, err = preflightClient.AskAll(context.Background(), []Request{noulRequest("a"), noulRequest("b")})
	require.ErrorIs(err, ErrRequestLimit)
	assert.Equal(int32(0), preflightCalls.Load(), "remaining request capacity is checked before egress")

	responseClient := newTestClient(t, &Budget{MaxRequests: 1000, StopUSD: 1}, func(*http.Request) (*http.Response, error) {
		return jsonResponse(strings.Repeat("x", DefaultMaxResponseBytes+1)), nil
	})
	_, err = responseClient.Ask(context.Background(), noulRequest("matches"))
	require.ErrorIs(err, ErrInvalidResponse)
	assert.Contains(err.Error(), "65536")

	redirectClient := newTestClient(t, &Budget{MaxRequests: 1000, StopUSD: 1}, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://elsewhere.example.test/"}}, Body: http.NoBody}, nil
	})
	_, err = redirectClient.Ask(context.Background(), noulRequest("matches"))
	require.Error(err)
	assert.Equal("provider returned HTTP 302", SafeFailure(err))
}

func TestClientRejectsInvalidOptions(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	_, err := NewClient(Options{Budget: &Budget{}})
	require.ErrorContains(err, "API key")
	_, err = NewClient(Options{APIKey: "k"})
	require.ErrorContains(err, "budget")
	_, err = NewClient(Options{APIKey: "k", Budget: &Budget{}, Endpoint: "http://api.example.test/v1"})
	require.ErrorContains(err, "https")
	_, err = NewClient(Options{APIKey: "k", Budget: &Budget{}, Endpoint: "https://user:pw@api.example.test/v1"})
	require.ErrorContains(err, "credentials")
	_, err = NewClient(Options{APIKey: "k", Budget: &Budget{}, Endpoint: "https://api.example.test/v1 "})
	require.ErrorContains(err, "whitespace", "a padded endpoint would fail every send, so it never builds a client")
	padded := DefaultConfig()
	padded.Endpoint = "\thttps://api.example.test/v1"
	require.ErrorContains(padded.Validate(), "whitespace", "config validation refuses it up front")
	client, err := NewClient(Options{APIKey: "k", Budget: &Budget{}, Endpoint: "http://127.0.0.1:8080/v1/systemone"})
	require.NoError(err, "loopback HTTP is allowed for fake servers")
	assert.Equal(DefaultModel, client.model)
}

func TestClientAccountingStopsAtMeasuredCost(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	response := `{"model":"jev-1.13.0","answers":{"matches":{"type":"noul","noul":0.37}},"usage":{"input_tokens":438,"output_tokens":40}}`
	budget := &Budget{MaxRequests: 10, StopUSD: 0.0005, InputUSDPerM: 1, OutputUSDPerM: 2}
	var requests atomic.Int32
	client := newTestClient(t, budget, func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return jsonResponse(response), nil
	})
	result, err := client.Ask(context.Background(), noulRequest("matches"))
	require.NoError(err)
	assert.InDelta(0.37, result.Answers["matches"].Noul, 1e-9)
	assert.Equal(1, result.Usage.Requests)
	assert.Equal(int64(438), *result.Usage.InputTokens)
	assert.Equal(int64(40), *result.Usage.OutputTokens)
	assert.InDelta(0.000518, budget.State().CostUSD, 1e-9)
	_, err = client.Ask(context.Background(), noulRequest("matches"))
	require.ErrorIs(err, ErrCostStop)
	assert.Equal(int32(1), requests.Load(), "a measured cost stop prevents another provider call")
}

func TestClientFailureReturnsAttemptedCallsAndPartialUsage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		response := `{"model":"jev-1.13.0","answers":{"matches":{"type":"noul","noul":0.15}},"usage":{"input_tokens":342,"output_tokens":20}}`
		budget := &Budget{MaxRequests: 10, StopUSD: 1, InputUSDPerM: 1, OutputUSDPerM: 1}
		var requests atomic.Int32
		client := newTestClient(t, budget, func(*http.Request) (*http.Response, error) {
			if requests.Add(1) == 1 {
				return jsonResponse(response), nil
			}
			// Let the successful call finish accounting before this failure cancels its group.
			synctest.Wait()
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("private provider body"))}, nil
		})
		result, err := client.AskAll(context.Background(), []Request{noulRequest("matches"), noulRequest("matches")})
		require.ErrorContains(err, "HTTP 503")
		assert.Equal(2, result.Usage.Requests)
		assert.Equal(int64(342), *result.Usage.InputTokens)
		assert.Equal(int64(20), *result.Usage.OutputTokens)
		assert.False(result.Usage.Complete)
		assert.Equal("provider returned HTTP 503", SafeFailure(err))
		assert.NotContains(err.Error(), "private provider body")
		assert.NotContains(err.Error(), "secret-key")
	})
}

type contextErrorBody struct{ ctx context.Context }

func (b contextErrorBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (contextErrorBody) Close() error { return nil }

func TestClientBodyReadTimeoutKeepsTimeoutCategory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		client := newTestClient(t, &Budget{MaxRequests: 1, StopUSD: 1}, func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       contextErrorBody{ctx: request.Context()},
			}, nil
		})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		start := time.Now()
		_, err := client.Ask(ctx, noulRequest("matches"))
		require.ErrorIs(err, context.DeadlineExceeded)
		assert.Equal("provider timeout or cancellation", SafeFailure(err))
		assert.Equal(DefaultRequestTimeout, time.Since(start))
		assert.Equal(1, client.BudgetState().ConsecutiveFailures,
			"a per-request timeout with a live caller context is a provider failure")
	})
}

func TestClientRequestDeadlineShortensTheTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		budget := &Budget{MaxRequests: 10, StopUSD: 1, FailureThreshold: 1}
		client := newTestClient(t, budget, func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       contextErrorBody{ctx: request.Context()},
			}, nil
		})
		request := noulRequest("matches")
		request.Deadline = time.Now().Add(800 * time.Millisecond)
		start := time.Now()
		_, err := client.Ask(context.Background(), request)
		require.ErrorIs(err, context.DeadlineExceeded)
		assert.Equal(800*time.Millisecond, time.Since(start))
		assert.Zero(budget.State().ConsecutiveFailures,
			"a caller deadline shorter than the client timeout is the caller's latency budget, not a provider failure")
		assert.True(budget.State().OpenUntil.IsZero())

		late := noulRequest("matches")
		late.Deadline = time.Now().Add(time.Minute)
		_, err = client.Ask(context.Background(), late)
		require.ErrorIs(err, context.DeadlineExceeded)
		assert.Equal(1, budget.State().ConsecutiveFailures,
			"a caller deadline beyond the client timeout leaves the client timeout in charge, which does count")
	})
}

func TestClientMissingUsageStopsFurtherCalls(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	response := `{"model":"jev-1.13.0","answers":{"matches":{"type":"noul","noul":0.75}}}`
	var requests atomic.Int32
	client := newTestClient(t, &Budget{MaxRequests: 10, StopUSD: 1, InputUSDPerM: 1}, func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return jsonResponse(response), nil
	})
	result, err := client.Ask(context.Background(), noulRequest("matches"))
	require.NoError(err)
	assert.InDelta(0.75, result.Answers["matches"].Noul, 1e-9)
	assert.Nil(result.Usage.InputTokens)
	assert.False(result.Usage.Complete)
	_, err = client.Ask(context.Background(), noulRequest("matches"))
	require.ErrorIs(err, ErrUsageUnknown)
	assert.Equal(int32(1), requests.Load(), "unknown usage prevents another provider call")
}

func TestClientFailureRedactsProviderBody(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	client := newTestClient(t, &Budget{MaxRequests: 10, StopUSD: 1}, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("candidate secret body"))}, nil
	})
	_, err := client.Ask(context.Background(), noulRequest("matches"))
	require.Error(err)
	assert.NotContains(err.Error(), "candidate secret body")
	assert.NotContains(err.Error(), "secret")
}

func TestSafeFailureCategories(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{ErrRequestLimit, "request limit reached"},
		{ErrCostStop, "local cost stop reached"},
		{ErrUsageUnknown, "provider usage unavailable"},
		{context.DeadlineExceeded, "provider timeout or cancellation"},
		{context.Canceled, "provider timeout or cancellation"},
		{httpStatusError(503), "provider returned HTTP 503"},
		{ErrInvalidResponse, "invalid provider response"},
		{ErrRequestBounds, "request bounds exceeded"},
		{ErrBreakerOpen, "provider circuit breaker open"},
		{ErrRunHalted, "provider failed; no further requests will start"},
		{ErrDayRequestLimit, "daily request limit reached"},
		{ErrDayCostStop, "daily cost limit reached"},
		{errors.New("request limit reached: secret"), "provider request failed"},
		{errors.New("provider returned HTTP 503 secret"), "provider request failed"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			// Misleading wrapper text must not change classification or reach the report.
			got := SafeFailure(fmt.Errorf("response query exceeds secret: %w", tc.err))
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestBatchRequestsSplitsUntilEveryRequestFits(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	client, err := NewClient(Options{APIKey: "k", Budget: &Budget{MaxRequests: 1, StopUSD: 1}, MaxRequestBytes: 500})
	require.NoError(err)
	items := make([]string, 8)
	for i := range items {
		items[i] = strings.Repeat("x", 100)
	}
	build := func(chunk []string, offset int) Request {
		questions := make([]Question, len(chunk))
		for i := range chunk {
			questions[i] = Question{ID: fmt.Sprintf("item_%d", offset+i), Type: QuestionNoul, Instructions: "?"}
		}
		return Request{State: map[string]any{"items": chunk}, Questions: questions}
	}
	requests, err := BatchRequests(client, items, build)
	require.NoError(err)
	assert.Len(requests, 4)
	for _, request := range requests {
		body, err := client.Encode(request)
		require.NoError(err)
		assert.LessOrEqual(len(body), 500)
	}
	assert.Equal("item_2", requests[1].Questions[0].ID, "offsets stay aligned to the original items")

	_, err = BatchRequests(client, []string{strings.Repeat("x", 800)}, build)
	require.ErrorIs(err, ErrRequestBounds)
	empty, err := BatchRequests(client, nil, build)
	require.NoError(err)
	assert.Empty(empty)
}
