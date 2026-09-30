// Package scheduler provides cron-based scheduling for automated email sync.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/jobctx"
	"go.kenn.io/msgvault/internal/syncerr"
)

// SyncFunc is the callback invoked when a scheduled sync should run.
// It receives the account email and should perform incremental sync + cache build.
type SyncFunc func(ctx context.Context, email string) error

type WorkTracker interface {
	BeginWork() (func(), bool)
	BeginWorkContext(ctx context.Context) (func(), bool)
}

// YieldChecker is optionally implemented by work trackers that can report a
// waiter the running job should yield to. Scheduled jobs are resumable, so
// they step aside rather than block an interactive command for their whole
// runtime.
type YieldChecker interface {
	ShouldYield() bool
}

// ErrYieldedToWaiter is the cancellation cause set when a scheduled job is
// interrupted to let a waiting operation acquire the work gate.
var ErrYieldedToWaiter = jobctx.ErrYieldedToWaiter

// ErrReschedule asks the scheduler to run another bounded pass after queued
// work. It does not record a job failure or wait for the next cron tick.
var ErrReschedule = errors.New("scheduled job has more work")

// yieldPollInterval is how often a running scheduled job checks for waiters.
// Variable only so tests can shorten it.
var yieldPollInterval = 5 * time.Second

// AccountStatus represents the sync status of a scheduled account.
type AccountStatus struct {
	Email     string    `json:"email"`
	Running   bool      `json:"running"`
	LastRun   time.Time `json:"last_run,omitzero"`
	NextRun   time.Time `json:"next_run"`
	Schedule  string    `json:"schedule"`
	LastError string    `json:"last_error,omitempty"`
	// Queued reports a run waiting for the daemon operation gate.
	Queued bool `json:"queued,omitempty"`
	// Pending reports a follow-up run requested while the current run was
	// executing or yielded; it queues when the current run finishes.
	Pending bool `json:"pending,omitempty"`
	// StartedAt is when the current run began executing (after the gate).
	StartedAt time.Time `json:"started_at,omitzero"`
}

type Job struct {
	Name     string
	Schedule string
	Run      func(context.Context) error
	// Preemptible permits cancellation for another scheduled job. Enable only
	// when the job can resume from persisted progress after cancellation.
	Preemptible bool
}

type JobStatus struct {
	Name      string    `json:"name"`
	Running   bool      `json:"running"`
	LastRun   time.Time `json:"last_run,omitzero"`
	NextRun   time.Time `json:"next_run"`
	Schedule  string    `json:"schedule"`
	LastError string    `json:"last_error,omitempty"`
	Queued    bool      `json:"queued,omitempty"`
	Pending   bool      `json:"pending,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
}

// scheduledJob keeps a registration and its run state together. Records stay
// stable across removal and re-registration so an active run keeps its
// reservation and can hand it to the current callback. All fields use s.mu.
type scheduledJob struct {
	entryID     cron.EntryID
	schedule    string
	registered  bool
	run         func(context.Context) error // generic jobs only
	preemptible bool
	running     bool
	queued      bool
	pending     bool
	startedAt   time.Time
	lastRun     time.Time
	lastErr     error
}

// Scheduler manages cron-based email sync scheduling.
type Scheduler struct {
	cron                    *cron.Cron
	syncFunc                SyncFunc
	logger                  *slog.Logger
	work                    WorkTracker
	accountPreemptionPolicy func(string) bool

	mu          sync.RWMutex
	accounts    map[string]*scheduledJob
	genericJobs map[string]*scheduledJob

	// queuedRuns counts runs blocked waiting for the work gate.
	queuedRuns int

	// Embed job state (optional). Set via SetEmbedJob; cron.EntryID 0
	// may be valid, so embedEntrySet tracks whether an entry exists.
	embedJob                   *EmbedJob
	embedEntry                 cron.EntryID
	embedEntrySet              bool
	runEmbedAfterSync          bool
	visualPostSync             func(context.Context) error
	visualPostRunning          bool
	visualPostPending          bool
	documentVectorJob          func(context.Context) error
	documentVectorEntry        cron.EntryID
	documentVectorEntrySet     bool
	runDocumentVectorAfterSync bool

	ctx     context.Context    // cancelled on Stop
	cancel  context.CancelFunc // cancels ctx
	wg      sync.WaitGroup     // tracks running sync goroutines
	started bool               // true after Start(), false after Stop()
	stopped bool               // true after Stop()
}

// SetVisualPostSyncJob installs the independently consented visual lane's
// bounded post-sync pass. Nil disables the hook.
func (s *Scheduler) SetVisualPostSyncJob(run func(context.Context) error) {
	s.mu.Lock()
	s.visualPostSync = run
	s.mu.Unlock()
}

// New creates a new Scheduler with the given sync callback.
func New(syncFunc SyncFunc) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		cron: cron.New(cron.WithParser(cron.NewParser(
			cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
		))),
		syncFunc:    syncFunc,
		logger:      slog.Default(),
		accounts:    make(map[string]*scheduledJob),
		genericJobs: make(map[string]*scheduledJob),
		ctx:         ctx,
		cancel:      cancel,
	}
}

// WithLogger sets the logger for the scheduler.
func (s *Scheduler) WithLogger(logger *slog.Logger) *Scheduler {
	s.logger = logger
	return s
}

func (s *Scheduler) WithWorkTracker(tracker WorkTracker) *Scheduler {
	s.work = tracker
	return s
}

// WithAccountPreemptionPolicy reports whether an account sync can safely
// yield to other scheduled work. When unset, account syncs remain preemptible.
func (s *Scheduler) WithAccountPreemptionPolicy(policy func(string) bool) *Scheduler {
	s.mu.Lock()
	s.accountPreemptionPolicy = policy
	s.mu.Unlock()
	return s
}

// AddAccount schedules sync for an account using the given cron expression.
// Returns an error if the cron expression is invalid.
func (s *Scheduler) AddAccount(email, cronExpr string) error {
	if err := ValidateCronExpr(cronExpr); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove existing schedule if present
	state := s.accountStateLocked(email)
	if state.registered {
		s.cron.Remove(state.entryID)
		state.registered = false
		state.schedule = ""
	}

	// Validate and add the cron job
	entryID, err := s.cron.AddFunc(cronExpr, func() {
		s.onAccountTick(email)
	})
	if err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", cronExpr, err)
	}

	state.entryID = entryID
	state.schedule = cronExpr
	state.registered = true
	s.logger.Info("scheduled sync",
		"email", email,
		"schedule", cronExpr,
		"next_run", s.nextRun(entryID))

	return nil
}

// onAccountTick handles one cron firing for an account. A tick that finds
// the previous run still waiting for the gate is redundant; one that finds it
// executing is remembered as a single follow-up run instead of being lost.
func (s *Scheduler) onAccountTick(email string) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	if !s.reserveRunLocked(s.accountStateLocked(email), "email", email, true) {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.runSync(email)
}

// coalesceTickLocked records a tick that arrived while its job was active.
// The caller holds s.mu.
func (s *Scheduler) coalesceTickLocked(state *scheduledJob, kind, name string) {
	if state.queued {
		s.logger.Debug("scheduled tick dropped: previous run is still waiting to start", kind, name)
		return
	}
	if state.pending {
		return
	}
	state.pending = true
	s.logger.Info("scheduled sync skipped: previous run still active; queued one follow-up run", kind, name)
}

// AddAccountsFromConfig adds all enabled accounts from the config.
// Returns the number of accounts scheduled and any errors encountered.
func (s *Scheduler) AddAccountsFromConfig(cfg *config.Config) (int, []error) {
	var errors []error
	scheduled := 0

	for _, acc := range cfg.ScheduledAccounts() {
		if err := s.AddAccount(acc.Email, acc.Schedule); err != nil {
			errors = append(errors, fmt.Errorf("%s: %w", acc.Email, err))
		} else {
			scheduled++
		}
	}

	return scheduled, errors
}

func (s *Scheduler) AddJob(job Job) error {
	if job.Name == "" || job.Run == nil {
		return errors.New("job name and run function are required")
	}
	if err := ValidateCronExpr(job.Schedule); err != nil {
		return err
	}
	entryID, err := s.cron.AddFunc(job.Schedule, func() {
		s.onJobTick(job.Name)
	})
	if err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", job.Schedule, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.genericJobs[job.Name]
	if state == nil {
		state = &scheduledJob{}
		s.genericJobs[job.Name] = state
	}
	if state.registered {
		s.cron.Remove(state.entryID)
	}
	state.entryID = entryID
	state.schedule = job.Schedule
	state.registered = true
	state.run = job.Run
	state.preemptible = job.Preemptible
	s.logger.Info("scheduled job", "job", job.Name, "schedule", job.Schedule, "next_run", s.nextRun(entryID))
	return nil
}

// RemoveJob removes a generic job's cron entry and callable registration.
// An invocation already running is allowed to finish under the normal work
// gate; no future cron or manual invocation can start after removal returns.
func (s *Scheduler) RemoveJob(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state := s.genericJobs[name]; state != nil {
		if state.registered {
			s.cron.Remove(state.entryID)
		}
		state.registered = false
		state.schedule = ""
		state.run = nil
		state.preemptible = false
		state.lastRun = time.Time{}
		state.lastErr = nil
		state.pending = false
	}
	s.logger.Info("removed scheduled job", "job", name)
}

// RemoveAccount removes the schedule for an account.
func (s *Scheduler) RemoveAccount(email string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state := s.accounts[email]; state != nil {
		if state.registered {
			s.cron.Remove(state.entryID)
			state.registered = false
			state.schedule = ""
			s.logger.Info("removed schedule", "email", email)
		}
		state.pending = false
	}
}

// SetEmbedJob registers the embed job on a cron schedule. If schedule
// is empty, no cron entry is created (the job can still fire via the
// post-sync hook when runAfterSync is true). Replacing a previously-set
// job removes the old cron entry. Passing nil clears any existing job.
//
// A schedule rejected by ValidateCronExpr is caught before any state
// mutates, so the previous job and cron entry are preserved. An
// internal AddFunc failure after that point is treated as an invariant
// violation (ValidateCronExpr already accepted the expression) and
// clears the embed job rather than restoring the prior one.
func (s *Scheduler) SetEmbedJob(job *EmbedJob, schedule string, runAfterSync bool) error {
	// Validate the cron expression before mutating any state so a bad
	// schedule can't leave the scheduler with a half-removed previous
	// job. ValidateCronExpr is cheap and pure.
	if job != nil && schedule != "" {
		if err := ValidateCronExpr(schedule); err != nil {
			return fmt.Errorf("invalid embed cron expression %q: %w", schedule, err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.embedEntrySet {
		s.cron.Remove(s.embedEntry)
		s.embedEntrySet = false
	}
	s.embedJob = job
	s.runEmbedAfterSync = runAfterSync && job != nil

	if job == nil || schedule == "" {
		return nil
	}
	entryID, err := s.cron.AddFunc(schedule, func() {
		if s.isStopped() {
			return
		}
		done, ok := s.beginWork()
		if !ok {
			return
		}
		defer done()
		runCtx, endRun := s.jobContext("embed", false)
		defer endRun()
		job.Run(runCtx)
	})
	if err != nil {
		// ValidateCronExpr above should have caught any parse error;
		// if AddFunc still fails here it's an internal invariant
		// violation, not caller input. Roll back the state we mutated.
		s.embedJob = nil
		s.runEmbedAfterSync = false
		return fmt.Errorf("register embed cron: %w", err)
	}
	s.embedEntry = entryID
	s.embedEntrySet = true
	s.logger.Info("scheduled embed job",
		"schedule", schedule,
		"next_run", s.nextRun(entryID))
	return nil
}

// SetDocumentVectorJob installs the bounded document-vector convergence job
// on the same cron/post-sync policy used by message embeddings.
func (s *Scheduler) SetDocumentVectorJob(job func(context.Context) error, schedule string, runAfterSync bool) error {
	if job != nil && schedule != "" {
		if err := ValidateCronExpr(schedule); err != nil {
			return fmt.Errorf("invalid document vector cron expression %q: %w", schedule, err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.documentVectorEntrySet {
		s.cron.Remove(s.documentVectorEntry)
		s.documentVectorEntrySet = false
	}
	s.documentVectorJob = job
	s.runDocumentVectorAfterSync = runAfterSync && job != nil
	if job == nil || schedule == "" {
		return nil
	}
	entry, err := s.cron.AddFunc(schedule, func() {
		if s.isStopped() {
			return
		}
		done, ok := s.beginWork()
		if !ok {
			return
		}
		defer done()
		runCtx, endRun := s.jobContext("document-vector", false)
		defer endRun()
		if runErr := job(runCtx); runErr != nil {
			s.logger.Error("scheduled document vector reconciliation failed", "error", runErr)
		}
	})
	if err != nil {
		s.documentVectorJob = nil
		s.runDocumentVectorAfterSync = false
		return fmt.Errorf("register document vector cron: %w", err)
	}
	s.documentVectorEntry = entry
	s.documentVectorEntrySet = true
	return nil
}

// isStopped reports s.stopped under a read lock. Used by cron
// callbacks that only need to abort on shutdown.
func (s *Scheduler) isStopped() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stopped
}

// Start begins executing scheduled jobs.
func (s *Scheduler) Start() {
	s.mu.Lock()
	s.started = true
	s.stopped = false
	accounts := 0
	for _, state := range s.accounts {
		if state.registered {
			accounts++
		}
	}
	s.mu.Unlock()

	s.cron.Start()
	s.logger.Info("scheduler started", "jobs", accounts)
}

// IsRunning returns true if the scheduler has been started and not yet stopped.
func (s *Scheduler) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.started && !s.stopped
}

// Stop gracefully stops the scheduler, cancels running sync jobs, and waits
// for them to finish. Returns a context that is done when all work completes.
// Stop also waits for any post-sync embed passes that are in flight. Those
// passes receive the cancelled s.ctx and are expected to bail quickly.
func (s *Scheduler) Stop() context.Context {
	s.logger.Info("scheduler stopping")

	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()

	cronCtx := s.cron.Stop()
	s.cancel() // signal running syncs to stop

	done := make(chan struct{})
	go func() {
		<-cronCtx.Done()
		s.wg.Wait()
		close(done)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-done
		cancel()
	}()
	return ctx
}

// runSync executes sync for an account (called by cron or TriggerSync).
// The caller must have reserved the account with reserveRunLocked.
func (s *Scheduler) runSync(email string) {
	defer s.wg.Done()
	defer s.finishAccountRun(email)

	s.mu.RLock()
	state := s.accounts[email]
	s.mu.RUnlock()
	done, ok := s.beginJobWork(state)
	if !ok {
		s.logger.Info("scheduled sync skipped: daemon is draining", "email", email)
		return
	}
	defer done()

	s.logger.Info("starting scheduled sync", "email", email)
	start := time.Now()

	s.mu.RLock()
	preemptionPolicy := s.accountPreemptionPolicy
	s.mu.RUnlock()
	preemptible := preemptionPolicy == nil || preemptionPolicy(email)
	runCtx, endRun := s.jobContext("sync "+email, preemptible)
	err := s.syncFunc(runCtx, email)
	endRun()
	yielded := yieldedToWaiter(runCtx) || jobctx.PreemptionRequested(runCtx)

	s.mu.Lock()
	if yielded {
		if callbackErr := callbackErrorAfterYield(runCtx, err); callbackErr != nil {
			state.lastErr = callbackErr
			logScheduledSyncError(s.logger, email, time.Since(start), callbackErr)
		}
		if state.registered {
			state.pending = true
		}
		s.logger.Info("scheduled sync yielded to a waiting operation; queued follow-up",
			"email", email,
			"duration", time.Since(start))
	} else if err != nil {
		state.lastErr = err
		logScheduledSyncError(s.logger, email, time.Since(start), err)
	} else {
		state.lastRun = time.Now()
		state.lastErr = nil
		s.logger.Info("scheduled sync completed",
			"email", email,
			"duration", time.Since(start))
	}
	s.mu.Unlock()

	// Post-sync embed hook: only fire on successful sync, and only
	// when configured. Runs synchronously in this goroutine so it's
	// naturally covered by the wg.Done deferred above.
	if err != nil || yielded {
		return
	}
	var postSync *EmbedJob
	s.mu.RLock()
	if s.runEmbedAfterSync && s.embedJob != nil && !s.stopped {
		postSync = s.embedJob
	}
	s.mu.RUnlock()
	if postSync != nil {
		embedCtx, endEmbed := s.jobContext("post-sync embed", false)
		postSync.Run(embedCtx)
		endEmbed()
	}
	var documentVectorPostSync func(context.Context) error
	s.mu.RLock()
	if s.runDocumentVectorAfterSync && s.documentVectorJob != nil && !s.stopped {
		documentVectorPostSync = s.documentVectorJob
	}
	s.mu.RUnlock()
	if documentVectorPostSync != nil {
		documentCtx, endDocument := s.jobContext("post-sync document vector", false)
		if documentErr := documentVectorPostSync(documentCtx); documentErr != nil {
			s.logger.Error("post-sync document vector reconciliation failed", "error", documentErr)
		}
		endDocument()
	}
	s.startVisualPostSync()
}

func callbackErrorAfterYield(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if yieldedToWaiter(ctx) {
		return withoutYieldCancellation(err)
	}
	return err
}

func withoutYieldCancellation(err error) error {
	_, filtered := filterYieldCancellation(err)
	return filtered
}

type yieldFilteredError struct {
	message string
	cause   error
}

func (e yieldFilteredError) Error() string { return e.message }

func (e yieldFilteredError) Unwrap() error { return e.cause }

func filterYieldCancellation(err error) (bool, error) {
	if err == nil {
		return false, nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		kept := make([]error, 0, len(causes))
		changed := false
		for _, cause := range causes {
			filteredOut, filtered := filterYieldCancellation(cause)
			changed = changed || filteredOut
			if filtered != nil {
				kept = append(kept, filtered)
			}
		}
		if !changed {
			return false, err
		}
		return true, errors.Join(kept...)
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		cause := wrapped.Unwrap()
		changed, filtered := filterYieldCancellation(cause)
		if !changed {
			return false, err
		}
		if filtered == nil {
			return true, nil
		}
		message := err.Error()
		if causeMessage := cause.Error(); causeMessage != "" {
			message = strings.Replace(message, causeMessage, filtered.Error(), 1)
		}
		if message == err.Error() {
			message = fmt.Sprintf("%s: %s", message, filtered.Error())
		}
		return true, yieldFilteredError{message: message, cause: filtered}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrYieldedToWaiter) {
		return true, nil
	}
	return false, err
}

// logScheduledSyncError keeps transient network failures below the error
// level while preserving other callback errors for scheduled account status.
func logScheduledSyncError(logger *slog.Logger, email string, duration time.Duration, err error) {
	if syncerr.IsTransientNetwork(err) {
		logger.Warn("scheduled sync skipped (transient network)",
			"email", email,
			"duration", duration,
			"error", err)
		return
	}
	logger.Error("scheduled sync failed",
		"email", email,
		"duration", duration,
		"error", err)
}

// finishAccountRun releases an account run, or hands its reservation to the
// single follow-up requested by a tick or yield. The follow-up is
// reserved (running stays true, wg.Add) before the finishing run's wg.Done.
func (s *Scheduler) finishAccountRun(email string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishRunLocked(s.accounts[email], func() { s.runSync(email) })
}

// startVisualPostSync queues hosted multimodal work after the source sync has
// committed. It deliberately does not wait in the sync goroutine: provider
// latency must never extend or fail an otherwise successful archive sync.
func (s *Scheduler) startVisualPostSync() {
	s.mu.Lock()
	if s.visualPostSync == nil || s.stopped {
		s.mu.Unlock()
		return
	}
	if s.visualPostRunning {
		// A sync finished while a pass is in flight; remember it so the
		// changes it committed are processed right after, instead of waiting
		// for the next sync or cron tick.
		s.visualPostPending = true
		s.mu.Unlock()
		return
	}
	run := s.visualPostSync
	s.visualPostRunning = true
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			s.visualPostRunning = false
			rerun := s.visualPostPending
			s.visualPostPending = false
			s.mu.Unlock()
			if rerun {
				s.startVisualPostSync()
			}
		}()
		done, ok := s.beginWork()
		if !ok {
			return
		}
		defer done()
		visualCtx, endVisual := s.jobContext("post-sync multimodal", false)
		defer endVisual()
		if err := run(visualCtx); err != nil {
			s.logger.Error("post-sync multimodal pass failed", "error", err)
		}
	}()
}

// IsScheduled returns true if the account has been added to the scheduler.
func (s *Scheduler) IsScheduled(email string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.accounts[email]
	return state != nil && state.registered
}

// TriggerSync manually triggers a sync for an account (outside of schedule).
// Returns an error if a sync is already running, the account is not scheduled,
// or the scheduler has been stopped.
func (s *Scheduler) TriggerSync(email string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return errors.New("scheduler is stopped")
	}

	state := s.accounts[email]
	if state == nil || !state.registered {
		return fmt.Errorf("account %s is not scheduled", email)
	}
	if !s.reserveRunLocked(state, "email", email, false) {
		return fmt.Errorf("sync already running for %s", email)
	}
	go s.runSync(email)
	return nil
}

func (s *Scheduler) IsJobScheduled(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.genericJobs[name]
	return state != nil && state.registered
}

// TriggerJob synchronously reserves and runs the named generic job. It is
// used by the cron scheduler (which fires jobs from its own goroutine with
// no gate held) and relies on running to completion before returning so
// lastRun/lastErr are recorded synchronously.
func (s *Scheduler) TriggerJob(name string) error {
	run, ok, err := s.reserveGenericJob(name, false)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return s.runJob(name, run)
}

// StartJob asynchronously reserves and runs the named generic job in a new
// goroutine, returning as soon as the reservation succeeds. This is used by
// callers (e.g. an HTTP handler) that may already hold the daemon's
// operation gate, so the job's gate acquisition must happen after the
// caller has had a chance to return and release it.
func (s *Scheduler) StartJob(name string) error {
	run, ok, err := s.reserveGenericJob(name, false)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	go func() {
		_ = s.runJob(name, run)
	}()
	return nil
}

// onJobTick handles one cron firing for a generic job, coalescing a tick
// that arrives while the job is active like onAccountTick does.
func (s *Scheduler) onJobTick(name string) {
	run, ok, err := s.reserveGenericJob(name, true)
	if err != nil || !ok {
		return
	}
	_ = s.runJob(name, run)
}

// reserveGenericJob validates and reserves the named generic job under the
// lock, mirroring TriggerSync's reservation of an account sync. ok is false
// when the job is already running (a no-op, not an error); a cron tick
// (coalesce) is then remembered as one follow-up run.
func (s *Scheduler) reserveGenericJob(name string, coalesce bool) (run func(context.Context) error, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.genericJobs[name]
	if state == nil || state.run == nil {
		return nil, false, fmt.Errorf("job %q is not scheduled", name)
	}
	if s.stopped {
		return nil, false, errors.New("scheduler is stopped")
	}
	if !s.reserveRunLocked(state, "job", name, coalesce) {
		return nil, false, nil
	}
	run = state.run
	return run, true, nil
}

// runJob executes an already-reserved generic job and records the result.
// The caller must have reserved the job with reserveRunLocked.
func (s *Scheduler) runJob(name string, run func(context.Context) error) error {
	defer s.wg.Done()
	defer s.finishGenericRun(name)

	s.mu.RLock()
	state := s.genericJobs[name]
	preemptible := state.preemptible
	s.mu.RUnlock()
	done, ok := s.beginJobWork(state)
	if !ok {
		return nil
	}
	defer done()

	runCtx, endRun := s.jobContext(name, preemptible)
	err := run(runCtx)
	endRun()
	s.mu.Lock()
	defer s.mu.Unlock()
	yielded := yieldedToWaiter(runCtx) || jobctx.PreemptionRequested(runCtx)
	if errors.Is(err, ErrReschedule) {
		state.pending = true
		s.logger.Info("scheduled job has remaining work; queued follow-up",
			"job", name)
		return nil
	}
	if yielded {
		state.pending = true
		if callbackErr := callbackErrorAfterYield(runCtx, err); callbackErr != nil {
			state.lastErr = callbackErr
			s.logger.Error("scheduled job yielded after callback error; queued follow-up",
				"job", name,
				"error", callbackErr)
			return callbackErr
		}
		s.logger.Info("scheduled job yielded to waiting work; queued follow-up",
			"job", name)
		return nil
	}
	if err != nil {
		state.lastErr = err
		return err
	}
	state.lastRun = time.Now()
	state.lastErr = nil
	return nil
}

// finishGenericRun releases a generic job run, or hands its reservation to
// the single follow-up requested by a tick, yield, or bounded pass.
func (s *Scheduler) finishGenericRun(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.genericJobs[name]
	// Use the current registration after replacement; removal drops a rerun.
	var rerun func()
	if current := state.run; current != nil {
		rerun = func() { _ = s.runJob(name, current) }
	}
	s.finishRunLocked(state, rerun)
}

// accountStateLocked preserves account history even if its schedule is removed.
func (s *Scheduler) accountStateLocked(email string) *scheduledJob {
	state := s.accounts[email]
	if state == nil {
		state = &scheduledJob{}
		s.accounts[email] = state
	}
	return state
}

// reserveRunLocked makes every entry point reserve work before launching it.
// Validation and duplicate-manual-trigger behavior remain with the caller.
func (s *Scheduler) reserveRunLocked(state *scheduledJob, kind, name string, coalesce bool) bool {
	if state.running {
		if coalesce {
			s.coalesceTickLocked(state, kind, name)
		}
		return false
	}
	state.running = true
	s.wg.Add(1)
	return true
}

// finishRunLocked transfers a pending reservation before the current run calls
// wg.Done, so Stop cannot return in the gap between consecutive runs.
func (s *Scheduler) finishRunLocked(state *scheduledJob, rerun func()) {
	state.startedAt = time.Time{}
	pending := state.pending
	state.pending = false
	if pending && !s.stopped && rerun != nil {
		s.wg.Add(1)
		go rerun()
		return
	}
	state.running = false
}

// beginJobWork exposes the same gate-wait and execution states for both kinds
// of jobs. Gate release remains deferred by the caller before finalization.
func (s *Scheduler) beginJobWork(state *scheduledJob) (func(), bool) {
	s.mu.Lock()
	state.queued = true
	s.mu.Unlock()
	done, ok := s.beginWork()
	s.mu.Lock()
	state.queued = false
	if ok {
		state.startedAt = time.Now()
	}
	s.mu.Unlock()
	return done, ok
}

func (s *Scheduler) beginWork() (func(), bool) {
	if s.work == nil {
		return func() {}, true
	}
	s.mu.Lock()
	s.queuedRuns++
	s.mu.Unlock()
	done, ok := s.work.BeginWorkContext(s.ctx)
	s.mu.Lock()
	s.queuedRuns--
	s.mu.Unlock()
	return done, ok
}

// preemptAfter is how long a scheduled run may hold the work gate while
// other scheduled runs are queued before it is asked to yield. Variable only
// so tests can shorten it.
var preemptAfter = time.Minute

// jobContext derives the context a scheduled job runs with. When the work
// tracker can report waiters, the context is cancelled with cause
// ErrYieldedToWaiter so the resumable job steps aside for the waiter and
// is queued to resume after the waiter. Preemptible contexts also carry a
// cooperative preemption request (see jobctx.WithPreemption), raised once the
// run has held the gate for preemptAfter while other scheduled runs queue
// behind it. Jobs that have not stopped at the next poll are cancelled with
// ErrYieldedToWaiter. Other jobs keep their own runtime budgets. The returned
// stop function must be called when the job finishes.
func (s *Scheduler) jobContext(label string, preemptible bool) (context.Context, func()) {
	started := time.Now()
	preemptCtx, requestPreemption := jobctx.WithPreemption(s.ctx)
	yc, canYield := s.work.(YieldChecker)
	if s.work == nil {
		return preemptCtx, func() {}
	}
	ctx, cancel := context.WithCancelCause(preemptCtx)
	go func() {
		ticker := time.NewTicker(yieldPollInterval)
		defer ticker.Stop()
		preempted := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if canYield && yc.ShouldYield() {
					cancel(ErrYieldedToWaiter)
					return
				}
				if preempted {
					cancel(ErrYieldedToWaiter)
					return
				}
				if preemptible && !preempted && time.Since(started) >= preemptAfter && s.hasQueuedRuns() {
					preempted = true
					s.logger.Info("scheduled job asked to yield to queued work",
						"job", label,
						"held", time.Since(started).Round(time.Second))
					requestPreemption()
					// Give cooperative jobs one poll interval to reach a safe
					// checkpoint, then cancel with the recognized yield cause so
					// other sources still release the work gate.
				}
			}
		}
	}()
	return ctx, func() { cancel(nil) }
}

func (s *Scheduler) hasQueuedRuns() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queuedRuns > 0
}

// nextRun reports when a cron entry fires next. robfig/cron fills Entry.Next
// only once its run loop has started, so before Start the value is computed
// from the schedule instead of reporting the zero time.
func (s *Scheduler) nextRun(entryID cron.EntryID) time.Time {
	entry := s.cron.Entry(entryID)
	if !entry.Next.IsZero() || entry.Schedule == nil {
		return entry.Next
	}
	return entry.Schedule.Next(time.Now())
}

func yieldedToWaiter(ctx context.Context) bool {
	return jobctx.YieldedToWaiter(ctx)
}

// Status returns the current status of all scheduled accounts.
func (s *Scheduler) Status() []AccountStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var statuses []AccountStatus
	for email, state := range s.accounts {
		if !state.registered {
			continue
		}
		status := AccountStatus{
			Email:     email,
			Running:   state.running,
			LastRun:   state.lastRun,
			NextRun:   s.nextRun(state.entryID),
			Schedule:  state.schedule,
			Queued:    state.queued,
			Pending:   state.pending,
			StartedAt: state.startedAt,
		}
		if state.lastErr != nil {
			status.LastError = state.lastErr.Error()
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func (s *Scheduler) JobStatus() []JobStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]JobStatus, 0, len(s.genericJobs))
	for name, state := range s.genericJobs {
		if !state.registered {
			continue
		}
		var lastErr string
		if state.lastErr != nil {
			lastErr = state.lastErr.Error()
		}
		out = append(out, JobStatus{
			Name:      name,
			Running:   state.running,
			LastRun:   state.lastRun,
			NextRun:   s.nextRun(state.entryID),
			Schedule:  state.schedule,
			LastError: lastErr,
			Queued:    state.queued,
			Pending:   state.pending,
			StartedAt: state.startedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// NormalizeCronExpr trims a schedule and turns a value that is only a time
// zone prefix into the empty string: with no fields after the zone there is
// no schedule, so it is off rather than an error.
func NormalizeCronExpr(expr string) string {
	trimmed := strings.TrimSpace(expr)
	if strings.HasPrefix(trimmed, "TZ=") || strings.HasPrefix(trimmed, "CRON_TZ=") {
		if !strings.ContainsAny(trimmed, " \t") {
			return ""
		}
	}
	return trimmed
}

// ValidateCronExpr validates a cron expression without scheduling anything.
// It accepts what the scheduler's parser accepts: five fields, optionally
// preceded by a "CRON_TZ=<zone>" or "TZ=<zone>" prefix. It also rejects a
// field made only of commas, which the parser stores but never matches.
func ValidateCronExpr(expr string) error {
	fields := expr
	if strings.HasPrefix(fields, "TZ=") || strings.HasPrefix(fields, "CRON_TZ=") {
		// The parser slices at the first space without checking for one.
		space := strings.Index(fields, " ")
		if space < 0 {
			return errors.New("invalid cron expression: the time zone must be followed by the five schedule fields")
		}
		fields = fields[space:]
	}
	for field := range strings.FieldsSeq(fields) {
		if strings.Trim(field, ",") == "" {
			return fmt.Errorf("invalid cron expression: field %q lists no values", field)
		}
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if _, err := parser.Parse(expr); err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}
	return nil
}
