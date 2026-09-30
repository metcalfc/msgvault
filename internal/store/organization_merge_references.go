package store

import (
	"context"
	"fmt"
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
	"organization_match_reviews.organization_id":     "repointed; a rejection outranks a pending review",
	"organization_title_aliases.organization_id":     "repointed; the survivor's mapping wins and chains collapse",
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
	if err := retargetOrganizationMatchReviewsTx(ctx, tx, survivorID, losingID); err != nil {
		return err
	}
	return retargetOrganizationTitleAliasesTx(ctx, tx, survivorID, losingID)
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

func retargetOrganizationMatchReviewsTx(
	ctx context.Context, tx *loggedTx, survivorID, losingID int64,
) error {
	// Both organizations were asked about the same name: they are one
	// organization now, so a rejection on either side decides the pending one.
	if _, err := tx.ExecContext(ctx, `
		UPDATE organization_match_reviews
		SET status = 'rejected',
		    decided_by = (
				SELECT losing.decided_by FROM organization_match_reviews losing
				WHERE losing.organization_id = ? AND losing.status = 'rejected'
				  AND losing.proposed_name_normalized = organization_match_reviews.proposed_name_normalized
				  AND losing.proposed_domain = organization_match_reviews.proposed_domain),
		    decided_at = (
				SELECT losing.decided_at FROM organization_match_reviews losing
				WHERE losing.organization_id = ? AND losing.status = 'rejected'
				  AND losing.proposed_name_normalized = organization_match_reviews.proposed_name_normalized
				  AND losing.proposed_domain = organization_match_reviews.proposed_domain)
		WHERE organization_id = ? AND status = 'pending' AND EXISTS (
			SELECT 1 FROM organization_match_reviews losing
			WHERE losing.organization_id = ? AND losing.status = 'rejected'
			  AND losing.proposed_name_normalized = organization_match_reviews.proposed_name_normalized
			  AND losing.proposed_domain = organization_match_reviews.proposed_domain)`,
		losingID, losingID, survivorID, losingID); err != nil {
		return fmt.Errorf("carry organization match rejections to the survivor: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM organization_match_reviews
		WHERE organization_id = ? AND EXISTS (
			SELECT 1 FROM organization_match_reviews survivor
			WHERE survivor.organization_id = ?
			  AND survivor.proposed_name_normalized = organization_match_reviews.proposed_name_normalized
			  AND survivor.proposed_domain = organization_match_reviews.proposed_domain)`,
		losingID, survivorID); err != nil {
		return fmt.Errorf("drop duplicate organization match reviews: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE organization_match_reviews SET organization_id = ? WHERE organization_id = ?`,
		survivorID, losingID); err != nil {
		return fmt.Errorf("repoint organization match reviews to the survivor: %w", err)
	}
	return nil
}

func retargetOrganizationTitleAliasesTx(
	ctx context.Context, tx *loggedTx, survivorID, losingID int64,
) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM organization_title_aliases
		WHERE organization_id = ? AND EXISTS (
			SELECT 1 FROM organization_title_aliases survivor
			WHERE survivor.organization_id = ?
			  AND survivor.title_normalized = organization_title_aliases.title_normalized)`,
		losingID, survivorID); err != nil {
		return fmt.Errorf("drop title aliases the survivor already decides: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE organization_title_aliases SET organization_id = ? WHERE organization_id = ?`,
		survivorID, losingID); err != nil {
		return fmt.Errorf("repoint title aliases to the survivor: %w", err)
	}
	return collapseEmploymentTitleAliasesTx(ctx, tx, survivorID)
}

// collapseEmploymentTitleAliasesTx restores the invariant that every title
// maps straight to a canonical title that has no mapping itself: each chain
// is pointed at its end, and a row whose chain loops back is dropped.
func collapseEmploymentTitleAliasesTx(ctx context.Context, tx *loggedTx, organizationID int64) error {
	direct, err := directEmploymentTitleAliasesTx(ctx, tx, organizationID)
	if err != nil {
		return err
	}
	for title, alias := range direct {
		final, ok := followEmploymentTitleAlias(direct, title)
		switch {
		case !ok:
			if _, err := tx.ExecContext(ctx, `
				DELETE FROM organization_title_aliases
				WHERE organization_id = ? AND title_normalized = ?`, organizationID, title); err != nil {
				return fmt.Errorf("drop looping title alias: %w", err)
			}
		case final.normalized != alias.normalized:
			if _, err := tx.ExecContext(ctx, `
				UPDATE organization_title_aliases
				SET canonical_title = ?, canonical_title_normalized = ?
				WHERE organization_id = ? AND title_normalized = ?`,
				final.display, final.normalized, organizationID, title); err != nil {
				return fmt.Errorf("collapse title alias chain: %w", err)
			}
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
