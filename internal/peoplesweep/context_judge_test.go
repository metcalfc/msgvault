package peoplesweep

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/personfacts"
)

// scoringContextJudge scores an item by the first keyword its excerpt
// contains, per target key, and records every call.
type scoringContextJudge struct {
	scores map[string]map[string]float64
	err    error
	calls  []contextJudgeCall
}

type contextJudgeCall struct {
	target   string
	excerpts []string
}

func (j *scoringContextJudge) JudgeContext(
	_ context.Context, target personfacts.TargetDescriptor, items []EvidenceItem,
) ([]float64, error) {
	call := contextJudgeCall{target: target.Key}
	for _, item := range items {
		call.excerpts = append(call.excerpts, item.Excerpt)
	}
	j.calls = append(j.calls, call)
	if j.err != nil {
		return nil, j.err
	}
	scores := make([]float64, len(items))
	for i, item := range items {
		for keyword, score := range j.scores[target.Key] {
			if strings.Contains(item.Excerpt, keyword) {
				scores[i] = score
			}
		}
	}
	return scores, nil
}

func judgedAssembly(t *testing.T, judge ContextJudge, maxItems, maxBatches int, context ...EvidenceItem) Assembly {
	t.Helper()
	packet := packetTestPacket()
	seed := packet.Seeds[1]
	source := &assemblySourceStub{windows: map[GenerationCursorMode]PersonWindow{
		GenerationCursorOptimistic: {
			Seeds: []EvidenceItem{seed}, NextSequence: 12,
			Changes: []ArchiveChange{{Sequence: 12, PersonID: 7, SourceLane: SourceConversationText}},
		},
	}}
	ids := make([]int64, len(context))
	for i, item := range context {
		ids[i] = item.Ref.MessageID
	}
	archive := &assemblyContextArchive{candidates: ids, items: context}
	assembler := Assembler{
		Source: source, Context: NewContextRetriever(archive),
		MaxBytes: 16_384, MaxItems: maxItems, MaxBatches: maxBatches, ContextPerTarget: 8,
		Judge: judge,
	}
	result, err := assembler.Build(t.Context(), AssemblyRequest{
		PersonID: 7, Cursors: []Cursor{{
			Key: CursorKey{PersonID: 7, SourceLane: SourceConversationText,
				ProgramFingerprint: ProgramFingerprint(), CatalogFingerprint: packet.Catalog.Fingerprint},
			OptimisticSequence: 10, ReconciliationComplete: true,
			LastBackstopAt: new(time.Date(2026, 8, 23, 8, 0, 0, 0, time.UTC)),
		}}, Catalog: packet.Catalog,
		Profile: ProviderProfile{AllowedSources: []SourceClass{SourceConversationText},
			SourceSince: "2020-01-01", AllowSensitive: true},
		Now: time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC), BackstopInterval: 24 * time.Hour,
	})
	require.NoError(t, err)
	return result
}

func contextExcerpts(items []EvidenceItem) []string {
	excerpts := make([]string, 0, len(items))
	for _, item := range items {
		excerpts = append(excerpts, item.Excerpt)
	}
	return excerpts
}

func TestAssemblerDropsContextJudgedIrrelevantToEveryTarget(t *testing.T) {
	checks := assert.New(t)
	judge := &scoringContextJudge{scores: map[string]map[string]float64{
		"target:food": {"ramen": 0.9, "weather": 0.1, "promotion": 0.05},
		"target:role": {"ramen": 0.1, "weather": 0.1, "promotion": 0.8},
	}}
	result := judgedAssembly(t, judge, 10, 0,
		packetTestEvidence(91, SourceConversationText, "context about ramen"),
		packetTestEvidence(92, SourceConversationText, "context about the weather"),
		packetTestEvidence(93, SourceConversationText, "context about a promotion"),
	)

	checks.ElementsMatch([]string{"context about ramen", "context about a promotion"},
		contextExcerpts(result.Packet.Context), "an item one target keeps stays")
	require.Len(t, judge.calls, 2, "one judgment per target")
	for _, call := range judge.calls {
		checks.NotContains(call.excerpts, "seed ten", "seeds are never judged")
		checks.Len(call.excerpts, 3)
	}
}

func TestAssemblerKeepsEveryContextItemWhenTheJudgmentFails(t *testing.T) {
	judge := &scoringContextJudge{err: errors.New("jev skipped")}
	result := judgedAssembly(t, judge, 10, 0,
		packetTestEvidence(91, SourceConversationText, "context about ramen"),
		packetTestEvidence(92, SourceConversationText, "context about the weather"),
	)
	assert.ElementsMatch(t, []string{"context about ramen", "context about the weather"},
		contextExcerpts(result.Packet.Context))
}

func TestAssemblerShrinksAPacketByDroppingTheLeastRelevantContext(t *testing.T) {
	checks := assert.New(t)
	items := []EvidenceItem{
		packetTestEvidence(91, SourceConversationText, "context about ramen"),
		packetTestEvidence(92, SourceConversationText, "context about noodles"),
	}
	unjudged := judgedAssembly(t, nil, 2, 1, items...)
	checks.Equal([]string{"context about ramen"}, contextExcerpts(unjudged.Packet.Context),
		"without a judge the packet shrinks from the end of the canonical order")

	judge := &scoringContextJudge{scores: map[string]map[string]float64{
		"target:food": {"ramen": 0.3, "noodles": 0.95},
		"target:role": {"ramen": 0.3, "noodles": 0.4},
	}}
	judged := judgedAssembly(t, judge, 2, 1, items...)
	require.Len(t, judged.Batches, 1)
	checks.Equal([]string{"context about noodles"}, contextExcerpts(judged.Packet.Context),
		"the least relevant item leaves first")
}

func TestMemoContextJudgeAsksOncePerTargetAndItemSet(t *testing.T) {
	judge := &scoringContextJudge{scores: map[string]map[string]float64{"target:food": {"ramen": 0.9}}}
	memo := newMemoContextJudge(judge)
	target := packetTestTarget("target:food", "favorite food")
	items := []EvidenceItem{packetTestEvidence(91, SourceConversationText, "context about ramen")}
	for range 3 {
		scores, err := memo.JudgeContext(t.Context(), target, items)
		require.NoError(t, err)
		assert.Equal(t, []float64{0.9}, scores)
	}
	assert.Len(t, judge.calls, 1)
	assert.Nil(t, newMemoContextJudge(nil))
}
