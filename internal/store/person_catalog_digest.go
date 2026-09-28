package store

import (
	"context"
	"fmt"
)

// PersonCatalogDigest summarizes the persons table so a caller can tell
// whether any person was added, removed, renamed, or had projected input
// change since a previous digest, without loading the rows.
//
// Adds and deletes move Count or MaxID; merges delete the absorbed row;
// display-name edits advance Revision; every projected input change advances
// VCardProjectionRevision. A collision would need one deletion to be balanced
// by exactly offsetting revision bumps and an equal count, which no single
// edit produces.
type PersonCatalogDigest struct {
	Count                 int64
	MaxID                 int64
	RevisionSum           int64
	ProjectionRevisionSum int64
}

// PersonCatalogDigestContext reads the digest in one statement.
func (s *Store) PersonCatalogDigestContext(ctx context.Context) (PersonCatalogDigest, error) {
	var digest PersonCatalogDigest
	err := s.withTxContext(ctx, func(tx *loggedTx) error {
		row := tx.QueryRowContext(ctx, `
			SELECT COUNT(*),
			       COALESCE(MAX(id), 0),
			       COALESCE(SUM(revision), 0),
			       COALESCE(SUM(vcard_projection_revision), 0)
			FROM persons`)
		if err := row.Scan(&digest.Count, &digest.MaxID, &digest.RevisionSum, &digest.ProjectionRevisionSum); err != nil {
			return fmt.Errorf("read person catalog digest: %w", err)
		}
		return nil
	})
	return digest, err
}
