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

// Derived correspondent kinds are the rule and Jev classifications `msgvault
// kinds build` writes. They never outrank a user decision (see
// correspondentkind.Source.Precedence), never touch identity match
// candidates or organizations, and are never written for a cluster that
// contains one of the archive owner's identities or already carries a user
// decision.

const correspondentKindRevisionKey = "correspondent_kind_revision"

// ErrDerivedCorrespondentKindInvalid reports a derived classification with
// an unknown source or a kind that source may not write.
var ErrDerivedCorrespondentKindInvalid = errors.New("invalid derived correspondent kind")

// CorrespondentKindRevision tracks every change to correspondent kinds so the
// analytical cache can tell when the exported kinds are stale.
func (s *Store) CorrespondentKindRevision() (int64, error) {
	return s.CorrespondentKindRevisionContext(context.Background())
}

// CorrespondentKindRevisionContext is the request-aware form of
// CorrespondentKindRevision.
func (s *Store) CorrespondentKindRevisionContext(ctx context.Context) (int64, error) {
	return readArchiveMetadataRevisionContext(ctx, s.db, correspondentKindRevisionKey, "correspondent kind")
}

func (s *Store) bumpCorrespondentKindRevisionTx(ctx context.Context, tx *loggedTx) error {
	if _, err := tx.ExecContext(ctx, s.dialect.InsertOrIgnore(
		`INSERT OR IGNORE INTO archive_metadata (key, value) VALUES (?, '0')`),
		correspondentKindRevisionKey); err != nil {
		return fmt.Errorf("seed correspondent kind revision: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE archive_metadata SET value = CAST(CAST(value AS INTEGER) + 1 AS TEXT)
		 WHERE key = ?`, correspondentKindRevisionKey); err != nil {
		return fmt.Errorf("bump correspondent kind revision: %w", err)
	}
	return nil
}

func decodeKindProbabilities(value sql.NullString) map[string]float64 {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	var probabilities map[string]float64
	if err := json.Unmarshal([]byte(value.String), &probabilities); err != nil || len(probabilities) == 0 {
		return nil
	}
	return probabilities
}

// DerivedCorrespondentKind is one rule or Jev classification of the cluster
// containing ParticipantID. Every current cluster member receives a row.
type DerivedCorrespondentKind struct {
	ParticipantID int64
	Source        correspondentkind.Source
	Kind          correspondentkind.Kind
	// Confidence and Probabilities are recorded for Jev judgments.
	Confidence    *float64
	Probabilities map[string]float64
	// Actor names the rule (rule:<reason>) or model (jev:<model>).
	Actor string
}

func (d DerivedCorrespondentKind) validate() error {
	switch {
	case d.ParticipantID <= 0:
		return fmt.Errorf("%w: participant id must be positive", ErrDerivedCorrespondentKindInvalid)
	case d.Source != correspondentkind.SourceRule && d.Source != correspondentkind.SourceJev:
		return fmt.Errorf("%w: source %q", ErrDerivedCorrespondentKindInvalid, d.Source)
	case !d.Kind.Known() || d.Kind == correspondentkind.Organization:
		// An organization needs an Organization record, which only a user
		// decision creates.
		return fmt.Errorf("%w: kind %q", ErrDerivedCorrespondentKindInvalid, d.Kind)
	case d.Kind == correspondentkind.Unclear && d.Source != correspondentkind.SourceJev:
		return fmt.Errorf("%w: only jev writes unclear", ErrDerivedCorrespondentKindInvalid)
	case d.Confidence != nil && (*d.Confidence < 0 || *d.Confidence > 1):
		return fmt.Errorf("%w: confidence out of range", ErrDerivedCorrespondentKindInvalid)
	}
	return nil
}

// clusterIndex maps participants to their cluster root and members, built
// once per transaction from the link graph.
type clusterIndex struct {
	roots   map[int64]int64
	members map[int64][]int64
}

func newClusterIndex(edges []linkEdge) clusterIndex {
	roots := clustersFromEdges(edges)
	members := map[int64][]int64{}
	for id, root := range roots {
		members[root] = append(members[root], id)
	}
	for root := range members {
		slices.Sort(members[root])
	}
	return clusterIndex{roots: roots, members: members}
}

func (c clusterIndex) rootOf(id int64) int64 {
	if root, ok := c.roots[id]; ok {
		return root
	}
	return id
}

func (c clusterIndex) membersOf(id int64) []int64 {
	if members, ok := c.members[c.rootOf(id)]; ok {
		return members
	}
	return []int64{id}
}

// WriteDerivedCorrespondentKindsContext records rule or Jev classifications.
// A classification of a cluster that contains an owner identity, or whose
// members carry a user decision, is skipped. It returns how many clusters
// were written.
func (s *Store) WriteDerivedCorrespondentKindsContext(
	ctx context.Context, kinds []DerivedCorrespondentKind,
) (int, error) {
	for _, kind := range kinds {
		if err := kind.validate(); err != nil {
			return 0, err
		}
	}
	if len(kinds) == 0 {
		return 0, nil
	}
	written := 0
	err := retryBusyWriteErr(ctx, s, "write derived correspondent kinds", func() error {
		return s.withTxContext(ctx, func(tx *loggedTx) error {
			written = 0
			if err := s.lockIdentityMutationTxContext(ctx, tx); err != nil {
				return err
			}
			edges, err := s.loadLinkEdgesTxContext(ctx, tx)
			if err != nil {
				return err
			}
			index := newClusterIndex(edges)
			owners, err := ownerParticipantIDsTx(ctx, tx)
			if err != nil {
				return err
			}
			userDecided, err := participantsWithSourceTx(ctx, tx, correspondentkind.SourceUser)
			if err != nil {
				return err
			}
			revision, err := readIdentityRevisionContext(ctx, tx)
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			done := map[int64]struct{}{}
			for _, kind := range kinds {
				if err := requireParticipantTx(ctx, tx, kind.ParticipantID); err != nil {
					return err
				}
				members := index.membersOf(kind.ParticipantID)
				root := members[0]
				if _, repeated := done[root]; repeated {
					continue
				}
				if slices.ContainsFunc(members, func(id int64) bool {
					_, owner := owners[id]
					_, decided := userDecided[id]
					return owner || decided
				}) {
					continue
				}
				var probabilities any
				if len(kind.Probabilities) > 0 {
					encoded, err := json.Marshal(kind.Probabilities, json.Deterministic(true))
					if err != nil {
						return fmt.Errorf("encode correspondent kind probabilities: %w", err)
					}
					probabilities = string(encoded)
				}
				for _, member := range members {
					if _, err := tx.ExecContext(ctx, `
						INSERT INTO correspondent_kinds (
							participant_id, source, kind, organization_id, confidence,
							probabilities_json, identity_revision, actor, classified_at
						) VALUES (?, ?, ?, NULL, ?, ?, ?, ?, ?)
						ON CONFLICT (participant_id, source) DO UPDATE SET
							kind = excluded.kind, organization_id = NULL,
							confidence = excluded.confidence,
							probabilities_json = excluded.probabilities_json,
							identity_revision = excluded.identity_revision,
							actor = excluded.actor, classified_at = excluded.classified_at`,
						member, kind.Source, kind.Kind, kind.Confidence, probabilities,
						revision, nullIfBlank(kind.Actor), now,
					); err != nil {
						return fmt.Errorf("write derived correspondent kind: %w", err)
					}
				}
				done[root] = struct{}{}
				written++
			}
			if written == 0 {
				return nil
			}
			return s.bumpCorrespondentKindRevisionTx(ctx, tx)
		})
	})
	return written, err
}

func nullIfBlank(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

// participantsWithSourceTx returns every participant carrying a row from
// one of the sources.
func participantsWithSourceTx(
	ctx context.Context, tx *loggedTx, sources ...correspondentkind.Source,
) (map[int64]struct{}, error) {
	result := map[int64]struct{}{}
	if len(sources) == 0 {
		return result, nil
	}
	args := make([]any, len(sources))
	for i, source := range sources {
		args[i] = string(source)
	}
	rows, err := tx.QueryContext(ctx, `SELECT participant_id FROM correspondent_kinds
		WHERE source IN (`+placeholders(len(sources))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("load classified participants: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan classified participant: %w", err)
		}
		result[id] = struct{}{}
	}
	return result, rows.Err()
}

// CorrespondentKindCandidateQuery selects clusters for `kinds build`.
type CorrespondentKindCandidateQuery struct {
	// MinMessages is the activity floor: messages the cluster sent plus
	// messages the owner sent to it.
	MinMessages int64
	// Limit caps the result; zero means no cap.
	Limit int
	// SkipSources leaves out clusters with any row from these sources, so a
	// rerun only visits clusters that source has not classified. Clusters
	// with a user decision are always left out.
	SkipSources []correspondentkind.Source
}

// CorrespondentKindCandidate is one cluster eligible for classification.
type CorrespondentKindCandidate struct {
	CanonicalID int64
	MemberIDs   []int64
	DisplayName string
	Emails      []string
	Phones      []string
	// Sent counts messages the cluster sent; Received counts messages the
	// owner sent to it; Meetings counts meetings and events it attended.
	Sent        int64
	Received    int64
	Meetings    int64
	ProviderBot bool
}

// botIdentifierTypes are participant identifier types a chat provider
// assigns only to bots, webhooks, and system accounts.
var botIdentifierTypes = []string{"discord_webhook_id", "discord_automated_id"}

// CorrespondentKindCandidatesContext lists identity clusters with enough
// activity that have not been classified, most active first. Clusters
// containing one of the owner's identities, clusters with a user decision,
// and clusters with a row from any skipped source are left out.
func (s *Store) CorrespondentKindCandidatesContext(
	ctx context.Context, query CorrespondentKindCandidateQuery,
) ([]CorrespondentKindCandidate, error) {
	var candidates []CorrespondentKindCandidate
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		sent, err := participantCountsTx(ctx, tx, `
			SELECT m.sender_id, COUNT(*) FROM messages m
			WHERE m.sender_id IS NOT NULL AND `+LiveMessagesWhere("m", false)+`
			  AND COALESCE(m.is_from_me, FALSE) = FALSE
			GROUP BY m.sender_id`)
		if err != nil {
			return err
		}
		received, err := participantCountsTx(ctx, tx, `
			SELECT per_message.participant_id, COUNT(*) FROM (
				SELECT mr.participant_id, mr.message_id FROM message_recipients mr
				JOIN messages m ON m.id = mr.message_id
				WHERE mr.recipient_type IN ('to', 'cc', 'bcc') AND `+LiveMessagesWhere("m", false)+`
				  AND COALESCE(m.is_from_me, FALSE) = TRUE
				GROUP BY mr.participant_id, mr.message_id
			) per_message
			GROUP BY per_message.participant_id`)
		if err != nil {
			return err
		}
		meetings, err := participantCountsTx(ctx, tx, `
			SELECT per_message.participant_id, COUNT(*) FROM (
				SELECT mr.participant_id, mr.message_id FROM message_recipients mr
				JOIN messages m ON m.id = mr.message_id
				WHERE m.message_type IN ('meeting_transcript', 'calendar_event')
				  AND `+LiveMessagesWhere("m", false)+`
				GROUP BY mr.participant_id, mr.message_id
			) per_message
			GROUP BY per_message.participant_id`)
		if err != nil {
			return err
		}
		edges, err := s.loadLinkEdgesTxContext(ctx, tx)
		if err != nil {
			return err
		}
		index := newClusterIndex(edges)
		owners, err := ownerParticipantIDsTx(ctx, tx)
		if err != nil {
			return err
		}
		excluded, err := participantsWithSourceTx(ctx, tx,
			append([]correspondentkind.Source{correspondentkind.SourceUser}, query.SkipSources...)...)
		if err != nil {
			return err
		}
		byRoot := map[int64]*CorrespondentKindCandidate{}
		add := func(counts map[int64]int64, field func(*CorrespondentKindCandidate) *int64) {
			for id, count := range counts {
				root := index.rootOf(id)
				candidate, ok := byRoot[root]
				if !ok {
					candidate = &CorrespondentKindCandidate{CanonicalID: root, MemberIDs: index.membersOf(id)}
					byRoot[root] = candidate
				}
				*field(candidate) += count
			}
		}
		add(sent, func(c *CorrespondentKindCandidate) *int64 { return &c.Sent })
		add(received, func(c *CorrespondentKindCandidate) *int64 { return &c.Received })
		add(meetings, func(c *CorrespondentKindCandidate) *int64 { return &c.Meetings })
		for root, candidate := range byRoot {
			if candidate.Sent+candidate.Received < max(query.MinMessages, 1) ||
				slices.ContainsFunc(candidate.MemberIDs, func(id int64) bool {
					_, owner := owners[id]
					_, skip := excluded[id]
					return owner || skip
				}) {
				delete(byRoot, root)
			}
		}
		selected := make([]*CorrespondentKindCandidate, 0, len(byRoot))
		for _, candidate := range byRoot {
			selected = append(selected, candidate)
		}
		slices.SortFunc(selected, func(a, b *CorrespondentKindCandidate) int {
			if c := cmp.Compare(b.Sent+b.Received, a.Sent+a.Received); c != 0 {
				return c
			}
			return cmp.Compare(a.CanonicalID, b.CanonicalID)
		})
		if query.Limit > 0 && len(selected) > query.Limit {
			selected = selected[:query.Limit]
		}
		candidates = make([]CorrespondentKindCandidate, 0, len(selected))
		for _, candidate := range selected {
			if err := describeCandidateTx(ctx, tx, candidate); err != nil {
				return err
			}
			candidates = append(candidates, *candidate)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

func participantCountsTx(ctx context.Context, tx *loggedTx, query string) (map[int64]int64, error) {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("count participant activity: %w", err)
	}
	defer func() { _ = rows.Close() }()
	counts := map[int64]int64{}
	for rows.Next() {
		var id, count int64
		if err := rows.Scan(&id, &count); err != nil {
			return nil, fmt.Errorf("scan participant activity: %w", err)
		}
		counts[id] += count
	}
	return counts, rows.Err()
}

func describeCandidateTx(ctx context.Context, tx *loggedTx, candidate *CorrespondentKindCandidate) error {
	name, err := clusterBestDisplayNameTx(ctx, tx, candidate.MemberIDs)
	if err != nil {
		return err
	}
	if name != nil {
		candidate.DisplayName = *name
	}
	addresses, err := clusterAddressesTx(ctx, tx, candidate.MemberIDs)
	if err != nil {
		return err
	}
	candidate.Emails, candidate.Phones = []string{}, []string{}
	for _, address := range addresses {
		if strings.Contains(address, "@") {
			candidate.Emails = append(candidate.Emails, address)
		} else {
			candidate.Phones = append(candidate.Phones, address)
		}
	}
	args := make([]any, 0, len(botIdentifierTypes))
	for _, identifierType := range botIdentifierTypes {
		args = append(args, identifierType)
	}
	return queryInChunksContext(ctx, tx, candidate.MemberIDs, args, `
		SELECT 1 FROM participant_identifiers
		WHERE identifier_type IN (`+placeholders(len(botIdentifierTypes))+`)
		  AND participant_id IN (%s) LIMIT 1`, func(*loggedRows) error {
		candidate.ProviderBot = true
		return nil
	})
}

// CorrespondentKindEvidenceOptions bounds the evidence read for one cluster.
type CorrespondentKindEvidenceOptions struct {
	// HeaderSample is how many of the cluster's newest email messages have
	// their raw header block read. Zero reads none.
	HeaderSample int
	// SubjectsFromThem and SubjectsFromOwner cap the subjects returned.
	SubjectsFromThem  int
	SubjectsFromOwner int
}

// CorrespondentKindEvidence is message metadata about one cluster. It never
// includes message bodies.
type CorrespondentKindEvidence struct {
	ListIDMessages    int64
	ListIDs           []string
	Categories        map[string]int64
	Headers           correspondentkind.HeaderCounts
	SubjectsFromThem  []string
	SubjectsFromOwner []string
}

// maxEvidenceMembers bounds the IN lists evidence queries use. A cluster
// larger than this is judged from its first members.
const maxEvidenceMembers = 400

// maxListIDs caps the distinct List-Id values kept per cluster.
const maxListIDs = 20

// CorrespondentKindEvidenceContext gathers list, label, header, and subject
// evidence about the messages a cluster sent and the messages the owner sent
// to it. Header evidence reads only the header block of a few sampled raw
// messages, by primary key.
func (s *Store) CorrespondentKindEvidenceContext(
	ctx context.Context, members []int64, options CorrespondentKindEvidenceOptions,
) (CorrespondentKindEvidence, error) {
	evidence := CorrespondentKindEvidence{
		ListIDs: []string{}, Categories: map[string]int64{},
		SubjectsFromThem: []string{}, SubjectsFromOwner: []string{},
	}
	if len(members) == 0 {
		return evidence, nil
	}
	if len(members) > maxEvidenceMembers {
		members = members[:maxEvidenceMembers]
	}
	args := make([]any, len(members))
	for i, id := range members {
		args[i] = id
	}
	in := placeholders(len(members))
	live := LiveMessagesWhere("m", false)
	sampled := []int64{}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT m.list_id, COUNT(*) FROM messages m
			WHERE m.sender_id IN (`+in+`) AND `+live+`
			  AND m.list_id IS NOT NULL AND m.list_id <> ''
			GROUP BY m.list_id ORDER BY COUNT(*) DESC, m.list_id`, args...)
		if err != nil {
			return fmt.Errorf("count cluster list ids: %w", err)
		}
		for rows.Next() {
			var listID string
			var count int64
			if err := rows.Scan(&listID, &count); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan cluster list id: %w", err)
			}
			evidence.ListIDMessages += count
			if len(evidence.ListIDs) < maxListIDs {
				evidence.ListIDs = append(evidence.ListIDs, listID)
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, `
			SELECT l.source_label_id, COUNT(*) FROM messages m
			JOIN message_labels ml ON ml.message_id = m.id
			JOIN labels l ON l.id = ml.label_id
			WHERE m.sender_id IN (`+in+`) AND `+live+`
			  AND l.source_label_id LIKE 'CATEGORY_%'
			GROUP BY l.source_label_id`, args...)
		if err != nil {
			return fmt.Errorf("count cluster categories: %w", err)
		}
		for rows.Next() {
			var label string
			var count int64
			if err := rows.Scan(&label, &count); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan cluster category: %w", err)
			}
			evidence.Categories[label] += count
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if options.HeaderSample > 0 {
			rows, err = tx.QueryContext(ctx, `
				SELECT m.id FROM messages m
				WHERE m.sender_id IN (`+in+`) AND `+live+` AND m.message_type = 'email'
				ORDER BY m.sent_at DESC, m.id DESC LIMIT ?`,
				append(slices.Clone(args), options.HeaderSample)...)
			if err != nil {
				return fmt.Errorf("sample cluster messages: %w", err)
			}
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					_ = rows.Close()
					return fmt.Errorf("scan sampled message: %w", err)
				}
				sampled = append(sampled, id)
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		if options.SubjectsFromThem > 0 {
			evidence.SubjectsFromThem, err = distinctSubjectsTx(ctx, tx, `
				SELECT m.subject FROM messages m
				WHERE m.sender_id IN (`+in+`) AND `+live+`
				  AND m.subject IS NOT NULL AND m.subject <> ''
				ORDER BY m.sent_at DESC, m.id DESC LIMIT ?`,
				append(slices.Clone(args), options.SubjectsFromThem*4), options.SubjectsFromThem)
			if err != nil {
				return err
			}
		}
		if options.SubjectsFromOwner > 0 {
			evidence.SubjectsFromOwner, err = distinctSubjectsTx(ctx, tx, `
				SELECT m.subject FROM messages m
				WHERE COALESCE(m.is_from_me, FALSE) = TRUE AND `+live+`
				  AND m.subject IS NOT NULL AND m.subject <> ''
				  AND EXISTS (
				      SELECT 1 FROM message_recipients mr
				      WHERE mr.message_id = m.id AND mr.participant_id IN (`+in+`))
				ORDER BY m.sent_at DESC, m.id DESC LIMIT ?`,
				append(slices.Clone(args), options.SubjectsFromOwner*4), options.SubjectsFromOwner)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return evidence, err
	}
	for _, id := range sampled {
		raw, err := s.GetMessageRawContext(ctx, id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			continue
		case errors.Is(err, ErrInvalidMessageRaw):
			evidence.Headers.Sampled++
			continue
		case err != nil:
			return evidence, fmt.Errorf("read sampled message headers: %w", err)
		}
		evidence.Headers.AddHeaders(raw)
	}
	return evidence, nil
}

// maxSubjectRunes bounds each subject kept as evidence.
const maxSubjectRunes = 160

func distinctSubjectsTx(ctx context.Context, tx *loggedTx, query string, args []any, limit int) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load cluster subjects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	subjects := []string{}
	seen := map[string]struct{}{}
	for rows.Next() && len(subjects) < limit {
		var subject string
		if err := rows.Scan(&subject); err != nil {
			return nil, fmt.Errorf("scan cluster subject: %w", err)
		}
		subject = strings.Join(strings.Fields(subject), " ")
		if runes := []rune(subject); len(runes) > maxSubjectRunes {
			subject = string(runes[:maxSubjectRunes])
		}
		key := strings.ToLower(subject)
		if _, ok := seen[key]; ok || subject == "" {
			continue
		}
		seen[key] = struct{}{}
		subjects = append(subjects, subject)
	}
	return subjects, rows.Err()
}

// RankingHiddenParticipantsContext maps every participant in a cluster that
// relationship rankings leave out by default (see
// correspondentkind.Kind.LeavesRankings) to its effective kind.
func (s *Store) RankingHiddenParticipantsContext(
	ctx context.Context,
) (map[int64]correspondentkind.Kind, error) {
	return s.participantsByEffectiveKindContext(ctx, func(kind correspondentkind.Kind) bool {
		return kind.LeavesRankings()
	})
}

// ParticipantsWithCorrespondentKindContext returns every participant whose
// cluster's effective kind is kind, sorted. Person is not accepted: an
// unclassified cluster is a person too.
func (s *Store) ParticipantsWithCorrespondentKindContext(
	ctx context.Context, kind correspondentkind.Kind,
) ([]int64, error) {
	if !kind.Known() || kind == correspondentkind.Person {
		return nil, fmt.Errorf("%w: cannot select kind %q", ErrCorrespondentKindInvalid, kind)
	}
	matched, err := s.participantsByEffectiveKindContext(ctx, func(effective correspondentkind.Kind) bool {
		return effective == kind
	})
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(matched))
	for id := range matched {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}

func (s *Store) participantsByEffectiveKindContext(
	ctx context.Context, keep func(correspondentkind.Kind) bool,
) (map[int64]correspondentkind.Kind, error) {
	result := map[int64]correspondentkind.Kind{}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		clusters, err := s.correspondentKindClustersTx(ctx, tx)
		if err != nil {
			return err
		}
		for _, cluster := range clusters {
			if !keep(cluster.effective.kind) {
				continue
			}
			for _, member := range cluster.members {
				result[member] = cluster.effective.kind
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// CorrespondentKindExportRow is the effective classification of one
// participant's cluster, for the analytical cache.
type CorrespondentKindExportRow struct {
	ParticipantID int64
	Kind          correspondentkind.Kind
	Source        correspondentkind.Source
	// IndividualPerson is the Jev probability that the cluster is an
	// individual person, when the effective classification is a Jev one.
	IndividualPerson *float64
}

// JevIndividualPersonOption is the Jev option whose probability gates
// relationship rankings.
const JevIndividualPersonOption = "individual_person"

// CorrespondentKindExportRowsContext returns one row per member of every
// classified cluster, sorted by participant.
func (s *Store) CorrespondentKindExportRowsContext(ctx context.Context) ([]CorrespondentKindExportRow, error) {
	rows := []CorrespondentKindExportRow{}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		clusters, err := s.correspondentKindClustersTx(ctx, tx)
		if err != nil {
			return err
		}
		for _, cluster := range clusters {
			effective := cluster.effective
			var individual *float64
			if value, ok := effective.probabilities[JevIndividualPersonOption]; ok && effective.source == correspondentkind.SourceJev {
				individual = &value
			}
			kind := effective.kind
			if kind == "" {
				kind = correspondentkind.Person
			}
			for _, member := range cluster.members {
				rows = append(rows, CorrespondentKindExportRow{
					ParticipantID: member, Kind: kind, Source: effective.source, IndividualPerson: individual,
				})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(rows, func(a, b CorrespondentKindExportRow) int { return cmp.Compare(a.ParticipantID, b.ParticipantID) })
	return rows, nil
}
