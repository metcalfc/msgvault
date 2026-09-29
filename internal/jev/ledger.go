package jev

import (
	"context"
	"errors"
	"math"
	"regexp"
	"time"
)

// Daily limit categories reported by a Ledger.
var (
	ErrDayRequestLimit = errors.New("daily request limit reached")
	ErrDayCostStop     = errors.New("daily cost limit reached")
)

var featureNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ValidFeatureName reports whether name is a lowercase feature identifier.
func ValidFeatureName(name string) bool {
	return featureNamePattern.MatchString(name)
}

// DayLimits cap one feature's requests and spend per UTC day. A zero limit
// means that dimension is not capped.
type DayLimits struct {
	MaxRequests      int64
	MaxCostUSDMicros int64
}

// DayReservation counts one request against a feature's day before egress.
type DayReservation struct {
	Feature string
	UTCDay  string
	Limits  DayLimits
}

// DayUsage records what one completed request measured. UsageKnown is false
// when the provider returned no token counts; the request still counts.
type DayUsage struct {
	Feature       string
	UTCDay        string
	InputTokens   int64
	OutputTokens  int64
	CostUSDMicros int64
	UsageKnown    bool
}

// DayCounters is one feature's persisted accounting for one UTC day.
type DayCounters struct {
	Feature       string `json:"feature"`
	UTCDay        string `json:"utc_day"`
	Requests      int64  `json:"requests"`
	InputTokens   int64  `json:"input_tokens"`
	OutputTokens  int64  `json:"output_tokens"`
	CostUSDMicros int64  `json:"cost_usd_micros"`
}

// Ledger persists daily request and cost counters so limits survive daemon
// restarts. *store.Store implements it. ReleaseJevDayRequest returns a
// reservation for a request the client never dispatched.
type Ledger interface {
	ReserveJevDayRequest(ctx context.Context, reservation DayReservation) error
	ReleaseJevDayRequest(ctx context.Context, reservation DayReservation) error
	RecordJevDayUsage(ctx context.Context, usage DayUsage) error
	JevDayCounters(ctx context.Context, feature, utcDay string) (DayCounters, error)
}

// UTCDay formats the day a request belongs to.
func UTCDay(at time.Time) string {
	return at.UTC().Format(time.DateOnly)
}

// CostUSDMicros prices token counts. Prices are USD per million tokens, so
// tokens times price is already micro-dollars.
func CostUSDMicros(inputTokens, outputTokens int64, inputUSDPerM, outputUSDPerM float64) int64 {
	return int64(math.Round(float64(inputTokens)*inputUSDPerM + float64(outputTokens)*outputUSDPerM))
}
