package persondedup_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/persondedup"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
	"go.kenn.io/msgvault/internal/testutil/jevtest"
)

func participant(t *testing.T, st *store.Store, email, name string) int64 {
	t.Helper()
	id, err := st.EnsureParticipant(email, name, "")
	require.NoError(t, err)
	return id
}

// samePersonWhenBothSidesHaveNames says a pair is one person when both
// sides carry a display name, and probably not otherwise.
func samePersonWhenBothSidesHaveNames(questionID string, _ map[string]any, state map[string]any) map[string]any {
	pairs, _ := state["pairs"].(map[string]any)
	pair, _ := pairs["pair_"+questionID[strings.LastIndexByte(questionID, '_')+1:]].(map[string]any)
	first, _ := pair["first"].(map[string]any)
	second, _ := pair["second"].(map[string]any)
	firstNames, _ := first["names"].([]any)
	secondNames, _ := second["names"].([]any)
	if len(firstNames) > 0 && len(secondNames) > 0 {
		return jevtest.Noul(0.86)
	}
	return jevtest.Noul(0.12)
}

func enabled(cfg *jev.Config) { cfg.PersonDuplicates = jev.FeatureConfig{Enabled: true} }

func TestRunProposesLikelyDuplicatesForReviewOnly(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("gmail", "owner@example.com")
	require.NoError(err)
	participant(t, st, "owner@example.com", "Avery Owner")
	participant(t, st, "avery@example.org", "Avery Owner")
	require.NoError(st.AddAccountIdentityContext(t.Context(), source.ID, "owner@example.com", "manual"))
	jane := participant(t, st, "jane@example.com", "Jane Doe")
	janeWork := participant(t, st, "jdoe@example.org", "Doe, Jane")
	participant(t, st, "robin.example@example.com", "")
	participant(t, st, "robin.example@example.net", "")

	server := jevtest.NewServer(t, samePersonWhenBothSidesHaveNames)
	service, cfg := server.Service(t, st, enabled)
	jevtest.GrantConsent(t, st, cfg, persondedup.Feature())

	report, err := persondedup.Run(t.Context(), st, persondedup.Options{Judge: service})
	require.NoError(err)
	assert.Equal(persondedup.Report{Proposals: 2, Requests: 1, Judged: 2, Candidates: 1}, report)

	requests := server.Requests()
	require.Len(requests, 1)
	raw := fmt.Sprint(requests[0])
	assert.NotContains(raw, "owner@example.com", "the owner's identities are never sent")
	assert.NotContains(raw, "Avery Owner")
	state, ok := requests[0]["state"].(map[string]any)
	require.True(ok)
	pairs, ok := state["pairs"].(map[string]any)
	require.True(ok)
	require.Len(pairs, 2)
	first, ok := pairs["pair_1"].(map[string]any)
	require.True(ok)
	assert.Equal([]any{"same_display_name"}, first["signals"])
	left, ok := first["first"].(map[string]any)
	require.True(ok)
	assert.Equal([]any{"Jane Doe"}, left["names"])
	assert.Equal([]any{"jane@example.com"}, left["addresses"])
	questions, ok := requests[0]["questions"].(map[string]any)
	require.True(ok)
	assert.Len(questions, 2, "only the questions for the sent pairs")

	candidates, err := st.ListPersonDuplicateCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	require.Len(candidates, 1)
	assert.Equal(jane, candidates[0].LeftID)
	assert.Equal(janeWork, candidates[0].RightID)
	assert.Equal(store.IdentityMatchStateCandidate, candidates[0].State)
	require.NotNil(candidates[0].Confidence)
	assert.InDelta(0.86, *candidates[0].Confidence, 1e-9)

	again, err := persondedup.Run(t.Context(), st, persondedup.Options{Judge: service})
	require.NoError(err)
	assert.Equal(persondedup.Report{}, again, "judged pairs are not asked again")
	assert.Len(server.Requests(), 1)
}

func TestRunBatchesTwentyPairsPerRequest(t *testing.T) {
	require := require.New(t)
	st := testutil.NewTestStore(t)
	for i := range persondedup.PairsPerRequest + 1 {
		name := fmt.Sprintf("Person%c Example", 'A'+rune(i))
		participant(t, st, fmt.Sprintf("p%d@example.com", i), name)
		participant(t, st, fmt.Sprintf("p%d@example.org", i), name)
	}
	server := jevtest.NewServer(t, samePersonWhenBothSidesHaveNames)
	service, cfg := server.Service(t, st, enabled)
	jevtest.GrantConsent(t, st, cfg, persondedup.Feature())

	report, err := persondedup.Run(t.Context(), st, persondedup.Options{Judge: service})
	require.NoError(err)
	assert.Equal(t, 2, report.Requests)
	assert.Equal(t, persondedup.PairsPerRequest+1, report.Candidates)
	requests := server.Requests()
	require.Len(requests, 2)
	firstQuestions, _ := requests[0]["questions"].(map[string]any)
	assert.Len(t, firstQuestions, persondedup.PairsPerRequest)
}

func TestRunSendsNothingWithoutConsentOrAutomaticUse(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	participant(t, st, "jane@example.com", "Jane Doe")
	participant(t, st, "jdoe@example.org", "Jane Doe")
	server := jevtest.NewServer(t, samePersonWhenBothSidesHaveNames)
	service, cfg := server.Service(t, st, enabled)

	report, err := persondedup.Run(t.Context(), st, persondedup.Options{Judge: service})
	require.NoError(err)
	assert.Equal("consent_required", report.Skipped)

	jevtest.GrantConsent(t, st, cfg, persondedup.Feature())
	report, err = persondedup.Run(t.Context(), st, persondedup.Options{Judge: service, Automatic: true})
	require.NoError(err)
	assert.Equal("manual_only", report.Skipped)
	assert.Empty(server.Requests())

	report, err = persondedup.Run(t.Context(), st, persondedup.Options{})
	require.NoError(err)
	assert.Equal(persondedup.Report{Proposals: 1}, report, "without Jev only the proposals are counted")
	candidates, err := st.ListPersonDuplicateCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	assert.Empty(candidates)
}
