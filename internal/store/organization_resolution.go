package store

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/personfacts"
)

// Organization resolution review errors.
var (
	ErrOrganizationMatchReviewNotFound     = errors.New("organization match review not found")
	ErrOrganizationMatchReviewStateChanged = errors.New("organization match review is no longer pending")
	ErrOrganizationMatchReviewAmbiguous    = errors.New(
		"more than one organization has the proposed name; merge them in the directory first")
	// ErrOrganizationWriteFenced refuses a write whose lease is no longer
	// held and unexpired when its transaction runs.
	ErrOrganizationWriteFenced = errors.New(
		"organization write refused: the lease it is fenced by is no longer held")
)

// Organization match review states.
const (
	OrganizationMatchPending  = "pending"
	OrganizationMatchAccepted = "accepted"
	OrganizationMatchRejected = "rejected"
)

// organizationResolutionSourceRefPrefix marks alias rows written from a Jev
// organization_resolution judgment. The model version follows it.
const organizationResolutionSourceRefPrefix = "jev:organization_resolution:"

// OrganizationDomainRuleSourceRef marks alias rows organization resolution
// wrote because the name's domain shares the organization's registrable
// domain: a rule decided them, not a judgment.
const OrganizationDomainRuleSourceRef = "rule:organization_resolution:registrable_domain"

// maxPersonOrganizationTitles bounds the known titles returned for one
// person at one organization.
const maxPersonOrganizationTitles = 20

// OrganizationResolutionSourceRef is the source_ref of a judgment-written
// alias: it names the Jev feature and the model version that decided it.
func OrganizationResolutionSourceRef(model string) string {
	return organizationResolutionSourceRefPrefix + model
}

// OrganizationAliasInput records that a missed organization name (and
// optionally its domain) names an existing organization.
type OrganizationAliasInput struct {
	OrganizationID int64
	Name           string
	Domain         string
	Model          string
	Confidence     float64
	// Fence, when set, is checked inside the write's transaction; the write
	// is refused with ErrOrganizationWriteFenced when that lease is lost.
	Fence *personfacts.WriteFence
}

// OrganizationAliasResult reports which lookup keys were added. Both false
// means the organization already answered to the name and domain.
type OrganizationAliasResult struct {
	NameAdded   bool
	DomainAdded bool
}

// RecordOrganizationResolutionAliasContext makes the deterministic lookup
// resolve a name (and domain) to an existing organization from now on. It
// writes an 'alias' organization name and, when the domain is new to the
// organization, a domain identifier, both with source 'system', a source_ref
// naming the Jev feature and model, and the judgment's confidence. Writing
// the same alias again changes nothing.
func (s *Store) RecordOrganizationResolutionAliasContext(
	ctx context.Context, input OrganizationAliasInput,
) (OrganizationAliasResult, error) {
	if input.OrganizationID <= 0 || strings.TrimSpace(input.Model) == "" ||
		math.IsNaN(input.Confidence) || input.Confidence < 0 || input.Confidence > 1 {
		return OrganizationAliasResult{}, fmt.Errorf("%w: organization alias input is incomplete",
			ErrOrganizationInvalid)
	}
	confidence := input.Confidence
	result, err := retryContendedWrite(ctx, s, "record organization alias",
		func() (*OrganizationAliasResult, error) {
			var result OrganizationAliasResult
			err := s.withTxContext(ctx, func(tx *loggedTx) error {
				if err := s.checkOrganizationWriteFenceTx(ctx, tx, input.Fence); err != nil {
					return err
				}
				var addErr error
				result, addErr = s.addOrganizationLookupAliasTx(ctx, tx, input.OrganizationID,
					input.Name, input.Domain, ProvenanceSystem,
					OrganizationResolutionSourceRef(input.Model), &confidence)
				return addErr
			})
			return &result, err
		})
	if err != nil {
		return OrganizationAliasResult{}, err
	}
	return *result, nil
}

// OrganizationDomainAliasInput records that a missed organization name and
// its domain name an existing organization because the domain shares that
// organization's registrable domain. No judgment is involved.
type OrganizationDomainAliasInput struct {
	OrganizationID int64
	Name           string
	Domain         string
	// Fence, when set, is checked inside the write's transaction; the write
	// is refused with ErrOrganizationWriteFenced when that lease is lost.
	Fence *personfacts.WriteFence
}

// RecordOrganizationDomainAliasContext makes the deterministic lookup
// resolve a name and domain to the organization whose own domain shares the
// domain's registrable domain (SharesRegistrableDomain). It writes the same
// lookup keys as a judgment alias, with source 'system', source_ref
// OrganizationDomainRuleSourceRef, and no confidence, because a rule decided
// it. It refuses an organization that does not share the registrable domain
// or a consumer mail domain. Writing the same alias again changes nothing.
func (s *Store) RecordOrganizationDomainAliasContext(
	ctx context.Context, input OrganizationDomainAliasInput,
) (OrganizationAliasResult, error) {
	domain := NormalizeDomain(input.Domain)
	if input.OrganizationID <= 0 || domain == "" {
		return OrganizationAliasResult{}, fmt.Errorf("%w: organization domain alias input is incomplete",
			ErrOrganizationInvalid)
	}
	result, err := retryContendedWrite(ctx, s, "record organization domain alias",
		func() (*OrganizationAliasResult, error) {
			var result OrganizationAliasResult
			err := s.withTxContext(ctx, func(tx *loggedTx) error {
				if err := s.checkOrganizationWriteFenceTx(ctx, tx, input.Fence); err != nil {
					return err
				}
				organization, err := s.canonicalOrganizationForWriteTx(ctx, tx, input.OrganizationID)
				if err != nil {
					return err
				}
				domains, err := queryOrganizationShortlistStrings(ctx, tx, `
					SELECT normalized_value FROM organization_identifiers
					WHERE organization_id = ? AND identifier_kind = 'domain'
					  AND active_until IS NULL AND superseded_at IS NULL`, organization.ID)
				if err != nil {
					return err
				}
				if organization.PrimaryDomain != nil {
					domains = append(domains, *organization.PrimaryDomain)
				}
				if !SharesRegistrableDomain(domain, domains) {
					return fmt.Errorf("%w: organization %d does not share the registrable domain of %s",
						ErrOrganizationInvalid, organization.ID, domain)
				}
				var addErr error
				result, addErr = s.addOrganizationLookupAliasTx(ctx, tx, organization.ID,
					input.Name, domain, ProvenanceSystem, OrganizationDomainRuleSourceRef, nil)
				return addErr
			})
			return &result, err
		})
	if err != nil {
		return OrganizationAliasResult{}, err
	}
	return *result, nil
}

// addOrganizationLookupAliasTx adds the name as an 'alias' name and the
// domain as a domain identifier when the organization does not already
// answer to them, and bumps the organization revision when it wrote
// anything so a stale profile replace cannot silently drop the alias.
func (s *Store) addOrganizationLookupAliasTx(
	ctx context.Context, tx *loggedTx, organizationID int64, name, domain string,
	source Provenance, sourceRef string, confidence *float64,
) (OrganizationAliasResult, error) {
	name = strings.Join(strings.Fields(name), " ")
	normalizedName := NormalizeOrganizationName(name)
	if normalizedName == "" {
		return OrganizationAliasResult{}, fmt.Errorf("%w: alias name is required", ErrOrganizationInvalid)
	}
	normalizedDomain := ""
	if strings.TrimSpace(domain) != "" {
		normalizedDomain = NormalizeDomain(domain)
		if normalizedDomain == "" {
			return OrganizationAliasResult{}, fmt.Errorf("%w: alias domain is invalid", ErrOrganizationInvalid)
		}
	}
	organization, err := s.canonicalOrganizationForWriteTx(ctx, tx, organizationID)
	if err != nil {
		return OrganizationAliasResult{}, err
	}
	organizationID = organization.ID
	var result OrganizationAliasResult
	nameKnown := NormalizeOrganizationName(organization.Name) == normalizedName
	if !nameKnown {
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM organization_names
			WHERE organization_id = ? AND name_normalized = ?
			  AND active_until IS NULL AND superseded_at IS NULL
		)`, organizationID, normalizedName).Scan(&nameKnown); err != nil {
			return OrganizationAliasResult{}, fmt.Errorf("check organization alias name: %w", err)
		}
	}
	if !nameKnown {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO organization_names (
				organization_id, name_kind, formatted, original_value, name_normalized,
				source, source_ref, confidence
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			organizationID, OrganizationNameKindAlias, name, name, normalizedName,
			source, sourceRef, confidence); err != nil {
			return OrganizationAliasResult{}, fmt.Errorf("add organization alias name: %w", err)
		}
		result.NameAdded = true
	}
	if normalizedDomain != "" {
		domainKnown := organization.PrimaryDomain != nil && *organization.PrimaryDomain == normalizedDomain
		if !domainKnown {
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
				SELECT 1 FROM organization_identifiers
				WHERE organization_id = ? AND identifier_kind = 'domain' AND normalized_value = ?
				  AND active_until IS NULL AND superseded_at IS NULL
			)`, organizationID, normalizedDomain).Scan(&domainKnown); err != nil {
				return OrganizationAliasResult{}, fmt.Errorf("check organization alias domain: %w", err)
			}
		}
		if !domainKnown {
			if _, err := tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO organization_identifiers (
					organization_id, identifier_kind, identifier_value, normalized_value,
					source, source_ref, confidence
				) VALUES (?, 'domain', ?, ?, ?, ?, ?)`,
				organizationID, normalizedDomain, normalizedDomain,
				source, sourceRef, confidence); err != nil {
				return OrganizationAliasResult{}, fmt.Errorf("add organization alias domain: %w", err)
			}
			result.DomainAdded = true
		}
	}
	if result.NameAdded || result.DomainAdded {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE organizations SET revision = revision + 1, updated_at = %s WHERE id = ?`,
			s.dialect.Now()), organizationID); err != nil {
			return OrganizationAliasResult{}, fmt.Errorf("bump aliased organization revision: %w", err)
		}
	}
	return result, nil
}

// EmploymentTitleAliasInput records that two job titles name the same role
// at one organization.
type EmploymentTitleAliasInput struct {
	OrganizationID int64
	Title          string
	CanonicalTitle string
	Model          string
	Confidence     float64
	// Fence, when set, is checked inside the write's transaction; the write
	// is refused with ErrOrganizationWriteFenced when that lease is lost.
	Fence *personfacts.WriteFence
}

// RecordEmploymentTitleAliasContext maps Title to CanonicalTitle at the
// organization. The canonical side is resolved through existing mappings
// first, titles already mapped to Title move to the new canonical, and a
// title that already has a mapping keeps it, so repeating a judgment is a
// no-op. It reports whether a mapping was written.
func (s *Store) RecordEmploymentTitleAliasContext(
	ctx context.Context, input EmploymentTitleAliasInput,
) (bool, error) {
	title := NormalizeEmploymentTitle(&input.Title)
	canonicalDisplay := strings.Join(strings.Fields(input.CanonicalTitle), " ")
	canonical := NormalizeEmploymentTitle(&canonicalDisplay)
	if input.OrganizationID <= 0 || title == "" || canonical == "" ||
		strings.TrimSpace(input.Model) == "" ||
		math.IsNaN(input.Confidence) || input.Confidence < 0 || input.Confidence > 1 {
		return false, fmt.Errorf("%w: title alias input is incomplete", ErrOrganizationInvalid)
	}
	written, err := retryContendedWrite(ctx, s, "record employment title alias",
		func() (*bool, error) {
			written := false
			err := s.withTxContext(ctx, func(tx *loggedTx) error {
				if err := s.checkOrganizationWriteFenceTx(ctx, tx, input.Fence); err != nil {
					return err
				}
				organization, err := s.canonicalOrganizationForWriteTx(ctx, tx, input.OrganizationID)
				if err != nil {
					return err
				}
				organizationID := organization.ID
				aliases, err := employmentTitleAliasesTx(ctx, tx, organizationID)
				if err != nil {
					return err
				}
				if target, mapped := aliases[canonical]; mapped {
					canonical, canonicalDisplay = target.normalized, target.display
				}
				if _, mapped := aliases[title]; mapped || title == canonical {
					return nil
				}
				if _, err := tx.ExecContext(ctx, `
					UPDATE organization_title_aliases
					SET canonical_title = ?, canonical_title_normalized = ?
					WHERE organization_id = ? AND canonical_title_normalized = ?`,
					canonicalDisplay, canonical, organizationID, title); err != nil {
					return fmt.Errorf("repoint employment title aliases: %w", err)
				}
				if _, err := tx.ExecContext(ctx, `
					INSERT OR IGNORE INTO organization_title_aliases (
						organization_id, title_normalized, canonical_title,
						canonical_title_normalized, source, source_ref, confidence
					) VALUES (?, ?, ?, ?, ?, ?, ?)`,
					organizationID, title, canonicalDisplay, canonical,
					ProvenanceSystem, OrganizationResolutionSourceRef(input.Model),
					input.Confidence); err != nil {
					return fmt.Errorf("add employment title alias: %w", err)
				}
				written = true
				return nil
			})
			return &written, err
		})
	if err != nil {
		return false, err
	}
	return *written, nil
}

type employmentTitleAlias struct {
	normalized string
	display    string
}

// directEmploymentTitleAliasesTx loads an organization's title mapping rows
// as stored, keyed by the mapped normalized title. An organization merge
// moves the losing side's rows here, so no redirect needs following.
func directEmploymentTitleAliasesTx(
	ctx context.Context, tx *loggedTx, organizationID int64,
) (map[string]employmentTitleAlias, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT title_normalized, canonical_title, canonical_title_normalized
		FROM organization_title_aliases
		WHERE organization_id = ?
		ORDER BY id`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("load employment title aliases: %w", err)
	}
	defer func() { _ = rows.Close() }()
	aliases := make(map[string]employmentTitleAlias)
	for rows.Next() {
		var title string
		var alias employmentTitleAlias
		if err := rows.Scan(&title, &alias.display, &alias.normalized); err != nil {
			return nil, fmt.Errorf("scan employment title alias: %w", err)
		}
		aliases[title] = alias
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate employment title aliases: %w", err)
	}
	return aliases, nil
}

// employmentTitleAliasesTx maps each of an organization's aliased titles to
// the canonical title at the end of its chain. Writes keep chains one hop
// long; resolving the whole chain here keeps reads right even if one is not.
// A title whose chain loops has no canonical title.
func employmentTitleAliasesTx(
	ctx context.Context, tx *loggedTx, organizationID int64,
) (map[string]employmentTitleAlias, error) {
	direct, err := directEmploymentTitleAliasesTx(ctx, tx, organizationID)
	if err != nil {
		return nil, err
	}
	resolved := make(map[string]employmentTitleAlias, len(direct))
	for title := range direct {
		if final, ok := followEmploymentTitleAlias(direct, title); ok {
			resolved[title] = final
		}
	}
	return resolved, nil
}

// EmploymentTitleCanonicalContext returns, for each of titles that has a
// mapping at the organization, its normalized form and canonical display
// title. Titles without a mapping are left out.
func (s *Store) EmploymentTitleCanonicalContext(
	ctx context.Context, organizationID int64, titles []string,
) (map[string]string, error) {
	canonical := make(map[string]string)
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		aliases, err := employmentTitleAliasesTx(ctx, tx, organizationID)
		if err != nil {
			return err
		}
		for _, title := range titles {
			key := NormalizeEmploymentTitle(&title)
			if alias, mapped := aliases[key]; mapped {
				canonical[key] = alias.display
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

// PersonOrganizationTitlesContext lists the distinct job titles already known
// for a person at an organization: their employment rows there, then the
// titles of their employment claims that name it by ID, name, or alternate
// name. Titles already mapped to the same canonical title count once.
func (s *Store) PersonOrganizationTitlesContext(
	ctx context.Context, personID, organizationID int64,
) ([]string, error) {
	var titles []string
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		aliases, err := employmentTitleAliasesTx(ctx, tx, organizationID)
		if err != nil {
			return err
		}
		seen := make(map[string]struct{})
		add := func(title string) {
			title = strings.Join(strings.Fields(title), " ")
			key := NormalizeEmploymentTitle(&title)
			if alias, mapped := aliases[key]; mapped {
				key = alias.normalized
			}
			if key == "" || len(titles) >= maxPersonOrganizationTitles {
				return
			}
			if _, duplicate := seen[key]; duplicate {
				return
			}
			seen[key] = struct{}{}
			titles = append(titles, title)
		}
		employmentTitles, err := queryOrganizationShortlistStrings(ctx, tx, `
			SELECT title FROM employments
			WHERE person_id = ? AND (organization_id = ? OR organization_id IN (
				SELECT id FROM organizations WHERE merged_into_id = ?))
			ORDER BY id`, personID, organizationID, organizationID)
		if err != nil {
			return err
		}
		for _, title := range employmentTitles {
			add(title)
		}
		names, err := organizationLookupNamesTx(ctx, tx, organizationID)
		if err != nil {
			return err
		}
		claims, err := queryOrganizationShortlistStrings(ctx, tx, `
			SELECT normalized_value_json FROM person_fact_claims
			WHERE person_id = ? AND target_kind = ? AND relation = 'support'
			  AND normalized_value_json IS NOT NULL
			ORDER BY id`, personID, string(personfacts.TargetEmployment))
		if err != nil {
			return err
		}
		for _, raw := range claims {
			var value personfacts.EmploymentValue
			if json.Unmarshal([]byte(raw), &value) != nil || value.Title == "" {
				continue
			}
			byID := value.Organization.ID != nil && *value.Organization.ID == organizationID
			if _, byName := names[NormalizeOrganizationName(value.Organization.Name)]; byID || byName {
				add(value.Title)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return titles, nil
}

// organizationLookupNamesTx is every normalized name the organization
// answers to: its own name and its active alternate names.
func organizationLookupNamesTx(
	ctx context.Context, tx *loggedTx, organizationID int64,
) (map[string]struct{}, error) {
	organization, err := getOrganizationTx(ctx, tx, organizationID)
	if err != nil {
		return nil, err
	}
	names := map[string]struct{}{NormalizeOrganizationName(organization.Name): {}}
	alternates, err := queryOrganizationShortlistStrings(ctx, tx, `
		SELECT name_normalized FROM organization_names
		WHERE organization_id = ? AND active_until IS NULL AND superseded_at IS NULL`,
		organizationID)
	if err != nil {
		return nil, err
	}
	for _, name := range alternates {
		names[name] = struct{}{}
	}
	return names, nil
}

// OrganizationMatchReviewInput proposes that a name may be an existing
// organization.
type OrganizationMatchReviewInput struct {
	OrganizationID int64
	Name           string
	Domain         string
	Model          string
	Probability    float64
	// Fence, when set, is checked inside the write's transaction; the write
	// is refused with ErrOrganizationWriteFenced when that lease is lost.
	Fence *personfacts.WriteFence
}

// RecordOrganizationMatchReviewContext stores a pending review. A review for
// the same organization, name, and domain, in any state, is kept as it is.
func (s *Store) RecordOrganizationMatchReviewContext(
	ctx context.Context, input OrganizationMatchReviewInput,
) (bool, error) {
	name := strings.Join(strings.Fields(input.Name), " ")
	normalizedName := NormalizeOrganizationName(name)
	domain := ""
	if strings.TrimSpace(input.Domain) != "" {
		domain = NormalizeDomain(input.Domain)
	}
	if input.OrganizationID <= 0 || normalizedName == "" || strings.TrimSpace(input.Model) == "" ||
		math.IsNaN(input.Probability) || input.Probability < 0 || input.Probability > 1 {
		return false, fmt.Errorf("%w: organization match review input is incomplete", ErrOrganizationInvalid)
	}
	inserted, err := retryContendedWrite(ctx, s, "record organization match review",
		func() (*bool, error) {
			inserted := false
			err := s.withTxContext(ctx, func(tx *loggedTx) error {
				if err := s.checkOrganizationWriteFenceTx(ctx, tx, input.Fence); err != nil {
					return err
				}
				organization, err := s.canonicalOrganizationForWriteTx(ctx, tx, input.OrganizationID)
				if err != nil {
					return err
				}
				result, err := tx.ExecContext(ctx, `
					INSERT OR IGNORE INTO organization_match_reviews (
						organization_id, proposed_name, proposed_name_normalized, proposed_domain,
						probability, model
					) VALUES (?, ?, ?, ?, ?, ?)`,
					organization.ID, name, normalizedName, domain, input.Probability, input.Model)
				if err != nil {
					return fmt.Errorf("record organization match review: %w", err)
				}
				rows, err := result.RowsAffected()
				if err != nil {
					return fmt.Errorf("record organization match review: %w", err)
				}
				inserted = rows > 0
				return nil
			})
			return &inserted, err
		})
	if err != nil {
		return false, err
	}
	return *inserted, nil
}

// OrganizationMatchReview is one pending "is this the same organization?"
// question for the user.
type OrganizationMatchReview struct {
	ID                 int64   `json:"id"`
	OrganizationID     int64   `json:"organization_id"`
	OrganizationName   string  `json:"organization_name"`
	OrganizationDomain *string `json:"organization_domain,omitzero" nullable:"false"`
	ProposedName       string  `json:"proposed_name"`
	ProposedDomain     *string `json:"proposed_domain,omitzero" nullable:"false"`
	// ProposedOrganizationID is the separate organization created for the
	// proposed name, when exactly one active company has it.
	ProposedOrganizationID *int64    `json:"proposed_organization_id,omitzero" nullable:"false"`
	Probability            float64   `json:"probability"`
	Model                  string    `json:"model"`
	CreatedAt              time.Time `json:"created_at"`
}

// OrganizationMatchDecision reports what a decision did.
type OrganizationMatchDecision struct {
	ReviewID       int64  `json:"review_id"`
	Decision       string `json:"decision" enum:"accepted,rejected"`
	OrganizationID int64  `json:"organization_id"`
	// MergedOrganizationID is the organization folded into OrganizationID
	// when an accepted name already had its own organization.
	MergedOrganizationID *int64 `json:"merged_organization_id,omitzero" nullable:"false"`
}

type organizationMatchReviewRow struct {
	id, organizationID                  int64
	proposedName, proposedNameNormalize string
	proposedDomain, model, status       string
	probability                         float64
	createdAt                           time.Time
}

func scanOrganizationMatchReviewRow(row scanner) (organizationMatchReviewRow, error) {
	var review organizationMatchReviewRow
	err := row.Scan(&review.id, &review.organizationID, &review.proposedName,
		&review.proposedNameNormalize, &review.proposedDomain, &review.probability,
		&review.model, &review.status, &review.createdAt)
	return review, err
}

const organizationMatchReviewColumns = `id, organization_id, proposed_name,
	proposed_name_normalized, proposed_domain, probability, model, status, created_at`

// ListOrganizationMatchReviewsContext returns pending reviews whose
// organization is still active, newest first.
func (s *Store) ListOrganizationMatchReviewsContext(
	ctx context.Context, limit int,
) ([]OrganizationMatchReview, error) {
	if limit <= 0 {
		limit = 50
	}
	reviews := make([]OrganizationMatchReview, 0)
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT r.id, r.organization_id, r.proposed_name, r.proposed_name_normalized,
			       r.proposed_domain, r.probability, r.model, r.status, r.created_at
			FROM organization_match_reviews r
			WHERE r.status = 'pending' AND EXISTS (
				SELECT 1 FROM organizations o
				WHERE o.id = r.organization_id AND o.merged_into_id IS NULL
				  AND o.retired_at IS NULL)
			ORDER BY r.created_at DESC, r.id DESC
			LIMIT ?`, limit)
		if err != nil {
			return fmt.Errorf("list organization match reviews: %w", err)
		}
		pending := make([]organizationMatchReviewRow, 0)
		for rows.Next() {
			review, scanErr := scanOrganizationMatchReviewRow(rows)
			if scanErr != nil {
				_ = rows.Close()
				return fmt.Errorf("scan organization match review: %w", scanErr)
			}
			pending = append(pending, review)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate organization match reviews: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close organization match reviews: %w", err)
		}
		for _, row := range pending {
			organization, err := getOrganizationTx(ctx, tx, row.organizationID)
			if err != nil {
				return err
			}
			review := OrganizationMatchReview{
				ID: row.id, OrganizationID: organization.ID, OrganizationName: organization.Name,
				OrganizationDomain: organization.PrimaryDomain, ProposedName: row.proposedName,
				Probability: row.probability, Model: row.model, CreatedAt: row.createdAt.UTC(),
			}
			if row.proposedDomain != "" {
				review.ProposedDomain = new(row.proposedDomain)
			}
			proposed, err := proposedOrganizationIDsTx(ctx, tx, row)
			if err != nil {
				return err
			}
			if len(proposed) == 1 {
				review.ProposedOrganizationID = new(proposed[0])
			}
			reviews = append(reviews, review)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return reviews, nil
}

// proposedOrganizationIDsTx finds the active companies, other than the
// reviewed organization, whose own name is the proposed name.
func proposedOrganizationIDsTx(
	ctx context.Context, tx *loggedTx, review organizationMatchReviewRow,
) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM organizations
		WHERE name_normalized = ? AND id <> ? AND kind = 'company'
		  AND merged_into_id IS NULL AND retired_at IS NULL
		ORDER BY id`, review.proposedNameNormalize, review.organizationID)
	if err != nil {
		return nil, fmt.Errorf("find proposed organization: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan proposed organization: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate proposed organizations: %w", err)
	}
	return ids, nil
}

func loadOrganizationMatchReviewTx(
	ctx context.Context, tx *loggedTx, id int64,
) (organizationMatchReviewRow, error) {
	review, err := scanOrganizationMatchReviewRow(tx.QueryRowContext(ctx,
		`SELECT `+organizationMatchReviewColumns+` FROM organization_match_reviews WHERE id = ?`,
		id))
	if errors.Is(err, sql.ErrNoRows) {
		return organizationMatchReviewRow{}, ErrOrganizationMatchReviewNotFound
	}
	if err != nil {
		return organizationMatchReviewRow{}, fmt.Errorf("load organization match review: %w", err)
	}
	if review.status != OrganizationMatchPending {
		return organizationMatchReviewRow{}, ErrOrganizationMatchReviewStateChanged
	}
	return review, nil
}

// AcceptOrganizationMatchReviewContext is the user saying the proposed name
// is the reviewed organization. When exactly one other active company
// already has that name (projection created it because the judgment was not
// confident), it is merged into the reviewed organization, which keeps its
// name as a former name. The reviewed organization then answers to the
// name and domain with user provenance, so later lookups resolve to it.
func (s *Store) AcceptOrganizationMatchReviewContext(
	ctx context.Context, id int64, actor string,
) (*OrganizationMatchDecision, error) {
	// The review lock, the merge, the alias, and the decision commit together:
	// a concurrent decision makes the conditional decide fail, and then the
	// merge rolls back with it.
	return retryContendedWrite(ctx, s, "accept organization match review",
		func() (*OrganizationMatchDecision, error) {
			var decision *OrganizationMatchDecision
			err := s.withTxContext(ctx, func(tx *loggedTx) error {
				if lock := s.dialect.RowWriterLockSQL("organization_match_reviews", "status"); lock != "" {
					if _, err := tx.ExecContext(ctx, lock, id); err != nil {
						return fmt.Errorf("lock organization match review: %w", err)
					}
				}
				review, err := loadOrganizationMatchReviewTx(ctx, tx, id)
				if err != nil {
					return err
				}
				proposed, err := proposedOrganizationIDsTx(ctx, tx, review)
				if err != nil {
					return err
				}
				if len(proposed) > 1 {
					return ErrOrganizationMatchReviewAmbiguous
				}
				decision = &OrganizationMatchDecision{
					ReviewID: id, Decision: OrganizationMatchAccepted, OrganizationID: review.organizationID,
				}
				if len(proposed) == 1 {
					survivor, err := getOrganizationTx(ctx, tx, review.organizationID)
					if err != nil {
						return err
					}
					losing, err := getOrganizationTx(ctx, tx, proposed[0])
					if err != nil {
						return err
					}
					if err := s.mergeOrganizationsTx(ctx, tx, survivor.ID, survivor.Revision,
						losing.ID, losing.Revision); err != nil {
						return err
					}
					decision.MergedOrganizationID = new(losing.ID)
				}
				if err := organizationMatchAcceptStage(ctx, "merged"); err != nil {
					return err
				}
				if _, err := s.addOrganizationLookupAliasTx(ctx, tx, review.organizationID,
					review.proposedName, review.proposedDomain, ProvenanceUser,
					fmt.Sprintf("organization-match-review:%d", id), nil); err != nil {
					return err
				}
				return decideOrganizationMatchReviewTx(ctx, tx, s.dialect, id,
					OrganizationMatchAccepted, actor)
			})
			return decision, err
		})
}

// RejectOrganizationMatchReviewContext is the user saying the proposed name
// is a different organization. The reviewed organization is kept off that
// name's shortlist from now on.
func (s *Store) RejectOrganizationMatchReviewContext(
	ctx context.Context, id int64, actor string,
) (*OrganizationMatchDecision, error) {
	return retryContendedWrite(ctx, s, "reject organization match review",
		func() (*OrganizationMatchDecision, error) {
			var decision *OrganizationMatchDecision
			err := s.withTxContext(ctx, func(tx *loggedTx) error {
				if lock := s.dialect.RowWriterLockSQL("organization_match_reviews", "status"); lock != "" {
					if _, err := tx.ExecContext(ctx, lock, id); err != nil {
						return fmt.Errorf("lock organization match review: %w", err)
					}
				}
				review, err := loadOrganizationMatchReviewTx(ctx, tx, id)
				if err != nil {
					return err
				}
				decision = &OrganizationMatchDecision{
					ReviewID: id, Decision: OrganizationMatchRejected,
					OrganizationID: review.organizationID,
				}
				return decideOrganizationMatchReviewTx(ctx, tx, s.dialect, id,
					OrganizationMatchRejected, actor)
			})
			return decision, err
		})
}

func decideOrganizationMatchReviewTx(
	ctx context.Context, tx *loggedTx, dialect Dialect, id int64, status, actor string,
) error {
	if strings.TrimSpace(actor) == "" {
		actor = "user"
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE organization_match_reviews
		SET status = ?, decided_by = ?, decided_at = %s
		WHERE id = ? AND status = 'pending'`, dialect.Now()), status, actor, id)
	if err != nil {
		return fmt.Errorf("decide organization match review: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("decide organization match review: %w", err)
	}
	if changed != 1 {
		return ErrOrganizationMatchReviewStateChanged
	}
	return nil
}

// employmentTitleCanonicalizer maps employment titles through each
// organization's title aliases within one transaction, loading each
// organization's aliases once.
type employmentTitleCanonicalizer struct {
	tx    *loggedTx
	byOrg map[int64]map[string]employmentTitleAlias
}

func newEmploymentTitleCanonicalizer(tx *loggedTx) *employmentTitleCanonicalizer {
	return &employmentTitleCanonicalizer{tx: tx, byOrg: make(map[int64]map[string]employmentTitleAlias)}
}

func (c *employmentTitleCanonicalizer) aliases(
	ctx context.Context, organizationID int64,
) (map[string]employmentTitleAlias, error) {
	if aliases, loaded := c.byOrg[organizationID]; loaded {
		return aliases, nil
	}
	aliases, err := employmentTitleAliasesTx(ctx, c.tx, organizationID)
	if err != nil {
		return nil, err
	}
	c.byOrg[organizationID] = aliases
	return aliases, nil
}

// title returns the canonical title for a title at the organization, or the
// title itself when it has no mapping.
func (c *employmentTitleCanonicalizer) title(
	ctx context.Context, organizationID int64, title string,
) (string, error) {
	if title == "" {
		return title, nil
	}
	aliases, err := c.aliases(ctx, organizationID)
	if err != nil {
		return "", err
	}
	if alias, mapped := aliases[NormalizeEmploymentTitle(&title)]; mapped {
		return alias.display, nil
	}
	return title, nil
}

// employment returns a copy of the employment carrying its canonical title.
func (c *employmentTitleCanonicalizer) employment(
	ctx context.Context, employment Employment,
) (Employment, error) {
	if employment.Title == nil {
		return employment, nil
	}
	title, err := c.title(ctx, employment.OrganizationID, *employment.Title)
	if err != nil {
		return Employment{}, err
	}
	employment.Title = &title
	return employment, nil
}

// employmentTitleGroupTx lists every normalized title that names the same
// role as title at the organization: its canonical title and every title
// mapped to it. A title without mappings is its own group.
func employmentTitleGroupTx(
	ctx context.Context, tx *loggedTx, organizationID int64, title *string,
) ([]any, error) {
	key := NormalizeEmploymentTitle(title)
	aliases, err := employmentTitleAliasesTx(ctx, tx, organizationID)
	if err != nil {
		return nil, err
	}
	canonical := key
	if alias, mapped := aliases[key]; mapped {
		canonical = alias.normalized
	}
	group := []string{canonical}
	for mapped, alias := range aliases {
		if alias.normalized == canonical && mapped != canonical {
			group = append(group, mapped)
		}
	}
	slices.Sort(group)
	args := make([]any, len(group))
	for i, value := range group {
		args[i] = value
	}
	return args, nil
}

type organizationMatchAcceptFailpointKey struct{}

// withOrganizationMatchAcceptFailpoint lets a test fail an accept at a named
// stage to prove what commits together.
func withOrganizationMatchAcceptFailpoint(ctx context.Context, fail func(string) error) context.Context {
	return context.WithValue(ctx, organizationMatchAcceptFailpointKey{}, fail)
}

func organizationMatchAcceptStage(ctx context.Context, stage string) error {
	fail, _ := ctx.Value(organizationMatchAcceptFailpointKey{}).(func(string) error)
	if fail == nil {
		return nil
	}
	return fail(stage)
}

// canonicalOrganizationForWriteTx locks the organization a write names and,
// when it has been merged, follows the redirect chain to the surviving
// organization, which must be an active company. A write that raced a merge
// therefore lands where the lookup will find it.
func (s *Store) canonicalOrganizationForWriteTx(
	ctx context.Context, tx *loggedTx, organizationID int64,
) (*Organization, error) {
	currentID := organizationID
	for range maxPersonFactOrganizationRedirects {
		organization, err := getOrganizationForUpdateTx(ctx, tx, currentID)
		if err != nil {
			return nil, err
		}
		if organization.MergedIntoID != nil {
			currentID = *organization.MergedIntoID
			continue
		}
		if organization.Kind != OrganizationKindCompany || organization.RetiredAt != nil {
			return nil, fmt.Errorf("%w: organization %d is not an active company",
				ErrOrganizationInvalid, organization.ID)
		}
		return organization, nil
	}
	return nil, fmt.Errorf("%w: organization merge redirect from %d is too long",
		ErrOrganizationInvalid, organizationID)
}

// checkOrganizationWriteFenceTx refuses the write unless the fence's lease is
// still held and unexpired, checked and locked inside the write's own
// transaction so the lease cannot be lost between the check and the commit.
func (s *Store) checkOrganizationWriteFenceTx(
	ctx context.Context, tx *loggedTx, fence *personfacts.WriteFence,
) error {
	if fence == nil {
		return nil
	}
	var query string
	var args []any
	switch fence.Kind {
	case personfacts.FencePersonSweep:
		query = `SELECT person_id FROM person_sweep_work
			WHERE person_id = ? AND lease_owner = ? AND lease_fence = ?
			  AND lease_until > ` + s.dialect.Now()
		args = []any{fence.PersonID, fence.Owner, fence.Fence}
	case personfacts.FencePersonEnrichment:
		query = `SELECT person_id FROM person_enrichment_work
			WHERE person_id = ? AND profile_fingerprint = ? AND run_id = ?
			  AND lease_owner = ? AND lease_fence = ? AND lease_until > ?
			  AND ((? = 0 AND active_attempt_id IS NULL) OR active_attempt_id = ?)`
		args = []any{fence.PersonID, fence.ProfileFingerprint, fence.RunID, fence.Owner, fence.Fence,
			s.personEnrichmentTime(), fence.AttemptID, fence.AttemptID}
	default:
		return fmt.Errorf("%w: unknown fence kind %q", ErrOrganizationWriteFenced, fence.Kind)
	}
	var personID int64
	err := tx.QueryRowContext(ctx, query, args...).Scan(&personID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrOrganizationWriteFenced
	}
	if err != nil {
		return fmt.Errorf("check organization write fence: %w", err)
	}
	return nil
}
