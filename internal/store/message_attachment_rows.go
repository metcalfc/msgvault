package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"go.kenn.io/msgvault/internal/attachmentpolicy"
)

// AttachmentPathsUniqueToSource returns local content and thumbnail paths for
// blobs referenced by sourceID and by no other source. Sharing is checked
// across both hash columns: a content blob used as another source's thumbnail
// (or vice versa) is preserved. Call this before RemoveSource so the cascade
// has not run yet.
func (s *Store) AttachmentPathsUniqueToSource(sourceID int64) ([]string, error) {
	rows, err := s.db.Query(`
		WITH source_blob_paths(blob_hash, blob_path) AS (
		    SELECT a.content_hash, a.storage_path
		    FROM attachments a
		    JOIN messages m ON m.id = a.message_id
		    WHERE m.source_id = ?
		      AND a.content_hash IS NOT NULL AND a.content_hash != ''
		    UNION
		    SELECT a.thumbnail_hash, a.thumbnail_path
		    FROM attachments a
		    JOIN messages m ON m.id = a.message_id
		    WHERE m.source_id = ?
		      AND a.thumbnail_hash IS NOT NULL AND a.thumbnail_hash != ''
		)
		SELECT DISTINCT sb.blob_path
		FROM source_blob_paths sb
		WHERE sb.blob_path IS NOT NULL
		  AND sb.blob_path != ''
		  AND sb.blob_path NOT LIKE 'http://%'
		  AND sb.blob_path NOT LIKE 'https://%'
		  AND NOT EXISTS (
		      SELECT 1 FROM attachments a2
		      JOIN messages m2 ON m2.id = a2.message_id
		      WHERE m2.source_id != ?
		        AND (a2.content_hash = sb.blob_hash OR a2.thumbnail_hash = sb.blob_hash)
		  )
	`, sourceID, sourceID, sourceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// IsAttachmentPathReferenced returns true if any attachment record still
// points to the given content or thumbnail path. Use this immediately before
// deleting a file to guard against a concurrent sync that added a new
// reference after the candidate list was collected.
func (s *Store) IsAttachmentPathReferenced(storagePath string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM attachments WHERE storage_path = ? OR thumbnail_path = ?`,
		storagePath, storagePath,
	).Scan(&count)
	if err != nil {
		return true, err // fail safe: treat error as referenced
	}
	return count > 0, nil
}

// UpsertAttachment is the compatibility write path for callers without stable
// occurrence provenance. It fails closed to role unknown/legacy_api and keeps
// the legacy best-effort content-hash identity. New and source-aware writers
// use UpsertAttachmentRecord. `size` is widened to int64 at the bind boundary
// so 32-bit builds cannot truncate large attachments before the SQLite INTEGER
// column receives the value.
//
// When contentHash is empty (the rare untyped-blob path used by some
// importers), the unique index does not cover the row; a best-effort
// (message_id, empty-hash) match is used to avoid trivial duplicates,
// but two concurrent empty-hash inserts on the same message may both
// succeed.
func (s *Store) UpsertAttachment(messageID int64, filename, mimeType, storagePath, contentHash string, size int) error {
	return s.UpsertAttachmentRecord(context.Background(), messageID, AttachmentWrite{
		Filename:    filename,
		MIMEType:    mimeType,
		StoragePath: storagePath,
		ContentHash: contentHash,
		Size:        int64(size),
		Role:        AttachmentRoleUnknown,
		RoleSource:  AttachmentRoleSourceLegacyAPI,
	})
}

// RecomputeMessageAttachmentStats refreshes the denormalized attachment flags
// on one message from its current attachment rows.
func (s *Store) RecomputeMessageAttachmentStats(messageID int64) error {
	write := func(q querier) error {
		if err := s.requireSyncMessageSourceTx(q, messageID); err != nil {
			return err
		}
		return recomputeMessageAttachmentStatsWith(q, messageID)
	}
	if s.syncGeneration == nil {
		return write(s.db)
	}
	return s.withTx(func(tx *loggedTx) error { return write(tx) })
}

func recomputeMessageAttachmentStatsWith(q querier, messageID int64) error {
	_, err := q.Exec(`
		UPDATE messages
		SET has_attachments = (SELECT COUNT(*) FROM attachments WHERE message_id = ?) > 0,
		    attachment_count = (SELECT COUNT(*) FROM attachments WHERE message_id = ?)
		WHERE id = ?
	`, messageID, messageID, messageID)
	return err
}

type AttachmentRef struct {
	Filename           string
	MimeType           string
	StoragePath        string
	ContentHash        string
	Size               int
	SourceAttachmentID string
	// Optional media metadata; zero values are stored as NULL.
	MediaType  string
	Width      int64
	Height     int64
	DurationMS int64
	// Metadata is importer-supplied JSON stored in attachments.attachment_metadata
	// (e.g. the source URL a link-preview attachment was forwarded from). Empty
	// stores NULL; callers own the shape and must supply valid JSON.
	Metadata      string
	Role          AttachmentRole
	RoleSource    AttachmentRoleSource
	SourcePartKey string
	ContentID     string
	// State and SkipReason distinguish unfinished fetches from deliberate policy exclusions.
	State      attachmentpolicy.DownloadState
	SkipReason attachmentpolicy.SkipReason
}

// replaceMessageAttachmentsWhere atomically deletes a message's attachment
// rows matching deleteWhere and inserts refs. Refs with an empty StoragePath
// (and, when requireHash is set, an empty ContentHash) are skipped.
func (s *Store) replaceMessageAttachmentsWhere(
	messageID int64, deleteWhere string, requireHash bool, refs []AttachmentRef, deleteArgs ...any,
) error {
	return s.withTx(func(tx *loggedTx) error {
		return s.replaceMessageAttachmentsWhereTx(tx, messageID, deleteWhere, requireHash, refs, deleteArgs...)
	})
}

func (s *Store) replaceMessageAttachmentsWhereTx(
	tx *loggedTx, messageID int64, deleteWhere string, requireHash bool,
	refs []AttachmentRef, deleteArgs ...any,
) error {
	args := append([]any{messageID}, deleteArgs...)
	if _, err := tx.Exec(`DELETE FROM attachments WHERE message_id = ? AND (`+deleteWhere+`)`, args...); err != nil {
		return err
	}
	for _, ref := range refs {
		if ref.StoragePath == "" || (requireHash && ref.ContentHash == "") {
			continue
		}
		write := AttachmentWrite{
			Filename:           ref.Filename,
			MIMEType:           ref.MimeType,
			StoragePath:        ref.StoragePath,
			ContentHash:        ref.ContentHash,
			Size:               int64(ref.Size),
			SourceAttachmentID: ref.SourceAttachmentID,
			MediaType:          ref.MediaType,
			Width:              ref.Width,
			Height:             ref.Height,
			DurationMS:         ref.DurationMS,
			Metadata:           ref.Metadata,
			Role:               ref.Role,
			RoleSource:         ref.RoleSource,
			SourcePartKey:      ref.SourcePartKey,
			ContentID:          ref.ContentID,
			State:              ref.State,
			SkipReason:         ref.SkipReason,
		}.normalized()
		if write.SourcePartKey == "" && write.SourceAttachmentID != "" {
			// Provider attachment IDs are already namespaced by every caller of
			// this replacement path. They are the stable occurrence identity;
			// unlike a content hash, they preserve two source parts with the
			// same bytes and keep hashless pending rows distinct.
			write.SourcePartKey = write.SourceAttachmentID
		}
		if err := write.validate(); err != nil {
			return err
		}
		if err := s.upsertAttachmentRecord(tx, messageID, write); err != nil {
			return err
		}
	}
	return nil
}

func nullIfEmpty(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func nullIfZero(n int64) sql.NullInt64 {
	return sql.NullInt64{Int64: n, Valid: n != 0}
}

// ReplaceMessageInlineAttachments replaces Teams-managed inline media rows for
// a message. When preserveLegacy is false, it also removes legacy unmarked
// Teams inline rows produced before stable source attachment IDs were added.
// Callers preserve those rows while a current hosted-content replacement is
// excluded or unfinished, so an ordinary resync cannot detach archived media.
// URL-backed reference/recording attachments are always left untouched.
// The size estimate includes the stored body and downloaded attachment bytes.
func (s *Store) ReplaceMessageInlineAttachments(messageID int64, refs []AttachmentRef, preserveLegacy bool) error {
	deleteWhere := `source_attachment_id LIKE 'teams:inline:%'`
	if !preserveLegacy {
		deleteWhere += ` OR (
		  (source_attachment_id IS NULL OR source_attachment_id = '')
		  AND storage_path != ''
		  AND storage_path NOT LIKE 'http://%'
		  AND storage_path NOT LIKE 'https://%'
		  AND content_hash IS NOT NULL
		  AND content_hash != ''
		  AND COALESCE(filename, '') = ''
		  AND COALESCE(mime_type, '') = ''
		)`
	}
	return s.withTx(func(tx *loggedTx) error {
		if err := s.replaceMessageAttachmentsWhereTx(tx, messageID, deleteWhere, false, refs); err != nil {
			return err
		}
		var body sql.NullString
		err := tx.QueryRow(`SELECT body_text FROM message_bodies WHERE message_id = ?`, messageID).Scan(&body)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read Teams message body for size estimate: %w", err)
		}
		_, err = tx.Exec(`
			UPDATE messages SET size_estimate = ? + (
				SELECT COALESCE(SUM(size), 0) FROM attachments
				WHERE message_id = ? AND content_hash != ''
			) WHERE id = ?
		`, len(body.String), messageID, messageID)
		if err != nil {
			return fmt.Errorf("update Teams message size estimate: %w", err)
		}
		return nil
	})
}

// MessageTeamsInlineAttachments returns Teams-managed inline media rows keyed
// by their stable hosted-content source identifier.
func (s *Store) MessageTeamsInlineAttachments(messageID int64) (map[string]AttachmentRef, error) {
	return s.messageProviderAttachments(messageID, "teams:inline:")
}

// ReplaceMessageBeeperAttachments replaces Beeper-managed attachment rows for
// a message (rows whose source_attachment_id carries the "beeper:" prefix).
// Rows with a content hash are downloaded media; rows without one are
// pending-download markers whose storage_path holds the source asset URL, so
// a later retry pass can find and repair them.
func (s *Store) ReplaceMessageBeeperAttachments(messageID int64, refs []AttachmentRef) error {
	return s.withTx(func(tx *loggedTx) error {
		metadataChanged, err := s.beeperAttachmentMetadataChangedTx(tx, messageID, refs)
		if err != nil {
			return err
		}
		if err := s.replaceMessageAttachmentsWhereTx(tx, messageID, `source_attachment_id LIKE ?`, false, refs, "beeper:%"); err != nil {
			return err
		}
		if metadataChanged {
			if err := s.bumpDerivedDataRevision(tx); err != nil {
				return fmt.Errorf("advance Beeper attachment cache revision: %w", err)
			}
		}
		return nil
	})
}

func (s *Store) beeperAttachmentMetadataChangedTx(
	tx *loggedTx, messageID int64, refs []AttachmentRef,
) (bool, error) {
	incoming := make(map[string]string, len(refs))
	for _, ref := range refs {
		if ref.StoragePath == "" || !strings.HasPrefix(ref.SourceAttachmentID, "beeper:") {
			continue
		}
		incoming[ref.SourceAttachmentID] = ref.Metadata
	}
	rows, err := tx.Query(`
		SELECT source_attachment_id
		FROM attachments
		WHERE message_id = ? AND source_attachment_id LIKE 'beeper:%'`, messageID)
	if err != nil {
		return false, fmt.Errorf("list existing Beeper attachment metadata: %w", err)
	}
	var existingIDs []string
	for rows.Next() {
		var sourceAttachmentID string
		if err := rows.Scan(&sourceAttachmentID); err != nil {
			_ = rows.Close()
			return false, fmt.Errorf("scan existing Beeper attachment metadata: %w", err)
		}
		existingIDs = append(existingIDs, sourceAttachmentID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, fmt.Errorf("iterate existing Beeper attachment metadata: %w", err)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close existing Beeper attachment metadata: %w", err)
	}
	if len(existingIDs) != len(incoming) {
		return true, nil
	}
	for _, sourceAttachmentID := range existingIDs {
		metadata, ok := incoming[sourceAttachmentID]
		if !ok {
			return true, nil
		}
		var same int
		if err := tx.QueryRow(fmt.Sprintf(`
			SELECT COUNT(*) FROM attachments
			WHERE message_id = ? AND source_attachment_id = ?
			  AND NOT (%s)`, s.dialect.JSONIsDistinctExpr("attachment_metadata")),
			messageID, sourceAttachmentID, nullIfEmpty(metadata)).Scan(&same); err != nil {
			return false, fmt.Errorf("compare existing Beeper attachment metadata: %w", err)
		}
		if same == 0 {
			return true, nil
		}
	}
	return false, nil
}

// MessageBeeperAttachments returns the message's existing Beeper-managed
// attachment rows keyed by source_attachment_id, so re-persisting a message
// can keep already-downloaded media without re-fetching it.
func (s *Store) MessageBeeperAttachments(messageID int64) (map[string]AttachmentRef, error) {
	return s.messageProviderAttachments(messageID, "beeper:")
}
