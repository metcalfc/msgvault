package store

import (
	"bytes"
	"cmp"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
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
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO archive_metadata (key, value) VALUES (?, '0')`,
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
	// ExpectedMembers, when set, is the cluster the evidence described. A
	// cluster whose members changed since is left unclassified.
	ExpectedMembers []int64
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
				if kind.ExpectedMembers != nil && !slices.Equal(members, kind.ExpectedMembers) {
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

// CorrespondentKindCandidatesCurrentContext reports, per candidate, whether
// it may still be classified and sent: its cluster has exactly the members
// its evidence described, none of them is one of the owner's identities,
// and none carries a user decision. Callers recheck right before anything
// leaves the machine, so an address confirmed as the owner's, or a link
// made, after the candidates were read is never sent or written.
func (s *Store) CorrespondentKindCandidatesCurrentContext(
	ctx context.Context, candidates []CorrespondentKindCandidate,
) (map[int64]bool, error) {
	current := make(map[int64]bool, len(candidates))
	if len(candidates) == 0 {
		return current, nil
	}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
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
		for _, candidate := range candidates {
			members := index.membersOf(candidate.CanonicalID)
			current[candidate.CanonicalID] = slices.Equal(members, candidate.MemberIDs) &&
				!slices.ContainsFunc(members, func(id int64) bool {
					_, owner := owners[id]
					_, decided := userDecided[id]
					return owner || decided
				})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return current, nil
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
	// RulesOnlySources returns clusters with a row from these sources (and
	// none from a skipped source or the user) marked RulesOnly, so a newer
	// deterministic rule can supersede an older judgment without asking
	// again. They bypass the evaluation gate and Limit, and follow the rest.
	RulesOnlySources []correspondentkind.Source
	// RevisitUnchanged keeps clusters an earlier run evaluated without a
	// decision even when nothing about them changed materially; a run that
	// can ask Jev sets it. Either way, never-evaluated clusters come first
	// and evaluated ones follow least recently evaluated first.
	RevisitUnchanged bool
}

// correspondentKindEvaluation is the recorded evaluation of one cluster.
type correspondentKindEvaluation struct {
	activity   int64
	membership string
	at         time.Time
}

// changedMaterially reports whether a cluster moved enough since its last
// evaluation to evaluate it again: its membership changed (any member set,
// not just its size), or its activity grew by half or by five messages,
// whichever is more.
func (e correspondentKindEvaluation) changedMaterially(activity int64, members []int64) bool {
	return membershipFingerprint(members) != e.membership || activity >= e.activity+max(5, e.activity/2)
}

// membershipFingerprint identifies a cluster's exact member set: a SHA-256
// of the sorted participant IDs.
func membershipFingerprint(members []int64) string {
	sorted := slices.Clone(members)
	slices.Sort(sorted)
	digest := sha256.New()
	for _, id := range sorted {
		_, _ = digest.Write([]byte(strconv.FormatInt(id, 10) + ","))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// clusterEvaluationsTx maps each cluster root to its latest evaluation.
// An archive opened before the table existed has none.
func (s *Store) clusterEvaluationsTx(
	ctx context.Context, tx *loggedTx, index clusterIndex,
) (map[int64]correspondentKindEvaluation, error) {
	rows, err := tx.QueryContext(ctx, `SELECT participant_id, activity, membership_fingerprint, evaluated_at
		FROM correspondent_kind_evaluations`)
	if err != nil {
		if s.dialect.IsNoSuchTableError(err) {
			return map[int64]correspondentKindEvaluation{}, nil
		}
		return nil, fmt.Errorf("load correspondent kind evaluations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := map[int64]correspondentKindEvaluation{}
	for rows.Next() {
		var id int64
		var evaluation correspondentKindEvaluation
		var at nullableTimestamp
		if err := rows.Scan(&id, &evaluation.activity, &evaluation.membership, &at); err != nil {
			return nil, fmt.Errorf("scan correspondent kind evaluation: %w", err)
		}
		evaluation.at = at.Time
		root := index.rootOf(id)
		if prior, ok := result[root]; !ok || evaluation.at.After(prior.at) {
			result[root] = evaluation
		}
	}
	return result, rows.Err()
}

// RecordCorrespondentKindEvaluationsContext records that these clusters
// were evaluated and left unclassified, so later runs visit other clusters
// first and revisit these only after a material change.
func (s *Store) RecordCorrespondentKindEvaluationsContext(
	ctx context.Context, candidates []CorrespondentKindCandidate,
) error {
	if len(candidates) == 0 {
		return nil
	}
	now := time.Now().UTC()
	return retryBusyWriteErr(ctx, s, "record correspondent kind evaluations", func() error {
		return s.withTxContext(ctx, func(tx *loggedTx) error {
			for _, candidate := range candidates {
				for _, member := range candidate.MemberIDs {
					if _, err := tx.ExecContext(ctx, `
						INSERT INTO correspondent_kind_evaluations
							(participant_id, activity, membership_fingerprint, evaluated_at)
						SELECT ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM participants WHERE id = ?)
						ON CONFLICT (participant_id) DO UPDATE SET
							activity = excluded.activity,
							membership_fingerprint = excluded.membership_fingerprint,
							evaluated_at = excluded.evaluated_at`,
						member, candidate.Sent+candidate.Received, membershipFingerprint(candidate.MemberIDs), now, member,
					); err != nil {
						return fmt.Errorf("record correspondent kind evaluation: %w", err)
					}
				}
			}
			return nil
		})
	})
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
	// RulesOnly marks a cluster an earlier source already judged: only the
	// deterministic rules may decide it again.
	RulesOnly bool
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
		rulesOnly, err := participantsWithSourceTx(ctx, tx, query.RulesOnlySources...)
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
		evaluations, err := s.clusterEvaluationsTx(ctx, tx, index)
		if err != nil {
			return err
		}
		for root, candidate := range byRoot {
			candidate.RulesOnly = slices.ContainsFunc(candidate.MemberIDs, func(id int64) bool {
				_, judged := rulesOnly[id]
				return judged
			})
			if evaluation, ok := evaluations[root]; ok && !candidate.RulesOnly && !query.RevisitUnchanged &&
				!evaluation.changedMaterially(candidate.Sent+candidate.Received, candidate.MemberIDs) {
				delete(byRoot, root)
				continue
			}
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
		var judged []*CorrespondentKindCandidate
		for _, candidate := range byRoot {
			if candidate.RulesOnly {
				judged = append(judged, candidate)
			} else {
				selected = append(selected, candidate)
			}
		}
		// Never-evaluated clusters first, most active first; then evaluated
		// ones, least recently evaluated first. A capped run therefore
		// reaches every cluster above the floor over successive runs.
		slices.SortFunc(selected, func(a, b *CorrespondentKindCandidate) int {
			evaluationA, evaluatedA := evaluations[a.CanonicalID]
			evaluationB, evaluatedB := evaluations[b.CanonicalID]
			if evaluatedA != evaluatedB {
				if evaluatedA {
					return 1
				}
				return -1
			}
			if evaluatedA && !evaluationA.at.Equal(evaluationB.at) {
				return evaluationA.at.Compare(evaluationB.at)
			}
			if c := cmp.Compare(b.Sent+b.Received, a.Sent+a.Received); c != 0 {
				return c
			}
			return cmp.Compare(a.CanonicalID, b.CanonicalID)
		})
		if query.Limit > 0 && len(selected) > query.Limit {
			selected = selected[:query.Limit]
		}
		slices.SortFunc(judged, func(a, b *CorrespondentKindCandidate) int {
			return cmp.Compare(a.CanonicalID, b.CanonicalID)
		})
		selected = append(selected, judged...)
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
		raw, err := s.messageRawHeaderContext(ctx, id)
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

// Header sampling reads at most rawHeaderCompressedBytes of a stored raw
// message and decodes at most rawHeaderBytes, stopping at the blank line
// that ends the header block. Attachments and bodies are never loaded.
const (
	rawHeaderCompressedBytes = 64 << 10
	rawHeaderBytes           = 64 << 10
	// rawHeaderDrainBytes bounds how much of a short stored message is
	// decoded after its header block, only to reach the stream's end and
	// verify its checksum.
	rawHeaderDrainBytes = 16 << 20
)

// messageRawHeaderContext returns the header block of one stored raw
// message, by primary key, without loading the whole blob: the database
// returns only a bounded prefix, and a zlib prefix is stream-decoded until
// the header block ends. A truncated stream is expected and not an error.
func (s *Store) messageRawHeaderContext(ctx context.Context, messageID int64) ([]byte, error) {
	var prefix []byte
	var compression sql.NullString
	if err := s.db.QueryRowContext(ctx, `
		SELECT substr(raw_data, 1, ?), compression FROM message_raw WHERE message_id = ?`,
		rawHeaderCompressedBytes, messageID).Scan(&prefix, &compression); err != nil {
		return nil, err
	}
	var source io.Reader = bytes.NewReader(prefix)
	if compression.Valid && compression.String == "zlib" {
		reader, err := zlib.NewReader(source)
		if err != nil {
			return nil, fmt.Errorf("%w: zlib reader: %w", ErrInvalidMessageRaw, err)
		}
		defer func() { _ = reader.Close() }()
		source = reader
	}
	// Decoded bytes count as header evidence only when the blank line that
	// ends the header block arrived within the cap. A corrupt stream, a
	// header block longer than the cap, or a prefix that ends first is a
	// failed sample with no signals.
	header := make([]byte, 0, 8<<10)
	chunk := make([]byte, 4<<10)
	//
	// A decoder error fails the sample even after the terminator was seen:
	// only the end of the stream, or an unexpected end caused by our own
	// prefix truncation, is acceptable. A stored message shorter than the
	// prefix is decoded to its end so its checksum is verified.
	truncated := len(prefix) >= rawHeaderCompressedBytes
	acceptable := func(err error) bool {
		return err == nil || errors.Is(err, io.EOF) || (truncated && errors.Is(err, io.ErrUnexpectedEOF))
	}
	for len(header) < rawHeaderBytes {
		n, err := source.Read(chunk)
		header = append(header, chunk[:n]...)
		if !acceptable(err) {
			return nil, fmt.Errorf("%w: decode header block: %w", ErrInvalidMessageRaw, err)
		}
		if end := headerBlockEnd(header); end >= 0 && end <= rawHeaderBytes {
			if err == nil {
				if _, drainErr := io.Copy(io.Discard, io.LimitReader(source, rawHeaderDrainBytes)); !acceptable(drainErr) {
					return nil, fmt.Errorf("%w: decode message: %w", ErrInvalidMessageRaw, drainErr)
				}
			}
			return header[:end], nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: header block not found: %w", ErrInvalidMessageRaw, err)
		}
	}
	return nil, fmt.Errorf("%w: header block exceeds %d bytes", ErrInvalidMessageRaw, rawHeaderBytes)
}

// headerBlockEnd returns the offset just past the blank line that ends a
// MIME header block, or -1.
func headerBlockEnd(data []byte) int {
	crlf := bytes.Index(data, []byte("\r\n\r\n"))
	lf := bytes.Index(data, []byte("\n\n"))
	switch {
	case crlf >= 0 && (lf < 0 || crlf <= lf):
		return crlf + 4
	case lf >= 0:
		return lf + 2
	default:
		return -1
	}
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
		// A SQLite archive opened before schema initialization created the
		// table has nothing classified.
		if s.dialect.IsNoSuchTableError(err) {
			return []CorrespondentKindExportRow{}, nil
		}
		return nil, err
	}
	slices.SortFunc(rows, func(a, b CorrespondentKindExportRow) int { return cmp.Compare(a.ParticipantID, b.ParticipantID) })
	return rows, nil
}
