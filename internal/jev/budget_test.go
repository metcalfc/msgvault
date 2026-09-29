package jev

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
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
	assert.False(state.UsageUnknown)
	assert.False(state.CostStopped)
	assert.Zero(state.CostUSD)
}

type fakeLedger struct {
	mu           sync.Mutex
	reservations []DayReservation
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
