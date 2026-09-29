package jev

import (
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
// tolerated. When a price is set, a response without usage makes the spend
// unknowable and the budget stops until the process restarts.
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
	unknown          bool
	stopped          bool
	failures         int
	openUntil        time.Time
	probing          bool
}

// BudgetState is a snapshot for status output and logs. It carries no
// request content.
type BudgetState struct {
	Attempts            int       `json:"attempts"`
	CostUSD             float64   `json:"cost_usd"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	OpenUntil           time.Time `json:"open_until,omitzero"`
	UsageUnknown        bool      `json:"usage_unknown"`
	CostStopped         bool      `json:"cost_stopped"`
}

// Attempts reports how many requests have been reserved so far.
func (b *Budget) Attempts() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.attempts
}

// CostUSD reports the measured spend so far.
func (b *Budget) CostUSD() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cost
}

// State returns a snapshot of the budget and breaker.
func (b *Budget) State() BudgetState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return BudgetState{
		Attempts: b.attempts, CostUSD: b.cost, ConsecutiveFailures: b.failures,
		OpenUntil: b.openUntil, UsageUnknown: b.unknown, CostStopped: b.stopped,
	}
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
	if !b.openUntil.IsZero() {
		if b.now().Before(b.openUntil) || b.probing {
			return ErrBreakerOpen
		}
	}
	if b.unknown {
		return ErrUsageUnknown
	}
	if b.priced() && (b.stopped || b.cost >= b.StopUSD) {
		b.stopped = true
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

func (b *Budget) record(usage Usage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.openUntil = time.Time{}
	b.probing = false
	if usage.InputTokens == nil || usage.OutputTokens == nil {
		if b.priced() {
			b.unknown = true
		}
		return
	}
	b.cost += float64(*usage.InputTokens)*b.InputUSDPerM/1e6 +
		float64(*usage.OutputTokens)*b.OutputUSDPerM/1e6
	if b.priced() && b.cost >= b.StopUSD {
		b.stopped = true
	}
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
