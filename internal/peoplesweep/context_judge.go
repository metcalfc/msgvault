package peoplesweep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"go.kenn.io/msgvault/internal/personfacts"
)

// ContextRelevanceFloor is the lowest relevance a judged context item may
// have and still reach the extraction model. A judged item below it for every
// target that retrieved it is left out of the packet.
const ContextRelevanceFloor = 0.20

// ContextJudge scores retrieved context for one catalog target before the
// packet is assembled: how likely each item is to bear on the target. The
// scores align with items and lie in [0, 1]. Any error means "not judged":
// the assembler keeps every item exactly as it would without a judge. It
// never sees seeds, which the cursor must cover regardless of relevance.
type ContextJudge interface {
	JudgeContext(ctx context.Context, target personfacts.TargetDescriptor, items []EvidenceItem) ([]float64, error)
}

// memoContextJudge asks its judge at most once per target and item set, so
// the several assemblies one person attempt may build (for example while
// narrowing to a bounded batch count) reuse one judgment.
type memoContextJudge struct {
	judge ContextJudge
	mu    sync.Mutex
	seen  map[string]memoContextResult
}

type memoContextResult struct {
	scores []float64
	err    error
}

func newMemoContextJudge(judge ContextJudge) ContextJudge {
	if judge == nil {
		return nil
	}
	return &memoContextJudge{judge: judge, seen: make(map[string]memoContextResult)}
}

func (m *memoContextJudge) JudgeContext(
	ctx context.Context, target personfacts.TargetDescriptor, items []EvidenceItem,
) ([]float64, error) {
	hash := sha256.New()
	_, _ = hash.Write([]byte(target.Key + "\x00" + target.Revision))
	for _, item := range items {
		_, _ = hash.Write([]byte("\x00" + packetEvidenceID(item)))
	}
	key := hex.EncodeToString(hash.Sum(nil))
	m.mu.Lock()
	result, ok := m.seen[key]
	m.mu.Unlock()
	if ok {
		return append([]float64(nil), result.scores...), result.err
	}
	scores, err := m.judge.JudgeContext(ctx, target, items)
	m.mu.Lock()
	m.seen[key] = memoContextResult{scores: append([]float64(nil), scores...), err: err}
	m.mu.Unlock()
	return scores, err
}

// ClaimGrounder rescores a generation's extracted claims before they are
// applied, replacing the chat model's self-reported confidence with an
// independent judgment of whether the cited evidence states each claim and
// whether it is still current. It returns claims in the same order with
// only Confidence changed; a claim it cannot judge keeps its reported
// score, and any failure returns the claims unchanged. It never adds,
// drops, or reorders claims.
type ClaimGrounder interface {
	GroundClaims(ctx context.Context, personID int64, claims []personfacts.ProposedClaim) []personfacts.ProposedClaim
}
