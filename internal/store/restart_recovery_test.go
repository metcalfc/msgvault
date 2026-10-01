package store_test

import (
	"bytes"
	"encoding/gob"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/peoplesweep"
	"go.kenn.io/msgvault/internal/store"
)

const recoveryChildPath = "MSGVAULT_RECOVERY_CHILD_DB"

type recoveryBoundary struct {
	SourceID  int64
	RunID     int64
	Lease     peoplesweep.Lease
	Started   peoplesweep.BudgetReservation
	Unstarted peoplesweep.BudgetReservation
}

func recoveryJevReservation() jev.DayReservation {
	return jev.DayReservation{Feature: "restart_test", UTCDay: "2026-09-28", Limits: jev.DayLimits{MaxRequests: 1}}
}

// TestAbruptRecoveryChild runs only in the child test binary. No defer or test
// cleanup may close the store before the parent kills this process.
func TestAbruptRecoveryChild(t *testing.T) {
	require := require.New(t)
	path := os.Getenv(recoveryChildPath)
	if path == "" {
		t.Skip("child process only")
	}
	st, err := store.Open(path) // Production WAL and synchronous durability settings.
	require.NoError(err)
	require.NoError(st.InitSchema())
	source, err := st.GetOrCreateSource("gmail", "restart@example.com")
	require.NoError(err)
	_, err = st.CreateSyncOperation(source.ID, "restart-operation")
	require.NoError(err)
	runID, err := st.StartSyncOperation(source.ID, "restart-operation")
	require.NoError(err)
	require.NoError(st.UpdateSyncCheckpoint(runID, &store.Checkpoint{
		PageToken: "resume-token", MessagesProcessed: 17, MessagesAdded: 11,
	}))
	require.NoError(st.ReserveJevDayRequest(t.Context(), recoveryJevReservation()))

	participant, err := st.EnsureParticipant("sweep@example.com", "Synthetic Person", "example.com")
	require.NoError(err)
	person, _, err := st.CreatePersonFromParticipant(participant)
	require.NoError(err)
	_, err = st.SetPersonTrackingContext(t.Context(), person.ID, true)
	require.NoError(err)
	lease, err := st.ClaimPersonSweep(t.Context(), peoplesweep.ClaimRequest{WorkerID: "killed-worker", LeaseDuration: time.Hour})
	require.NoError(err)
	require.NotNil(lease)
	_, err = st.StartPersonSweepRun(t.Context(), peoplesweep.StartRun{
		ID: "restart-sweep", Kind: peoplesweep.RunScheduled, Mode: peoplesweep.RunIncremental,
		ProgramFingerprint: "program-fingerprint", CatalogFingerprint: "catalog-fingerprint",
		ProviderFingerprint: "provider-fingerprint", StartedAt: sweepBudgetNow(),
	})
	require.NoError(err)
	require.NoError(st.StartPersonSweepAttempt(t.Context(), sweepStartAttempt(t,
		"restart-attempt", "restart-sweep", person.ID, lease.Fence)))
	f := personSweepBudgetFixture{store: st, personID: person.ID, runID: "restart-sweep", attemptID: "restart-attempt"}
	started, err := st.ReservePersonSweepBudget(t.Context(), sweepReservation(f, 0, 250, "provider-fingerprint", generousSweepBudget()))
	require.NoError(err)
	require.NoError(st.MarkPersonSweepBudgetStarted(t.Context(), started, *lease))
	unstarted, err := st.ReservePersonSweepBudget(t.Context(), sweepReservation(f, 1, 400, "provider-fingerprint", generousSweepBudget()))
	require.NoError(err)
	// This private handshake uses Go values from the same test binary and follows
	// committed Store calls, never a time-based guess.
	require.NoError(gob.NewEncoder(os.Stdout).Encode(recoveryBoundary{
		SourceID: source.ID, RunID: runID, Lease: *lease, Started: started, Unstarted: unstarted,
	}))
	_, err = io.Copy(io.Discard, os.Stdin)
	require.NoError(err)
}

func TestStoreRecoversAfterAbruptProcessDeath(t *testing.T) {
	requirements := require.New(t)
	path := filepath.Join(t.TempDir(), "archive.db")
	executable, err := os.Executable()
	requirements.NoError(err)
	cmd := exec.Command(executable, "-test.run=^TestAbruptRecoveryChild$")
	cmd.Env = append(os.Environ(), recoveryChildPath+"="+path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	requirements.NoError(err)
	stdin, err := cmd.StdinPipe()
	requirements.NoError(err)
	requirements.NoError(cmd.Start())
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		_ = stdin.Close()
	})
	var boundary recoveryBoundary
	ready := make(chan error, 1)
	go func() { ready <- gob.NewDecoder(stdout).Decode(&boundary) }()
	select {
	case err = <-ready:
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			waited = true
			requirements.NoError(err, "child must commit before termination: %s", stderr.String())
		}
	case <-time.After(30 * time.Second):
		requirements.FailNow("child did not reach its durable boundary")
	}
	observer, err := store.Open(path)
	requirements.NoError(err)
	_, err = observer.StartSync(boundary.SourceID, "full")
	requirements.ErrorIs(err, store.ErrSyncAlreadyActive, "the living child owns the source")
	requirements.NoError(observer.Close())
	requirements.NoError(cmd.Process.Kill())
	requirements.Error(cmd.Wait(), "the child must not exit normally and run cleanup")
	waited = true
	requirements.False(cmd.ProcessState.Success())

	st, err := store.Open(path)
	requirements.NoError(err)
	t.Cleanup(func() { _ = st.Close() })
	t.Run("sync ownership and checkpoint", func(t *testing.T) {
		require := require.New(t)
		assert := assert.New(t)
		active, err := st.GetActiveSync(boundary.SourceID)
		require.ErrorIs(err, store.ErrSyncRunNotFound)
		assert.Nil(active)
		op, err := st.GetSyncOperation("restart-operation")
		require.NoError(err)
		require.Len(op.Runs, 1)
		assert.Equal("failed", op.Status)
		assert.Equal(store.SyncStatusFailed, op.Runs[0].Status)
		assert.Equal(boundary.RunID, op.Runs[0].ID)
		assert.Equal("resume-token", op.Runs[0].CursorBefore.String)
		assert.Equal(int64(17), op.Runs[0].MessagesProcessed)
		assert.Equal(int64(11), op.Runs[0].MessagesAdded)
		checkpoint, err := st.GetLatestCheckpointedSync(boundary.SourceID)
		require.NoError(err, "the importer can retrieve its resumable checkpoint")
		assert.Equal(boundary.RunID, checkpoint.ID)
		assert.Equal("resume-token", checkpoint.CursorBefore.String)
		run, err := st.StartSync(boundary.SourceID, "full")
		require.NoError(err, "the killed process's file lock must be released")
		require.NoError(st.CompleteSync(run, "new-cursor"))
		_, err = st.GetLatestCheckpointedSync(boundary.SourceID)
		require.ErrorIs(err, store.ErrSyncRunNotFound, "successful recovery retires the old checkpoint")
	})
	t.Run("Jev reserved request remains charged", func(t *testing.T) {
		require := require.New(t)
		assert := assert.New(t)
		reservation := recoveryJevReservation()
		counters, err := st.JevDayCounters(t.Context(), reservation.Feature, reservation.UTCDay)
		require.NoError(err)
		assert.Equal(jev.DayCounters{Feature: reservation.Feature, UTCDay: reservation.UTCDay, Requests: 1}, counters,
			"no measured token usage or cost is invented for an interrupted request")
		require.ErrorIs(st.ReserveJevDayRequest(t.Context(), reservation), jev.ErrDayRequestLimit)
		reservation.UTCDay = "2026-09-29"
		require.NoError(st.ReserveJevDayRequest(t.Context(), reservation), "the next UTC day remains usable")
	})
	t.Run("sweep reservations and fenced retry", func(t *testing.T) {
		require := require.New(t)
		assert := assert.New(t)
		recovered, err := st.RecoverPersonSweepRunsContext(t.Context())
		require.NoError(err)
		assert.Equal(int64(1), recovered)
		recovered, err = st.RecoverPersonSweepRunsContext(t.Context())
		require.NoError(err)
		assert.Zero(recovered, "recovery is idempotent")
		attempts, err := st.ListPersonSweepAttempts(t.Context(), peoplesweep.AttemptFilter{RunID: "restart-sweep", Limit: 10})
		require.NoError(err)
		require.Len(attempts, 1)
		assert.Equal(peoplesweep.AttemptFailed, attempts[0].Status)
		assert.Equal(peoplesweep.FailureLeaseLost, attempts[0].FailureClass)
		assert.Equal(peoplesweep.Usage{Requests: 1, InputTokens: 250, OutputTokens: 100, EstimatedCostMicroUSD: 350}, attempts[0].Usage,
			"started work keeps its conservative charge; unsent work releases its reservation")
		var reserved, actual int64
		require.NoError(st.DB().QueryRowContext(t.Context(), `SELECT reserved_requests, actual_requests FROM person_sweep_daily_usage WHERE utc_day = ?`, testSweepUTCDate).Scan(&reserved, &actual))
		assert.Zero(reserved)
		assert.Equal(int64(1), actual)
		lease := claimPersonSweepFixture(t, st, "replacement-worker")
		assert.Equal(boundary.Lease.PersonID, lease.PersonID)
		assert.Greater(lease.Fence, boundary.Lease.Fence)
		require.Error(st.MarkPersonSweepBudgetStarted(t.Context(), boundary.Unstarted, boundary.Lease), "a stale worker cannot dispatch after recovery")
		require.Error(st.MarkPersonSweepBudgetStarted(t.Context(), boundary.Started, boundary.Lease), "a stale started call cannot regain ownership")
	})
}
