package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/personfacts"
	"golang.org/x/net/publicsuffix"
)

// organizationPlatformDomains are platforms and hosts that carry many
// unrelated organizations' pages, profiles, and sites. Their domain says
// nothing about which organization a page belongs to, so a reference on one
// of them is never settled by its domain. Entries are compared with both the
// registrable domain and the public suffix, so acme.github.io (whose
// registrable domain is itself, github.io being a public suffix) and
// sites.google.com are both caught.
var organizationPlatformDomains = map[string]struct{}{
	"linkedin.com": {}, "google.com": {}, "medium.com": {},
	"github.com": {}, "github.io": {}, "facebook.com": {},
	"x.com": {}, "twitter.com": {}, "instagram.com": {}, "youtube.com": {},
	"notion.site": {}, "substack.com": {}, "wordpress.com": {},
	"blogspot.com": {}, "wixsite.com": {}, "squarespace.com": {},
	"linktr.ee": {}, "crunchbase.com": {}, "angel.co": {}, "wellfound.com": {},
}

// settlementRegistrableDomain is the registrable domain (eTLD+1, by the
// public suffix list) a reference domain may settle on, or "" when the
// domain cannot settle anything: no registrable domain, a consumer mail
// domain, or a platform domain.
func settlementRegistrableDomain(domain string) string {
	if domain == "" {
		return ""
	}
	base, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		return ""
	}
	suffix, _ := publicsuffix.PublicSuffix(domain)
	_, platformBase := organizationPlatformDomains[base]
	_, platformSuffix := organizationPlatformDomains[suffix]
	if platformBase || platformSuffix || correspondentkind.IsFreemailDomain(base) {
		return ""
	}
	return base
}

// organizationNameCore is a name with case, punctuation, spacing, and
// legal-entity words removed, so "Example Labs, Inc." and "example labs"
// are one name.
func organizationNameCore(name string) string {
	return strings.Join(organizationNameTokens(name), " ")
}

// OrganizationDomainSettlementContext reports the organization a missed
// reference names without any judgment. It settles only when all of these
// hold:
//
//   - the reference domain has a registrable domain that is not a consumer
//     mail or platform domain;
//   - exactly one active company has any domain on that registrable domain,
//     counted over every organization and every active domain, whatever its
//     review history;
//   - that organization's name or an active alternate name equals the
//     reference name once case, punctuation, and legal-entity words are
//     removed;
//   - the user has not rejected that name for that organization.
//
// Anything else, including a shared domain under a different name, is a
// judgment and is not settled here.
func (s *Store) OrganizationDomainSettlementContext(
	ctx context.Context, ref personfacts.OrganizationReference,
) (int64, bool, error) {
	var (
		id int64
		ok bool
	)
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		var settleErr error
		id, ok, settleErr = organizationDomainSettlementTx(ctx, tx, ref.Name, NormalizeDomain(ref.Domain))
		return settleErr
	})
	if err != nil {
		return 0, false, err
	}
	return id, ok, nil
}

func organizationDomainSettlementTx(
	ctx context.Context, tx *loggedTx, name, domain string,
) (int64, bool, error) {
	base := settlementRegistrableDomain(domain)
	core := organizationNameCore(name)
	if base == "" || core == "" {
		return 0, false, nil
	}
	sharers, err := organizationsOnRegistrableDomainTx(ctx, tx, base)
	if err != nil {
		return 0, false, err
	}
	if len(sharers) != 1 {
		return 0, false, nil
	}
	var id int64
	for sharer := range sharers {
		id = sharer
	}
	names, err := queryOrganizationShortlistStrings(ctx, tx, `
		SELECT name FROM organizations WHERE id = ?
		UNION ALL
		SELECT original_value FROM organization_names
		WHERE organization_id = ? AND active_until IS NULL AND superseded_at IS NULL`, id, id)
	if err != nil {
		return 0, false, err
	}
	named := false
	for _, candidate := range names {
		if organizationNameCore(candidate) == core {
			named = true
			break
		}
	}
	if !named {
		return 0, false, nil
	}
	var rejected bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM organization_match_reviews
		WHERE organization_id = ? AND proposed_name_normalized = ? AND status = 'rejected'
	)`, id, NormalizeOrganizationName(name)).Scan(&rejected); err != nil {
		return 0, false, fmt.Errorf("check rejected organization match: %w", err)
	}
	if rejected {
		return 0, false, nil
	}
	return id, true, nil
}

// organizationsOnRegistrableDomainTx returns every active company with a
// primary domain or active domain identifier on the registrable domain base.
// It is deliberately uncapped and ignores review state: whether a domain
// tells organizations apart is a property of the domain, not of a shortlist.
func organizationsOnRegistrableDomainTx(
	ctx context.Context, tx *loggedTx, base string,
) (map[int64]struct{}, error) {
	suffix := "%." + escapeLike(base)
	rows, err := tx.QueryContext(ctx, `
		SELECT o.id, o.primary_domain FROM organizations o
		WHERE o.kind = 'company' AND o.retired_at IS NULL AND o.merged_into_id IS NULL
		  AND (o.primary_domain = ? OR o.primary_domain LIKE ? ESCAPE '\')
		UNION ALL
		SELECT o.id, i.normalized_value FROM organizations o
		JOIN organization_identifiers i ON i.organization_id = o.id
		WHERE o.kind = 'company' AND o.retired_at IS NULL AND o.merged_into_id IS NULL
		  AND i.identifier_kind = 'domain' AND i.active_until IS NULL AND i.superseded_at IS NULL
		  AND (i.normalized_value = ? OR i.normalized_value LIKE ? ESCAPE '\')`,
		base, suffix, base, suffix)
	if err != nil {
		return nil, fmt.Errorf("query organizations on a registrable domain: %w", err)
	}
	defer func() { _ = rows.Close() }()
	sharers := make(map[int64]struct{})
	for rows.Next() {
		var (
			id     int64
			domain sql.NullString
		)
		if err := rows.Scan(&id, &domain); err != nil {
			return nil, fmt.Errorf("scan organization on a registrable domain: %w", err)
		}
		if !domain.Valid {
			continue
		}
		if registrable, err := publicsuffix.EffectiveTLDPlusOne(domain.String); err == nil && registrable == base {
			sharers[id] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate organizations on a registrable domain: %w", err)
	}
	return sharers, nil
}
