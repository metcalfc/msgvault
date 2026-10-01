package store

import (
	"context"
)

// lockProfileIdentityKeyTxContext serializes a check-then-insert for one
// logical profile-identity key. PostgreSQL row locks cannot lock an absent
// row, and its ordinary unique indexes treat NULL values as distinct. A
// transaction-scoped advisory lock closes that gap without changing the
// duplicate-tolerant API contract. SQLite has a single writer, so taking the
// existing identity-mutation write lock before the read provides the same
// ordering there.
func (s *Store) lockProfileIdentityKeyTxContext(
	ctx context.Context,
	tx *loggedTx,
	namespace string,
	parts ...any,
) error {
	{
		return s.lockIdentityMutationTxContext(ctx, tx)
	}

}
