package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"

	"go.kenn.io/msgvault/internal/personfacts"
	"golang.org/x/net/publicsuffix"
)

// MaxOrganizationShortlist bounds how many existing organizations a missed
// organization reference is compared against.
const MaxOrganizationShortlist = 8

// Shortlist signals name why a candidate was kept.
const (
	OrganizationSignalTokenOverlap  = "token_overlap"
	OrganizationSignalPrefix        = "prefix"
	OrganizationSignalTrigram       = "trigram"
	OrganizationSignalDomainSibling = "domain_sibling"
)

const (
	organizationShortlistTokenOverlap = 0.5
	organizationShortlistTrigram      = 0.35
	organizationShortlistPrefixRunes  = 4
	organizationShortlistMaxTokens    = 6
	organizationShortlistScanLimit    = 200
	organizationShortlistMaxNames     = 5
)

// organizationLegalTokens are dropped before names are compared: they say
// what kind of legal entity a company is, not which one it is.
var organizationLegalTokens = map[string]struct{}{
	"inc": {}, "incorporated": {}, "llc": {}, "llp": {}, "lp": {}, "ltd": {}, "limited": {},
	"corp": {}, "corporation": {}, "co": {}, "company": {}, "plc": {}, "gmbh": {}, "ag": {},
	"sa": {}, "sas": {}, "bv": {}, "nv": {}, "pty": {}, "srl": {}, "oy": {}, "ab": {},
	"the": {}, "and": {}, "of": {},
}

// OrganizationShortlistCandidate is one existing organization a missed
// reference might name. Score is the strongest name or domain signal in
// [0, 1]; Signals lists every signal that kept it.
type OrganizationShortlistCandidate struct {
	OrganizationID int64
	Name           string
	Domains        []string
	OtherNames     []string
	Signals        []string
	Score          float64
}

// OrganizationShortlist is the deterministic lookup outcome for one
// organization reference. Status is OrganizationReused or
// OrganizationAmbiguous when the exact lookup decided, with MatchedIDs set;
// OrganizationCreated means the exact lookup missed and Candidates holds at
// most MaxOrganizationShortlist near matches, strongest first.
type OrganizationShortlist struct {
	Reference  personfacts.OrganizationReference
	Status     OrganizationResolutionStatus
	MatchedIDs []int64
	Candidates []OrganizationShortlistCandidate
}

// OrganizationShortlistContext runs the same exact lookup person fact
// projection uses and, on a miss, builds a shortlist in code: token overlap,
// a shared prefix, and trigram similarity against organization names and
// their active alternate names, plus organizations whose domain shares the
// reference's registrable domain. It writes nothing and asks no one.
func (s *Store) OrganizationShortlistContext(
	ctx context.Context, ref personfacts.OrganizationReference,
) (*OrganizationShortlist, error) {
	ref, keys, err := normalizePersonFactOrganizationReference(ref)
	if err != nil {
		return nil, err
	}
	shortlist := &OrganizationShortlist{Reference: ref}
	if ref.ID != nil {
		shortlist.Status = OrganizationReused
		shortlist.MatchedIDs = []int64{*ref.ID}
		return shortlist, nil
	}
	err = s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		matched, lookupErr := s.personFactOrganizationCandidateIDsTx(ctx, tx, keys)
		if lookupErr != nil {
			return lookupErr
		}
		switch len(matched) {
		case 0:
			shortlist.Status = OrganizationCreated
		case 1:
			shortlist.Status = OrganizationReused
		default:
			shortlist.Status = OrganizationAmbiguous
		}
		shortlist.MatchedIDs = matched
		if len(matched) != 0 {
			return nil
		}
		candidates, shortlistErr := s.organizationShortlistTx(ctx, tx, ref)
		shortlist.Candidates = candidates
		return shortlistErr
	})
	if err != nil {
		return nil, err
	}
	return shortlist, nil
}

func (s *Store) organizationShortlistTx(
	ctx context.Context, tx *loggedTx, ref personfacts.OrganizationReference,
) ([]OrganizationShortlistCandidate, error) {
	tokens := organizationNameTokens(ref.Name)
	core := strings.Join(tokens, " ")
	ids := make(map[int64]struct{})
	for _, token := range organizationSearchTokens(tokens) {
		if err := s.collectOrganizationShortlistIDsTx(ctx, tx, ids, `
			(o.name_normalized LIKE ? ESCAPE '\' OR EXISTS (
				SELECT 1 FROM organization_names n
				WHERE n.organization_id = o.id AND n.name_normalized LIKE ? ESCAPE '\'
				  AND n.active_until IS NULL AND n.superseded_at IS NULL))`,
			"%"+escapeLike(token)+"%", "%"+escapeLike(token)+"%"); err != nil {
			return nil, err
		}
	}
	prefix := organizationNamePrefix(core)
	if prefix != "" {
		if err := s.collectOrganizationShortlistIDsTx(ctx, tx, ids, `
			(o.name_normalized LIKE ? ESCAPE '\' OR EXISTS (
				SELECT 1 FROM organization_names n
				WHERE n.organization_id = o.id AND n.name_normalized LIKE ? ESCAPE '\'
				  AND n.active_until IS NULL AND n.superseded_at IS NULL))`,
			escapeLike(prefix)+"%", escapeLike(prefix)+"%"); err != nil {
			return nil, err
		}
	}
	base := registrableDomain(ref.Domain)
	if base != "" {
		if err := s.collectOrganizationShortlistIDsTx(ctx, tx, ids, `
			(o.primary_domain = ? OR o.primary_domain LIKE ? ESCAPE '\' OR EXISTS (
				SELECT 1 FROM organization_identifiers i
				WHERE i.organization_id = o.id AND i.identifier_kind = 'domain'
				  AND (i.normalized_value = ? OR i.normalized_value LIKE ? ESCAPE '\')
				  AND i.active_until IS NULL AND i.superseded_at IS NULL))`,
			base, "%."+escapeLike(base), base, "%."+escapeLike(base)); err != nil {
			return nil, err
		}
	}

	// A user who rejected "this name is that organization" has answered for
	// good; the organization never returns to the name's shortlist.
	rejected, err := queryOrganizationShortlistStrings(ctx, tx, `
		SELECT CAST(organization_id AS TEXT) FROM organization_match_reviews
		WHERE proposed_name_normalized = ? AND status = 'rejected'`,
		NormalizeOrganizationName(ref.Name))
	if err != nil {
		return nil, err
	}
	for _, raw := range rejected {
		var id int64
		if _, scanErr := fmt.Sscan(raw, &id); scanErr == nil {
			delete(ids, id)
		}
	}

	candidates := make([]OrganizationShortlistCandidate, 0, len(ids))
	for id := range ids {
		candidate, err := loadOrganizationShortlistCandidateTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if scoreOrganizationShortlistCandidate(candidate, tokens, core, prefix, base) {
			candidates = append(candidates, *candidate)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].OrganizationID < candidates[j].OrganizationID
	})
	if len(candidates) > MaxOrganizationShortlist {
		candidates = candidates[:MaxOrganizationShortlist]
	}
	return candidates, nil
}

// collectOrganizationShortlistIDsTx adds the active companies matching one
// prefilter condition, at most organizationShortlistScanLimit per condition.
func (s *Store) collectOrganizationShortlistIDsTx(
	ctx context.Context, tx *loggedTx, ids map[int64]struct{}, condition string, args ...any,
) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT o.id FROM organizations o
		WHERE o.kind = 'company' AND o.retired_at IS NULL AND o.merged_into_id IS NULL
		  AND `+condition+`
		ORDER BY o.id LIMIT `+fmt.Sprint(organizationShortlistScanLimit), args...)
	if err != nil {
		return fmt.Errorf("query organization shortlist: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan organization shortlist: %w", err)
		}
		ids[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate organization shortlist: %w", err)
	}
	return nil
}

func loadOrganizationShortlistCandidateTx(
	ctx context.Context, tx *loggedTx, id int64,
) (*OrganizationShortlistCandidate, error) {
	organization, err := getOrganizationTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	candidate := &OrganizationShortlistCandidate{OrganizationID: organization.ID, Name: organization.Name}
	if organization.PrimaryDomain != nil {
		candidate.Domains = append(candidate.Domains, *organization.PrimaryDomain)
	}
	names, err := queryOrganizationShortlistStrings(ctx, tx, `
		SELECT original_value FROM organization_names
		WHERE organization_id = ? AND active_until IS NULL AND superseded_at IS NULL
		ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if NormalizeOrganizationName(name) == NormalizeOrganizationName(organization.Name) ||
			slices.Contains(candidate.OtherNames, name) {
			continue
		}
		if len(candidate.OtherNames) < organizationShortlistMaxNames {
			candidate.OtherNames = append(candidate.OtherNames, name)
		}
	}
	domains, err := queryOrganizationShortlistStrings(ctx, tx, `
		SELECT normalized_value FROM organization_identifiers
		WHERE organization_id = ? AND identifier_kind = 'domain'
		  AND active_until IS NULL AND superseded_at IS NULL
		ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	for _, domain := range domains {
		if !slices.Contains(candidate.Domains, domain) && len(candidate.Domains) < organizationShortlistMaxNames {
			candidate.Domains = append(candidate.Domains, domain)
		}
	}
	return candidate, nil
}

func queryOrganizationShortlistStrings(
	ctx context.Context, tx *loggedTx, query string, args ...any,
) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load organization shortlist details: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var values []string
	for rows.Next() {
		var value sql.NullString
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan organization shortlist details: %w", err)
		}
		if value.Valid && value.String != "" {
			values = append(values, value.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate organization shortlist details: %w", err)
	}
	return values, nil
}

// scoreOrganizationShortlistCandidate fills Score and Signals and reports
// whether any signal is strong enough to keep the candidate.
func scoreOrganizationShortlistCandidate(
	candidate *OrganizationShortlistCandidate, tokens []string, core, prefix, base string,
) bool {
	signals := make(map[string]struct{})
	for _, name := range append([]string{candidate.Name}, candidate.OtherNames...) {
		otherTokens := organizationNameTokens(name)
		otherCore := strings.Join(otherTokens, " ")
		if overlap := organizationTokenOverlap(tokens, otherTokens); overlap >= organizationShortlistTokenOverlap {
			signals[OrganizationSignalTokenOverlap] = struct{}{}
			candidate.Score = max(candidate.Score, overlap)
		}
		if similarity := trigramSimilarity(core, otherCore); similarity >= organizationShortlistTrigram {
			signals[OrganizationSignalTrigram] = struct{}{}
			candidate.Score = max(candidate.Score, similarity)
		}
		if prefix != "" && organizationNamePrefix(otherCore) == prefix {
			signals[OrganizationSignalPrefix] = struct{}{}
			candidate.Score = max(candidate.Score, 0.3)
		}
	}
	if base != "" {
		for _, domain := range candidate.Domains {
			if registrableDomain(domain) == base {
				signals[OrganizationSignalDomainSibling] = struct{}{}
				candidate.Score = max(candidate.Score, 0.5)
				break
			}
		}
	}
	candidate.Signals = candidate.Signals[:0]
	for signal := range signals {
		candidate.Signals = append(candidate.Signals, signal)
	}
	sort.Strings(candidate.Signals)
	return len(candidate.Signals) > 0
}

// organizationNameTokens lowercases a name, splits it on anything that is
// not a letter or digit, and drops legal-entity words.
func organizationNameTokens(name string) []string {
	fields := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	tokens := make([]string, 0, len(fields))
	for _, field := range fields {
		if _, legal := organizationLegalTokens[field]; legal {
			continue
		}
		tokens = append(tokens, field)
	}
	if len(tokens) == 0 {
		return fields
	}
	return tokens
}

// organizationSearchTokens picks the distinctive tokens to prefilter on:
// at least three characters, longest first, at most a handful.
func organizationSearchTokens(tokens []string) []string {
	search := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if len([]rune(token)) >= 3 && !slices.Contains(search, token) {
			search = append(search, token)
		}
	}
	sort.SliceStable(search, func(i, j int) bool { return len(search[i]) > len(search[j]) })
	if len(search) > organizationShortlistMaxTokens {
		search = search[:organizationShortlistMaxTokens]
	}
	return search
}

func organizationNamePrefix(core string) string {
	runes := []rune(strings.ReplaceAll(core, " ", ""))
	if len(runes) < organizationShortlistPrefixRunes {
		return ""
	}
	return string(runes[:organizationShortlistPrefixRunes])
}

// organizationTokenOverlap is the share of the shorter name's tokens that
// the other name also has.
func organizationTokenOverlap(left, right []string) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	rightSet := make(map[string]struct{}, len(right))
	for _, token := range right {
		rightSet[token] = struct{}{}
	}
	shared := 0
	seen := make(map[string]struct{}, len(left))
	for _, token := range left {
		if _, done := seen[token]; done {
			continue
		}
		seen[token] = struct{}{}
		if _, ok := rightSet[token]; ok {
			shared++
		}
	}
	return float64(shared) / float64(min(len(seen), len(rightSet)))
}

// trigramSimilarity is the Jaccard similarity of two strings' padded
// character trigrams.
func trigramSimilarity(left, right string) float64 {
	a, b := trigrams(left), trigrams(right)
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	shared := 0
	for gram := range a {
		if _, ok := b[gram]; ok {
			shared++
		}
	}
	return float64(shared) / float64(len(a)+len(b)-shared)
}

func trigrams(value string) map[string]struct{} {
	grams := make(map[string]struct{})
	for _, word := range strings.Fields(value) {
		runes := []rune("  " + word + " ")
		for i := 0; i+3 <= len(runes); i++ {
			grams[string(runes[i:i+3])] = struct{}{}
		}
	}
	return grams
}

// registrableDomain reduces a normalized host to its registrable domain
// (eTLD+1), so a regional or product subdomain meets its parent.
func registrableDomain(domain string) string {
	if domain == "" {
		return ""
	}
	base, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		return domain
	}
	return base
}
