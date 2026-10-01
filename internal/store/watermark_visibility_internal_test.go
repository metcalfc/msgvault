package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSQLiteQuiescentProofsAreStoredInTheOrderTheyAreProved pins that the
// remembered proof is the one proved last, not the one that finished last.
//
// The probe proves an instant under the database's write lock, so concurrent
// pages prove instants in a definite order — but two of them could return from
// the probe in one order and reach the shared state in the other, storing an
// older proof over a newer one and making complete_through regress. Proving and
// storing therefore happen under the same lock, which is what this asserts: a
// second prover cannot get in while the first is still proving.
func TestSQLiteQuiescentProofsAreStoredInTheOrderTheyAreProved(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	base := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	older, newer := base.Add(5*time.Second), base.Add(9*time.Second)

	d := &SQLiteDialect{}
	secondCalling := make(chan struct{})
	secondDone := make(chan struct{})

	// The first prover holds the write lock for a while and comes back with the
	// OLDER instant, because it read the clock before the second one did.
	first, err := d.proveQuiescentInstant(
		func() (time.Time, bool, time.Time, error) {
			go func() {
				defer close(secondDone)
				// The barrier the timeout below depends on. Without it the
				// second prover being still unscheduled is indistinguishable
				// from its being blocked, and the whole test passes on a busy
				// machine without exercising anything -- it passes even with
				// the serialisation removed outright.
				close(secondCalling)
				_, _ = d.proveQuiescentInstant(
					func() (time.Time, bool, time.Time, error) {
						return newer, true, newer, nil
					})
			}()
			<-secondCalling
			select {
			case <-secondDone:
				require.FailNow(
					"a second prover stored its proof while the first was still " +
						"proving; the two can then store out of order and the older " +
						"proof wins")
			case <-time.After(50 * time.Millisecond):
			}
			return older, true, older, nil
		})
	require.NoError(err, "first prover")
	assert.Equal(older, first.CommitBound, "the first prover publishes its own proof")

	<-secondDone

	// A probe that timed out proves nothing and falls back to the remembered
	// instant, which must be the one proved last.
	fallback, err := d.proveQuiescentInstant(
		func() (time.Time, bool, time.Time, error) {
			return time.Time{}, false, newer.Add(time.Second), nil
		})
	require.NoError(err, "timed-out probe")
	assert.Equal(newer, fallback.CommitBound,
		"the proof taken last must be the one remembered")
}
