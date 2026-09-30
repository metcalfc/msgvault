package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

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

// labelRoleSQL matches a label by its Gmail system ID or, for sources that
// only carry names, by its upper-cased name.
func labelRoleSQL(alias string, roles ...string) string {
	in := ""
	for i, role := range roles {
		if i > 0 {
			in += ", "
		}
		in += "'" + role + "'"
	}
	return "(" + alias + ".source_label_id IN (" + in + ") OR UPPER(" + alias + ".name) IN (" + in + "))"
}

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
			           WHERE ml.message_id = m.id AND `+labelRoleSQL("l", "STARRED")+`)
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

// MessageRemoteImagesBlockedContext reports whether one message carries a
// spam or trash label, which blocks fetching its remote images: a sender
// of junk mail must never learn the message was opened. A missing message
// is reported as sql.ErrNoRows.
func (s *Store) MessageRemoteImagesBlockedContext(ctx context.Context, messageID int64) (bool, error) {
	var blocked int
	err := s.db.QueryRowContext(ctx, `
		SELECT CASE WHEN EXISTS (
		    SELECT 1 FROM message_labels ml JOIN labels l ON l.id = ml.label_id
		    WHERE ml.message_id = m.id AND `+labelRoleSQL("l", "SPAM", "TRASH", "JUNK")+`)
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
