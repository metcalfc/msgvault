package store

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/personenrichment"
	"go.kenn.io/msgvault/internal/personfacts"
)

type enrichmentResultFixture struct {
	store   *Store
	person  *Person
	profile personenrichment.ProviderProfile
	lease   *personenrichment.WorkLease
	attempt *personenrichment.DurableAttempt
	result  personenrichment.Result
	commit  personenrichment.ClaimCommit
	now     time.Time
}

type enrichmentClaimResult struct {
	outcome *personenrichment.ClaimOutcome
	err     error
}

func newEnrichmentResultFixture(t *testing.T) *enrichmentResultFixture {
	t.Helper()
	st, personID, targets := newPersonFactProjectionStore(t)
	_, err := st.AddPersonContactPointContext(t.Context(), personID, PersonContactPointInput{
		AddressKind: ContactAddressURL, OriginalValue: "https://profiles.example.test/result-person",
		Envelope: ValueEnvelopeInput{Source: ProvenanceUser},
	})
	require.NoError(t, err)
	person, err := st.GetPersonContext(t.Context(), personID)
	require.NoError(t, err)
	selected := []personfacts.TargetDescriptor{
		targets[AttributeSlugPrimaryChannel],
		targets[AttributeSlugAskMeAbout],
	}
	profile, err := (personenrichment.ProviderConfig{
		Name: "exa-results", Kind: personenrichment.ProviderExa, Enabled: true,
		Endpoint: "https://api.example.test/search", APIKeyEnv: "PROVIDER_API_KEY",
		Mode: "deep", NumResults: 1,
		AllowedIdentifiers: []personenrichment.IdentifierClass{
			personenrichment.IdentifierEmail, personenrichment.IdentifierPublicProfileURL,
		},
		TargetKeys:       []string{selected[0].Key, selected[1].Key},
		RetentionPosture: "zero_retention", TrainingPosture: "no_training",
		RefreshInterval: 24 * time.Hour, RequestTimeout: time.Minute,
		PollInterval: 30 * time.Second, MaxJobAge: 15 * time.Minute, MaxRetries: 5,
		MaxRequestsPerRun: 10, MaxRequestsPerDay: 100,
	}).Profile(personfacts.Catalog{Version: "fixture-v1", Targets: selected})
	require.NoError(t, err)
	_, err = st.EnsurePersonEnrichmentProfile(t.Context(), profile)
	require.NoError(t, err)
	_, _, err = st.GrantPersonEnrichmentConsent(t.Context(), profile.Fingerprint, "test")
	require.NoError(t, err)

	now := time.Date(2026, 8, 23, 12, 0, 0, 123000000, time.UTC)
	SetPersonEnrichmentClockForTest(st, func() time.Time { return now })
	run, created, err := st.StartRun(t.Context(), personenrichment.RunStart{
		Kind: "manual", RequestedBy: "result-fixture", RequestedAt: now,
	})
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, st.PutPersonEnrichmentWorkContext(t.Context(), PersonEnrichmentWorkInput{
		PersonID: person.ID, ProfileFingerprint: profile.Fingerprint,
		Trigger: personenrichment.Trigger{Kind: personenrichment.TriggerManual, Generation: "manual:result-fixture"},
		DueAt:   now,
	}))
	lease, err := st.ClaimWork(t.Context(), personenrichment.ClaimOptions{
		RunID: run.ID, Owner: "result-worker", ProviderName: profile.Name,
		Now: now, LeaseDuration: 5 * time.Minute,
	})
	require.NoError(t, err)
	require.NotNil(t, lease)
	attempt, created, err := st.BeginAttempt(t.Context(), lease.Token, personenrichment.AttemptStart{
		RunID: run.ID, PersonID: person.ID, ProfileFingerprint: profile.Fingerprint,
		PayloadHash: strings.Repeat("1", 64), RequestHash: strings.Repeat("2", 64),
		PersonRevision: person.Revision, Trigger: lease.Trigger,
	})
	require.NoError(t, err)
	require.True(t, created)

	schemaHash := strings.Repeat("a", 64)
	result := personenrichment.Result{
		State: personenrichment.ResultComplete, RequestID: " opaque-request\t",
		JobID: "opaque/job:Case?part=1", FreshAsOf: now.Add(-time.Hour),
		AdapterVersion: "exa-adapter-fixture-v1", SchemaVersion: "exa-wire-fixture-v1",
		ProviderVersion: "provider-fixture-v1", Model: "fixture-model", ModelVersion: "fixture-model-v1",
		GeneratedSchema: true, GeneratedSchemaHash: schemaHash,
		ProviderPersonIDs: []personenrichment.ProviderPersonID{{
			ID: "Opaque/Person:Case?part=1", Confidence: 975,
		}},
		CanonicalPublicURLs: []string{"https://profiles.example.test/alice%2Fstable"},
		Citations: []personenrichment.Citation{{
			Key: "citation-profile", URL: "https://sources.example.test/profile/alice",
			Title: "Synthetic public profile", Publisher: "Example Publisher",
			Excerpt:     "Synthetic public evidence shared by two claims.",
			PublishedAt: now.Add(-48 * time.Hour), RetrievedAt: now.Add(-time.Hour),
		}},
		SourceAttempts: []personenrichment.SourceAttempt{
			{URL: "https://sources.example.test/profile/alice", Outcome: "cited", ObservedAt: now.Add(-time.Hour)},
			{URL: "https://directory.example.test/visited", Outcome: "visited", ObservedAt: now.Add(-2 * time.Hour)},
		},
		Cost: personenrichment.Cost{Currency: "USD", AmountMicros: 600},
		Claims: []personfacts.ProposedClaim{
			enrichmentPublicClaim(selected[0], `"chat"`, 930),
			enrichmentPublicClaim(selected[1], `"sailing"`, 870),
		},
	}
	programFingerprint, err := personenrichment.ProgramFingerprint(personenrichment.ProgramDescriptor{
		HostMappingVersion: personenrichment.HostClaimMappingVersion,
		AdapterVersion:     result.AdapterVersion, WireSchemaVersion: result.SchemaVersion,
		GeneratedSchema: true, GeneratedSchemaHash: schemaHash,
	})
	require.NoError(t, err)
	require.NoError(t, st.AuthorizeAttemptDispatch(t.Context(), attempt.Token))
	require.NoError(t, st.RecordProviderStarted(t.Context(), attempt.Token, personenrichment.Attempt{
		State: personenrichment.AttemptPending, RequestID: result.RequestID, JobID: result.JobID,
		StartedAt:      now,
		AdapterVersion: result.AdapterVersion, SchemaVersion: result.SchemaVersion,
		GeneratedSchema: true, GeneratedSchemaHash: schemaHash, Targets: selected,
		ProgramFingerprint: programFingerprint,
	}))
	hasher, err := personenrichment.NewSuppressionHasher(bytes.Repeat([]byte{0x6a}, 32))
	require.NoError(t, err)
	commit, err := personenrichment.NewClaimCommit(personenrichment.ClaimCommitInput{
		AttemptID: attempt.ID, RunID: run.ID, PersonID: person.ID,
		LeaseFence: attempt.Token.Fence, ProfileFingerprint: profile.Fingerprint,
		ProviderNamespace: profile.ProviderNamespace, RequestHash: strings.Repeat("2", 64),
		IdentityAssessment: personenrichment.IdentityAssessment{
			Accepted: true, Score: 1000, Reason: "strong_identifier_match",
			MatchedClasses: []personenrichment.IdentifierClass{personenrichment.IdentifierEmail},
		},
	}, result, hasher)
	require.NoError(t, err)
	return &enrichmentResultFixture{
		store: st, person: person, profile: profile, lease: lease,
		attempt: attempt, result: result, commit: commit, now: now,
	}
}

func TestPersonEnrichmentSynchronousResultCommitsFromStartingAttempt(t *testing.T) {
	requirements := require.New(t)
	checks := assert.New(t)
	f := newEnrichmentResultFixture(t)
	_, err := f.store.db.ExecContext(t.Context(), `UPDATE person_enrichment_attempts
		SET state = 'starting', provider_request_id = NULL, provider_job_id = NULL,
		    adapter_version = NULL, schema_version = NULL, generated_schema = FALSE,
		    generated_schema_hash = NULL, targets_json = NULL, program_fingerprint = NULL,
		    provider_started_at = NULL, dispatch_authorized_at = ?
		WHERE id = ?`, f.now, f.attempt.ID)
	requirements.NoError(err)

	outcome, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
	requirements.NoError(err)
	checks.Equal(personenrichment.ClaimApplied, outcome.Status)
	stored, err := f.store.GetPersonEnrichmentAttemptContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	checks.Equal("succeeded", stored.State)
}

func enrichmentPublicClaim(
	target personfacts.TargetDescriptor, submitted string, reportedScore int,
) personfacts.ProposedClaim {
	return personfacts.ProposedClaim{
		Target: target, Relation: personfacts.RelationSupport,
		SubmittedValue: json.RawMessage(submitted), Origin: personfacts.OriginEnrichment,
		Confidence: personfacts.ConfidenceInputs{ReportedScore: reportedScore},
		Evidence: []personfacts.EvidenceInput{{
			SourceClass: personfacts.EvidencePublic, SourceRef: "citation-profile",
			Directness: personfacts.DirectOther, Authority: personfacts.AuthorityAuthoritative,
		}},
	}
}

func enrichmentProviderAssertionClaim(
	target personfacts.TargetDescriptor, submitted, sourceRef string, reportedScore int,
) personfacts.ProposedClaim {
	return personfacts.ProposedClaim{
		Target: target, Relation: personfacts.RelationSupport,
		SubmittedValue: json.RawMessage(submitted), Origin: personfacts.OriginEnrichment,
		Confidence: personfacts.ConfidenceInputs{ReportedScore: reportedScore},
		Evidence: []personfacts.EvidenceInput{{
			SourceClass: personfacts.EvidenceProviderAssertion, SourceRef: sourceRef,
			Directness: personfacts.Indirect, Authority: personfacts.AuthorityAggregator,
			Excerpt: "Synthetic provider assertion.",
		}},
	}
}

func (f *enrichmentResultFixture) reseal(t *testing.T) {
	t.Helper()
	hasher, err := personenrichment.NewSuppressionHasher(bytes.Repeat([]byte{0x6a}, 32))
	require.NoError(t, err)
	f.commit, err = personenrichment.NewClaimCommit(personenrichment.ClaimCommitInput{
		AttemptID: f.attempt.ID, RunID: f.attempt.RunID, PersonID: f.person.ID,
		LeaseFence: f.attempt.Token.Fence, ProfileFingerprint: f.profile.Fingerprint,
		ProviderNamespace: f.profile.ProviderNamespace, RequestHash: f.attempt.RequestHash,
		IdentityAssessment: f.commit.IdentityAssessment,
	}, f.result, hasher)
	require.NoError(t, err)
}

func TestCommitEnrichmentClaimsAppliesAtomicallyAndReplaysRichResult(t *testing.T) {
	checks := assert.New(t)
	requirements := require.New(t)
	f := newEnrichmentResultFixture(t)
	first, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
	requirements.NoError(err)
	requirements.Equal(personenrichment.ClaimApplied, first.Status)
	requirements.NotNil(first.Generation)
	requirements.Len(first.Generation.Resolutions, 2)

	second, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
	requirements.NoError(err)
	requirements.Equal(personenrichment.ClaimApplied, second.Status)
	requirements.NotNil(second.Generation)
	checks.Equal(first.Generation.GenerationID, second.Generation.GenerationID)
	checks.Equal(first.Generation.GenerationKey, second.Generation.GenerationKey)
	checks.Equal(first.Generation.Resolutions, second.Generation.Resolutions)

	claims, err := f.store.ListPersonFactClaimsContext(t.Context(), f.person.ID, personfacts.ClaimFilter{})
	requirements.NoError(err)
	checks.Len(claims, 2)
	decisions, err := f.store.ListPersonFactDecisionsContext(t.Context(), f.person.ID, personfacts.DecisionFilter{})
	requirements.NoError(err)
	checks.Len(decisions, 2)
	for _, decision := range decisions {
		checks.Equal(personfacts.DecisionApplied, decision.Action)
	}
	citations, err := f.store.ListPersonEnrichmentAttemptCitationsContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	requirements.Len(citations, 1)
	checks.Equal("citation-profile", citations[0].CitationKey)
	checks.Equal("https://sources.example.test/profile/alice", citations[0].CanonicalURL)

	identities, err := f.store.LoadProviderPersonIDs(t.Context(), f.person.ID, f.profile.ProviderNamespace)
	requirements.NoError(err)
	checks.Equal([]personenrichment.ProviderPersonID{{ID: "Opaque/Person:Case?part=1", Confidence: 975}}, identities)
	attempt, err := f.store.GetPersonEnrichmentAttemptContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	checks.Equal("succeeded", attempt.State)
	requirements.NotNil(attempt.CompletedAt)
	checks.Equal(f.now, *attempt.CompletedAt)
	requirements.NotNil(attempt.FactGenerationKey)
	checks.Equal(first.Generation.GenerationKey, *attempt.FactGenerationKey)
	work, err := f.store.ListPersonEnrichmentWorkContext(t.Context(), PersonEnrichmentWorkFilter{
		PersonID: f.person.ID, ProfileFingerprint: f.profile.Fingerprint, Limit: 10,
	})
	requirements.NoError(err)
	requirements.Len(work, 1)
	checks.Nil(work[0].RunID)
	checks.Nil(work[0].ActiveAttemptID)
	checks.Equal(f.now.Add(f.profile.RefreshInterval), work[0].DueAt)
}

func TestPersonEnrichmentResultRejectsConcurrentConfiguredKeyWinner(t *testing.T) {
	for _, winner := range []string{"suppression", "deletion"} {
		t.Run(winner, func(t *testing.T) {
			checks := assert.New(t)
			requirements := require.New(t)
			f := newEnrichmentResultFixture(t)
			newHasher, err := personenrichment.NewSuppressionHasher(bytes.Repeat([]byte{0x4e}, 32))
			requirements.NoError(err)
			newDigest := newHasher.Digest(f.profile.ProviderNamespace,
				personenrichment.SuppressionEmail, personenrichment.EmailNormalizationV1,
				"configured-result-winner@example.test")
			newKeyID, err := newHasher.KeyID()
			requirements.NoError(err)
			input := PersonEnrichmentSuppressionInput{
				ProviderNamespace: newDigest.ProviderNamespace, IdentifierClass: newDigest.IdentifierClass,
				NormalizationVersion: newDigest.NormalizationVersion, KeyID: newDigest.KeyID,
				Digest: newDigest.Digest, Reason: PersonEnrichmentSuppressionDeletion,
				Actor: "privacy-test",
			}
			var deletePerson *Person
			if winner == "deletion" {
				participantID, createErr := f.store.EnsureParticipant(
					"delete-result-winner@example.test", "Delete Result Winner", "example.test")
				requirements.NoError(createErr)
				deletePerson, _, createErr = f.store.CreatePersonFromParticipantContext(
					t.Context(), participantID)
				requirements.NoError(createErr)
			}

			resultReached := make(chan struct{})
			releaseResult := make(chan struct{})
			SetPersonEnrichmentTxBarrierForTest(f.store, func(phase string) {
				if phase == "result_before_authority_lock" {
					close(resultReached)
					<-releaseResult
				}
			})
			type commitResult struct {
				outcome *personenrichment.ClaimOutcome
				err     error
			}
			result := make(chan commitResult, 1)
			go func() {
				outcome, commitErr := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
				result <- commitResult{outcome: outcome, err: commitErr}
			}()
			requireEnrichmentResultSignal(t, resultReached,
				"result did not reach its authority gate")
			if winner == "suppression" {
				requirements.NoError(f.store.InsertPersonEnrichmentSuppressionsForConfiguredKeyContext(
					t.Context(), newKeyID, []PersonEnrichmentSuppressionInput{input}))
			} else {
				requirements.NoError(f.store.DeletePersonWithEnrichmentSuppressionsContext(
					t.Context(), DeletePersonEnrichmentInput{
						PersonID: deletePerson.ID, ExpectedRevision: deletePerson.Revision,
						ConfiguredKeyID: newKeyID, Actor: "privacy-test",
						Reason:             PersonEnrichmentSuppressionDeletion,
						CurrentIdentifiers: []PersonEnrichmentSuppressionInput{input},
					}))
			}
			close(releaseResult)
			got := <-result
			requirements.ErrorIs(got.err, personenrichment.ErrSuppressionKeyMismatch)
			checks.Nil(got.outcome)
			assertNoEnrichmentResultSideEffects(t, f, true)
			attempt, getErr := f.store.GetPersonEnrichmentAttemptContext(t.Context(), f.attempt.ID)
			requirements.NoError(getErr)
			checks.Equal("pending", attempt.State)
			if winner == "deletion" {
				_, getErr = f.store.GetPersonContext(t.Context(), deletePerson.ID)
				checks.ErrorIs(getErr, ErrPersonNotFound)
			}
		})
	}
}

func TestPersonEnrichmentResultDeletionRevocationLockHierarchyDoesNotDeadlock(t *testing.T) {
	checks := assert.New(t)
	requirements := require.New(t)
	f := newEnrichmentResultFixture(t)
	hasher, err := personenrichment.NewSuppressionHasher(bytes.Repeat([]byte{0x6a}, 32))
	requirements.NoError(err)
	keyID, err := hasher.KeyID()
	requirements.NoError(err)
	digest := hasher.Digest(f.profile.ProviderNamespace,
		personenrichment.SuppressionEmail, personenrichment.EmailNormalizationV1,
		"delete-lock-hierarchy@example.test")
	participantID, err := f.store.EnsureParticipant(
		"delete-lock-hierarchy@example.test", "Delete Lock Hierarchy", "example.test")
	requirements.NoError(err)
	deletePerson, _, err := f.store.CreatePersonFromParticipantContext(t.Context(), participantID)
	requirements.NoError(err)

	resultLocked := make(chan struct{})
	releaseResult := make(chan struct{})
	var resultOnce sync.Once
	SetPersonEnrichmentTxBarrierForTest(f.store, func(phase string) {
		if phase == "result_person_locked" {
			resultOnce.Do(func() {
				close(resultLocked)
				<-releaseResult
			})
		}
	})
	claimDone := make(chan enrichmentClaimResult, 1)
	go func() {
		outcome, commitErr := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
		claimDone <- enrichmentClaimResult{outcome: outcome, err: commitErr}
	}()
	requireEnrichmentResultSignal(t, resultLocked, "result did not acquire its person-first gate")
	revokeDone := make(chan error, 1)
	revokeStarted := make(chan struct{})
	go func() {
		close(revokeStarted)
		_, revokeErr := f.store.RevokePersonEnrichmentConsent(
			t.Context(), f.profile.Fingerprint, "privacy-test")
		revokeDone <- revokeErr
	}()
	requireEnrichmentResultSignal(t, revokeStarted, "revocation did not start")
	deleteDone := make(chan error, 1)
	deleteStarted := make(chan struct{})
	go func() {
		close(deleteStarted)
		deleteDone <- f.store.DeletePersonWithEnrichmentSuppressionsContext(
			t.Context(), DeletePersonEnrichmentInput{
				PersonID: deletePerson.ID, ExpectedRevision: deletePerson.Revision,
				ConfiguredKeyID: keyID, Actor: "privacy-test",
				Reason: PersonEnrichmentSuppressionDeletion,
				CurrentIdentifiers: []PersonEnrichmentSuppressionInput{{
					ProviderNamespace: digest.ProviderNamespace,
					IdentifierClass:   digest.IdentifierClass, NormalizationVersion: digest.NormalizationVersion,
					KeyID: digest.KeyID, Digest: digest.Digest,
					Reason: PersonEnrichmentSuppressionDeletion, Actor: "privacy-test",
				}},
			})
	}()
	requireEnrichmentResultSignal(t, deleteStarted, "deletion did not start")
	close(releaseResult)
	claim := requireEnrichmentClaimResult(t, claimDone)
	requirements.NoError(claim.err)
	requirements.NotNil(claim.outcome)
	checks.Equal(personenrichment.ClaimApplied, claim.outcome.Status)
	requirements.NoError(requireEnrichmentErrorResult(t, revokeDone, "revocation deadlocked"))
	requirements.NoError(requireEnrichmentErrorResult(t, deleteDone, "deletion deadlocked"))
	_, err = f.store.GetPersonContext(t.Context(), deletePerson.ID)
	checks.ErrorIs(err, ErrPersonNotFound)
}

func requireEnrichmentClaimResult(
	t *testing.T, result <-chan enrichmentClaimResult,
) enrichmentClaimResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(5 * time.Second):
		require.FailNow(t, "person enrichment result deadlocked")
		return enrichmentClaimResult{}
	}
}

func requireEnrichmentErrorResult(t *testing.T, result <-chan error, message string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		require.FailNow(t, message)
		return nil
	}
}

func requireEnrichmentResultSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		require.FailNow(t, message)
	}
}

func TestPersonEnrichmentResultPreparationHasNoDurableSideEffects(t *testing.T) {
	checks := assert.New(t)
	requirements := require.New(t)
	f := newEnrichmentResultFixture(t)
	prepared, err := f.store.preparePersonEnrichmentCommit(t.Context(), f.commit)
	requirements.NoError(err)
	requirements.NotNil(prepared)
	checks.Equal(int64(0), enrichmentTableCount(t, f.store, "person_enrichment_citations"))
	checks.Equal(int64(0), enrichmentTableCount(t, f.store, "person_fact_generations"))
	attempt, err := f.store.GetPersonEnrichmentAttemptContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	checks.Equal("pending", attempt.State)
}

func TestCommitEnrichmentClaimsRollsBackCitationAndProjectionFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		trigger string
	}{
		{name: "after citation insertion", trigger: `
			CREATE TRIGGER fail_enrichment_citation AFTER INSERT ON person_enrichment_citations
			BEGIN SELECT RAISE(FAIL, 'injected citation failure'); END`},
		{name: "during projection", trigger: `
			CREATE TRIGGER fail_enrichment_projection BEFORE INSERT ON person_attribute_values
			BEGIN SELECT RAISE(FAIL, 'injected projection failure'); END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newEnrichmentResultFixture(t)

			_, err := f.store.DB().ExecContext(t.Context(), test.trigger)
			require.NoError(t, err)
			_, err = f.store.CommitEnrichmentClaims(t.Context(), f.commit)
			require.Error(t, err)
			assertNoEnrichmentResultSideEffects(t, f, true)
		})
	}
}

func TestCommitEnrichmentClaimsRejectsStaleFenceWithoutWrites(t *testing.T) {
	f := newEnrichmentResultFixture(t)
	_, err := f.store.DB().ExecContext(t.Context(), f.store.Rebind(
		`UPDATE person_enrichment_attempts SET lease_fence = lease_fence + 1 WHERE id = ?`), f.attempt.ID)
	require.NoError(t, err)
	_, err = f.store.CommitEnrichmentClaims(t.Context(), f.commit)
	require.ErrorIs(t, err, ErrStaleLease)
	assertNoEnrichmentResultSideEffects(t, f, true)
}

func TestPersonEnrichmentResultRejectsEnvelopeChangesAfterPreparation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *enrichmentResultFixture)
	}{
		{name: "request", mutate: func(t *testing.T, f *enrichmentResultFixture) {
			t.Helper()
			_, err := f.store.DB().ExecContext(t.Context(), f.store.Rebind(
				`UPDATE person_enrichment_attempts SET request_hash = ? WHERE id = ?`),
				strings.Repeat("3", 64), f.attempt.ID)
			require.NoError(t, err)
		}},
		{name: "profile", mutate: func(t *testing.T, f *enrichmentResultFixture) {
			t.Helper()
			_, err := f.store.DB().ExecContext(t.Context(), f.store.Rebind(
				`UPDATE person_enrichment_profiles SET endpoint = ? WHERE fingerprint = ?`),
				"https://changed.example.test/search", f.profile.Fingerprint)
			require.NoError(t, err)
		}},
		{name: "catalog", mutate: func(t *testing.T, f *enrichmentResultFixture) {
			t.Helper()
			description := "Changed inference semantics"
			definition, err := f.store.GetAttributeDefinitionBySlugContext(
				t.Context(), AttributeObjectPerson, AttributeSlugPrimaryChannel)
			require.NoError(t, err)
			descriptionPointer := &description
			_, err = f.store.UpdateAttributeDefinitionContext(t.Context(), definition.ID, definition.Revision,
				AttributeDefinitionUpdate{Description: &descriptionPointer})
			require.NoError(t, err)
		}},
		{name: "person revision", mutate: func(t *testing.T, f *enrichmentResultFixture) {
			t.Helper()
			_, err := f.store.DB().ExecContext(t.Context(), f.store.Rebind(
				`UPDATE persons SET revision = revision + 1 WHERE id = ?`), f.person.ID)
			require.NoError(t, err)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newEnrichmentResultFixture(t)
			prepared, err := f.store.preparePersonEnrichmentCommit(t.Context(), f.commit)
			require.NoError(t, err)
			test.mutate(t, f)
			_, err = f.store.commitPreparedPersonEnrichmentResult(t.Context(), prepared)
			require.Error(t, err)
			assertNoEnrichmentResultSideEffects(t, f, true)
		})
	}
}

func TestPersonEnrichmentResultSensitivePostureChangeAfterPreparationIsPolicyTerminal(t *testing.T) {
	checks := assert.New(t)
	requirements := require.New(t)
	f := newEnrichmentResultFixture(t)
	prepared, err := f.store.preparePersonEnrichmentCommit(t.Context(), f.commit)
	requirements.NoError(err)
	definition, err := f.store.GetAttributeDefinitionBySlugContext(
		t.Context(), AttributeObjectPerson, AttributeSlugPrimaryChannel)
	requirements.NoError(err)
	_, err = f.store.DB().ExecContext(t.Context(), f.store.Rebind(
		`UPDATE attribute_definitions SET is_sensitive = ?, revision = revision + 1 WHERE id = ?`),
		true, definition.ID)
	requirements.NoError(err)
	outcome, err := f.store.commitPreparedPersonEnrichmentResult(t.Context(), prepared)
	requirements.NoError(err)
	checks.Equal(personenrichment.ClaimPolicyRejected, outcome.Status)
	checks.Nil(outcome.Generation)
	assertNoEnrichmentResultSideEffects(t, f, false)
	attempt, err := f.store.GetPersonEnrichmentAttemptContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	checks.Equal("terminal", attempt.State)
	checks.Equal(int64(600), requireAttemptActualCost(t, attempt))
}

func TestPersonEnrichmentResultUsesOnePreparedCompletionTimestamp(t *testing.T) {
	checks := assert.New(t)
	requirements := require.New(t)
	f := newEnrichmentResultFixture(t)
	prepared, err := f.store.preparePersonEnrichmentCommit(t.Context(), f.commit)
	requirements.NoError(err)
	SetPersonEnrichmentClockForTest(f.store, func() time.Time { return f.now.Add(9 * time.Hour) })
	outcome, err := f.store.commitPreparedPersonEnrichmentResult(t.Context(), prepared)
	requirements.NoError(err)
	checks.Equal(personenrichment.ClaimApplied, outcome.Status)
	attempt, err := f.store.GetPersonEnrichmentAttemptContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	requirements.NotNil(attempt.CompletedAt)
	checks.Equal(f.now, *attempt.CompletedAt)
	work, err := f.store.ListPersonEnrichmentWorkContext(t.Context(), PersonEnrichmentWorkFilter{
		PersonID: f.person.ID, ProfileFingerprint: f.profile.Fingerprint, Limit: 10,
	})
	requirements.NoError(err)
	requirements.Len(work, 1)
	checks.Equal(f.now.Add(f.profile.RefreshInterval), work[0].DueAt)
}

func TestPersonEnrichmentResultDeduplicatesMetadataAndPreservesOpaqueIDs(t *testing.T) {
	checks := assert.New(t)
	requirements := require.New(t)
	f := newEnrichmentResultFixture(t)
	f.result.Citations = append(f.result.Citations, f.result.Citations[0])
	f.result.SourceAttempts = append(f.result.SourceAttempts, f.result.SourceAttempts...)
	f.result.ProviderPersonIDs = []personenrichment.ProviderPersonID{{
		ID: "Opaque ID/Not-A-URL:MiXeD?x=1#fragment", Confidence: 901,
	}}
	f.reseal(t)
	outcome, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
	requirements.NoError(err)
	checks.Equal(personenrichment.ClaimApplied, outcome.Status)
	checks.Equal(int64(1), enrichmentTableCount(t, f.store, "person_enrichment_citations"))
	checks.Equal(int64(1), enrichmentTableCount(t, f.store, "person_enrichment_attempt_citations"))
	checks.Equal(int64(2), enrichmentTableCount(t, f.store, "person_enrichment_attempt_sources"))
	identities, err := f.store.LoadProviderPersonIDs(t.Context(), f.person.ID, f.profile.ProviderNamespace)
	requirements.NoError(err)
	checks.Equal([]personenrichment.ProviderPersonID{{ID: "Opaque ID/Not-A-URL:MiXeD?x=1#fragment", Confidence: 901}}, identities)
}

func TestCommitEnrichmentClaimsReusesCitationAcrossAttempts(t *testing.T) {
	checks := assert.New(t)
	requirements := require.New(t)
	f := newEnrichmentResultFixture(t)
	firstAttemptID := f.attempt.ID
	firstRetrievedAt := f.result.Citations[0].RetrievedAt
	_, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
	requirements.NoError(err)
	firstCitations, err := f.store.ListPersonEnrichmentAttemptCitationsContext(t.Context(), firstAttemptID)
	requirements.NoError(err)
	requirements.Len(firstCitations, 1)

	now := f.now.Add(f.profile.RefreshInterval)
	SetPersonEnrichmentClockForTest(f.store, func() time.Time { return now })
	run, _, err := f.store.StartRun(t.Context(), personenrichment.RunStart{
		Kind: "scheduled", RequestedBy: "citation-refresh", RequestedAt: now,
	})
	requirements.NoError(err)
	lease, err := f.store.ClaimWork(t.Context(), personenrichment.ClaimOptions{
		RunID: run.ID, Owner: "result-worker", ProviderName: f.profile.Name,
		Now: now, LeaseDuration: 5 * time.Minute,
	})
	requirements.NoError(err)
	requirements.NotNil(lease)
	person, err := f.store.GetPersonContext(t.Context(), f.person.ID)
	requirements.NoError(err)
	f.attempt, _, err = f.store.BeginAttempt(t.Context(), lease.Token, personenrichment.AttemptStart{
		RunID: run.ID, PersonID: person.ID, ProfileFingerprint: f.profile.Fingerprint,
		PayloadHash: strings.Repeat("1", 64), RequestHash: strings.Repeat("3", 64),
		PersonRevision: person.Revision, Trigger: lease.Trigger,
	})
	requirements.NoError(err)
	requirements.NoError(f.store.AuthorizeAttemptDispatch(t.Context(), f.attempt.Token))
	f.result.RequestID = "refresh-request"
	f.result.JobID = "refresh-job"
	f.result.Citations[0].RetrievedAt = now
	f.result.SourceAttempts[0].ObservedAt = now
	f.reseal(t)

	outcome, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
	requirements.NoError(err)
	checks.Equal(personenrichment.ClaimApplied, outcome.Status)
	attempt, err := f.store.GetPersonEnrichmentAttemptContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	checks.Equal("succeeded", attempt.State)
	checks.Nil(attempt.LeaseUntil)
	secondCitations, err := f.store.ListPersonEnrichmentAttemptCitationsContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	checks.Equal(firstCitations, secondCitations, "reuse the citation and preserve its first retrieval time")
	checks.Equal(int64(1), enrichmentTableCount(t, f.store, "person_enrichment_citations"))
	checks.Equal(int64(2), enrichmentTableCount(t, f.store, "person_enrichment_attempt_citations"))

	evidence, err := f.store.ListPersonFactEvidenceContext(t.Context(), person.ID, personfacts.EvidenceFilter{})
	requirements.NoError(err)
	recordedTimes := make([]time.Time, 0, len(evidence))
	for _, item := range evidence {
		recordedTimes = append(recordedTimes, item.Input.RecordedTime)
	}
	checks.ElementsMatch([]time.Time{firstRetrievedAt, now}, recordedTimes)
}

func TestCommitEnrichmentClaimsRejectsChangedCitationMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*personenrichment.Citation)
	}{
		{"URL", func(c *personenrichment.Citation) { c.URL = "https://sources.example.test/other" }},
		{"title", func(c *personenrichment.Citation) { c.Title = "Different title" }},
		{"publisher", func(c *personenrichment.Citation) { c.Publisher = "Different publisher" }},
		{"excerpt", func(c *personenrichment.Citation) { c.Excerpt = "Different excerpt" }},
		{"published time", func(c *personenrichment.Citation) { c.PublishedAt = c.PublishedAt.Add(time.Hour) }},
		{"missing published time", func(c *personenrichment.Citation) { c.PublishedAt = time.Time{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := assert.New(t)
			requirements := require.New(t)
			f := newEnrichmentResultFixture(t)
			citation := f.result.Citations[0]
			_, err := f.store.db.ExecContext(t.Context(), `INSERT INTO person_enrichment_citations
				(person_id, citation_key, canonical_url, title, publisher, excerpt, published_at, retrieved_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, f.person.ID, citation.Key, citation.URL,
				citation.Title, citation.Publisher, citation.Excerpt, citation.PublishedAt, citation.RetrievedAt)
			requirements.NoError(err)
			tc.change(&f.result.Citations[0])
			f.reseal(t)

			_, err = f.store.CommitEnrichmentClaims(t.Context(), f.commit)
			requirements.ErrorContains(err, "citation key has different immutable metadata")
			citations, err := f.store.ListPersonEnrichmentAttemptCitationsContext(t.Context(), f.attempt.ID)
			requirements.NoError(err)
			checks.Empty(citations)
		})
	}
}

func TestCommitEnrichmentClaimsRanksUnsupportedAggregatorEvidenceBelowThreshold(t *testing.T) {
	checks := assert.New(t)
	requirements := require.New(t)
	f := newEnrichmentResultFixture(t)
	f.result.Claims = []personfacts.ProposedClaim{enrichmentProviderAssertionClaim(
		f.profile.Targets[0], `"chat"`, "", 1000,
	)}
	f.result.Citations = nil
	f.reseal(t)
	outcome, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
	requirements.NoError(err)
	requirements.NotNil(outcome.Generation)
	requirements.Len(outcome.Generation.Decisions, 1)
	checks.Equal(personfacts.DecisionRetained, outcome.Generation.Decisions[0].Action)
	checks.Equal(personfacts.ReasonBelowThreshold, outcome.Generation.Decisions[0].Reason)
	checks.Empty(outcome.Generation.Projections)
	checks.Equal(int64(0), enrichmentTableCount(t, f.store, "person_enrichment_citations"))
	evidence, err := f.store.ListPersonFactEvidenceContext(t.Context(), f.person.ID,
		personfacts.EvidenceFilter{Limit: 10})
	requirements.NoError(err)
	requirements.Len(evidence, 1)
	checks.Equal("enrichment-attempt:"+strconv.FormatInt(f.attempt.ID, 10)+
		":job:"+f.result.JobID, evidence[0].Input.SourceRef)
	checks.Empty(evidence[0].Input.SourceURL)
}

func TestCommitEnrichmentClaimsRecordsMismatchedExternalEvidenceAsInvalid(t *testing.T) {
	tests := []struct {
		name   string
		claim  func(*enrichmentResultFixture) personfacts.ProposedClaim
		reason personfacts.DecisionReason
	}{
		{name: "forged provider assertion ref", claim: func(f *enrichmentResultFixture) personfacts.ProposedClaim {
			return enrichmentProviderAssertionClaim(
				f.profile.Targets[0], `"chat"`, "enrichment-attempt:999:job:forged", 1000)
		}, reason: personfacts.ReasonUnalignedEvidence},
		{name: "adapter supplied current-looking provider assertion ref", claim: func(f *enrichmentResultFixture) personfacts.ProposedClaim {
			return enrichmentProviderAssertionClaim(f.profile.Targets[0], `"chat"`,
				"enrichment-attempt:"+strconv.FormatInt(f.attempt.ID, 10)+":job:"+f.result.JobID, 1000)
		}, reason: personfacts.ReasonUnalignedEvidence},
		{name: "adapter supplied provider assertion URL", claim: func(f *enrichmentResultFixture) personfacts.ProposedClaim {
			claim := enrichmentProviderAssertionClaim(f.profile.Targets[0], `"chat"`, "", 1000)
			claim.Evidence[0].SourceURL = "https://provider.example.test/unsupported"
			return claim
		}, reason: personfacts.ReasonUnalignedEvidence},
		{name: "citation URL mismatch", claim: func(f *enrichmentResultFixture) personfacts.ProposedClaim {
			claim := enrichmentPublicClaim(f.profile.Targets[0], `"chat"`, 1000)
			claim.Evidence[0].SourceURL = "https://other.example.test/profile"
			return claim
		}, reason: personfacts.ReasonUnalignedEvidence},
		{name: "unknown citation key", claim: func(f *enrichmentResultFixture) personfacts.ProposedClaim {
			claim := enrichmentPublicClaim(f.profile.Targets[0], `"chat"`, 1000)
			claim.Evidence[0].SourceRef = "unknown-citation"
			claim.Evidence[0].SourceURL = "https://other.example.test/profile"
			return claim
		}, reason: personfacts.ReasonUnalignedEvidence},
		{name: "unsupported source class", claim: func(f *enrichmentResultFixture) personfacts.ProposedClaim {
			claim := enrichmentProviderAssertionClaim(f.profile.Targets[0], `"chat"`,
				"enrichment-attempt:"+strconv.FormatInt(f.attempt.ID, 10)+":job:"+f.result.JobID, 1000)
			claim.Evidence[0].SourceClass = personfacts.EvidenceSystem
			return claim
		}, reason: personfacts.ReasonMalformedValue},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checks := assert.New(t)
			requirements := require.New(t)
			f := newEnrichmentResultFixture(t)
			f.result.Claims = []personfacts.ProposedClaim{test.claim(f)}
			f.reseal(t)
			outcome, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
			requirements.NoError(err)
			checks.Equal(personenrichment.ClaimApplied, outcome.Status)
			requirements.NotNil(outcome.Generation)
			requirements.Len(outcome.Generation.Decisions, 1)
			checks.Equal(personfacts.DecisionInvalid, outcome.Generation.Decisions[0].Action)
			checks.Equal(test.reason, outcome.Generation.Decisions[0].Reason)
			checks.Empty(outcome.Generation.Projections)
		})
	}
}

func TestCommitEnrichmentClaimsWeakIdentityIsAuditableAndCannotProject(t *testing.T) {
	checks := assert.New(t)
	requirements := require.New(t)
	f := newEnrichmentResultFixture(t)
	f.commit.IdentityAssessment = personenrichment.IdentityAssessment{
		Accepted: false, Score: 0, Reason: "identity_not_verified",
	}
	f.result.Claims = []personfacts.ProposedClaim{
		enrichmentPublicClaim(f.profile.Targets[0], `"chat"`, 999),
		enrichmentProviderAssertionClaim(f.profile.Targets[1], `"provider-value"`, "", 998),
	}
	f.reseal(t)
	outcome, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
	requirements.NoError(err)
	checks.Equal(personenrichment.ClaimIdentityRejected, outcome.Status)
	requirements.NotNil(outcome.Generation)
	requirements.Len(outcome.Generation.Decisions, 2)
	for _, decision := range outcome.Generation.Decisions {
		checks.Equal(personfacts.DecisionIdentityRejected, decision.Action)
	}
	checks.Empty(outcome.Generation.Projections)
	evidence, err := f.store.ListPersonFactEvidenceContext(t.Context(), f.person.ID,
		personfacts.EvidenceFilter{Limit: 10})
	requirements.NoError(err)
	requirements.Len(evidence, 2)
	for _, item := range evidence {
		checks.Equal(0, item.Input.IdentityScore)
	}
	claims, err := f.store.ListPersonFactClaimsContext(t.Context(), f.person.ID,
		personfacts.ClaimFilter{Limit: 10})
	requirements.NoError(err)
	requirements.Len(claims, 2)
	checks.ElementsMatch([]int{998, 999}, []int{
		claims[0].Confidence.ReportedScore, claims[1].Confidence.ReportedScore,
	})
	attempt, err := f.store.GetPersonEnrichmentAttemptContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	checks.Equal("identity_rejected", attempt.State)
	checks.Equal(int64(0), enrichmentTableCount(t, f.store, "person_enrichment_attempt_identifiers"))
	checks.Equal(int64(0), enrichmentTableCount(t, f.store, "person_enrichment_citations"))
	checks.Equal(int64(0), enrichmentTableCount(t, f.store, "person_enrichment_attempt_sources"))
	assertNoRefreshWork(t, f)
}

func TestCommitEnrichmentClaimsLateReturnedIdentifierSuppressionIsPrivateAndTerminal(t *testing.T) {
	for _, class := range []personenrichment.SuppressionIdentifierClass{
		personenrichment.SuppressionProviderPersonID,
		personenrichment.SuppressionPublicProfileURL,
	} {
		t.Run(string(class), func(t *testing.T) {
			checks := assert.New(t)
			requirements := require.New(t)
			f := newEnrichmentResultFixture(t)
			digests, err := f.commit.VerifiedReturnedIdentifierDigests()
			requirements.NoError(err)
			index := slices.IndexFunc(digests, func(item personenrichment.SuppressionDigest) bool {
				return item.IdentifierClass == class
			})
			requirements.NotEqual(-1, index)
			digest := digests[index]
			requirements.NoError(f.store.InsertPersonEnrichmentSuppressionsContext(t.Context(),
				[]PersonEnrichmentSuppressionInput{{
					ProviderNamespace:    digest.ProviderNamespace,
					IdentifierClass:      digest.IdentifierClass,
					NormalizationVersion: digest.NormalizationVersion,
					KeyID:                digest.KeyID, Digest: digest.Digest,
					Reason: PersonEnrichmentSuppressionOptOut, Actor: "privacy-test",
				}}))
			outcome, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
			requirements.NoError(err)
			checks.Equal(personenrichment.ClaimSuppressed, outcome.Status)
			checks.Nil(outcome.Generation)
			assertNoEnrichmentResultSideEffects(t, f, false)
			attempt, err := f.store.GetPersonEnrichmentAttemptContext(t.Context(), f.attempt.ID)
			requirements.NoError(err)
			checks.Equal("suppressed", attempt.State)
			checks.Equal(int64(600), requireAttemptActualCost(t, attempt))
			assertNoRefreshWork(t, f)
		})
	}
}

func TestCommitEnrichmentClaimsProviderIdentityOwnedByAnotherPersonIsAuditable(t *testing.T) {
	checks := assert.New(t)
	requirements := require.New(t)
	f := newEnrichmentResultFixture(t)
	otherParticipant, err := f.store.EnsureParticipant("owner@example.com", "Other Owner", "example.com")
	requirements.NoError(err)
	other, _, err := f.store.CreatePersonFromParticipantContext(t.Context(), otherParticipant)
	requirements.NoError(err)
	_, err = f.store.DB().ExecContext(t.Context(), f.store.Rebind(`
		INSERT INTO person_enrichment_provider_identities
			(person_id, provider_namespace, provider_person_id, confidence, verified_at)
		VALUES (?, ?, ?, ?, ?)`), other.ID, f.profile.ProviderNamespace,
		f.result.ProviderPersonIDs[0].ID, 1000, f.now.Add(-time.Hour))
	requirements.NoError(err)

	outcome, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
	requirements.NoError(err)
	checks.Equal(personenrichment.ClaimIdentityRejected, outcome.Status)
	requirements.NotNil(outcome.Generation)
	for _, decision := range outcome.Generation.Decisions {
		checks.Equal(personfacts.DecisionIdentityRejected, decision.Action)
		checks.Equal(personfacts.ReasonIdentityMismatch, decision.Reason)
	}
	owned, err := f.store.LoadProviderPersonIDs(t.Context(), f.person.ID, f.profile.ProviderNamespace)
	requirements.NoError(err)
	checks.Empty(owned)
	checks.Empty(outcome.Generation.Projections)
	firstGeneration := outcome.Generation
	replayed, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
	requirements.NoError(err)
	checks.Equal(personenrichment.ClaimIdentityRejected, replayed.Status)
	requirements.NotNil(replayed.Generation)
	checks.Equal(firstGeneration.GenerationID, replayed.Generation.GenerationID)
	checks.Equal(firstGeneration.GenerationKey, replayed.Generation.GenerationKey)
	checks.Equal(int64(0), enrichmentTableCount(t, f.store, "person_enrichment_attempt_identifiers"))
	checks.Equal(int64(0), enrichmentTableCount(t, f.store, "person_enrichment_citations"))
	checks.Equal(int64(0), enrichmentTableCount(t, f.store, "person_enrichment_attempt_sources"))
	assertNoRefreshWork(t, f)
}

func TestPersonEnrichmentResultRevokedConsentAfterPreparationIsPolicyTerminal(t *testing.T) {
	checks := assert.New(t)
	requirements := require.New(t)
	f := newEnrichmentResultFixture(t)
	prepared, err := f.store.preparePersonEnrichmentCommit(t.Context(), f.commit)
	requirements.NoError(err)
	changed, err := f.store.RevokePersonEnrichmentConsent(t.Context(), f.profile.Fingerprint, "privacy-test")
	requirements.NoError(err)
	requirements.True(changed)
	outcome, err := f.store.commitPreparedPersonEnrichmentResult(t.Context(), prepared)
	requirements.NoError(err)
	checks.Equal(personenrichment.ClaimPolicyRejected, outcome.Status)
	checks.Nil(outcome.Generation)
	assertNoEnrichmentResultSideEffects(t, f, false)
	attempt, err := f.store.GetPersonEnrichmentAttemptContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	checks.Equal("terminal", attempt.State)
	requirements.NotNil(attempt.FailureClass)
	checks.Equal(string(personenrichment.FailurePolicy), *attempt.FailureClass)
	checks.Equal(int64(600), requireAttemptActualCost(t, attempt))
	assertNoRefreshWork(t, f)
}

func assertNoEnrichmentResultSideEffects(t *testing.T, f *enrichmentResultFixture, workMustRemain bool) {
	t.Helper()
	for _, table := range []string{
		"person_enrichment_attempt_identifiers", "person_enrichment_provider_identities",
		"person_enrichment_citations", "person_enrichment_attempt_citations",
		"person_enrichment_attempt_sources", "person_fact_generations",
		"person_fact_evidence", "person_fact_claims", "person_fact_decisions",
		"person_attribute_values",
	} {
		assert.Equal(t, int64(0), enrichmentTableCount(t, f.store, table), table)
	}
	work, err := f.store.ListPersonEnrichmentWorkContext(t.Context(), PersonEnrichmentWorkFilter{
		PersonID: f.person.ID, ProfileFingerprint: f.profile.Fingerprint, Limit: 10,
	})
	require.NoError(t, err)
	if workMustRemain {
		require.Len(t, work, 1)
		require.NotNil(t, work[0].ActiveAttemptID)
		assert.Equal(t, f.attempt.ID, *work[0].ActiveAttemptID)
	} else {
		assert.Empty(t, work)
	}
}

func assertNoRefreshWork(t *testing.T, f *enrichmentResultFixture) {
	t.Helper()
	work, err := f.store.ListPersonEnrichmentWorkContext(t.Context(), PersonEnrichmentWorkFilter{
		PersonID: f.person.ID, ProfileFingerprint: f.profile.Fingerprint, Limit: 10,
	})
	require.NoError(t, err)
	assert.Empty(t, work)
}

func enrichmentTableCount(t *testing.T, st *Store, table string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, st.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count))
	return count
}

func requireAttemptActualCost(t *testing.T, attempt *PersonEnrichmentAttempt) int64 {
	t.Helper()
	require.NotNil(t, attempt.ActualCostUSDMicros)
	return *attempt.ActualCostUSDMicros
}

func TestClaimsWithIdentityScoreCopiesEveryEvidenceAndRejectsEmptyClaims(t *testing.T) {
	checks := assert.New(t)
	requirements := require.New(t)
	target := personfacts.TargetDescriptor{
		Kind: personfacts.TargetAttribute, Key: "attribute:test", Revision: strings.Repeat("1", 64),
		UniversalID: "attribute:test", Slug: "test", Description: "Synthetic target",
		ValueType: personfacts.ValueText, Cardinality: personfacts.CardinalitySingle,
	}
	claims := []personfacts.ProposedClaim{{
		Target: target, Relation: personfacts.RelationSupport, SubmittedValue: json.RawMessage(`"value"`),
		Origin:   personfacts.OriginEnrichment,
		Evidence: []personfacts.EvidenceInput{{IdentityScore: 999}, {IdentityScore: 1}},
	}}
	got, err := claimsWithIdentityScore(claims, 400)
	requirements.NoError(err)
	requirements.Len(got, 1)
	checks.Equal([]int{400, 400}, []int{
		got[0].Evidence[0].IdentityScore, got[0].Evidence[1].IdentityScore,
	})
	got[0].Evidence[0].Excerpt = "changed"
	checks.Empty(claims[0].Evidence[0].Excerpt)
	_, err = claimsWithIdentityScore(claims, -1)
	requirements.Error(err)
	claims[0].Evidence = nil
	_, err = claimsWithIdentityScore(claims, 400)
	requirements.Error(err)
}

func TestExternalEvidenceAlignerRejectsMismatchedAndUnsafeEvidence(t *testing.T) {
	checks := assert.New(t)
	requirements := require.New(t)
	aligner := externalEvidenceAligner{
		AttemptID: 42, ProviderRequestID: "request/id", ProviderJobID: "job:id",
		CitationKeys: map[string]struct{}{"citation-profile": {}},
	}
	subject := int64(7)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	public := personfacts.EvidenceInput{
		PersonID: 7, SubjectPersonID: &subject, SourceClass: personfacts.EvidencePublic,
		SourceRef: "citation-profile", SourceURL: "https://example.test/profile",
		Directness: personfacts.DirectOther, Authority: personfacts.AuthorityAuthoritative,
		EventTime: now, RecordedTime: now,
	}
	accepted, err := aligner.Align(t.Context(), public)
	requirements.NoError(err)
	checks.True(accepted.Accepted)
	provider := public
	provider.SourceClass = personfacts.EvidenceProviderAssertion
	provider.SourceURL = ""
	provider.SourceRef = "enrichment-attempt:42:job:job:id"
	provider.Directness = personfacts.Indirect
	provider.Authority = personfacts.AuthorityAggregator
	accepted, err = aligner.Align(t.Context(), provider)
	requirements.NoError(err)
	checks.True(accepted.Accepted)

	for _, mutate := range []func(*personfacts.EvidenceInput){
		func(v *personfacts.EvidenceInput) { v.SourceRef = "missing" },
		func(v *personfacts.EvidenceInput) { v.SourceURL = "http://example.test/profile" },
		func(v *personfacts.EvidenceInput) { v.SourceClass = personfacts.EvidenceSystem },
	} {
		candidate := public
		mutate(&candidate)
		result, alignErr := aligner.Align(t.Context(), candidate)
		requirements.NoError(alignErr)
		checks.False(result.Accepted)
		requirements.NotNil(result.Failure)
		checks.Equal(personfacts.DecisionInvalid, result.Failure.Action)
	}
}

func TestPersonEnrichmentResultMetadataStructsDoNotExposeSecretsOrRawIdentifiers(t *testing.T) {
	citation := PersonEnrichmentCitation{}
	encoded, err := json.Marshal(citation)
	require.NoError(t, err)
	for _, forbidden := range []string{"credential", "suppression_key", "raw_identifier", "normalized_identifier"} {
		assert.NotContains(t, string(encoded), forbidden)
	}
}

// TestPersonEnrichmentResultCommitAndLeaseRenewalDoNotDeadlock pins the lock
// order between a result commit and the worker's concurrent lease renewal.
// The commit is held after it has locked the attempt row, and a renewal for the
// same attempt is started against it. Before the work row was locked ahead of
// the attempt row here, the renewal took the work row and then waited on the
// attempt row while the released commit waited on the work row, and PostgreSQL
// aborted one side with "deadlock detected". With the shared order the renewal
// simply waits for the commit; it then either renews or, because the commit
// settled the work row, reports a stale lease. Either is fine; a deadlock is
// not.
func TestPersonEnrichmentResultCommitAndLeaseRenewalDoNotDeadlock(t *testing.T) {
	checks := assert.New(t)
	requirements := require.New(t)
	f := newEnrichmentResultFixture(t)

	attemptLocked := make(chan struct{})
	releaseResult := make(chan struct{})
	var resultOnce sync.Once
	SetPersonEnrichmentTxBarrierForTest(f.store, func(phase string) {
		if phase == "result_attempt_locked" {
			resultOnce.Do(func() {
				close(attemptLocked)
				<-releaseResult
			})
		}
	})
	claimDone := make(chan enrichmentClaimResult, 1)
	go func() {
		outcome, commitErr := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
		claimDone <- enrichmentClaimResult{outcome: outcome, err: commitErr}
	}()
	requireEnrichmentResultSignal(t, attemptLocked, "result did not lock its attempt")

	renewDone := make(chan error, 1)
	go func() {
		renewDone <- f.store.RenewLease(t.Context(), f.attempt.Token, f.now.Add(time.Minute))
	}()
	// Give the renewal time to reach the row locks while the commit is held.
	// The test cannot observe a blocked statement directly; without this the
	// renewal may not have started before the commit is released, and the
	// interleaving under test never happens.
	select {
	case err := <-renewDone:
		requirements.FailNowf("lease renewal did not wait for the held commit", "%v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(releaseResult)

	claim := requireEnrichmentClaimResult(t, claimDone)
	requirements.NoError(claim.err)
	requirements.NotNil(claim.outcome)
	checks.Equal(personenrichment.ClaimApplied, claim.outcome.Status)
	renewErr := requireEnrichmentErrorResult(t, renewDone, "lease renewal deadlocked")
	if renewErr != nil {
		checks.ErrorIs(renewErr, ErrStaleLease)
	}
}
