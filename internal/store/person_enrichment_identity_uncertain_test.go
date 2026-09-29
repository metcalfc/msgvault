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

// execWithForeignKeysOff runs statements on one connection with foreign keys
// suspended, the only way to plant a dangling reference for a test.
func execWithForeignKeysOff(t *testing.T, st *Store, statements ...string) {
	t.Helper()
	requirements := require.New(t)
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

func rerunIdentityUncertainMigration(t *testing.T, st *Store) error {
	t.Helper()
	_, err := st.DB().ExecContext(t.Context(), st.Rebind(
		`DELETE FROM applied_migrations WHERE name = ?`), migrationPersonEnrichmentIdentityUncertain)
	require.NoError(t, err)
	return st.InitSchema()
}

func TestPersonEnrichmentIdentityUncertainMigrationIgnoresUnrelatedDanglingReferences(t *testing.T) {
	f := newEnrichmentResultFixture(t)
	if f.store.IsPostgreSQL() {
		t.Skip("the rebuild and its foreign key check are SQLite-only")
	}
	installLegacyPersonEnrichmentAttemptStates(t, f.store)
	execWithForeignKeysOff(t, f.store,
		`INSERT INTO labels (source_id, name) VALUES (987654321, 'orphaned label')`)

	require.NoError(t, rerunIdentityUncertainMigration(t, f.store),
		"a legacy orphan in an unrelated table must not block the upgrade")
	var definition string
	require.NoError(t, f.store.DB().QueryRowContext(t.Context(), `SELECT sql FROM sqlite_master
		WHERE type = 'table' AND name = 'person_enrichment_attempts'`).Scan(&definition))
	assert.Contains(t, definition, "'identity_uncertain'")
}

func TestPersonEnrichmentIdentityUncertainMigrationRefusesDanglingAttemptReferences(t *testing.T) {
	cases := map[string]string{
		"from the rebuilt table": `UPDATE person_enrichment_attempts SET person_id = 987654321`,
		"into the rebuilt table": `UPDATE person_enrichment_work SET active_attempt_id = 987654321
			WHERE active_attempt_id IS NOT NULL`,
	}
	for name, plant := range cases {
		t.Run(name, func(t *testing.T) {
			f := newEnrichmentResultFixture(t)
			if f.store.IsPostgreSQL() {
				t.Skip("the rebuild and its foreign key check are SQLite-only")
			}
			installLegacyPersonEnrichmentAttemptStates(t, f.store)
			execWithForeignKeysOff(t, f.store, plant)

			err := rerunIdentityUncertainMigration(t, f.store)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "dangling references")
			var definition string
			require.NoError(t, f.store.DB().QueryRowContext(t.Context(), `SELECT sql FROM sqlite_master
				WHERE type = 'table' AND name = 'person_enrichment_attempts'`).Scan(&definition))
			assert.NotContains(t, definition, "'identity_uncertain'", "the failed rebuild rolls back")
		})
	}
}

func personParticipantIDs(t *testing.T, st *Store, personID int64) []int64 {
	t.Helper()
	rows, err := st.DB().QueryContext(t.Context(), st.Rebind(`SELECT participant_id
		FROM person_participants WHERE person_id = ? ORDER BY participant_id`), personID)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

func identityJudgmentRows(t *testing.T, st *Store, personID int64) (judgments, attempts int64) {
	t.Helper()
	require.NoError(t, st.DB().QueryRowContext(t.Context(), st.Rebind(`SELECT
		(SELECT COUNT(*) FROM person_enrichment_identity_judgments WHERE person_id = ?),
		(SELECT COUNT(*) FROM person_enrichment_attempts WHERE person_id = ?)`), personID, personID).
		Scan(&judgments, &attempts))
	return judgments, attempts
}

// TestPersonMergeAndSplitTreatIdentityJudgmentsLikeTheirAttempts pins the
// merge policy for identity judgments: like the attempt each belongs to, a
// judgment stays scoped to the person it was made for. The survivor keeps
// its judgments through a merge and a split; an absorbed person's judgments
// cascade with its root and are not restored onto the split-out person.
func TestPersonMergeAndSplitTreatIdentityJudgmentsLikeTheirAttempts(t *testing.T) {
	for _, side := range []string{"survivor", "absorbed"} {
		t.Run(side, func(t *testing.T) {
			checks := assert.New(t)
			requirements := require.New(t)
			f := newEnrichmentResultFixture(t)
			f.commit.IdentityAssessment = personenrichment.IdentityAssessment{
				Reason: personenrichment.IdentityUncertainReason, Judgment: uncertainJudgment(),
			}
			f.reseal(t)
			_, err := f.store.CommitEnrichmentClaims(t.Context(), f.commit)
			requirements.NoError(err)
			judged, attempts := identityJudgmentRows(t, f.store, f.person.ID)
			requirements.Equal(int64(1), judged)
			requirements.Equal(int64(1), attempts)

			participantID, err := f.store.EnsureParticipant(
				"merge-judgment-"+side+"@example.test", "Merge Judgment", "example.test")
			requirements.NoError(err)
			other, _, err := f.store.CreatePersonFromParticipantContext(t.Context(), participantID)
			requirements.NoError(err)
			judgedPerson, err := f.store.GetPersonContext(t.Context(), f.person.ID)
			requirements.NoError(err)
			survivor, absorbed := judgedPerson, other
			if side == "absorbed" {
				survivor, absorbed = other, judgedPerson
			}
			absorbedParticipants := personParticipantIDs(t, f.store, absorbed.ID)
			merged, err := f.store.MergePersonsContext(t.Context(), PersonMergeRequest{
				SurvivorID: survivor.ID, AbsorbedID: absorbed.ID,
				ExpectedSurvivorRevision: survivor.Revision, ExpectedAbsorbedRevision: absorbed.Revision,
				IdempotencyKey: "merge-judgment-" + side, Actor: "test",
			})
			requirements.NoError(err)

			wantSurvivor := int64(0)
			if side == "survivor" {
				wantSurvivor = 1
			}
			judged, attempts = identityJudgmentRows(t, f.store, merged.Person.ID)
			checks.Equal(wantSurvivor, judged, "judgments follow the attempts they belong to")
			checks.Equal(wantSurvivor, attempts)
			judged, _ = identityJudgmentRows(t, f.store, absorbed.ID)
			checks.Equal(int64(0), judged, "no judgment is left on the absorbed root")
			checks.Equal(wantSurvivor, enrichmentTableCount(t, f.store, "person_enrichment_identity_judgments"))

			split, err := f.store.SplitPersonMergeContext(t.Context(), PersonSplitRequest{
				SourcePersonID: merged.Person.ID, MergeID: merged.Merge.ID,
				ParticipantIDs:         absorbedParticipants,
				ExpectedSourceRevision: merged.Person.Revision,
				IdempotencyKey:         "split-judgment-" + side, Actor: "test",
			})
			requirements.NoError(err)
			judged, _ = identityJudgmentRows(t, f.store, merged.Person.ID)
			checks.Equal(wantSurvivor, judged, "a split leaves the survivor's judgments in place")
			judged, _ = identityJudgmentRows(t, f.store, split.NewPerson.ID)
			checks.Equal(int64(0), judged, "an absorbed person's judgments are not restored by a split")
		})
	}
}

// TestPersonEnrichmentAttemptStateVocabularyMatchesTheSchema checks that the
// one state list the migration and validation derive from is exactly what a
// fresh archive's table admits, on the backend under test.
func TestPersonEnrichmentAttemptStateVocabularyMatchesTheSchema(t *testing.T) {
	f := newEnrichmentResultFixture(t)
	for _, state := range personEnrichmentAttemptStates {
		assert.True(t, validPersonEnrichmentAttemptState(state), state)
		_, err := f.store.DB().ExecContext(t.Context(), f.store.Rebind(
			`UPDATE person_enrichment_attempts SET state = ? WHERE id = ?`), state, f.attempt.ID)
		assert.NoError(t, err, "the fresh schema admits %s", state)
	}
	assert.False(t, validPersonEnrichmentAttemptState("abandoned"))
	_, err := f.store.DB().ExecContext(t.Context(), f.store.Rebind(
		`UPDATE person_enrichment_attempts SET state = 'abandoned' WHERE id = ?`), f.attempt.ID)
	assert.Error(t, err, "the fresh schema rejects a state outside the vocabulary")
}
