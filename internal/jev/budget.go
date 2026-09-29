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

// ErrRunHalted reports that a per-run budget saw a provider failure and will
// start no further requests for the rest of the run.
var ErrRunHalted = errors.New("provider failed; no further requests will start")

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
	// PerRun switches to the accounting a bounded run such as `msgvault
	// eval` documents: spend accumulates for the whole run instead of
	// resetting each UTC day, and the first provider failure or unknowable
	// usage stops the run for good instead of pausing for a cool-down.
	PerRun       bool
	halted       bool
	attempts     int
	cost         float64
	costDay      string
	unknownUntil time.Time
	failures     int
	openUntil    time.Time
	probing      bool
	inFlight     int
	// epoch counts breaker openings. A reservation carries the epoch it was
	// admitted in; one from before the latest opening is stale.
	epoch uint64
}

// Budget invariants. Every method below preserves them; tests pin each one.
//
//  1. At most one half-open probe is in flight: reserve sets probing only for
//     the reservation it marks as the probe, and only that reservation clears
//     it when it settles.
//  2. Every successful reserve is settled exactly once, by release, record,
//     or outcome; inFlight counts the unsettled ones.
//  3. release is for a request that never left the process; it also returns
//     the in-process attempt. The client pairs it with releasing the day.
//  4. Caller cancellation never counts toward the breaker.
//  5. Prices are read under the lock.
//  6. Only the probe, or a request admitted since the breaker last opened,
//     changes breaker state on an answer. A stale answer that was in flight
//     when the breaker opened still counts its usage, but it cannot close
//     the breaker or reset its failures.

// reservation is one admitted request. probe marks the reservation that
// holds the half-open breaker's single probe slot.
type reservation struct {
	probe bool
	epoch uint64
}

// BudgetState is a snapshot for status output and logs. It carries no
// request content. CostUSD and CostStopped describe the current UTC day.
type BudgetState struct {
	Attempts            int       `json:"attempts"`
	CostUSD             float64   `json:"cost_usd"`
	CostDay             string    `json:"cost_day,omitzero"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	InFlight            int       `json:"in_flight"`
	OpenUntil           time.Time `json:"open_until,omitzero"`
	UsageUnknownUntil   time.Time `json:"usage_unknown_until,omitzero"`
	CostStopped         bool      `json:"cost_stopped"`
}

// State returns a snapshot of the budget and breaker.
func (b *Budget) State() BudgetState {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollDay()
	return BudgetState{
		Attempts: b.attempts, CostUSD: b.cost, CostDay: b.costDay, ConsecutiveFailures: b.failures,
		InFlight:  b.inFlight,
		OpenUntil: b.openUntil, UsageUnknownUntil: b.unknownUntil, CostStopped: b.costStopped(),
	}
}

// rollDay forgets the previous day's spend once the UTC day changes, so a
// long-running process is capped per day rather than for its lifetime. A
// per-run budget keeps its total for the whole run. The caller holds the
// lock.
func (b *Budget) rollDay() {
	if b.PerRun {
		return
	}
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

// prices reads the token prices under the lock, for a client snapshotting
// them while a service may be rebinding the shared budget.
func (b *Budget) prices() (inputUSDPerM, outputUSDPerM float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.InputUSDPerM, b.OutputUSDPerM
}

func (b *Budget) priced() bool {
	return b.InputUSDPerM > 0 || b.OutputUSDPerM > 0
}

// blocked reports the first reason no request may start. The caller holds
// the lock.
func (b *Budget) blocked() error {
	if b.halted {
		return ErrRunHalted
	}
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

// halfOpen reports whether the breaker's cool-down has passed and the next
// request will be its single probe. AskAll serializes that probe so sibling
// requests cannot be refused with ErrBreakerOpen and cancel it.
func (b *Budget) halfOpen() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.openUntil.IsZero() && !b.now().Before(b.openUntil) && !b.probing
}

// reserve admits one request. The caller must settle the returned
// reservation exactly once with release, record, or outcome.
func (b *Budget) reserve() (reservation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.blocked(); err != nil {
		return reservation{}, err
	}
	if b.attempts >= b.MaxRequests {
		return reservation{}, ErrRequestLimit
	}
	admitted := reservation{epoch: b.epoch}
	if !b.openUntil.IsZero() {
		// Half-open: exactly one probe may run until it reports back.
		b.probing = true
		admitted.probe = true
	}
	b.attempts++
	b.inFlight++
	return admitted, nil
}

// settle retires one reservation. Only the probe's own reservation ends the
// probe, so a sibling settling first cannot let a second probe start. The
// caller holds the lock.
func (b *Budget) settle(admitted reservation) {
	b.inFlight--
	if admitted.probe {
		b.probing = false
	}
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
// day reservation does not consume an in-process attempt. A released probe
// frees the probe slot; any other release leaves an in-flight probe alone.
func (b *Budget) release(admitted reservation) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.attempts > 0 {
		b.attempts--
	}
	b.settle(admitted)
}

// record settles a request that got an answer and counts its usage. The
// probe's answer, or one admitted since the breaker last opened, closes the
// breaker; a stale answer leaves breaker state alone (invariant 6).
func (b *Budget) record(admitted reservation, usage Usage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.settle(admitted)
	if admitted.probe || admitted.epoch == b.epoch {
		b.failures = 0
		b.openUntil = time.Time{}
	}
	if !b.usageStoppedForRun() {
		// A measured answer ends a day budget's cool-down pause. A per-run
		// stop is permanent: a sibling that was already in flight cannot
		// make the run's total spend knowable again.
		b.unknownUntil = time.Time{}
	}
	if usage.InputTokens == nil || usage.OutputTokens == nil {
		if b.priced() {
			// The day's spend is now unknowable; pause for one cool-down
			// rather than for the life of the process. A per-run budget
			// cannot bound the run's spend any more and stops it.
			b.unknownUntil = b.now().Add(b.cooldown())
			if b.PerRun {
				b.unknownUntil = stickyUntil
			}
		}
		return
	}
	b.rollDay()
	b.cost += float64(*usage.InputTokens)*b.InputUSDPerM/1e6 +
		float64(*usage.OutputTokens)*b.OutputUSDPerM/1e6
}

// outcome records a failed send. A request the caller cancelled, that a
// sibling's failure cancelled through the shared group context, or that ran
// out of a caller deadline shorter than the client's own timeout says nothing
// about the provider and does not count toward the breaker; it only settles
// the reservation, which ends the half-open probe if it was one. The client's
// per-request timeout does count: the provider did not answer in the time the
// operator allowed it.
func (b *Budget) outcome(ctx context.Context, admitted reservation, err error, callerBound bool) {
	cancelled := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	if cancelled && (ctx.Err() != nil || callerBound) {
		b.mu.Lock()
		b.settle(admitted)
		b.mu.Unlock()
		return
	}
	b.fail(admitted)
}

func (b *Budget) fail(admitted reservation) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.settle(admitted)
	b.failures++
	if b.PerRun {
		b.halted = true
		return
	}
	if b.failures >= b.threshold() {
		b.openUntil = b.now().Add(b.cooldown())
		b.epoch++
	}
}

// usageStoppedForRun reports whether a per-run budget has permanently
// stopped on unknowable usage. The caller holds the lock.
func (b *Budget) usageStoppedForRun() bool {
	return b.PerRun && b.unknownUntil.Equal(stickyUntil)
}

// stickyUntil is a deadline no clock reaches: a per-run stop never expires.
var stickyUntil = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
