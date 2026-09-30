package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// PersonProfileJudgmentKind names which profile choice a stored
// person_profile_choices judgment made.
type PersonProfileJudgmentKind string

const (
	// ProfileJudgmentPrimaryRole chose the primary current employment.
	ProfileJudgmentPrimaryRole PersonProfileJudgmentKind = "primary_role"
	// ProfileJudgmentDisplayName chose the display name of a promoted person.
	ProfileJudgmentDisplayName PersonProfileJudgmentKind = "display_name"
)

// MaxPrimaryRoleOptions and MaxDisplayNameOptions bound one choice. A person
// with more options is not judged.
const (
	MaxPrimaryRoleOptions = 6
	MaxDisplayNameOptions = 6
)

// PersonMergeConflictJudgeActor is the reviewed_by of a merge conflict the
// person_profile_choices judgment resolved.
const PersonMergeConflictJudgeActor = "jev"

// ErrPersonProfileJudgmentInvalid reports a malformed profile judgment.
var ErrPersonProfileJudgmentInvalid = errors.New("invalid person profile judgment")

// PrimaryRoleOption is one current, system-set employment offered as the
// primary role.
type PrimaryRoleOption struct {
	EmploymentID int64
	Organization string
	Title        string
	// Start is the start date as YYYY or YYYY-MM, or empty.
	Start     string
	IsPrimary bool
}

// PrimaryRoleCandidate is a person whose primary current role may be chosen:
// two or more current employments, none of them user-declared, and no pin
// on the person's employment.
type PrimaryRoleCandidate struct {
	PersonID    int64
	Roles       []PrimaryRoleOption
	Fingerprint string
}

// PrimaryRoleJudgment records one primary-role judgment. EmploymentID is
// the chosen employment when the choice met the caller's threshold, else
// nil.
type PrimaryRoleJudgment struct {
	PersonID      int64
	Fingerprint   string
	EmploymentID  *int64
	Confidence    float64
	Probabilities map[string]float64
	Model         string
}

// DisplayNameCandidate is a promoted person whose display name was chosen
// by rule from two or more distinct names and has not changed since.
type DisplayNameCandidate struct {
	PersonID    int64
	Current     string
	Names       []string
	Fingerprint string
}

// DisplayNameJudgment records one display-name judgment. Name is the chosen
// name when the choice met the caller's threshold, else nil.
type DisplayNameJudgment struct {
	PersonID      int64
	Fingerprint   string
	Name          *string
	Confidence    float64
	Probabilities map[string]float64
	Model         string
}

// MergeConflictCandidate is a pending attribute conflict left by a person
// merge whose two values can be compared: scalar values of a non-sensitive
// attribute, the absorbed value not user-declared.
type MergeConflictCandidate struct {
	CandidateID int64
	PersonID    int64
	Field       string
	Survivor    string
	Absorbed    string
}

// MergeConflictJudgment records one merge-conflict judgment. Resolve keeps
// the survivor's value (rejects the absorbed one) because the two state the
// same fact.
type MergeConflictJudgment struct {
	CandidateID int64
	PersonID    int64
	Probability float64
	Model       string
	Resolve     bool
}

func declaredProvenanceSQL(column string) string {
	return column + ` IN ('` + string(ProvenanceUser) + `', '` + string(ProvenanceCardDAVImport) +
		`', '` + string(ProvenanceVCardImport) + `')`
}

// PrimaryRoleCandidatesContext lists people whose primary current role can
// be chosen and whose current roles changed since their last judgment, at
// most limit (0 means no cap).
func (s *Store) PrimaryRoleCandidatesContext(ctx context.Context, limit int) ([]PrimaryRoleCandidate, error) {
	candidates := []PrimaryRoleCandidate{}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		rows, err := tx.QueryContext(ctx, `SELECT e.person_id FROM employments e
			WHERE `+s.dialect.BoolTrueExpr("e.is_current")+`
			GROUP BY e.person_id
			HAVING COUNT(*) >= 2 AND SUM(CASE WHEN `+declaredProvenanceSQL("e.source")+` THEN 1 ELSE 0 END) = 0
			ORDER BY e.person_id`)
		if err != nil {
			return fmt.Errorf("list primary role candidates: %w", err)
		}
		people := []int64{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan primary role candidate: %w", err)
			}
			people = append(people, id)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close primary role candidates: %w", err)
		}
		judged, err := profileJudgmentFingerprintsTx(ctx, tx, ProfileJudgmentPrimaryRole)
		if err != nil {
			return err
		}
		for _, personID := range people {
			if limit > 0 && len(candidates) >= limit {
				break
			}
			roles, eligible, err := s.primaryRoleOptionsTx(ctx, tx, personID)
			if err != nil {
				return err
			}
			if !eligible {
				continue
			}
			fingerprint := primaryRoleFingerprint(roles)
			if judged[personID] == fingerprint {
				continue
			}
			candidates = append(candidates, PrimaryRoleCandidate{
				PersonID: personID, Roles: roles, Fingerprint: fingerprint,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

// primaryRoleOptionsTx loads a person's current employments and reports
// whether a primary role may be chosen: two to MaxPrimaryRoleOptions current
// roles, none user-declared, and the employment target not pinned.
func (s *Store) primaryRoleOptionsTx(
	ctx context.Context, tx *loggedTx, personID int64,
) ([]PrimaryRoleOption, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.id, COALESCE(o.name, ''), COALESCE(e.title, ''),
			e.start_year, e.start_month, `+s.dialect.BoolTrueExpr("e.is_primary")+`, e.source
		FROM employments e LEFT JOIN organizations o ON o.id = e.organization_id
		WHERE e.person_id = ? AND `+s.dialect.BoolTrueExpr("e.is_current")+`
		ORDER BY e.id`, personID)
	if err != nil {
		return nil, false, fmt.Errorf("load current employments: %w", err)
	}
	roles := []PrimaryRoleOption{}
	declared := false
	for rows.Next() {
		var role PrimaryRoleOption
		var year, month sql.NullInt64
		var source string
		if err := rows.Scan(&role.EmploymentID, &role.Organization, &role.Title,
			&year, &month, &role.IsPrimary, &source); err != nil {
			_ = rows.Close()
			return nil, false, fmt.Errorf("scan current employment: %w", err)
		}
		if Provenance(source).IsDeclared() {
			declared = true
		}
		if year.Valid {
			role.Start = strconv.FormatInt(year.Int64, 10)
			if month.Valid {
				role.Start += fmt.Sprintf("-%02d", month.Int64)
			}
		}
		roles = append(roles, role)
	}
	if err := rows.Close(); err != nil {
		return nil, false, fmt.Errorf("close current employments: %w", err)
	}
	if declared || len(roles) < 2 || len(roles) > MaxPrimaryRoleOptions {
		return roles, false, nil
	}
	pinned, err := employmentPinnedTx(ctx, tx, personID)
	if err != nil {
		return nil, false, err
	}
	return roles, !pinned, nil
}

// employmentPinnedTx reports whether the person's latest employment pin
// event pins it.
func employmentPinnedTx(ctx context.Context, tx *loggedTx, personID int64) (bool, error) {
	target, err := personFactEmploymentTargetRef()
	if err != nil {
		return false, err
	}
	var pinned bool
	err = tx.QueryRowContext(ctx, `SELECT pinned FROM person_fact_pin_events
		WHERE person_id = ? AND target_kind = ? AND target_key = ?
		ORDER BY id DESC LIMIT 1`, personID, target.Kind, target.Key).Scan(&pinned)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read employment pin: %w", err)
	}
	return pinned, nil
}

// primaryRoleFingerprint hashes what the judgment sees, not which role is
// currently primary, so the store's own choice does not trigger a new one.
func primaryRoleFingerprint(roles []PrimaryRoleOption) string {
	hash := sha256.New()
	for _, role := range roles {
		_, _ = fmt.Fprintf(hash, "%d\x1f%s\x1f%s\x1f%s\x00", role.EmploymentID, role.Organization, role.Title, role.Start)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func profileJudgmentFingerprintsTx(
	ctx context.Context, tx *loggedTx, kind PersonProfileJudgmentKind,
) (map[int64]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT person_id, inputs_fingerprint
		FROM person_profile_judgments WHERE kind = ?`, kind)
	if err != nil {
		return nil, fmt.Errorf("load person profile judgments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	judged := map[int64]string{}
	for rows.Next() {
		var personID int64
		var fingerprint string
		if err := rows.Scan(&personID, &fingerprint); err != nil {
			return nil, fmt.Errorf("scan person profile judgment: %w", err)
		}
		judged[personID] = fingerprint
	}
	return judged, rows.Err()
}

func (s *Store) recordProfileJudgmentTx(
	ctx context.Context, tx *loggedTx, personID int64, kind PersonProfileJudgmentKind,
	fingerprint string, choice *string, confidence float64, probabilities map[string]float64,
	model string, applied bool,
) error {
	encoded := []byte("{}")
	if probabilities != nil {
		var err error
		encoded, err = json.Marshal(probabilities, json.Deterministic(true))
		if err != nil {
			return fmt.Errorf("encode profile judgment probabilities: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO person_profile_judgments
			(person_id, kind, inputs_fingerprint, choice, confidence, probabilities_json, model, applied, judged_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, `+s.dialect.Now()+`)
		ON CONFLICT (person_id, kind) DO UPDATE SET
			inputs_fingerprint = excluded.inputs_fingerprint, choice = excluded.choice,
			confidence = excluded.confidence, probabilities_json = excluded.probabilities_json,
			model = excluded.model, applied = excluded.applied, judged_at = excluded.judged_at`,
		personID, kind, fingerprint, choice, confidence, string(encoded), model, applied); err != nil {
		return fmt.Errorf("record person profile judgment: %w", err)
	}
	return nil
}

func validProfileJudgment(personID int64, fingerprint, model string, confidence float64) bool {
	return personID > 0 && fingerprint != "" && strings.TrimSpace(model) != "" &&
		confidence >= 0 && confidence <= 1
}

// ApplyPrimaryRoleJudgmentContext records the judgment and, when it chose a
// role, makes that role the primary current employment: only if the
// person's current roles are still exactly the ones judged, none is
// user-declared, and the employment is not pinned. It never records a pin,
// so a later user choice always wins. It reports whether the primary role
// changed.
func (s *Store) ApplyPrimaryRoleJudgmentContext(ctx context.Context, judgment PrimaryRoleJudgment) (bool, error) {
	if !validProfileJudgment(judgment.PersonID, judgment.Fingerprint, judgment.Model, judgment.Confidence) {
		return false, ErrPersonProfileJudgmentInvalid
	}
	changed, err := retryContendedWrite(ctx, s, "apply primary role judgment", func() (*bool, error) {
		changed := false
		err := s.withTxContext(ctx, func(tx *loggedTx) error {
			if err := s.claimEmploymentPeopleTx(ctx, tx, judgment.PersonID); err != nil {
				return err
			}
			roles, eligible, err := s.primaryRoleOptionsTx(ctx, tx, judgment.PersonID)
			if err != nil {
				return err
			}
			var choice *string
			if judgment.EmploymentID != nil {
				value := strconv.FormatInt(*judgment.EmploymentID, 10)
				choice = &value
			}
			current := eligible && primaryRoleFingerprint(roles) == judgment.Fingerprint
			chosen := -1
			if current && judgment.EmploymentID != nil {
				chosen = slices.IndexFunc(roles, func(role PrimaryRoleOption) bool {
					return role.EmploymentID == *judgment.EmploymentID
				})
			}
			if chosen >= 0 && !roles[chosen].IsPrimary {
				if err := s.promotePrimaryEmploymentTx(ctx, tx, judgment.PersonID, *judgment.EmploymentID); err != nil {
					return err
				}
				changed = true
			}
			return s.recordProfileJudgmentTx(ctx, tx, judgment.PersonID, ProfileJudgmentPrimaryRole,
				judgment.Fingerprint, choice, judgment.Confidence, judgment.Probabilities, judgment.Model,
				chosen >= 0)
		})
		return &changed, err
	})
	if err != nil {
		return false, err
	}
	return *changed, nil
}

// promotePrimaryEmploymentTx makes one current employment the person's only
// primary one, with the same revision, inference-export, and enrichment
// bookkeeping as a user's choice but without the user's pin.
func (s *Store) promotePrimaryEmploymentTx(ctx context.Context, tx *loggedTx, personID, employmentID int64) error {
	inferenceBefore, err := s.captureInferenceExportPeopleTx(ctx, tx, personID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE employments SET is_primary = ?,
		revision = revision + 1, updated_at = %s WHERE person_id = ? AND id <> ? AND %s`,
		s.dialect.Now(), s.dialect.BoolTrueExpr("is_primary")), false, personID, employmentID); err != nil {
		return fmt.Errorf("demote primary employment: %w", err)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE employments SET is_primary = ?,
		revision = revision + 1, updated_at = %s WHERE id = ? AND person_id = ? AND %s`,
		s.dialect.Now(), s.dialect.BoolTrueExpr("is_current")), true, employmentID, personID)
	if err != nil {
		return fmt.Errorf("promote employment %d: %w", employmentID, err)
	}
	if changed, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("count promoted employment: %w", err)
	} else if changed != 1 {
		return fmt.Errorf("%w: employment %d is no longer current", ErrEmploymentInvalid, employmentID)
	}
	if err := s.invalidateInferenceExportChangesTx(ctx, tx, inferenceBefore); err != nil {
		return err
	}
	return s.publishPersonIdentityEnrichmentTx(ctx, tx, personID)
}

// recordDisplayNameSeedTx remembers, for a person promoted just now, the
// display name the rule chose when the cluster used two or more distinct
// names, so the display_name judgment may later offer a better one while
// the name is still the rule's.
func (s *Store) recordDisplayNameSeedTx(
	ctx context.Context, tx *loggedTx, personID int64, seeded *string, members []int64,
) error {
	if seeded == nil {
		return nil
	}
	names, err := clusterDistinctDisplayNamesTx(ctx, tx, members)
	if err != nil {
		return err
	}
	if len(names) < 2 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, s.dialect.InsertOrIgnore(
		`INSERT OR IGNORE INTO person_display_name_seeds (person_id, seeded_name) VALUES (?, ?)`),
		personID, *seeded); err != nil {
		return fmt.Errorf("record display name seed: %w", err)
	}
	return nil
}

// dropDisplayNameSeedTx forgets a person's seed: a rename by anyone, the
// display-name judgment included, makes the name no longer the rule's
// choice, even when it is renamed back to the same text later.
func dropDisplayNameSeedTx(ctx context.Context, tx *loggedTx, personID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM person_display_name_seeds WHERE person_id = ?`, personID); err != nil {
		return fmt.Errorf("drop display name seed: %w", err)
	}
	return nil
}

// clusterDistinctDisplayNamesTx returns the distinct display names of the
// participants, compared case-insensitively, that could be a person's name:
// with a letter and without an address. Order is by participant ID.
func clusterDistinctDisplayNamesTx(ctx context.Context, tx *loggedTx, members []int64) ([]string, error) {
	names := []string{}
	seen := map[string]struct{}{}
	if err := queryInChunksContext(ctx, tx, members, nil, `
		SELECT id, TRIM(display_name) FROM participants
		WHERE id IN (%s) AND TRIM(COALESCE(display_name, '')) <> '' ORDER BY id`,
		func(rows *loggedRows) error {
			var id int64
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				return fmt.Errorf("scan cluster display name: %w", err)
			}
			name = strings.Join(strings.Fields(name), " ")
			if strings.Contains(name, "@") || !strings.ContainsFunc(name, unicode.IsLetter) {
				return nil
			}
			key := strings.ToLower(name)
			if _, ok := seen[key]; ok {
				return nil
			}
			seen[key] = struct{}{}
			names = append(names, name)
			return nil
		}); err != nil {
		return nil, fmt.Errorf("load cluster display names: %w", err)
	}
	return names, nil
}

// DisplayNameCandidatesContext lists promoted people whose display name is
// still the one the rule chose at promotion, from two or more distinct
// names, and whose names changed since their last judgment, at most limit
// (0 means no cap).
func (s *Store) DisplayNameCandidatesContext(ctx context.Context, limit int) ([]DisplayNameCandidate, error) {
	candidates := []DisplayNameCandidate{}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		rows, err := tx.QueryContext(ctx, `SELECT s.person_id, s.seeded_name FROM person_display_name_seeds s
			JOIN persons p ON p.id = s.person_id
			WHERE COALESCE(p.display_name, '') = s.seeded_name
			ORDER BY s.person_id`)
		if err != nil {
			return fmt.Errorf("list display name candidates: %w", err)
		}
		type seed struct {
			personID int64
			name     string
		}
		seeds := []seed{}
		for rows.Next() {
			var entry seed
			if err := rows.Scan(&entry.personID, &entry.name); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan display name candidate: %w", err)
			}
			seeds = append(seeds, entry)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close display name candidates: %w", err)
		}
		judged, err := profileJudgmentFingerprintsTx(ctx, tx, ProfileJudgmentDisplayName)
		if err != nil {
			return err
		}
		for _, entry := range seeds {
			if limit > 0 && len(candidates) >= limit {
				break
			}
			names, err := personDisplayNameOptionsTx(ctx, tx, entry.personID)
			if err != nil {
				return err
			}
			if len(names) < 2 || len(names) > MaxDisplayNameOptions {
				continue
			}
			fingerprint := displayNameFingerprint(names)
			if judged[entry.personID] == fingerprint {
				continue
			}
			candidates = append(candidates, DisplayNameCandidate{
				PersonID: entry.personID, Current: entry.name, Names: names, Fingerprint: fingerprint,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

func personDisplayNameOptionsTx(ctx context.Context, tx *loggedTx, personID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT participant_id FROM person_participants
		WHERE person_id = ? ORDER BY participant_id`, personID)
	if err != nil {
		return nil, fmt.Errorf("load person participants: %w", err)
	}
	members := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan person participant: %w", err)
		}
		members = append(members, id)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close person participants: %w", err)
	}
	return clusterDistinctDisplayNamesTx(ctx, tx, members)
}

func displayNameFingerprint(names []string) string {
	sorted := slices.Clone(names)
	slices.Sort(sorted)
	digest := sha256.Sum256([]byte(strings.Join(sorted, "\x00")))
	return hex.EncodeToString(digest[:])
}

// ApplyDisplayNameJudgmentContext records the judgment and, when it chose a
// name, renames the person: only if the display name is still the one the
// rule chose at promotion and the names are exactly the ones judged. A
// rename by anyone else in between wins. It reports whether the name
// changed.
func (s *Store) ApplyDisplayNameJudgmentContext(ctx context.Context, judgment DisplayNameJudgment) (bool, error) {
	if !validProfileJudgment(judgment.PersonID, judgment.Fingerprint, judgment.Model, judgment.Confidence) ||
		(judgment.Name != nil && strings.TrimSpace(*judgment.Name) == "") {
		return false, ErrPersonProfileJudgmentInvalid
	}
	var revision int64
	eligible := false
	err := retryContendedWriteErr(ctx, s, "record display name judgment", func() error {
		return s.withTxContext(ctx, func(tx *loggedTx) error {
			var current, seeded string
			err := tx.QueryRowContext(ctx, `SELECT p.revision, COALESCE(p.display_name, ''), s.seeded_name
				FROM persons p JOIN person_display_name_seeds s ON s.person_id = p.id
				WHERE p.id = ?`, judgment.PersonID).Scan(&revision, &current, &seeded)
			if errors.Is(err, sql.ErrNoRows) {
				// The person is gone or was never promoted from several names.
				eligible = false
				return nil
			}
			if err != nil {
				return fmt.Errorf("read display name seed: %w", err)
			}
			names, err := personDisplayNameOptionsTx(ctx, tx, judgment.PersonID)
			if err != nil {
				return err
			}
			eligible = current == seeded && displayNameFingerprint(names) == judgment.Fingerprint &&
				judgment.Name != nil && *judgment.Name != current && slices.Contains(names, *judgment.Name)
			return s.recordProfileJudgmentTx(ctx, tx, judgment.PersonID, ProfileJudgmentDisplayName,
				judgment.Fingerprint, judgment.Name, judgment.Confidence, judgment.Probabilities, judgment.Model,
				eligible)
		})
	})
	if err != nil || !eligible {
		return false, err
	}
	name := *judgment.Name
	if _, err := s.UpdatePersonDisplayNameContext(ctx, judgment.PersonID, revision, &name); err != nil {
		if errors.Is(err, ErrPersonRevisionConflict) || errors.Is(err, ErrPersonNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// MergeConflictCandidatesContext lists pending merge attribute conflicts the
// person_profile_choices judgment may compare and has not judged, at most
// limit (0 means no cap).
func (s *Store) MergeConflictCandidatesContext(ctx context.Context, limit int) ([]MergeConflictCandidate, error) {
	candidates := []MergeConflictCandidate{}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		rows, err := tx.QueryContext(ctx, `SELECT c.id, c.survivor_person_id, d.label,
				c.survivor_value_id, c.absorbed_value_id
			FROM person_merge_review_candidates c
			JOIN attribute_definitions d ON d.id = c.definition_id
			WHERE c.state = 'pending' AND NOT (`+s.dialect.BoolTrueExpr("d.is_sensitive")+`)
			  AND NOT EXISTS (SELECT 1 FROM person_merge_conflict_judgments j WHERE j.candidate_id = c.id)
			ORDER BY c.id`)
		if err != nil {
			return fmt.Errorf("list merge conflict candidates: %w", err)
		}
		type pending struct {
			candidate          MergeConflictCandidate
			survivor, absorbed int64
		}
		entries := []pending{}
		for rows.Next() {
			var entry pending
			if err := rows.Scan(&entry.candidate.CandidateID, &entry.candidate.PersonID,
				&entry.candidate.Field, &entry.survivor, &entry.absorbed); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan merge conflict candidate: %w", err)
			}
			entries = append(entries, entry)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close merge conflict candidates: %w", err)
		}
		for _, entry := range entries {
			if limit > 0 && len(candidates) >= limit {
				break
			}
			survivor, err := mergeConflictValueTx(ctx, tx, entry.survivor)
			if err != nil {
				return err
			}
			absorbed, err := mergeConflictValueTx(ctx, tx, entry.absorbed)
			if err != nil {
				return err
			}
			if survivor == nil || absorbed == nil || absorbed.Source.IsDeclared() {
				continue
			}
			left, leftErr := survivor.Value.CanonicalString()
			right, rightErr := absorbed.Value.CanonicalString()
			if leftErr != nil || rightErr != nil || strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
				continue
			}
			entry.candidate.Survivor, entry.candidate.Absorbed = left, right
			candidates = append(candidates, entry.candidate)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

func mergeConflictValueTx(ctx context.Context, tx *loggedTx, valueID int64) (*PersonAttributeValue, error) {
	value, err := scanPersonAttributeValue(tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT %s
		FROM person_attribute_values v
		JOIN attribute_definitions d ON d.id = v.definition_id
		WHERE v.id = ?`, personAttributeValueColumns), valueID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // a missing value is simply not comparable.
	}
	if err != nil {
		return nil, fmt.Errorf("load merge conflict value: %w", err)
	}
	switch value.Value.Type {
	case AttributeValueText, AttributeValueInteger, AttributeValueReal,
		AttributeValueBoolean, AttributeValueDate, AttributeValueTimestamp:
		return value, nil
	default:
		return nil, nil //nolint:nilnil // structured values are not compared.
	}
}

// ApplyMergeConflictJudgmentContext records the judgment and, when Resolve
// is set, keeps the survivor's value by rejecting the absorbed one as the
// same fact, with reviewer PersonMergeConflictJudgeActor. A conflict decided
// in the meantime, a user-declared absorbed value, or a concurrent profile
// change leaves it pending. It reports whether the conflict was resolved.
func (s *Store) ApplyMergeConflictJudgmentContext(ctx context.Context, judgment MergeConflictJudgment) (bool, error) {
	if judgment.CandidateID <= 0 || judgment.PersonID <= 0 || judgment.Probability < 0 ||
		judgment.Probability > 1 || strings.TrimSpace(judgment.Model) == "" {
		return false, ErrPersonProfileJudgmentInvalid
	}
	resolved := false
	if judgment.Resolve {
		var revision int64
		var absorbedSource string
		err := s.db.QueryRowContext(ctx, `SELECT p.revision, v.source
			FROM person_merge_review_candidates c
			JOIN persons p ON p.id = c.survivor_person_id
			JOIN person_attribute_values v ON v.id = c.absorbed_value_id
			WHERE c.id = ? AND c.survivor_person_id = ? AND c.state = 'pending'`,
			judgment.CandidateID, judgment.PersonID).Scan(&revision, &absorbedSource)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return false, fmt.Errorf("read merge conflict: %w", err)
		case !Provenance(absorbedSource).IsDeclared():
			_, decideErr := s.DecidePersonMergeCandidateContext(ctx, PersonMergeCandidateDecisionRequest{
				CandidateID: judgment.CandidateID, PersonID: judgment.PersonID,
				ExpectedPersonRevision: revision, Decision: PersonMergeCandidateReject,
				Actor: PersonMergeConflictJudgeActor,
			})
			switch {
			case decideErr == nil:
				resolved = true
			case errors.Is(decideErr, ErrPersonRevisionConflict), errors.Is(decideErr, ErrPersonMergeCandidateState),
				errors.Is(decideErr, ErrPersonMergeCandidateNotFound), errors.Is(decideErr, ErrPersonNotFound):
			default:
				return false, decideErr
			}
		}
	}
	err := retryBusyWriteErr(ctx, s, "record merge conflict judgment", func() error {
		_, err := s.db.ExecContext(ctx, s.dialect.InsertOrIgnore(`INSERT OR IGNORE INTO person_merge_conflict_judgments
			(candidate_id, probability, model, resolved) VALUES (?, ?, ?, ?)`),
			judgment.CandidateID, judgment.Probability, judgment.Model, resolved)
		return err
	})
	if err != nil {
		return resolved, fmt.Errorf("record merge conflict judgment: %w", err)
	}
	return resolved, nil
}
