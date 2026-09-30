package personenrichment_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/personenrichment"
	"go.kenn.io/msgvault/internal/personfacts"
	"go.kenn.io/msgvault/internal/store"
)

// recordingPreparer records what the worker hands organization resolution.
type recordingPreparer struct {
	mu             sync.Mutex
	store          *store.Store
	organizationID int64
	people         []int64
	claims         [][]personfacts.ProposedClaim
	fenced         []error
}

// PrepareEmploymentOrganizations records the call and makes one fenced write
// through the real store, which checks the fence inside its transaction.
func (p *recordingPreparer) PrepareEmploymentOrganizations(
	ctx context.Context, personID int64, claims []personfacts.ProposedClaim, fence *personfacts.WriteFence,
) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.people = append(p.people, personID)
	p.claims = append(p.claims, claims)
	_, err := p.store.RecordEmploymentTitleAliasContext(ctx, store.EmploymentTitleAliasInput{
		OrganizationID: p.organizationID, Title: "GP", CanonicalTitle: "Partner",
		Model: "jev-1.13.0", Confidence: 0.9, Fence: fence,
	})
	p.fenced = append(p.fenced, err)
}

func TestWorkerPreparesOrganizationsBeforeCommittingAnAcceptedResult(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newWorkerFixture(t, "organizations", nil)
	fixture.enqueue(t)
	organization, err := fixture.store.CreateOrganizationContext(t.Context(), store.OrganizationInput{
		Name: "Example Labs", Kind: store.OrganizationKindCompany,
	})
	require.NoError(err)
	preparer := &recordingPreparer{store: fixture.store, organizationID: organization.ID}
	factories := map[string]personenrichment.ProviderFactory{
		fixture.config.Name: func(personenrichment.ProviderConfig, string) (personenrichment.Provider, error) {
			return &functionProvider{
				start: func(_ context.Context, request personenrichment.Request) (personenrichment.Attempt, error) {
					result := workerResult(t, request, fixture.target, "opaque-request-organizations", "", false, "", personenrichment.Cost{})
					return personenrichment.Attempt{
						State: personenrichment.AttemptComplete, RequestID: result.RequestID,
						AdapterVersion: result.AdapterVersion, SchemaVersion: result.SchemaVersion,
						ProgramFingerprint: workerProgramFingerprint(t, false, ""), Result: &result,
					}, nil
				},
				poll: func(context.Context, personenrichment.Attempt) (personenrichment.Result, error) {
					return personenrichment.Result{}, errors.New("unexpected poll")
				},
			}, nil
		},
	}
	options := fixture.options(map[string]personenrichment.ProviderConfig{fixture.config.Name: fixture.config})
	options.OrganizationPreparer = preparer
	worker, err := personenrichment.NewWorker(fixture.store, fixture.store,
		fixture.gate(t, func(string) (string, bool) { return "test-key", true }), factories, options)
	require.NoError(err)

	processed, err := worker.RunOnce(t.Context(), fixture.run.ID)
	require.NoError(err)
	assert.True(processed)
	require.Equal([]int64{fixture.person.ID}, preparer.people)
	require.Len(preparer.claims, 1)
	require.Len(preparer.claims[0], 1)
	assert.Equal(fixture.target.Key, preparer.claims[0][0].Target.Key)
	require.Len(preparer.fenced, 1)
	require.NoError(preparer.fenced[0], "the attempt's own work lease lets a fenced write through")

	attempts, err := fixture.store.ListPersonEnrichmentAttemptsContext(t.Context(), store.PersonEnrichmentAttemptFilter{
		PersonID: fixture.person.ID, RunID: fixture.run.ID, Limit: 10,
	})
	require.NoError(err)
	require.Len(attempts, 1)
	assert.Equal("succeeded", attempts[0].State, "preparing organizations never blocks the commit")
}
