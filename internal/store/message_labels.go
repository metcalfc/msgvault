package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// EnsureLabel gets or creates a label, handling renames and ID changes.
// For batch operations prefer EnsureLabelsBatch which runs in a single
// transaction.
func (s *Store) EnsureLabel(
	sourceID int64,
	sourceLabelID, name, labelType string,
) (int64, error) {
	var id int64
	err := s.withTx(func(tx *loggedTx) error {
		var txErr error
		id, txErr = ensureLabelWith(
			tx, sourceID, sourceLabelID, name, labelType, nil,
		)
		return txErr
	})
	return id, err
}

// ensureLabelWith is the core label-upsert logic, parameterised on the
// database handle so it works both standalone and inside a transaction.
// The handle is expected to be *loggedDB or *loggedTx so placeholder
// rebinding is applied automatically.
//
// Labels are identified by source_label_id (Gmail label ID) but have a
// UNIQUE constraint on (source_id, name). This function handles:
//   - Existing label found by source_label_id: updates name if renamed
//   - Name conflict with different source_label_id: upserts, adopting
//     the new source_label_id (handles deleted+recreated labels, imports)
func ensureLabelWith(
	q querier,
	sourceID int64,
	sourceLabelID, name, labelType string,
	systemRole *string,
) (int64, error) {
	// Look up by canonical identifier (Gmail label ID).
	var id int64
	var existingName string
	var existingType sql.NullString
	var existingRole sql.NullString
	err := q.QueryRow(`
		SELECT id, name, label_type, system_role FROM labels
		WHERE source_id = ? AND source_label_id = ?
	`, sourceID, sourceLabelID).Scan(&id, &existingName, &existingType, &existingRole)

	if err == nil {
		if existingName == name {
			if !existingType.Valid || existingType.String != labelType ||
				(systemRole != nil && !labelSystemRoleMatches(existingRole, *systemRole)) {
				if systemRole != nil {
					if _, err = q.Exec(`
						UPDATE labels SET label_type = ?, system_role = ?
						WHERE id = ?
					`, labelType, labelSystemRoleValue(*systemRole), id); err != nil {
						return 0, fmt.Errorf("update label type and role: %w", err)
					}
					return id, nil
				}
				if _, err = q.Exec(`
					UPDATE labels SET label_type = ?
					WHERE id = ?
				`, labelType, id); err != nil {
					return 0, fmt.Errorf("update label type: %w", err)
				}
			}
			return id, nil
		}
		// Label was renamed — update the name. If another row already
		// claims the target name, merge it: move its message-label
		// associations to the canonical row and delete the stale one.
		if err = mergeLabelByName(q, sourceID, name, id); err != nil {
			return 0, err
		}
		if systemRole != nil {
			if _, err = q.Exec(`
				UPDATE labels SET name = ?, label_type = ?, system_role = ?
				WHERE id = ?
			`, name, labelType, labelSystemRoleValue(*systemRole), id); err != nil {
				return 0, fmt.Errorf("update label name and role: %w", err)
			}
		} else if _, err = q.Exec(`
			UPDATE labels SET name = ?, label_type = ?
			WHERE id = ?
		`, name, labelType, id); err != nil {
			return 0, fmt.Errorf("update label name: %w", err)
		}
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	// Not found by source_label_id — upsert by name. Handles the case
	// where a label with this name exists from a previous import or
	// with a stale/NULL source_label_id.
	if systemRole != nil {
		if _, err = q.Exec(`
			INSERT INTO labels (source_id, source_label_id, name, label_type, system_role)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(source_id, name) DO UPDATE SET
				source_label_id = excluded.source_label_id,
				label_type = excluded.label_type,
				system_role = excluded.system_role
		`, sourceID, sourceLabelID, name, labelType, labelSystemRoleValue(*systemRole)); err != nil {
			return 0, err
		}
	} else if _, err = q.Exec(`
		INSERT INTO labels (source_id, source_label_id, name, label_type)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(source_id, name) DO UPDATE SET
			source_label_id = excluded.source_label_id,
			label_type = excluded.label_type
	`, sourceID, sourceLabelID, name, labelType); err != nil {
		return 0, err
	}

	err = q.QueryRow(`
		SELECT id FROM labels WHERE source_id = ? AND name = ?
	`, sourceID, name).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func labelSystemRoleValue(systemRole string) any {
	if systemRole == "" {
		return nil
	}
	return systemRole
}

func labelSystemRoleMatches(existing sql.NullString, systemRole string) bool {
	if systemRole == "" {
		return !existing.Valid
	}
	return existing.Valid && existing.String == systemRole
}

// mergeLabelByName finds a label with the given name (excluding keepID)
// and merges it into keepID: message-label associations are reassigned
// and the stale row is deleted. No-op if no conflicting label exists.
func mergeLabelByName(
	q querier, sourceID int64, name string, keepID int64,
) error {
	var conflictID int64
	err := q.QueryRow(`
		SELECT id FROM labels
		WHERE source_id = ? AND name = ? AND id != ?
	`, sourceID, name, keepID).Scan(&conflictID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find conflicting label: %w", err)
	}
	// Drop associations that would conflict after reassignment because the
	// message is already linked to keepID. The remaining rows can then move
	// without violating the association key.
	if _, err = q.Exec(`
		DELETE FROM message_labels
		WHERE label_id = ?
		AND message_id IN (
			SELECT message_id FROM message_labels WHERE label_id = ?
		)
	`, conflictID, keepID); err != nil {
		return fmt.Errorf("drop conflicting associations: %w", err)
	}
	// Reassign the remaining associations (no PK violations possible now).
	if _, err = q.Exec(`
		UPDATE message_labels SET label_id = ? WHERE label_id = ?
	`, keepID, conflictID); err != nil {
		return fmt.Errorf("reassign label associations: %w", err)
	}
	if _, err = q.Exec(`
		DELETE FROM labels WHERE id = ?
	`, conflictID); err != nil {
		return fmt.Errorf("delete conflicting label: %w", err)
	}
	return nil
}

// LabelInfo holds the name and type for a label to be ensured.
type LabelInfo struct {
	Name       string
	Type       string // "system" or "user"
	SystemRole string // trusted canonical role; empty clears any stale value
}

// IsSystemLabel returns true if the given Gmail label ID represents a system label.
func IsSystemLabel(sourceLabelID string) bool {
	switch sourceLabelID {
	case "INBOX", "SENT", "TRASH", "SPAM", "DRAFT", "UNREAD", "STARRED", "IMPORTANT":
		return true
	}
	return strings.HasPrefix(sourceLabelID, "CATEGORY_")
}

// EnsureLabelsBatch ensures all labels exist and returns a map of
// source_label_id -> internal ID. Runs in a single transaction with
// a two-phase rename to handle cross-renames safely (e.g. L1:Foo→Bar
// and L2:Bar→Foo in the same batch).
func (s *Store) EnsureLabelsBatch(
	sourceID int64, labels map[string]LabelInfo,
) (map[string]int64, error) {
	var result map[string]int64
	err := s.withTx(func(tx *loggedTx) error {
		var err error
		result, err = ensureLabelsBatchWith(tx, sourceID, labels)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func ensureLabelsBatchWith(
	q querier, sourceID int64, labels map[string]LabelInfo,
) (map[string]int64, error) {
	result := make(map[string]int64, len(labels))
	// Phase 1: Move all renamed labels to temporary names so that cross-renames
	// do not merge labels that are both present in this snapshot.
	for sourceLabelID, info := range labels {
		var id int64
		var curName string
		err := q.QueryRow(`
			SELECT id, name FROM labels
			WHERE source_id = ? AND source_label_id = ?
		`, sourceID, sourceLabelID).Scan(&id, &curName)
		if errors.Is(err, sql.ErrNoRows) || curName == info.Name {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("check label %s: %w", sourceLabelID, err)
		}
		tempName := fmt.Sprintf("\x01__msgvault_pending_rename__%d", id)
		if _, err = q.Exec(`UPDATE labels SET name = ? WHERE id = ?`, tempName, id); err != nil {
			return nil, fmt.Errorf("clear name for label %s: %w", sourceLabelID, err)
		}
	}

	// Phase 2: Apply final descriptors. Any remaining name conflict belongs to
	// a label outside this snapshot and is safe for ensureLabelWith to merge.
	for sourceLabelID, info := range labels {
		id, err := ensureLabelWith(
			q, sourceID, sourceLabelID, info.Name, info.Type, &info.SystemRole,
		)
		if err != nil {
			return nil, err
		}
		result[sourceLabelID] = id
	}
	return result, nil
}

func ensureMessageLabelRefsWith(
	q querier, sourceID int64, refs []MessageLabelRef,
) ([]int64, error) {
	labels := make(map[string]LabelInfo, len(refs))
	for _, ref := range refs {
		if ref.SourceLabelID == "" {
			return nil, errors.New("message label reference requires a source label ID")
		}
		labels[ref.SourceLabelID] = ref.Info
	}
	resolved, err := ensureLabelsBatchWith(q, sourceID, labels)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(refs))
	seen := make(map[int64]struct{}, len(refs))
	for _, ref := range refs {
		id := resolved[ref.SourceLabelID]
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

// MessageLabelIDsContext returns the labels currently assigned to a message.
func (s *Store) MessageLabelIDsContext(ctx context.Context, messageID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT label_id FROM message_labels WHERE message_id = ?`, messageID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ReplaceMessageLabels replaces all labels for a message atomically.
func (s *Store) ReplaceMessageLabels(messageID int64, labelIDs []int64) error {
	return s.withTx(func(tx *loggedTx) error {
		return replaceMessageLabelsTx(tx, messageID, labelIDs)
	})
}

// ReconcileMessageLabels replaces or merges labels and reports whether the
// persisted label set changed.
func (s *Store) ReconcileMessageLabels(
	messageID int64, labelIDs []int64, replace bool,
) (bool, error) {
	var changed bool
	err := s.withTx(func(tx *loggedTx) error {
		var err error
		changed, err = s.reconcileMessageLabelsTx(
			tx, messageID, labelIDs, replace)
		return err
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

func (s *Store) reconcileMessageLabelsTx(
	tx *loggedTx, messageID int64, labelIDs []int64, replace bool,
) (bool, error) {
	return s.reconcileMessageLabelsTxContext(
		context.Background(), tx, messageID, labelIDs, replace,
	)
}

// reconcileMessageLabelsTxContext is the context-aware form of
// reconcileMessageLabelsTx: every statement carries ctx, so a cancelled caller
// interrupts the read and the write in flight rather than only between calls.
// ReconcileMessageLabels and AddMessageLabels have no ctx to give, so they keep
// reaching it through the background-context wrapper above.
func (s *Store) reconcileMessageLabelsTxContext(
	ctx context.Context, tx *loggedTx, messageID int64, labelIDs []int64, replace bool,
) (bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT label_id FROM message_labels WHERE message_id = ?
	`, messageID)
	if err != nil {
		return false, err
	}

	existing := make(map[int64]struct{})
	for rows.Next() {
		var labelID int64
		if err := rows.Scan(&labelID); err != nil {
			_ = rows.Close()
			return false, err
		}
		existing[labelID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}

	desired := make(map[int64]struct{}, len(labelIDs))
	for _, labelID := range labelIDs {
		desired[labelID] = struct{}{}
	}

	if replace {
		changed := len(existing) != len(desired)
		if !changed {
			for labelID := range desired {
				if _, ok := existing[labelID]; !ok {
					changed = true
					break
				}
			}
		}
		if !changed {
			return false, nil
		}
		if err := replaceMessageLabelsTx(
			boundQuerier{ctx: ctx, q: tx}, messageID, labelIDs,
		); err != nil {
			return false, err
		}
		return true, nil
	}

	missing := make([]int64, 0, len(desired))
	for labelID := range desired {
		if _, ok := existing[labelID]; !ok {
			missing = append(missing, labelID)
		}
	}
	if len(missing) == 0 {
		return false, nil
	}
	if err := s.addMessageLabelsTx(boundQuerier{ctx: ctx, q: tx}, messageID, missing); err != nil {
		return false, err
	}
	return true, nil
}

func replaceMessageLabelsTx(tx querier, messageID int64, labelIDs []int64) error {
	_, err := tx.Exec(`
		DELETE FROM message_labels WHERE message_id = ?
	`, messageID)
	if err != nil {
		return err
	}

	if len(labelIDs) == 0 {
		return nil
	}

	return insertInChunks(tx, chunkInsert{
		totalRows:    len(labelIDs),
		valuesPerRow: 2,
		prefix:       "INSERT INTO message_labels (message_id, label_id) VALUES ",
	}, func(start, end int) ([]string, []any) {
		values := make([]string, end-start)
		args := make([]any, 0, (end-start)*2)
		for i := start; i < end; i++ {
			values[i-start] = "(?, ?)"
			args = append(args, messageID, labelIDs[i])
		}
		return values, args
	})
}

// AddMessageLabels adds labels to a message without removing existing ones.
// Uses INSERT OR IGNORE to skip labels that already exist.
func (s *Store) AddMessageLabels(messageID int64, labelIDs []int64) error {
	if len(labelIDs) == 0 {
		return nil
	}
	return s.withTx(func(tx *loggedTx) error {
		changed, err := s.reconcileMessageLabelsTx(
			tx, messageID, labelIDs, false,
		)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		return s.bumpDerivedDataRevision(tx, true)
	})
}

func (s *Store) addMessageLabelsTx(
	tx querier, messageID int64, labelIDs []int64,
) error {
	return insertInChunks(tx, chunkInsert{
		totalRows:    len(labelIDs),
		valuesPerRow: 2,
		prefix:       "INSERT OR IGNORE INTO message_labels (message_id, label_id) VALUES ",
	}, func(start, end int) ([]string, []any) {
		values := make([]string, end-start)
		args := make([]any, 0, (end-start)*2)
		for i := start; i < end; i++ {
			values[i-start] = "(?, ?)"
			args = append(args, messageID, labelIDs[i])
		}
		return values, args
	})
}

// LinkMessageLabel links a single label to a message.
// Uses INSERT OR IGNORE — safe to call multiple times.
func (s *Store) LinkMessageLabel(messageID, labelID int64) error {
	return s.AddMessageLabels(messageID, []int64{labelID})
}

// RemoveMessageLabels removes specific labels from a message.
func (s *Store) RemoveMessageLabels(messageID int64, labelIDs []int64) error {
	if len(labelIDs) == 0 {
		return nil
	}
	if s.syncGeneration != nil {
		return s.withTx(func(tx *loggedTx) error {
			if err := s.requireSyncMessageSourceTx(tx, messageID); err != nil {
				return err
			}
			return execInChunks(tx, labelIDs, []any{messageID},
				`DELETE FROM message_labels WHERE message_id = ? AND label_id IN (%s)`)
		})
	}
	return execInChunks(s.db, labelIDs, []any{messageID},
		`DELETE FROM message_labels WHERE message_id = ? AND label_id IN (%s)`)
}
