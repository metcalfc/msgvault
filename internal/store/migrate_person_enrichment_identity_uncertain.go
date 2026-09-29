package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// personEnrichmentAttemptStates is the attempt state vocabulary, defined
// once. The rebuilt table's CHECK, the migration's validation, and
// validPersonEnrichmentAttemptState all derive from it, so they cannot drift.
// identity_uncertain is a semantic identity check that landed between the
// accept and reject thresholds, recorded for a person to look at with no
// claim applied.
var personEnrichmentAttemptStates = []string{
	"queued", "starting", "pending", "retry_wait", personEnrichmentStateSucceeded, "terminal",
	"suppressed", "identity_rejected", "uncertain_start", personEnrichmentStateIdentityUncertain,
}

// personEnrichmentAttemptStateSQLList renders the vocabulary as a SQL value
// list: 'queued', 'starting', ...
var personEnrichmentAttemptStateSQLList = "'" + strings.Join(personEnrichmentAttemptStates, "', '") + "'"

// personEnrichmentAttemptStateCheck is the column check both backends use.
var personEnrichmentAttemptStateCheck = `CHECK(state IN (` + personEnrichmentAttemptStateSQLList + `))`

// personEnrichmentAttemptStateConstraint is the constraint name both backends
// use. PostgreSQL auto-names an inline column check exactly this way, so an
// archive that predates the named constraint drops under the same name.
const personEnrichmentAttemptStateConstraint = "person_enrichment_attempts_state_check"

// migratePersonEnrichmentIdentityUncertain widens person_enrichment_attempts
// .state to admit identity_uncertain. Only the check changes; every column,
// key, index, and row is preserved, including the identifiers, citations,
// sources, and work rows that reference the attempt.
func (s *Store) migratePersonEnrichmentIdentityUncertain(ctx context.Context) error {
	if s.IsPostgreSQL() {
		return s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
			if err := validatePersonEnrichmentAttemptStateRows(ctx, tx); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `ALTER TABLE person_enrichment_attempts
				DROP CONSTRAINT IF EXISTS `+personEnrichmentAttemptStateConstraint); err != nil {
				return fmt.Errorf("drop person enrichment attempt state constraint: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `ALTER TABLE person_enrichment_attempts
				ADD CONSTRAINT `+personEnrichmentAttemptStateConstraint+` `+
				personEnrichmentAttemptStateCheck); err != nil {
				return fmt.Errorf("create person enrichment attempt state constraint: %w", err)
			}
			return nil
		})
	}
	return s.migratePersonEnrichmentIdentityUncertainSQLite(ctx)
}

// migratePersonEnrichmentIdentityUncertainSQLite rebuilds
// person_enrichment_attempts with the widened state check. The identifiers,
// citations, sources, identity judgments, and work rows reference the table
// with ON DELETE CASCADE, which is why the rebuild runs with foreign keys
// suspended. The post-rebuild check covers the attempts table and every
// table that references it; unrelated legacy dangling references elsewhere
// in the archive do not block the upgrade.
func (s *Store) migratePersonEnrichmentIdentityUncertainSQLite(ctx context.Context) error {
	return s.rebuildSQLiteTable(ctx, sqliteTableRebuild{
		Table: "person_enrichment_attempts", AppliedMarker: "'identity_uncertain'",
		Label:           "widen person enrichment attempt states",
		Validate:        validatePersonEnrichmentAttemptStateRows,
		Statements:      personEnrichmentAttemptStateRebuildStatements(),
		CountViolations: countPersonEnrichmentAttemptForeignKeyViolations,
	})
}

// countPersonEnrichmentAttemptForeignKeyViolations checks the rebuilt
// attempts table and the tables that reference it, and nothing else.
func countPersonEnrichmentAttemptForeignKeyViolations(ctx context.Context, tx *sql.Tx) (int, error) {
	return countSQLiteTableForeignKeyViolations(ctx, tx, "person_enrichment_attempts")
}

const personEnrichmentAttemptColumnList = `id, run_id, person_id, profile_fingerprint, trigger_kind,
			trigger_generation, person_revision, payload_hash, request_hash, fact_generation_key, state,
			provider_request_id, provider_job_id, adapter_version, schema_version, generated_schema,
			generated_schema_hash, targets_json, program_fingerprint, provider_started_at,
			dispatch_authorized_at, lease_owner, lease_fence, lease_until, next_action_at, attempt_count,
			hard_cost_cap_enforced, reserved_cost_usd_micros, actual_cost_usd_micros, failure_class,
			created_at, completed_at`

func personEnrichmentAttemptStateRebuildStatements() []string {
	return []string{
		`DROP TABLE IF EXISTS person_enrichment_attempts_state_v2`,
		`CREATE TABLE person_enrichment_attempts_state_v2 (
			id INTEGER PRIMARY KEY,
			run_id INTEGER NOT NULL REFERENCES person_enrichment_runs(id) ON DELETE RESTRICT,
			person_id INTEGER NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
			profile_fingerprint TEXT NOT NULL REFERENCES person_enrichment_profiles(fingerprint),
			trigger_kind TEXT NOT NULL CHECK(trigger_kind IN ('tracked', 'identity', 'claim_expiry', 'refresh', 'manual')),
			trigger_generation TEXT NOT NULL,
			person_revision INTEGER NOT NULL CHECK(person_revision >= 0),
			payload_hash TEXT NOT NULL,
			request_hash TEXT NOT NULL UNIQUE,
			fact_generation_key TEXT,
			state TEXT NOT NULL CONSTRAINT ` + personEnrichmentAttemptStateConstraint + ` ` + personEnrichmentAttemptStateCheck + `,
			provider_request_id TEXT,
			provider_job_id TEXT,
			adapter_version TEXT,
			schema_version TEXT,
			generated_schema INTEGER NOT NULL DEFAULT 0 CHECK(generated_schema IN (0, 1)),
			generated_schema_hash TEXT,
			targets_json TEXT,
			program_fingerprint TEXT,
			provider_started_at DATETIME,
			dispatch_authorized_at DATETIME,
			lease_owner TEXT,
			lease_fence INTEGER NOT NULL CHECK(lease_fence >= 0),
			lease_until DATETIME,
			next_action_at DATETIME,
			attempt_count INTEGER NOT NULL DEFAULT 0 CHECK(attempt_count >= 0),
			hard_cost_cap_enforced INTEGER NOT NULL CHECK(hard_cost_cap_enforced IN (0, 1)),
			reserved_cost_usd_micros INTEGER NOT NULL CHECK(reserved_cost_usd_micros >= 0),
			actual_cost_usd_micros INTEGER CHECK(actual_cost_usd_micros >= 0),
			failure_class TEXT,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			completed_at DATETIME,
			CHECK ((generated_schema = 1 AND generated_schema_hash IS NOT NULL) OR
			       (generated_schema = 0 AND generated_schema_hash IS NULL))
		)`,
		`INSERT INTO person_enrichment_attempts_state_v2 (` + personEnrichmentAttemptColumnList + `)
		 SELECT ` + personEnrichmentAttemptColumnList + ` FROM person_enrichment_attempts`,
		`DROP TABLE person_enrichment_attempts`,
		`ALTER TABLE person_enrichment_attempts_state_v2 RENAME TO person_enrichment_attempts`,
		`CREATE INDEX IF NOT EXISTS person_enrichment_attempts_next_action
			ON person_enrichment_attempts(state, next_action_at)`,
		`CREATE INDEX IF NOT EXISTS person_enrichment_attempts_person_created
			ON person_enrichment_attempts(person_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS person_enrichment_attempts_run_state
			ON person_enrichment_attempts(run_id, state)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS person_enrichment_attempts_provider_job
			ON person_enrichment_attempts(profile_fingerprint, provider_job_id)
			WHERE provider_job_id IS NOT NULL`,
	}
}

// validatePersonEnrichmentAttemptStateRows refuses to widen the constraint
// over rows the widened vocabulary would still reject, so a corrupt ledger
// fails the upgrade loudly instead of at the next insert.
func validatePersonEnrichmentAttemptStateRows(ctx context.Context, tx *loggedTx) error {
	var invalid int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM person_enrichment_attempts
		WHERE state NOT IN (`+personEnrichmentAttemptStateSQLList+`)`,
	).Scan(&invalid); err != nil {
		return fmt.Errorf("validate person enrichment attempt states: %w", err)
	}
	if invalid != 0 {
		return fmt.Errorf("person enrichment attempts contain %d invalid states", invalid)
	}
	return nil
}
