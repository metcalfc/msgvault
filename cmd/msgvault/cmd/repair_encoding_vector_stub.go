//go:build !sqlite_vec

package cmd

import (
	"context"

	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/vector"
)

// openVectorBackendForRepair is a no-op for builds without sqlite_vec:
// there is no embeddings store to open or upgrade backfill to run. The real
// implementation lives in repair_encoding_vector.go. repair-encoding still
// resets embed_gen on the main DB, harmless when vector search is unavailable.
func openVectorBackendForRepair(_ context.Context, _ *store.Store, _ *config.Config) (vector.Backend, func() error, error) {
	return nil, nil, nil
}

// lowerEmbedWatermarkForRepair is a no-op for builds without sqlite_vec:
// there is no embeddings store or watermark to lower. The real implementation
// lives in repair_encoding_vector.go.
func lowerEmbedWatermarkForRepair(_ context.Context, _ vector.Backend, _ int64) error {
	return nil
}
