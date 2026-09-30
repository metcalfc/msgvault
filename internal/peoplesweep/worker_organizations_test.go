package peoplesweep

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/personfacts"
)

// recordingOrganizations records what the worker hands organization
// resolution, and when relative to the apply.
type recordingOrganizations struct {
	sink   *workerProductionSink
	people []int64
	claims [][]personfacts.ProposedClaim
	before []int
	fences []*personfacts.WriteFence
}

func (r *recordingOrganizations) PrepareEmploymentOrganizations(
	_ context.Context, personID int64, claims []personfacts.ProposedClaim, fence *personfacts.WriteFence,
) {
	r.people = append(r.people, personID)
	r.claims = append(r.claims, claims)
	r.before = append(r.before, len(r.sink.requests))
	r.fences = append(r.fences, fence)
}

func TestPersonSweepWorkerPreparesOrganizationsBeforeApplying(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	config, catalog := workerTestConfig(t)
	mutateTestProvider(&config, func(provider *ProviderConfig) { provider.AllowSensitive = true })
	config.Budgets.MaxRequestsPerPerson = 2
	config.Budgets.MaxRequestsPerRun = 2
	config.Budgets.MaxRequestsPerDay = 2
	config.Budgets.MaxInputTokensPerPerson = 4_000_000
	config.Budgets.MaxInputTokensPerRun = 4_000_000
	config.Budgets.MaxInputTokensPerDay = 4_000_000
	config.Budgets.MaxOutputTokensPerPerson = 2 * extractionMaxOutputTokens
	config.Budgets.MaxOutputTokensPerRun = 2 * extractionMaxOutputTokens
	config.Budgets.MaxOutputTokensPerDay = 2 * extractionMaxOutputTokens
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	store := &workerFailureStore{cursor: Cursor{ReconciliationComplete: true, LastBackstopAt: &now}}
	seed := packetTestEvidence(71, SourceConversationText, "organization seed")
	source := &workerProductionSource{windows: map[GenerationCursorMode]PersonWindow{
		GenerationCursorOptimistic: {Seeds: []EvidenceItem{seed}, Changes: []ArchiveChange{{
			Sequence: 1, PersonID: 7, SourceLane: SourceConversationText}}, NextSequence: 1},
	}}
	candidate := fmt.Sprintf(`{"claims":[{"target_key":%q,"relation":"support","value":"Riverton",`+
		`"evidence_ids":[%q],"valid_from":null,"valid_until":null,"confidence_basis_points":900}]}`,
		catalog.Targets[0].Key, packetEvidenceID(seed))
	driver := &workerRepairDriver{responses: []DriverResponse{
		briefDriverResponse(json.RawMessage(candidate), "request-organizations"),
	}}
	sink := &workerProductionSink{}
	organizations := &recordingOrganizations{sink: sink}
	worker := Worker{Config: config, Store: store, Source: source,
		Context: NewContextRetriever(source), Sink: sink,
		Runner:        newWorkerRepairRunner(t, config, driver),
		Organizations: organizations,
		Catalog:       workerFailureCatalog{catalog: catalog}, Clock: func() time.Time { return now },
		NewID: func() string { return "attempt-organizations" }, WorkerID: "worker-fixture"}

	_, err := worker.RunPerson(t.Context(), "run-organizations", Lease{PersonID: 7,
		WorkerID: "worker-fixture", Fence: 1, ExpiresAt: now.Add(time.Hour)}, RunIncremental)
	require.NoError(err)
	require.Len(sink.requests, 1)
	require.Len(sink.requests[0].Generation.Claims, 1)
	assert.Equal([]int64{7}, organizations.people)
	require.Len(organizations.claims, 1)
	assert.Equal(sink.requests[0].Generation.Claims, organizations.claims[0])
	assert.Equal([]int{0}, organizations.before, "organizations are prepared before the generation is applied")
	assert.Equal([]*personfacts.WriteFence{{
		Kind: personfacts.FencePersonSweep, PersonID: 7, Owner: "worker-fixture", Fence: 1,
	}}, organizations.fences, "every organization write is fenced by this attempt's lease")
}

// rescoringGrounder sets every claim's reported score and records what it
// was given.
type rescoringGrounder struct {
	score  int
	people []int64
	given  [][]personfacts.ProposedClaim
}

func (g *rescoringGrounder) GroundClaims(
	_ context.Context, personID int64, claims []personfacts.ProposedClaim,
) []personfacts.ProposedClaim {
	g.people = append(g.people, personID)
	g.given = append(g.given, claims)
	out := append([]personfacts.ProposedClaim(nil), claims...)
	for i := range out {
		out[i].Confidence = personfacts.ConfidenceInputs{ReportedScore: g.score}
	}
	return out
}

func TestPersonSweepWorkerAppliesGroundedClaimConfidence(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	config, catalog := workerTestConfig(t)
	mutateTestProvider(&config, func(provider *ProviderConfig) { provider.AllowSensitive = true })
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	store := &workerFailureStore{cursor: Cursor{ReconciliationComplete: true, LastBackstopAt: &now}}
	seed := packetTestEvidence(71, SourceConversationText, "grounding seed")
	source := &workerProductionSource{windows: map[GenerationCursorMode]PersonWindow{
		GenerationCursorOptimistic: {Seeds: []EvidenceItem{seed}, Changes: []ArchiveChange{{
			Sequence: 1, PersonID: 7, SourceLane: SourceConversationText}}, NextSequence: 1},
	}}
	candidate := fmt.Sprintf(`{"claims":[{"target_key":%q,"relation":"support","value":"Riverton",`+
		`"evidence_ids":[%q],"valid_from":null,"valid_until":null,"confidence_basis_points":990}]}`,
		catalog.Targets[0].Key, packetEvidenceID(seed))
	driver := &workerRepairDriver{responses: []DriverResponse{
		briefDriverResponse(json.RawMessage(candidate), "request-grounding"),
	}}
	sink := &workerProductionSink{}
	organizations := &recordingOrganizations{sink: sink}
	grounder := &rescoringGrounder{score: 420}
	worker := Worker{Config: config, Store: store, Source: source,
		Context: NewContextRetriever(source), Sink: sink,
		Runner:        newWorkerRepairRunner(t, config, driver),
		Organizations: organizations, Grounder: grounder,
		Catalog: workerFailureCatalog{catalog: catalog}, Clock: func() time.Time { return now },
		NewID: func() string { return "attempt-grounding" }, WorkerID: "worker-fixture"}

	_, err := worker.RunPerson(t.Context(), "run-grounding", Lease{PersonID: 7,
		WorkerID: "worker-fixture", Fence: 1, ExpiresAt: now.Add(time.Hour)}, RunIncremental)
	require.NoError(err)
	assert.Equal([]int64{7}, grounder.people)
	require.Len(grounder.given, 1)
	require.Len(grounder.given[0], 1)
	assert.Equal(990, grounder.given[0][0].Confidence.ReportedScore, "the grounder sees the model's score")
	require.Len(sink.requests, 1)
	require.Len(sink.requests[0].Generation.Claims, 1)
	assert.Equal(420, sink.requests[0].Generation.Claims[0].Confidence.ReportedScore)
	require.Len(organizations.claims, 1)
	assert.Equal(420, organizations.claims[0][0].Confidence.ReportedScore,
		"organization resolution sees the grounded claims")
}
