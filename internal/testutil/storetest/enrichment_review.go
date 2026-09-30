package storetest

import (
	"bytes"
	"encoding/json/jsontext"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/personenrichment"
	"go.kenn.io/msgvault/internal/personfacts"
	"go.kenn.io/msgvault/internal/store"
)

// UncertainEnrichmentAttempt builds a tracked person whose single Exa
// enrichment attempt ended identity_uncertain, through the production
// claim commit path, and completes the run. It returns the person and
// attempt IDs. Names and addresses are synthetic.
func UncertainEnrichmentAttempt(t *testing.T, st *store.Store, email, name string) (int64, int64) {
	t.Helper()
	ctx := t.Context()
	participantID, err := st.EnsureParticipant(email, name, "example.test")
	require.NoError(t, err)
	person, _, err := st.CreatePersonFromParticipantContext(ctx, participantID)
	require.NoError(t, err)
	_, err = st.SetPersonTrackingContext(ctx, person.ID, true)
	require.NoError(t, err)
	person, err = st.GetPersonContext(ctx, person.ID)
	require.NoError(t, err)

	catalog, err := st.BuildPersonFactCatalogContext(ctx, true)
	require.NoError(t, err)
	var selected []personfacts.TargetDescriptor
	for _, slug := range []string{store.AttributeSlugPrimaryChannel, store.AttributeSlugAskMeAbout} {
		for _, target := range catalog.Targets {
			if target.Slug == slug {
				selected = append(selected, target)
			}
		}
	}
	require.Len(t, selected, 2)
	profile, err := (personenrichment.ProviderConfig{
		Name: "exa-review", Kind: personenrichment.ProviderExa, Enabled: true,
		Endpoint: "https://api.example.test/search", APIKeyEnv: "PROVIDER_API_KEY",
		Mode: "deep", NumResults: 1,
		AllowedIdentifiers: []personenrichment.IdentifierClass{personenrichment.IdentifierEmail},
		TargetKeys:         []string{selected[0].Key, selected[1].Key},
		RetentionPosture:   "zero_retention", TrainingPosture: "no_training",
		RefreshInterval: 24 * time.Hour, RequestTimeout: time.Minute,
		PollInterval: 30 * time.Second, MaxJobAge: 15 * time.Minute, MaxRetries: 5,
		MaxRequestsPerRun: 10, MaxRequestsPerDay: 100,
	}).Profile(personfacts.Catalog{Version: "review-fixture-v1", Targets: selected})
	require.NoError(t, err)
	_, err = st.EnsurePersonEnrichmentProfile(ctx, profile)
	require.NoError(t, err)
	_, _, err = st.GrantPersonEnrichmentConsent(ctx, profile.Fingerprint, "test")
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Millisecond)
	run, _, err := st.StartRun(ctx, personenrichment.RunStart{
		Kind: "manual", RequestedBy: fmt.Sprintf("review-fixture-%d", person.ID), RequestedAt: now,
	})
	require.NoError(t, err)
	require.NoError(t, st.PutPersonEnrichmentWorkContext(ctx, store.PersonEnrichmentWorkInput{
		PersonID: person.ID, ProfileFingerprint: profile.Fingerprint,
		Trigger: personenrichment.Trigger{Kind: personenrichment.TriggerManual, Generation: "manual:review"},
		DueAt:   now,
	}))
	lease, err := st.ClaimWork(ctx, personenrichment.ClaimOptions{
		RunID: run.ID, Owner: "review-worker", ProviderName: profile.Name,
		Now: now, LeaseDuration: 5 * time.Minute,
	})
	require.NoError(t, err)
	require.NotNil(t, lease)
	requestHash := strings.Repeat(fmt.Sprintf("%x", person.ID%16), 64)
	attempt, _, err := st.BeginAttempt(ctx, lease.Token, personenrichment.AttemptStart{
		RunID: run.ID, PersonID: person.ID, ProfileFingerprint: profile.Fingerprint,
		PayloadHash: strings.Repeat("1", 64), RequestHash: requestHash,
		PersonRevision: person.Revision, Trigger: lease.Trigger,
	})
	require.NoError(t, err)

	schemaHash := strings.Repeat("a", 64)
	claim := func(target personfacts.TargetDescriptor, value string) personfacts.ProposedClaim {
		return personfacts.ProposedClaim{
			Target: target, Relation: personfacts.RelationSupport,
			SubmittedValue: jsontext.Value(value), Origin: personfacts.OriginEnrichment,
			Confidence: personfacts.ConfidenceInputs{ReportedScore: 900},
			Evidence: []personfacts.EvidenceInput{{
				SourceClass: personfacts.EvidencePublic, SourceRef: "citation-profile",
				Directness: personfacts.DirectOther, Authority: personfacts.AuthorityAuthoritative,
			}},
		}
	}
	result := personenrichment.Result{
		State: personenrichment.ResultComplete, RequestID: "review-request",
		JobID: fmt.Sprintf("review-job-%d", person.ID), FreshAsOf: now.Add(-time.Hour),
		AdapterVersion: "exa-adapter-review-v1", SchemaVersion: "exa-wire-review-v1",
		ProviderVersion: "provider-review-v1", Model: "review-model", ModelVersion: "review-model-v1",
		GeneratedSchema: true, GeneratedSchemaHash: schemaHash,
		ProviderPersonIDs: []personenrichment.ProviderPersonID{{
			ID: fmt.Sprintf("review-person-%d", person.ID), Confidence: 700,
		}},
		CanonicalPublicURLs: []string{"https://profiles.example.test/review"},
		Citations: []personenrichment.Citation{{
			Key: "citation-profile", URL: "https://profiles.example.test/review",
			Title: "Synthetic public profile", Publisher: "Example Publisher",
			Excerpt: "Synthetic public evidence.", PublishedAt: now.Add(-48 * time.Hour),
			RetrievedAt: now.Add(-time.Hour),
		}},
		Cost: personenrichment.Cost{Currency: "USD", AmountMicros: 600},
		Claims: []personfacts.ProposedClaim{
			claim(selected[0], `"chat"`), claim(selected[1], `"sailing"`),
		},
	}
	programFingerprint, err := personenrichment.ProgramFingerprint(personenrichment.ProgramDescriptor{
		HostMappingVersion: personenrichment.HostClaimMappingVersion,
		AdapterVersion:     result.AdapterVersion, WireSchemaVersion: result.SchemaVersion,
		GeneratedSchema: true, GeneratedSchemaHash: schemaHash,
	})
	require.NoError(t, err)
	require.NoError(t, st.AuthorizeAttemptDispatch(ctx, attempt.Token))
	require.NoError(t, st.RecordProviderStarted(ctx, attempt.Token, personenrichment.Attempt{
		State: personenrichment.AttemptPending, RequestID: result.RequestID, JobID: result.JobID,
		StartedAt: now, AdapterVersion: result.AdapterVersion, SchemaVersion: result.SchemaVersion,
		GeneratedSchema: true, GeneratedSchemaHash: schemaHash, Targets: selected,
		ProgramFingerprint: programFingerprint,
	}))
	hasher, err := personenrichment.NewSuppressionHasher(bytes.Repeat([]byte{0x6a}, 32))
	require.NoError(t, err)
	commit, err := personenrichment.NewClaimCommit(personenrichment.ClaimCommitInput{
		AttemptID: attempt.ID, RunID: run.ID, PersonID: person.ID,
		LeaseFence: attempt.Token.Fence, ProfileFingerprint: profile.Fingerprint,
		ProviderNamespace: profile.ProviderNamespace, RequestHash: requestHash,
		IdentityAssessment: personenrichment.IdentityAssessment{
			Reason: personenrichment.IdentityUncertainReason,
			Judgment: &personenrichment.IdentityJudgment{
				Outcome:        personenrichment.IdentityJudgmentUncertain,
				ExactClass:     personenrichment.IdentifierCurrentCompany,
				NameCompatible: 0.70, CompanySame: 0.99, NameConflict: 0.10, Model: "jev-1.13.0",
			},
		},
	}, result, hasher)
	require.NoError(t, err)
	outcome, err := st.CommitEnrichmentClaims(ctx, commit)
	require.NoError(t, err)
	require.Equal(t, personenrichment.ClaimIdentityUncertain, outcome.Status)
	require.NoError(t, st.CompleteRun(ctx, run.ID, personenrichment.RunCompletion{}))
	return person.ID, attempt.ID
}
