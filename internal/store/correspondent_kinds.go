package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/textimport"
)

// Correspondent kinds record whether a participant identity cluster is a
// person, an organization, a shared mailbox, or a record the user does not
// need. Rows are participant-local (see schema.sql): a classification writes
// one row per cluster member, and readers resolve a cluster's effective kind
// from all of its members, so a participant linked into a classified cluster
// later is covered without rewriting rows on link or unlink.

var (
	// ErrCorrespondentKindInvalid reports an unknown kind or a malformed
	// organization choice.
	ErrCorrespondentKindInvalid = errors.New("invalid correspondent kind")
	// ErrCorrespondentKindOwner reports an attempt to classify one of the
	// archive owner's own identities.
	ErrCorrespondentKindOwner = errors.New("the archive owner's own identities are always a person")
	// ErrCorrespondentKindOrganizationAmbiguous reports that an organization
	// name matched more than one organization; the caller must choose one by
	// ID.
	ErrCorrespondentKindOrganizationAmbiguous = errors.New(
		"organization name matches more than one organization; choose one by id")
)

// correspondentKindContactSourcePrefix marks organization contact points a
// classification added, so clearing it can withdraw exactly those rows.
const correspondentKindContactSourcePrefix = "correspondent_kind:"

// CorrespondentKindPerson names a saved Directory person bound to a
// classified cluster.
type CorrespondentKindPerson struct {
	ID          int64   `json:"id"`
	DisplayName *string `json:"display_name,omitzero" nullable:"false"`
	Revision    int64   `json:"revision"`
	// OnlyThisCluster is true when every participant bound to the person is
	// in this cluster, so the profile describes nothing but this record.
	OnlyThisCluster bool `json:"only_this_cluster"`
}

// CorrespondentKindRecord is one classified identity cluster.
type CorrespondentKindRecord struct {
	CanonicalID      int64                     `json:"canonical_id" doc:"The cluster's smallest participant ID."`
	MemberIDs        []int64                   `json:"member_ids"`
	Kind             correspondentkind.Kind    `json:"kind" enum:"person,organization,shared_mailbox,ignored,automated,mailing_list,unclear" doc:"The effective kind. unclear is only ever written by a Jev judgment and awaits review."`
	Source           *correspondentkind.Source `json:"source,omitzero" nullable:"false" doc:"Who classified the cluster: user, rule, or jev. Absent when it was never classified."`
	DisplayName      *string                   `json:"display_name,omitzero" nullable:"false"`
	Addresses        []string                  `json:"addresses"`
	OrganizationID   *int64                    `json:"organization_id,omitzero" nullable:"false"`
	OrganizationName *string                   `json:"organization_name,omitzero" nullable:"false"`
	Person           *CorrespondentKindPerson  `json:"person,omitzero" nullable:"false" doc:"The saved Directory person bound to this cluster, if any."`
	Actor            *string                   `json:"actor,omitzero" nullable:"false" doc:"Who wrote the effective classification. Rules record rule:<reason>; Jev records jev:<model>."`
	ClassifiedAt     *time.Time                `json:"classified_at,omitempty"`
	// Confidence and Probabilities are present only for a jev
	// classification: the judgment's confidence and its distribution over
	// the Jev options.
	Confidence    *float64           `json:"confidence,omitzero" nullable:"false" doc:"Confidence of a jev classification."`
	Probabilities map[string]float64 `json:"probabilities,omitzero" nullable:"false" doc:"Probability of each Jev option for a jev classification: individual_person, shared_role_or_team_mailbox, mailing_list_or_group, automated_notification_or_transactional, marketing_or_newsletter, unclear."`
}

// CorrespondentKindAssignment is the effective kind of one cluster.
type CorrespondentKindAssignment struct {
	Kind             correspondentkind.Kind   `json:"kind" enum:"person,organization,shared_mailbox,ignored,automated,mailing_list,unclear"`
	Source           correspondentkind.Source `json:"source" doc:"Who classified the cluster: user, rule, or jev."`
	OrganizationID   *int64                   `json:"organization_id,omitzero" nullable:"false"`
	OrganizationName *string                  `json:"organization_name,omitzero" nullable:"false"`
}

// SetCorrespondentKindInput classifies the cluster containing ParticipantID.
// OrganizationID or OrganizationName applies only to the organization kind;
// with neither, the organization is named after the cluster's display name
// or, failing that, its email domain.
type SetCorrespondentKindInput struct {
	ParticipantID    int64
	Kind             correspondentkind.Kind
	OrganizationID   *int64
	OrganizationName *string
	// RemoveOrganizationID applies only to kind person: when the cluster
	// was classified under this organization and, once the classification
	// is cleared, nothing else refers to it, the organization is deleted.
	// It lets an undo remove the organization its classification created.
	RemoveOrganizationID *int64
	Actor                string
}

// SetCorrespondentKindResult reports what a classification changed.
type SetCorrespondentKindResult struct {
	Record CorrespondentKindRecord `json:"record"`
	// ResolvedCandidates counts open identity match candidates the
	// classification rejected with reason not_a_person.
	ResolvedCandidates int `json:"resolved_candidates"`
	// RestoredCandidates counts candidates returned to review because the
	// cluster is a person again.
	RestoredCandidates  int  `json:"restored_candidates"`
	OrganizationCreated bool `json:"organization_created"`
	// OrganizationRemoved reports that the organization named by
	// RemoveOrganizationID was deleted because nothing else used it.
	OrganizationRemoved bool `json:"organization_removed"`
}

type correspondentKindRow struct {
	participantID    int64
	source           correspondentkind.Source
	kind             correspondentkind.Kind
	organizationID   *int64
	organizationName *string
	actor            *string
	classifiedAt     time.Time
	confidence       *float64
	probabilities    map[string]float64
}

// correspondentKindCluster is the resolved classification of one cluster.
type correspondentKindCluster struct {
	root      int64
	members   []int64
	effective correspondentKindRow
}

// rowWins reports whether row outranks current: the higher source
// precedence wins, then the later classification, then the lower
// participant ID so the choice is deterministic.
func (row correspondentKindRow) rowWins(current correspondentKindRow) bool {
	if a, b := row.source.Precedence(), current.source.Precedence(); a != b {
		return a > b
	}
	if !row.classifiedAt.Equal(current.classifiedAt) {
		return row.classifiedAt.After(current.classifiedAt)
	}
	return row.participantID < current.participantID
}

func loadCorrespondentKindRowsTx(ctx context.Context, tx *loggedTx) ([]correspondentKindRow, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT ck.participant_id, ck.source, ck.kind, ck.organization_id, o.name,
		       ck.actor, ck.classified_at, ck.confidence, ck.probabilities_json
		FROM correspondent_kinds ck
		LEFT JOIN organizations o ON o.id = ck.organization_id
		ORDER BY ck.participant_id, ck.source`)
	if err != nil {
		return nil, fmt.Errorf("load correspondent kinds: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := []correspondentKindRow{}
	for rows.Next() {
		var row correspondentKindRow
		var source, kind string
		var organizationID sql.NullInt64
		var organizationName, actor, probabilities sql.NullString
		var classifiedAt nullableTimestamp
		var confidence sql.NullFloat64
		if err := rows.Scan(&row.participantID, &source, &kind, &organizationID,
			&organizationName, &actor, &classifiedAt, &confidence, &probabilities); err != nil {
			return nil, fmt.Errorf("scan correspondent kind: %w", err)
		}
		if confidence.Valid {
			row.confidence = &confidence.Float64
		}
		row.probabilities = decodeKindProbabilities(probabilities)
		row.source = correspondentkind.Source(source)
		row.kind = correspondentkind.Kind(kind)
		if organizationID.Valid {
			row.organizationID = &organizationID.Int64
		}
		row.organizationName = nonBlankString(organizationName)
		row.actor = nonBlankString(actor)
		row.classifiedAt = classifiedAt.Time
		result = append(result, row)
	}
	return result, rows.Err()
}

// correspondentKindClustersTx resolves every cluster that has at least one
// correspondent kind row to its effective classification, including clusters
// whose effective kind is person.
func (s *Store) correspondentKindClustersTx(
	ctx context.Context, tx *loggedTx,
) ([]correspondentKindCluster, error) {
	return s.correspondentKindClustersFromTx(ctx, tx, false)
}

// userCorrespondentKindClustersTx resolves clusters from user decisions
// alone. Identity matching reads it: a rule or Jev classification changes
// rankings, lists, and enrichment, but only the user resolves or refuses
// identity matches.
func (s *Store) userCorrespondentKindClustersTx(
	ctx context.Context, tx *loggedTx,
) ([]correspondentKindCluster, error) {
	return s.correspondentKindClustersFromTx(ctx, tx, true)
}

func (s *Store) correspondentKindClustersFromTx(
	ctx context.Context, tx *loggedTx, userOnly bool,
) ([]correspondentKindCluster, error) {
	rows, err := loadCorrespondentKindRowsTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if userOnly {
		rows = slices.DeleteFunc(rows, func(row correspondentKindRow) bool {
			return row.source != correspondentkind.SourceUser
		})
	}
	if len(rows) == 0 {
		return nil, nil
	}
	edges, err := s.loadLinkEdgesTxContext(ctx, tx)
	if err != nil {
		return nil, err
	}
	adjacency := buildAdjacency(edges)
	roots := clustersFromEdges(edges)
	byRoot := map[int64]*correspondentKindCluster{}
	order := []int64{}
	for _, row := range rows {
		root, linked := roots[row.participantID]
		if !linked {
			root = row.participantID
		}
		cluster, ok := byRoot[root]
		if !ok {
			members := []int64{root}
			if linked {
				component := componentOfAdj(root, adjacency)
				members = make([]int64, 0, len(component))
				for id := range component {
					members = append(members, id)
				}
				slices.Sort(members)
			}
			cluster = &correspondentKindCluster{root: root, members: members, effective: row}
			byRoot[root] = cluster
			order = append(order, root)
			continue
		}
		if row.rowWins(cluster.effective) {
			cluster.effective = row
		}
	}
	slices.Sort(order)
	result := make([]correspondentKindCluster, 0, len(order))
	for _, root := range order {
		result = append(result, *byRoot[root])
	}
	if err := applyOwnerIdentityRuleTx(ctx, tx, result); err != nil {
		return nil, err
	}
	return result, nil
}

// applyOwnerIdentityRuleTx makes every cluster that contains one of the
// archive owner's identities resolve as a person, whatever rows its members
// carry: linking a classified address into the owner's cluster, or
// confirming a classified address as the owner's, never hides the owner.
func applyOwnerIdentityRuleTx(ctx context.Context, tx *loggedTx, clusters []correspondentKindCluster) error {
	// Unclear counts here too: an owner cluster is plainly a person, never
	// held out of anything.
	plainPerson := func(kind correspondentkind.Kind) bool {
		return kind == correspondentkind.Person || kind == ""
	}
	if !slices.ContainsFunc(clusters, func(cluster correspondentKindCluster) bool {
		return !plainPerson(cluster.effective.kind)
	}) {
		return nil
	}
	owners, err := ownerParticipantIDsTx(ctx, tx)
	if err != nil || len(owners) == 0 {
		return err
	}
	for i := range clusters {
		if plainPerson(clusters[i].effective.kind) {
			continue
		}
		if slices.ContainsFunc(clusters[i].members, func(id int64) bool {
			_, owner := owners[id]
			return owner
		}) {
			clusters[i].effective = correspondentKindRow{
				participantID: clusters[i].root, kind: correspondentkind.Person,
			}
		}
	}
	return nil
}

// anyNotPersonClassificationTx is a one-row probe for any classification
// other than person. Identity writers call it on every revision bump, so it
// also tolerates a SQLite archive opened before the table existed (schema
// initialization creates it).
func (s *Store) anyNotPersonClassificationTx(ctx context.Context, tx *loggedTx) (bool, error) {
	var classified int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
		SELECT 1 FROM correspondent_kinds WHERE kind <> ? LIMIT 1) probe`,
		correspondentkind.Person).Scan(&classified); err != nil {
		if s.dialect.IsNoSuchTableError(err) {
			return false, nil
		}
		return false, fmt.Errorf("probe correspondent kinds: %w", err)
	}
	return classified > 0, nil
}

// ownerIdentityActor records why a classification was replaced when its
// cluster came to include one of the owner's identities.
const ownerIdentityActor = "system:owner_identity"

// dropOwnerClusterClassificationsTx replaces the non-person classifications
// of clusters that now include an owner identity. A user row becomes an
// explicit person row with actor system:owner_identity, so the change is on
// record; derived rows are removed; organization contact points the
// classification added are withdrawn; and the identity match candidates the
// classification resolved return to review exactly as an explicit clear
// would return them.
//
// It runs with every identity revision bump, under the identity lock, so it
// is bounded: one indexed probe when nothing is classified; no edge scan
// when no classified participant is itself an owner identity and no owner
// identity is linked to anything; otherwise one linear pass over the link
// graph for the whole call.
func (s *Store) dropOwnerClusterClassificationsTx(ctx context.Context, tx *loggedTx) error {
	classified, err := s.anyNotPersonClassificationTx(ctx, tx)
	if err != nil || !classified {
		return err
	}
	rows, err := loadCorrespondentKindRowsTx(ctx, tx)
	if err != nil {
		return err
	}
	owners, err := ownerParticipantIDsTx(ctx, tx)
	if err != nil || len(owners) == 0 {
		return err
	}
	notPerson := make([]correspondentKindRow, 0, len(rows))
	direct := false
	for _, row := range rows {
		if row.kind.IsPerson() {
			continue
		}
		notPerson = append(notPerson, row)
		if _, owner := owners[row.participantID]; owner {
			direct = true
		}
	}
	if len(notPerson) == 0 {
		return nil
	}
	if !direct {
		linked, err := ownersHaveLinksTx(ctx, tx, owners)
		if err != nil || !linked {
			return err
		}
	}
	edges, err := s.loadLinkEdgesTxContext(ctx, tx)
	if err != nil {
		return err
	}
	roots := clustersFromEdges(edges)
	rootOf := func(id int64) int64 {
		if root, ok := roots[id]; ok {
			return root
		}
		return id
	}
	ownerRoots := make(map[int64]struct{}, len(owners))
	for id := range owners {
		ownerRoots[rootOf(id)] = struct{}{}
	}
	affected := []correspondentKindRow{}
	affectedRoots := map[int64]struct{}{}
	for _, row := range notPerson {
		root := rootOf(row.participantID)
		if _, ok := ownerRoots[root]; ok {
			affected = append(affected, row)
			affectedRoots[root] = struct{}{}
		}
	}
	if len(affected) == 0 {
		return nil
	}
	now := time.Now().UTC()
	participants := make([]int64, 0, len(affected))
	for _, row := range affected {
		participants = append(participants, row.participantID)
		if row.source != correspondentkind.SourceUser {
			if _, err := tx.ExecContext(ctx, `DELETE FROM correspondent_kinds
				WHERE participant_id = ? AND source = ?`, row.participantID, row.source); err != nil {
				return fmt.Errorf("drop owner cluster classification: %w", err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE correspondent_kinds
			SET kind = ?, organization_id = NULL, actor = ?, classified_at = ?
			WHERE participant_id = ? AND source = ?`,
			correspondentkind.Person, ownerIdentityActor, now, row.participantID, row.source); err != nil {
			return fmt.Errorf("clear owner cluster classification: %w", err)
		}
	}
	slices.Sort(participants)
	participants = slices.Compact(participants)
	if err := s.bumpCorrespondentKindRevisionTx(ctx, tx); err != nil {
		return err
	}
	if err := s.withdrawCorrespondentOrganizationContactsTx(ctx, tx, participants); err != nil {
		return err
	}
	// Every member of an affected cluster, so candidates resolved through a
	// member without its own row come back too.
	members := []int64{}
	for id, root := range roots {
		if _, ok := affectedRoots[root]; ok {
			members = append(members, id)
		}
	}
	for root := range affectedRoots {
		members = append(members, root)
	}
	slices.Sort(members)
	members = slices.Compact(members)
	_, err = s.restoreNotAPersonCandidatesTx(ctx, tx, members)
	return err
}

// ownersHaveLinksTx reports whether any owner participant appears in a
// participant link.
func ownersHaveLinksTx(ctx context.Context, tx *loggedTx, owners map[int64]struct{}) (bool, error) {
	ids := make([]int64, 0, len(owners))
	for id := range owners {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	linked := false
	for _, side := range []string{"participant_a", "participant_b"} {
		if err := queryInChunksContext(ctx, tx, ids, nil, `
			SELECT 1 FROM participant_links WHERE `+side+` IN (%s) LIMIT 1`,
			func(*loggedRows) error {
				linked = true
				return nil
			}); err != nil {
			return false, fmt.Errorf("check owner identity links: %w", err)
		}
		if linked {
			return true, nil
		}
	}
	return false, nil
}

// hiddenCorrespondentParticipantsTx maps every member of every cluster whose
// effective kind is not a person to that kind.
func (s *Store) hiddenCorrespondentParticipantsTx(
	ctx context.Context, tx *loggedTx,
) (map[int64]correspondentKindRow, error) {
	clusters, err := s.correspondentKindClustersTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	return hiddenMembers(clusters), nil
}

// userHiddenCorrespondentParticipantsTx is hiddenCorrespondentParticipantsTx
// for user decisions only; identity matching reads it.
func (s *Store) userHiddenCorrespondentParticipantsTx(
	ctx context.Context, tx *loggedTx,
) (map[int64]correspondentKindRow, error) {
	clusters, err := s.userCorrespondentKindClustersTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	return hiddenMembers(clusters), nil
}

func hiddenMembers(clusters []correspondentKindCluster) map[int64]correspondentKindRow {
	hidden := map[int64]correspondentKindRow{}
	for _, cluster := range clusters {
		if cluster.effective.kind.IsPerson() {
			continue
		}
		for _, member := range cluster.members {
			hidden[member] = cluster.effective
		}
	}
	return hidden
}

// NotPersonParticipantsContext maps every participant in a cluster that is
// classified as something other than a person to its kind. Consumers use it
// to leave those clusters out of People lists, rankings, and matching.
func (s *Store) NotPersonParticipantsContext(
	ctx context.Context,
) (map[int64]correspondentkind.Kind, error) {
	result := map[int64]correspondentkind.Kind{}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		hidden, err := s.hiddenCorrespondentParticipantsTx(ctx, tx)
		if err != nil {
			return err
		}
		for id, row := range hidden {
			result[id] = row.kind
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// CorrespondentKindsForParticipantsContext returns the effective
// non-person kind of each requested participant's cluster. Participants in
// person or unclassified clusters are absent from the result.
func (s *Store) CorrespondentKindsForParticipantsContext(
	ctx context.Context, participantIDs []int64,
) (map[int64]CorrespondentKindAssignment, error) {
	result := map[int64]CorrespondentKindAssignment{}
	if len(participantIDs) == 0 {
		return result, nil
	}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		hidden, err := s.hiddenCorrespondentParticipantsTx(ctx, tx)
		if err != nil {
			return err
		}
		for _, id := range participantIDs {
			if row, ok := hidden[id]; ok {
				result[id] = row.assignment()
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (row correspondentKindRow) assignment() CorrespondentKindAssignment {
	return CorrespondentKindAssignment{
		Kind: row.kind, Source: row.source,
		OrganizationID: row.organizationID, OrganizationName: row.organizationName,
	}
}

// CorrespondentKindListFilter narrows ListCorrespondentKindsContext.
type CorrespondentKindListFilter struct {
	Kind           correspondentkind.Kind
	OrganizationID *int64
}

// ListCorrespondentKindsContext lists every cluster whose effective kind is
// not a person, newest classification first.
func (s *Store) ListCorrespondentKindsContext(
	ctx context.Context, filter CorrespondentKindListFilter,
) ([]CorrespondentKindRecord, error) {
	if filter.Kind != "" && (!filter.Kind.Known() || filter.Kind == correspondentkind.Person) {
		return nil, fmt.Errorf("%w: cannot list kind %q", ErrCorrespondentKindInvalid, filter.Kind)
	}
	records := []CorrespondentKindRecord{}
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		clusters, err := s.correspondentKindClustersTx(ctx, tx)
		if err != nil {
			return err
		}
		for _, cluster := range clusters {
			effective := cluster.effective
			// Unclear judgments are listed only when asked for: they are a
			// review queue, not records marked as not a person.
			if effective.kind.IsPerson() && (filter.Kind != correspondentkind.Unclear ||
				!isUnclearCorrespondentCluster(cluster)) {
				continue
			}
			if filter.Kind != "" && effective.kind != filter.Kind {
				continue
			}
			if filter.OrganizationID != nil && (effective.organizationID == nil ||
				*effective.organizationID != *filter.OrganizationID) {
				continue
			}
			record, err := s.correspondentKindRecordTx(ctx, tx, cluster)
			if err != nil {
				return err
			}
			records = append(records, record)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(records, func(a, b CorrespondentKindRecord) int {
		if !a.ClassifiedAt.Equal(*b.ClassifiedAt) {
			if a.ClassifiedAt.After(*b.ClassifiedAt) {
				return -1
			}
			return 1
		}
		return compareInt64(a.CanonicalID, b.CanonicalID)
	})
	return records, nil
}

// GetCorrespondentKindContext returns the effective classification of the
// cluster containing participantID. A cluster with no classification reports
// kind person with no source.
func (s *Store) GetCorrespondentKindContext(
	ctx context.Context, participantID int64,
) (*CorrespondentKindRecord, error) {
	var record *CorrespondentKindRecord
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		if err := requireParticipantTx(ctx, tx, participantID); err != nil {
			return err
		}
		cluster, err := s.clusterCorrespondentKindTx(ctx, tx, participantID)
		if err != nil {
			return err
		}
		value, err := s.correspondentKindRecordTx(ctx, tx, cluster)
		if err != nil {
			return err
		}
		record = &value
		return nil
	})
	return record, err
}

// isUnclearCorrespondentCluster reports a cluster the Unclear
// correspondents review queue lists: its effective kind is a Jev unclear
// judgment no user or rule decision outranks. The pending-review check uses
// it too, so the Reviews dot and the queue agree.
func isUnclearCorrespondentCluster(cluster correspondentKindCluster) bool {
	return cluster.effective.kind == correspondentkind.Unclear
}

// clusterCorrespondentKindTx resolves the cluster containing participantID.
// Its effective kind is person when no member carries a row.
func (s *Store) clusterCorrespondentKindTx(
	ctx context.Context, tx *loggedTx, participantID int64,
) (correspondentKindCluster, error) {
	edges, err := s.loadLinkEdgesTxContext(ctx, tx)
	if err != nil {
		return correspondentKindCluster{}, err
	}
	members := sortedComponentMembers(participantID, edges)
	cluster := correspondentKindCluster{
		root: members[0], members: members,
		effective: correspondentKindRow{participantID: members[0], kind: correspondentkind.Person},
	}
	// An unclassified cluster has no source and no classification time.
	rows, err := loadCorrespondentKindRowsTx(ctx, tx)
	if err != nil {
		return cluster, err
	}
	found := false
	for _, row := range rows {
		if !slices.Contains(members, row.participantID) {
			continue
		}
		if !found || row.rowWins(cluster.effective) {
			cluster.effective = row
			found = true
		}
	}
	resolved := []correspondentKindCluster{cluster}
	if err := applyOwnerIdentityRuleTx(ctx, tx, resolved); err != nil {
		return cluster, err
	}
	return resolved[0], nil
}

func requireParticipantTx(ctx context.Context, tx *loggedTx, participantID int64) error {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM participants WHERE id = ?`, participantID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %d", ErrParticipantNotFound, participantID)
	}
	if err != nil {
		return fmt.Errorf("look up participant %d: %w", participantID, err)
	}
	return nil
}

func (s *Store) correspondentKindRecordTx(
	ctx context.Context, tx *loggedTx, cluster correspondentKindCluster,
) (CorrespondentKindRecord, error) {
	effective := cluster.effective
	record := CorrespondentKindRecord{
		CanonicalID: cluster.root, MemberIDs: slices.Clone(cluster.members),
		Kind:           effective.kind,
		OrganizationID: effective.organizationID, OrganizationName: effective.organizationName,
		Actor: effective.actor, Addresses: []string{},
		Confidence: effective.confidence, Probabilities: effective.probabilities,
	}
	if effective.source != "" {
		source, classifiedAt := effective.source, effective.classifiedAt
		record.Source, record.ClassifiedAt = &source, &classifiedAt
	}
	name, err := clusterBestDisplayNameTx(ctx, tx, cluster.members)
	if err != nil {
		return record, err
	}
	record.DisplayName = name
	addresses, err := clusterAddressesTx(ctx, tx, cluster.members)
	if err != nil {
		return record, err
	}
	record.Addresses = addresses
	person, err := clusterBoundPersonTx(ctx, tx, cluster.members)
	if err != nil {
		return record, err
	}
	record.Person = person
	return record, nil
}

// clusterAddressesTx returns the members' distinct email addresses and
// phone numbers, emails first.
func clusterAddressesTx(ctx context.Context, tx *loggedTx, members []int64) ([]string, error) {
	emails, phones := []string{}, []string{}
	seen := map[string]struct{}{}
	add := func(list *[]string, value sql.NullString, lower bool) {
		text := strings.TrimSpace(value.String)
		if !value.Valid || text == "" {
			return
		}
		key := text
		if lower {
			key = strings.ToLower(text)
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		*list = append(*list, text)
	}
	if err := queryInChunksContext(ctx, tx, members, nil, `
		SELECT email_address, phone_number FROM participants
		WHERE id IN (%s) ORDER BY id`, func(rows *loggedRows) error {
		var email, phone sql.NullString
		if err := rows.Scan(&email, &phone); err != nil {
			return fmt.Errorf("scan cluster address: %w", err)
		}
		add(&emails, email, true)
		add(&phones, phone, false)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("load cluster addresses: %w", err)
	}
	if err := queryInChunksContext(ctx, tx, members, nil, `
		SELECT identifier_type, identifier_value FROM participant_identifiers
		WHERE participant_id IN (%s) AND identifier_type IN ('email', 'phone')
		ORDER BY participant_id, id`, func(rows *loggedRows) error {
		var kind string
		var value sql.NullString
		if err := rows.Scan(&kind, &value); err != nil {
			return fmt.Errorf("scan cluster identifier: %w", err)
		}
		if kind == "email" {
			add(&emails, value, true)
		} else {
			add(&phones, value, false)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("load cluster identifiers: %w", err)
	}
	return append(emails, phones...), nil
}

// clusterBoundPersonTx returns the saved person bound to the cluster, if any,
// and whether that person is bound to nothing outside the cluster. Bindings
// within a cluster are all-or-none to at most one person; when unlinking left
// several, the lowest person ID is reported.
func clusterBoundPersonTx(
	ctx context.Context, tx *loggedTx, members []int64,
) (*CorrespondentKindPerson, error) {
	persons, err := personIDsForParticipantsTx(ctx, tx, members)
	if err != nil || len(persons) == 0 {
		return nil, err
	}
	person := &CorrespondentKindPerson{ID: persons[0]}
	var name sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT display_name, revision FROM persons WHERE id = ?`,
		person.ID).Scan(&name, &person.Revision); err != nil {
		return nil, fmt.Errorf("load classified cluster person: %w", err)
	}
	person.DisplayName = nonBlankString(name)
	outside := 0
	args := []any{person.ID}
	for _, member := range members {
		args = append(args, member)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM person_participants
		WHERE person_id = ? AND participant_id NOT IN (`+placeholders(len(members))+`)`,
		args...).Scan(&outside); err != nil {
		return nil, fmt.Errorf("count person bindings outside cluster: %w", err)
	}
	person.OnlyThisCluster = outside == 0
	return person, nil
}

// SetCorrespondentKindContext classifies the identity cluster containing the
// participant. A non-person kind rejects the cluster's open identity match
// candidates with reason not_a_person; setting person restores those
// candidates and withdraws organization contact points the classification
// added. Saved people are never deleted here: when a person profile exists
// only for this cluster the result names it so the caller can offer an
// explicit delete.
func (s *Store) SetCorrespondentKindContext(
	ctx context.Context, input SetCorrespondentKindInput,
) (*SetCorrespondentKindResult, error) {
	if !input.Kind.Valid() {
		return nil, fmt.Errorf("%w: unknown kind %q", ErrCorrespondentKindInvalid, input.Kind)
	}
	if input.ParticipantID <= 0 {
		return nil, fmt.Errorf("%w: participant id must be positive", ErrInvalidParticipantID)
	}
	if input.Kind != correspondentkind.Organization &&
		(input.OrganizationID != nil || input.OrganizationName != nil) {
		return nil, fmt.Errorf("%w: an organization applies only to kind organization",
			ErrCorrespondentKindInvalid)
	}
	if input.OrganizationID != nil && input.OrganizationName != nil {
		return nil, fmt.Errorf("%w: choose an organization by id or by name, not both",
			ErrCorrespondentKindInvalid)
	}
	if input.OrganizationName != nil && strings.TrimSpace(*input.OrganizationName) == "" {
		return nil, fmt.Errorf("%w: organization name is blank", ErrCorrespondentKindInvalid)
	}
	if input.RemoveOrganizationID != nil && (!input.Kind.IsPerson() || *input.RemoveOrganizationID <= 0) {
		return nil, fmt.Errorf("%w: removing an organization applies only to kind person",
			ErrCorrespondentKindInvalid)
	}
	actor := strings.TrimSpace(input.Actor)
	if actor == "" {
		actor = string(ProvenanceUser)
	}
	return retryBusyWrite(ctx, s, "set correspondent kind", func() (*SetCorrespondentKindResult, error) {
		var result *SetCorrespondentKindResult
		err := s.withTxContext(ctx, func(tx *loggedTx) error {
			var err error
			result, err = s.setCorrespondentKindTx(ctx, tx, input, actor)
			return err
		})
		return result, err
	})
}

func (s *Store) setCorrespondentKindTx(
	ctx context.Context, tx *loggedTx, input SetCorrespondentKindInput, actor string,
) (*SetCorrespondentKindResult, error) {
	if err := s.lockIdentityMutationTxContext(ctx, tx); err != nil {
		return nil, err
	}
	if err := requireParticipantTx(ctx, tx, input.ParticipantID); err != nil {
		return nil, err
	}
	edges, err := s.loadLinkEdgesTxContext(ctx, tx)
	if err != nil {
		return nil, err
	}
	members := sortedComponentMembers(input.ParticipantID, edges)
	if !input.Kind.IsPerson() {
		owners, err := ownerParticipantIDsTx(ctx, tx)
		if err != nil {
			return nil, err
		}
		for _, member := range members {
			if _, owner := owners[member]; owner {
				return nil, ErrCorrespondentKindOwner
			}
		}
	}
	result := &SetCorrespondentKindResult{}
	removeOrganization := false
	if input.RemoveOrganizationID != nil {
		removeOrganization, err = clusterClassifiedUnderTx(ctx, tx, members, *input.RemoveOrganizationID)
		if err != nil {
			return nil, err
		}
	}
	var organizationID *int64
	if input.Kind == correspondentkind.Organization {
		organization, created, err := s.resolveCorrespondentOrganizationTx(ctx, tx, input, members)
		if err != nil {
			return nil, err
		}
		organizationID = &organization.ID
		result.OrganizationCreated = created
	}
	// Withdraw what an earlier organization classification attached before
	// writing the new one, so a change of organization moves the addresses.
	if err := s.withdrawCorrespondentOrganizationContactsTx(ctx, tx, members); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for _, member := range members {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO correspondent_kinds (
				participant_id, source, kind, organization_id, actor, classified_at
			) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (participant_id, source) DO UPDATE SET
				kind = excluded.kind, organization_id = excluded.organization_id,
				confidence = NULL, probabilities_json = NULL, identity_revision = NULL,
				actor = excluded.actor, classified_at = excluded.classified_at`,
			member, correspondentkind.SourceUser, input.Kind, organizationID, actor, now,
		); err != nil {
			return nil, fmt.Errorf("write correspondent kind: %w", err)
		}
	}
	if err := s.bumpCorrespondentKindRevisionTx(ctx, tx); err != nil {
		return nil, err
	}
	if organizationID != nil {
		if err := s.attachCorrespondentOrganizationContactsTx(ctx, tx, *organizationID, members, now); err != nil {
			return nil, err
		}
	}
	if input.Kind.IsPerson() {
		result.RestoredCandidates, err = s.restoreNotAPersonCandidatesTx(ctx, tx, members)
	} else {
		result.ResolvedCandidates, err = s.resolveNotAPersonCandidatesTx(ctx, tx, members)
	}
	if err != nil {
		return nil, err
	}
	if removeOrganization {
		result.OrganizationRemoved, err = s.deleteUnreferencedOrganizationTx(ctx, tx, *input.RemoveOrganizationID)
		if err != nil {
			return nil, err
		}
	}
	cluster, err := s.clusterCorrespondentKindTx(ctx, tx, input.ParticipantID)
	if err != nil {
		return nil, err
	}
	result.Record, err = s.correspondentKindRecordTx(ctx, tx, cluster)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// clusterClassifiedUnderTx reports whether a user decision grouped any
// member of the cluster under the organization.
func clusterClassifiedUnderTx(
	ctx context.Context, tx *loggedTx, members []int64, organizationID int64,
) (bool, error) {
	for _, member := range members {
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM correspondent_kinds
			WHERE participant_id = ? AND source = ? AND organization_id = ?`,
			member, correspondentkind.SourceUser, organizationID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("check classified organization: %w", err)
		}
		return true, nil
	}
	return false, nil
}

// organizationReferenceQueries each find one row that keeps an organization
// in use: an employment, a merge redirect, a classification, an active
// contact point, or any profile, attribute, alias, review, or fact record.
// The only rows that do not count are contact points a classification
// attached and a later clear withdrew; every other contact point, current or
// superseded, keeps the organization.
var organizationReferenceQueries = []string{
	`SELECT 1 FROM employments WHERE organization_id = ? LIMIT 1`,
	`SELECT 1 FROM organizations WHERE merged_into_id = ? LIMIT 1`,
	`SELECT 1 FROM correspondent_kinds WHERE organization_id = ? LIMIT 1`,
	`SELECT 1 FROM organization_contact_points
		WHERE organization_id = ? AND NOT (
			superseded_at IS NOT NULL AND source = '` + string(ProvenanceUser) + `'
			AND COALESCE(source_ref, '') LIKE '` + correspondentKindContactSourcePrefix + `%')
		LIMIT 1`,
	`SELECT 1 FROM organization_names WHERE organization_id = ? LIMIT 1`,
	`SELECT 1 FROM organization_identifiers WHERE organization_id = ? LIMIT 1`,
	`SELECT 1 FROM organization_addresses WHERE organization_id = ? LIMIT 1`,
	`SELECT 1 FROM organization_categories WHERE organization_id = ? LIMIT 1`,
	`SELECT 1 FROM organization_media WHERE organization_id = ? LIMIT 1`,
	`SELECT 1 FROM organization_attribute_values WHERE organization_id = ? LIMIT 1`,
	`SELECT 1 FROM organization_title_aliases WHERE organization_id = ? LIMIT 1`,
	`SELECT 1 FROM organization_match_reviews WHERE organization_id = ? LIMIT 1`,
	`SELECT 1 FROM person_fact_decisions WHERE resolved_organization_id = ? LIMIT 1`,
}

// deleteUnreferencedOrganizationTx deletes the organization only when
// nothing refers to it, reporting whether it did. A missing or merged
// organization is left alone.
func (s *Store) deleteUnreferencedOrganizationTx(
	ctx context.Context, tx *loggedTx, organizationID int64,
) (bool, error) {
	organization, err := getOrganizationForUpdateTx(ctx, tx, organizationID)
	if errors.Is(err, ErrOrganizationNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if organization.MergedIntoID != nil {
		return false, nil
	}
	for _, query := range organizationReferenceQueries {
		var one int
		err := tx.QueryRowContext(ctx, query, organizationID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("check organization %d references: %w", organizationID, err)
		}
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM organizations WHERE id = ?`, organizationID); err != nil {
		return false, fmt.Errorf("delete organization %d: %w", organizationID, err)
	}
	return true, nil
}

// resolveCorrespondentOrganizationTx finds or creates the organization a
// cluster is classified under. An explicit ID must name an organization (a
// merged one resolves to its survivor). A name, or the cluster's display
// name or email domain when none is given, reuses the one active
// organization with that name or alias and creates one when none exists.
func (s *Store) resolveCorrespondentOrganizationTx(
	ctx context.Context, tx *loggedTx, input SetCorrespondentKindInput, members []int64,
) (*Organization, bool, error) {
	if input.OrganizationID != nil {
		organization, err := getOrganizationForUpdateTx(ctx, tx, *input.OrganizationID)
		if err != nil {
			return nil, false, err
		}
		for hops := 0; organization.MergedIntoID != nil && hops < 64; hops++ {
			organization, err = getOrganizationForUpdateTx(ctx, tx, *organization.MergedIntoID)
			if err != nil {
				return nil, false, err
			}
		}
		if organization.RetiredAt != nil {
			return nil, false, fmt.Errorf("%w: organization %d is retired",
				ErrCorrespondentKindInvalid, organization.ID)
		}
		return organization, false, nil
	}
	domain, err := clusterEmailDomainTx(ctx, tx, members)
	if err != nil {
		return nil, false, err
	}
	name := ""
	if input.OrganizationName != nil {
		name = strings.TrimSpace(*input.OrganizationName)
	}
	if name == "" {
		display, err := clusterBestDisplayNameTx(ctx, tx, members)
		if err != nil {
			return nil, false, err
		}
		if display != nil {
			name = *display
		}
	}
	if name == "" {
		name = domain
	}
	if name == "" {
		return nil, false, fmt.Errorf("%w: name the organization", ErrCorrespondentKindInvalid)
	}
	normalized := NormalizeOrganizationName(name)
	rows, err := tx.QueryContext(ctx, `
		SELECT o.id FROM organizations o
		WHERE o.retired_at IS NULL AND o.merged_into_id IS NULL
		  AND (o.name_normalized = ? OR EXISTS (
			SELECT 1 FROM organization_names n
			WHERE n.organization_id = o.id AND n.name_normalized = ?
			  AND n.active_until IS NULL AND n.superseded_at IS NULL))
		ORDER BY o.id`, normalized, normalized)
	if err != nil {
		return nil, false, fmt.Errorf("look up organization by name: %w", err)
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, false, fmt.Errorf("scan organization match: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, false, fmt.Errorf("close organization matches: %w", err)
	}
	switch len(ids) {
	case 1:
		organization, err := getOrganizationForUpdateTx(ctx, tx, ids[0])
		return organization, false, err
	case 0:
	default:
		return nil, false, ErrCorrespondentKindOrganizationAmbiguous
	}
	organizationInput := OrganizationInput{Name: name, Kind: OrganizationKindCompany}
	if domain != "" && !correspondentkind.IsFreemailDomain(domain) {
		organizationInput.PrimaryDomain = &domain
	}
	organizationInput, err = validateOrganizationInput(organizationInput)
	if err != nil {
		return nil, false, err
	}
	organization, err := scanOrganization(tx.QueryRowContext(ctx, `
		INSERT INTO organizations (name, name_normalized, kind, primary_domain, description)
		VALUES (?, ?, ?, ?, ?)
		RETURNING `+organizationColumns,
		organizationInput.Name, NormalizeOrganizationName(organizationInput.Name),
		organizationInput.Kind, organizationInput.PrimaryDomain, organizationInput.Description))
	if err != nil {
		return nil, false, fmt.Errorf("create organization: %w", err)
	}
	return organization, true, nil
}

// clusterEmailDomainTx returns the domain of the cluster's first email
// address, or "" when it has none.
func clusterEmailDomainTx(ctx context.Context, tx *loggedTx, members []int64) (string, error) {
	addresses, err := clusterAddressesTx(ctx, tx, members)
	if err != nil {
		return "", err
	}
	for _, address := range addresses {
		if at := strings.LastIndex(address, "@"); at > 0 && at < len(address)-1 {
			return strings.ToLower(address[at+1:]), nil
		}
	}
	return "", nil
}

// attachCorrespondentOrganizationContactsTx records each member email
// address as a user-sourced organization contact point, skipping addresses
// the organization already lists.
func (s *Store) attachCorrespondentOrganizationContactsTx(
	ctx context.Context, tx *loggedTx, organizationID int64, members []int64, now time.Time,
) error {
	type memberEmail struct {
		participantID int64
		email         string
	}
	emails := []memberEmail{}
	seen := map[string]struct{}{}
	if err := queryInChunksContext(ctx, tx, members, nil, `
		SELECT id, email_address FROM participants
		WHERE id IN (%s) AND email_address IS NOT NULL ORDER BY id`, func(rows *loggedRows) error {
		var id int64
		var email string
		if err := rows.Scan(&id, &email); err != nil {
			return fmt.Errorf("scan member email: %w", err)
		}
		email = strings.TrimSpace(email)
		if _, ok := seen[strings.ToLower(email)]; ok || email == "" {
			return nil
		}
		seen[strings.ToLower(email)] = struct{}{}
		emails = append(emails, memberEmail{participantID: id, email: email})
		return nil
	}); err != nil {
		return fmt.Errorf("load member emails: %w", err)
	}
	added := false
	for _, entry := range emails {
		normalized, err := NormalizeServiceValue(nil, ContactAddressEmail, entry.email)
		if err != nil {
			continue
		}
		var existing int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM organization_contact_points
			WHERE organization_id = ? AND address_kind = ? AND normalized_value = ?
			  AND active_until IS NULL AND superseded_at IS NULL`,
			organizationID, ContactAddressEmail, normalized).Scan(&existing)
		if err != nil {
			return fmt.Errorf("check organization contact point: %w", err)
		}
		if existing > 0 {
			continue
		}
		sourceRef := correspondentKindContactSourcePrefix + strconv.FormatInt(entry.participantID, 10)
		if _, err := s.insertOrganizationContactTx(ctx, tx, organizationID, preparedOrganizationContact{
			input: OrganizationContactPointInput{
				AddressKind: ContactAddressEmail, OriginalValue: entry.email,
				Envelope: ValueEnvelopeInput{Source: ProvenanceUser, SourceRef: &sourceRef},
			},
			normalized:           normalized,
			normalization:        fallbackContactNormalization(ContactAddressEmail),
			normalizationVersion: 1,
		}, now); err != nil {
			return err
		}
		added = true
	}
	if !added {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE organizations
		SET revision = revision + 1, updated_at = `+s.dialect.Now()+` WHERE id = ?`,
		organizationID); err != nil {
		return fmt.Errorf("bump organization revision: %w", err)
	}
	return s.bumpEmployedPersonVCardProjectionsTx(ctx, tx, organizationID)
}

// withdrawCorrespondentOrganizationContactsTx supersedes the organization
// contact points an earlier classification of these members added.
func (s *Store) withdrawCorrespondentOrganizationContactsTx(
	ctx context.Context, tx *loggedTx, members []int64,
) error {
	refs := make([]string, 0, len(members))
	for _, member := range members {
		refs = append(refs, correspondentKindContactSourcePrefix+strconv.FormatInt(member, 10))
	}
	organizations := []int64{}
	if err := queryInChunksContext(ctx, tx, refs, []any{ProvenanceUser}, `
		SELECT DISTINCT organization_id FROM organization_contact_points
		WHERE source = ? AND superseded_at IS NULL AND source_ref IN (%s)`,
		func(rows *loggedRows) error {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("scan classified organization: %w", err)
			}
			organizations = append(organizations, id)
			return nil
		}); err != nil {
		return fmt.Errorf("load classified organization contacts: %w", err)
	}
	if len(organizations) == 0 {
		return nil
	}
	now := time.Now().UTC()
	if _, err := execCountInChunksTx(ctx, tx, refs, []any{now, now, ProvenanceUser}, `
		UPDATE organization_contact_points
		SET active_until = ?, superseded_at = ?, updated_at = CURRENT_TIMESTAMP
		WHERE source = ? AND superseded_at IS NULL AND source_ref IN (%s)`); err != nil {
		return fmt.Errorf("withdraw classified organization contacts: %w", err)
	}
	slices.Sort(organizations)
	for _, id := range slices.Compact(organizations) {
		if _, err := tx.ExecContext(ctx, `UPDATE organizations
			SET revision = revision + 1, updated_at = `+s.dialect.Now()+` WHERE id = ?`, id); err != nil {
			return fmt.Errorf("bump organization revision: %w", err)
		}
		if err := s.bumpEmployedPersonVCardProjectionsTx(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

// resolveNotAPersonCandidatesTx rejects every open identity match candidate
// with a participant endpoint in the cluster, recording reason
// not_a_person, so the queue does not propose it again.
func (s *Store) resolveNotAPersonCandidatesTx(
	ctx context.Context, tx *loggedTx, members []int64,
) (int, error) {
	ids := map[int64]struct{}{}
	for _, side := range []string{"left", "right"} {
		if err := queryInChunksContext(ctx, tx, members, []any{
			IdentityMatchStateCandidate, IdentityMatchStateConflict, IdentityMatchParticipant,
		}, `SELECT id FROM identity_match_candidates
			WHERE state IN (?, ?) AND `+side+`_kind = ? AND `+side+`_id IN (%s)`,
			func(rows *loggedRows) error {
				var id int64
				if err := rows.Scan(&id); err != nil {
					return fmt.Errorf("scan open candidate: %w", err)
				}
				ids[id] = struct{}{}
				return nil
			}); err != nil {
			return 0, fmt.Errorf("load open candidates: %w", err)
		}
	}
	ordered := make([]int64, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	slices.Sort(ordered)
	for _, id := range ordered {
		if err := snapshotCandidateDecisionTx(ctx, tx, id); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE identity_match_candidates SET
				state = ?, decided_by = ?, decided_at = CURRENT_TIMESTAMP, notes = ?,
				application_pending = FALSE, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`,
			IdentityMatchStateRejected, string(ProvenanceUser), correspondentkind.NotAPersonReason, id); err != nil {
			return 0, fmt.Errorf("resolve identity candidate %d as not a person: %w", id, err)
		}
	}
	return len(ordered), nil
}

// carryNotAPersonSnapshotTx keeps a "not a person" resolution restorable
// when duplicate candidates collapse into one (after a participant merge).
// The losers are deleted and their snapshots go with them, so when the
// collapsed row ends up as a not_a_person rejection the survivor needs its
// own snapshot: its own pre-collapse decision when it was not itself the
// resolved row, otherwise a loser's snapshot moved onto it. group[0] is the
// winner; the losers are still present when this runs.
func carryNotAPersonSnapshotTx(
	ctx context.Context, tx *loggedTx, group []identityMatchCandidateMergeRow,
	state IdentityMatchState, notes sql.NullString,
) error {
	winner := group[0]
	resolved := state == IdentityMatchStateRejected && notes.Valid &&
		notes.String == correspondentkind.NotAPersonReason
	if !resolved {
		return nil
	}
	var winnerSnapshots int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM correspondent_kind_candidate_snapshots
		WHERE candidate_id = ?`, winner.ID).Scan(&winnerSnapshots); err != nil {
		return fmt.Errorf("check candidate decision snapshot: %w", err)
	}
	if winnerSnapshots > 0 {
		return nil
	}
	var source int64
	for _, loser := range group[1:] {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM correspondent_kind_candidate_snapshots
			WHERE candidate_id = ?`, loser.ID).Scan(&count); err != nil {
			return fmt.Errorf("check candidate decision snapshot: %w", err)
		}
		if count > 0 {
			source = loser.ID
			break
		}
	}
	if source == 0 {
		return nil
	}
	winnerResolved := winner.State == IdentityMatchStateRejected && winner.Notes.Valid &&
		winner.Notes.String == correspondentkind.NotAPersonReason
	if !winnerResolved {
		// The survivor's own open decision is what clearing should restore.
		return snapshotCandidateDecisionTx(ctx, tx, winner.ID)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE correspondent_kind_candidate_snapshots
		SET candidate_id = ? WHERE candidate_id = ?`, winner.ID, source); err != nil {
		return fmt.Errorf("move candidate decision snapshot: %w", err)
	}
	return nil
}

// dropCandidateDecisionSnapshotTx forgets the pre-resolution decision of a
// candidate that just received an explicit decision, so clearing the
// classification later never restores an older state over it. It tolerates
// a SQLite archive opened before the snapshot table existed.
func dropCandidateDecisionSnapshotTx(ctx context.Context, tx *loggedTx, candidateID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM correspondent_kind_candidate_snapshots
		WHERE candidate_id = ?`, candidateID); err != nil {
		if isSQLiteError(err, "no such table") {
			return nil
		}
		return fmt.Errorf("drop candidate decision snapshot: %w", err)
	}
	return nil
}

// snapshotCandidateDecisionTx records a candidate's current decision fields
// before a classification resolves it.
func snapshotCandidateDecisionTx(ctx context.Context, tx *loggedTx, candidateID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM correspondent_kind_candidate_snapshots
		WHERE candidate_id = ?`, candidateID); err != nil {
		return fmt.Errorf("clear candidate decision snapshot: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO correspondent_kind_candidate_snapshots (
			candidate_id, prior_state, prior_decided_by, prior_decided_at, prior_notes,
			prior_application_pending, prior_pre_conflict_state)
		SELECT id, state, decided_by, decided_at, notes, application_pending, pre_conflict_state
		FROM identity_match_candidates WHERE id = ?`, candidateID); err != nil {
		return fmt.Errorf("snapshot candidate decision: %w", err)
	}
	return nil
}

// restoreNotAPersonCandidatesTx returns candidates a classification rejected
// to review.
func (s *Store) restoreNotAPersonCandidatesTx(
	ctx context.Context, tx *loggedTx, members []int64,
) (int, error) {
	type resolvedCandidate struct {
		id                  int64
		leftKind, rightKind IdentityMatchEndpointKind
		leftID, rightID     int64
		notes               string
	}
	found := map[int64]resolvedCandidate{}
	for _, side := range []string{"left", "right"} {
		if err := queryInChunksContext(ctx, tx, members, []any{
			IdentityMatchStateRejected, correspondentkind.NotAPersonReason, IdentityMatchParticipant,
		}, `SELECT c.id, c.left_kind, c.left_id, c.right_kind, c.right_id, c.notes
			FROM identity_match_candidates c
			WHERE c.state = ? AND c.notes = ? AND EXISTS (
				SELECT 1 FROM correspondent_kind_candidate_snapshots snapshot
				WHERE snapshot.candidate_id = c.id)
			  AND c.`+side+`_kind = ? AND c.`+side+`_id IN (%s)`,
			func(rows *loggedRows) error {
				var row resolvedCandidate
				if err := rows.Scan(&row.id, &row.leftKind, &row.leftID,
					&row.rightKind, &row.rightID, &row.notes); err != nil {
					return fmt.Errorf("scan resolved candidate: %w", err)
				}
				found[row.id] = row
				return nil
			}); err != nil {
			return 0, fmt.Errorf("load resolved candidates: %w", err)
		}
	}
	if len(found) == 0 {
		return 0, nil
	}
	// A candidate returns to review only when neither endpoint is still not
	// a person by a user decision; the other side may carry its own.
	hidden, err := s.userHiddenCorrespondentParticipantsTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	ids := make([]int64, 0, len(found))
	for id := range found {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	restored := 0
	for _, id := range ids {
		row := found[id]
		stillHidden := false
		for _, endpoint := range []struct {
			kind IdentityMatchEndpointKind
			id   int64
		}{{row.leftKind, row.leftID}, {row.rightKind, row.rightID}} {
			if _, ok := hidden[endpoint.id]; ok && endpoint.kind == IdentityMatchParticipant {
				stillHidden = true
			}
		}
		if stillHidden {
			continue
		}
		// A candidate that would reconnect an identity the user detached
		// from a person stays rejected; the detachment takes over its
		// snapshot so undoing the detachment restores it.
		detachmentID, detached, err := s.activePersonDetachmentSeparatingTx(
			ctx, tx, row.leftKind, row.leftID, row.rightKind, row.rightID)
		if err != nil {
			return restored, err
		}
		if detached {
			if err := s.adoptNotAPersonCandidateForDetachmentTx(ctx, tx, detachmentID, id); err != nil {
				return restored, err
			}
			continue
		}
		// Restore the exact decision fields the candidate had before it was
		// resolved, then drop the snapshot.
		if _, err := tx.ExecContext(ctx, `UPDATE identity_match_candidates SET
				state = (SELECT prior_state FROM correspondent_kind_candidate_snapshots WHERE candidate_id = ?),
				decided_by = (SELECT prior_decided_by FROM correspondent_kind_candidate_snapshots WHERE candidate_id = ?),
				decided_at = (SELECT prior_decided_at FROM correspondent_kind_candidate_snapshots WHERE candidate_id = ?),
				notes = (SELECT prior_notes FROM correspondent_kind_candidate_snapshots WHERE candidate_id = ?),
				application_pending = (SELECT prior_application_pending FROM correspondent_kind_candidate_snapshots WHERE candidate_id = ?),
				pre_conflict_state = (SELECT prior_pre_conflict_state FROM correspondent_kind_candidate_snapshots WHERE candidate_id = ?),
				updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`, id, id, id, id, id, id, id); err != nil {
			return restored, fmt.Errorf("restore identity candidate %d: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM correspondent_kind_candidate_snapshots
			WHERE candidate_id = ?`, id); err != nil {
			return restored, fmt.Errorf("drop candidate decision snapshot: %w", err)
		}
		restored++
	}
	return restored, nil
}

// execCountInChunksTx runs an IN-list statement over ids in bounded chunks
// and returns the total number of rows it changed.
func execCountInChunksTx[T any](
	ctx context.Context, tx *loggedTx, ids []T, prefixArgs []any, queryTemplate string,
) (int, error) {
	const chunkSize = 500
	total := 0
	for start := 0; start < len(ids); start += chunkSize {
		chunk := ids[start:min(start+chunkSize, len(ids))]
		args := slices.Clone(prefixArgs)
		for _, id := range chunk {
			args = append(args, id)
		}
		result, err := tx.ExecContext(ctx, fmt.Sprintf(queryTemplate, placeholders(len(chunk))), args...)
		if err != nil {
			return total, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += int(changed)
	}
	return total, nil
}

// participantsClassifiedNotPersonTx reports whether any of the given
// participants is in a cluster whose effective kind is not a person. It
// resolves clusters from all their members' rows (so a later-linked member
// is covered and a newer person override wins), and costs one indexed probe
// when nothing is classified.
func (s *Store) participantsClassifiedNotPersonTx(
	ctx context.Context, tx *loggedTx, participantIDs []int64,
) (bool, error) {
	if len(participantIDs) == 0 {
		return false, nil
	}
	classified, err := s.anyNotPersonClassificationTx(ctx, tx)
	if err != nil {
		return false, err
	}
	if !classified {
		return false, nil
	}
	hidden, err := s.userHiddenCorrespondentParticipantsTx(ctx, tx)
	if err != nil {
		return false, err
	}
	for _, id := range participantIDs {
		if _, ok := hidden[id]; ok {
			return true, nil
		}
	}
	return false, nil
}

// rewriteCorrespondentKindsForMergeTx moves a merged-away participant's
// classifications to the survivor. Rows from different sources both move and
// resolve by source precedence; for the same source the newer
// classification wins, and a tie keeps the survivor's.
func rewriteCorrespondentKindsForMergeTx(ctx context.Context, tx *loggedTx, oldID, newID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM correspondent_kinds
		WHERE participant_id = ? AND EXISTS (
			SELECT 1 FROM correspondent_kinds absorbed
			WHERE absorbed.participant_id = ? AND absorbed.source = correspondent_kinds.source
			  AND absorbed.classified_at > correspondent_kinds.classified_at)`,
		newID, oldID); err != nil {
		return fmt.Errorf("drop superseded survivor correspondent kinds: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM correspondent_kinds
		WHERE participant_id = ? AND EXISTS (
			SELECT 1 FROM correspondent_kinds survivor
			WHERE survivor.participant_id = ? AND survivor.source = correspondent_kinds.source)`,
		oldID, newID); err != nil {
		return fmt.Errorf("drop merged correspondent kinds: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE correspondent_kinds SET participant_id = ?
		WHERE participant_id = ?`, newID, oldID); err != nil {
		return fmt.Errorf("move correspondent kinds: %w", err)
	}
	// Organization contact points an organization classification added name
	// their participant; follow it to the survivor so clearing the
	// survivor withdraws them.
	if _, err := tx.ExecContext(ctx, `UPDATE organization_contact_points SET source_ref = ?
		WHERE source = ? AND source_ref = ?`,
		correspondentKindContactSourcePrefix+strconv.FormatInt(newID, 10), ProvenanceUser,
		correspondentKindContactSourcePrefix+strconv.FormatInt(oldID, 10)); err != nil {
		return fmt.Errorf("move classified organization contacts: %w", err)
	}
	return nil
}

// hidesSavedPerson reports whether a kind removes a saved person whose every
// identity carries it from People lists.
func hidesSavedPerson(kind correspondentkind.Kind) bool {
	return kind.LeavesPeopleLists()
}

// notPeoplePersonIDsTx returns saved people whose every bound participant is
// in a cluster classified as an organization or ignored. People lists hide
// them by default.
func (s *Store) notPeoplePersonIDsTx(ctx context.Context, tx *loggedTx) ([]int64, error) {
	hidden, err := s.hiddenCorrespondentParticipantsTx(ctx, tx)
	if err != nil || len(hidden) == 0 {
		return nil, err
	}
	candidates := []int64{}
	for id, row := range hidden {
		if hidesSavedPerson(row.kind) {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	slices.Sort(candidates)
	bindings := map[int64][]int64{}
	if err := queryInChunksContext(ctx, tx, candidates, nil, `
		SELECT person_id FROM person_participants WHERE participant_id IN (%s)`,
		func(rows *loggedRows) error {
			var personID int64
			if err := rows.Scan(&personID); err != nil {
				return fmt.Errorf("scan classified person: %w", err)
			}
			bindings[personID] = nil
			return nil
		}); err != nil {
		return nil, fmt.Errorf("load classified people: %w", err)
	}
	personIDs := make([]int64, 0, len(bindings))
	for personID := range bindings {
		personIDs = append(personIDs, personID)
	}
	slices.Sort(personIDs)
	if err := queryInChunksContext(ctx, tx, personIDs, nil, `
		SELECT person_id, participant_id FROM person_participants WHERE person_id IN (%s)`,
		func(rows *loggedRows) error {
			var personID, participantID int64
			if err := rows.Scan(&personID, &participantID); err != nil {
				return fmt.Errorf("scan classified person binding: %w", err)
			}
			bindings[personID] = append(bindings[personID], participantID)
			return nil
		}); err != nil {
		return nil, fmt.Errorf("load classified person bindings: %w", err)
	}
	result := []int64{}
	for _, personID := range personIDs {
		allHidden := len(bindings[personID]) > 0
		for _, participantID := range bindings[personID] {
			if row, ok := hidden[participantID]; !ok || !hidesSavedPerson(row.kind) {
				allHidden = false
				break
			}
		}
		if allHidden {
			result = append(result, personID)
		}
	}
	return result, nil
}

// notPersonAddressesContext returns the lowercased email addresses and the
// normalized phone numbers of the given not-a-person participants, so a
// profile's curated contact points can leave out the same addresses.
func (s *Store) notPersonAddressesContext(
	ctx context.Context, notPerson map[int64]correspondentkind.Kind,
) (map[string]struct{}, map[string]struct{}, error) {
	emails, phones := map[string]struct{}{}, map[string]struct{}{}
	if len(notPerson) == 0 {
		return emails, phones, nil
	}
	ids := make([]int64, 0, len(notPerson))
	for id := range notPerson {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if err := queryInChunksContext(ctx, s.db, ids, nil, `
		SELECT email_address, phone_number FROM participants WHERE id IN (%s)`,
		func(rows *loggedRows) error {
			var email, phone sql.NullString
			if err := rows.Scan(&email, &phone); err != nil {
				return fmt.Errorf("scan not-a-person address: %w", err)
			}
			if value := strings.ToLower(strings.TrimSpace(email.String)); value != "" {
				emails[value] = struct{}{}
			}
			if value := strings.TrimSpace(phone.String); value != "" {
				phones[value] = struct{}{}
				if normalized, err := textimport.NormalizePhone(value); err == nil {
					phones[normalized] = struct{}{}
				}
			}
			return nil
		}); err != nil {
		return nil, nil, fmt.Errorf("load not-a-person addresses: %w", err)
	}
	return emails, phones, nil
}

// PersonIsNotAPersonContext reports whether every archive identity bound to
// the person is classified as an organization or ignored. People-oriented
// consumers such as enrichment skip such a profile.
func (s *Store) PersonIsNotAPersonContext(ctx context.Context, personID int64) (bool, error) {
	found := false
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		ids, err := s.notPeoplePersonIDsTx(ctx, tx)
		if err != nil {
			return err
		}
		found = slices.Contains(ids, personID)
		return nil
	})
	return found, err
}
