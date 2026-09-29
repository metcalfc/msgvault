package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestJevDayCountersReserveRecordAndCap(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	ctx := t.Context()
	limits := jev.DayLimits{MaxRequests: 2, MaxCostUSDMicros: 500}
	reservation := jev.DayReservation{Feature: "enrichment_identity", UTCDay: "2026-09-28", Limits: limits}

	empty, err := st.JevDayCounters(ctx, "enrichment_identity", "2026-09-28")
	require.NoError(err)
	assert.Equal(jev.DayCounters{Feature: "enrichment_identity", UTCDay: "2026-09-28"}, empty)

	require.NoError(st.ReserveJevDayRequest(ctx, reservation))
	require.NoError(st.RecordJevDayUsage(ctx, jev.DayUsage{
		Feature: "enrichment_identity", UTCDay: "2026-09-28",
		InputTokens: 300, OutputTokens: 20, CostUSDMicros: 320, UsageKnown: true,
	}))
	require.NoError(st.ReserveJevDayRequest(ctx, reservation))
	require.NoError(st.RecordJevDayUsage(ctx, jev.DayUsage{
		Feature: "enrichment_identity", UTCDay: "2026-09-28", UsageKnown: false,
	}), "a request without usage still counts but adds no tokens")
	counters, err := st.JevDayCounters(ctx, "enrichment_identity", "2026-09-28")
	require.NoError(err)
	assert.Equal(jev.DayCounters{
		Feature: "enrichment_identity", UTCDay: "2026-09-28",
		Requests: 2, InputTokens: 300, OutputTokens: 20, CostUSDMicros: 320,
	}, counters)

	err = st.ReserveJevDayRequest(ctx, reservation)
	require.ErrorIs(err, jev.ErrDayRequestLimit)
	counters, err = st.JevDayCounters(ctx, "enrichment_identity", "2026-09-28")
	require.NoError(err)
	assert.Equal(int64(2), counters.Requests, "a refused reservation does not count")

	require.NoError(st.ReleaseJevDayRequest(ctx, reservation))
	counters, err = st.JevDayCounters(ctx, "enrichment_identity", "2026-09-28")
	require.NoError(err)
	assert.Equal(int64(1), counters.Requests, "a released reservation frees the slot")
	require.NoError(st.ReserveJevDayRequest(ctx, reservation), "the freed slot can be used again")
	require.NoError(st.ReleaseJevDayRequest(ctx, jev.DayReservation{Feature: "never", UTCDay: "2026-09-28"}),
		"releasing an unknown day is harmless")
	require.NoError(st.ReleaseJevDayRequest(ctx, jev.DayReservation{Feature: "never", UTCDay: "2026-09-28"}))
	never, err := st.JevDayCounters(ctx, "never", "2026-09-28")
	require.NoError(err)
	assert.Zero(never.Requests, "a day never goes below zero")

	require.NoError(st.ReserveJevDayRequest(ctx, jev.DayReservation{
		Feature: "enrichment_identity", UTCDay: "2026-09-29", Limits: limits,
	}), "a new day starts fresh")
	require.NoError(st.ReserveJevDayRequest(ctx, jev.DayReservation{
		Feature: "search_rerank", UTCDay: "2026-09-28", Limits: limits,
	}), "features are counted separately")

	costLimited := jev.DayReservation{Feature: "cost_feature", UTCDay: "2026-09-28",
		Limits: jev.DayLimits{MaxCostUSDMicros: 100}}
	require.NoError(st.ReserveJevDayRequest(ctx, costLimited))
	require.NoError(st.RecordJevDayUsage(ctx, jev.DayUsage{
		Feature: "cost_feature", UTCDay: "2026-09-28", CostUSDMicros: 100, UsageKnown: true,
	}))
	require.ErrorIs(st.ReserveJevDayRequest(ctx, costLimited), jev.ErrDayCostStop)

	uncapped := jev.DayReservation{Feature: "uncapped", UTCDay: "2026-09-28"}
	for range 5 {
		require.NoError(st.ReserveJevDayRequest(ctx, uncapped), "zero limits mean no cap")
	}
}

func TestJevDayCountersRejectInvalidInputs(t *testing.T) {
	require := require.New(t)
	st := testutil.NewTestStore(t)
	ctx := t.Context()
	require.Error(st.ReserveJevDayRequest(ctx, jev.DayReservation{Feature: "Bad Name", UTCDay: "2026-09-28"}))
	require.Error(st.ReserveJevDayRequest(ctx, jev.DayReservation{Feature: "ok", UTCDay: "28/09/2026"}))
	require.Error(st.ReserveJevDayRequest(ctx, jev.DayReservation{Feature: "ok", UTCDay: "2026-09-28",
		Limits: jev.DayLimits{MaxRequests: -1}}))
	require.Error(st.RecordJevDayUsage(ctx, jev.DayUsage{Feature: "ok", UTCDay: "2026-09-28", InputTokens: -1, UsageKnown: true}))
	require.Error(st.RecordJevDayUsage(ctx, jev.DayUsage{Feature: "never_reserved", UTCDay: "2026-09-28", UsageKnown: true}),
		"usage cannot be recorded before a reservation")
	_, err := st.JevDayCounters(ctx, "", "2026-09-28")
	require.Error(err)
}
