package jev

import (
	"errors"
	"sync"
)

// Budget shares request and cost limits across every client that holds it.
// Set its limits before use. Prices are USD per million tokens; when both are
// zero the budget counts requests only and never reports a cost.
type Budget struct {
	mu            sync.Mutex
	MaxRequests   int
	StopUSD       float64
	InputUSDPerM  float64
	OutputUSDPerM float64
	attempts      int
	cost          float64
	unknown       bool
	stopped       bool
	failed        bool
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

func (b *Budget) reserve() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed {
		return errors.New("jev provider failed; no further requests will start")
	}
	if b.unknown {
		return ErrUsageUnknown
	}
	if b.stopped || b.cost >= b.StopUSD {
		b.stopped = true
		return ErrCostStop
	}
	if b.attempts >= b.MaxRequests {
		return ErrRequestLimit
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
	if b.failed {
		return errors.New("jev provider failed; no further requests will start")
	}
	if b.unknown {
		return ErrUsageUnknown
	}
	if b.stopped || b.cost >= b.StopUSD {
		return ErrCostStop
	}
	return nil
}

func (b *Budget) record(usage Usage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if usage.InputTokens == nil || usage.OutputTokens == nil {
		b.unknown = true
		return
	}
	b.cost += float64(*usage.InputTokens)*b.InputUSDPerM/1e6 +
		float64(*usage.OutputTokens)*b.OutputUSDPerM/1e6
	if b.cost >= b.StopUSD {
		b.stopped = true
	}
}

func (b *Budget) fail() {
	b.mu.Lock()
	b.failed = true
	b.mu.Unlock()
}
