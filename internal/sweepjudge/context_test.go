package sweepjudge_test

import (
	"strings"
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
	"go.kenn.io/msgvault/internal/vector/rerank"
)

func employmentTarget() personfacts.TargetDescriptor {
	return personfacts.TargetDescriptor{
		Kind: personfacts.TargetEmployment, Key: "employment", Slug: "employment",
		Description: "Current and historical employment, including organization and title",
	}
}

func contextItem(id int64, excerpt string) peoplesweep.EvidenceItem {
	personID := int64(7)
	return peoplesweep.EvidenceItem{
		Ref:      peoplesweep.EvidenceRef{SourceLane: peoplesweep.SourceConversationText, SourceID: 1, MessageID: id},
		PersonID: personID, SubjectPersonID: &personID, SourceClass: peoplesweep.SourceConversationText,
		EventTime: time.Date(2026, 3, int(id), 9, 0, 0, 0, time.UTC), Excerpt: excerpt,
	}
}

// relevanceByKeyword answers each candidate by whether its text mentions a
// job.
func relevanceByKeyword(questionID string, _ map[string]any, state map[string]any) map[string]any {
	candidates, _ := state["candidates"].([]any)
	for i, candidate := range candidates {
		if rerank.BatchedQuestionID(i) != questionID {
			continue
		}
		if text, _ := candidate.(string); strings.Contains(text, "joined") {
			return jevtest.Noul(0.92)
		}
	}
	return jevtest.Noul(0.05)
}

func TestContextJudgeScoresExcerptsAgainstTheTargetDescription(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	server := jevtest.NewServer(t, relevanceByKeyword)
	service, cfg := server.Service(t, st, func(cfg *jev.Config) {
		cfg.SweepEvidenceRerank = jev.FeatureConfig{Enabled: true}
	})
	jevtest.GrantConsent(t, st, cfg, sweepjudge.EvidenceRerankFeature())
	judge := sweepjudge.NewContextJudge(service, false, nil)

	scores, err := judge.JudgeContext(t.Context(), employmentTarget(), []peoplesweep.EvidenceItem{
		contextItem(1, "I joined Example Labs as VP Product. Reach me at casey@example.com or +1 555 010 0199."),
		contextItem(2, "Lunch on Friday?"),
	})
	require.NoError(err)
	assert.Equal([]float64{0.92, 0.05}, scores)

	requests := server.Requests()
	require.Len(requests, 1)
	state, ok := requests[0]["state"].(map[string]any)
	require.True(ok)
	assert.Equal("Current and historical employment, including organization and title", state["query"])
	candidates, ok := state["candidates"].([]any)
	require.True(ok)
	require.Len(candidates, 2)
	first, _ := candidates[0].(string)
	assert.True(strings.HasPrefix(first, "Date: 2026-03-01\n\n"), first)
	assert.Contains(first, "I joined Example Labs as VP Product")
	assert.NotContains(first, "casey@example.com", "addresses never leave")
	assert.NotContains(first, "555 010 0199", "phone numbers never leave")
	assert.Len(state, 2, "only the query and candidates leave")
	questions, ok := requests[0]["questions"].(map[string]any)
	require.True(ok)
	assert.Len(questions, 2, "only the questions for sent candidates are asked")
}

func TestContextJudgeSendsNothingWithoutConsentOrForSensitiveTargets(t *testing.T) {
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	server := jevtest.NewServer(t, relevanceByKeyword)
	service, cfg := server.Service(t, st, func(cfg *jev.Config) {
		cfg.SweepEvidenceRerank = jev.FeatureConfig{Enabled: true}
	})
	items := []peoplesweep.EvidenceItem{contextItem(1, "I joined Example Labs.")}

	_, err := sweepjudge.NewContextJudge(service, false, nil).JudgeContext(t.Context(), employmentTarget(), items)
	assert.ErrorIs(err, jev.ErrConsentRequired)

	jevtest.GrantConsent(t, st, cfg, sweepjudge.EvidenceRerankFeature())
	_, err = sweepjudge.NewContextJudge(service, true, nil).JudgeContext(t.Context(), employmentTarget(), items)
	assert.ErrorIs(err, jev.ErrAutomaticDisabled, "a scheduled sweep needs automatic = true")

	sensitive := employmentTarget()
	sensitive.Sensitive = true
	_, err = sweepjudge.NewContextJudge(service, false, nil).JudgeContext(t.Context(), sensitive, items)
	assert.Error(err)
	assert.Empty(server.Requests())
}

func TestContextJudgeSplitsLongItemListsAcrossRequests(t *testing.T) {
	require := require.New(t)
	st := testutil.NewTestStore(t)
	server := jevtest.NewServer(t, relevanceByKeyword)
	service, cfg := server.Service(t, st, func(cfg *jev.Config) {
		cfg.SweepEvidenceRerank = jev.FeatureConfig{Enabled: true}
	})
	jevtest.GrantConsent(t, st, cfg, sweepjudge.EvidenceRerankFeature())
	items := make([]peoplesweep.EvidenceItem, sweepjudge.ContextCandidatesPerRequest+2)
	for i := range items {
		items[i] = contextItem(int64(i%27+1), "Lunch on Friday?")
	}
	items[len(items)-1].Excerpt = "I joined Example Labs."

	scores, err := sweepjudge.NewContextJudge(service, false, nil).JudgeContext(t.Context(), employmentTarget(), items)
	require.NoError(err)
	require.Len(scores, len(items))
	assert.InDelta(t, 0.92, scores[len(scores)-1], 1e-9)
	assert.Len(t, server.Requests(), 2)
}
