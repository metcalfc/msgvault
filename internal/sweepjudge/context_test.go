//go:build sqlite_vec

package sweepjudge_test

import (
	"bytes"
	"context"
	"log/slog"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/peoplesweep"
	"go.kenn.io/msgvault/internal/personfacts"
	"go.kenn.io/msgvault/internal/sweepjudge"
	"go.kenn.io/msgvault/internal/testutil"
	"go.kenn.io/msgvault/internal/testutil/jevtest"
	"go.kenn.io/msgvault/internal/vector"
	"go.kenn.io/msgvault/internal/vector/rerank"
	"go.kenn.io/msgvault/internal/vector/sqlitevec"
)

const (
	fakeModel       = "fake-embed"
	fakeDimension   = 4
	fakeFingerprint = "fake-embed:4"
)

func employmentTarget() personfacts.TargetDescriptor {
	return personfacts.TargetDescriptor{
		Kind: personfacts.TargetEmployment, Key: "employment", Slug: "employment",
		Description: "Current and historical employment, including organization and title",
	}
}

func contextItem(id int64, lane peoplesweep.SourceClass, excerpt string) peoplesweep.EvidenceItem {
	personID := int64(7)
	return peoplesweep.EvidenceItem{
		Ref:      peoplesweep.EvidenceRef{SourceLane: lane, SourceID: 1, MessageID: id},
		PersonID: personID, SubjectPersonID: &personID, SourceClass: lane,
		EventTime: time.Date(2026, 3, int(id), 9, 0, 0, 0, time.UTC), Excerpt: excerpt,
	}
}

// fakeEmbedder is a deterministic provider: each text maps to a unit vector
// over three topic axes and a small shared bias. It records every input it
// is asked to embed.
type fakeEmbedder struct {
	mu     sync.Mutex
	inputs []string
	scale  float64
}

func (f *fakeEmbedder) vector(text string) []float32 {
	text = strings.ToLower(text)
	raw := []float64{0, 0, 0, 0.1}
	for _, word := range []string{"employment", "joined", "title"} {
		if strings.Contains(text, word) {
			raw[0]++
		}
	}
	for _, word := range []string{"lunch", "ramen"} {
		if strings.Contains(text, word) {
			raw[1]++
		}
	}
	if strings.Contains(text, "weather") {
		raw[2]++
	}
	var sum float64
	for _, value := range raw {
		sum += value * value
	}
	norm := math.Sqrt(sum)
	scale := f.scale
	if scale == 0 {
		scale = 1
	}
	out := make([]float32, len(raw))
	for i, value := range raw {
		out[i] = float32(scale * value / norm)
	}
	return out
}

func (f *fakeEmbedder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	f.mu.Lock()
	f.inputs = append(f.inputs, text)
	f.mu.Unlock()
	return f.vector(text), nil
}

func (f *fakeEmbedder) Inputs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.inputs...)
}

// indexedBackend opens a real vector backend whose active generation holds
// each message's text as the indexing worker would embed it with the same
// provider.
func indexedBackend(t *testing.T, embedder *fakeEmbedder, messages map[int64]string) *sqlitevec.Backend {
	t.Helper()
	st := testutil.NewTestStore(t)
	backend, err := sqlitevec.Open(t.Context(), sqlitevec.Options{
		Path: filepath.Join(t.TempDir(), "vectors.db"), Dimension: fakeDimension, MainDB: st.DB(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = backend.Close() })
	gen, err := backend.CreateGeneration(t.Context(), fakeModel, fakeDimension, fakeFingerprint)
	require.NoError(t, err)
	chunks := make([]vector.Chunk, 0, len(messages))
	for id, text := range messages {
		chunks = append(chunks, vector.Chunk{
			MessageID: id, Vector: embedder.vector(text),
			SourceCharLen: len(text), ChunkCharEnd: len(text),
		})
	}
	require.NoError(t, backend.Upsert(t.Context(), gen, chunks))
	require.NoError(t, backend.ActivateGeneration(t.Context(), gen, true))
	return backend
}

func sourceFor(backend vector.ChunkScoringBackend, embedder sweepjudge.QueryEmbedder) sweepjudge.SimilaritySource {
	return func() (sweepjudge.Similarity, bool) {
		return sweepjudge.Similarity{Backend: backend, Embedder: embedder, Fingerprint: fakeFingerprint}, true
	}
}

func TestContextScorerRanksExcerptsByEmbeddingSimilarityToTheTarget(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	embedder := &fakeEmbedder{}
	backend := indexedBackend(t, embedder, map[int64]string{
		1: "I joined Example Labs as VP Product.",
		2: "Lunch on Friday? The ramen place again.",
		3: "Did you see the weather? I joined the hiking club.",
	})
	scorer := sweepjudge.NewContextScorer(sourceFor(backend, embedder), nil)
	items := []peoplesweep.EvidenceItem{
		contextItem(1, peoplesweep.SourceConversationText, "I joined Example Labs as VP Product."),
		contextItem(2, peoplesweep.SourceConversationText, "Lunch on Friday? The ramen place again."),
		contextItem(3, peoplesweep.SourceConversationText, "Did you see the weather? I joined the hiking club."),
		contextItem(4, peoplesweep.SourceConversationText, "A message the index has not embedded."),
		contextItem(1, peoplesweep.SourceDocumentText, "An attached offer letter: joined as VP Product."),
	}

	scores, err := scorer.JudgeContext(t.Context(), employmentTarget(), items)
	require.NoError(err)
	require.Len(scores, len(items))
	assert.Greater(scores[0], scores[2], "the job message ranks above the partly related one")
	assert.Greater(scores[2], scores[1], "the partly related message ranks above the unrelated one")
	assert.GreaterOrEqual(scores[0], peoplesweep.ContextRelevanceFloor)
	assert.Less(scores[1], peoplesweep.ContextRelevanceFloor, "the unrelated message is left out")
	assert.InDelta(peoplesweep.ContextNotJudged, scores[3], 0, "an unindexed message is kept unjudged")
	assert.InDelta(peoplesweep.ContextNotJudged, scores[4], 0, "document text is not scored by its message")

	_, err = scorer.JudgeContext(t.Context(), employmentTarget(), items[:2])
	require.NoError(err)
	assert.Equal([]string{employmentTarget().Description}, embedder.Inputs(),
		"only the target description is embedded, once; no excerpt reaches the provider")
}

func TestContextScorerKeepsEverythingWithoutAUsableIndex(t *testing.T) {
	assert := assert.New(t)
	items := []peoplesweep.EvidenceItem{
		contextItem(1, peoplesweep.SourceConversationText, "I joined Example Labs as VP Product."),
	}
	embedder := &fakeEmbedder{}
	backend := indexedBackend(t, embedder, map[int64]string{1: "I joined Example Labs as VP Product."})

	cases := map[string]sweepjudge.SimilaritySource{
		"no vector index": nil,
		"index not initialized": func() (sweepjudge.Similarity, bool) {
			return sweepjudge.Similarity{}, false
		},
		"index built by another model": func() (sweepjudge.Similarity, bool) {
			return sweepjudge.Similarity{Backend: backend, Embedder: embedder, Fingerprint: "other-model:4"}, true
		},
		"model without unit-length vectors": sourceFor(backend, &fakeEmbedder{scale: 3}),
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			scores, err := sweepjudge.NewContextScorer(source, nil).JudgeContext(t.Context(), employmentTarget(), items)
			require.Error(t, err, "an error keeps every retrieved item")
			assert.Nil(scores)
		})
	}

	sensitive := employmentTarget()
	sensitive.Sensitive = true
	before := len(embedder.Inputs())
	_, err := sweepjudge.NewContextScorer(sourceFor(backend, embedder), nil).JudgeContext(t.Context(), sensitive, items)
	require.Error(t, err)
	assert.Len(embedder.Inputs(), before, "a sensitive target's description is never embedded")
}

func TestContextScorerWarnsOnceWhenTheModelDoesNotNormalize(t *testing.T) {
	assert := assert.New(t)
	stored := &fakeEmbedder{}
	backend := indexedBackend(t, stored, map[int64]string{1: "I joined Example Labs as VP Product."})
	unnormalized := &fakeEmbedder{scale: 3}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	source := func() (sweepjudge.Similarity, bool) {
		return sweepjudge.Similarity{
			Backend: backend, Embedder: unnormalized, Fingerprint: fakeFingerprint, Model: fakeModel,
		}, true
	}
	scorer := sweepjudge.NewContextScorer(source, logger)
	items := []peoplesweep.EvidenceItem{
		contextItem(1, peoplesweep.SourceConversationText, "I joined Example Labs as VP Product."),
	}
	other := employmentTarget()
	other.Key, other.Description = "location", "Current city of residence"

	for _, target := range []personfacts.TargetDescriptor{employmentTarget(), other, employmentTarget()} {
		scores, err := scorer.JudgeContext(t.Context(), target, items)
		require.Error(t, err, "every target keeps all of its context")
		assert.Nil(scores)
	}
	assert.Equal(1, strings.Count(logs.String(), "level=WARN"), logs.String())
	assert.Contains(logs.String(), "model="+fakeModel)
	assert.Contains(logs.String(), "keeping all retrieved context")
	assert.Len(unnormalized.Inputs(), 1, "the run stops embedding once the model is known not to normalize")
}

// TestRetiredEvidenceRerankFeatureCannotReachJev covers an archive that
// consented to sweep_evidence_rerank before it was retired: the gate no
// longer knows the feature, so nothing is sent even with the old grant.
func TestRetiredEvidenceRerankFeatureCannotReachJev(t *testing.T) {
	st := testutil.NewTestStore(t)
	server := jevtest.NewServer(t, func(string, map[string]any, map[string]any) map[string]any {
		return jevtest.Noul(0.9)
	})
	service, cfg := server.Service(t, st, nil)
	retired := jev.FeatureSpec{
		Name: "sweep_evidence_rerank", Title: "People sweep evidence relevance",
		Purpose:     "Retired: relevance now uses local embeddings.",
		Questions:   rerank.BatchedQuestions(),
		StateFields: []string{"query", "candidates[]"},
	}
	jevtest.GrantConsent(t, st, cfg, retired)

	_, err := service.JudgeQuestions(t.Context(), retired, false,
		map[string]any{"query": "employment", "candidates": []string{"I joined Example Labs."}},
		[]string{rerank.BatchedQuestionID(0)}, time.Time{})
	require.ErrorIs(t, err, jev.ErrUnknownFeature)
	assert.Empty(t, server.Requests())
}
