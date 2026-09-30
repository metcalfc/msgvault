package store

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/correspondentkind"
)

// Cleanup suggestions are per-message judgments that help the owner find
// junk and phishing worth deleting. They are inputs to review only: nothing
// here stages, deletes, or hides a message.

// ErrCleanupSuggestionInvalid reports a suggestion that cannot be stored.
var ErrCleanupSuggestionInvalid = errors.New("invalid cleanup suggestion")

// CleanupCandidateQuery pages the cleanup pool newest first.
type CleanupCandidateQuery struct {
	// BeforeID continues a previous page: only messages with a smaller ID.
	// Zero starts from the newest message.
	BeforeID int64
	Limit    int
	// Rejudge includes messages that already have a suggestion.
	Rejudge bool
}

// CleanupCandidate is one message in the cleanup pool.
type CleanupCandidate struct {
	MessageID int64
	// SenderID is zero when the message has no resolvable sender.
	SenderID int64
}

// The pool: live email the owner did not send, labeled spam or Promotions,
// in a conversation the owner never wrote in. Sender kind and links are
// checked per message by the caller.
const cleanupPoolSQL = `
	SELECT m.id, ` + messageSenderSQL + `
	FROM messages m
	WHERE m.deleted_at IS NULL AND m.deleted_from_source_at IS NULL
	  AND COALESCE(m.message_type, '') IN ('', 'email')
	  AND COALESCE(m.is_from_me, FALSE) = FALSE AND m.identity_is_from_me = FALSE
	  AND EXISTS (
	      SELECT 1 FROM message_labels ml JOIN labels l ON l.id = ml.label_id
	      WHERE ml.message_id = m.id
	        AND (` + "l.source_label_id = 'CATEGORY_PROMOTIONS' OR " + `
	             (l.source_label_id IN ('SPAM', 'JUNK') OR UPPER(l.name) IN ('SPAM', 'JUNK'))))
	  AND NOT EXISTS (
	      SELECT 1 FROM messages r
	      WHERE r.conversation_id = m.conversation_id
	        AND (COALESCE(r.is_from_me, FALSE) = TRUE OR r.identity_is_from_me = TRUE))`

// CleanupCandidatesContext returns one page of the cleanup pool, newest
// message first. No body is read.
func (s *Store) CleanupCandidatesContext(ctx context.Context, query CleanupCandidateQuery) ([]CleanupCandidate, error) {
	if query.Limit <= 0 {
		return nil, fmt.Errorf("%w: limit must be positive", ErrCleanupSuggestionInvalid)
	}
	sqlText := cleanupPoolSQL
	args := []any{}
	if query.BeforeID > 0 {
		sqlText += " AND m.id < ?"
		args = append(args, query.BeforeID)
	}
	if !query.Rejudge {
		sqlText += " AND NOT EXISTS (SELECT 1 FROM cleanup_suggestions cs WHERE cs.message_id = m.id)"
	}
	sqlText += " ORDER BY m.id DESC LIMIT ?"
	args = append(args, query.Limit)
	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("list cleanup candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	candidates := []CleanupCandidate{}
	for rows.Next() {
		var candidate CleanupCandidate
		var sender sql.NullInt64
		if err := rows.Scan(&candidate.MessageID, &sender); err != nil {
			return nil, fmt.Errorf("scan cleanup candidate: %w", err)
		}
		candidate.SenderID = sender.Int64
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// SenderKindsContext names the effective correspondent kind of each
// requested sender's identity cluster: a person classification, another
// kind, or "unclassified" when nothing classified the cluster.
func (s *Store) SenderKindsContext(ctx context.Context, senderIDs []int64) (map[int64]string, error) {
	result := map[int64]string{}
	ids := sortedUniqueInt64s(senderIDs...)
	if len(ids) == 0 {
		return result, nil
	}
	notPeople, err := s.NotPersonParticipantsForContext(ctx, ids)
	if err != nil {
		return nil, err
	}
	people, err := s.PersonClassifiedParticipantsContext(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		switch kind, notPerson := notPeople[id]; {
		case notPerson:
			result[id] = string(kind)
		case isPerson(people, id):
			result[id] = string(correspondentkind.Person)
		default:
			result[id] = "unclassified"
		}
	}
	return result, nil
}

func isPerson(people map[int64]PersonClassification, id int64) bool {
	_, ok := people[id]
	return ok
}

// cleanupBodyPrefixBytes bounds how much of each stored body is read for
// the opening text and link hosts.
const cleanupBodyPrefixBytes = 256 << 10

// CleanupEvidence is what one message offers the cleanup judgment. The
// header block is the bounded, decoded prefix of the stored raw message;
// the bodies are bounded prefixes read by primary key.
type CleanupEvidence struct {
	MessageID int64
	SenderID  int64
	FromName  string
	FromEmail string
	Subject   string
	Labels    []string
	// AddressedToOwner is true when an owner address is a To or Cc
	// recipient; otherwise the owner got the message by Bcc or through an
	// undisclosed-recipients list.
	AddressedToOwner bool
	ThreadReplied    bool
	// HeaderBlock is nil when no raw message is stored or its header block
	// could not be decoded; header-derived evidence is then unknown.
	HeaderBlock []byte
	BodyText    string
	BodyHTML    string
}

// CleanupEvidenceContext gathers one message's evidence. Every read is by
// the message's primary key; the raw message contributes only its header
// block, decoded from a bounded stored prefix.
func (s *Store) CleanupEvidenceContext(ctx context.Context, messageID int64) (CleanupEvidence, error) {
	evidence := CleanupEvidence{MessageID: messageID, Labels: []string{}}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		var sender sql.NullInt64
		var subject, fromName, fromEmail sql.NullString
		var addressed, replied int
		if err := tx.QueryRowContext(ctx, `
			SELECT m.subject, `+messageSenderSQL+`,
			       CASE WHEN EXISTS (
			           SELECT 1 FROM message_recipients mr
			           JOIN participants p ON p.id = mr.participant_id
			           JOIN account_identities ai ON LOWER(ai.address) = LOWER(p.email_address)
			           WHERE mr.message_id = m.id AND mr.recipient_type IN ('to', 'cc'))
			            THEN 1 ELSE 0 END,
			       CASE WHEN EXISTS (
			           SELECT 1 FROM messages r
			           WHERE r.conversation_id = m.conversation_id AND r.id <> m.id
			             AND (COALESCE(r.is_from_me, FALSE) = TRUE OR r.identity_is_from_me = TRUE))
			            THEN 1 ELSE 0 END
			FROM messages m WHERE m.id = ?`, messageID).Scan(&subject, &sender, &addressed, &replied); err != nil {
			return fmt.Errorf("load cleanup message: %w", err)
		}
		evidence.Subject = subject.String
		evidence.SenderID = sender.Int64
		evidence.AddressedToOwner = addressed == 1
		evidence.ThreadReplied = replied == 1
		if sender.Valid {
			err := tx.QueryRowContext(ctx, `
				SELECT COALESCE(
				           (SELECT mr.display_name FROM message_recipients mr
				            WHERE mr.message_id = ? AND mr.participant_id = p.id AND mr.recipient_type = 'from'
				              AND mr.display_name IS NOT NULL AND mr.display_name <> ''
				            ORDER BY mr.id LIMIT 1),
				           p.display_name),
				       p.email_address
				FROM participants p WHERE p.id = ?`, messageID, sender.Int64).Scan(&fromName, &fromEmail)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("load cleanup sender: %w", err)
			}
			evidence.FromName, evidence.FromEmail = fromName.String, fromEmail.String
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT l.name FROM message_labels ml JOIN labels l ON l.id = ml.label_id
			WHERE ml.message_id = ? ORDER BY l.name`, messageID)
		if err != nil {
			return fmt.Errorf("load cleanup labels: %w", err)
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan cleanup label: %w", err)
			}
			evidence.Labels = append(evidence.Labels, name)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		var text, html sql.NullString
		err = tx.QueryRowContext(ctx, `
			SELECT substr(body_text, 1, ?), substr(body_html, 1, ?)
			FROM message_bodies WHERE message_id = ?`,
			cleanupBodyPrefixBytes, cleanupBodyPrefixBytes, messageID).Scan(&text, &html)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("load cleanup body: %w", err)
		}
		evidence.BodyText, evidence.BodyHTML = text.String, html.String
		return nil
	})
	if err != nil {
		return evidence, err
	}
	header, err := s.messageRawHeaderContext(ctx, messageID)
	switch {
	case err == nil:
		evidence.HeaderBlock = header
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, ErrInvalidMessageRaw):
		// Fail closed: without a decodable header block, header evidence
		// is unknown rather than guessed.
	default:
		return evidence, fmt.Errorf("read cleanup header block: %w", err)
	}
	return evidence, nil
}

// CleanupSuggestion is one stored judgment.
type CleanupSuggestion struct {
	MessageID             int64
	Score                 float64
	Impersonation         float64
	Pressure              float64
	Category              string
	CategoryProbabilities map[string]float64
	// KeepProbability is the probability the message is personal or work
	// mail.
	KeepProbability float64
	// Signals names the hard signals that moved the score.
	Signals []string
	Model   string
}

func validProbability(value float64) bool { return value >= 0 && value <= 1 }

func (c CleanupSuggestion) validate() error {
	switch {
	case c.MessageID <= 0:
		return fmt.Errorf("%w: message id must be positive", ErrCleanupSuggestionInvalid)
	case !validProbability(c.Score) || !validProbability(c.Impersonation) ||
		!validProbability(c.Pressure) || !validProbability(c.KeepProbability):
		return fmt.Errorf("%w: probability out of range", ErrCleanupSuggestionInvalid)
	case strings.TrimSpace(c.Category) == "" || strings.TrimSpace(c.Model) == "":
		return fmt.Errorf("%w: category and model are required", ErrCleanupSuggestionInvalid)
	}
	return nil
}

// WriteCleanupSuggestionsContext stores suggestions, replacing an earlier
// judgment of the same message. A message deleted since it was read is
// skipped.
func (s *Store) WriteCleanupSuggestionsContext(ctx context.Context, suggestions []CleanupSuggestion) (int, error) {
	for _, suggestion := range suggestions {
		if err := suggestion.validate(); err != nil {
			return 0, err
		}
	}
	written := 0
	err := s.withTxContext(ctx, func(tx *loggedTx) error {
		for _, suggestion := range suggestions {
			probabilities, err := json.Marshal(suggestion.CategoryProbabilities, json.Deterministic(true))
			if err != nil {
				return fmt.Errorf("encode cleanup category probabilities: %w", err)
			}
			signals := suggestion.Signals
			if signals == nil {
				signals = []string{}
			}
			encodedSignals, err := json.Marshal(signals)
			if err != nil {
				return fmt.Errorf("encode cleanup signals: %w", err)
			}
			result, err := tx.ExecContext(ctx, `
				INSERT INTO cleanup_suggestions (
				    message_id, score, impersonation, pressure, category,
				    category_probabilities_json, keep_probability, signals_json, model)
				SELECT m.id, ?, ?, ?, ?, ?, ?, ?, ? FROM messages m WHERE m.id = ?
				ON CONFLICT (message_id) DO UPDATE SET
				    score = excluded.score, impersonation = excluded.impersonation,
				    pressure = excluded.pressure, category = excluded.category,
				    category_probabilities_json = excluded.category_probabilities_json,
				    keep_probability = excluded.keep_probability,
				    signals_json = excluded.signals_json, model = excluded.model,
				    judged_at = CURRENT_TIMESTAMP`,
				suggestion.Score, suggestion.Impersonation, suggestion.Pressure, suggestion.Category,
				string(probabilities), suggestion.KeepProbability, string(encodedSignals), suggestion.Model,
				suggestion.MessageID)
			if err != nil {
				return fmt.Errorf("write cleanup suggestion: %w", err)
			}
			if affected, err := result.RowsAffected(); err == nil && affected > 0 {
				written++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return written, nil
}

// CleanupSuggestionRow is a stored suggestion with the message's display
// fields.
type CleanupSuggestionRow struct {
	CleanupSuggestion

	SourceMessageID string
	Subject         string
	FromName        string
	FromEmail       string
	SentAt          *time.Time
	JudgedAt        time.Time
}

// CleanupSuggestionFilter narrows ListCleanupSuggestionsContext.
type CleanupSuggestionFilter struct {
	MinScore float64
	Limit    int
}

const cleanupSuggestionColumns = `
	cs.message_id, cs.score, cs.impersonation, cs.pressure, cs.category,
	cs.category_probabilities_json, cs.keep_probability, cs.signals_json, cs.model, cs.judged_at,
	m.source_message_id, m.subject, m.sent_at, p.display_name, p.email_address`

func scanCleanupSuggestionRow(rows *loggedRows) (CleanupSuggestionRow, error) {
	var row CleanupSuggestionRow
	var probabilities, signals string
	var sourceMessageID, subject, name, email sql.NullString
	var judgedAt, sentAt nullableTimestamp
	if err := rows.Scan(&row.MessageID, &row.Score, &row.Impersonation, &row.Pressure, &row.Category,
		&probabilities, &row.KeepProbability, &signals, &row.Model, &judgedAt,
		&sourceMessageID, &subject, &sentAt, &name, &email); err != nil {
		return row, fmt.Errorf("scan cleanup suggestion: %w", err)
	}
	if err := json.Unmarshal([]byte(probabilities), &row.CategoryProbabilities); err != nil {
		return row, fmt.Errorf("decode cleanup category probabilities: %w", err)
	}
	if err := json.Unmarshal([]byte(signals), &row.Signals); err != nil {
		return row, fmt.Errorf("decode cleanup signals: %w", err)
	}
	row.JudgedAt = judgedAt.Time
	if sentAt.Valid {
		sent := sentAt.Time
		row.SentAt = &sent
	}
	row.SourceMessageID = sourceMessageID.String
	row.Subject, row.FromName, row.FromEmail = subject.String, name.String, email.String
	return row, nil
}

// ListCleanupSuggestionsContext lists stored suggestions at or above a
// score, highest first, for live messages only.
func (s *Store) ListCleanupSuggestionsContext(
	ctx context.Context, filter CleanupSuggestionFilter,
) ([]CleanupSuggestionRow, error) {
	if filter.Limit <= 0 {
		return nil, fmt.Errorf("%w: limit must be positive", ErrCleanupSuggestionInvalid)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+cleanupSuggestionColumns+`
		FROM cleanup_suggestions cs
		JOIN messages m ON m.id = cs.message_id
		LEFT JOIN participants p ON p.id = `+messageSenderSQL+`
		WHERE cs.score >= ? AND m.deleted_at IS NULL AND m.deleted_from_source_at IS NULL
		ORDER BY cs.score DESC, cs.message_id DESC LIMIT ?`, filter.MinScore, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("list cleanup suggestions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := []CleanupSuggestionRow{}
	for rows.Next() {
		row, err := scanCleanupSuggestionRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// KeepCandidatesForSourceMessagesContext returns the suggestions, among the
// given provider message IDs of one source, whose keep probability reached
// minKeep, highest first. Deletion review uses it to name staged messages
// that look like personal or work mail.
func (s *Store) KeepCandidatesForSourceMessagesContext(
	ctx context.Context, sourceID int64, sourceMessageIDs []string, minKeep float64,
) ([]CleanupSuggestionRow, error) {
	ids := slices.Clone(sourceMessageIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	result := []CleanupSuggestionRow{}
	if sourceID <= 0 || len(ids) == 0 {
		return result, nil
	}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		return queryInChunksContext(ctx, tx, ids, []any{sourceID, minKeep}, `
			SELECT `+cleanupSuggestionColumns+`
			FROM messages m
			JOIN cleanup_suggestions cs ON cs.message_id = m.id
			LEFT JOIN participants p ON p.id = `+messageSenderSQL+`
			WHERE m.source_id = ? AND cs.keep_probability >= ? AND m.source_message_id IN (%s)`,
			func(rows *loggedRows) error {
				row, err := scanCleanupSuggestionRow(rows)
				if err != nil {
					return err
				}
				result = append(result, row)
				return nil
			})
	})
	if err != nil {
		if s.dialect.IsNoSuchTableError(err) {
			return []CleanupSuggestionRow{}, nil
		}
		return nil, fmt.Errorf("list keep candidates: %w", err)
	}
	slices.SortFunc(result, func(a, b CleanupSuggestionRow) int {
		switch {
		case a.KeepProbability > b.KeepProbability:
			return -1
		case a.KeepProbability < b.KeepProbability:
			return 1
		default:
			return cmp.Compare(a.MessageID, b.MessageID)
		}
	})
	return result, nil
}
