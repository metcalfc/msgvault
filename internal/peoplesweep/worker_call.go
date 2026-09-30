package peoplesweep

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// accountedCallResult keeps execution and accounting failures separate so each
// program can retain its own repair and failure policy.
type accountedCallResult struct {
	lease     Lease
	response  StructuredResponse
	marked    bool
	runErr    error
	recordErr error
}

// executeAccountedCall owns the durable start boundary shared by extraction and
// briefs. Unstarted reservations remain refundable; completed responses are
// accounted even when validation or the lease heartbeat subsequently fails.
func (w *Worker) executeAccountedCall(ctx context.Context, lease Lease, call sweepProviderCall,
	prepared PreparedStructuredCall, accounting *sweepCallAccounting,
) accountedCallResult {
	result := accountedCallResult{}
	started := w.now()
	result.lease, result.response, result.runErr = w.runPreparedWithLeaseHeartbeat(ctx, lease,
		func(markCtx context.Context) error {
			if err := w.Store.MarkPersonSweepBudgetStarted(markCtx, call.reservation, lease); err != nil {
				return err
			}
			result.marked = true
			return nil
		}, prepared)
	if !result.marked {
		_ = w.Store.ReleasePersonSweepBudget(ctx, call.reservation)
		return result
	}
	if structuredResponseCompleted(result.response) {
		result.recordErr = accounting.record(call, result.response, max(time.Duration(0), w.now().Sub(started)))
	}
	return result
}

func (w *Worker) estimateProviderRequest(prepared PreparedStructuredRequest, maxOutputTokens int) (TokenUsage, int64, error) {
	estimate, err := EstimateWireTokenReservation(prepared.WireRequest(), maxOutputTokens)
	if err != nil {
		return TokenUsage{}, 0, err
	}
	cost, err := EstimateCostMicroUSD(estimate, w.Config.Budgets)
	return estimate, cost, err
}

// reserveProviderCall adds immutable request facts to the caller's coordinate.
// Admission order remains a program decision: extraction reserves every primary
// batch before paid I/O, whereas the optional brief is admitted afterwards.
func (w *Worker) reserveProviderCall(ctx context.Context, coordinate BudgetReservationRequest,
	batch PacketBatch, prepared PreparedStructuredRequest, estimate TokenUsage, cost int64,
) (BudgetReservation, error) {
	coordinate.InputHash = prepared.WireSHA256()
	coordinate.ItemCount = len(batch.Packet.Seeds) + len(batch.Packet.Context)
	coordinate.EstimatedRequests = 1
	coordinate.EstimatedInputTokens = estimate.InputTokens
	coordinate.EstimatedOutputTokens = estimate.OutputTokens
	coordinate.EstimatedCostMicroUSD = cost
	coordinate.Budget = w.Config.Budgets
	return w.Store.ReservePersonSweepBudget(ctx, coordinate)
}

type leaseHeartbeatResult struct {
	lease Lease
	err   error
}

// errLeaseHeartbeat distinguishes renewal failures from provider call errors.
// Even a transient store error leaves lease ownership uncertain for this attempt.
var errLeaseHeartbeat = errors.New("person sweep lease heartbeat failed")

func (w *Worker) runPreparedWithLeaseHeartbeat(
	ctx context.Context,
	lease Lease,
	markStarted func(context.Context) error,
	call PreparedStructuredCall,
) (Lease, StructuredResponse, error) {
	heartbeatCtx, cancel := context.WithCancel(ctx)
	stop := make(chan struct{})
	result := make(chan leaseHeartbeatResult, 1)
	interval := max(time.Millisecond, w.Config.LeaseDuration/3)
	go func(current Lease) {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				result <- leaseHeartbeatResult{lease: current}
				return
			case <-ticker.C:
				renewed, err := w.Store.RenewPersonSweep(heartbeatCtx, current, w.Config.LeaseDuration)
				if err != nil || renewed == nil {
					select {
					case <-stop:
						result <- leaseHeartbeatResult{lease: current}
					default:
						if err == nil {
							err = ErrLeaseLost
						}
						result <- leaseHeartbeatResult{lease: current, err: err}
					}
					cancel()
					return
				}
				current = *renewed
			}
		}
	}(lease)
	response, runErr := call.Execute(heartbeatCtx, markStarted)
	close(stop)
	cancel()
	heartbeat := <-result
	if heartbeat.err != nil {
		return heartbeat.lease, response, fmt.Errorf("%w: %w", errLeaseHeartbeat, heartbeat.err)
	}
	return heartbeat.lease, response, runErr
}

// sweepProviderCall is one admitted provider call: the batch it summarizes, the
// exact prepared request, the reservation it holds, and its call coordinate.
type sweepProviderCall struct {
	batch         PacketBatch
	prepared      PreparedStructuredRequest
	estimate      TokenUsage
	estimatedCost int64
	reservation   BudgetReservation
	callOrdinal   int
	purpose       string
}

// sweepCallAccounting is the usage every call in one attempt contributes to.
// The extraction batches and the brief share it so the attempt reports one
// provider identity and one usage total.
type sweepCallAccounting struct {
	budget           BudgetConfig
	completedUsage   []CompletedUsage
	completedBatches []CompletedBatch
	totalUsage       Usage
	providerVersion  string
	modelVersion     string
}

// record validates and accounts a completed call before recording anything. A
// response rejected below is untrusted, and a completed usage record derived
// from it would either fail failure finalization outright (stranding the
// attempt and lease until expiry) or write untrusted values into durable
// history. Finalizing without a record lets the store conservatively charge the
// reservation instead.
func (a *sweepCallAccounting) record(
	call sweepProviderCall, response StructuredResponse, latency time.Duration,
) error {
	if response.Usage.InputTokens < 0 || response.Usage.OutputTokens < 0 {
		return invalidOutputError{errors.New("provider returned negative token usage")}
	}
	if !canonicalProviderIdentity(response.ProviderVersion) ||
		!canonicalProviderIdentity(response.ModelVersion) ||
		!IsSafeProviderMetadata(response.ProviderRequestID) {
		return invalidOutputError{errors.New("provider returned unsafe identity metadata")}
	}
	if a.providerVersion == "" {
		a.providerVersion, a.modelVersion = response.ProviderVersion, response.ModelVersion
	} else if a.providerVersion != response.ProviderVersion || a.modelVersion != response.ModelVersion {
		return invalidOutputError{errors.New("provider call identities differ")}
	}
	accounted, actualCost, err := accountCompletedProviderCall(
		response, call.estimate, call.estimatedCost, a.budget)
	if err != nil {
		return err
	}
	accountedTotal, err := addUsage(a.totalUsage, accounted)
	if err != nil {
		return err
	}
	a.completedUsage = append(a.completedUsage, CompletedUsage{
		BatchOrdinal: call.batch.Ordinal, CallOrdinal: call.callOrdinal, Purpose: call.purpose,
		ProviderRequestID: response.ProviderRequestID, Usage: accountableTokenUsage(response.Usage),
		UsageKnown: response.UsageKnown, Latency: latency,
	})
	a.completedBatches = append(a.completedBatches, CompletedBatch{
		Ordinal: call.batch.Ordinal, CallOrdinal: call.callOrdinal, Purpose: call.purpose,
		ReservationID: call.reservation.ID, InputHash: call.prepared.WireSHA256(),
		ProviderRequestID: response.ProviderRequestID, ProviderVersion: response.ProviderVersion,
		ModelVersion: response.ModelVersion, Usage: accountableTokenUsage(response.Usage),
		UsageKnown: response.UsageKnown, ActualCostMicroUSD: actualCost, Latency: latency,
	})
	a.totalUsage = accountedTotal
	return nil
}
