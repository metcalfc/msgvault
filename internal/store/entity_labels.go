package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
)

// MaxEntityLabelIDs bounds each kind of one EntityLabelsContext request.
const MaxEntityLabelIDs = 500

// ErrEntityLabelRequestTooLarge reports a request with more distinct IDs of
// one kind than MaxEntityLabelIDs.
var ErrEntityLabelRequestTooLarge = errors.New("entity label request too large")

// EntityLabelRequest names the entities a caller needs labels for. IDs may
// repeat; each kind is deduplicated before the cap applies.
type EntityLabelRequest struct {
	PersonIDs       []int64
	ParticipantIDs  []int64
	OrganizationIDs []int64
}

// EntityLabels maps each requested ID to its human label. An ID with no
// label (no row, or a row with nothing to name it by) is absent: callers
// must never render the ID itself in its place.
type EntityLabels struct {
	People        map[int64]string
	Participants  map[int64]string
	Organizations map[int64]string
}

// sqlParticipantIdentifierLabelExpr renders the identifier chain for one
// participants row (alias): phone → email → best stored identifier evidence.
// It is the store-side counterpart of the analytics fallback chain in
// internal/query/person_label.go, without an ID-based terminal fallback, and
// yields NULL when the participant has no identifier.
func sqlParticipantIdentifierLabelExpr(alias string) string {
	return `COALESCE(NULLIF(TRIM(` + alias + `.phone_number), ''), NULLIF(TRIM(` + alias + `.email_address), ''),
		(SELECT COALESCE(NULLIF(TRIM(pi.display_value), ''), NULLIF(TRIM(pi.identifier_value), ''))
		 FROM participant_identifiers pi
		 WHERE pi.participant_id = ` + alias + `.id
		   AND COALESCE(NULLIF(TRIM(pi.display_value), ''), NULLIF(TRIM(pi.identifier_value), '')) IS NOT NULL
		 ORDER BY pi.is_primary DESC, pi.identifier_type, pi.identifier_value LIMIT 1))`
}

// sqlParticipantLabelExpr renders one participant's label: its trimmed
// display name, then its identifier chain. NULL when it has neither.
func sqlParticipantLabelExpr(alias string) string {
	return `COALESCE(NULLIF(TRIM(` + alias + `.display_name), ''), ` + sqlParticipantIdentifierLabelExpr(alias) + `)`
}

// sqlDurablePersonLabelExpr renders the label of one durable persons row
// (alias): its trimmed display name, then the display name of its
// smallest-ID named bound participant, then the identifier chain of its
// smallest-ID bound participant that has one. NULL when nothing names the
// person; it never falls back to the person ID or vCard UID.
func sqlDurablePersonLabelExpr(alias string) string {
	return `COALESCE(NULLIF(TRIM(` + alias + `.display_name), ''),
		(SELECT NULLIF(TRIM(dpl_named.display_name), '')
		 FROM person_participants dpl_named_binding
		 JOIN participants dpl_named ON dpl_named.id = dpl_named_binding.participant_id
		 WHERE dpl_named_binding.person_id = ` + alias + `.id
		   AND NULLIF(TRIM(dpl_named.display_name), '') IS NOT NULL
		 ORDER BY dpl_named.id LIMIT 1),
		(SELECT ` + sqlParticipantIdentifierLabelExpr("dpl_identified") + `
		 FROM person_participants dpl_identified_binding
		 JOIN participants dpl_identified ON dpl_identified.id = dpl_identified_binding.participant_id
		 WHERE dpl_identified_binding.person_id = ` + alias + `.id
		   AND ` + sqlParticipantIdentifierLabelExpr("dpl_identified") + ` IS NOT NULL
		 ORDER BY dpl_identified.id LIMIT 1))`
}

// EntityLabelsContext resolves human labels for people, participants, and
// organizations. A person absorbed by a merge (its row deleted) is named by
// the display name its most recent merge snapshot recorded, else by the
// participants it was bound to at that merge.
func (s *Store) EntityLabelsContext(ctx context.Context, request EntityLabelRequest) (EntityLabels, error) {
	personIDs := sortedUniqueInt64s(request.PersonIDs...)
	participantIDs := sortedUniqueInt64s(request.ParticipantIDs...)
	organizationIDs := sortedUniqueInt64s(request.OrganizationIDs...)
	for kind, ids := range map[string][]int64{
		"person": personIDs, "participant": participantIDs, "organization": organizationIDs,
	} {
		if len(ids) > MaxEntityLabelIDs {
			return EntityLabels{}, fmt.Errorf("%w: %d %s IDs exceed %d",
				ErrEntityLabelRequestTooLarge, len(ids), kind, MaxEntityLabelIDs)
		}
	}
	labels := EntityLabels{}
	var err error
	if labels.Participants, err = s.participantLabels(ctx, participantIDs); err != nil {
		return EntityLabels{}, err
	}
	if labels.People, err = s.durablePersonLabels(ctx, personIDs); err != nil {
		return EntityLabels{}, err
	}
	if labels.Organizations, err = s.OrganizationNamesContext(ctx, organizationIDs); err != nil {
		return EntityLabels{}, err
	}
	for id, name := range labels.Organizations {
		if name == "" {
			delete(labels.Organizations, id)
		}
	}
	return labels, nil
}

func (s *Store) participantLabels(ctx context.Context, ids []int64) (map[int64]string, error) {
	return s.queryEntityLabels(ctx, "participant", ids,
		`SELECT p.id, `+sqlParticipantLabelExpr("p")+` FROM participants p WHERE p.id IN (`+placeholders(len(ids))+`)`)
}

func (s *Store) durablePersonLabels(ctx context.Context, ids []int64) (map[int64]string, error) {
	labels, err := s.queryEntityLabels(ctx, "person", ids,
		`SELECT p.id, `+sqlDurablePersonLabelExpr("p")+` FROM persons p WHERE p.id IN (`+placeholders(len(ids))+`)`)
	if err != nil {
		return nil, err
	}
	// Persons that no longer exist may have been absorbed by a merge. A
	// labelled person always exists, so only unlabelled IDs are candidates;
	// the existence check keeps a live but unnamed person unlabelled.
	missing, err := s.missingPersonIDs(ctx, ids, labels)
	if err != nil || len(missing) == 0 {
		return labels, err
	}
	absorbed, err := s.absorbedPersonLabels(ctx, missing)
	if err != nil {
		return nil, err
	}
	maps.Copy(labels, absorbed)
	return labels, nil
}

func (s *Store) missingPersonIDs(ctx context.Context, ids []int64, labels map[int64]string) ([]int64, error) {
	candidates := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := labels[id]; !ok {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	existing := make(map[int64]bool, len(candidates))
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM persons WHERE id IN (`+placeholders(len(candidates))+`)`, int64Args(candidates)...)
	if err != nil {
		return nil, fmt.Errorf("check person label candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan person label candidate: %w", err)
		}
		existing[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("check person label candidates: %w", err)
	}
	missing := make([]int64, 0, len(candidates))
	for _, id := range candidates {
		if !existing[id] {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// absorbedPersonLabels names deleted people from the most recent merge that
// absorbed each of them. A snapshot that fails to decode leaves its person
// unlabelled rather than failing the whole lookup.
func (s *Store) absorbedPersonLabels(ctx context.Context, ids []int64) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.absorbed_person_id, m.snapshot_blob, m.snapshot_sha256
		FROM person_merges m
		WHERE m.absorbed_person_id IN (`+placeholders(len(ids))+`)
		  AND NOT EXISTS (
		      SELECT 1 FROM person_merges newer
		      WHERE newer.absorbed_person_id = m.absorbed_person_id AND newer.id > m.id)`,
		int64Args(ids)...)
	if err != nil {
		return nil, fmt.Errorf("load absorbed person snapshots: %w", err)
	}
	labels := make(map[int64]string, len(ids))
	fallbackParticipants := make(map[int64][]int64)
	for rows.Next() {
		var id int64
		var blob []byte
		var hash string
		if err := rows.Scan(&id, &blob, &hash); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan absorbed person snapshot: %w", err)
		}
		snapshot, err := decodePersonMergeSnapshot(blob, hash)
		if err != nil {
			slog.Warn("skipping undecodable person merge snapshot for label", "person_id", id, "error", err)
			continue
		}
		for _, person := range snapshot.Persons {
			if person.ID != id {
				continue
			}
			if person.DisplayName != nil {
				if name := strings.TrimSpace(*person.DisplayName); name != "" {
					labels[id] = name
					break
				}
			}
			fallbackParticipants[id] = sortedUniqueInt64s(person.ParticipantIDs...)
			break
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("load absorbed person snapshots: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close absorbed person snapshots: %w", err)
	}
	if len(fallbackParticipants) == 0 {
		return labels, nil
	}
	for id, participantIDs := range fallbackParticipants {
		if label, ok, err := s.participantsBestLabel(ctx, participantIDs); err != nil {
			return nil, err
		} else if ok {
			labels[id] = label
		}
	}
	return labels, nil
}

// participantsBestLabel applies the durable-person member rule to a fixed
// participant set: the smallest-ID named participant, else the smallest-ID
// participant with an identifier label.
func (s *Store) participantsBestLabel(ctx context.Context, ids []int64) (string, bool, error) {
	if len(ids) == 0 {
		return "", false, nil
	}
	if len(ids) > MaxEntityLabelIDs {
		ids = ids[:MaxEntityLabelIDs]
	}
	var named, identified string
	var namedID, identifiedID int64
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.id, COALESCE(NULLIF(TRIM(p.display_name), ''), ''), COALESCE(`+sqlParticipantIdentifierLabelExpr("p")+`, '')
		 FROM participants p WHERE p.id IN (`+placeholders(len(ids))+`)`, int64Args(ids)...)
	if err != nil {
		return "", false, fmt.Errorf("label absorbed person participants: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var name, identifier string
		if err := rows.Scan(&id, &name, &identifier); err != nil {
			return "", false, fmt.Errorf("scan absorbed person participant label: %w", err)
		}
		if name != "" && (namedID == 0 || id < namedID) {
			named, namedID = name, id
		}
		if identifier != "" && (identifiedID == 0 || id < identifiedID) {
			identified, identifiedID = identifier, id
		}
	}
	if err := rows.Err(); err != nil {
		return "", false, fmt.Errorf("label absorbed person participants: %w", err)
	}
	if named != "" {
		return named, true, nil
	}
	return identified, identified != "", nil
}

func (s *Store) queryEntityLabels(ctx context.Context, kind string, ids []int64, query string) (map[int64]string, error) {
	labels := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return labels, nil
	}
	rows, err := s.db.QueryContext(ctx, query, int64Args(ids)...)
	if err != nil {
		return nil, fmt.Errorf("list %s labels: %w", kind, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var label *string
		if err := rows.Scan(&id, &label); err != nil {
			return nil, fmt.Errorf("scan %s label: %w", kind, err)
		}
		if label != nil && *label != "" {
			labels[id] = *label
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list %s labels: %w", kind, err)
	}
	return labels, nil
}

func int64Args(ids []int64) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}
