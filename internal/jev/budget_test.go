package jev

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const measuredResponse = `{"model":"jev-1.13.0","answers":{"matches":{"type":"noul","noul":0.5}},"usage":{"input_tokens":100,"output_tokens":10}}`

func TestBudgetBreakerOpensAfterConsecutiveFailuresAndProbesAfterCooldown(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	budget := &Budget{
		MaxRequests: 100, FailureThreshold: 2, Cooldown: time.Minute,
		Now: func() time.Time { return now },
	}
	var failing atomic.Bool
	failing.Store(true)
	var calls atomic.Int32
	client := newTestClient(t, budget, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		if failing.Load() {
			return nil, errors.New("connection refused")
		}
		return jsonResponse(measuredResponse), nil
	})

	for range 2 {
		_, err := client.Ask(context.Background(), noulRequest("matches"))
		require.Error(err)
	}
	assert.Equal(int32(2), calls.Load())
	_, err := client.Ask(context.Background(), noulRequest("matches"))
	require.ErrorIs(err, ErrBreakerOpen, "the threshold opens the breaker")
	assert.Equal("provider circuit breaker open", SafeFailure(err))
	assert.Equal(int32(2), calls.Load(), "an open breaker sends nothing")
	state := budget.State()
	assert.Equal(2, state.ConsecutiveFailures)
	assert.Equal(now.Add(time.Minute), state.OpenUntil)

	now = now.Add(30 * time.Second)
	_, err = client.Ask(context.Background(), noulRequest("matches"))
	require.ErrorIs(err, ErrBreakerOpen, "still cooling down")

	now = now.Add(31 * time.Second)
	_, err = client.Ask(context.Background(), noulRequest("matches"))
	require.Error(err, "the half-open probe fails")
	assert.Equal(int32(3), calls.Load(), "half-open lets exactly one probe through")
	_, err = client.Ask(context.Background(), noulRequest("matches"))
	require.ErrorIs(err, ErrBreakerOpen, "a failed probe reopens the breaker")

	now = now.Add(2 * time.Minute)
	failing.Store(false)
	response, err := client.Ask(context.Background(), noulRequest("matches"))
	require.NoError(err, "a successful probe closes the breaker")
	assert.InDelta(0.5, response.Answers["matches"].Noul, 1e-9)
	state = budget.State()
	assert.Zero(state.ConsecutiveFailures)
	assert.True(state.OpenUntil.IsZero())
	_, err = client.Ask(context.Background(), noulRequest("matches"))
	require.NoError(err)
	assert.Equal(int32(5), calls.Load())
}

func TestAskAllSiblingCancellationDoesNotCountTowardTheBreaker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		budget := &Budget{MaxRequests: 100, FailureThreshold: 3, Cooldown: time.Minute}
		var calls atomic.Int32
		client := newTestClient(t, budget, func(request *http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: http.NoBody}, nil
			}
			// Every other request waits until the group cancels it.
			<-request.Context().Done()
			return nil, request.Context().Err()
		})
		requests := make([]Request, 8)
		for i := range requests {
			requests[i] = noulRequest("matches")
		}
		_, err := client.AskAll(context.Background(), requests)
		require.ErrorContains(err, "HTTP 503")
		// Siblings that had already dispatched are cancelled by the group;
		// siblings that had not are never sent at all. Neither counts.
		assert.GreaterOrEqual(calls.Load(), int32(1))
		assert.LessOrEqual(calls.Load(), int32(8))
		state := budget.State()
		assert.Equal(1, state.ConsecutiveFailures, "one real failure; cancelled or unsent siblings do not count")
		assert.True(state.OpenUntil.IsZero(), "the breaker stays closed")
	})
}

func TestAskAllHalfOpenProbeClosesTheBreakerForTheWholeBatch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	budget := &Budget{MaxRequests: 100, FailureThreshold: 3, Cooldown: time.Minute, Now: func() time.Time { return now }}
	var healthy atomic.Bool
	var calls atomic.Int32
	client := newTestClient(t, budget, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		if !healthy.Load() {
			return nil, errors.New("connection refused")
		}
		return jsonResponse(measuredResponse), nil
	})
	for range 3 {
		_, err := client.Ask(context.Background(), noulRequest("matches"))
		require.Error(err)
	}
	require.False(budget.State().OpenUntil.IsZero(), "three failures open the breaker")

	now = now.Add(2 * time.Minute)
	healthy.Store(true)
	requests := make([]Request, 8)
	for i := range requests {
		requests[i] = noulRequest("matches")
	}
	batch, err := client.AskAll(context.Background(), requests)
	require.NoError(err, "the probe runs alone, succeeds, and the rest of the batch follows")
	assert.Equal(8, batch.Usage.Requests)
	for i, response := range batch.Responses {
		require.NotNil(response, "response %d", i)
	}
	assert.Equal(int32(11), calls.Load())
	state := budget.State()
	assert.True(state.OpenUntil.IsZero(), "the breaker is closed")
	assert.Zero(state.ConsecutiveFailures)

	batch, err = client.AskAll(context.Background(), requests[:2])
	require.NoError(err, "later batches run normally")
	assert.Equal(2, batch.Usage.Requests)
}

func TestAskAllHalfOpenProbeFailureReopensWithoutSendingSiblings(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	budget := &Budget{MaxRequests: 100, FailureThreshold: 1, Cooldown: time.Minute, Now: func() time.Time { return now }}
	var calls atomic.Int32
	client := newTestClient(t, budget, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("connection refused")
	})
	_, err := client.Ask(context.Background(), noulRequest("matches"))
	require.Error(err)
	now = now.Add(2 * time.Minute)
	requests := []Request{noulRequest("matches"), noulRequest("matches"), noulRequest("matches")}
	batch, err := client.AskAll(context.Background(), requests)
	require.Error(err)
	assert.Equal(1, batch.Usage.Requests, "only the probe was attempted")
	assert.Equal(int32(2), calls.Load())
	assert.Equal(now.Add(time.Minute), budget.State().OpenUntil, "a failed probe reopens for another cool-down")
}

func TestAskCallerCancellationDoesNotCountTowardTheBreaker(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	budget := &Budget{MaxRequests: 100, FailureThreshold: 1, Cooldown: time.Minute}
	client := newTestClient(t, budget, func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Ask(ctx, noulRequest("matches"))
	require.ErrorIs(err, context.Canceled)
	assert.Zero(budget.State().ConsecutiveFailures)
	assert.True(budget.State().OpenUntil.IsZero())
}

func TestBudgetHalfOpenAllowsOneProbeAtATime(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	budget := &Budget{MaxRequests: 100, FailureThreshold: 1, Cooldown: time.Second, Now: func() time.Time { return now }}
	require.NoError(budget.reserve())
	budget.fail()
	now = now.Add(2 * time.Second)
	require.NoError(budget.reserve(), "first probe")
	require.ErrorIs(budget.reserve(), ErrBreakerOpen, "second concurrent probe waits")
	require.ErrorIs(budget.preflight(1), ErrBreakerOpen)
	budget.record(Usage{InputTokens: new(int64(1)), OutputTokens: new(int64(1)), Complete: true})
	require.NoError(budget.reserve())
	assert.Equal(3, budget.Attempts())
}

func TestBudgetWithoutPricesCountsRequestsOnly(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	budget := &Budget{MaxRequests: 2}
	client := newTestClient(t, budget, func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{"model":"jev-1.13.0","answers":{"matches":{"type":"noul","noul":0.5}}}`), nil
	})
	_, err := client.Ask(context.Background(), noulRequest("matches"))
	require.NoError(err, "a zero cost stop is not a stop when nothing is priced")
	_, err = client.Ask(context.Background(), noulRequest("matches"))
	require.NoError(err, "missing usage is tolerated when nothing is priced")
	_, err = client.Ask(context.Background(), noulRequest("matches"))
	require.ErrorIs(err, ErrRequestLimit)
	state := budget.State()
	assert.True(state.UsageUnknownUntil.IsZero())
	assert.False(state.CostStopped)
	assert.Zero(state.CostUSD)
}

func TestBudgetCostAccountingResetsEachUTCDay(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 9, 28, 23, 0, 0, 0, time.UTC)
	// Each measured response costs 100 in + 10 out at 1 USD/M each = 0.00011 USD.
	budget := &Budget{MaxRequests: 100, StopUSD: 0.0002, InputUSDPerM: 1, OutputUSDPerM: 1, Now: func() time.Time { return now }}
	client := newTestClient(t, budget, func(*http.Request) (*http.Response, error) {
		return jsonResponse(measuredResponse), nil
	})
	_, err := client.Ask(context.Background(), noulRequest("matches"))
	require.NoError(err)
	_, err = client.Ask(context.Background(), noulRequest("matches"))
	require.NoError(err)
	_, err = client.Ask(context.Background(), noulRequest("matches"))
	require.ErrorIs(err, ErrCostStop, "two responses reach the day's cap")
	assert.True(budget.State().CostStopped)

	now = now.Add(2 * time.Hour) // 01:00 the next UTC day
	_, err = client.Ask(context.Background(), noulRequest("matches"))
	require.NoError(err, "a new UTC day starts a new cost account")
	state := budget.State()
	assert.Equal("2026-09-29", state.CostDay)
	assert.InDelta(0.00011, state.CostUSD, 1e-9)
	assert.False(state.CostStopped)

	uncapped := &Budget{MaxRequests: 100, InputUSDPerM: 1, OutputUSDPerM: 1, Now: func() time.Time { return now }}
	free := newTestClient(t, uncapped, func(*http.Request) (*http.Response, error) {
		return jsonResponse(measuredResponse), nil
	})
	for range 3 {
		_, err = free.Ask(context.Background(), noulRequest("matches"))
		require.NoError(err, "a zero StopUSD is no cap, even when priced")
	}
}

func TestPerRunBudgetStaysCappedAcrossMidnightAndStopsForGood(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 9, 28, 23, 0, 0, 0, time.UTC)
	// Each measured response costs 0.00011 USD; the run cap admits two.
	budget := &Budget{MaxRequests: 100, StopUSD: 0.0002, InputUSDPerM: 1, OutputUSDPerM: 1, PerRun: true, Now: func() time.Time { return now }}
	var failing atomic.Bool
	client := newTestClient(t, budget, func(*http.Request) (*http.Response, error) {
		if failing.Load() {
			return nil, errors.New("connection refused")
		}
		return jsonResponse(measuredResponse), nil
	})
	_, err := client.Ask(context.Background(), noulRequest("matches"))
	require.NoError(err)
	now = now.Add(2 * time.Hour) // the run crosses midnight UTC
	_, err = client.Ask(context.Background(), noulRequest("matches"))
	require.NoError(err)
	_, err = client.Ask(context.Background(), noulRequest("matches"))
	require.ErrorIs(err, ErrCostStop, "the run's spend does not reset with the day")
	assert.InDelta(0.00022, budget.State().CostUSD, 1e-9)

	sticky := &Budget{MaxRequests: 100, StopUSD: 1, PerRun: true, Cooldown: time.Second, Now: func() time.Time { return now }}
	failing.Store(true)
	stickyClient := newTestClient(t, sticky, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})
	_, err = stickyClient.Ask(context.Background(), noulRequest("matches"))
	require.Error(err)
	now = now.Add(time.Hour)
	_, err = stickyClient.Ask(context.Background(), noulRequest("matches"))
	require.ErrorIs(err, ErrRunHalted, "one failure stops a run for good")
	assert.Equal("provider request failed", SafeFailure(err))

	unknown := &Budget{MaxRequests: 100, StopUSD: 1, InputUSDPerM: 1, PerRun: true, Cooldown: time.Second, Now: func() time.Time { return now }}
	unknownClient := newTestClient(t, unknown, func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{"model":"jev-1.13.0","answers":{"matches":{"type":"noul","noul":0.5}}}`), nil
	})
	_, err = unknownClient.Ask(context.Background(), noulRequest("matches"))
	require.NoError(err)
	now = now.Add(time.Hour)
	_, err = unknownClient.Ask(context.Background(), noulRequest("matches"))
	require.ErrorIs(err, ErrUsageUnknown, "unknowable spend stops a priced run for good")
}

func TestBudgetUnknownUsageStopExpiresWithTheCooldown(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	budget := &Budget{MaxRequests: 100, StopUSD: 1, InputUSDPerM: 1, Cooldown: time.Minute, Now: func() time.Time { return now }}
	var withUsage atomic.Bool
	var calls atomic.Int32
	client := newTestClient(t, budget, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		if withUsage.Load() {
			return jsonResponse(measuredResponse), nil
		}
		return jsonResponse(`{"model":"jev-1.13.0","answers":{"matches":{"type":"noul","noul":0.5}}}`), nil
	})
	_, err := client.Ask(context.Background(), noulRequest("matches"))
	require.NoError(err)
	_, err = client.Ask(context.Background(), noulRequest("matches"))
	require.ErrorIs(err, ErrUsageUnknown, "unknowable spend pauses a priced budget")
	assert.Equal(now.Add(time.Minute), budget.State().UsageUnknownUntil)
	assert.Equal(int32(1), calls.Load())

	now = now.Add(61 * time.Second)
	withUsage.Store(true)
	_, err = client.Ask(context.Background(), noulRequest("matches"))
	require.NoError(err, "the pause expires with the cool-down instead of lasting the process lifetime")
	assert.True(budget.State().UsageUnknownUntil.IsZero(), "a measured response clears the pause")
}

type fakeLedger struct {
	mu           sync.Mutex
	reservations []DayReservation
	released     []DayReservation
	usage        []DayUsage
	reserveErr   error
	recordErr    error
}

func (l *fakeLedger) ReserveJevDayRequest(_ context.Context, reservation DayReservation) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reservations = append(l.reservations, reservation)
	return l.reserveErr
}

func (l *fakeLedger) ReleaseJevDayRequest(_ context.Context, reservation DayReservation) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.released = append(l.released, reservation)
	return nil
}

func (l *fakeLedger) RecordJevDayUsage(_ context.Context, usage DayUsage) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.usage = append(l.usage, usage)
	return l.recordErr
}

func (l *fakeLedger) JevDayCounters(context.Context, string, string) (DayCounters, error) {
	return DayCounters{}, nil
}

func TestClientLedgerReservesBeforeEgressAndRecordsMeasuredUsage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ledger := &fakeLedger{}
	var calls atomic.Int32
	client, err := NewClient(Options{
		APIKey: "k", Budget: &Budget{MaxRequests: 10, InputUSDPerM: 2, OutputUSDPerM: 4, StopUSD: 1},
		Ledger: ledger, DayLimits: DayLimits{MaxRequests: 5, MaxCostUSDMicros: 1000},
		Now: func() time.Time { return time.Date(2026, 9, 28, 23, 59, 0, 0, time.FixedZone("west", -3600)) },
		Transport: testTransport(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return jsonResponse(measuredResponse), nil
		}),
	})
	require.NoError(err)
	request := noulRequest("matches")
	request.Feature = "enrichment_identity"
	_, err = client.Ask(context.Background(), request)
	require.NoError(err)
	require.Len(ledger.reservations, 1)
	assert.Equal(DayReservation{
		Feature: "enrichment_identity", UTCDay: "2026-09-29",
		Limits: DayLimits{MaxRequests: 5, MaxCostUSDMicros: 1000},
	}, ledger.reservations[0], "the day is computed in UTC")
	require.Len(ledger.usage, 1)
	assert.Equal(DayUsage{
		Feature: "enrichment_identity", UTCDay: "2026-09-29",
		InputTokens: 100, OutputTokens: 10, CostUSDMicros: 240, UsageKnown: true,
	}, ledger.usage[0])

	ledger.reserveErr = ErrDayRequestLimit
	_, err = client.Ask(context.Background(), request)
	require.ErrorIs(err, ErrDayRequestLimit)
	assert.Equal("daily request limit reached", SafeFailure(err))
	assert.Equal(int32(1), calls.Load(), "a refused day reservation sends nothing")

	unnamed := noulRequest("matches")
	_, err = client.Ask(context.Background(), unnamed)
	require.ErrorIs(err, ErrRequestBounds, "a ledgered client needs a feature name")
	assert.Equal(int32(1), calls.Load())
}

func TestClientReservesTheProcessBudgetBeforeTheDay(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	ledger := &fakeLedger{}
	budget := &Budget{MaxRequests: 100, FailureThreshold: 1, Cooldown: time.Hour, Now: func() time.Time { return now }}
	var calls atomic.Int32
	client, err := NewClient(Options{
		APIKey: "k", Budget: budget, Ledger: ledger, Now: func() time.Time { return now },
		Transport: testTransport(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("connection refused")
		}),
	})
	require.NoError(err)
	request := noulRequest("matches")
	request.Feature = "enrichment_identity"
	_, err = client.Ask(context.Background(), request)
	require.Error(err, "the first failure opens the breaker")
	require.Len(ledger.reservations, 1)

	_, err = client.Ask(context.Background(), request)
	require.ErrorIs(err, ErrBreakerOpen)
	_, err = client.AskAll(context.Background(), []Request{request, request})
	require.ErrorIs(err, ErrBreakerOpen)
	assert.Len(ledger.reservations, 1, "an open breaker never touches the day counters")
	assert.Len(ledger.usage, 1)
	assert.Equal(int32(1), calls.Load())

	now = now.Add(2 * time.Hour)
	ledger.reserveErr = ErrDayRequestLimit
	_, err = client.Ask(context.Background(), request)
	require.ErrorIs(err, ErrDayRequestLimit)
	assert.Equal(1, budget.Attempts(), "a refused day reservation releases the in-process slot")
	assert.Equal(1, budget.State().ConsecutiveFailures, "a refused day reservation is not a provider failure")
	assert.Len(ledger.usage, 1, "nothing is recorded for a request that never left")
}

func TestAskAllSiblingFailureLeavesUnsentRequestsOffTheLedger(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ledger := &fakeLedger{}
	var calls atomic.Int32
	client, err := NewClient(Options{
		APIKey: "k", Budget: &Budget{MaxRequests: 100, FailureThreshold: 10}, Ledger: ledger, MaxConcurrent: 1,
		Transport: testTransport(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: http.NoBody}, nil
		}),
	})
	require.NoError(err)
	requests := make([]Request, 8)
	for i := range requests {
		requests[i] = noulRequest("matches")
		requests[i].Feature = "enrichment_identity"
	}
	batch, err := client.AskAll(context.Background(), requests)
	require.ErrorContains(err, "HTTP 503")
	assert.Equal(int32(1), calls.Load(), "the first failure cancels the group before the next request reserves")
	assert.Len(ledger.reservations, 1, "only the request that left the process charged the day")
	assert.Empty(ledger.released)
	assert.Equal(1, batch.Usage.Requests)
	assert.Equal(1, client.BudgetState().Attempts, "unsent requests do not consume in-process attempts either")
}

func TestClientReleasesTheDayWhenTheContextIsDoneBeforeDispatch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ledger := &fakeLedger{}
	var calls atomic.Int32
	client, err := NewClient(Options{
		APIKey: "k", Budget: &Budget{MaxRequests: 100}, Ledger: ledger,
		Transport: testTransport(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return jsonResponse(measuredResponse), nil
		}),
	})
	require.NoError(err)
	request := noulRequest("matches")
	request.Feature = "enrichment_identity"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Ask(ctx, request)
	require.ErrorIs(err, context.Canceled)
	assert.Zero(calls.Load())
	assert.Empty(ledger.reservations, "a done context reserves nothing")
	assert.Empty(ledger.usage)
	assert.Zero(client.BudgetState().Attempts)

	_, err = client.AskAll(ctx, []Request{request, request})
	require.ErrorIs(err, context.Canceled)
	assert.Empty(ledger.reservations)
}

func TestClientLedgerRecordsFailedRequestsWithoutUsage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ledger := &fakeLedger{recordErr: errors.New("disk full")}
	client, err := NewClient(Options{
		APIKey: "k", Budget: &Budget{MaxRequests: 10}, Ledger: ledger,
		Transport: testTransport(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		}),
	})
	require.NoError(err)
	request := noulRequest("matches")
	request.Feature = "enrichment_identity"
	_, err = client.Ask(context.Background(), request)
	require.Error(err)
	require.Len(ledger.usage, 1)
	assert.False(ledger.usage[0].UsageKnown)
	assert.Zero(ledger.usage[0].InputTokens)
}

func TestCostUSDMicrosAndUTCDay(t *testing.T) {
	assert := assert.New(t)
	assert.Equal(int64(240), CostUSDMicros(100, 10, 2, 4))
	assert.Equal(int64(0), CostUSDMicros(100, 10, 0, 0))
	assert.Equal(int64(1), CostUSDMicros(1, 0, 0.6, 0))
	assert.Equal("2026-01-01", UTCDay(time.Date(2025, 12, 31, 23, 30, 0, 0, time.FixedZone("west", -3600))))
	assert.True(ValidFeatureName("enrichment_identity"))
	assert.False(ValidFeatureName("Enrichment"))
	assert.False(ValidFeatureName(""))
}
