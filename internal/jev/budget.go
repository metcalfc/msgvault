package jev

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	// DefaultFailureThreshold is how many consecutive failures open the breaker.
	DefaultFailureThreshold = 3
	// DefaultCooldown is how long the breaker stays open before one probe
	// request is allowed through.
	DefaultCooldown = 30 * time.Second
)

// ErrBreakerOpen reports that recent consecutive failures have paused
// requests until the cool-down ends.
var ErrBreakerOpen = errors.New("provider circuit breaker open")

// Budget shares in-process request and cost limits across every client that
// holds it and carries the circuit breaker. Set its limits before use.
//
// Prices are USD per million tokens. When both are zero the budget counts
// requests only: the cost stop is not consulted and missing provider usage is
// tolerated. When a price is set, the measured spend is accounted per UTC day
// and StopUSD caps that day; a response without usage makes the day's spend
// unknowable and pauses requests for one cool-down, like a failure would.
type Budget struct {
	mu            sync.Mutex
	MaxRequests   int
	StopUSD       float64
	InputUSDPerM  float64
	OutputUSDPerM float64
	// FailureThreshold and Cooldown configure the breaker; zero values take
	// the defaults. Now lets tests control the clock.
	FailureThreshold int
	Cooldown         time.Duration
	Now              func() time.Time
	attempts         int
	cost             float64
	costDay          string
	unknownUntil     time.Time
	failures         int
	openUntil        time.Time
	probing          bool
}

// BudgetState is a snapshot for status output and logs. It carries no
// request content. CostUSD and CostStopped describe the current UTC day.
type BudgetState struct {
	Attempts            int       `json:"attempts"`
	CostUSD             float64   `json:"cost_usd"`
	CostDay             string    `json:"cost_day,omitzero"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	OpenUntil           time.Time `json:"open_until,omitzero"`
	UsageUnknownUntil   time.Time `json:"usage_unknown_until,omitzero"`
	CostStopped         bool      `json:"cost_stopped"`
}

// Attempts reports how many requests have been reserved so far.
func (b *Budget) Attempts() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.attempts
}

// CostUSD reports the measured spend for the current UTC day.
func (b *Budget) CostUSD() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollDay()
	return b.cost
}

// State returns a snapshot of the budget and breaker.
func (b *Budget) State() BudgetState {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollDay()
	return BudgetState{
		Attempts: b.attempts, CostUSD: b.cost, CostDay: b.costDay, ConsecutiveFailures: b.failures,
		OpenUntil: b.openUntil, UsageUnknownUntil: b.unknownUntil, CostStopped: b.costStopped(),
	}
}

// rollDay forgets the previous day's spend once the UTC day changes, so a
// long-running process is capped per day rather than for its lifetime. The
// caller holds the lock.
func (b *Budget) rollDay() {
	day := UTCDay(b.now())
	if b.costDay == day {
		return
	}
	b.costDay = day
	b.cost = 0
}

// costStopped reports whether the current day's measured spend reached the
// cap. A zero StopUSD means no cost cap. The caller holds the lock.
func (b *Budget) costStopped() bool {
	return b.priced() && b.StopUSD > 0 && b.cost >= b.StopUSD
}

func (b *Budget) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Budget) threshold() int {
	if b.FailureThreshold > 0 {
		return b.FailureThreshold
	}
	return DefaultFailureThreshold
}

func (b *Budget) cooldown() time.Duration {
	if b.Cooldown > 0 {
		return b.Cooldown
	}
	return DefaultCooldown
}

func (b *Budget) priced() bool {
	return b.InputUSDPerM > 0 || b.OutputUSDPerM > 0
}

// blocked reports the first reason no request may start. The caller holds
// the lock.
func (b *Budget) blocked() error {
	now := b.now()
	if !b.openUntil.IsZero() {
		if now.Before(b.openUntil) || b.probing {
			return ErrBreakerOpen
		}
	}
	if !b.unknownUntil.IsZero() && now.Before(b.unknownUntil) {
		return ErrUsageUnknown
	}
	b.rollDay()
	if b.costStopped() {
		return ErrCostStop
	}
	return nil
}

func (b *Budget) reserve() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.blocked(); err != nil {
		return err
	}
	if b.attempts >= b.MaxRequests {
		return ErrRequestLimit
	}
	if !b.openUntil.IsZero() {
		// Half-open: exactly one probe may run until it reports back.
		b.probing = true
	}
	b.attempts++
	return nil
}

func (b *Budget) preflight(requests int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if requests <= 0 || b.attempts+requests > b.MaxRequests {
		return ErrRequestLimit
	}
	return b.blocked()
}

// release returns a reservation that never left the process, so a refused
// day reservation does not consume an in-process attempt or a probe.
func (b *Budget) release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.attempts > 0 {
		b.attempts--
	}
	b.probing = false
}

func (b *Budget) record(usage Usage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.openUntil = time.Time{}
	b.probing = false
	b.unknownUntil = time.Time{}
	if usage.InputTokens == nil || usage.OutputTokens == nil {
		if b.priced() {
			// The day's spend is now unknowable; pause for one cool-down
			// rather than for the life of the process.
			b.unknownUntil = b.now().Add(b.cooldown())
		}
		return
	}
	b.rollDay()
	b.cost += float64(*usage.InputTokens)*b.InputUSDPerM/1e6 +
		float64(*usage.OutputTokens)*b.OutputUSDPerM/1e6
}

// outcome records a failed send. A request the caller cancelled, or that a
// sibling's failure cancelled through the shared group context, says nothing
// about the provider and does not count toward the breaker; it only ends a
// half-open probe. A per-request timeout with a live caller context does
// count: the provider did not answer in time.
func (b *Budget) outcome(ctx context.Context, err error) {
	if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		b.mu.Lock()
		b.probing = false
		b.mu.Unlock()
		return
	}
	b.fail()
}

func (b *Budget) fail() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
	b.failures++
	if b.failures >= b.threshold() {
		b.openUntil = b.now().Add(b.cooldown())
	}
}
