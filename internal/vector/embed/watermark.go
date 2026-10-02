package embed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"go.kenn.io/msgvault/internal/vector"
)

// Watermark reads and writes per-generation forward-scan resume points in
// vectors.db's embed_watermark table alongside generation metadata. The worker
// seeds its scan from GetWatermark at run start and advances it after each
// successful batch via SetWatermark.
//
// The watermark is a pure optimization. Losing it restarts the next scan
// from ID 0; the embed_gen predicate and idempotent Upsert prevent duplicate
// work from changing results. The full-scan backstop ignores it entirely.
type Watermark struct {
	db *sql.DB
}

// NewWatermark returns a Watermark bound to the generation database.
// The caller retains ownership of db.
func NewWatermark(db *sql.DB) *Watermark {
	return &Watermark{db: db}
}

// GetWatermark returns the stored watermark for gen, or 0 when no row
// exists yet (which makes the next scan start from the beginning — safe by
// design). A nil db (watermark disabled) returns 0 without error.
func (w *Watermark) GetWatermark(ctx context.Context, gen vector.GenerationID) (int64, error) {
	if w == nil || w.db == nil {
		return 0, nil
	}
	var id int64
	err := w.db.QueryRowContext(ctx,
		`SELECT watermark_id FROM embed_watermark WHERE generation_id = ?`,
		int64(gen)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get watermark: %w", err)
	}
	return id, nil
}

// SetWatermark upserts the watermark for gen to id. A nil db (watermark
// disabled) is a no-op. Advancing the watermark is non-critical — a
// failure here is logged by the worker, not fatal — so callers may treat
// the error as best-effort.
func (w *Watermark) SetWatermark(ctx context.Context, gen vector.GenerationID, id int64) error {
	if w == nil || w.db == nil {
		return nil
	}
	stmt := `INSERT INTO embed_watermark (generation_id, watermark_id) VALUES (?, ?)
	         ON CONFLICT (generation_id) DO UPDATE SET watermark_id = excluded.watermark_id`
	if _, err := w.db.ExecContext(ctx, stmt, int64(gen), id); err != nil {
		return fmt.Errorf("set watermark: %w", err)
	}
	return nil
}
