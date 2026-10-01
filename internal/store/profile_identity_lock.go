package store

import "context"

// lockProfileIdentityKeyTxContext reserves SQLite's writer before a logical
// profile-identity check-then-insert. Key arguments describe the calling
// operation; SQLite serializes all of these mutations with the same writer lock.
func (s *Store) lockProfileIdentityKeyTxContext(
	ctx context.Context, tx *loggedTx, _ string, _ ...any,
) error {
	return s.lockIdentityMutationTxContext(ctx, tx)
}
