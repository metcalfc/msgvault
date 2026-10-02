package cmd

import (
	"context"
	"sync"
	"sync/atomic"

	"go.kenn.io/msgvault/internal/peoplesweep"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/sweepjudge"
	"go.kenn.io/msgvault/internal/vector"
)

// sweepSimilarityFromFeatures exposes the message vector index to the people
// sweep's context relevance: the backend's chunk vectors and the hybrid
// engine's query embedder, under the configured generation fingerprint.
func sweepSimilarityFromFeatures(vf *vectorFeatures) (sweepjudge.Similarity, bool) {
	if vf == nil || !vf.Cfg.Enabled || vf.HybridEngine == nil || vf.Backend == nil {
		return sweepjudge.Similarity{}, false
	}
	backend, ok := vf.Backend.(vector.ChunkScoringBackend)
	if !ok {
		return sweepjudge.Similarity{}, false
	}
	return sweepjudge.Similarity{
		Backend: backend, Embedder: vf.HybridEngine, Fingerprint: vf.Cfg.GenerationFingerprint(),
		Model: vf.Cfg.Embeddings.Model,
	}, true
}

// newSweepContextJudge scores retrieved context by embedding similarity, or
// returns nil without a vector index so the sweep keeps every item.
func newSweepContextJudge(source sweepjudge.SimilaritySource) peoplesweep.ContextJudge {
	if source == nil {
		return nil
	}
	return sweepjudge.NewContextScorer(source, nil)
}

// daemonSweepSimilarity reads the daemon's vector features, which a
// background goroutine initializes after the sweep job is registered. Until
// that finishes, or when it fails, there is no index and nothing is scored.
type daemonSweepSimilarity struct {
	handle atomic.Pointer[vectorInitHandle]
}

func (d *daemonSweepSimilarity) attach(handle *vectorInitHandle) {
	if d != nil {
		d.handle.Store(handle)
	}
}

func (d *daemonSweepSimilarity) source() (sweepjudge.Similarity, bool) {
	if d == nil {
		return sweepjudge.Similarity{}, false
	}
	handle := d.handle.Load()
	if handle == nil {
		return sweepjudge.Similarity{}, false
	}
	handle.mu.Lock()
	vf := handle.vf
	handle.mu.Unlock()
	return sweepSimilarityFromFeatures(vf)
}

// lazySweepSimilarity opens the vector index for one CLI sweep run the
// first time the sweep asks about context, and closes it after the run.
type lazySweepSimilarity struct {
	ctx      context.Context // the command context that owns this run
	st       *store.Store
	mainPath string

	once sync.Once
	vf   *vectorFeatures
}

func (l *lazySweepSimilarity) source() (sweepjudge.Similarity, bool) {
	l.once.Do(func() {
		vf, err := setupVectorFeatures(l.ctx, l.st, l.mainPath, true)
		if err != nil {
			loggerFromContext(l.ctx).Warn("people sweep context relevance: vector index unavailable; keeping all context",
				"error", err)
			return
		}
		l.vf = vf
	})
	return sweepSimilarityFromFeatures(l.vf)
}

func (l *lazySweepSimilarity) close() {
	if l.vf != nil && l.vf.Close != nil {
		_ = l.vf.Close()
	}
}

// closingPersonSweepRunner releases the run's vector index after the run.
type closingPersonSweepRunner struct {
	personSweepRunner

	release func()
}

func (r closingPersonSweepRunner) Run(
	ctx context.Context, request peoplesweep.RunRequest,
) (peoplesweep.RunResult, error) {
	defer r.release()
	return r.personSweepRunner.Run(ctx, request)
}
