package store

import (
	"errors"
	"fmt"
)

// FinalizeSyncFailure records a failed run's last counters and returns its cause,
// joined with any error recording the terminal state. A nil cause is a no-op.
// Like FailSyncWithCheckpoint, cleanup does not inherit the import's context:
// cancellation must not prevent recording the failed run or releasing its lock.
func (s *Store) FinalizeSyncFailure(syncID int64, cause error, checkpoint *Checkpoint) error {
	if cause == nil {
		return nil
	}
	if err := s.FailSyncWithCheckpoint(syncID, cause.Error(), checkpoint); err != nil {
		return errors.Join(cause, fmt.Errorf("record failed sync %d: %w", syncID, err))
	}
	return cause
}
