package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"go.kenn.io/msgvault/internal/correspondentkind"
)

// NotPersonParticipantsForContext is NotPersonParticipantsContext scoped to
// the given participants: it maps each of them whose identity cluster
// resolves to a kind other than person to that kind. It never reads the
// whole link graph or every classification. One indexed probe answers
// when nothing is classified; otherwise it walks participant_links out from
// the requested IDs level by level, loads the classification rows of those
// clusters only, and applies the same precedence and owner-identity rule.
func (s *Store) NotPersonParticipantsForContext(
	ctx context.Context, participantIDs []int64,
) (map[int64]correspondentkind.Kind, error) {
	result := map[int64]correspondentkind.Kind{}
	ids := sortedUniqueInt64s(participantIDs...)
	if len(ids) == 0 {
		return result, nil
	}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		classified, err := s.anyNotPersonClassificationTx(ctx, tx)
		if err != nil || !classified {
			return err
		}
		clusters, err := scopedCorrespondentKindClustersTx(ctx, tx, ids, true)
		if err != nil {
			return err
		}
		for _, cluster := range clusters {
			if cluster.effective.kind.IsPerson() {
				continue
			}
			for _, member := range cluster.members {
				if _, requested := slices.BinarySearch(ids, member); requested {
					result[member] = cluster.effective.kind
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// scopedCorrespondentKindClustersTx resolves the clusters containing ids
// that carry a classification row. With notPersonOnly it keeps only the
// clusters with at least one non-person row.
func scopedCorrespondentKindClustersTx(
	ctx context.Context, tx *loggedTx, ids []int64, notPersonOnly bool,
) ([]correspondentKindCluster, error) {
	components, err := linkComponentsFromTx(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	members := make([]int64, 0, len(components))
	for member := range components {
		members = append(members, member)
	}
	slices.Sort(members)
	rows, err := loadCorrespondentKindRowsForTx(ctx, tx, members)
	if err != nil {
		return nil, err
	}
	byRoot := map[int64]*correspondentKindCluster{}
	kept := map[int64]bool{}
	for _, row := range rows {
		root := components[row.participantID]
		if !notPersonOnly || !row.kind.IsPerson() {
			kept[root] = true
		}
		cluster, ok := byRoot[root]
		if !ok {
			byRoot[root] = &correspondentKindCluster{root: root, effective: row}
			continue
		}
		if row.rowWins(cluster.effective) {
			cluster.effective = row
		}
	}
	for _, member := range members {
		root := components[member]
		if cluster, ok := byRoot[root]; ok && kept[root] {
			cluster.members = append(cluster.members, member)
		}
	}
	roots := make([]int64, 0, len(byRoot))
	for root := range byRoot {
		if kept[root] {
			roots = append(roots, root)
		}
	}
	slices.Sort(roots)
	result := make([]correspondentKindCluster, 0, len(roots))
	for _, root := range roots {
		result = append(result, *byRoot[root])
	}
	if err := applyOwnerIdentityRuleTx(ctx, tx, result); err != nil {
		return nil, err
	}
	return result, nil
}

// linkComponentsFromTx maps every participant in the link components of
// seeds to its component's smallest member. It reads only the edges of
// those components, one indexed IN query per side per level.
func linkComponentsFromTx(ctx context.Context, tx *loggedTx, seeds []int64) (map[int64]int64, error) {
	// Union-find over the visited participants.
	parent := make(map[int64]int64, len(seeds))
	var find func(int64) int64
	find = func(id int64) int64 {
		for parent[id] != id {
			parent[id] = parent[parent[id]]
			id = parent[id]
		}
		return id
	}
	union := func(a, b int64) {
		ra, rb := find(a), find(b)
		if ra == rb {
			return
		}
		if ra < rb {
			parent[rb] = ra
		} else {
			parent[ra] = rb
		}
	}
	frontier := make([]int64, 0, len(seeds))
	for _, id := range seeds {
		if _, seen := parent[id]; !seen {
			parent[id] = id
			frontier = append(frontier, id)
		}
	}
	for len(frontier) > 0 {
		next := []int64{}
		visit := func(from, to int64) {
			if _, seen := parent[to]; !seen {
				parent[to] = to
				next = append(next, to)
			}
			union(from, to)
		}
		scan := func(query string, forward bool) error {
			return queryInChunksContext(ctx, tx, frontier, nil, query, func(rows *loggedRows) error {
				var a, b int64
				if err := rows.Scan(&a, &b); err != nil {
					return fmt.Errorf("scan participant link: %w", err)
				}
				if forward {
					visit(a, b)
				} else {
					visit(b, a)
				}
				return nil
			})
		}
		if err := scan(`SELECT participant_a, participant_b FROM participant_links WHERE participant_a IN (%s)`, true); err != nil {
			return nil, fmt.Errorf("walk participant links: %w", err)
		}
		if err := scan(`SELECT participant_a, participant_b FROM participant_links WHERE participant_b IN (%s)`, false); err != nil {
			return nil, fmt.Errorf("walk participant links: %w", err)
		}
		frontier = next
	}
	components := make(map[int64]int64, len(parent))
	for id := range parent {
		components[id] = find(id)
	}
	return components, nil
}

// loadCorrespondentKindRowsForTx loads the classification rows of the given
// participants, ordered as loadCorrespondentKindRowsTx orders them.
func loadCorrespondentKindRowsForTx(
	ctx context.Context, tx *loggedTx, participantIDs []int64,
) ([]correspondentKindRow, error) {
	result := []correspondentKindRow{}
	err := queryInChunksContext(ctx, tx, participantIDs, nil, `
		SELECT ck.participant_id, ck.source, ck.kind, ck.organization_id, o.name,
		       ck.actor, ck.classified_at, ck.confidence, ck.probabilities_json
		FROM correspondent_kinds ck
		LEFT JOIN organizations o ON o.id = ck.organization_id
		WHERE ck.participant_id IN (%s)
		ORDER BY ck.participant_id, ck.source`, func(rows *loggedRows) error {
		var row correspondentKindRow
		var source, kind string
		var organizationID sql.NullInt64
		var organizationName, actor, probabilities sql.NullString
		var classifiedAt nullableTimestamp
		var confidence sql.NullFloat64
		if err := rows.Scan(&row.participantID, &source, &kind, &organizationID,
			&organizationName, &actor, &classifiedAt, &confidence, &probabilities); err != nil {
			return fmt.Errorf("scan correspondent kind: %w", err)
		}
		if confidence.Valid {
			row.confidence = &confidence.Float64
		}
		row.probabilities = decodeKindProbabilities(probabilities)
		row.source = correspondentkind.Source(source)
		row.kind = correspondentkind.Kind(kind)
		if organizationID.Valid {
			row.organizationID = &organizationID.Int64
		}
		row.organizationName = nonBlankString(organizationName)
		row.actor = nonBlankString(actor)
		row.classifiedAt = classifiedAt.Time
		result = append(result, row)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load correspondent kinds: %w", err)
	}
	return result, nil
}
