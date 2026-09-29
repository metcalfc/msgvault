package store

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/personenrichment"
	"go.kenn.io/msgvault/internal/personfacts"
)

func uncertainJudgment() *personenrichment.IdentityJudgment {
	return &personenrichment.IdentityJudgment{
		Outcome: personenrichment.IdentityJudgmentUncertain, ExactClass: personenrichment.IdentifierCurrentCompany,
		NameCompatible: 0.70, CompanySame: 0.99, NameConflict: 0.10, Model: "jev-1.13.0",
	}
}

func TestCommitEnrichmentClaimsUncertainIdentityRecordsJudgmentAndAppliesNothing(t *testing.T) {
	checks := assert.New(t)
	requirements := require.New(t)
	f := newEnrichmentResultFixture(t)
	f.commit.IdentityAssessment = personenrichment.IdentityAssessment{
		Reason: personenrichment.IdentityUncertainReason, Judgment: uncertainJudgment(),
	}
	f.reseal(t)
	outcome, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
	requirements.NoError(err)
	checks.Equal(personenrichment.ClaimIdentityUncertain, outcome.Status)
	requirements.NotNil(outcome.Generation)
	checks.Empty(outcome.Generation.Projections, "an uncertain identity applies no claim")
	for _, decision := range outcome.Generation.Decisions {
		checks.Equal(personfacts.DecisionIdentityRejected, decision.Action)
	}

	attempt, err := f.store.GetPersonEnrichmentAttemptContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	checks.Equal("identity_uncertain", attempt.State)
	requirements.NotNil(attempt.FailureClass)
	checks.Equal("identity_uncertain", *attempt.FailureClass)
	checks.Equal(int64(0), enrichmentTableCount(t, f.store, "person_enrichment_attempt_identifiers"))
	checks.Equal(int64(0), enrichmentTableCount(t, f.store, "person_enrichment_citations"))

	judgment, err := f.store.GetPersonEnrichmentIdentityJudgmentContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	checks.Equal(personenrichment.IdentityJudgmentUncertain, judgment.Outcome)
	checks.Equal(personenrichment.IdentifierCurrentCompany, judgment.ExactClass)
	checks.InDelta(0.70, judgment.NameCompatible, 1e-9)
	checks.InDelta(0.99, judgment.CompanySame, 1e-9)
	checks.InDelta(0.10, judgment.NameConflict, 1e-9)
	checks.Equal("jev-1.13.0", judgment.Model)
	checks.Equal(f.person.ID, judgment.PersonID)
	checks.Equal("identity_uncertain", judgment.AttemptState)
	checks.Equal(f.now.UTC(), judgment.JudgedAt)

	listed, err := f.store.ListPersonEnrichmentIdentityJudgmentsContext(t.Context(), PersonEnrichmentIdentityJudgmentFilter{
		Outcome: personenrichment.IdentityJudgmentUncertain, PersonID: f.person.ID, Limit: 5,
	})
	requirements.NoError(err)
	requirements.Len(listed, 1)
	checks.Equal(f.attempt.ID, listed[0].AttemptID)
	none, err := f.store.ListPersonEnrichmentIdentityJudgmentsContext(t.Context(), PersonEnrichmentIdentityJudgmentFilter{
		Outcome: personenrichment.IdentityJudgmentAccepted, Limit: 5,
	})
	requirements.NoError(err)
	checks.Empty(none)

	replay, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
	requirements.NoError(err)
	checks.Equal(personenrichment.ClaimIdentityUncertain, replay.Status, "a replay reports the same disposition")
	checks.Equal(int64(1), enrichmentTableCount(t, f.store, "person_enrichment_identity_judgments"))
	assertNoRefreshWork(t, f)
}

func TestCommitEnrichmentClaimsKeepsTheJudgmentWhenPolicyRejectsTheResult(t *testing.T) {
	checks := assert.New(t)
	requirements := require.New(t)
	f := newEnrichmentResultFixture(t)
	f.commit.IdentityAssessment = personenrichment.IdentityAssessment{
		Accepted: true, Score: personenrichment.SemanticIdentityScore, Reason: personenrichment.SemanticIdentityReason,
		MatchedClasses: []personenrichment.IdentifierClass{personenrichment.IdentifierName, personenrichment.IdentifierCurrentCompany},
		Judgment: &personenrichment.IdentityJudgment{
			Outcome: personenrichment.IdentityJudgmentAccepted, ExactClass: personenrichment.IdentifierName,
			NameCompatible: 0.99, CompanySame: 0.95, NameConflict: 0.01, Model: "jev-1.13.0",
		},
	}
	f.reseal(t)
	revoked, err := f.store.RevokePersonEnrichmentConsent(t.Context(), f.profile.Fingerprint, "test")
	requirements.NoError(err)
	requirements.True(revoked)
	outcome, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
	requirements.NoError(err)
	checks.Equal(personenrichment.ClaimPolicyRejected, outcome.Status)
	attempt, err := f.store.GetPersonEnrichmentAttemptContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	checks.Equal("terminal", attempt.State)
	judgment, err := f.store.GetPersonEnrichmentIdentityJudgmentContext(t.Context(), f.attempt.ID)
	requirements.NoError(err, "the paid judgment leaves a row even though policy rejected the result")
	checks.Equal(personenrichment.IdentityJudgmentAccepted, judgment.Outcome)
	checks.Equal("terminal", judgment.AttemptState)
	checks.Equal(int64(1), enrichmentTableCount(t, f.store, "person_enrichment_identity_judgments"))
}

func TestCommitEnrichmentClaimsSemanticIdentityAppliesAtTheExactScore(t *testing.T) {
	checks := assert.New(t)
	requirements := require.New(t)
	f := newEnrichmentResultFixture(t)
	f.commit.IdentityAssessment = personenrichment.IdentityAssessment{
		Accepted: true, Score: personenrichment.SemanticIdentityScore, Reason: personenrichment.SemanticIdentityReason,
		MatchedClasses: []personenrichment.IdentifierClass{personenrichment.IdentifierName, personenrichment.IdentifierCurrentCompany},
		Judgment: &personenrichment.IdentityJudgment{
			Outcome: personenrichment.IdentityJudgmentAccepted, ExactClass: personenrichment.IdentifierCurrentCompany,
			NameCompatible: 0.95, CompanySame: 0.99, NameConflict: 0.02, Model: "jev-1.13.0",
		},
	}
	f.reseal(t)
	outcome, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
	requirements.NoError(err)
	checks.Equal(personenrichment.ClaimApplied, outcome.Status)
	attempt, err := f.store.GetPersonEnrichmentAttemptContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	checks.Equal("succeeded", attempt.State)
	evidence, err := f.store.ListPersonFactEvidenceContext(t.Context(), f.person.ID, personfacts.EvidenceFilter{Limit: 10})
	requirements.NoError(err)
	requirements.NotEmpty(evidence)
	for _, item := range evidence {
		checks.Equal(personenrichment.SemanticIdentityScore, item.Input.IdentityScore)
	}
	judgment, err := f.store.GetPersonEnrichmentIdentityJudgmentContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	checks.Equal(personenrichment.IdentityJudgmentAccepted, judgment.Outcome)
	checks.Equal("succeeded", judgment.AttemptState)
}

func TestCommitEnrichmentClaimsRejectsNonCanonicalSemanticAssessments(t *testing.T) {
	cases := map[string]personenrichment.IdentityAssessment{
		"uncertain without a judgment": {Reason: personenrichment.IdentityUncertainReason},
		"uncertain with an accepted judgment": {
			Reason: personenrichment.IdentityUncertainReason,
			Judgment: &personenrichment.IdentityJudgment{
				Outcome: personenrichment.IdentityJudgmentAccepted, ExactClass: personenrichment.IdentifierName,
				NameCompatible: 0.99, CompanySame: 0.99, NameConflict: 0.01, Model: "jev-1.13.0",
			},
		},
		"semantic accept without a judgment": {
			Accepted: true, Score: personenrichment.SemanticIdentityScore, Reason: personenrichment.SemanticIdentityReason,
			MatchedClasses: []personenrichment.IdentifierClass{personenrichment.IdentifierName, personenrichment.IdentifierCurrentCompany},
		},
		"semantic accept at the wrong score": {
			Accepted: true, Score: 1000, Reason: personenrichment.SemanticIdentityReason,
			MatchedClasses: []personenrichment.IdentifierClass{personenrichment.IdentifierName, personenrichment.IdentifierCurrentCompany},
			Judgment: &personenrichment.IdentityJudgment{
				Outcome: personenrichment.IdentityJudgmentAccepted, ExactClass: personenrichment.IdentifierName,
				NameCompatible: 0.99, CompanySame: 0.99, NameConflict: 0.01, Model: "jev-1.13.0",
			},
		},
		"unverified with an uncertain judgment": {
			Reason: "identity_not_verified", Judgment: uncertainJudgment(),
		},
	}
	for name, assessment := range cases {
		t.Run(name, func(t *testing.T) {
			f := newEnrichmentResultFixture(t)
			f.commit.IdentityAssessment = assessment
			f.reseal(t)
			_, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "identity")
		})
	}
}

// installLegacyPersonEnrichmentAttemptStates puts the attempts table back
// into its pre-Jev shape so the migration runs against an archive that
// already holds attempts and their dependent rows.
func installLegacyPersonEnrichmentAttemptStates(t *testing.T, st *Store) {
	t.Helper()
	requirements := require.New(t)
	legacy := strings.Replace(personEnrichmentAttemptStateCheck, ", 'identity_uncertain'", "", 1)
	if st.IsPostgreSQL() {
		_, err := st.DB().ExecContext(t.Context(), `
			ALTER TABLE person_enrichment_attempts
				DROP CONSTRAINT IF EXISTS `+personEnrichmentAttemptStateConstraint+`;
			ALTER TABLE person_enrichment_attempts
				ADD CONSTRAINT `+personEnrichmentAttemptStateConstraint+` `+legacy)
		requirements.NoError(err)
		return
	}
	statements := personEnrichmentAttemptStateRebuildStatements()
	for i, statement := range statements {
		statements[i] = strings.Replace(statement, personEnrichmentAttemptStateCheck, legacy, 1)
	}
	conn, err := st.DB().Conn(t.Context())
	requirements.NoError(err)
	defer func() { _ = conn.Close() }()
	_, err = conn.ExecContext(t.Context(), `PRAGMA foreign_keys = OFF`)
	requirements.NoError(err)
	for _, statement := range statements {
		_, err = conn.ExecContext(t.Context(), statement)
		requirements.NoError(err, statement)
	}
	_, err = conn.ExecContext(t.Context(), `PRAGMA foreign_keys = ON`)
	requirements.NoError(err)
}

func TestPersonEnrichmentIdentityUncertainMigrationPreservesAttemptsAndAdmitsTheState(t *testing.T) {
	checks := assert.New(t)
	requirements := require.New(t)
	f := newEnrichmentResultFixture(t)
	before, err := f.store.GetPersonEnrichmentAttemptContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	requirements.Equal("pending", before.State)

	installLegacyPersonEnrichmentAttemptStates(t, f.store)
	_, err = f.store.DB().ExecContext(t.Context(), f.store.Rebind(
		`UPDATE person_enrichment_attempts SET state = 'identity_uncertain' WHERE id = ?`), f.attempt.ID)
	requirements.Error(err, "the legacy constraint must reject identity_uncertain")

	_, err = f.store.DB().ExecContext(t.Context(), f.store.Rebind(
		`DELETE FROM applied_migrations WHERE name = ?`), migrationPersonEnrichmentIdentityUncertain)
	requirements.NoError(err)
	requirements.NoError(f.store.InitSchema())

	after, err := f.store.GetPersonEnrichmentAttemptContext(t.Context(), f.attempt.ID)
	requirements.NoError(err)
	checks.Equal(before, after, "every attempt column survives the rebuild")
	var identifiers int64
	requirements.NoError(f.store.DB().QueryRowContext(t.Context(), f.store.Rebind(
		`SELECT COUNT(*) FROM person_enrichment_work WHERE active_attempt_id = ?`), f.attempt.ID).Scan(&identifiers))
	checks.Equal(int64(1), identifiers, "the work row still references the attempt")

	f.commit.IdentityAssessment = personenrichment.IdentityAssessment{
		Reason: personenrichment.IdentityUncertainReason, Judgment: uncertainJudgment(),
	}
	f.reseal(t)
	outcome, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
	requirements.NoError(err, "the migrated constraint must admit identity_uncertain")
	checks.Equal(personenrichment.ClaimIdentityUncertain, outcome.Status)
	requirements.NoError(f.store.InitSchema(), "the migration must be idempotent")
}
