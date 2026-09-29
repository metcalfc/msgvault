package personenrichment_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/personenrichment"
	"go.kenn.io/msgvault/internal/personfacts"
	"go.kenn.io/msgvault/internal/store"
)

// fixedJudge answers every review with the same probabilities, or fails.
type fixedJudge struct {
	nameCompatible, companySame, nameConflict float64
	err                                       error
	reviews                                   []personenrichment.IdentityReview
}

func (j *fixedJudge) JudgeIdentity(_ context.Context, review personenrichment.IdentityReview) (personenrichment.IdentityJudgment, error) {
	j.reviews = append(j.reviews, review)
	if j.err != nil {
		return personenrichment.IdentityJudgment{}, j.err
	}
	judgment := personenrichment.IdentityJudgment{
		ExactClass: review.Exact, NameCompatible: j.nameCompatible, CompanySame: j.companySame,
		NameConflict: j.nameConflict, Model: jev.DefaultModel,
	}
	judgment.Outcome = personenrichment.DecideIdentityJudgment(review.Exact, j.nameCompatible, j.companySame, j.nameConflict)
	return judgment, nil
}

// partialIdentityFixture prepares a worker whose Exa provider returns a
// result in which exactly one of name and company matched exactly.
func partialIdentityFixture(t *testing.T, name string, requestName, requestCompany string,
	returned personenrichment.ReturnedIdentity, exactMatch personenrichment.IdentityMatch,
) (*workerFixture, map[string]personenrichment.ProviderFactory, map[string]personenrichment.ProviderConfig) {
	t.Helper()
	f := newWorkerFixture(t, name, func(cfg *personenrichment.ProviderConfig) {
		cfg.Mode = "people"
		cfg.AllowedIdentifiers = []personenrichment.IdentifierClass{
			personenrichment.IdentifierName, personenrichment.IdentifierCurrentCompany,
		}
	})
	_, err := f.store.AddPersonContactPointContext(t.Context(), f.person.ID, store.PersonContactPointInput{
		AddressKind: store.ContactAddressURL, OriginalValue: "https://profiles.example.test/unused",
		Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceUser},
	})
	require.NoError(t, err)
	factories := map[string]personenrichment.ProviderFactory{
		f.config.Name: func(personenrichment.ProviderConfig, string) (personenrichment.Provider, error) {
			return &functionProvider{
				start: func(_ context.Context, request personenrichment.Request) (personenrichment.Attempt, error) {
					require.Equal(t, requestName, request.Identity.Name)
					require.Equal(t, requestCompany, request.Identity.CurrentCompany)
					returnedCopy := returned
					result := partialWorkerResult(f.target, exactMatch, &returnedCopy)
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
	return f, factories, map[string]personenrichment.ProviderConfig{f.config.Name: f.config}
}

// partialWorkerResult is a complete people-mode result whose only exact
// identity match is the given one, with the provider-side identity attached
// for review. Its claim is a synthetic provider assertion.
func partialWorkerResult(
	target personfacts.TargetDescriptor, exactMatch personenrichment.IdentityMatch,
	returned *personenrichment.ReturnedIdentity,
) personenrichment.Result {
	return personenrichment.Result{
		State: personenrichment.ResultComplete, RequestID: "opaque-request-partial",
		AdapterVersion: "test-adapter-v1", SchemaVersion: "test-wire-v1",
		ProviderVersion: "test-provider-v1", FreshAsOf: time.Now().UTC(),
		IdentityMatches:   []personenrichment.IdentityMatch{exactMatch},
		ReturnedIdentity:  returned,
		ProviderPersonIDs: []personenrichment.ProviderPersonID{{ID: "provider-person-partial"}},
		Claims: []personfacts.ProposedClaim{{
			Target: target, Relation: personfacts.RelationSupport,
			SubmittedValue: json.RawMessage(`"Synthetic biography"`),
			Origin:         personfacts.OriginEnrichment,
			Confidence:     personfacts.ConfidenceInputs{ReportedScore: 900},
			Evidence: []personfacts.EvidenceInput{{
				SourceClass: personfacts.EvidenceProviderAssertion,
				Directness:  personfacts.Indirect, Authority: personfacts.AuthorityAggregator,
				Excerpt: "Synthetic provider assertion.",
			}},
		}},
	}
}

// seedNameAndCompany gives the fixture person a preferred formatted name and
// a current primary employment, which LoadRequestInput turns into the
// request's name and current company.
func seedNameAndCompany(t *testing.T, f *workerFixture, name, company string) {
	t.Helper()
	_, err := f.store.AddPersonNameContext(t.Context(), f.person.ID, store.PersonNameInput{
		NameKind: store.PersonNameFormatted, OriginalValue: name,
		Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceUser, Pref: new(1)},
	})
	require.NoError(t, err)
	org, err := f.store.CreateOrganizationContext(t.Context(), store.OrganizationInput{Name: company})
	require.NoError(t, err)
	_, err = f.store.AddEmploymentContext(t.Context(), store.EmploymentInput{
		PersonID: f.person.ID, OrganizationID: org.ID, IsCurrent: new(true), IsPrimary: new(true),
	})
	require.NoError(t, err)
}

func runPartialIdentityCase(t *testing.T, f *workerFixture, factories map[string]personenrichment.ProviderFactory,
	configs map[string]personenrichment.ProviderConfig, judge personenrichment.IdentityJudge,
) store.PersonEnrichmentAttempt {
	t.Helper()
	f.enqueue(t)
	options := f.options(configs)
	options.IdentityJudge = judge
	worker, err := personenrichment.NewWorker(f.store, f.store, f.gate(t, func(string) (string, bool) { return "test-key", true }), factories, options)
	require.NoError(t, err)
	processed, err := worker.RunOnce(t.Context(), f.run.ID)
	require.NoError(t, err)
	require.True(t, processed)
	attempts, err := f.store.ListPersonEnrichmentAttemptsContext(t.Context(), store.PersonEnrichmentAttemptFilter{
		PersonID: f.person.ID, RunID: f.run.ID, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	return attempts[0]
}

func TestWorkerAcceptsAbbreviatedSurnameAtTheSameCompanyWithAJudge(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f, factories, configs := partialIdentityFixture(t, "exa-priya", "priya ramanathan", "example capital",
		personenrichment.ReturnedIdentity{
			Name: "Priya R.", FirstName: "Priya", LastName: "R.",
			CurrentRoles: []personenrichment.ReturnedRole{{Title: "Partner", Company: "Example Capital"}},
		},
		personenrichment.IdentityMatch{Class: personenrichment.IdentifierCurrentCompany, Value: "Example Capital", Confidence: 900},
	)
	seedNameAndCompany(t, f, "Priya Ramanathan", "Example Capital")
	judge := &fixedJudge{nameCompatible: 0.95, companySame: 0.99, nameConflict: 0.02}
	attempt := runPartialIdentityCase(t, f, factories, configs, judge)
	assert.Equal("succeeded", attempt.State)
	require.Len(judge.reviews, 1)
	assert.Equal(personenrichment.IdentifierCurrentCompany, judge.reviews[0].Exact)
	assert.Equal("priya ramanathan", judge.reviews[0].Requested.Name)
	assert.Equal("Priya R.", judge.reviews[0].Returned.Name)

	judgment, err := f.store.GetPersonEnrichmentIdentityJudgmentContext(t.Context(), attempt.ID)
	require.NoError(err)
	assert.Equal(personenrichment.IdentityJudgmentAccepted, judgment.Outcome)
	assert.Equal("succeeded", judgment.AttemptState)
	assert.InDelta(0.95, judgment.NameCompatible, 1e-9)
	evidence, err := f.store.ListPersonFactEvidenceContext(t.Context(), f.person.ID, personfacts.EvidenceFilter{Limit: 10})
	require.NoError(err)
	require.NotEmpty(evidence)
	for _, item := range evidence {
		assert.Equal(personenrichment.SemanticIdentityScore, item.Input.IdentityScore,
			"an accepted semantic match carries the same identity score as an exact one")
	}
}

func TestWorkerAcceptsAcceleratorBatchTagOnTheCompanyWithAJudge(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f, factories, configs := partialIdentityFixture(t, "exa-batch-tag", "test user", "example labs",
		personenrichment.ReturnedIdentity{
			Name: "Test User", CurrentRoles: []personenrichment.ReturnedRole{{Title: "Founder", Company: "Example Labs (YC W21)"}},
		},
		personenrichment.IdentityMatch{Class: personenrichment.IdentifierName, Value: "Test User", Confidence: 900},
	)
	seedNameAndCompany(t, f, "Test User", "Example Labs")
	judge := &fixedJudge{nameCompatible: 0.99, companySame: 0.97, nameConflict: 0.01}
	attempt := runPartialIdentityCase(t, f, factories, configs, judge)
	assert.Equal("succeeded", attempt.State)
	require.Len(judge.reviews, 1)
	assert.Equal(personenrichment.IdentifierName, judge.reviews[0].Exact)
	assert.Equal("Example Labs (YC W21)", judge.reviews[0].Returned.CurrentRoles[0].Company)
}

func TestWorkerRejectsWrongPersonWithMatchingCommonNameDespiteAJudge(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f, factories, configs := partialIdentityFixture(t, "exa-wrong", "priya ramanathan", "example capital",
		personenrichment.ReturnedIdentity{
			Name: "Priya Okafor", CurrentRoles: []personenrichment.ReturnedRole{{Title: "Analyst", Company: "Example Capital"}},
		},
		personenrichment.IdentityMatch{Class: personenrichment.IdentifierCurrentCompany, Value: "Example Capital", Confidence: 900},
	)
	seedNameAndCompany(t, f, "Priya Ramanathan", "Example Capital")
	judge := &fixedJudge{nameCompatible: 0.30, companySame: 0.99, nameConflict: 0.85}
	attempt := runPartialIdentityCase(t, f, factories, configs, judge)
	assert.Equal("identity_rejected", attempt.State)
	judgment, err := f.store.GetPersonEnrichmentIdentityJudgmentContext(t.Context(), attempt.ID)
	require.NoError(err)
	assert.Equal(personenrichment.IdentityJudgmentRejected, judgment.Outcome, "the rejection is recorded for audit")
	evidence, err := f.store.ListPersonFactEvidenceContext(t.Context(), f.person.ID, personfacts.EvidenceFilter{Limit: 10})
	require.NoError(err)
	for _, item := range evidence {
		assert.Zero(item.Input.IdentityScore, "no claim from a rejected identity can reach the resolver's floor")
	}
}

func TestWorkerFallsBackToRejectionWhenTheJudgeIsUnavailable(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f, factories, configs := partialIdentityFixture(t, "exa-unavailable", "priya ramanathan", "example capital",
		personenrichment.ReturnedIdentity{
			Name: "Priya R.", CurrentRoles: []personenrichment.ReturnedRole{{Company: "Example Capital"}},
		},
		personenrichment.IdentityMatch{Class: personenrichment.IdentifierCurrentCompany, Value: "Example Capital", Confidence: 900},
	)
	seedNameAndCompany(t, f, "Priya Ramanathan", "Example Capital")
	judge := &fixedJudge{err: jev.ErrBreakerOpen}
	attempt := runPartialIdentityCase(t, f, factories, configs, judge)
	assert.Equal("identity_rejected", attempt.State, "an unavailable judge leaves the exact rule's answer")
	assert.Len(judge.reviews, 1)
	_, err := f.store.GetPersonEnrichmentIdentityJudgmentContext(t.Context(), attempt.ID)
	require.Error(err, "no judgment is recorded when none was made")
}

func TestWorkerWithoutAJudgeRejectsPartialMatchesAsBefore(t *testing.T) {
	assert := assert.New(t)
	f, factories, configs := partialIdentityFixture(t, "exa-nojudge", "priya ramanathan", "example capital",
		personenrichment.ReturnedIdentity{
			Name: "Priya R.", CurrentRoles: []personenrichment.ReturnedRole{{Company: "Example Capital"}},
		},
		personenrichment.IdentityMatch{Class: personenrichment.IdentifierCurrentCompany, Value: "Example Capital", Confidence: 900},
	)
	seedNameAndCompany(t, f, "Priya Ramanathan", "Example Capital")
	attempt := runPartialIdentityCase(t, f, factories, configs, nil)
	assert.Equal("identity_rejected", attempt.State)
}

func TestWorkerRecordsUncertainJudgmentsWithoutApplyingClaims(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f, factories, configs := partialIdentityFixture(t, "exa-uncertain", "priya ramanathan", "example capital",
		personenrichment.ReturnedIdentity{
			Name: "P. Ramanathan-Example", CurrentRoles: []personenrichment.ReturnedRole{{Company: "Example Capital"}},
		},
		personenrichment.IdentityMatch{Class: personenrichment.IdentifierCurrentCompany, Value: "Example Capital", Confidence: 900},
	)
	seedNameAndCompany(t, f, "Priya Ramanathan", "Example Capital")
	judge := &fixedJudge{nameCompatible: 0.70, companySame: 0.99, nameConflict: 0.10}
	attempt := runPartialIdentityCase(t, f, factories, configs, judge)
	assert.Equal("identity_uncertain", attempt.State)
	require.NotNil(attempt.FailureClass)
	assert.Equal("identity_uncertain", *attempt.FailureClass)
	judgment, err := f.store.GetPersonEnrichmentIdentityJudgmentContext(t.Context(), attempt.ID)
	require.NoError(err)
	assert.Equal(personenrichment.IdentityJudgmentUncertain, judgment.Outcome)
	assert.InDelta(0.70, judgment.NameCompatible, 1e-9)
	uncertain, err := f.store.ListPersonEnrichmentIdentityJudgmentsContext(t.Context(), store.PersonEnrichmentIdentityJudgmentFilter{
		Outcome: personenrichment.IdentityJudgmentUncertain, Limit: 10,
	})
	require.NoError(err)
	require.Len(uncertain, 1)
	assert.Equal(attempt.ID, uncertain[0].AttemptID)
	evidence, err := f.store.ListPersonFactEvidenceContext(t.Context(), f.person.ID, personfacts.EvidenceFilter{Limit: 10})
	require.NoError(err)
	for _, item := range evidence {
		assert.Zero(item.Input.IdentityScore, "uncertain evidence cannot reach the resolver's identity floor")
	}
}
