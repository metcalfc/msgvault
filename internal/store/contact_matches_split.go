package store

import (
	"context"
	"fmt"
	"slices"
)

// rememberContactProfileSplitTx keeps a split from being undone by contact
// matching. When one side of a split is left with no archive identity, it is
// a contact-only profile again, and its exact email would match the other
// side's identities: the rule would merge it straight back, or the queue
// would ask again. Every contact-match candidate between the two sides is
// rejected, and each identity cluster of the other side gets a rejection
// against the contact-only side if it has none, as the user's decision.
func (s *Store) rememberContactProfileSplitTx(
	ctx context.Context, tx *loggedTx, firstID, secondID int64, actor string,
) error {
	firstParticipants, err := personParticipantIDsTx(ctx, tx, firstID)
	if err != nil {
		return err
	}
	secondParticipants, err := personParticipantIDsTx(ctx, tx, secondID)
	if err != nil {
		return err
	}
	var contactID int64
	var participants []int64
	switch {
	case len(firstParticipants) == 0 && len(secondParticipants) > 0:
		contactID, participants = firstID, secondParticipants
	case len(secondParticipants) == 0 && len(firstParticipants) > 0:
		contactID, participants = secondID, firstParticipants
	default:
		return nil
	}
	edges, err := s.loadLinkEdgesTxContext(ctx, tx)
	if err != nil {
		return err
	}
	roots := clustersFromEdges(edges)
	clusters := map[int64][]int64{}
	for _, id := range participants {
		root, ok := roots[id]
		if !ok {
			root = id
		}
		clusters[root] = append(clusters[root], id)
	}
	for _, members := range clusters {
		slices.Sort(members)
		if err := s.rejectClusterForContactProfileTx(ctx, tx, members, contactID, actor); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) rejectClusterForContactProfileTx(
	ctx context.Context, tx *loggedTx, members []int64, personID int64, actor string,
) error {
	if err := execInChunksContext(ctx, tx, members,
		[]any{IdentityMatchStateRejected, actor, personSplitNote,
			IdentityMatchParticipant, IdentityMatchPerson, personID, IdentityMatchStateRejected},
		`UPDATE identity_match_candidates SET
			state = ?, decided_by = ?, decided_at = `+s.dialect.Now()+`, notes = ?,
			pre_conflict_state = NULL, application_pending = FALSE,
			updated_at = `+s.dialect.Now()+`
		WHERE left_kind = ? AND right_kind = ? AND right_id = ? AND state <> ?
		  AND left_id IN (%s)`); err != nil {
		return fmt.Errorf("reject split contact matches: %w", err)
	}
	rejected := false
	if err := queryInChunksContext(ctx, tx, members,
		[]any{IdentityMatchParticipant, IdentityMatchPerson, personID, IdentityMatchStateRejected}, `
		SELECT id FROM identity_match_candidates
		WHERE left_kind = ? AND right_kind = ? AND right_id = ? AND state = ?
		  AND left_id IN (%s)`, func(rows *loggedRows) error {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("scan split contact match rejection: %w", err)
			}
			rejected = true
			return nil
		}); err != nil {
		return fmt.Errorf("check split contact match rejections: %w", err)
	}
	if rejected {
		return nil
	}
	representative := members[0]
	basis, normalized, err := participantTombstoneBasisTx(ctx, tx, representative)
	if err != nil {
		return err
	}
	sourceRef := ContactMatchSourceRef
	if _, err := tx.ExecContext(ctx, `INSERT INTO identity_match_candidates (
		left_kind, left_id, right_kind, right_id, basis, normalized_value, state,
		source, source_ref, notes, decided_by, decided_at, application_pending,
		created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, `+s.dialect.Now()+`, FALSE,
		`+s.dialect.Now()+`, `+s.dialect.Now()+`)`,
		IdentityMatchParticipant, representative, IdentityMatchPerson, personID,
		basis, normalized, IdentityMatchStateRejected,
		ProvenanceUser, sourceRef, personSplitNote, actor,
	); err != nil {
		return fmt.Errorf("record split contact profile rejection: %w", err)
	}
	return nil
}

func personParticipantIDsTx(ctx context.Context, tx *loggedTx, personID int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT participant_id FROM person_participants
		WHERE person_id = ? ORDER BY participant_id`, personID)
	if err != nil {
		return nil, fmt.Errorf("load person %d participants: %w", personID, err)
	}
	defer func() { _ = rows.Close() }()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan person %d participant: %w", personID, err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// personSeparations answers whether two current people were ever split
// apart, including splits made before contact matching recorded its own
// rejections. A split separates its source and new person; a later merge
// carries that separation to the survivor, so each historical person ID is
// followed through merges to the person that holds it now.
type personSeparations struct {
	absorbedInto map[int64]int64
	pairs        map[[2]int64]struct{}
}

func loadPersonSeparationsTx(ctx context.Context, tx *loggedTx) (*personSeparations, error) {
	separations := &personSeparations{
		absorbedInto: map[int64]int64{}, pairs: map[[2]int64]struct{}{},
	}
	rows, err := tx.QueryContext(ctx, `SELECT absorbed_person_id, survivor_person_id_at_merge
		FROM person_merges ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load person merge history: %w", err)
	}
	for rows.Next() {
		var absorbed, survivor int64
		if err := rows.Scan(&absorbed, &survivor); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan person merge history: %w", err)
		}
		separations.absorbedInto[absorbed] = survivor
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close person merge history: %w", err)
	}
	rows, err = tx.QueryContext(ctx, `SELECT source_person_id, new_person_id
		FROM person_splits ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load person split history: %w", err)
	}
	splits := [][2]int64{}
	for rows.Next() {
		var source, created int64
		if err := rows.Scan(&source, &created); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan person split history: %w", err)
		}
		splits = append(splits, [2]int64{source, created})
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close person split history: %w", err)
	}
	for _, split := range splits {
		separations.pairs[separationKey(
			separations.current(split[0]), separations.current(split[1]))] = struct{}{}
	}
	return separations, nil
}

// current follows a person ID through the merges that absorbed it.
func (p *personSeparations) current(id int64) int64 {
	seen := map[int64]struct{}{}
	for {
		next, absorbed := p.absorbedInto[id]
		if !absorbed {
			return id
		}
		if _, loop := seen[id]; loop {
			return id
		}
		seen[id] = struct{}{}
		id = next
	}
}

func (p *personSeparations) separated(a, b int64) bool {
	_, found := p.pairs[separationKey(p.current(a), p.current(b))]
	return found
}

func separationKey(a, b int64) [2]int64 {
	if a > b {
		a, b = b, a
	}
	return [2]int64{a, b}
}
