package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"go.kenn.io/msgvault/internal/correspondentkind"
)

// PendingReviewKind names a Reviews queue that can wait for a decision.
type PendingReviewKind string

const (
	// PendingReviewIdentity covers open identity match candidates: contact
	// matches, possible duplicate people, and other proposed links.
	PendingReviewIdentity PendingReviewKind = "identity"
	// PendingReviewEnrichment covers enrichment lookups whose identity check
	// was uncertain.
	PendingReviewEnrichment PendingReviewKind = "enrichment"
	// PendingReviewOrganization covers organization names the check could
	// not match with confidence.
	PendingReviewOrganization PendingReviewKind = "organization"
	// PendingReviewCorrespondent covers identities Jev could not classify.
	PendingReviewCorrespondent PendingReviewKind = "correspondent"
	// PendingReviewRelationship covers imported relationships awaiting a
	// decision.
	PendingReviewRelationship PendingReviewKind = "relationship"
)

// pendingReviewQuery is one indexed existence probe. Each returns at most
// one row, so the whole check reads a handful of index entries no matter
// how large the queues are.
type pendingReviewQuery struct {
	kind  PendingReviewKind
	query string
	args  []any
	// confirm, when set, decides a hit the way the queue itself does. It
	// runs only after the indexed probe finds a candidate row.
	confirm func(ctx context.Context, s *Store, tx *loggedTx) (bool, error)
}

func pendingReviewQueries() []pendingReviewQuery {
	return []pendingReviewQuery{
		{
			kind: PendingReviewIdentity,
			query: `SELECT 1 FROM identity_match_candidates
				WHERE state IN (?, ?) LIMIT 1`,
			args: []any{IdentityMatchStateCandidate, IdentityMatchStateConflict},
		},
		{
			kind:  PendingReviewRelationship,
			query: `SELECT 1 FROM person_relationship_reviews WHERE status = ? LIMIT 1`,
			args:  []any{RelationshipReviewPending},
		},
		{
			kind: PendingReviewEnrichment,
			query: `SELECT 1 FROM person_enrichment_attempts a
				WHERE a.state = ? AND EXISTS (
					SELECT 1 FROM person_enrichment_identity_judgments j WHERE j.attempt_id = a.id)
				LIMIT 1`,
			args: []any{personEnrichmentStateIdentityUncertain},
		},
		{
			kind: PendingReviewOrganization,
			query: `SELECT 1 FROM organization_match_reviews r
				WHERE r.status = 'pending' AND EXISTS (
					SELECT 1 FROM organizations o
					WHERE o.id = r.organization_id AND o.merged_into_id IS NULL
					  AND o.retired_at IS NULL)
				LIMIT 1`,
		},
		{
			// A Jev judgment is effective only while no user or rule
			// decision outranks it. The probe rules out the same
			// participant cheaply; confirm then applies the cluster rule
			// the Unclear correspondents queue lists by.
			kind:    PendingReviewCorrespondent,
			confirm: unclearCorrespondentWaitingTx,
			query: `SELECT 1 FROM correspondent_kinds k
				WHERE k.source = ? AND k.kind = ? AND NOT EXISTS (
					SELECT 1 FROM correspondent_kinds d
					WHERE d.participant_id = k.participant_id AND d.source IN (?, ?))
				LIMIT 1`,
			args: []any{
				correspondentkind.SourceJev, correspondentkind.Unclear,
				correspondentkind.SourceUser, correspondentkind.SourceRule,
			},
		},
	}
}

const (
	// unclearPendingBatch and unclearPendingMaxBatches bound the cluster
	// check: at most this many batches of candidate identities are resolved
	// per call before the check answers "waiting" and leaves the rest to
	// the queue, which is authoritative when Reviews opens.
	unclearPendingBatch      = 50
	unclearPendingMaxBatches = 4
)

// resolveUnclearCandidateClustersTx resolves only the clusters of the given
// candidate identities. Tests swap it to count what is loaded.
var resolveUnclearCandidateClustersTx = scopedCorrespondentKindClustersTx

// unclearCorrespondentWaitingTx reports whether any Jev unclear judgment is
// still the effective kind of its cluster, applying the rule the Unclear
// correspondents queue lists by. It resolves candidate clusters in small
// batches, never the whole link graph or every classification.
func unclearCorrespondentWaitingTx(ctx context.Context, _ *Store, tx *loggedTx) (bool, error) {
	after := int64(0)
	for range unclearPendingMaxBatches {
		ids, err := unclearCandidateBatchTx(ctx, tx, after)
		if err != nil {
			return false, err
		}
		if len(ids) == 0 {
			return false, nil
		}
		clusters, err := resolveUnclearCandidateClustersTx(ctx, tx, ids, false)
		if err != nil {
			return false, err
		}
		if slices.ContainsFunc(clusters, isUnclearCorrespondentCluster) {
			return true, nil
		}
		if len(ids) < unclearPendingBatch {
			return false, nil
		}
		after = ids[len(ids)-1]
	}
	return true, nil
}

// unclearCandidateBatchTx returns the next participants, by ID, that carry
// a Jev unclear judgment with no user or rule decision of their own.
func unclearCandidateBatchTx(ctx context.Context, tx *loggedTx, after int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT k.participant_id FROM correspondent_kinds k
		WHERE k.source = ? AND k.kind = ? AND k.participant_id > ? AND NOT EXISTS (
			SELECT 1 FROM correspondent_kinds d
			WHERE d.participant_id = k.participant_id AND d.source IN (?, ?))
		ORDER BY k.participant_id LIMIT ?`,
		correspondentkind.SourceJev, correspondentkind.Unclear, after,
		correspondentkind.SourceUser, correspondentkind.SourceRule, unclearPendingBatch)
	if err != nil {
		return nil, fmt.Errorf("list unclear candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan unclear candidate: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// AllPendingReviewKinds lists every queue the pending check covers, in
// Reviews order.
func AllPendingReviewKinds() []PendingReviewKind {
	queries := pendingReviewQueries()
	kinds := make([]PendingReviewKind, 0, len(queries))
	for _, probe := range queries {
		kinds = append(kinds, probe.kind)
	}
	return kinds
}

// PendingReviewKindsContext reports which Reviews queues have at least one
// item waiting, in queue order. It answers "is anything waiting?" for a
// navigation hint, not how many: each queue costs one indexed probe, and
// unclear correspondents also resolve the clusters of a bounded batch of
// candidates when that probe hits.
func (s *Store) PendingReviewKindsContext(ctx context.Context) ([]PendingReviewKind, error) {
	kinds := []PendingReviewKind{}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		for _, probe := range pendingReviewQueries() {
			var one int
			err := tx.QueryRowContext(ctx, probe.query, probe.args...).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return fmt.Errorf("check pending %s reviews: %w", probe.kind, err)
			}
			if probe.confirm != nil {
				waiting, err := probe.confirm(ctx, s, tx)
				if err != nil {
					return fmt.Errorf("confirm pending %s reviews: %w", probe.kind, err)
				}
				if !waiting {
					continue
				}
			}
			kinds = append(kinds, probe.kind)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return kinds, nil
}
