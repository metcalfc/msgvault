package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"go.kenn.io/msgvault/internal/correspondentkind"
)

// Deletion protection names the messages a deletion should think twice
// about: starred messages, messages the owner sent, and messages from a
// sender classified as a person. It is plain archive metadata; nothing here
// asks Jev anything.

// Protection reasons, as reported to staging callers.
const (
	ProtectionStarred      = "starred"
	ProtectionOwnerSent    = "owner_sent"
	ProtectionPersonSender = "person_sender"
)

// PersonSenderThreshold is the Jev individual_person probability at which a
// Jev-classified sender counts as a person, the same bar relationship
// rankings use.
const PersonSenderThreshold = 0.60

// DeletionProtection is why one message is protected.
type DeletionProtection struct {
	MessageID    int64
	Starred      bool
	OwnerSent    bool
	PersonSender bool
	// PersonProbability is the stored Jev individual_person probability of
	// the sender's cluster, when a Jev judgment made it a person.
	PersonProbability *float64
}

// Protected reports whether any reason applies.
func (p DeletionProtection) Protected() bool {
	return p.Starred || p.OwnerSent || p.PersonSender
}

// Reasons lists the reasons that apply, in a stable order.
func (p DeletionProtection) Reasons() []string {
	reasons := []string{}
	if p.Starred {
		reasons = append(reasons, ProtectionStarred)
	}
	if p.OwnerSent {
		reasons = append(reasons, ProtectionOwnerSent)
	}
	if p.PersonSender {
		reasons = append(reasons, ProtectionPersonSender)
	}
	return reasons
}

// PersonClassification is the explicit person classification of one
// participant's identity cluster.
type PersonClassification struct {
	Source correspondentkind.Source
	// IndividualPerson is the Jev probability, for a Jev classification.
	IndividualPerson *float64
}

// messageSenderSQL resolves a message's sender participant: the sender
// column, else the lowest 'from' recipient.
const messageSenderSQL = `COALESCE(m.sender_id, (
	SELECT MIN(mr.participant_id) FROM message_recipients mr
	WHERE mr.message_id = m.id AND mr.recipient_type = 'from'))`

// DeletionProtectionsContext returns the protection of each requested
// message that has one. Messages with no reason are absent. Every lookup is
// by message primary key; no body is read.
func (s *Store) DeletionProtectionsContext(
	ctx context.Context, messageIDs []int64,
) (map[int64]DeletionProtection, error) {
	result := map[int64]DeletionProtection{}
	ids := sortedUniqueInt64s(messageIDs...)
	if len(ids) == 0 {
		return result, nil
	}
	senders := map[int64]int64{}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		if err := queryInChunksContext(ctx, tx, ids, nil, `
			SELECT m.id,
			       CASE WHEN COALESCE(m.is_from_me, FALSE) = TRUE OR m.identity_is_from_me = TRUE
			            THEN 1 ELSE 0 END,
			       CASE WHEN EXISTS (
			           SELECT 1 FROM message_labels ml JOIN labels l ON l.id = ml.label_id
			           WHERE ml.message_id = m.id AND `+starredLabelSQL("l")+`)
			            THEN 1 ELSE 0 END,
			       `+messageSenderSQL+`
			FROM messages m WHERE m.id IN (%s)`, func(rows *loggedRows) error {
			var id int64
			var ownerSent, starred int
			var sender sql.NullInt64
			if err := rows.Scan(&id, &ownerSent, &starred, &sender); err != nil {
				return fmt.Errorf("scan deletion protection: %w", err)
			}
			protection := DeletionProtection{MessageID: id, OwnerSent: ownerSent == 1, Starred: starred == 1}
			if protection.Protected() {
				result[id] = protection
			}
			if sender.Valid {
				senders[id] = sender.Int64
			}
			return nil
		}); err != nil {
			return fmt.Errorf("load deletion protections: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	senderIDs := make([]int64, 0, len(senders))
	for _, sender := range senders {
		senderIDs = append(senderIDs, sender)
	}
	people, err := s.PersonClassifiedParticipantsContext(ctx, senderIDs)
	if err != nil {
		return nil, err
	}
	for messageID, sender := range senders {
		person, ok := people[sender]
		if !ok {
			continue
		}
		protection := result[messageID]
		protection.MessageID = messageID
		protection.PersonSender = true
		protection.PersonProbability = person.IndividualPerson
		result[messageID] = protection
	}
	return result, nil
}

// PersonClassifiedParticipantsContext maps each requested participant whose
// identity cluster is explicitly classified as a person to that
// classification: a user decision of person, or a Jev judgment whose
// individual_person probability reached PersonSenderThreshold. A cluster
// no one classified is absent; so is one whose effective kind is anything
// else, since a user decision outranks Jev. The archive owner's own
// clusters count as people only through an explicit row.
func (s *Store) PersonClassifiedParticipantsContext(
	ctx context.Context, participantIDs []int64,
) (map[int64]PersonClassification, error) {
	result := map[int64]PersonClassification{}
	ids := sortedUniqueInt64s(participantIDs...)
	if len(ids) == 0 {
		return result, nil
	}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		clusters, err := scopedCorrespondentKindClustersTx(ctx, tx, ids, false)
		if err != nil {
			if s.dialect.IsNoSuchTableError(err) {
				return nil
			}
			return err
		}
		for _, cluster := range clusters {
			effective := cluster.effective
			var probability *float64
			if value, ok := effective.probabilities[JevIndividualPersonOption]; ok && effective.source == correspondentkind.SourceJev {
				probability = &value
			}
			person := false
			switch effective.source {
			case correspondentkind.SourceUser:
				person = effective.kind == correspondentkind.Person
			case correspondentkind.SourceJev:
				person = effective.kind == correspondentkind.Person ||
					(probability != nil && *probability >= PersonSenderThreshold)
			case correspondentkind.SourceRule:
				// Rules only ever decide that a cluster is not a person.
			}
			if !person {
				continue
			}
			for _, member := range cluster.members {
				result[member] = PersonClassification{Source: effective.source, IndividualPerson: probability}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for id := range result {
		if _, requested := slices.BinarySearch(ids, id); !requested {
			delete(result, id)
		}
	}
	return result, nil
}

// DeletionProtectionsForSourceMessagesContext is DeletionProtectionsContext
// keyed by the provider message IDs of one source, the form a deletion
// manifest holds. Provider IDs with no live protected message are absent.
func (s *Store) DeletionProtectionsForSourceMessagesContext(
	ctx context.Context, sourceID int64, sourceMessageIDs []string,
) (map[string]DeletionProtection, error) {
	result := map[string]DeletionProtection{}
	ids := slices.Clone(sourceMessageIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if sourceID <= 0 || len(ids) == 0 {
		return result, nil
	}
	byMessage := map[int64]string{}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		return queryInChunksContext(ctx, tx, ids, []any{sourceID}, `
			SELECT m.id, m.source_message_id FROM messages m
			WHERE m.source_id = ? AND m.source_message_id IN (%s)`, func(rows *loggedRows) error {
			var id int64
			var providerID string
			if err := rows.Scan(&id, &providerID); err != nil {
				return fmt.Errorf("scan deletion target: %w", err)
			}
			byMessage[id] = providerID
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("resolve deletion targets: %w", err)
	}
	messageIDs := make([]int64, 0, len(byMessage))
	for id := range byMessage {
		messageIDs = append(messageIDs, id)
	}
	protections, err := s.DeletionProtectionsContext(ctx, messageIDs)
	if err != nil {
		return nil, err
	}
	for id, protection := range protections {
		result[byMessage[id]] = protection
	}
	return result, nil
}

// RemoteImagesBlockedMessagesContext reports, for each requested message
// that exists, whether its remote images are blocked (see
// MessageRemoteImagesBlockedContext).
func (s *Store) RemoteImagesBlockedMessagesContext(ctx context.Context, messageIDs []int64) (map[int64]bool, error) {
	result := map[int64]bool{}
	ids := sortedUniqueInt64s(messageIDs...)
	if len(ids) == 0 {
		return result, nil
	}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		return queryInChunksContext(ctx, tx, ids, nil, `
			SELECT m.id, CASE WHEN EXISTS (
			    SELECT 1 FROM message_labels ml JOIN labels l ON l.id = ml.label_id
			    WHERE ml.message_id = m.id AND `+junkOrTrashLabelSQL()+`)
			  THEN 1 ELSE 0 END
			FROM messages m WHERE m.id IN (%s)`, func(rows *loggedRows) error {
			var id int64
			var blocked int
			if err := rows.Scan(&id, &blocked); err != nil {
				return fmt.Errorf("scan remote image policy: %w", err)
			}
			result[id] = blocked == 1
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("check remote image policy: %w", err)
	}
	return result, nil
}

// RemoteImageState is what the image proxy checks on every request: whether
// the message's folder blocks remote images, and the version of its row,
// which the database bumps whenever the message or its body changes.
type RemoteImageState struct {
	// Blocked is true for spam, junk, trash, and deleted-items messages.
	Blocked bool
	// Version changes when the message or its body changes, so a cached
	// reference set keyed on it is never served for an edited body.
	Version string
}

// remoteImageBodyPrefixBytes matches the remote image archiver's HTML cap.
const remoteImageBodyPrefixBytes = 8 << 20

// RemoteImageStateContext reads one message's remote image state by primary
// key; no body is read. A missing message is reported as sql.ErrNoRows.
func (s *Store) RemoteImageStateContext(ctx context.Context, messageID int64) (RemoteImageState, error) {
	var state RemoteImageState
	var blocked int
	var modified nullableTimestamp
	err := s.db.QueryRowContext(ctx, `
		SELECT CASE WHEN EXISTS (
		    SELECT 1 FROM message_labels ml JOIN labels l ON l.id = ml.label_id
		    WHERE ml.message_id = m.id AND `+junkOrTrashLabelSQL()+`)
		  THEN 1 ELSE 0 END, m.last_modified
		FROM messages m WHERE m.id = ?`, messageID).Scan(&blocked, &modified)
	if errors.Is(err, sql.ErrNoRows) {
		return state, err
	}
	if err != nil {
		return state, fmt.Errorf("check remote image policy: %w", err)
	}
	state.Blocked = blocked == 1
	if modified.Valid {
		state.Version = modified.Time.UTC().Format(time.RFC3339Nano)
	}
	return state, nil
}

// RemoteImageBodiesContext loads bounded prefixes of one message's stored
// bodies by primary key: the only places an image the reader may request
// can come from.
func (s *Store) RemoteImageBodiesContext(ctx context.Context, messageID int64) (text, html string, err error) {
	var bodyText, bodyHTML sql.NullString
	err = s.db.QueryRowContext(ctx, `
		SELECT substr(body_text, 1, ?), substr(body_html, 1, ?) FROM message_bodies WHERE message_id = ?`,
		remoteImageBodyPrefixBytes, remoteImageBodyPrefixBytes, messageID).Scan(&bodyText, &bodyHTML)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("load remote image references: %w", err)
	}
	return bodyText.String, bodyHTML.String, nil
}

// MessageRemoteImagesBlockedContext reports whether one message is in a
// spam, junk, trash, or deleted-items folder, which blocks fetching its
// remote images: a sender of junk mail must never learn the message was
// opened. Folders are matched by provider role first, then by a short list
// of common names. A missing message is reported as sql.ErrNoRows.
func (s *Store) MessageRemoteImagesBlockedContext(ctx context.Context, messageID int64) (bool, error) {
	var blocked int
	err := s.db.QueryRowContext(ctx, `
		SELECT CASE WHEN EXISTS (
		    SELECT 1 FROM message_labels ml JOIN labels l ON l.id = ml.label_id
		    WHERE ml.message_id = m.id AND `+junkOrTrashLabelSQL()+`)
		  THEN 1 ELSE 0 END
		FROM messages m WHERE m.id = ?`, messageID).Scan(&blocked)
	if errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err != nil {
		return false, fmt.Errorf("check remote image policy: %w", err)
	}
	return blocked == 1, nil
}
