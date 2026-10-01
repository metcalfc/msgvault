//go:build sqlite_vec

package cmd

import (
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/vector"
	"go.kenn.io/msgvault/internal/vector/hybrid"
	"go.kenn.io/msgvault/internal/vector/rerank"
)

// jevRerankStore is what the search rerank stage needs from the archive: the
// Jev service's consent and counters plus the batched message lookup.
type jevRerankStore interface {
	jevRuntimeStore
	rerank.MessageLoader
}

// newJevSearchReranker wires the hybrid search rerank stage, or returns nil
// when Jev or [jev.rerank] is off in the startup configuration so hybrid
// search keeps its fused order. Consent and the credential are rechecked on
// every request; the stage only runs for searches that ask for it, which
// automatic callers never do.
func newJevSearchReranker(cfg *config.Config, st jevRerankStore, vectorCfg vector.Config) (hybrid.Reranker, error) {
	if cfg == nil || !cfg.Jev.Enabled || !cfg.Jev.Rerank.Enabled {
		return nil, nil //nolint:nilnil // nil means "no rerank stage".
	}
	service, err := newJevService(cfg, st)
	if err != nil || service == nil {
		return nil, err
	}
	shape, err := rerank.ShapeFromConfig(cfg.Jev.Rerank.Shape)
	if err != nil {
		return nil, err
	}
	scorer, err := rerank.NewServiceScorer(service, shape)
	if err != nil {
		return nil, err
	}
	rerankCfg := cfg.Jev.Rerank
	return rerank.NewStage(rerank.StageOptions{
		Scorer: scorer, Loader: st, Shape: shape, Model: cfg.Jev.Model, Top: rerankCfg.Top,
		Excludes: rerankCfg.Excludes, Preprocess: embeddingPreprocessConfig(vectorCfg),
		Timeout: cfg.Jev.RequestTimeout,
	})
}
