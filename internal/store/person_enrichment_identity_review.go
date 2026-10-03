package store

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/personenrichment"
	"go.kenn.io/msgvault/internal/personfacts"
)

// Enrichment identity review lets a user decide an attempt whose semantic
// identity check landed between the accept and reject thresholds
// (identity_uncertain). Confirming commits the attempt's stored claims as a
// verified identity; rejecting records that this provider identity is not
// the person. Both are user actions only.

const (
	// PersonEnrichmentIdentityConfirmedReason is the recorded reason of a
	// user-confirmed identity.
	PersonEnrichmentIdentityConfirmedReason = "user_confirmed"
	// PersonEnrichmentIdentityRejectedReason is the recorded reason of a
	// user-rejected identity.
	PersonEnrichmentIdentityRejectedReason = "user_rejected"
	// PersonEnrichmentUserConfirmedIdentityScore is the identity score and
	// provider-ID confidence of a user-confirmed identity: the verified-ID
	// level, above personenrichment.VerifiedProviderPersonIDConfidence.
	PersonEnrichmentUserConfirmedIdentityScore = 1000

	personEnrichmentRejectionProviderID = "provider_person_id"
	personEnrichmentRejectionProfileURL = "profile_url"
	maxPersonEnrichmentReviewLimit      = 200
)

var (
	// ErrPersonEnrichmentReviewNotFound reports an unknown attempt.
	ErrPersonEnrichmentReviewNotFound = errors.New("person enrichment attempt not found")
	// ErrPersonEnrichmentReviewStateChanged reports that the attempt is no
	// longer awaiting an identity decision.
	ErrPersonEnrichmentReviewStateChanged = errors.New(
		"person enrichment attempt is no longer awaiting an identity decision")
	// ErrPersonEnrichmentIdentityOwnedElsewhere reports that the returned
	// provider identity already belongs to another person.
	ErrPersonEnrichmentIdentityOwnedElsewhere = errors.New(
		"the provider identity is already attached to another person")
)

// PersonEnrichmentReturnedRole is one current role the provider returned.
type PersonEnrichmentReturnedRole struct {
	Title   string `json:"title,omitempty"`
	Company string `json:"company,omitempty"`
}

// PersonEnrichmentReturnedIdentity is the provider identity as far as the
// archive stored it: derived from the attempt's claims and the host of its
// evidence URL. The adapter's raw returned identity is never stored.
type PersonEnrichmentReturnedIdentity struct {
	Name           *string                        `json:"name,omitzero" nullable:"false"`
	CurrentRoles   []PersonEnrichmentReturnedRole `json:"current_roles"`
	Location       *string                        `json:"location,omitzero" nullable:"false"`
	ProfileURLHost *string                        `json:"profile_url_host,omitzero" nullable:"false"`
}

// PersonEnrichmentReviewClaim is one stored claim, shown as target and text.
type PersonEnrichmentReviewClaim struct {
	Target string `json:"target"`
	Value  string `json:"value"`
}

// PersonEnrichmentIdentityReview is one attempt awaiting an identity
// decision, with the stored judgment and what the provider returned.
type PersonEnrichmentIdentityReview struct {
	AttemptID             int64                            `json:"attempt_id"`
	PersonID              int64                            `json:"person_id"`
	PersonDisplayName     *string                          `json:"person_display_name,omitzero" nullable:"false"`
	ProviderName          string                           `json:"provider_name"`
	ProviderKind          string                           `json:"provider_kind"`
	CompletedAt           *time.Time                       `json:"completed_at,omitempty"`
	ExactClass            string                           `json:"exact_class"`
	NameCompatible        float64                          `json:"name_compatible"`
	CompanySame           float64                          `json:"company_same"`
	NameConflict          float64                          `json:"name_conflict"`
	Model                 string                           `json:"model"`
	JudgedAt              time.Time                        `json:"judged_at"`
	Returned              PersonEnrichmentReturnedIdentity `json:"returned"`
	Claims                []PersonEnrichmentReviewClaim    `json:"claims"`
	ProviderPersonIDKnown bool                             `json:"provider_person_id_known"`
}

// PersonEnrichmentIdentityDecision is the outcome of a confirm or reject.
type PersonEnrichmentIdentityDecision struct {
	AttemptID         int64   `json:"attempt_id"`
	PersonID          int64   `json:"person_id"`
	Decision          string  `json:"decision" enum:"confirmed,rejected"`
	Reason            string  `json:"reason" enum:"user_confirmed,user_rejected"`
	AttemptState      string  `json:"attempt_state"`
	FactGenerationKey *string `json:"fact_generation_key,omitzero" nullable:"false"`
	// Projections counts values the confirmed claims projected onto the
	// person. Rejections leave it zero.
	Projections int `json:"projections"`
	// ProviderIdentitiesAttached counts provider person IDs attached at the
	// verified confidence by a confirmation.
	ProviderIdentitiesAttached int `json:"provider_identities_attached"`
	// Negatives counts per-person negatives a rejection recorded.
	Negatives int `json:"negatives"`
}

// ListPersonEnrichmentIdentityReviewsContext lists attempts awaiting an
// identity decision, newest first.
func (s *Store) ListPersonEnrichmentIdentityReviewsContext(
	ctx context.Context, limit int,
) ([]PersonEnrichmentIdentityReview, error) {
	if limit < 1 || limit > maxPersonEnrichmentReviewLimit {
		return nil, fmt.Errorf("person enrichment review limit must be in [1,%d]",
			maxPersonEnrichmentReviewLimit)
	}
	reviews := []PersonEnrichmentIdentityReview{}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		rows, err := tx.QueryContext(ctx, `SELECT a.id, a.person_id, a.profile_fingerprint,
			a.fact_generation_key, a.completed_at, j.exact_class, j.name_compatible,
			j.company_same, j.name_conflict, j.model, j.judged_at
			FROM person_enrichment_attempts a
			JOIN person_enrichment_identity_judgments j ON j.attempt_id = a.id
			WHERE a.state = ?
			ORDER BY j.judged_at DESC, a.id DESC LIMIT ?`,
			personEnrichmentStateIdentityUncertain, limit)
		if err != nil {
			return fmt.Errorf("list person enrichment identity reviews: %w", err)
		}
		type pending struct {
			review        PersonEnrichmentIdentityReview
			fingerprint   string
			generationKey sql.NullString
		}
		items := []pending{}
		for rows.Next() {
			var item pending
			var completed, judged nullableTimestamp
			if err := rows.Scan(&item.review.AttemptID, &item.review.PersonID, &item.fingerprint,
				&item.generationKey, &completed, &item.review.ExactClass,
				&item.review.NameCompatible, &item.review.CompanySame,
				&item.review.NameConflict, &item.review.Model, &judged); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan person enrichment identity review: %w", err)
			}
			item.review.CompletedAt = optionalTimestamp(completed)
			if judged.Valid {
				item.review.JudgedAt = judged.Time.UTC()
			}
			items = append(items, item)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close person enrichment identity reviews: %w", err)
		}
		profiles := map[string]personenrichment.ProviderProfile{}
		for _, item := range items {
			review := item.review
			profile, ok := profiles[item.fingerprint]
			if !ok {
				profile, err = s.loadPersonEnrichmentProfile(ctx, tx, item.fingerprint)
				if err != nil {
					return err
				}
				profiles[item.fingerprint] = profile
			}
			review.ProviderName, review.ProviderKind = profile.Name, profile.Kind
			person, err := s.getPersonTx(ctx, tx, review.PersonID)
			if err != nil {
				return err
			}
			review.PersonDisplayName = person.DisplayName
			review.Returned.CurrentRoles = []PersonEnrichmentReturnedRole{}
			review.Claims = []PersonEnrichmentReviewClaim{}
			if item.generationKey.Valid {
				generation, err := s.loadPersonFactGenerationTx(
					ctx, tx, review.PersonID, item.generationKey.String)
				if err != nil {
					return err
				}
				review.Returned, review.Claims = summarizeReturnedIdentity(generation, profile)
			}
			known, err := attemptProviderIDsTx(ctx, tx, review.AttemptID)
			if err != nil {
				return err
			}
			review.ProviderPersonIDKnown = len(known) > 0
			reviews = append(reviews, review)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return reviews, nil
}

func summarizeReturnedIdentity(
	generation personFactLedgerGeneration, profile personenrichment.ProviderProfile,
) (PersonEnrichmentReturnedIdentity, []PersonEnrichmentReviewClaim) {
	returned := PersonEnrichmentReturnedIdentity{CurrentRoles: []PersonEnrichmentReturnedRole{}}
	claims := []PersonEnrichmentReviewClaim{}
	targets := map[string]personfacts.TargetDescriptor{}
	for _, target := range profile.Targets {
		targets[target.Key] = target
	}
	var first, last string
	for _, claim := range generation.Claims {
		target := targets[claim.Target.Key]
		label := target.Slug
		if label == "" {
			label = claim.Target.Key
		}
		if claim.Target.Kind == personfacts.TargetEmployment {
			label = "employment"
			var employment personfacts.EmploymentValue
			if err := json.Unmarshal(claim.SubmittedValue, &employment); err == nil {
				role := PersonEnrichmentReturnedRole{
					Title: employment.Title, Company: employment.Organization.Name,
				}
				if employment.EndDate == nil && (role.Title != "" || role.Company != "") {
					returned.CurrentRoles = append(returned.CurrentRoles, role)
				}
				claims = append(claims, PersonEnrichmentReviewClaim{
					Target: label, Value: strings.TrimSpace(strings.Join(
						nonEmpty(role.Title, role.Company), " at ")),
				})
				continue
			}
		}
		value := claimText(claim.SubmittedValue)
		claims = append(claims, PersonEnrichmentReviewClaim{Target: label, Value: value})
		switch label {
		case "name", "full_name":
			returned.Name = &value
		case "first_name":
			first = value
		case "last_name":
			last = value
		case AttributeSlugLocation:
			returned.Location = &value
		}
	}
	if returned.Name == nil && (first != "" || last != "") {
		name := strings.TrimSpace(first + " " + last)
		returned.Name = &name
	}
	for _, evidence := range generation.Evidence {
		if host := urlHost(evidence.Input.SourceURL); host != "" {
			returned.ProfileURLHost = &host
			break
		}
	}
	return returned, claims
}

func nonEmpty(values ...string) []string {
	result := []string{}
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, strings.TrimSpace(value))
		}
	}
	return result
}

func claimText(value jsontext.Value) string {
	var text string
	if err := json.Unmarshal(value, &text); err == nil {
		return text
	}
	return string(value)
}

func urlHost(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

type attemptProviderID struct {
	namespace, id string
	confidence    int
}

func attemptProviderIDsTx(
	ctx context.Context, tx *loggedTx, attemptID int64,
) ([]attemptProviderID, error) {
	rows, err := tx.QueryContext(ctx, `SELECT provider_namespace, provider_person_id, confidence
		FROM person_enrichment_attempt_provider_ids WHERE attempt_id = ?
		ORDER BY provider_namespace, provider_person_id`, attemptID)
	if err != nil {
		return nil, fmt.Errorf("load attempt provider identities: %w", err)
	}
	defer func() { _ = rows.Close() }()
	ids := []attemptProviderID{}
	for rows.Next() {
		var id attemptProviderID
		if err := rows.Scan(&id.namespace, &id.id, &id.confidence); err != nil {
			return nil, fmt.Errorf("scan attempt provider identity: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// recordUncertainAttemptProviderIDsTx keeps the provider person IDs an
// identity_uncertain result returned, so a later user decision can use them.
func recordUncertainAttemptProviderIDsTx(
	ctx context.Context, tx *loggedTx, commit personenrichment.ClaimCommit,
) error {
	for _, identity := range commit.Result().ProviderPersonIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO person_enrichment_attempt_provider_ids
			(attempt_id, person_id, provider_namespace, provider_person_id, confidence)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (attempt_id, provider_namespace, provider_person_id) DO NOTHING`,
			commit.AttemptID, commit.PersonID, commit.ProviderNamespace,
			identity.ID, identity.Confidence); err != nil {
			return fmt.Errorf("record uncertain attempt provider identity: %w", err)
		}
	}
	return nil
}

// personEnrichmentIdentityRefusedTx reports whether the user rejected any of
// the result's provider identities for this person, by provider person ID
// or, for legacy decisions, by profile URL.
func personEnrichmentIdentityRefusedTx(
	ctx context.Context, tx *loggedTx, personID int64, namespace string,
	providerIDs []personenrichment.ProviderPersonID, urls []string,
) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key_kind, key_value
		FROM person_enrichment_identity_rejections
		WHERE person_id = ? AND provider_namespace = ?`, personID, namespace)
	if err != nil {
		return false, fmt.Errorf("load person enrichment identity rejections: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind, value string
		if err := rows.Scan(&kind, &value); err != nil {
			return false, fmt.Errorf("scan person enrichment identity rejection: %w", err)
		}
		switch kind {
		case personEnrichmentRejectionProviderID:
			if slices.ContainsFunc(providerIDs, func(id personenrichment.ProviderPersonID) bool {
				return id.ID == value
			}) {
				return true, nil
			}
		case personEnrichmentRejectionProfileURL:
			stored := normalizeRejectedProfileURL(value)
			if stored != "" && slices.ContainsFunc(urls, func(candidate string) bool {
				return normalizeRejectedProfileURL(candidate) == stored
			}) {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}

// normalizeRejectedProfileURL gives equivalent profile URLs one form, applied
// to both a stored negative and an incoming URL (RFC 3986 section 6.2.2):
// the scheme and host are lowercased, percent-encoded unreserved characters
// are decoded, other escapes keep their encoding with uppercase hex, the
// fragment is dropped, and a trailing slash is trimmed. The path otherwise
// stays case-sensitive.
func normalizeRejectedProfileURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return ""
	}
	path := normalizePercentEncoding(parsed.EscapedPath())
	path = strings.TrimSuffix(path, "/")
	query := normalizePercentEncoding(parsed.RawQuery)
	normalized := strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host) + path
	if query != "" {
		normalized += "?" + query
	}
	return normalized
}

// normalizePercentEncoding decodes percent-escapes of RFC 3986 unreserved
// characters and uppercases the hex digits of every other escape.
func normalizePercentEncoding(escaped string) string {
	var builder strings.Builder
	for i := 0; i < len(escaped); i++ {
		if escaped[i] != '%' || i+2 >= len(escaped) || !isHexDigit(escaped[i+1]) || !isHexDigit(escaped[i+2]) {
			builder.WriteByte(escaped[i])
			continue
		}
		decoded := hexValue(escaped[i+1])<<4 | hexValue(escaped[i+2])
		if isUnreservedURLByte(decoded) {
			builder.WriteByte(decoded)
		} else {
			builder.WriteString(strings.ToUpper(escaped[i : i+3]))
		}
		i += 2
	}
	return builder.String()
}

func isUnreservedURLByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || value == '-' || value == '.' || value == '_' || value == '~'
}

func isHexDigit(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func hexValue(value byte) byte {
	switch {
	case value >= '0' && value <= '9':
		return value - '0'
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10
	default:
		return value - 'A' + 10
	}
}

type reviewAttempt struct {
	attempt personEnrichmentCommitAttempt
	profile personenrichment.ProviderProfile
}

// lockPersonEnrichmentReviewTx takes the commit path's lock order and loads
// an attempt that must still be identity_uncertain.
func (s *Store) lockPersonEnrichmentReviewTx(
	ctx context.Context, tx *loggedTx, attemptID int64,
) (reviewAttempt, error) {
	if err := s.lockPersonEnrichmentAuthorityMutationTx(ctx, tx); err != nil {
		return reviewAttempt{}, err
	}
	if err := s.lockAttributeDefinitionCatalogTx(ctx, tx, false); err != nil {
		return reviewAttempt{}, err
	}
	unlocked, err := s.loadPersonEnrichmentCommitAttempt(ctx, tx, attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return reviewAttempt{}, ErrPersonEnrichmentReviewNotFound
	}
	if err != nil {
		return reviewAttempt{}, err
	}
	if _, err := lockPersonEnrichmentPersonTx(ctx, tx, unlocked.PersonID); err != nil {
		return reviewAttempt{}, err
	}
	if err := lockEnrichmentWorkRowForOrderingTx(
		ctx, tx, unlocked.PersonID, unlocked.ProfileFingerprint); err != nil {
		return reviewAttempt{}, err
	}
	attempt, err := s.loadPersonEnrichmentCommitAttempt(ctx, tx, attemptID)
	if err != nil {
		return reviewAttempt{}, err
	}
	if attempt.State != personEnrichmentStateIdentityUncertain {
		return reviewAttempt{}, ErrPersonEnrichmentReviewStateChanged
	}
	profile, err := s.loadPersonEnrichmentProfile(ctx, tx, attempt.ProfileFingerprint)
	if err != nil {
		return reviewAttempt{}, err
	}
	return reviewAttempt{attempt: attempt, profile: profile}, nil
}

// transitionReviewedAttemptTx moves the attempt out of identity_uncertain.
// The state guard makes a decision that lost a race fail instead of
// overwriting the winner.
func (s *Store) transitionReviewedAttemptTx(
	ctx context.Context, tx *loggedTx, attemptID int64, state string,
	failure *string, generationKey string,
) error {
	if s.personEnrichmentReviewBeforeUpdateHook != nil {
		s.personEnrichmentReviewBeforeUpdateHook(tx)
	}
	var failureValue any
	if failure != nil {
		failureValue = *failure
	}
	result, err := tx.ExecContext(ctx, `UPDATE person_enrichment_attempts
		SET state = ?, failure_class = ?, fact_generation_key = ?
		WHERE id = ? AND state = ?`,
		state, failureValue, nullableTrimmed(generationKey), attemptID,
		personEnrichmentStateIdentityUncertain)
	if err != nil {
		return fmt.Errorf("record person enrichment identity decision: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count person enrichment identity decision: %w", err)
	}
	if changed != 1 {
		return ErrPersonEnrichmentReviewStateChanged
	}
	return nil
}

func (s *Store) recordPersonEnrichmentReviewTx(
	ctx context.Context, tx *loggedTx, attempt personEnrichmentCommitAttempt,
	decision, reason, actor, generationKey string, decidedAt time.Time,
) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO person_enrichment_identity_reviews
		(attempt_id, person_id, decision, reason, actor, fact_generation_key, decided_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		attempt.ID, attempt.PersonID, decision, reason, actor,
		nullableTrimmed(generationKey), decidedAt); err != nil {
		return fmt.Errorf("record person enrichment identity review: %w", err)
	}
	return nil
}

// refreshPersonEnrichmentRunCountsTx keeps the run's counters in step with
// an attempt decided by review. It locks the run row first, as CompleteRun
// does, and recomputes whether the run is running or completed: whichever
// of the two transactions takes the lock second counts the other's committed
// attempt state, so neither can leave stale counts behind.
func (s *Store) refreshPersonEnrichmentRunCountsTx(ctx context.Context, tx *loggedTx, runID int64) error {
	var state string
	if err := tx.QueryRowContext(ctx, "SELECT state FROM person_enrichment_runs WHERE id = ?", runID).Scan(&state); err != nil {
		return fmt.Errorf("lock person enrichment run: %w", err)
	}
	outcome, err := derivePersonEnrichmentRunOutcomeTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE person_enrichment_runs SET
		succeeded_count = ?, failed_count = ?, suppressed_count = ?, identity_rejected_count = ?
		WHERE id = ?`,
		outcome.succeeded, outcome.failed, outcome.suppressed, outcome.rejected, runID); err != nil {
		return fmt.Errorf("refresh person enrichment run counts: %w", err)
	}
	return nil
}

// storedEvidenceAligner re-accepts exactly the evidence the original
// commit already aligned, with its stored version and content hash.
type storedEvidenceAligner struct {
	stored map[string]personfacts.EvidenceInput
}

func storedEvidenceKey(input personfacts.EvidenceInput) string {
	return string(input.SourceClass) + "\x00" + input.SourceRef + "\x00" + input.SourceURL +
		"\x00" + input.ContentSHA256
}

func (a storedEvidenceAligner) Align(
	_ context.Context, input personfacts.EvidenceInput,
) (personfacts.AlignmentResult, error) {
	stored, ok := a.stored[storedEvidenceKey(input)]
	if !ok {
		return personfacts.AlignmentResult{Failure: &personfacts.ValidationFailure{
			Action: personfacts.DecisionInvalid, Reason: personfacts.ReasonUnalignedEvidence,
			Detail: "evidence is not part of the reviewed attempt",
		}}, nil
	}
	return personfacts.AlignmentResult{
		Accepted: true, SourceVersion: stored.SourceVersion, ContentSHA256: stored.ContentSHA256,
	}, nil
}

// confirmedEnrichmentGeneration rebuilds the attempt's stored claims with
// the user-confirmed identity score.
func confirmedEnrichmentGeneration(
	ctx context.Context, generation personFactLedgerGeneration,
	profile personenrichment.ProviderProfile, resolvedAt time.Time,
) (personfacts.PreparedGeneration, error) {
	targets := map[string]personfacts.TargetDescriptor{}
	for _, target := range profile.Targets {
		targets[target.Key] = target
	}
	evidence := map[int64]personfacts.EvidenceInput{}
	aligner := storedEvidenceAligner{stored: map[string]personfacts.EvidenceInput{}}
	for _, item := range generation.Evidence {
		evidence[item.ID] = item.Input
		aligner.stored[storedEvidenceKey(item.Input)] = item.Input
	}
	claims := []personfacts.ProposedClaim{}
	for _, claim := range generation.Claims {
		if claim.Failure != nil {
			continue
		}
		target, ok := targets[claim.Target.Key]
		if !ok || target.Kind != claim.Target.Kind {
			continue
		}
		target.Revision = claim.Target.Revision
		inputs := []personfacts.EvidenceInput{}
		for _, id := range claim.EvidenceIDs {
			if input, ok := evidence[id]; ok {
				inputs = append(inputs, input)
			}
		}
		if len(inputs) == 0 {
			continue
		}
		claims = append(claims, personfacts.ProposedClaim{
			Target: target, Relation: claim.Relation,
			SubmittedValue: append(jsontext.Value(nil), claim.SubmittedValue...),
			Evidence:       inputs, ValidFrom: claim.ValidFrom, ValidUntil: claim.ValidUntil,
			Origin: claim.Origin, Confidence: claim.Confidence,
		})
	}
	if len(claims) == 0 {
		return personfacts.PreparedGeneration{}, errors.New(
			"the reviewed attempt has no stored claims to confirm")
	}
	claims, err := claimsWithIdentityScore(claims, PersonEnrichmentUserConfirmedIdentityScore)
	if err != nil {
		return personfacts.PreparedGeneration{}, err
	}
	stored := generation.Generation
	input := personfacts.GenerationInput{
		PersonID:           stored.PersonID,
		SourceCursors:      slices.Clone(stored.SourceCursors),
		ProgramID:          stored.ProgramID,
		ProgramVersion:     stored.ProgramVersion,
		ProgramFingerprint: stored.ProgramFingerprint,
		CatalogFingerprint: stored.CatalogFingerprint,
		Provider:           stored.Provider, ProviderVersion: stored.ProviderVersion,
		Model: stored.Model, ModelVersion: stored.ModelVersion,
		ResolvedAt: resolvedAt,
		Policy: personfacts.PolicyContext{
			AllowSensitive:            profile.AllowSensitiveTargets,
			ProviderPolicyFingerprint: profile.Fingerprint,
		},
		Claims: claims,
	}
	return personfacts.PreparePersonFactGeneration(ctx, input, aligner)
}

// ConfirmPersonEnrichmentIdentityContext is the user confirming that an
// identity_uncertain attempt found the right person. In one transaction it
// tracks the person if they are not tracked yet (confirming the research is
// the user choosing to keep this profile; the attempt being confirmed is its
// enrollment result, so no second provider search is queued), re-applies
// the attempt's stored claims at the verified identity score as a new fact
// generation, attaches the returned provider person IDs at the
// verified confidence, moves the attempt to succeeded (only if it is still
// identity_uncertain), records the decision with reason user_confirmed,
// schedules the profile's refresh, and brings the run's counters up to date.
func (s *Store) ConfirmPersonEnrichmentIdentityContext(
	ctx context.Context, attemptID int64, actor string,
) (*PersonEnrichmentIdentityDecision, error) {
	actor = strings.TrimSpace(actor)
	if attemptID <= 0 || actor == "" {
		return nil, errors.New("person enrichment identity confirmation needs an attempt ID and actor")
	}
	return retryBusyWrite(ctx, s, "confirm person enrichment identity",
		func() (*PersonEnrichmentIdentityDecision, error) {
			var decision *PersonEnrichmentIdentityDecision
			err := s.withTxContext(ctx, func(tx *loggedTx) error {
				var err error
				decision, err = s.confirmPersonEnrichmentIdentityTx(ctx, tx, attemptID, actor)
				return err
			})
			return decision, err
		})
}

func (s *Store) confirmPersonEnrichmentIdentityTx(
	ctx context.Context, tx *loggedTx, attemptID int64, actor string,
) (*PersonEnrichmentIdentityDecision, error) {
	review, err := s.lockPersonEnrichmentReviewTx(ctx, tx, attemptID)
	if err != nil {
		return nil, err
	}
	attempt, profile := review.attempt, review.profile
	if _, err := s.setPersonTrackingWithEnrollmentTx(
		ctx, tx, attempt.PersonID, true, false); err != nil {
		return nil, err
	}
	if !attempt.FactGenerationKey.Valid {
		return nil, errors.New("the reviewed attempt has no stored claims to confirm")
	}
	generation, err := s.loadPersonFactGenerationTx(
		ctx, tx, attempt.PersonID, attempt.FactGenerationKey.String)
	if err != nil {
		return nil, err
	}
	now := s.personEnrichmentTime()
	prepared, err := confirmedEnrichmentGeneration(ctx, generation, profile, now)
	if err != nil {
		return nil, err
	}
	providerIDs, err := attemptProviderIDsTx(ctx, tx, attempt.ID)
	if err != nil {
		return nil, err
	}
	for _, identity := range providerIDs {
		owner, owned, err := s.lockPersonEnrichmentProviderIdentityOwnershipTx(
			ctx, tx, identity.namespace, identity.id)
		if err != nil {
			return nil, err
		}
		if owned && owner != attempt.PersonID {
			return nil, ErrPersonEnrichmentIdentityOwnedElsewhere
		}
	}
	applied, err := s.applyPreparedPersonFactGenerationTx(ctx, tx, prepared)
	if err != nil {
		return nil, err
	}
	for _, identity := range providerIDs {
		if err := s.attachPersonEnrichmentProviderIdentityTx(ctx, tx, attempt.PersonID,
			identity.namespace, personenrichment.ProviderPersonID{
				ID: identity.id, Confidence: PersonEnrichmentUserConfirmedIdentityScore,
			}, now); err != nil {
			return nil, err
		}
	}
	if err := s.transitionReviewedAttemptTx(ctx, tx, attempt.ID,
		personEnrichmentStateSucceeded, nil, applied.GenerationKey); err != nil {
		return nil, err
	}
	if err := s.recordPersonEnrichmentReviewTx(ctx, tx, attempt, "confirmed",
		PersonEnrichmentIdentityConfirmedReason, actor, applied.GenerationKey, now); err != nil {
		return nil, err
	}
	if profile.RefreshInterval > 0 {
		if err := putPersonEnrichmentWorkWithExecer(ctx, tx, EnrichmentTriggerInput{
			PersonID: attempt.PersonID, ProfileFingerprint: attempt.ProfileFingerprint,
			Kind: personenrichment.TriggerRefresh, Generation: "refresh:" + applied.GenerationKey,
			DueAt: now.Add(profile.RefreshInterval),
		}); err != nil {
			return nil, err
		}
	}
	if err := s.refreshPersonEnrichmentRunCountsTx(ctx, tx, attempt.RunID); err != nil {
		return nil, err
	}
	key := applied.GenerationKey
	return &PersonEnrichmentIdentityDecision{
		AttemptID: attempt.ID, PersonID: attempt.PersonID, Decision: "confirmed",
		Reason: PersonEnrichmentIdentityConfirmedReason, AttemptState: personEnrichmentStateSucceeded,
		FactGenerationKey: &key, Projections: len(applied.Projections),
		ProviderIdentitiesAttached: len(providerIDs),
	}, nil
}

// RejectPersonEnrichmentIdentityContext is the user saying an
// identity_uncertain attempt found someone else. The attempt becomes
// identity_rejected with reason user_rejected, and a per-person negative is
// recorded for each returned provider person ID (or, for an attempt that
// kept none, each profile URL its evidence cited) so the same identity is
// never proposed for this person again.
func (s *Store) RejectPersonEnrichmentIdentityContext(
	ctx context.Context, attemptID int64, actor string,
) (*PersonEnrichmentIdentityDecision, error) {
	actor = strings.TrimSpace(actor)
	if attemptID <= 0 || actor == "" {
		return nil, errors.New("person enrichment identity rejection needs an attempt ID and actor")
	}
	return retryBusyWrite(ctx, s, "reject person enrichment identity",
		func() (*PersonEnrichmentIdentityDecision, error) {
			var decision *PersonEnrichmentIdentityDecision
			err := s.withTxContext(ctx, func(tx *loggedTx) error {
				var err error
				decision, err = s.rejectPersonEnrichmentIdentityTx(ctx, tx, attemptID, actor)
				return err
			})
			return decision, err
		})
}

func (s *Store) rejectPersonEnrichmentIdentityTx(
	ctx context.Context, tx *loggedTx, attemptID int64, actor string,
) (*PersonEnrichmentIdentityDecision, error) {
	review, err := s.lockPersonEnrichmentReviewTx(ctx, tx, attemptID)
	if err != nil {
		return nil, err
	}
	attempt, profile := review.attempt, review.profile
	now := s.personEnrichmentTime()
	type negative struct{ kind, value string }
	negatives := []negative{}
	providerIDs, err := attemptProviderIDsTx(ctx, tx, attempt.ID)
	if err != nil {
		return nil, err
	}
	for _, identity := range providerIDs {
		negatives = append(negatives, negative{personEnrichmentRejectionProviderID, identity.id})
	}
	if len(negatives) == 0 && attempt.FactGenerationKey.Valid {
		generation, err := s.loadPersonFactGenerationTx(
			ctx, tx, attempt.PersonID, attempt.FactGenerationKey.String)
		if err != nil {
			return nil, err
		}
		seen := map[string]struct{}{}
		for _, evidence := range generation.Evidence {
			value := normalizeRejectedProfileURL(evidence.Input.SourceURL)
			if value == "" {
				continue
			}
			if _, dup := seen[value]; dup {
				continue
			}
			seen[value] = struct{}{}
			negatives = append(negatives, negative{personEnrichmentRejectionProfileURL, value})
		}
	}
	failure := string(personenrichment.FailureIdentityRejected)
	if err := s.transitionReviewedAttemptTx(ctx, tx, attempt.ID, "identity_rejected",
		&failure, attempt.FactGenerationKey.String); err != nil {
		return nil, err
	}
	if err := s.recordPersonEnrichmentReviewTx(ctx, tx, attempt, "rejected",
		PersonEnrichmentIdentityRejectedReason, actor, "", now); err != nil {
		return nil, err
	}
	for _, item := range negatives {
		if _, err := tx.ExecContext(ctx, `INSERT INTO person_enrichment_identity_rejections
			(person_id, provider_namespace, key_kind, key_value, attempt_id, actor, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (person_id, provider_namespace, key_kind, key_value) DO NOTHING`,
			attempt.PersonID, profile.ProviderNamespace, item.kind, item.value,
			attempt.ID, actor, now); err != nil {
			return nil, fmt.Errorf("record person enrichment identity rejection: %w", err)
		}
	}
	if err := s.refreshPersonEnrichmentRunCountsTx(ctx, tx, attempt.RunID); err != nil {
		return nil, err
	}
	return &PersonEnrichmentIdentityDecision{
		AttemptID: attempt.ID, PersonID: attempt.PersonID, Decision: "rejected",
		Reason: PersonEnrichmentIdentityRejectedReason, AttemptState: "identity_rejected",
		Negatives: len(negatives),
	}, nil
}
