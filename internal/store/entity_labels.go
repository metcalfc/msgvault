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

// sqlParticipantLabelExpr renders one participant's label: the trimmed
// display name of the durable person it is bound to, then its own trimmed
// display name, then its identifier chain. NULL when it has none of them.
// The curated person name leads, as it does in the analytics labels
// (sqlPersonNameOverrideExpr and the identity index); a participant binds to
// at most one person, and the person ID order mirrors that override's pin.
func sqlParticipantLabelExpr(alias string) string {
	return `COALESCE(
		(SELECT NULLIF(TRIM(bound_person.display_name), '')
		 FROM person_participants bound_binding
		 JOIN persons bound_person ON bound_person.id = bound_binding.person_id
		 WHERE bound_binding.participant_id = ` + alias + `.id
		   AND NULLIF(TRIM(bound_person.display_name), '') IS NOT NULL
		 ORDER BY bound_binding.person_id, bound_binding.participant_id LIMIT 1),
		NULLIF(TRIM(` + alias + `.display_name), ''), ` + sqlParticipantIdentifierLabelExpr(alias) + `)`
}

// sqlPersonNameValueExpr renders one person_names row's (alias) name: its
// trimmed formatted value, else its given and family names. NULL when both
// are blank.
func sqlPersonNameValueExpr(alias string) string {
	return `COALESCE(NULLIF(TRIM(` + alias + `.formatted), ''),
		NULLIF(TRIM(TRIM(COALESCE(` + alias + `.given_name, '')) || ' ' || TRIM(COALESCE(` + alias + `.family_name, ''))), ''))`
}

// sqlDurablePersonLabelExpr renders the label of one durable persons row
// (alias): its trimmed display name; then its best current formatted or
// structured person name (formatted kind first, then vCard pref, ordinal,
// and ID, as ListPersonNamesContext orders them); then the display name of
// its smallest-ID named bound participant; then the identifier chain of its
// smallest-ID bound participant that has one. NULL when nothing names the
// person; it never falls back to the person ID or vCard UID.
func sqlDurablePersonLabelExpr(alias string) string {
	return `COALESCE(NULLIF(TRIM(` + alias + `.display_name), ''),
		(SELECT ` + sqlPersonNameValueExpr("dpl_name") + `
		 FROM person_names dpl_name
		 WHERE dpl_name.person_id = ` + alias + `.id
		   AND dpl_name.name_kind IN ('formatted', 'structured')
		   AND dpl_name.active_until IS NULL AND dpl_name.superseded_at IS NULL
		   AND ` + sqlPersonNameValueExpr("dpl_name") + ` IS NOT NULL
		 ORDER BY CASE WHEN dpl_name.name_kind = 'formatted' THEN 0 ELSE 1 END,
		          CASE WHEN dpl_name.pref IS NULL THEN 1 ELSE 0 END, dpl_name.pref,
		          dpl_name.ordinal, dpl_name.id
		 LIMIT 1),
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
