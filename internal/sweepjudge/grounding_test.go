package sweepjudge_test

import (
	"encoding/json/jsontext"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/personfacts"
	"go.kenn.io/msgvault/internal/sweepjudge"
	"go.kenn.io/msgvault/internal/testutil"
	"go.kenn.io/msgvault/internal/testutil/jevtest"
)

func groundingClaim(value string, reported int, excerpts ...string) personfacts.ProposedClaim {
	personID := int64(7)
	evidence := make([]personfacts.EvidenceInput, len(excerpts))
	for i, excerpt := range excerpts {
		evidence[i] = personfacts.EvidenceInput{
			PersonID: personID, SubjectPersonID: &personID, Excerpt: excerpt,
			EventTime: time.Date(2026, 2, i+1, 9, 0, 0, 0, time.UTC),
		}
	}
	return personfacts.ProposedClaim{
		Target: personfacts.TargetDescriptor{
			Kind: personfacts.TargetAttribute, Key: "job_title", Slug: "job_title", Description: "Job title",
		},
		Relation: personfacts.RelationSupport, SubmittedValue: jsontext.Value(value),
		Evidence: evidence, Origin: personfacts.OriginExtraction,
		Confidence: personfacts.ConfidenceInputs{ReportedScore: reported},
	}
}

// groundingByValue says a claim is stated when an excerpt contains its
// value, and current unless an excerpt says "used to".
func groundingByValue(questionID string, _ map[string]any, state map[string]any) map[string]any {
	claims, _ := state["claims"].(map[string]any)
	key := "claim_" + questionID[strings.LastIndexByte(questionID, '_')+1:]
	claim, _ := claims[key].(map[string]any)
	value, _ := claim["value"].(string)
	evidence, _ := claim["evidence"].([]any)
	stated, current := 0.1, 0.9
	for _, item := range evidence {
		text, _ := item.(map[string]any)["text"].(string)
		if value != "" && strings.Contains(text, value) {
			stated = 0.8
		}
		if strings.Contains(text, "used to") {
			current = 0.25
		}
	}
	if strings.HasPrefix(questionID, "stated_") {
		return jevtest.Noul(stated)
	}
	return jevtest.Noul(current)
}

func TestGrounderReplacesReportedConfidenceWithStatedTimesCurrent(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	server := jevtest.NewServer(t, groundingByValue)
	service, cfg := server.Service(t, st, func(cfg *jev.Config) {
		cfg.SweepClaimGrounding = jev.FeatureConfig{Enabled: true}
	})
	jevtest.GrantConsent(t, st, cfg, sweepjudge.ClaimGroundingFeature())
	sensitive := groundingClaim(`"Deacon"`, 700, "I am a Deacon at the chapel.")
	sensitive.Target.Sensitive = true
	claims := []personfacts.ProposedClaim{
		groundingClaim(`"VP Product"`, 950, "I just started as VP Product at Example Labs. Mail casey@example.com."),
		groundingClaim(`"Designer"`, 900, "I used to be a Designer.", "Weekend plans?"),
		groundingClaim(`"Engineer"`, 990, "Lunch?"),
		groundingClaim(`"casey@example.com"`, 800, "Write to casey@example.com"),
		sensitive,
	}

	grounded := sweepjudge.NewGrounder(service, false, nil).GroundClaims(t.Context(), 7, claims)
	require.Len(grounded, len(claims))
	assert.Equal(720, grounded[0].Confidence.ReportedScore, "0.8 stated × 0.9 current")
	assert.Equal(200, grounded[1].Confidence.ReportedScore, "0.8 stated × 0.25 current")
	assert.Equal(90, grounded[2].Confidence.ReportedScore, "0.1 stated × 0.9 current")
	assert.Equal(800, grounded[3].Confidence.ReportedScore, "a value with an address is never sent")
	assert.Equal(700, grounded[4].Confidence.ReportedScore, "a sensitive target is never sent")
	assert.Equal(950, claims[0].Confidence.ReportedScore, "the input is not modified")
	for i := range claims {
		assert.Equal(claims[i].SubmittedValue, grounded[i].SubmittedValue)
		assert.Equal(claims[i].Evidence, grounded[i].Evidence)
	}

	requests := server.Requests()
	require.Len(requests, 1)
	state, ok := requests[0]["state"].(map[string]any)
	require.True(ok)
	sent, ok := state["claims"].(map[string]any)
	require.True(ok)
	require.Len(sent, 3)
	first, ok := sent["claim_1"].(map[string]any)
	require.True(ok)
	assert.Equal("Job title", first["fact"])
	assert.Equal("support", first["relation"])
	assert.Equal("VP Product", first["value"])
	evidence, ok := first["evidence"].([]any)
	require.True(ok)
	require.Len(evidence, 1)
	item, ok := evidence[0].(map[string]any)
	require.True(ok)
	assert.Equal("2026-02-01", item["date"])
	assert.NotContains(item["text"], "casey@example.com")
	second, ok := sent["claim_2"].(map[string]any)
	require.True(ok)
	secondEvidence, ok := second["evidence"].([]any)
	require.True(ok)
	require.Len(secondEvidence, 2)
	assert.Equal("2026-02-02", secondEvidence[0].(map[string]any)["date"], "newest excerpt first")
	questions, ok := requests[0]["questions"].(map[string]any)
	require.True(ok)
	assert.Len(questions, 6, "two questions per sent claim")
}

func TestGrounderPacksDenseClaimsWithinTheTokenBudget(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	server := jevtest.NewServer(t, groundingByValue)
	service, cfg := server.Service(t, st, func(cfg *jev.Config) {
		cfg.SweepClaimGrounding = jev.FeatureConfig{Enabled: true}
	})
	jevtest.GrantConsent(t, st, cfg, sweepjudge.ClaimGroundingFeature())
	// Eight claims with three full-length excerpts of densely tokenizing
	// text each do not fit one request's token budget.
	dense := strings.Repeat("🧾", 990)
	claims := make([]personfacts.ProposedClaim, sweepjudge.ClaimsPerRequest)
	for i := range claims {
		claims[i] = groundingClaim(`"Designer"`, 900, "Designer "+dense, dense, dense)
	}

	grounded := sweepjudge.NewGrounder(service, false, nil).GroundClaims(t.Context(), 7, claims)
	for i := range grounded {
		assert.Equal(720, grounded[i].Confidence.ReportedScore, "claim %d is grounded", i)
	}
	requests := server.Requests()
	require.Greater(len(requests), 1)
	for _, request := range requests {
		tokens, err := jev.EstimateStateTokens(request["state"], sweepjudge.ClaimGroundingFeature().Questions)
		require.NoError(err)
		assert.LessOrEqual(tokens, jev.MaxStateTokens)
	}
}

func TestGrounderKeepsReportedConfidenceWhenJevIsUnavailable(t *testing.T) {
	st := testutil.NewTestStore(t)
	server := jevtest.NewServer(t, groundingByValue)
	service, _ := server.Service(t, st, func(cfg *jev.Config) {
		cfg.SweepClaimGrounding = jev.FeatureConfig{Enabled: true}
	})
	claims := []personfacts.ProposedClaim{groundingClaim(`"VP Product"`, 950, "I am VP Product.")}

	grounded := sweepjudge.NewGrounder(service, false, nil).GroundClaims(t.Context(), 7, claims)
	assert.Equal(t, claims, grounded, "no consent: nothing is sent and nothing changes")
	assert.Empty(t, server.Requests())
}

func TestGroundedScoreRoundsAndClamps(t *testing.T) {
	assert := assert.New(t)
	assert.Equal(1000, sweepjudge.GroundedScore(1, 1))
	assert.Equal(0, sweepjudge.GroundedScore(0, 0.9))
	assert.Equal(333, sweepjudge.GroundedScore(0.5, 0.666))
	assert.Equal(1000, sweepjudge.GroundedScore(1.2, 1))
}
