package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// organizationMergePolicy classifies every column that references an
// organization by what an organization merge does to it, so a new reference
// cannot be added without deciding. The inventory test fails for any column
// missing here.
var organizationMergePolicy = map[string]string{
	"organizations.merged_into_id":                   "set on the losing organization: the redirect",
	"employments.organization_id":                    "repointed to the survivor",
	"organization_names.organization_id":             "superseded; the losing name is kept as a former name",
	"organization_identifiers.organization_id":       "superseded",
	"organization_addresses.organization_id":         "superseded",
	"organization_contact_points.organization_id":    "superseded",
	"organization_media.organization_id":             "superseded",
	"organization_categories.organization_id":        "superseded",
	"organization_attribute_values.organization_id":  "retired",
	"person_fact_decisions.resolved_organization_id": "kept; replay follows the redirect chain",
	"correspondent_kinds.organization_id":            "repointed to the survivor",
	"organization_match_reviews.organization_id":     "repointed; decisions merge by a direction-independent rule",
	"organization_title_aliases.organization_id":     "repointed as equivalence classes under one canonical title",
}

// retargetOrganizationReferencesTx moves the losing organization's
// references that must keep applying after a merge onto the survivor:
// correspondent kinds that name it, organization match reviews (so a
// rejection keeps the survivor off that name's shortlist), and title aliases
// (so same-role titles stay one role).
func (s *Store) retargetOrganizationReferencesTx(
	ctx context.Context, tx *loggedTx, survivorID, losingID int64,
) error {
	hasKinds, err := s.tableExistsTx(ctx, tx, "correspondent_kinds")
	if err != nil {
		return err
	}
	if hasKinds {
		if _, err := tx.ExecContext(ctx, `
			UPDATE correspondent_kinds SET organization_id = ? WHERE organization_id = ?`,
			survivorID, losingID); err != nil {
			return fmt.Errorf("repoint correspondent kinds to the surviving organization: %w", err)
		}
	}
	if err := retargetOrganizationMatchReviewsTx(ctx, tx, s.dialect, survivorID, losingID); err != nil {
		return err
	}
	if err := carryResolutionAliasesTx(ctx, tx, s.dialect, survivorID, losingID); err != nil {
		return err
	}
	return retargetOrganizationTitleAliasesTx(ctx, tx, s.dialect, survivorID, losingID)
}

// tableExistsTx is tableExists inside a transaction.
func (s *Store) tableExistsTx(ctx context.Context, tx *loggedTx, name string) (bool, error) {
	query := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`
	if s.dialect.DriverName() == postgresDriverName {
		query = `SELECT COUNT(*) FROM information_schema.tables
		         WHERE table_schema = current_schema() AND table_name = ?`
	}
	var count int
	if err := tx.QueryRowContext(ctx, query, name).Scan(&count); err != nil {
		return false, fmt.Errorf("check table %s: %w", name, err)
	}
	return count > 0, nil
}

type organizationMatchReviewMergeRow struct {
	id          int64
	key         string
	status      string
	probability float64
	model       string
	decidedBy   sql.NullString
	decidedAt   sql.NullTime
}

// loadOrganizationMatchReviewMergeRowsTx reads and locks an organization's
// reviews, so a decision cannot commit between the read and the merge's
// rewrite of them (SQLite already holds the writer lock).
func loadOrganizationMatchReviewMergeRowsTx(
	ctx context.Context, tx *loggedTx, dialect Dialect, organizationID int64,
) ([]organizationMatchReviewMergeRow, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, proposed_name_normalized, proposed_domain, status, probability, model,
		       decided_by, decided_at
		FROM organization_match_reviews WHERE organization_id = ? ORDER BY id`+dialect.SelectForUpdate(),
		organizationID)
	if err != nil {
		return nil, fmt.Errorf("load organization match reviews for merge: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var loaded []organizationMatchReviewMergeRow
	for rows.Next() {
		var row organizationMatchReviewMergeRow
		var name, domain string
		if err := rows.Scan(&row.id, &name, &domain, &row.status, &row.probability, &row.model,
			&row.decidedBy, &row.decidedAt); err != nil {
			return nil, fmt.Errorf("scan organization match review for merge: %w", err)
		}
		row.key = name + "\x00" + domain
		loaded = append(loaded, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate organization match reviews for merge: %w", err)
	}
	return loaded, nil
}

// mergedOrganizationMatchReview decides what one name's review says once
// two organizations that were both asked about it become one. The rule does
// not depend on which organization survives: a decision beats a pending
// question; between two decisions the later one wins, and a rejection wins a
// tie; between two pending questions the more probable one stays.
func mergedOrganizationMatchReview(
	left, right organizationMatchReviewMergeRow,
) organizationMatchReviewMergeRow {
	leftDecided, rightDecided := left.status != OrganizationMatchPending, right.status != OrganizationMatchPending
	switch {
	case leftDecided && !rightDecided:
		return left
	case rightDecided && !leftDecided:
		return right
	case !leftDecided:
		if right.probability > left.probability {
			return right
		}
		return left
	}
	leftAt, rightAt := left.decidedAt.Time, right.decidedAt.Time
	switch {
	case leftAt.After(rightAt):
		return left
	case rightAt.After(leftAt):
		return right
	case right.status == OrganizationMatchRejected:
		return right
	default:
		return left
	}
}

func retargetOrganizationMatchReviewsTx(
	ctx context.Context, tx *loggedTx, dialect Dialect, survivorID, losingID int64,
) error {
	survivorRows, err := loadOrganizationMatchReviewMergeRowsTx(ctx, tx, dialect, survivorID)
	if err != nil {
		return err
	}
	survivorByKey := make(map[string]organizationMatchReviewMergeRow, len(survivorRows))
	for _, row := range survivorRows {
		survivorByKey[row.key] = row
	}
	losingRows, err := loadOrganizationMatchReviewMergeRowsTx(ctx, tx, dialect, losingID)
	if err != nil {
		return err
	}
	for _, losing := range losingRows {
		survivor, both := survivorByKey[losing.key]
		if !both {
			if _, err := tx.ExecContext(ctx, `
				UPDATE organization_match_reviews SET organization_id = ? WHERE id = ?`,
				survivorID, losing.id); err != nil {
				return fmt.Errorf("repoint organization match review to the survivor: %w", err)
			}
			continue
		}
		merged := mergedOrganizationMatchReview(survivor, losing)
		if merged.id != survivor.id {
			// Conditional on the state that was read: if a decision still
			// landed in between, the merge fails rather than overwrite it.
			result, err := tx.ExecContext(ctx, `
				UPDATE organization_match_reviews
				SET status = ?, probability = ?, model = ?, decided_by = ?, decided_at = ?
				WHERE id = ? AND status = ?`,
				merged.status, merged.probability, merged.model, merged.decidedBy, merged.decidedAt,
				survivor.id, survivor.status)
			if err != nil {
				return fmt.Errorf("carry organization match decision to the survivor: %w", err)
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("carry organization match decision to the survivor: %w", err)
			}
			if changed != 1 {
				return ErrOrganizationMatchReviewStateChanged
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM organization_match_reviews WHERE id = ?`,
			losing.id); err != nil {
			return fmt.Errorf("drop merged organization match review: %w", err)
		}
	}
	return nil
}

// carryResolutionAliasesTx copies the lookup keys organization resolution
// wrote on the losing organization (judgment aliases and accepted reviews)
// to the survivor before the merge supersedes the losing organization's
// names, so a name that resolved before the merge still resolves after it.
func carryResolutionAliasesTx(
	ctx context.Context, tx *loggedTx, dialect Dialect, survivorID, losingID int64,
) error {
	const resolutionSource = `(source_ref LIKE 'jev:organization_resolution:%' OR
		source_ref LIKE 'organization-match-review:%')`
	if _, err := tx.ExecContext(ctx, dialect.InsertOrIgnore(`
		INSERT OR IGNORE INTO organization_names (
			organization_id, name_kind, formatted, original_value, name_normalized,
			source, source_ref, confidence
		)
		SELECT ?, name_kind, formatted, original_value, name_normalized, source, source_ref, confidence
		FROM organization_names losing
		WHERE losing.organization_id = ? AND losing.active_until IS NULL
		  AND losing.superseded_at IS NULL AND `+resolutionSource+`
		  AND NOT EXISTS (
			SELECT 1 FROM organization_names kept
			WHERE kept.organization_id = ? AND kept.name_normalized = losing.name_normalized
			  AND kept.active_until IS NULL AND kept.superseded_at IS NULL)
		  AND NOT EXISTS (
			SELECT 1 FROM organizations survivor
			WHERE survivor.id = ? AND survivor.name_normalized = losing.name_normalized)`),
		survivorID, losingID, survivorID, survivorID); err != nil {
		return fmt.Errorf("carry resolution alias names to the survivor: %w", err)
	}
	if _, err := tx.ExecContext(ctx, dialect.InsertOrIgnore(`
		INSERT OR IGNORE INTO organization_identifiers (
			organization_id, identifier_kind, identifier_value, normalized_value,
			source, source_ref, confidence
		)
		SELECT ?, identifier_kind, identifier_value, normalized_value, source, source_ref, confidence
		FROM organization_identifiers losing
		WHERE losing.organization_id = ? AND losing.identifier_kind = 'domain'
		  AND losing.active_until IS NULL AND losing.superseded_at IS NULL AND `+resolutionSource+`
		  AND NOT EXISTS (
			SELECT 1 FROM organization_identifiers kept
			WHERE kept.organization_id = ? AND kept.identifier_kind = 'domain'
			  AND kept.normalized_value = losing.normalized_value
			  AND kept.active_until IS NULL AND kept.superseded_at IS NULL)
		  AND NOT EXISTS (
			SELECT 1 FROM organizations survivor
			WHERE survivor.id = ? AND survivor.primary_domain = losing.normalized_value)`),
		survivorID, losingID, survivorID, survivorID); err != nil {
		return fmt.Errorf("carry resolution alias domains to the survivor: %w", err)
	}
	return nil
}

type titleAliasMergeRow struct {
	title, canonical, canonicalDisplay string
	source                             string
	sourceRef                          sql.NullString
	confidence                         sql.NullFloat64
	survivor                           bool
}

func loadTitleAliasMergeRowsTx(
	ctx context.Context, tx *loggedTx, organizationID int64, survivor bool,
) ([]titleAliasMergeRow, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT title_normalized, canonical_title_normalized, canonical_title, source, source_ref, confidence
		FROM organization_title_aliases WHERE organization_id = ? ORDER BY id`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("load title aliases for merge: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var loaded []titleAliasMergeRow
	for rows.Next() {
		row := titleAliasMergeRow{survivor: survivor}
		if err := rows.Scan(&row.title, &row.canonical, &row.canonicalDisplay, &row.source,
			&row.sourceRef, &row.confidence); err != nil {
			return nil, fmt.Errorf("scan title alias for merge: %w", err)
		}
		loaded = append(loaded, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate title aliases for merge: %w", err)
	}
	return loaded, nil
}

// retargetOrganizationTitleAliasesTx merges both organizations' title
// mappings as equivalence classes: every title the two sides connect is one
// role, whatever direction each side mapped it. Each class keeps the
// survivor's canonical title (its first mapping's, when the class joins
// several), or the losing side's when the survivor had none, and every other
// title in the class maps straight to it. No equivalence is dropped.
func retargetOrganizationTitleAliasesTx(
	ctx context.Context, tx *loggedTx, dialect Dialect, survivorID, losingID int64,
) error {
	survivorRows, err := loadTitleAliasMergeRowsTx(ctx, tx, survivorID, true)
	if err != nil {
		return err
	}
	losingRows, err := loadTitleAliasMergeRowsTx(ctx, tx, losingID, false)
	if err != nil {
		return err
	}
	if len(losingRows) == 0 {
		return nil
	}
	rows := make([]titleAliasMergeRow, 0, len(survivorRows)+len(losingRows))
	rows = append(rows, survivorRows...)
	rows = append(rows, losingRows...)
	parent := make(map[string]string)
	var find func(string) string
	find = func(node string) string {
		if parent[node] == "" || parent[node] == node {
			parent[node] = node
			return node
		}
		root := find(parent[node])
		parent[node] = root
		return root
	}
	for _, row := range rows {
		parent[find(row.title)] = find(row.canonical)
	}
	// The first canonical named in survivor-then-losing row order leads its
	// class; its display comes from that row.
	leader := make(map[string]titleAliasMergeRow)
	for _, row := range rows {
		if _, chosen := leader[find(row.canonical)]; !chosen {
			leader[find(row.canonical)] = row
		}
	}
	// Each non-canonical title keeps the provenance of the row that named it,
	// preferring a row where it was the mapped title and the survivor's rows.
	provenance := make(map[string]titleAliasMergeRow)
	for _, row := range rows {
		if _, known := provenance[row.title]; !known {
			provenance[row.title] = row
		}
	}
	for _, row := range rows {
		if _, known := provenance[row.canonical]; !known {
			provenance[row.canonical] = row
		}
	}
	nodes := make([]string, 0, len(parent))
	for node := range parent {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM organization_title_aliases WHERE organization_id IN (?, ?)`,
		survivorID, losingID); err != nil {
		return fmt.Errorf("clear title aliases for merge: %w", err)
	}
	for _, node := range nodes {
		canonical := leader[find(node)]
		if node == canonical.canonical {
			continue
		}
		origin := provenance[node]
		if _, err := tx.ExecContext(ctx, dialect.InsertOrIgnore(`
			INSERT OR IGNORE INTO organization_title_aliases (
				organization_id, title_normalized, canonical_title,
				canonical_title_normalized, source, source_ref, confidence
			) VALUES (?, ?, ?, ?, ?, ?, ?)`),
			survivorID, node, canonical.canonicalDisplay, canonical.canonical,
			origin.source, origin.sourceRef, origin.confidence); err != nil {
			return fmt.Errorf("write merged title alias: %w", err)
		}
	}
	return nil
}

// followEmploymentTitleAlias walks a title's mappings to the canonical title
// at the end of the chain. ok is false when the chain returns to a title it
// already visited, including the title itself.
func followEmploymentTitleAlias(
	direct map[string]employmentTitleAlias, title string,
) (employmentTitleAlias, bool) {
	visited := map[string]struct{}{title: {}}
	current, mapped := direct[title]
	if !mapped {
		return employmentTitleAlias{}, false
	}
	for {
		if _, seen := visited[current.normalized]; seen {
			return employmentTitleAlias{}, false
		}
		visited[current.normalized] = struct{}{}
		next, more := direct[current.normalized]
		if !more {
			return current, true
		}
		current = next
	}
}
