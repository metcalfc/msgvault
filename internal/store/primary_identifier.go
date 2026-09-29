package store

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// Primary identifier kinds, in selection order.
const (
	PrimaryIdentifierEmail  = "email"
	PrimaryIdentifierPhone  = "phone"
	PrimaryIdentifierHandle = "handle"
)

// PrimaryIdentifier is the one identifier a list row shows beneath a name.
// Selection prefers an email address, then a phone number, then any other
// handle (a username, IM address, or service-specific identifier).
type PrimaryIdentifier struct {
	Kind  string `json:"kind" enum:"email,phone,handle" doc:"Identifier class: email, phone, or handle (any other username or service identifier)"`
	Value string `json:"value" doc:"The identifier as stored, suitable for display"`
}

// PrimaryIdentifierCandidate is one identifier offered to
// SelectPrimaryIdentifier. RawKind is the stored kind or identifier type
// ("email", "phone", "username", "imessage", ...). Rank orders candidates of
// the same class; lower ranks win, and the value breaks a remaining tie.
//
// Archive marks a stored participant identifier row. Apart from email
// addresses and phone numbers, those rows hold service-internal keys (a
// Slack member ID, a Beeper room key, an SMS-bridge thread key) that the
// people views never show as text, so they never become a handle.
type PrimaryIdentifierCandidate struct {
	RawKind string
	Value   string
	Rank    []int64
	Archive bool
}

// PrimaryIdentifierClass maps a stored identifier kind to its display class
// and class order. Names and non-address kinds (URLs, calendars, social
// profiles, languages) are not identifiers a row should show.
func PrimaryIdentifierClass(rawKind string) (string, int, bool) {
	switch strings.ToLower(strings.TrimSpace(rawKind)) {
	case "email":
		return PrimaryIdentifierEmail, 0, true
	case "phone":
		return PrimaryIdentifierPhone, 1, true
	case "", "name", "url", "social", "calendar", "contact_uri", "org_directory", "language",
		"provider_identity",
		// Opaque chat-service keys, whatever row they arrive on.
		"slack", "beeper", "synctech_sms", "discord_webhook_id":
		return "", 0, false
	default:
		return PrimaryIdentifierHandle, 2, true
	}
}

// SelectPrimaryIdentifier deterministically picks the best candidate: the
// lowest class (email, then phone, then handle), then the lowest Rank tuple,
// then the lexically smallest value. It returns nil when no candidate
// qualifies.
func SelectPrimaryIdentifier(candidates []PrimaryIdentifierCandidate) *PrimaryIdentifier {
	type classified struct {
		kind  string
		class int
		value string
		rank  []int64
	}
	var best *classified
	for _, candidate := range candidates {
		value := strings.TrimSpace(candidate.Value)
		kind, class, ok := PrimaryIdentifierClass(candidate.RawKind)
		if !ok || value == "" || (candidate.Archive && kind == PrimaryIdentifierHandle) {
			continue
		}
		current := classified{kind: kind, class: class, value: value, rank: candidate.Rank}
		if best == nil || cmp.Or(
			cmp.Compare(current.class, best.class),
			slices.Compare(current.rank, best.rank),
			strings.Compare(current.value, best.value),
		) < 0 {
			best = &current
		}
	}
	if best == nil {
		return nil
	}
	return &PrimaryIdentifier{Kind: best.kind, Value: best.value}
}

// hydrateDirectoryPrimaryIdentifiersTx loads every identifier candidate for
// one Directory page in a single query. Within a class, the person's current
// curated contact points come first in vCard preference order (pref, then
// ordinal); then addresses observed on bound participants, lowest participant
// ID first, the participant's own email address or phone number before its
// stored identifier rows (primary rows first).
func (s *Store) hydrateDirectoryPrimaryIdentifiersTx(
	ctx context.Context, tx *loggedTx, placeholders string, ids []any,
	byID map[int64]int, candidates []directoryPersonCandidate,
) error {
	query := `SELECT person_id, kind, value, archive, source_rank, rank_a, rank_b, rank_c FROM (
		SELECT point.person_id AS person_id, point.address_kind AS kind, point.original_value AS value,
			0 AS archive, 0 AS source_rank, COALESCE(point.pref, 101) AS rank_a, point.ordinal AS rank_b, point.id AS rank_c
		FROM person_contact_points point
		WHERE point.person_id IN (` + placeholders + `)
			AND point.active_until IS NULL AND point.superseded_at IS NULL
		UNION ALL
		SELECT binding.person_id, 'email', participant.email_address,
			0, 1, binding.participant_id, 0, 0
		FROM person_participants binding
		JOIN participants participant ON participant.id = binding.participant_id
		WHERE binding.person_id IN (` + placeholders + `)
			AND TRIM(COALESCE(participant.email_address, '')) <> ''
		UNION ALL
		SELECT binding.person_id, 'phone', participant.phone_number,
			0, 1, binding.participant_id, 0, 1
		FROM person_participants binding
		JOIN participants participant ON participant.id = binding.participant_id
		WHERE binding.person_id IN (` + placeholders + `)
			AND TRIM(COALESCE(participant.phone_number, '')) <> ''
		UNION ALL
		SELECT binding.person_id, identifier.identifier_type, identifier.identifier_value,
			1, 1, binding.participant_id, CASE WHEN identifier.is_primary THEN 1 ELSE 2 END, identifier.id
		FROM person_participants binding
		JOIN participant_identifiers identifier ON identifier.participant_id = binding.participant_id
		WHERE binding.person_id IN (` + placeholders + `)
			AND TRIM(identifier.identifier_value) <> ''
	) candidates`
	args := make([]any, 0, len(ids)*4)
	for range 4 {
		args = append(args, ids...)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("load directory primary identifiers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	offered := make(map[int64][]PrimaryIdentifierCandidate, len(ids))
	for rows.Next() {
		var personID, sourceRank, rankA, rankB, rankC int64
		var kind string
		var value sql.NullString
		var archive bool
		if err := rows.Scan(&personID, &kind, &value, &archive, &sourceRank, &rankA, &rankB, &rankC); err != nil {
			return fmt.Errorf("scan directory primary identifier: %w", err)
		}
		offered[personID] = append(offered[personID], PrimaryIdentifierCandidate{
			RawKind: kind, Value: value.String, Rank: []int64{sourceRank, rankA, rankB, rankC}, Archive: archive,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate directory primary identifiers: %w", err)
	}
	for personID, personCandidates := range offered {
		index, ok := byID[personID]
		if !ok {
			continue
		}
		candidates[index].summary.PrimaryIdentifier = SelectPrimaryIdentifier(personCandidates)
	}
	return nil
}
