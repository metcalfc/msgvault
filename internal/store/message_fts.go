package store

import (
	"context"
	"fmt"
)

// UpsertFTS inserts or updates the FTS index for a message.
// No-op if FTS is not available.
func (s *Store) UpsertFTS(messageID int64, subject, bodyText, fromAddr, toAddrs, ccAddrs string) error {
	return s.UpsertFTSContext(context.Background(), messageID, subject, bodyText, fromAddr, toAddrs, ccAddrs)
}

// UpsertFTSContext is the request-aware form of UpsertFTS.
func (s *Store) UpsertFTSContext(ctx context.Context, messageID int64, subject, bodyText, fromAddr, toAddrs, ccAddrs string) error {
	if !s.fts5Available {
		return nil
	}
	doc := FTSDoc{
		MessageID: messageID,
		Subject:   subject,
		Body:      bodyText,
		FromAddr:  fromAddr,
		ToAddrs:   toAddrs,
		CcAddrs:   ccAddrs,
	}
	if s.syncGeneration != nil {
		return s.withTxContext(ctx, func(tx *loggedTx) error {
			q := boundQuerier{ctx: ctx, q: tx}
			if err := s.requireSyncMessageSourceTx(q, messageID); err != nil {
				return err
			}
			return s.dialect.FTSUpsert(q, doc)
		})
	}
	return s.dialect.FTSUpsert(boundQuerier{ctx: ctx, q: s.db}, doc)
}

// BackfillFTS populates the FTS table from existing message data.
// Processes in batches to avoid blocking for minutes on large archives.
// The progress callback (if non-nil) is called after each batch with
// (position in ID range, total ID range). Each batch is committed
// independently so partial progress is preserved if interrupted.
// Returns the number of rows inserted. No-op if FTS5 is not available.
//
// BackfillFTS clears FTS rows with DELETE before inserting. If the FTS5
// shadow tables are themselves malformed, that DELETE will either fail or
// leave corruption in place — callers recovering from shadow-table
// corruption should use RebuildFTS instead.
func (s *Store) BackfillFTS(progress func(done, total int64)) (int64, error) {
	return s.BackfillFTSContext(context.Background(), progress)
}

// BackfillFTSContext is the request-aware form of BackfillFTS.
func (s *Store) BackfillFTSContext(
	ctx context.Context,
	progress func(done, total int64),
) (int64, error) {
	if !s.fts5Available {
		return 0, nil
	}

	minID, maxID, err := s.messageIDRangeContext(ctx)
	if err != nil {
		return 0, err
	}
	if maxID == 0 {
		return 0, nil
	}

	// Clear the FTS table inside a maintenance transaction before backfilling.
	if err := s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
		_, err := tx.ExecContext(ctx, s.dialect.FTSClearSQL())
		return err
	}); err != nil {
		return 0, fmt.Errorf("clear FTS: %w", err)
	}

	return s.backfillFTSRangeContext(ctx, minID, maxID, progress)
}

// RebuildFTS fully recreates the FTS index from the underlying message
// tables. Unlike BackfillFTS (DELETE + INSERT), this drops and recreates
// the FTS table itself so malformed FTS5 shadow tables are fully replaced.
//
// Ignores the cached fts5Available flag: a corrupt shadow table causes the
// availability probe to fail, which is precisely the symptom this method
// exists to recover from. On successful completion, fts5Available is set to
// true. Returns an error if the binary was built without FTS5 support.
func (s *Store) RebuildFTS(progress func(done, total int64)) (int64, error) {
	return s.RebuildFTSContext(context.Background(), progress)
}

// RebuildFTSContext is the request-aware form of RebuildFTS.
func (s *Store) RebuildFTSContext(
	ctx context.Context,
	progress func(done, total int64),
) (int64, error) {
	// Drop and recreate messages_fts in one maintenance transaction.
	if err := s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
		return s.dialect.FTSRebuildSchema(ctx, tx)
	}); err != nil {
		return 0, err
	}

	minID, maxID, err := s.messageIDRangeContext(ctx)
	if err != nil {
		return 0, err
	}
	if maxID == 0 {
		s.fts5Available = true
		return 0, nil
	}

	indexed, err := s.backfillFTSRangeContext(ctx, minID, maxID, progress)
	if err != nil {
		return indexed, err
	}
	s.fts5Available = true
	return indexed, nil
}

// messageIDRangeContext returns (minID, maxID) using MIN/MAX B-tree lookups
// rather than COUNT(*), which would scan the whole table.
func (s *Store) messageIDRangeContext(ctx context.Context) (int64, int64, error) {
	var minID, maxID int64
	err := s.db.QueryRowContext(ctx,
		"SELECT COALESCE(MIN(id),0), COALESCE(MAX(id),0) FROM messages",
	).Scan(&minID, &maxID)
	if err != nil {
		return 0, 0, fmt.Errorf("get message ID range: %w", err)
	}
	return minID, maxID, nil
}

// backfillFTSRange inserts FTS rows for all messages with id in [minID, maxID],
// in batches. Shared between BackfillFTS (DELETE+fill) and RebuildFTS
// (DROP+CREATE+fill). Each batch is committed independently so partial
// progress is preserved if interrupted.
func (s *Store) backfillFTSRangeContext(
	ctx context.Context,
	minID, maxID int64,
	progress func(done, total int64),
) (int64, error) {
	const batchSize = 5000
	idRange := maxID - minID + 1
	var indexed int64
	cursor := minID

	for cursor <= maxID {
		batchEnd := cursor + batchSize
		n, err := s.backfillFTSBatchContext(ctx, cursor, batchEnd)
		if err != nil {
			return indexed, err
		}
		indexed += n
		cursor = batchEnd

		if progress != nil {
			pos := min(cursor-minID, idRange)
			progress(pos, idRange)
		}
	}
	return indexed, nil
}

// backfillFTSBatchContext inserts FTS rows for [fromID, toID) in its own
// transaction so an interrupted backfill keeps its previously committed batches.
func (s *Store) backfillFTSBatchContext(
	ctx context.Context,
	fromID, toID int64,
) (int64, error) {
	var affected int64
	err := s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
		result, err := tx.ExecContext(ctx, s.dialect.FTSBackfillBatchSQL(), fromID, toID)
		if err != nil {
			return err
		}
		affected, err = result.RowsAffected()
		return err
	})
	return affected, err
}
