// Package jev is the shared client for TypeSafe's System One model (Jev). It
// owns transport, request and response bounds, budgets, and the three typed
// question kinds (Choice, Noul, Score). Feature packages build a small state
// object, ask independent questions in one request, and apply their own
// thresholds to the probabilities that come back. Nothing here decides what a
// judgment means.
package jev

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	// DefaultEndpoint is the pinned System One evaluation endpoint.
	DefaultEndpoint = "https://api.typesafe.ai/v1/systemone"
	// DefaultModel is the pinned model. Responses from any other model are
	// rejected so a silent upgrade cannot change judgments.
	DefaultModel = "jev-1.13.0"
	// DefaultRequestTimeout bounds one HTTP exchange.
	DefaultRequestTimeout = 10 * time.Second
	// DefaultMaxRequestBytes caps one encoded request body.
	DefaultMaxRequestBytes = 128 << 10
	// DefaultMaxResponseBytes caps one response body.
	DefaultMaxResponseBytes = 64 << 10
	// DefaultMaxConcurrent bounds fan-out inside AskAll.
	DefaultMaxConcurrent = 8
)

// QuestionType selects one of the three System One primitives.
type QuestionType string

const (
	// QuestionChoice picks one option from criteria and returns a distribution.
	QuestionChoice QuestionType = "choice"
	// QuestionNoul answers a yes/no question with a probability of yes.
	QuestionNoul QuestionType = "noul"
	// QuestionScore rates the state along ordered levels.
	QuestionScore QuestionType = "score"
)

// Failure categories let callers report errors without exposing provider
// content, credentials, or state.
var (
	ErrRequestLimit    = errors.New("request limit reached")
	ErrCostStop        = errors.New("local cost stop reached")
	ErrUsageUnknown    = errors.New("provider usage unavailable")
	ErrRequestBounds   = errors.New("request bounds exceeded")
	ErrInvalidResponse = errors.New("invalid provider response")
)

type httpStatusError int

func (e httpStatusError) Error() string { return fmt.Sprintf("provider returned HTTP %d", int(e)) }

// Question is one typed question keyed by ID inside a request. Instructions
// and Criteria accept a string, an object, or an array exactly as the API
// does; Criteria is omitted from the wire when nil.
type Question struct {
	ID           string
	Type         QuestionType
	Instructions any
	Criteria     any
}

// NoulCriteria describes what yes and no mean for a Noul.
type NoulCriteria struct {
	True  any `json:"true"`
	False any `json:"false"`
}

// Answer is one decoded answer. Fields outside the answer's Type are zero.
type Answer struct {
	Type          QuestionType
	Choice        string
	Probabilities map[string]float64
	Confidence    float64
	Score         float64
	Noul          float64
}

// Request is one evaluation of a state against a set of questions. Deadline,
// when set, bounds the exchange in addition to the caller's context and the
// client's per-request timeout. Feature names the daily counter the request
// is charged to and is required when the client has a Ledger.
type Request struct {
	State     any
	Questions []Question
	Deadline  time.Time
	Feature   string
}

// Usage records attempted requests and provider token accounting. Complete is
// false when at least one attempt lacks usage, so token counts are subtotals.
type Usage struct {
	Requests     int
	InputTokens  *int64
	OutputTokens *int64
	Complete     bool
}

// Response carries every answer keyed by question ID plus usage.
type Response struct {
	Model   string
	Answers map[string]Answer
	Usage   Usage
}

// BatchResult is the outcome of AskAll. Responses aligns with the requests;
// a failed request leaves a nil entry. Usage is the aggregate of every
// attempted request.
type BatchResult struct {
	Responses []*Response
	Usage     Usage
}

// Options configure a client. Zero values take the pinned defaults. Ledger
// and DayLimits add persisted per-feature daily accounting on top of the
// in-process Budget; Now lets tests pick the day.
type Options struct {
	Endpoint         string
	Model            string
	APIKey           string
	Transport        http.RoundTripper
	Budget           *Budget
	Ledger           Ledger
	DayLimits        DayLimits
	Now              func() time.Time
	RequestTimeout   time.Duration
	MaxRequestBytes  int
	MaxResponseBytes int
	// MaxConcurrent bounds AskAll fan-out; zero takes DefaultMaxConcurrent.
	MaxConcurrent int
}

// Client sends bounded System One requests.
type Client struct {
	endpoint      string
	model         string
	key           string
	client        *http.Client
	budget        *Budget
	ledger        Ledger
	dayLimits     DayLimits
	now           func() time.Time
	timeout       time.Duration
	maxRequest    int
	maxResponse   int
	maxConcurrent int
	// inputUSDPerM and outputUSDPerM are the budget's prices when the client
	// was built. The day ledger prices each request with them rather than
	// reading the shared budget unlocked while a service rebinds it.
	inputUSDPerM  float64
	outputUSDPerM float64
}

// errNotSent marks a request the client refused to dispatch because its
// context was already done. Nothing left the process, so its reservations
// are returned and it neither counts toward the breaker nor the ledger.
var errNotSent = errors.New("jev request was not sent")

// NewClient validates the options and builds a client. Construction performs
// no I/O.
func NewClient(options Options) (*Client, error) {
	if strings.TrimSpace(options.APIKey) == "" {
		return nil, errors.New("jev API key is required")
	}
	if options.Budget == nil {
		return nil, errors.New("jev budget is required")
	}
	endpoint := options.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	if err := ValidateEndpoint(endpoint); err != nil {
		return nil, err
	}
	model := options.Model
	if model == "" {
		model = DefaultModel
	}
	if strings.TrimSpace(model) != model || model == "" {
		return nil, errors.New("jev model is invalid")
	}
	timeout := options.RequestTimeout
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	maxRequest := options.MaxRequestBytes
	if maxRequest <= 0 {
		maxRequest = DefaultMaxRequestBytes
	}
	maxResponse := options.MaxResponseBytes
	if maxResponse <= 0 {
		maxResponse = DefaultMaxResponseBytes
	}
	maxConcurrent := options.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrent
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	inputUSDPerM, outputUSDPerM := options.Budget.prices()
	return &Client{
		endpoint: endpoint, model: model, key: options.APIKey,
		client: &http.Client{Transport: options.Transport, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		budget: options.Budget, ledger: options.Ledger, dayLimits: options.DayLimits, now: now,
		timeout: timeout, maxRequest: maxRequest, maxResponse: maxResponse, maxConcurrent: maxConcurrent,
		inputUSDPerM: inputUSDPerM, outputUSDPerM: outputUSDPerM,
	}, nil
}

// BudgetState snapshots the in-process budget and breaker for status output.
func (c *Client) BudgetState() BudgetState { return c.budget.State() }

// reserveDay counts one request against the feature's persisted day before
// any bytes leave the process.
func (c *Client) reserveDay(ctx context.Context, feature string) (string, error) {
	if c.ledger == nil {
		return "", nil
	}
	if !ValidFeatureName(feature) {
		return "", fmt.Errorf("%w: a ledgered request needs a feature name", ErrRequestBounds)
	}
	day := UTCDay(c.now())
	if err := c.ledger.ReserveJevDayRequest(ctx, DayReservation{Feature: feature, UTCDay: day, Limits: c.dayLimits}); err != nil {
		return "", err
	}
	return day, nil
}

// callerBound reports whether a request's own deadline would expire before
// the client's per-request timeout. Such a deadline belongs to the caller's
// latency budget, not to the provider's health, so running out of it is not
// a breaker failure.
func (c *Client) callerBound(deadline, started time.Time) bool {
	return !deadline.IsZero() && deadline.Before(started.Add(c.timeout))
}

// releaseDay returns a day reservation for a request that never left the
// process.
func (c *Client) releaseDay(ctx context.Context, feature, day string) {
	if c.ledger == nil {
		return
	}
	if err := c.ledger.ReleaseJevDayRequest(context.WithoutCancel(ctx), DayReservation{
		Feature: feature, UTCDay: day, Limits: c.dayLimits,
	}); err != nil {
		slog.Warn("jev day reservation was not released", "feature", feature, "utc_day", day, "error", err.Error())
	}
}

// recordDay persists what a completed request measured. A ledger write
// failure is logged, not returned: the answer was paid for and the caller
// still gets it.
func (c *Client) recordDay(ctx context.Context, feature, day string, usage Usage) {
	if c.ledger == nil {
		return
	}
	record := DayUsage{Feature: feature, UTCDay: day, UsageKnown: usage.Complete}
	if usage.Complete {
		record.InputTokens = *usage.InputTokens
		record.OutputTokens = *usage.OutputTokens
		record.CostUSDMicros = CostUSDMicros(record.InputTokens, record.OutputTokens,
			c.inputUSDPerM, c.outputUSDPerM)
	}
	if err := c.ledger.RecordJevDayUsage(context.WithoutCancel(ctx), record); err != nil {
		slog.Warn("jev day usage was not recorded", "feature", feature, "utc_day", day, "error", err.Error())
	}
}

type wireRequest struct {
	State     any                     `json:"state"`
	Model     string                  `json:"model"`
	Questions map[string]wireQuestion `json:"questions"`
}

type wireQuestion struct {
	Type         QuestionType `json:"type"`
	Instructions any          `json:"instructions"`
	Criteria     any          `json:"criteria,omitzero"`
}

type wireResponse struct {
	Model   string                `json:"model"`
	Answers map[string]wireAnswer `json:"answers"`
	Usage   *wireUsage            `json:"usage"`
}

type wireAnswer struct {
	Type          QuestionType        `json:"type"`
	Noul          *float64            `json:"noul"`
	Choice        *string             `json:"choice"`
	Score         *float64            `json:"score"`
	Probabilities map[string]*float64 `json:"probabilities"`
	Confidence    *float64            `json:"confidence"`
}

type wireUsage struct {
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
}

// Encode validates a request and returns its wire body. It is exported so
// callers can size batches and tests can assert the exact wire shape.
func (c *Client) Encode(request Request) ([]byte, error) {
	if request.State == nil {
		return nil, fmt.Errorf("%w: request has no state", ErrRequestBounds)
	}
	if len(request.Questions) == 0 {
		return nil, fmt.Errorf("%w: request has no questions", ErrRequestBounds)
	}
	questions := make(map[string]wireQuestion, len(request.Questions))
	for i, question := range request.Questions {
		if question.ID == "" || strings.TrimSpace(question.ID) != question.ID {
			return nil, fmt.Errorf("%w: question %d has an invalid id", ErrRequestBounds, i)
		}
		if _, duplicate := questions[question.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate question id", ErrRequestBounds)
		}
		if question.Instructions == nil {
			return nil, fmt.Errorf("%w: question %d has no instructions", ErrRequestBounds, i)
		}
		switch question.Type {
		case QuestionNoul:
		case QuestionChoice, QuestionScore:
			if question.Criteria == nil {
				return nil, fmt.Errorf("%w: question %d requires criteria", ErrRequestBounds, i)
			}
		default:
			return nil, fmt.Errorf("%w: question %d has an unknown type", ErrRequestBounds, i)
		}
		questions[question.ID] = wireQuestion{
			Type: question.Type, Instructions: question.Instructions, Criteria: question.Criteria,
		}
	}
	body, err := json.Marshal(wireRequest{State: request.State, Model: c.model, Questions: questions}, json.Deterministic(true))
	if err != nil {
		return nil, errors.New("encode Jev request")
	}
	if len(body) > c.maxRequest {
		return nil, fmt.Errorf("%w: encoded Jev request exceeds %d bytes", ErrRequestBounds, c.maxRequest)
	}
	return body, nil
}

// Ask sends one request and returns its answers. The budget is consulted
// before any bytes leave the process.
func (c *Client) Ask(ctx context.Context, request Request) (Response, error) {
	body, err := c.Encode(request)
	if err != nil {
		return emptyResponse(), err
	}
	if err := c.budget.preflight(1); err != nil {
		return emptyResponse(), err
	}
	if err := ctx.Err(); err != nil {
		return emptyResponse(), err
	}
	// The in-process budget is reserved first: a breaker or cost stop must
	// never touch the persisted day counters. A refused day reservation
	// releases the in-process slot so neither count drifts.
	admitted, err := c.budget.reserve()
	if err != nil {
		return emptyResponse(), err
	}
	day, err := c.reserveDay(ctx, request.Feature)
	if err != nil {
		c.budget.release(admitted)
		return emptyResponse(), err
	}
	started := c.now()
	response, err := c.send(ctx, request.Deadline, body, request.Questions)
	if errors.Is(err, errNotSent) {
		c.budget.release(admitted)
		c.releaseDay(ctx, request.Feature, day)
		return emptyResponse(), ctx.Err()
	}
	if err != nil {
		c.budget.outcome(ctx, admitted, err, c.callerBound(request.Deadline, started))
		c.recordDay(ctx, request.Feature, day, Usage{})
		return Response{Usage: Usage{Requests: 1}}, err
	}
	c.budget.record(admitted, response.Usage)
	c.recordDay(ctx, request.Feature, day, response.Usage)
	response.Usage.Requests = 1
	return response, nil
}

// AskAll sends several independent requests concurrently and aggregates
// usage. The first failure cancels the remaining requests; responses that
// completed before it are kept so callers can account for their usage.
func (c *Client) AskAll(ctx context.Context, requests []Request) (BatchResult, error) {
	bodies := make([][]byte, len(requests))
	for i, request := range requests {
		body, err := c.Encode(request)
		if err != nil {
			return emptyBatch(), err
		}
		bodies[i] = body
	}
	if err := c.budget.preflight(len(requests)); err != nil {
		return emptyBatch(), err
	}
	result := BatchResult{Responses: make([]*Response, len(requests))}
	var mu sync.Mutex
	var totalInput, totalOutput int64
	complete := true
	first := 0
	if c.budget.halfOpen() && len(requests) > 1 {
		// A half-open breaker admits exactly one probe. Run it alone so the
		// siblings neither get refused with ErrBreakerOpen nor cancel it
		// through the group; the rest of the batch runs once it closes.
		probe, err := c.Ask(ctx, requests[0])
		result.Usage.Requests = probe.Usage.Requests
		if err != nil {
			result.Usage.InputTokens, result.Usage.OutputTokens = &totalInput, &totalOutput
			return result, fmt.Errorf("jev requests failed: %w", err)
		}
		result.Responses[0] = &probe
		addUsage(&totalInput, &totalOutput, &complete, probe.Usage)
		first = 1
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(c.maxConcurrent)
	for i := first; i < len(bodies); i++ {
		body := bodies[i]
		group.Go(func() error {
			// A sibling's failure cancels the group; a request that has not
			// reserved anything yet must not start charging the day.
			if err := groupCtx.Err(); err != nil {
				return err
			}
			admitted, err := c.budget.reserve()
			if err != nil {
				return err
			}
			day, err := c.reserveDay(groupCtx, requests[i].Feature)
			if err != nil {
				c.budget.release(admitted)
				return err
			}
			started := c.now()
			response, err := c.send(groupCtx, requests[i].Deadline, body, requests[i].Questions)
			if errors.Is(err, errNotSent) {
				c.budget.release(admitted)
				c.releaseDay(groupCtx, requests[i].Feature, day)
				return groupCtx.Err()
			}
			mu.Lock()
			result.Usage.Requests++
			mu.Unlock()
			if err != nil {
				mu.Lock()
				complete = false
				mu.Unlock()
				c.budget.outcome(groupCtx, admitted, err, c.callerBound(requests[i].Deadline, started))
				c.recordDay(groupCtx, requests[i].Feature, day, Usage{})
				return err
			}
			c.budget.record(admitted, response.Usage)
			c.recordDay(groupCtx, requests[i].Feature, day, response.Usage)
			mu.Lock()
			defer mu.Unlock()
			response.Usage.Requests = 1
			result.Responses[i] = &response
			addUsage(&totalInput, &totalOutput, &complete, response.Usage)
			return nil
		})
	}
	groupErr := group.Wait()
	result.Usage.InputTokens = &totalInput
	result.Usage.OutputTokens = &totalOutput
	result.Usage.Complete = complete
	if groupErr != nil {
		return result, fmt.Errorf("jev requests failed: %w", groupErr)
	}
	return result, nil
}

// addUsage folds one response's token counts into a batch total; a missing
// count marks the total incomplete.
func addUsage(totalInput, totalOutput *int64, complete *bool, usage Usage) {
	if usage.InputTokens == nil {
		*complete = false
	} else {
		*totalInput += *usage.InputTokens
	}
	if usage.OutputTokens == nil {
		*complete = false
	} else {
		*totalOutput += *usage.OutputTokens
	}
}

func emptyResponse() Response {
	input, output := int64(0), int64(0)
	return Response{Usage: Usage{InputTokens: &input, OutputTokens: &output, Complete: true}}
}

func emptyBatch() BatchResult {
	input, output := int64(0), int64(0)
	return BatchResult{Usage: Usage{InputTokens: &input, OutputTokens: &output, Complete: true}}
}

// SafeFailure reports a known error category without including state, message
// text, credentials, or provider response bodies.
func SafeFailure(err error) string {
	for _, category := range []error{
		ErrRequestLimit, ErrCostStop, ErrUsageUnknown, ErrRequestBounds, ErrInvalidResponse,
		ErrBreakerOpen, ErrRunHalted, ErrDayRequestLimit, ErrDayCostStop,
	} {
		if errors.Is(err, category) {
			return category.Error()
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "provider timeout or cancellation"
	}
	if status, ok := errors.AsType[httpStatusError](err); ok {
		return status.Error()
	}
	return "provider request failed"
}

func (c *Client) send(ctx context.Context, deadline time.Time, body []byte, questions []Question) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, fmt.Errorf("%w: %w", errNotSent, err)
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if !deadline.IsZero() {
		var cancelDeadline context.CancelFunc
		ctx, cancelDeadline = context.WithDeadline(ctx, deadline)
		defer cancelDeadline()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Response{}, errors.New("construct Jev request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	response, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, errors.New("provider request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return Response{}, httpStatusError(response.StatusCode)
	}
	mediaType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseErr != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
		return Response{}, fmt.Errorf("%w: non-JSON content type", ErrInvalidResponse)
	}
	limited := io.LimitReader(response.Body, int64(c.maxResponse)+1)
	body, err = io.ReadAll(limited)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, fmt.Errorf("%w: cannot read body", ErrInvalidResponse)
	}
	if len(body) > c.maxResponse {
		return Response{}, fmt.Errorf("%w: response exceeds %d bytes", ErrInvalidResponse, c.maxResponse)
	}
	return c.Decode(body, questions)
}

// Decode validates a response body against the questions that were sent.
// Every answer must be present, of the asked type, and carry finite
// probabilities.
func (c *Client) Decode(data []byte, questions []Question) (Response, error) {
	var wire wireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return Response{}, fmt.Errorf("%w: malformed JSON", ErrInvalidResponse)
	}
	if wire.Model != c.model || len(wire.Answers) != len(questions) {
		return Response{}, fmt.Errorf("%w: model or answer count did not match request", ErrInvalidResponse)
	}
	answers := make(map[string]Answer, len(questions))
	for _, question := range questions {
		raw, ok := wire.Answers[question.ID]
		if !ok {
			return Response{}, fmt.Errorf("%w: missing answer", ErrInvalidResponse)
		}
		if raw.Type != question.Type {
			return Response{}, fmt.Errorf("%w: answer type does not match the asked question", ErrInvalidResponse)
		}
		answer, err := decodeAnswer(raw)
		if err != nil {
			return Response{}, err
		}
		answers[question.ID] = answer
	}
	var usage Usage
	if wire.Usage != nil {
		if wire.Usage.InputTokens != nil && *wire.Usage.InputTokens < 0 ||
			wire.Usage.OutputTokens != nil && *wire.Usage.OutputTokens < 0 {
			return Response{}, fmt.Errorf("%w: invalid token usage", ErrInvalidResponse)
		}
		usage.InputTokens = validTokenPointer(wire.Usage.InputTokens)
		usage.OutputTokens = validTokenPointer(wire.Usage.OutputTokens)
	}
	usage.Complete = usage.InputTokens != nil && usage.OutputTokens != nil
	return Response{Model: wire.Model, Answers: answers, Usage: usage}, nil
}

func decodeAnswer(raw wireAnswer) (Answer, error) {
	switch raw.Type {
	case QuestionNoul:
		if !validProbability(raw.Noul) {
			return Answer{}, fmt.Errorf("%w: invalid noul answer", ErrInvalidResponse)
		}
		return Answer{Type: QuestionNoul, Noul: *raw.Noul}, nil
	case QuestionChoice:
		probabilities, err := decodeProbabilities(raw.Probabilities)
		if err != nil {
			return Answer{}, err
		}
		if raw.Choice == nil || !validProbability(raw.Confidence) {
			return Answer{}, fmt.Errorf("%w: invalid choice answer", ErrInvalidResponse)
		}
		if _, ok := probabilities[*raw.Choice]; !ok {
			return Answer{}, fmt.Errorf("%w: choice is not an option", ErrInvalidResponse)
		}
		return Answer{
			Type: QuestionChoice, Choice: *raw.Choice, Probabilities: probabilities,
			Confidence: *raw.Confidence,
		}, nil
	case QuestionScore:
		probabilities, err := decodeProbabilities(raw.Probabilities)
		if err != nil {
			return Answer{}, err
		}
		if raw.Score == nil || math.IsNaN(*raw.Score) || math.IsInf(*raw.Score, 0) ||
			*raw.Score < 0 || !validProbability(raw.Confidence) {
			return Answer{}, fmt.Errorf("%w: invalid score answer", ErrInvalidResponse)
		}
		return Answer{
			Type: QuestionScore, Score: *raw.Score, Probabilities: probabilities,
			Confidence: *raw.Confidence,
		}, nil
	default:
		return Answer{}, fmt.Errorf("%w: unknown answer type", ErrInvalidResponse)
	}
}

func decodeProbabilities(raw map[string]*float64) (map[string]float64, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: missing probabilities", ErrInvalidResponse)
	}
	probabilities := make(map[string]float64, len(raw))
	for option, value := range raw {
		if !validProbability(value) {
			return nil, fmt.Errorf("%w: invalid probability", ErrInvalidResponse)
		}
		probabilities[option] = *value
	}
	return probabilities, nil
}

func validProbability(value *float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0 && *value <= 1
}

func validTokenPointer(value *int64) *int64 {
	if value == nil || *value < 0 {
		return nil
	}
	usage := *value
	return &usage
}

// ValidateEndpoint accepts only an absolute HTTPS URL without credentials,
// query, fragment, or surrounding whitespace. Tests may use plain HTTP against
// loopback hosts. Whitespace is rejected rather than trimmed because the
// client sends the endpoint exactly as configured: a padded value would pass
// here and then fail every request, opening the breaker.
func ValidateEndpoint(endpoint string) error {
	if strings.TrimSpace(endpoint) != endpoint {
		return errors.New("jev endpoint must not have surrounding whitespace")
	}
	origin, err := EndpointOrigin(endpoint)
	if err != nil {
		return err
	}
	if strings.HasPrefix(origin, "http://") && !loopbackOrigin(origin) {
		return errors.New("jev endpoint must use https")
	}
	return nil
}
