package personenrichment_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

// retryFixture prepares a worker whose Exa-shaped provider returns no entity
// for the requested name and a full match for its first code-built variant.
func retryFixture(t *testing.T, name string, maxRequestsPerRun int64) (*workerFixture, map[string]personenrichment.ProviderFactory, map[string]personenrichment.ProviderConfig, *[]string) {
	t.Helper()
	f := newWorkerFixture(t, name, func(cfg *personenrichment.ProviderConfig) {
		cfg.Mode = "people"
		cfg.AllowedIdentifiers = []personenrichment.IdentifierClass{
			personenrichment.IdentifierName, personenrichment.IdentifierCurrentCompany,
		}
		cfg.MaxRequestsPerRun = maxRequestsPerRun
	})
	seedNameAndCompany(t, f, "Test Q. User", "Example Labs")
	names := make([]string, 0, 2)
	factories := map[string]personenrichment.ProviderFactory{
		f.config.Name: func(personenrichment.ProviderConfig, string) (personenrichment.Provider, error) {
			return &functionProvider{
				start: func(_ context.Context, request personenrichment.Request) (personenrichment.Attempt, error) {
					names = append(names, request.Identity.Name)
					if request.Identity.Name != "test user" {
						return personenrichment.Attempt{}, &personenrichment.NoEntityError{
							Provider: &personenrichment.ProviderError{Class: personenrichment.FailureInvalidOutput, RequestID: "empty-1"},
							Cost:     personenrichment.Cost{Currency: "USD", AmountMicros: 2000},
						}
					}
					result := partialWorkerResult(f.target,
						personenrichment.IdentityMatch{Class: personenrichment.IdentifierName, Value: "Test User", Confidence: 900},
						&personenrichment.ReturnedIdentity{Name: "Test User"})
					result.IdentityMatches = append(result.IdentityMatches, personenrichment.IdentityMatch{
						Class: personenrichment.IdentifierCurrentCompany, Value: "Example Labs", Confidence: 900,
					})
					result.IdentityConfidence = 900
					result.Cost = personenrichment.Cost{Currency: "USD", AmountMicros: 7000}
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
	return f, factories, map[string]personenrichment.ProviderConfig{f.config.Name: f.config}, &names
}

func nameCompanyIdentifierCount(t *testing.T, f *workerFixture, attemptID int64) int64 {
	t.Helper()
	var count int64
	require.NoError(t, f.store.DB().QueryRowContext(t.Context(), f.store.Rebind(`
		SELECT COUNT(*) FROM person_enrichment_attempt_identifiers
		WHERE attempt_id = ? AND identifier_class = 'name_company'`), attemptID).Scan(&count))
	return count
}

func runRequestsStarted(t *testing.T, f *workerFixture) int64 {
	t.Helper()
	var started int64
	require.NoError(t, f.store.DB().QueryRowContext(t.Context(), f.store.Rebind(`
		SELECT requests_started FROM person_enrichment_run_counters WHERE run_id = ?`), f.run.ID).Scan(&started))
	return started
}

func runCostCharged(t *testing.T, f *workerFixture) int64 {
	t.Helper()
	var charged int64
	require.NoError(t, f.store.DB().QueryRowContext(t.Context(), f.store.Rebind(`
		SELECT cost_charged_usd_micros FROM person_enrichment_run_counters WHERE run_id = ?`), f.run.ID).Scan(&charged))
	return charged
}

func dayCostCharged(t *testing.T, f *workerFixture) int64 {
	t.Helper()
	var charged int64
	require.NoError(t, f.store.DB().QueryRowContext(t.Context(), f.store.Rebind(`
		SELECT COALESCE(SUM(cost_charged_usd_micros), 0) FROM person_enrichment_day_counters
		WHERE profile_fingerprint = ?`), f.profile.Fingerprint).Scan(&charged))
	return charged
}

func TestWorkerChargesBothCallsWhenTheNameVariantRetryFails(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	for _, estimated := range []bool{true, false} {
		name := "exa-retry-fails-firm"
		if estimated {
			// Exa reports every charge as an estimate.
			name = "exa-retry-fails-estimated"
		}
		f := newWorkerFixture(t, name, func(cfg *personenrichment.ProviderConfig) {
			cfg.Mode = "people"
			cfg.AllowedIdentifiers = []personenrichment.IdentifierClass{
				personenrichment.IdentifierName, personenrichment.IdentifierCurrentCompany,
			}
		})
		seedNameAndCompany(t, f, "Test Q. User", "Example Labs")
		names := make([]string, 0, 2)
		factories := map[string]personenrichment.ProviderFactory{
			f.config.Name: func(personenrichment.ProviderConfig, string) (personenrichment.Provider, error) {
				return &functionProvider{
					start: func(_ context.Context, request personenrichment.Request) (personenrichment.Attempt, error) {
						names = append(names, request.Identity.Name)
						if len(names) == 1 {
							empty := personenrichment.Cost{Currency: "USD", AmountMicros: 2000, Estimated: estimated}
							return personenrichment.Attempt{}, &personenrichment.NoEntityError{
								Provider: &personenrichment.ProviderError{Class: personenrichment.FailureInvalidOutput, RequestID: "empty-1",
									Cost: empty},
								Cost: empty,
							}
						}
						return personenrichment.Attempt{}, &personenrichment.ProviderError{
							Class: personenrichment.FailureInvalidOutput, RequestID: "variant-1",
							Cost: personenrichment.Cost{Currency: "USD", AmountMicros: 3000, Estimated: estimated},
						}
					},
					poll: func(context.Context, personenrichment.Attempt) (personenrichment.Result, error) {
						return personenrichment.Result{}, errors.New("unexpected poll")
					},
				}, nil
			},
		}
		attempt := runPartialIdentityCase(t, f, factories, map[string]personenrichment.ProviderConfig{f.config.Name: f.config}, nil)
		assert.Equal([]string{"test q. user", "test user"}, names, name)
		assert.Equal("terminal", attempt.State, name)
		require.NotNil(attempt.ActualCostUSDMicros, name)
		assert.Equal(int64(5000), *attempt.ActualCostUSDMicros, "%s: the attempt records both calls' charges", name)
		assert.Equal(int64(5000), runCostCharged(t, f), "%s: both reach the run counter", name)
		assert.Equal(int64(5000), dayCostCharged(t, f), "%s: and the day counter", name)
		assert.Equal(int64(2), runRequestsStarted(t, f), name)
	}

	capped := newWorkerFixture(t, "exa-retry-capped-charge", func(cfg *personenrichment.ProviderConfig) {
		cfg.Mode = "people"
		cfg.AllowedIdentifiers = []personenrichment.IdentifierClass{
			personenrichment.IdentifierName, personenrichment.IdentifierCurrentCompany,
		}
		cfg.MaxRequestsPerRun = 1
	})
	seedNameAndCompany(t, capped, "Test Q. User", "Example Labs")
	cappedFactories := map[string]personenrichment.ProviderFactory{
		capped.config.Name: func(personenrichment.ProviderConfig, string) (personenrichment.Provider, error) {
			return &functionProvider{
				start: func(context.Context, personenrichment.Request) (personenrichment.Attempt, error) {
					return personenrichment.Attempt{}, &personenrichment.NoEntityError{
						Provider: &personenrichment.ProviderError{Class: personenrichment.FailureInvalidOutput, RequestID: "empty-2",
							Cost: personenrichment.Cost{Currency: "USD", AmountMicros: 2000}},
						Cost: personenrichment.Cost{Currency: "USD", AmountMicros: 2000},
					}
				},
				poll: func(context.Context, personenrichment.Attempt) (personenrichment.Result, error) {
					return personenrichment.Result{}, errors.New("unexpected poll")
				},
			}, nil
		},
	}
	cappedAttempt := runPartialIdentityCase(t, capped, cappedFactories, map[string]personenrichment.ProviderConfig{capped.config.Name: capped.config}, nil)
	assert.Equal("terminal", cappedAttempt.State)
	assert.Equal(int64(2000), runCostCharged(t, capped), "a skipped retry still charges the empty lookup")
}

func TestWorkerRetriesAnEmptyLookupOnceWithANameVariantUnderBudget(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f, factories, configs, names := retryFixture(t, "exa-retry", 2)
	attempt := runPartialIdentityCase(t, f, factories, configs, nil)
	assert.Equal([]string{"test q. user", "test user"}, *names, "the middle initial is dropped for exactly one retry")
	assert.Equal("succeeded", attempt.State, "the variant result matches the requested name and company")
	require.NotNil(attempt.ActualCostUSDMicros)
	assert.Equal(int64(9000), *attempt.ActualCostUSDMicros, "both calls' charges are recorded")
	assert.Equal(int64(2), runRequestsStarted(t, f), "each provider call counts against the run budget")
	assert.Equal(int64(2), nameCompanyIdentifierCount(t, f, attempt.ID),
		"the variant identity is recorded through the same identifier-hash path as the first call")
}

func TestWorkerSkipsTheNameVariantRetryWhenTheRequestCapWouldBeExceeded(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f, factories, configs, names := retryFixture(t, "exa-capped", 1)
	attempt := runPartialIdentityCase(t, f, factories, configs, nil)
	assert.Equal([]string{"test q. user"}, *names, "a cap of one forbids the second paid call")
	assert.Equal("terminal", attempt.State, "the empty lookup's own outcome stands")
	require.NotNil(attempt.FailureClass)
	assert.Equal(string(personenrichment.FailureInvalidOutput), *attempt.FailureClass)
	assert.Equal(int64(1), runRequestsStarted(t, f), "a refused retry counts nothing")
	assert.Equal(int64(1), nameCompanyIdentifierCount(t, f, attempt.ID), "only the consented identity was recorded")
}

// runAnotherAttempt enqueues the fixture person again under a new trigger
// generation and returns the attempt the worker made for it.
func runAnotherAttempt(t *testing.T, f *workerFixture, factories map[string]personenrichment.ProviderFactory,
	configs map[string]personenrichment.ProviderConfig, judge personenrichment.IdentityJudge, generation string,
) store.PersonEnrichmentAttempt {
	t.Helper()
	require.NoError(t, f.store.PutPersonEnrichmentWorkContext(t.Context(), store.PersonEnrichmentWorkInput{
		PersonID: f.person.ID, ProfileFingerprint: f.profile.Fingerprint,
		Trigger: personenrichment.Trigger{Kind: personenrichment.TriggerTracked, Generation: generation},
		DueAt:   f.now,
	}))
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
	require.NotEmpty(t, attempts)
	latest := attempts[0]
	for _, attempt := range attempts[1:] {
		if attempt.ID > latest.ID {
			latest = attempt
		}
	}
	return latest
}

func TestWorkerStoresASemanticAcceptanceAtItsScoreAndOnlyVerifiedIDsSkipTheCheck(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f, factories, configs := partialIdentityFixture(t, "exa-second-run", "priya ramanathan", "example capital",
		personenrichment.ReturnedIdentity{
			Name: "Priya R.", CurrentRoles: []personenrichment.ReturnedRole{{Title: "Partner", Company: "Example Capital"}},
		},
		personenrichment.IdentityMatch{Class: personenrichment.IdentifierCurrentCompany, Value: "Example Capital", Confidence: 900},
	)
	seedNameAndCompany(t, f, "Priya Ramanathan", "Example Capital")
	first := runPartialIdentityCase(t, f, factories, configs,
		&fixedJudge{nameCompatible: 0.95, companySame: 0.99, nameConflict: 0.02})
	require.Equal("succeeded", first.State)
	stored, err := f.store.LoadProviderPersonIDs(t.Context(), f.person.ID, f.profile.ProviderNamespace)
	require.NoError(err)
	assert.Equal([]personenrichment.ProviderPersonID{{
		ID: "provider-person-partial", Confidence: personenrichment.SemanticIdentityScore,
	}}, stored, "a semantic acceptance is stored at the score it was accepted with, not zero")

	unavailable := &fixedJudge{err: jev.ErrBreakerOpen}
	second := runAnotherAttempt(t, f, factories, configs, unavailable, "revision:2")
	assert.NotEqual(first.ID, second.ID)
	assert.Equal("succeeded", second.State, "an ID verified at the semantic score is trusted on the next attempt")
	assert.Empty(unavailable.reviews, "the verified ID answers before any judgment is asked")

	_, err = f.store.DB().ExecContext(t.Context(), f.store.Rebind(`UPDATE person_enrichment_provider_identities
		SET confidence = 0 WHERE person_id = ?`), f.person.ID)
	require.NoError(err)
	third := runAnotherAttempt(t, f, factories, configs, nil, "revision:3")
	assert.NotEqual(second.ID, third.ID)
	assert.Equal("identity_rejected", third.State,
		"an ID stored below the verified confidence does not skip the name and company check")
}

// emptyThenRetryFactory returns a provider whose first lookup finds no entity
// at an estimated charge and whose name-variant retry fails with retryErr.
func emptyThenRetryFactory(f *workerFixture, retryErr error) map[string]personenrichment.ProviderFactory {
	calls := 0
	return map[string]personenrichment.ProviderFactory{
		f.config.Name: func(personenrichment.ProviderConfig, string) (personenrichment.Provider, error) {
			return &functionProvider{
				start: func(context.Context, personenrichment.Request) (personenrichment.Attempt, error) {
					calls++
					if calls == 1 {
						empty := personenrichment.Cost{Currency: "USD", AmountMicros: 2000, Estimated: true}
						return personenrichment.Attempt{}, &personenrichment.NoEntityError{
							Provider: &personenrichment.ProviderError{Class: personenrichment.FailureInvalidOutput,
								RequestID: "empty-1", Cost: empty},
							Cost: empty,
						}
					}
					return personenrichment.Attempt{}, retryErr
				},
				poll: func(context.Context, personenrichment.Attempt) (personenrichment.Result, error) {
					return personenrichment.Result{}, errors.New("unexpected poll")
				},
			}, nil
		},
	}
}

// TestWorkerCarriesTheEmptyLookupChargeThroughUncertainAndRetriedRetries: the
// empty lookup was billed whatever its name-variant retry did. When the retry
// times out (an uncertain start) or is rate limited (a scheduled retry), the
// run and day counters still include that charge.
func TestWorkerCarriesTheEmptyLookupChargeThroughUncertainAndRetriedRetries(t *testing.T) {
	cases := map[string]struct {
		retryErr error
		state    string
	}{
		"uncertain start": {retryErr: context.DeadlineExceeded, state: "uncertain_start"},
		"rate limited": {retryErr: &personenrichment.ProviderError{
			Class: personenrichment.FailureRateLimited, Status: 429, RequestID: "variant-429",
		}, state: "retry_wait"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			f := newWorkerFixture(t, "exa-carry-"+strings.ReplaceAll(name, " ", "-"), func(cfg *personenrichment.ProviderConfig) {
				cfg.Mode = "people"
				cfg.AllowedIdentifiers = []personenrichment.IdentifierClass{
					personenrichment.IdentifierName, personenrichment.IdentifierCurrentCompany,
				}
			})
			seedNameAndCompany(t, f, "Test Q. User", "Example Labs")
			attempt := runPartialIdentityCase(t, f, emptyThenRetryFactory(f, tc.retryErr),
				map[string]personenrichment.ProviderConfig{f.config.Name: f.config}, nil)
			assert.Equal(tc.state, attempt.State)
			assert.Equal(int64(2), runRequestsStarted(t, f))
			assert.Equal(int64(2000), runCostCharged(t, f), "the run counter includes the empty lookup")
			assert.Equal(int64(2000), dayCostCharged(t, f), "and so does the day counter")
			require.NotNil(attempt.ActualCostUSDMicros)
			assert.Equal(int64(2000), *attempt.ActualCostUSDMicros)
		})
	}
}

// TestWorkerKeepsTheEmptyLookupChargeWhenTheVariantIsSuppressed: when the
// name-variant identity is suppressed, the retry never goes out, but the
// empty lookup was already billed and the suppressed outcome records it.
func TestWorkerKeepsTheEmptyLookupChargeWhenTheVariantIsSuppressed(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := newWorkerFixture(t, "exa-variant-suppressed", func(cfg *personenrichment.ProviderConfig) {
		cfg.Mode = "people"
		cfg.AllowedIdentifiers = []personenrichment.IdentifierClass{
			personenrichment.IdentifierName, personenrichment.IdentifierCurrentCompany,
		}
	})
	seedNameAndCompany(t, f, "Test Q. User", "Example Labs")
	normalized, err := personenrichment.NormalizeSuppressionIdentifier(
		personenrichment.SuppressionNameCompany, []string{"test user", "example labs"})
	require.NoError(err)
	digest := f.hasher.Digest(f.profile.ProviderNamespace, normalized.Class, normalized.NormalizationVersion, normalized.Value)
	require.NoError(f.store.InsertPersonEnrichmentSuppressionsContext(t.Context(), []store.PersonEnrichmentSuppressionInput{{
		ProviderNamespace: digest.ProviderNamespace, IdentifierClass: digest.IdentifierClass,
		NormalizationVersion: digest.NormalizationVersion, KeyID: digest.KeyID, Digest: digest.Digest,
		Reason: store.PersonEnrichmentSuppressionOptOut, Actor: "test",
	}}))
	attempt := runPartialIdentityCase(t, f, emptyThenRetryFactory(f, errors.New("the variant must not be sent")),
		map[string]personenrichment.ProviderConfig{f.config.Name: f.config}, nil)
	assert.Equal("suppressed", attempt.State)
	assert.Equal(int64(1), runRequestsStarted(t, f), "the suppressed variant was never sent")
	assert.Equal(int64(2000), runCostCharged(t, f), "the empty lookup's charge reaches the run counter")
	assert.Equal(int64(2000), dayCostCharged(t, f))
	require.NotNil(attempt.ActualCostUSDMicros)
	assert.Equal(int64(2000), *attempt.ActualCostUSDMicros)
}
