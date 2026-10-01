package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// sqliteTableRebuild describes one SQLite table rebuild that follows the
// documented procedure for an arbitrary schema change: on a dedicated pooled
// connection with foreign keys suspended, inside one transaction, validate
// the rows, run the rebuild statements, check that no reference dangles, and
// commit. Foreign keys must be off because DROP TABLE performs an implicit
// delete that would otherwise fire ON DELETE actions on referencing tables
// and destroy their rows.
type sqliteTableRebuild struct {
	// Table is inspected in sqlite_master; when its definition already
	// contains AppliedMarker the rebuild is skipped.
	Table         string
	AppliedMarker string
	// Label names the change in errors.
	Label string
	// Validate refuses to rebuild over rows the new shape would reject.
	Validate func(ctx context.Context, tx *loggedTx) error
	// Statements create the replacement table, copy the rows, drop the old
	// table, rename the replacement, and recreate indexes.
	Statements []string
	// CountViolations reports dangling references after the rebuild.
	CountViolations func(ctx context.Context, tx *sql.Tx) (int, error)
}

// rebuildSQLiteTable runs one rebuild. The foreign-key pragma is
// connection-scoped and cannot change inside a transaction, so this takes its
// own pooled connection rather than going through runMaintenance, and it
// restores enforcement before the connection returns to the pool whatever
// happens; failing to restore it is reported rather than swallowed.
func (s *Store) rebuildSQLiteTable(ctx context.Context, rebuild sqliteTableRebuild) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection to %s: %w", rebuild.Label, err)
	}
	defer func() { _ = conn.Close() }()

	var definition string
	if err := conn.QueryRowContext(ctx, `SELECT sql FROM sqlite_master
		WHERE type = 'table' AND name = ?`, rebuild.Table).Scan(&definition); err != nil {
		return fmt.Errorf("inspect %s: %w", rebuild.Label, err)
	}
	if strings.Contains(definition, rebuild.AppliedMarker) {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("suspend foreign keys to %s: %w", rebuild.Label, err)
	}
	defer func() {
		if _, restoreErr := conn.ExecContext(context.WithoutCancel(ctx),
			`PRAGMA foreign_keys = ON`); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf(
				"restore foreign keys after %s: %w", rebuild.Label, restoreErr))
		}
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin %s: %w", rebuild.Label, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if rebuild.Validate != nil {
		if err := rebuild.Validate(ctx, &loggedTx{Tx: tx, rebind: s.Rebind}); err != nil {
			return err
		}
	}
	for _, statement := range rebuild.Statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("%s: %w", rebuild.Label, err)
		}
	}
	if rebuild.CountViolations != nil {
		violations, err := rebuild.CountViolations(ctx, tx)
		if err != nil {
			return err
		}
		if violations != 0 {
			return fmt.Errorf("%s left %d dangling references", rebuild.Label, violations)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s: %w", rebuild.Label, err)
	}
	committed = true
	return nil
}

// countSQLiteTableForeignKeyViolations checks references from the rebuilt
// table and from every table whose foreign keys name it. It is scoped on
// purpose: an archive may already hold an unrelated dangling reference, and
// that must not block an upgrade that never touched it.
func countSQLiteTableForeignKeyViolations(ctx context.Context, tx *sql.Tx, table string) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT m.name FROM sqlite_master AS m
		WHERE m.type = 'table' AND m.name <> ?
		AND EXISTS (SELECT 1 FROM pragma_foreign_key_list(m.name) AS f WHERE f."table" = ?)`, table, table)
	if err != nil {
		return 0, fmt.Errorf("list tables referencing %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	var referencing []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return 0, fmt.Errorf("list tables referencing %s: %w", table, err)
		}
		referencing = append(referencing, name)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("list tables referencing %s: %w", table, err)
	}
	var violations int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check(?)`, table).
		Scan(&violations); err != nil {
		return 0, fmt.Errorf("check references from %s: %w", table, err)
	}
	for _, child := range referencing {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check(?)
			WHERE parent = ?`, child, table).Scan(&count); err != nil {
			return 0, fmt.Errorf("check references from %s to %s: %w", child, table, err)
		}
		violations += count
	}
	return violations, nil
}
