package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"go.kenn.io/msgvault/internal/personfacts"
	"go.kenn.io/msgvault/internal/textutil"
)

// identifierParticipantLabel removes emoji from the name of a participant
// known only by a service identifier (a Slack or Discord user ID). Such a
// participant has no email or phone to fall back to, so a name made only of
// emoji is kept rather than leaving the participant labeled by its ID.
func identifierParticipantLabel(name string) string {
	if stripped := textutil.StripLabelEmoji(name); stripped != "" {
		return stripped
	}
	return name
}

// nonASCIIPredicate filters a text column to values with a character outside
// printable ASCII. Every emoji is outside that range, so the cleanup reads
// only rows that could change.
func (s *Store) nonASCIIPredicate(column string) string {
	if s.IsPostgreSQL() {
		return column + ` ~ '[^ -~]'`
	}
	return column + ` GLOB '*[^ -~]*'`
}

// stripStoredLabelEmoji applies textutil.StripLabelEmoji to the derived name
// and profile labels already in the archive, once, so labels imported before
// ingest removed emoji match ones imported after. Raw source values (vCard
// bodies, MIME, person_names.original_value) are left alone, and so is every
// value a user entered. Each stage advances the revisions a normal write of
// that label advances so caches and name resolvers refresh.
func (s *Store) stripStoredLabelEmoji(ctx context.Context) error {
	// Persons first: whether a person's display name was derived is decided
	// by comparing it with the participant and imported names it came from,
	// which later stages rewrite.
	if err := s.runMaintenance(ctx, s.stripPersonLabelEmojiTx); err != nil {
		return fmt.Errorf("strip emoji from person labels: %w", err)
	}
	if err := s.runMaintenance(ctx, s.stripParticipantLabelEmojiTx); err != nil {
		return fmt.Errorf("strip emoji from participant names: %w", err)
	}
	if err := s.runMaintenance(ctx, s.stripRecipientLabelEmojiTx); err != nil {
		return fmt.Errorf("strip emoji from recipient names: %w", err)
	}
	return nil
}

type labelRow struct {
	id    int64
	value string
}

func queryLabelRows(ctx context.Context, tx *loggedTx, query string, args ...any) ([]labelRow, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []labelRow
	for rows.Next() {
		var row labelRow
		if err := rows.Scan(&row.id, &row.value); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// stripPersonLabelEmojiTx cleans persons.display_name, non-user person_names,
// and non-user employment and label-attribute values.
//
// persons.display_name has no provenance column. It is cleaned only when it
// can be traced to a derived source: the promotion seed, a bound
// participant's observed name, or a current non-user person_names row. A name
// with no such source is treated as the user's own rename and kept. The
// cleanup is not a rename, so display_name_changed_at is kept.
func (s *Store) stripPersonLabelEmojiTx(ctx context.Context, tx *loggedTx) error {
	if err := s.lockIdentityMutationTxContext(ctx, tx); err != nil {
		return err
	}
	changedPersons := map[int64]struct{}{}
	displayChanged := false

	persons, err := queryLabelRows(ctx, tx, `SELECT id, display_name FROM persons
		WHERE display_name IS NOT NULL AND `+s.nonASCIIPredicate("display_name"))
	if err != nil {
		return fmt.Errorf("read person display names: %w", err)
	}
	for _, person := range persons {
		cleaned := textutil.StripLabelEmoji(person.value)
		if cleaned == "" || cleaned == person.value {
			continue
		}
		var derived bool
		if err := tx.QueryRowContext(ctx, `SELECT
			EXISTS (SELECT 1 FROM person_display_name_seeds
				WHERE person_id = ? AND seeded_name = ?)
			OR EXISTS (SELECT 1 FROM person_participants pp
				JOIN participants p ON p.id = pp.participant_id
				WHERE pp.person_id = ? AND TRIM(p.display_name) = ?)
			OR EXISTS (SELECT 1 FROM person_names
				WHERE person_id = ? AND source <> ? AND formatted = ?
				  AND active_until IS NULL AND superseded_at IS NULL)`,
			person.id, person.value, person.id, person.value,
			person.id, ProvenanceUser, person.value).Scan(&derived); err != nil {
			return fmt.Errorf("trace person %d display name: %w", person.id, err)
		}
		if !derived {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE persons SET display_name = ? WHERE id = ?`,
			cleaned, person.id); err != nil {
			return fmt.Errorf("clean person %d display name: %w", person.id, err)
		}
		// The seed still names the rule's choice, now without emoji, so the
		// display-name judgment may still replace it.
		if _, err := tx.ExecContext(ctx, `UPDATE person_display_name_seeds SET seeded_name = ?
			WHERE person_id = ? AND seeded_name = ?`, cleaned, person.id, person.value); err != nil {
			return fmt.Errorf("clean person %d display name seed: %w", person.id, err)
		}
		if err := s.bumpDisplayNameCounterpartVCardProjectionsTx(ctx, tx, person.id); err != nil {
			return err
		}
		changedPersons[person.id] = struct{}{}
		displayChanged = true
	}

	nameColumns := []string{"formatted", "given_name", "family_name", "additional_names"}
	for _, column := range nameColumns {
		rows, err := queryLabelRows(ctx, tx, `SELECT id, `+column+` FROM person_names
			WHERE source <> ? AND superseded_at IS NULL AND `+column+` IS NOT NULL
			  AND `+s.nonASCIIPredicate(column), ProvenanceUser)
		if err != nil {
			return fmt.Errorf("read person name %s: %w", column, err)
		}
		for _, row := range rows {
			cleaned := textutil.StripLabelEmoji(row.value)
			if cleaned == "" || cleaned == row.value {
				continue
			}
			var personID int64
			if err := tx.QueryRowContext(ctx, `UPDATE person_names SET `+column+` = ?, updated_at = `+
				s.dialect.Now()+` WHERE id = ? RETURNING person_id`, cleaned, row.id).Scan(&personID); err != nil {
				return fmt.Errorf("clean person name %d: %w", row.id, err)
			}
			changedPersons[personID] = struct{}{}
		}
	}

	if err := s.stripEmploymentLabelEmojiTx(ctx, tx, changedPersons); err != nil {
		return err
	}
	if err := s.stripAttributeLabelEmojiTx(ctx, tx, changedPersons); err != nil {
		return err
	}

	if len(changedPersons) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(changedPersons))
	for id := range changedPersons {
		ids = append(ids, id)
	}
	ids = sortedUniqueInt64s(ids...)
	if err := s.bumpPersonRevisionsTx(ctx, tx, ids...); err != nil {
		return err
	}
	if displayChanged {
		if err := s.bumpPersonDisplayNameRevisionContext(ctx, tx); err != nil {
			return err
		}
	}
	return s.invalidatePersonEnrichmentIdentitiesAfterRevisionTx(ctx, tx, ids...)
}

// stripEmploymentLabelEmojiTx cleans title, role, department, and location
// on employments a user did not enter. A cleaned title that would duplicate
// another current employment at the same organization is left as it was.
func (s *Store) stripEmploymentLabelEmojiTx(
	ctx context.Context, tx *loggedTx, changedPersons map[int64]struct{},
) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, person_id, organization_id, is_current,
			title, role, department, location
		FROM employments WHERE source <> ? AND (`+
		s.nonASCIIPredicate("COALESCE(title, '')")+` OR `+
		s.nonASCIIPredicate("COALESCE(role, '')")+` OR `+
		s.nonASCIIPredicate("COALESCE(department, '')")+` OR `+
		s.nonASCIIPredicate("COALESCE(location, '')")+`)`, ProvenanceUser)
	if err != nil {
		return fmt.Errorf("read employment labels: %w", err)
	}
	type employmentLabels struct {
		id, personID, organizationID int64
		current                      bool
		fields                       [4]sql.NullString
	}
	var employments []employmentLabels
	for rows.Next() {
		var row employmentLabels
		if err := rows.Scan(&row.id, &row.personID, &row.organizationID, &row.current,
			&row.fields[0], &row.fields[1], &row.fields[2], &row.fields[3]); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan employment labels: %w", err)
		}
		employments = append(employments, row)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close employment labels: %w", err)
	}
	for _, row := range employments {
		cleaned := row.fields
		changed := false
		for i, field := range row.fields {
			if !field.Valid {
				continue
			}
			value := strings.Join(strings.Fields(textutil.StripLabelEmoji(field.String)), " ")
			if value != field.String {
				cleaned[i] = sql.NullString{String: value, Valid: value != ""}
				changed = true
			}
		}
		if !changed {
			continue
		}
		var title *string
		if cleaned[0].Valid {
			title = &cleaned[0].String
		}
		titleNormalized := NormalizeEmploymentTitle(title)
		if row.current {
			var duplicate bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM employments
				WHERE person_id = ? AND organization_id = ? AND title_normalized = ?
				  AND id <> ? AND `+s.dialect.BoolTrueExpr("is_current")+`)`,
				row.personID, row.organizationID, titleNormalized, row.id).Scan(&duplicate); err != nil {
				return fmt.Errorf("check cleaned employment %d: %w", row.id, err)
			}
			if duplicate {
				continue
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE employments SET title = ?, title_normalized = ?,
			role = ?, department = ?, location = ?, revision = revision + 1, updated_at = `+
			s.dialect.Now()+` WHERE id = ?`,
			cleaned[0], titleNormalized, cleaned[1], cleaned[2], cleaned[3], row.id); err != nil {
			return fmt.Errorf("clean employment %d labels: %w", row.id, err)
		}
		changedPersons[row.personID] = struct{}{}
	}
	return nil
}

// stripAttributeLabelEmojiTx cleans current, non-user values of text
// attributes that hold a short label (personfacts.IsLabelTarget). Notes and
// other free text keep their emoji.
func (s *Store) stripAttributeLabelEmojiTx(
	ctx context.Context, tx *loggedTx, changedPersons map[int64]struct{},
) error {
	definitions, err := queryLabelRows(ctx, tx, `SELECT id, slug FROM attribute_definitions
		WHERE object_type = ? AND value_type = ?`, AttributeObjectPerson, AttributeValueText)
	if err != nil {
		return fmt.Errorf("read attribute definitions: %w", err)
	}
	for _, definition := range definitions {
		if !personfacts.IsLabelTarget(personfacts.TargetDescriptor{
			Kind: personfacts.TargetAttribute, ValueType: personfacts.ValueText, Slug: definition.value,
		}) {
			continue
		}
		rows, err := tx.QueryContext(ctx, `SELECT id, person_id, value_text FROM person_attribute_values
			WHERE definition_id = ? AND source <> ? AND value_text IS NOT NULL
			  AND active_until IS NULL AND superseded_at IS NULL AND `+
			s.nonASCIIPredicate("value_text"), definition.id, ProvenanceUser)
		if err != nil {
			return fmt.Errorf("read attribute %s values: %w", definition.value, err)
		}
		type attributeValue struct {
			id, personID int64
			value        string
		}
		var values []attributeValue
		for rows.Next() {
			var value attributeValue
			if err := rows.Scan(&value.id, &value.personID, &value.value); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan attribute value: %w", err)
			}
			values = append(values, value)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close attribute values: %w", err)
		}
		for _, value := range values {
			cleaned := textutil.StripLabelEmoji(value.value)
			if cleaned == "" || cleaned == value.value {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE person_attribute_values SET value_text = ? WHERE id = ?`,
				cleaned, value.id); err != nil {
				return fmt.Errorf("clean attribute value %d: %w", value.id, err)
			}
			changedPersons[value.personID] = struct{}{}
		}
	}
	return nil
}

// stripParticipantLabelEmojiTx cleans participants.display_name. Every
// participant name is an observed label: users rename people, not
// participants.
func (s *Store) stripParticipantLabelEmojiTx(ctx context.Context, tx *loggedTx) error {
	if err := s.lockIdentityMutationTxContext(ctx, tx); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, display_name,
			email_address IS NULL AND phone_number IS NULL
		FROM participants WHERE display_name IS NOT NULL AND `+s.nonASCIIPredicate("display_name"))
	if err != nil {
		return fmt.Errorf("read participant names: %w", err)
	}
	type participantName struct {
		id             int64
		name           string
		identifierOnly bool
	}
	var names []participantName
	for rows.Next() {
		var row participantName
		if err := rows.Scan(&row.id, &row.name, &row.identifierOnly); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan participant name: %w", err)
		}
		names = append(names, row)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close participant names: %w", err)
	}
	var changed []int64
	for _, row := range names {
		cleaned := textutil.StripLabelEmoji(row.name)
		if row.identifierOnly {
			cleaned = identifierParticipantLabel(row.name)
		}
		if cleaned == row.name {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE participants SET display_name = ? WHERE id = ?`,
			cleaned, row.id); err != nil {
			return fmt.Errorf("clean participant %d name: %w", row.id, err)
		}
		changed = append(changed, row.id)
	}
	if len(changed) == 0 {
		return nil
	}
	if err := s.bumpParticipantDisplayNameRevisionContext(ctx, tx); err != nil {
		return err
	}
	return s.invalidateParticipantPersonEnrichmentTx(ctx, tx, changed...)
}

// stripRecipientLabelEmojiTx cleans message_recipients.display_name, the
// name a message's header or roster gave each participant. Exported message
// rows cannot be rewritten incrementally, so a change forces a full
// analytics-cache rebuild.
func (s *Store) stripRecipientLabelEmojiTx(ctx context.Context, tx *loggedTx) error {
	rows, err := queryLabelRows(ctx, tx, `SELECT id, display_name FROM message_recipients
		WHERE display_name IS NOT NULL AND `+s.nonASCIIPredicate("display_name"))
	if err != nil {
		return fmt.Errorf("read recipient names: %w", err)
	}
	changed := false
	for _, row := range rows {
		cleaned := textutil.StripLabelEmoji(row.value)
		if cleaned == row.value {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE message_recipients SET display_name = ? WHERE id = ?`,
			cleaned, row.id); err != nil {
			return fmt.Errorf("clean recipient %d name: %w", row.id, err)
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return s.bumpDerivedDataRevision(tx)
}
