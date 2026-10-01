//go:build !sqlite_vec

// Package sqlitevec is a stub when the sqlite_vec build tag is not set.
// The real implementation in ext.go wires up the sqlite-vec extension.
package sqlitevec

import (
	"context"
	"database/sql"
	"errors"

	"go.kenn.io/msgvault/internal/vector"
)

// ErrNotBuilt is returned when sqlite-vec features are used in a build
// that did not set the `sqlite_vec` build tag.
var ErrNotBuilt = errors.New(
	"sqlite-vec support not compiled in; rebuild with `go build -tags \"fts5 sqlite_vec\"`")

// RegisterExtension reports that sqlite-vec is unavailable in this build.
func RegisterExtension() error { return ErrNotBuilt }

// DriverName returns the default sqlite3 driver name since sqlite-vec
// is not compiled in.
func DriverName() string { return "sqlite3" }

// Available reports whether this build includes sqlite-vec support.
func Available() bool { return false }

// Options mirrors the configuration type for builds without sqlite_vec
// so callers compile while Open reports ErrNotBuilt.
type Options struct {
	Path            string
	MainPath        string
	Dimension       int
	MainDB          *sql.DB
	BuildScope      vector.BuildScope
	ReadOnly        bool
	ANNWorkCeiling  int
	ANNOversample   int
	ANNNProbe       int
	AcceleratorMode string
}

// Backend implements vector.Backend for builds without sqlite_vec.
// Its operations report ErrNotBuilt where the extension is required.
type Backend struct{}

// Compile-time assertion: stub Backend must satisfy vector.Backend.
var _ vector.Backend = (*Backend)(nil)

// Open always returns ErrNotBuilt in builds without sqlite_vec.
func Open(_ context.Context, _ Options) (*Backend, error) {
	return nil, ErrNotBuilt
}

// DB returns nil because this stub has no database handle.
func (b *Backend) DB() *sql.DB { return nil }

// Close is a no-op stub.
func (b *Backend) Close() error { return nil }

// CreateGeneration is a stub that always returns ErrNotBuilt.
func (b *Backend) CreateGeneration(_ context.Context, _ string, _ int, _ string) (vector.GenerationID, error) {
	return 0, ErrNotBuilt
}

// ActivateGeneration is a stub that always returns ErrNotBuilt.
func (b *Backend) ActivateGeneration(_ context.Context, _ vector.GenerationID, _ bool) error {
	return ErrNotBuilt
}

// RetireGeneration is a stub that always returns ErrNotBuilt.
func (b *Backend) RetireGeneration(_ context.Context, _ vector.GenerationID, _ bool) error {
	return ErrNotBuilt
}

// ActiveGeneration is a stub that always returns ErrNotBuilt.
func (b *Backend) ActiveGeneration(_ context.Context) (vector.Generation, error) {
	return vector.Generation{}, ErrNotBuilt
}

// BuildingGeneration is a stub that always returns ErrNotBuilt.
func (b *Backend) BuildingGeneration(_ context.Context) (*vector.Generation, error) {
	return nil, ErrNotBuilt
}

// Upsert is a stub that always returns ErrNotBuilt.
func (b *Backend) Upsert(_ context.Context, _ vector.GenerationID, _ []vector.Chunk) error {
	return ErrNotBuilt
}

// Search is a stub that always returns ErrNotBuilt.
func (b *Backend) Search(_ context.Context, _ vector.GenerationID, _ []float32, _ int, _ vector.Filter) ([]vector.Hit, error) {
	return nil, ErrNotBuilt
}

// SearchWithMetadata is a stub that always returns ErrNotBuilt.
func (b *Backend) SearchWithMetadata(_ context.Context, _ vector.GenerationID, _ []float32, _ int, _ vector.Filter) ([]vector.Hit, vector.SearchMetadata, error) {
	return nil, vector.SearchMetadata{}, ErrNotBuilt
}

// Delete is a stub that always returns ErrNotBuilt.
func (b *Backend) Delete(_ context.Context, _ vector.GenerationID, _ []int64) error {
	return ErrNotBuilt
}

// Stats is a stub that always returns ErrNotBuilt.
func (b *Backend) Stats(_ context.Context, _ vector.GenerationID) (vector.Stats, error) {
	return vector.Stats{}, ErrNotBuilt
}

// LoadVector is a stub that always returns ErrNotBuilt.
func (b *Backend) LoadVector(_ context.Context, _ int64) ([]float32, error) {
	return nil, ErrNotBuilt
}

// ResetWatermarkBelow is a stub that always returns ErrNotBuilt.
func (b *Backend) ResetWatermarkBelow(_ context.Context, _ int64) error {
	return ErrNotBuilt
}

// EmbeddedMessageCount is a stub that always returns ErrNotBuilt.
func (b *Backend) EmbeddedMessageCount(_ context.Context, _ vector.GenerationID) (int64, error) {
	return 0, ErrNotBuilt
}

// ScoreMessageChunks is a stub that always returns ErrNotBuilt.
func (b *Backend) ScoreMessageChunks(_ context.Context, _ vector.GenerationID, _ int64, _ []float32) ([]vector.ChunkHit, error) {
	return nil, ErrNotBuilt
}
