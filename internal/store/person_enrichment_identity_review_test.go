package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/personenrichment"
	"go.kenn.io/msgvault/internal/personfacts"
)

// uncertainEnrichmentFixture commits the result fixture as an
// identity_uncertain attempt and completes its run.
func uncertainEnrichmentFixture(t *testing.T) *enrichmentResultFixture {
	t.Helper()
	f := newEnrichmentResultFixture(t)
	f.commit.IdentityAssessment = personenrichment.IdentityAssessment{
		Reason: personenrichment.IdentityUncertainReason, Judgment: uncertainJudgment(),
	}
	f.reseal(t)
	outcome, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
	require.NoError(t, err)
	require.Equal(t, personenrichment.ClaimIdentityUncertain, outcome.Status)
	require.NoError(t, f.store.CompleteRun(t.Context(), f.commit.RunID, personenrichment.RunCompletion{}))
	return f
}

func runCounts(t *testing.T, f *enrichmentResultFixture) (succeeded, rejected int64) {
	t.Helper()
	require.NoError(t, f.store.DB().QueryRowContext(t.Context(), `SELECT succeeded_count,
		identity_rejected_count FROM person_enrichment_runs WHERE id = ?`, f.commit.RunID,
	).Scan(&succeeded, &rejected))
	return succeeded, rejected
}

func TestUncertainAttemptKeepsItsProviderPersonIDsForReview(t *testing.T) {
	requirements := require.New(t)
	checks := assert.New(t)
	f := uncertainEnrichmentFixture(t)

	ids, err := attemptProviderIDsTxForTest(t, f)
	requirements.NoError(err)
	requirements.Len(ids, 1)
	checks.Equal("Opaque/Person:Case?part=1", ids[0].id)
	checks.Equal(975, ids[0].confidence)
	checks.Equal(int64(0), enrichmentTableCount(t, f.store, "person_enrichment_provider_identities"),
		"an uncertain identity attaches nothing")
}

func attemptProviderIDsTxForTest(t *testing.T, f *enrichmentResultFixture) ([]attemptProviderID, error) {
	t.Helper()
	var ids []attemptProviderID
	err := f.store.withReadSnapshotContext(t.Context(), func(tx *loggedTx) error {
		var err error
		ids, err = attemptProviderIDsTx(t.Context(), tx, f.attempt.ID)
		return err
	})
	return ids, err
}

func TestListPersonEnrichmentIdentityReviewsShowsJudgmentAndReturnedIdentity(t *testing.T) {
	requirements := require.New(t)
	checks := assert.New(t)
	f := uncertainEnrichmentFixture(t)

	reviews, err := f.store.ListPersonEnrichmentIdentityReviewsContext(t.Context(), 20)
	requirements.NoError(err)
	requirements.Len(reviews, 1)
	review := reviews[0]
	checks.Equal(f.attempt.ID, review.AttemptID)
	checks.Equal(f.person.ID, review.PersonID)
	checks.Equal(f.profile.Name, review.ProviderName)
	checks.Equal(string(personenrichment.ProviderExa), review.ProviderKind)
	checks.Equal(string(personenrichment.IdentifierCurrentCompany), review.ExactClass)
	checks.InDelta(0.70, review.NameCompatible, 1e-9)
	checks.InDelta(0.99, review.CompanySame, 1e-9)
	checks.InDelta(0.10, review.NameConflict, 1e-9)
	checks.True(review.ProviderPersonIDKnown)
	requirements.NotNil(review.Returned.ProfileURLHost)
	checks.Equal("sources.example.test", *review.Returned.ProfileURLHost)
	checks.ElementsMatch([]PersonEnrichmentReviewClaim{
		{Target: AttributeSlugPrimaryChannel, Value: "chat"},
		{Target: AttributeSlugAskMeAbout, Value: "sailing"},
	}, review.Claims)

	_, err = f.store.ListPersonEnrichmentIdentityReviewsContext(t.Context(), 0)
	checks.Error(err)
}

func TestSummarizeReturnedIdentityReadsNameRolesAndLocation(t *testing.T) {
	checks := assert.New(t)
	claim := func(kind personfacts.TargetKind, key, value string) personfacts.Claim {
		return personfacts.Claim{Target: personfacts.TargetRef{Kind: kind, Key: key}, SubmittedValue: []byte(value)}
	}
	generation := personFactLedgerGeneration{
		Claims: []personfacts.Claim{
			claim(personfacts.TargetAttribute, "first", `"Ada"`),
			claim(personfacts.TargetAttribute, "last", `"Example"`),
			claim(personfacts.TargetAttribute, "where", `"Example City"`),
			claim(personfacts.TargetEmployment, "system:employment",
				`{"organization":{"name":"Example Labs"},"title":"Engineer"}`),
			claim(personfacts.TargetEmployment, "system:employment",
				`{"organization":{"name":"Old Co"},"title":"Intern","end_date":{"year":2020}}`),
		},
		Evidence: []personfacts.Evidence{{Input: personfacts.EvidenceInput{
			SourceURL: "https://Profiles.Example.test/ada"}}},
	}
	profile := personenrichment.ProviderProfile{Targets: []personfacts.TargetDescriptor{
		{Kind: personfacts.TargetAttribute, Key: "first", Slug: "first_name"},
		{Kind: personfacts.TargetAttribute, Key: "last", Slug: "last_name"},
		{Kind: personfacts.TargetAttribute, Key: "where", Slug: AttributeSlugLocation},
	}}
	returned, claims := summarizeReturnedIdentity(generation, profile)
	if checks.NotNil(returned.Name) {
		checks.Equal("Ada Example", *returned.Name)
	}
	if checks.NotNil(returned.Location) {
		checks.Equal("Example City", *returned.Location)
	}
	checks.Equal([]PersonEnrichmentReturnedRole{{Title: "Engineer", Company: "Example Labs"}}, returned.CurrentRoles)
	if checks.NotNil(returned.ProfileURLHost) {
		checks.Equal("profiles.example.test", *returned.ProfileURLHost)
	}
	checks.Len(claims, 5)
	checks.Contains(claims, PersonEnrichmentReviewClaim{Target: "employment", Value: "Engineer at Example Labs"})
}

func TestConfirmPersonEnrichmentIdentityCommitsClaimsAtTheVerifiedScore(t *testing.T) {
	requirements := require.New(t)
	checks := assert.New(t)
	f := uncertainEnrichmentFixture(t)
	succeeded, rejected := runCounts(t, f)
	checks.Equal(int64(0), succeeded)
	checks.Equal(int64(1), rejected)

	decision, err := f.store.ConfirmPersonEnrichmentIdentityContext(t.Context(), f.attempt.ID, "user")
	requirements.NoError(err)
	checks.Equal("confirmed", decision.Decision)
	checks.Equal(PersonEnrichmentIdentityConfirmedReason, decision.Reason)
	checks.Equal("succeeded", decision.AttemptState)
	checks.Positive(decision.Projections, "confirmed claims project onto the person")
	checks.Equal(1, decision.ProviderIdentitiesAttached)

	attempt, err := f.store.GetPersonEnrichmentAttemptContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	checks.Equal("succeeded", attempt.State)
	checks.Nil(attempt.FailureClass)

	evidence, err := f.store.ListPersonFactEvidenceContext(t.Context(), f.person.ID, personfacts.EvidenceFilter{Limit: 20})
	requirements.NoError(err)
	confirmed := 0
	for _, item := range evidence {
		if item.Input.IdentityScore == PersonEnrichmentUserConfirmedIdentityScore {
			confirmed++
		}
	}
	checks.Positive(confirmed)

	var confidence int
	requirements.NoError(f.store.DB().QueryRowContext(t.Context(), `SELECT confidence
		FROM person_enrichment_provider_identities WHERE person_id = ? AND provider_person_id = ?`,
		f.person.ID, "Opaque/Person:Case?part=1").Scan(&confidence))
	checks.Equal(PersonEnrichmentUserConfirmedIdentityScore, confidence)

	var reason string
	requirements.NoError(f.store.DB().QueryRowContext(t.Context(), `SELECT reason
		FROM person_enrichment_identity_reviews WHERE attempt_id = ?`, f.attempt.ID).Scan(&reason))
	checks.Equal(PersonEnrichmentIdentityConfirmedReason, reason)

	work, err := f.store.ListPersonEnrichmentWorkContext(t.Context(), PersonEnrichmentWorkFilter{
		PersonID: f.person.ID, ProfileFingerprint: f.profile.Fingerprint, Limit: 10,
	})
	requirements.NoError(err)
	checks.Len(work, 1, "confirmation schedules the profile's refresh")

	succeeded, rejected = runCounts(t, f)
	checks.Equal(int64(1), succeeded)
	checks.Equal(int64(0), rejected)

	reviews, err := f.store.ListPersonEnrichmentIdentityReviewsContext(t.Context(), 20)
	requirements.NoError(err)
	checks.Empty(reviews)
	_, err = f.store.ConfirmPersonEnrichmentIdentityContext(t.Context(), f.attempt.ID, "user")
	requirements.ErrorIs(err, ErrPersonEnrichmentReviewStateChanged)
}

func TestConfirmPersonEnrichmentIdentityLosesARaceWithoutWriting(t *testing.T) {
	requirements := require.New(t)
	checks := assert.New(t)
	f := uncertainEnrichmentFixture(t)
	generationsBefore := enrichmentTableCount(t, f.store, "person_fact_generations")

	// Another decision moves the attempt after this one locked and applied
	// its generation but before its guarded update.
	f.store.personEnrichmentReviewBeforeUpdateHook = func(tx *loggedTx) {
		_, err := tx.ExecContext(t.Context(), `UPDATE person_enrichment_attempts
			SET state = 'identity_rejected' WHERE id = ?`, f.attempt.ID)
		requirements.NoError(err)
	}
	_, err := f.store.ConfirmPersonEnrichmentIdentityContext(t.Context(), f.attempt.ID, "user")
	f.store.personEnrichmentReviewBeforeUpdateHook = nil
	requirements.ErrorIs(err, ErrPersonEnrichmentReviewStateChanged)

	checks.Equal(generationsBefore, enrichmentTableCount(t, f.store, "person_fact_generations"),
		"the losing decision's generation rolls back")
	checks.Equal(int64(0), enrichmentTableCount(t, f.store, "person_enrichment_provider_identities"))
	checks.Equal(int64(0), enrichmentTableCount(t, f.store, "person_enrichment_identity_reviews"))
	attempt, err := f.store.GetPersonEnrichmentAttemptContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	checks.Equal("identity_uncertain", attempt.State)
}

func TestConfirmPersonEnrichmentIdentityRefusesUntrackedAndOwnedElsewhere(t *testing.T) {
	requirements := require.New(t)
	f := uncertainEnrichmentFixture(t)

	_, err := f.store.SetPersonTrackingContext(t.Context(), f.person.ID, false)
	requirements.NoError(err)
	_, err = f.store.ConfirmPersonEnrichmentIdentityContext(t.Context(), f.attempt.ID, "user")
	requirements.ErrorIs(err, ErrPersonFactPersonNotTracked)
	_, err = f.store.SetPersonTrackingContext(t.Context(), f.person.ID, true)
	requirements.NoError(err)

	var otherID int64
	requirements.NoError(f.store.DB().QueryRowContext(t.Context(),
		`INSERT INTO persons (vcard_uid) VALUES ('other-owner') RETURNING id`).Scan(&otherID))
	_, err = f.store.DB().ExecContext(t.Context(), `INSERT INTO person_enrichment_provider_identities
		(person_id, provider_namespace, provider_person_id, confidence, verified_at)
		VALUES (?, ?, ?, 1000, ?)`, otherID, f.profile.ProviderNamespace,
		"Opaque/Person:Case?part=1", f.now)
	requirements.NoError(err)
	_, err = f.store.ConfirmPersonEnrichmentIdentityContext(t.Context(), f.attempt.ID, "user")
	requirements.ErrorIs(err, ErrPersonEnrichmentIdentityOwnedElsewhere)
}

func TestRejectPersonEnrichmentIdentityRecordsANegative(t *testing.T) {
	requirements := require.New(t)
	checks := assert.New(t)
	f := uncertainEnrichmentFixture(t)

	decision, err := f.store.RejectPersonEnrichmentIdentityContext(t.Context(), f.attempt.ID, "user")
	requirements.NoError(err)
	checks.Equal("rejected", decision.Decision)
	checks.Equal(PersonEnrichmentIdentityRejectedReason, decision.Reason)
	checks.Equal(1, decision.Negatives)
	attempt, err := f.store.GetPersonEnrichmentAttemptContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	checks.Equal("identity_rejected", attempt.State)
	var kind, value string
	requirements.NoError(f.store.DB().QueryRowContext(t.Context(), `SELECT key_kind, key_value
		FROM person_enrichment_identity_rejections WHERE person_id = ?`, f.person.ID).Scan(&kind, &value))
	checks.Equal("provider_person_id", kind)
	checks.Equal("Opaque/Person:Case?part=1", value)
	_, rejected := runCounts(t, f)
	checks.Equal(int64(1), rejected)
	_, err = f.store.RejectPersonEnrichmentIdentityContext(t.Context(), f.attempt.ID, "user")
	requirements.ErrorIs(err, ErrPersonEnrichmentReviewStateChanged)
}

func TestRejectLegacyUncertainAttemptFallsBackToTheProfileURL(t *testing.T) {
	requirements := require.New(t)
	checks := assert.New(t)
	f := uncertainEnrichmentFixture(t)
	_, err := f.store.DB().ExecContext(t.Context(),
		`DELETE FROM person_enrichment_attempt_provider_ids WHERE attempt_id = ?`, f.attempt.ID)
	requirements.NoError(err)

	decision, err := f.store.RejectPersonEnrichmentIdentityContext(t.Context(), f.attempt.ID, "user")
	requirements.NoError(err)
	checks.Equal(1, decision.Negatives)
	var kind, value string
	requirements.NoError(f.store.DB().QueryRowContext(t.Context(), `SELECT key_kind, key_value
		FROM person_enrichment_identity_rejections WHERE person_id = ?`, f.person.ID).Scan(&kind, &value))
	checks.Equal("profile_url", kind)
	checks.Equal("https://sources.example.test/profile/alice", value)
}

func TestRejectedProviderIdentityIsNeverAppliedAgain(t *testing.T) {
	for _, negative := range []struct{ kind, value string }{
		{"provider_person_id", "Opaque/Person:Case?part=1"},
		{"profile_url", "https://sources.example.test/profile/alice"},
	} {
		t.Run(negative.kind, func(t *testing.T) {
			requirements := require.New(t)
			checks := assert.New(t)
			f := newEnrichmentResultFixture(t)
			_, err := f.store.DB().ExecContext(t.Context(), `INSERT INTO person_enrichment_identity_rejections
				(person_id, provider_namespace, key_kind, key_value, actor, created_at)
				VALUES (?, ?, ?, ?, 'user', ?)`, f.person.ID, f.profile.ProviderNamespace,
				negative.kind, negative.value, f.now)
			requirements.NoError(err)

			// The fixture's assessment is an exact, accepted identity; the
			// user's negative still wins.
			outcome, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
			requirements.NoError(err)
			checks.Equal(personenrichment.ClaimIdentityRejected, outcome.Status)
			requirements.NotNil(outcome.Generation)
			checks.Empty(outcome.Generation.Projections)
			checks.Equal(int64(0), enrichmentTableCount(t, f.store, "person_enrichment_provider_identities"))
		})
	}
}
