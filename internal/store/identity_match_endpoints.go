package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
)

// maxEndpointSummaryAddresses bounds the addresses shown for one endpoint.
const maxEndpointSummaryAddresses = 3

// IdentityMatchEndpointSummary resolves one candidate endpoint to what a
// reviewer recognizes: a name, its email or phone addresses, and the person
// that owns it. Found is false when the endpoint row no longer exists.
type IdentityMatchEndpointSummary struct {
	Kind              IdentityMatchEndpointKind `json:"kind"`
	ID                int64                     `json:"id"`
	Found             bool                      `json:"found"`
	DisplayName       *string                   `json:"display_name,omitzero" nullable:"false"`
	Addresses         []string                  `json:"addresses"`
	PersonID          *int64                    `json:"person_id,omitzero" nullable:"false"`
	PersonDisplayName *string                   `json:"person_display_name,omitzero" nullable:"false"`
}

// ContactMatchStatus is the live cluster-level verdict for one
// participant-to-person candidate: what accepting it would do now, and
// whether a person merge it needs would be refused.
type ContactMatchStatus struct {
	CandidateID      int64                      `json:"candidate_id"`
	Classification   ContactMatchClassification `json:"classification" enum:"bind,merge,ambiguous,linked"`
	BlockedReason    *ContactMatchBlockReason   `json:"blocked_reason,omitzero" nullable:"false" enum:"published,carddav_conflict"`
	ClusterPersonIDs []int64                    `json:"cluster_person_ids"`
}

type endpointKey struct {
	kind IdentityMatchEndpointKind
	id   int64
}

// DescribeIdentityMatchCandidatesContext resolves every endpoint of the given
// candidates and computes the live status of each participant-to-person
// candidate, from one consistent snapshot.
func (s *Store) DescribeIdentityMatchCandidatesContext(
	ctx context.Context, candidates []IdentityMatchCandidate,
) ([]IdentityMatchEndpointSummary, []ContactMatchStatus, error) {
	summaries := []IdentityMatchEndpointSummary{}
	statuses := []ContactMatchStatus{}
	if len(candidates) == 0 {
		return summaries, statuses, nil
	}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		var err error
		summaries, err = describeIdentityMatchEndpointsTx(ctx, tx, candidates)
		if err != nil {
			return err
		}
		statuses, err = s.participantPersonMatchStatusesTx(ctx, tx, candidates)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return summaries, statuses, nil
}

func describeIdentityMatchEndpointsTx(
	ctx context.Context, tx *loggedTx, candidates []IdentityMatchCandidate,
) ([]IdentityMatchEndpointSummary, error) {
	keys := []endpointKey{}
	seen := map[endpointKey]struct{}{}
	byKind := map[IdentityMatchEndpointKind][]int64{}
	for _, candidate := range candidates {
		for _, key := range []endpointKey{
			{candidate.LeftKind, candidate.LeftID}, {candidate.RightKind, candidate.RightID},
		} {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
			byKind[key.kind] = append(byKind[key.kind], key.id)
		}
	}
	summaries := map[endpointKey]*IdentityMatchEndpointSummary{}
	for _, key := range keys {
		summaries[key] = &IdentityMatchEndpointSummary{
			Kind: key.kind, ID: key.id, Addresses: []string{},
		}
	}
	ownerPersons := map[endpointKey]int64{}

	if err := describeParticipantEndpointsTx(ctx, tx, byKind[IdentityMatchParticipant],
		summaries, ownerPersons); err != nil {
		return nil, err
	}
	if err := describeSimpleEndpointsTx(ctx, tx, IdentityMatchObservation, byKind[IdentityMatchObservation],
		`SELECT o.id, p.display_name, o.original_value, NULL
		 FROM participant_contact_observations o
		 JOIN participants p ON p.id = o.participant_id
		 WHERE o.id IN (%s)`, summaries, ownerPersons); err != nil {
		return nil, err
	}
	if err := describeSimpleEndpointsTx(ctx, tx, IdentityMatchContactPoint, byKind[IdentityMatchContactPoint],
		`SELECT id, NULL, original_value, person_id FROM person_contact_points WHERE id IN (%s)`,
		summaries, ownerPersons); err != nil {
		return nil, err
	}
	if err := describeSimpleEndpointsTx(ctx, tx, IdentityMatchCardDAVResource, byKind[IdentityMatchCardDAVResource],
		`SELECT id, NULL, href, person_id FROM carddav_resources WHERE id IN (%s)`,
		summaries, ownerPersons); err != nil {
		return nil, err
	}
	for _, id := range byKind[IdentityMatchPerson] {
		ownerPersons[endpointKey{IdentityMatchPerson, id}] = id
	}

	personIDs := []int64{}
	for _, personID := range ownerPersons {
		personIDs = append(personIDs, personID)
	}
	names, addresses, err := personNamesAndAddressesTx(ctx, tx, personIDs)
	if err != nil {
		return nil, err
	}
	result := make([]IdentityMatchEndpointSummary, 0, len(keys))
	for _, key := range keys {
		summary := summaries[key]
		if personID, ok := ownerPersons[key]; ok {
			if name, exists := names[personID]; exists {
				summary.PersonID = &personID
				summary.PersonDisplayName = name
			}
		}
		if key.kind == IdentityMatchPerson {
			if _, exists := names[key.id]; exists {
				summary.Found = true
				summary.DisplayName = names[key.id]
				summary.Addresses = append(summary.Addresses, addresses[key.id]...)
			}
		}
		result = append(result, *summary)
	}
	return result, nil
}

func describeParticipantEndpointsTx(
	ctx context.Context, tx *loggedTx, ids []int64,
	summaries map[endpointKey]*IdentityMatchEndpointSummary,
	ownerPersons map[endpointKey]int64,
) error {
	if len(ids) == 0 {
		return nil
	}
	if err := queryInChunksContext(ctx, tx, ids, nil, `
		SELECT id, display_name, email_address, phone_number FROM participants
		WHERE id IN (%s)`, func(rows *loggedRows) error {
		var id int64
		var name, email, phone sql.NullString
		if err := rows.Scan(&id, &name, &email, &phone); err != nil {
			return fmt.Errorf("scan participant endpoint: %w", err)
		}
		summary := summaries[endpointKey{IdentityMatchParticipant, id}]
		summary.Found = true
		summary.DisplayName = nonBlankString(name)
		for _, value := range []sql.NullString{email, phone} {
			if address := nonBlankString(value); address != nil {
				summary.Addresses = append(summary.Addresses, *address)
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("describe participant endpoints: %w", err)
	}
	if err := queryInChunksContext(ctx, tx, ids, nil, `
		SELECT participant_id, MIN(person_id) FROM person_participants
		WHERE participant_id IN (%s) GROUP BY participant_id`, func(rows *loggedRows) error {
		var participantID, personID int64
		if err := rows.Scan(&participantID, &personID); err != nil {
			return fmt.Errorf("scan participant endpoint person: %w", err)
		}
		ownerPersons[endpointKey{IdentityMatchParticipant, participantID}] = personID
		return nil
	}); err != nil {
		return fmt.Errorf("describe participant endpoint persons: %w", err)
	}
	return nil
}

// describeSimpleEndpointsTx fills endpoints whose query returns id, an
// optional name, one address, and an optional owning person.
func describeSimpleEndpointsTx(
	ctx context.Context, tx *loggedTx, kind IdentityMatchEndpointKind, ids []int64,
	query string, summaries map[endpointKey]*IdentityMatchEndpointSummary,
	ownerPersons map[endpointKey]int64,
) error {
	if len(ids) == 0 {
		return nil
	}
	if err := queryInChunksContext(ctx, tx, ids, nil, query, func(rows *loggedRows) error {
		var id int64
		var name, address sql.NullString
		var personID sql.NullInt64
		if err := rows.Scan(&id, &name, &address, &personID); err != nil {
			return fmt.Errorf("scan %s endpoint: %w", kind, err)
		}
		key := endpointKey{kind, id}
		summary := summaries[key]
		summary.Found = true
		summary.DisplayName = nonBlankString(name)
		if value := nonBlankString(address); value != nil {
			summary.Addresses = append(summary.Addresses, *value)
		}
		if personID.Valid {
			ownerPersons[key] = personID.Int64
		}
		return nil
	}); err != nil {
		return fmt.Errorf("describe %s endpoints: %w", kind, err)
	}
	return nil
}

// personNamesAndAddressesTx returns each existing person's display name
// (nil when unnamed) and up to three current email or phone contact points.
func personNamesAndAddressesTx(
	ctx context.Context, tx *loggedTx, personIDs []int64,
) (map[int64]*string, map[int64][]string, error) {
	ids := slices.Clone(personIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	names := map[int64]*string{}
	addresses := map[int64][]string{}
	if len(ids) == 0 {
		return names, addresses, nil
	}
	if err := queryInChunksContext(ctx, tx, ids, nil, `
		SELECT id, display_name FROM persons WHERE id IN (%s)`, func(rows *loggedRows) error {
		var id int64
		var name sql.NullString
		if err := rows.Scan(&id, &name); err != nil {
			return fmt.Errorf("scan person endpoint: %w", err)
		}
		names[id] = nonBlankString(name)
		return nil
	}); err != nil {
		return nil, nil, fmt.Errorf("describe person endpoints: %w", err)
	}
	if err := queryInChunksContext(ctx, tx, ids, []any{ContactAddressEmail, ContactAddressPhone}, `
		SELECT person_id, original_value FROM person_contact_points
		WHERE address_kind IN (?, ?)
		  AND active_until IS NULL AND superseded_at IS NULL
		  AND person_id IN (%s)
		ORDER BY person_id, CASE WHEN pref IS NULL THEN 1 ELSE 0 END, pref, ordinal, id`,
		func(rows *loggedRows) error {
			var personID int64
			var value string
			if err := rows.Scan(&personID, &value); err != nil {
				return fmt.Errorf("scan person endpoint address: %w", err)
			}
			if len(addresses[personID]) < maxEndpointSummaryAddresses &&
				!slices.Contains(addresses[personID], value) {
				addresses[personID] = append(addresses[personID], value)
			}
			return nil
		}); err != nil {
		return nil, nil, fmt.Errorf("describe person endpoint addresses: %w", err)
	}
	return names, addresses, nil
}

func nonBlankString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	trimmed := trimmedOrNil(&value.String)
	return trimmed
}

// participantPersonMatchStatusesTx classifies each participant-to-person
// candidate against the current link graph and bindings.
func (s *Store) participantPersonMatchStatusesTx(
	ctx context.Context, tx *loggedTx, candidates []IdentityMatchCandidate,
) ([]ContactMatchStatus, error) {
	relevant := []IdentityMatchCandidate{}
	for _, candidate := range candidates {
		if candidate.LeftKind == IdentityMatchParticipant && candidate.RightKind == IdentityMatchPerson {
			relevant = append(relevant, candidate)
		}
	}
	statuses := make([]ContactMatchStatus, 0, len(relevant))
	if len(relevant) == 0 {
		return statuses, nil
	}
	edges, err := s.loadLinkEdgesTxContext(ctx, tx)
	if err != nil {
		return nil, err
	}
	adjacency := buildAdjacency(edges)
	clusters := make(map[int64][]int64, len(relevant))
	allMembers := []int64{}
	for _, candidate := range relevant {
		component := componentOfAdj(candidate.LeftID, adjacency)
		members := make([]int64, 0, len(component))
		for id := range component {
			members = append(members, id)
		}
		slices.Sort(members)
		clusters[candidate.ID] = members
		allMembers = append(allMembers, members...)
	}
	bindings, err := personBindingsForParticipantsTx(ctx, tx, allMembers)
	if err != nil {
		return nil, err
	}
	matches := make([]ContactMatch, 0, len(relevant))
	personIDs := []int64{}
	for _, candidate := range relevant {
		persons := personsForMembers(clusters[candidate.ID], bindings)
		matches = append(matches, ContactMatch{
			ContactPersonID:  candidate.RightID,
			ParticipantID:    candidate.LeftID,
			ClusterMembers:   clusters[candidate.ID],
			ClusterPersonIDs: persons,
			Classification:   classifyContactMatch(candidate.RightID, persons),
		})
		personIDs = append(personIDs, candidate.RightID)
		personIDs = append(personIDs, persons...)
	}
	blocks, err := contactMatchBlockReasonsTx(ctx, tx, personIDs)
	if err != nil {
		return nil, err
	}
	for i, candidate := range relevant {
		statuses = append(statuses, ContactMatchStatus{
			CandidateID:      candidate.ID,
			Classification:   matches[i].Classification,
			BlockedReason:    contactMatchBlockedReason(matches[i], blocks),
			ClusterPersonIDs: matches[i].ClusterPersonIDs,
		})
	}
	return statuses, nil
}
