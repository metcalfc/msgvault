package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.kenn.io/msgvault/internal/personenrichment"
)

// PersonEnrichmentIdentityJudgment is one stored semantic identity check:
// the three probabilities, the decision, and the attempt it decided. It
// carries no names; the compared values were transient adapter output.
type PersonEnrichmentIdentityJudgment struct {
	AttemptID          int64                                    `json:"attempt_id"`
	PersonID           int64                                    `json:"person_id"`
	ProfileFingerprint string                                   `json:"profile_fingerprint"`
	AttemptState       string                                   `json:"attempt_state"`
	Outcome            personenrichment.IdentityJudgmentOutcome `json:"outcome"`
	ExactClass         personenrichment.IdentifierClass         `json:"exact_class"`
	NameCompatible     float64                                  `json:"name_compatible"`
	CompanySame        float64                                  `json:"company_same"`
	NameConflict       float64                                  `json:"name_conflict"`
	Model              string                                   `json:"model"`
	JudgedAt           time.Time                                `json:"judged_at"`
}

// PersonEnrichmentIdentityJudgmentFilter narrows a judgment listing. Outcome
// and PersonID are optional; Limit is required and capped at 200.
type PersonEnrichmentIdentityJudgmentFilter struct {
	Outcome  personenrichment.IdentityJudgmentOutcome
	PersonID int64
	Limit    int
}

func (s *Store) insertPersonEnrichmentIdentityJudgmentTx(
	ctx context.Context, tx *loggedTx, commit personenrichment.ClaimCommit, judgedAt time.Time,
) error {
	judgment := commit.IdentityAssessment.Judgment
	if judgment == nil {
		return nil
	}
	if err := judgment.Validate(); err != nil {
		return fmt.Errorf("validate person enrichment identity judgment: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO person_enrichment_identity_judgments
		(attempt_id, person_id, profile_fingerprint, outcome, exact_class,
		 name_compatible, company_same, name_conflict, model, judged_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (attempt_id) DO NOTHING`,
		commit.AttemptID, commit.PersonID, commit.ProfileFingerprint,
		string(judgment.Outcome), string(judgment.ExactClass),
		judgment.NameCompatible, judgment.CompanySame, judgment.NameConflict,
		judgment.Model, judgedAt); err != nil {
		return fmt.Errorf("record person enrichment identity judgment: %w", err)
	}
	return nil
}

const personEnrichmentIdentityJudgmentSelect = `SELECT j.attempt_id, j.person_id, j.profile_fingerprint,
	a.state, j.outcome, j.exact_class, j.name_compatible, j.company_same, j.name_conflict,
	j.model, j.judged_at
	FROM person_enrichment_identity_judgments j
	JOIN person_enrichment_attempts a ON a.id = j.attempt_id`

// GetPersonEnrichmentIdentityJudgmentContext returns the judgment recorded
// for one attempt, if any.
func (s *Store) GetPersonEnrichmentIdentityJudgmentContext(
	ctx context.Context, attemptID int64,
) (*PersonEnrichmentIdentityJudgment, error) {
	if attemptID <= 0 {
		return nil, errors.New("person enrichment attempt ID must be positive")
	}
	judgment, err := scanPersonEnrichmentIdentityJudgment(s.db.QueryRowContext(ctx,
		personEnrichmentIdentityJudgmentSelect+` WHERE j.attempt_id = ?`, attemptID))
	if err != nil {
		return nil, fmt.Errorf("get person enrichment identity judgment: %w", err)
	}
	return judgment, nil
}

// ListPersonEnrichmentIdentityJudgmentsContext lists judgments newest first,
// optionally narrowed to one outcome or person. The enrichment status
// command uses it to surface identity_uncertain attempts for review.
func (s *Store) ListPersonEnrichmentIdentityJudgmentsContext(
	ctx context.Context, filter PersonEnrichmentIdentityJudgmentFilter,
) ([]PersonEnrichmentIdentityJudgment, error) {
	if filter.Limit < 1 || filter.Limit > 200 || filter.PersonID < 0 {
		return nil, errors.New("person enrichment identity judgment filter is invalid")
	}
	switch filter.Outcome {
	case "", personenrichment.IdentityJudgmentAccepted, personenrichment.IdentityJudgmentUncertain,
		personenrichment.IdentityJudgmentRejected:
	default:
		return nil, errors.New("person enrichment identity judgment filter is invalid")
	}
	rows, err := s.db.QueryContext(ctx, personEnrichmentIdentityJudgmentSelect+`
		WHERE (? = '' OR j.outcome = ?) AND (? = 0 OR j.person_id = ?)
		ORDER BY j.judged_at DESC, j.attempt_id DESC LIMIT ?`,
		string(filter.Outcome), string(filter.Outcome), filter.PersonID, filter.PersonID, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("list person enrichment identity judgments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]PersonEnrichmentIdentityJudgment, 0)
	for rows.Next() {
		judgment, err := scanPersonEnrichmentIdentityJudgment(rows)
		if err != nil {
			return nil, fmt.Errorf("scan person enrichment identity judgment: %w", err)
		}
		result = append(result, *judgment)
	}
	return result, rows.Err()
}

func scanPersonEnrichmentIdentityJudgment(row scanner) (*PersonEnrichmentIdentityJudgment, error) {
	var (
		judgment            PersonEnrichmentIdentityJudgment
		outcome, exactClass string
		judgedAt            nullableTimestamp
	)
	if err := row.Scan(&judgment.AttemptID, &judgment.PersonID, &judgment.ProfileFingerprint,
		&judgment.AttemptState, &outcome, &exactClass, &judgment.NameCompatible,
		&judgment.CompanySame, &judgment.NameConflict, &judgment.Model, &judgedAt); err != nil {
		return nil, err
	}
	if !judgedAt.Valid {
		return nil, errors.New("person enrichment identity judgment has invalid judged_at")
	}
	judgment.Outcome = personenrichment.IdentityJudgmentOutcome(outcome)
	judgment.ExactClass = personenrichment.IdentifierClass(exactClass)
	judgment.JudgedAt = judgedAt.Time.UTC()
	return &judgment, nil
}
