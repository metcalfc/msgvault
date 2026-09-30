package api

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"go.kenn.io/msgvault/internal/cleanupsuggest"
	"go.kenn.io/msgvault/internal/deletion"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

// stageDeletionSampleSize caps the dry-run Gmail-ID preview.
const stageDeletionSampleSize = 10

// deletionMessageIDResolver is the optional engine capability for
// resolving internal message IDs to Gmail IDs. SQLite/DuckDB engines
// implement it; the daemonclient HTTP engine does not need to.
type deletionMessageIDResolver interface {
	GetDeletionTargetsByMessageIDs(ctx context.Context, ids []int64) ([]query.DeletionTarget, error)
}

// DeletionManifestLister lists staged deletion manifests. Implemented by
// the serve daemon's store adapter; status "" means all statuses.
type DeletionManifestLister interface {
	ListDeletionManifests(ctx context.Context, status deletion.Status) ([]*deletion.Manifest, error)
}

// DeletionManifestCanceller resolves and cancels staged deletion
// manifests. GetDeletionManifest returns the directory-derived status;
// not-found errors wrap deletion.ErrManifestNotFound.
type DeletionManifestCanceller interface {
	GetDeletionManifest(ctx context.Context, id string) (*deletion.Manifest, deletion.Status, error)
	CancelDeletionManifest(ctx context.Context, id string) error
}

// DeletionProtectionStore reports which messages about to be staged are
// starred, sent by the owner, or from a sender classified as a person.
// Implemented by the serve daemon's store adapter.
type DeletionProtectionStore interface {
	DeletionProtectionsContext(ctx context.Context, messageIDs []int64) (map[int64]store.DeletionProtection, error)
}

// DeletionKeepCandidateStore finds staged messages whose stored cleanup
// suggestion says they look like personal or work mail. Implemented by the
// serve daemon's store adapter.
type DeletionKeepCandidateStore interface {
	KeepCandidatesForSourceMessagesContext(
		ctx context.Context, sourceID int64, sourceMessageIDs []string, minKeep float64,
	) ([]store.CleanupSuggestionRow, error)
}

// deletionKeepCandidateLimit caps the possibly-worth-keeping messages a
// manifest detail lists.
const deletionKeepCandidateLimit = 50

// deletionProtectionSampleSize caps the protected message IDs a staging
// response names.
const deletionProtectionSampleSize = 20

// DeletionProtectionSummary counts the staged candidates that deserve a
// second look before deletion. A message can have several reasons, so the
// reason counts may add up to more than ProtectedCount.
type DeletionProtectionSummary struct {
	ProtectedCount   int     `json:"protected_count" doc:"Candidate messages with at least one protection reason."`
	Starred          int     `json:"starred" doc:"Candidate messages that are starred."`
	OwnerSent        int     `json:"owner_sent" doc:"Candidate messages the archive owner sent."`
	PersonSender     int     `json:"person_sender" doc:"Candidate messages from a sender classified as a person (a user decision, or a Jev individual_person probability of at least 0.60)."`
	Skipped          bool    `json:"skipped" doc:"True when protect was requested and these messages were left out of the batch."`
	SampleMessageIDs []int64 `json:"sample_message_ids,omitempty" doc:"Up to 20 protected message IDs, lowest first."`
}

// StageDeletionFilter selects messages to stage. All fields optional,
// but the effective request must contain at least one criterion.
type StageDeletionFilter struct {
	Sender        string `json:"sender,omitempty"`
	SenderName    string `json:"sender_name,omitempty"`
	Recipient     string `json:"recipient,omitempty"`
	RecipientName string `json:"recipient_name,omitempty"`
	Domain        string `json:"domain,omitempty"`
	Label         string `json:"label,omitempty"`
	ListID        string `json:"list_id,omitempty"`
	SourceID      *int64 `json:"source_id,omitzero" nullable:"false"`
	After         string `json:"after,omitempty"`
	Before        string `json:"before,omitempty"`
}

func (f *StageDeletionFilter) isEmpty() bool {
	return f == nil || (f.Sender == "" && f.SenderName == "" && f.Recipient == "" &&
		f.RecipientName == "" && f.Domain == "" && f.Label == "" && f.ListID == "" &&
		f.SourceID == nil && f.After == "" && f.Before == "")
}

func (f *StageDeletionFilter) toMessageFilter() (query.MessageFilter, *apiHTTPError) {
	var mf query.MessageFilter
	mf.Sender = f.Sender
	mf.SenderName = f.SenderName
	mf.Recipient = f.Recipient
	mf.RecipientName = f.RecipientName
	mf.Domain = f.Domain
	mf.Label = f.Label
	mf.ListID = f.ListID
	mf.SourceID = f.SourceID
	if f.After != "" {
		ts, err := parseAPITime(f.After)
		if err != nil {
			return mf, newAPIHTTPError(http.StatusBadRequest, "invalid_date",
				fmt.Sprintf("filter field %q must be an RFC3339 or YYYY-MM-DD date, got %q", "after", f.After))
		}
		mf.After = &ts
	}
	if f.Before != "" {
		ts, err := parseAPITime(f.Before)
		if err != nil {
			return mf, newAPIHTTPError(http.StatusBadRequest, "invalid_date",
				fmt.Sprintf("filter field %q must be an RFC3339 or YYYY-MM-DD date, got %q", "before", f.Before))
		}
		mf.Before = &ts
	}
	return mf, nil
}

// StageDeletionRequest is the POST /api/v1/deletions body.
type StageDeletionRequest struct {
	Filter         *StageDeletionFilter `json:"filter,omitzero" nullable:"false"`
	MessageIDs     []int64              `json:"message_ids,omitempty"`
	Selection      *ExploreSelection    `json:"selection,omitzero" nullable:"false"`
	OperationToken string               `json:"operation_token,omitempty"`
	Description    string               `json:"description,omitempty"`
	DryRun         bool                 `json:"dry_run,omitzero"`
	Protect        bool                 `json:"protect,omitzero" doc:"Leave starred, owner-sent, and person-sent messages out of the batch instead of only reporting them."`
}

// StageDeletionResponse covers both dry-run (200) and create (201).
// MessageCount is the staged subset. MatchedCount and SkippedCount are
// reported for reviewed selections, where the match set may contain items
// no source supports deleting.
type StageDeletionResponse struct {
	DryRun         bool                      `json:"dry_run"`
	MessageCount   int                       `json:"message_count"`
	MatchedCount   int                       `json:"matched_count,omitzero"`
	SkippedCount   int                       `json:"skipped_count,omitzero"`
	Account        string                    `json:"account,omitempty"`
	SampleGmailIDs []string                  `json:"sample_gmail_ids,omitempty"`
	ID             string                    `json:"id,omitempty"`
	Status         string                    `json:"status,omitempty"`
	Source         *deletion.SourceReference `json:"source,omitzero" nullable:"false"`
	// Protection is present whenever the daemon could check the candidates.
	Protection *DeletionProtectionSummary `json:"protection,omitzero" nullable:"false" doc:"Starred, owner-sent, and person-sent candidates. Without protect they are staged and only reported."`
}

// DeletionManifestSummary is one row of GET /api/v1/deletions.
type DeletionManifestSummary struct {
	ID           string    `json:"id"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	CreatedBy    string    `json:"created_by"`
	Description  string    `json:"description"`
	MessageCount int       `json:"message_count"`
}

// ListDeletionsResponse is the GET /api/v1/deletions body.
type ListDeletionsResponse struct {
	Manifests []DeletionManifestSummary `json:"manifests"`
}

type DeletionManifestDetail struct {
	ID           string                    `json:"id"`
	Status       string                    `json:"status"`
	CreatedAt    time.Time                 `json:"created_at"`
	CreatedBy    string                    `json:"created_by"`
	Description  string                    `json:"description"`
	Account      string                    `json:"account,omitempty"`
	MessageCount int                       `json:"message_count"`
	Summary      *deletion.Summary         `json:"summary,omitzero" nullable:"false"`
	Execution    *deletion.Execution       `json:"execution,omitzero" nullable:"false"`
	Source       *deletion.SourceReference `json:"source,omitzero" nullable:"false"`
	// PossiblyWorthKeeping lists staged messages whose cleanup suggestion
	// scored personal plus work at or above 0.50, most likely first.
	PossiblyWorthKeeping      []deletion.KeepCandidate `json:"possibly_worth_keeping,omitempty" doc:"Up to 50 staged messages that a cleanup suggestion found likely to be personal or work mail (personal plus work at least 0.50), most likely first."`
	PossiblyWorthKeepingCount int                      `json:"possibly_worth_keeping_count,omitzero" doc:"How many staged messages are possibly worth keeping, including any beyond the listed 50."`
}

// CancelDeletionResponse is the DELETE /api/v1/deletions/{id} body.
type CancelDeletionResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (s *Server) handleStageDeletion(w http.ResponseWriter, r *http.Request) {
	if s.queryEngineForContext(r.Context()) == nil {
		writeError(w, http.StatusServiceUnavailable, "engine_unavailable", "Query engine not available")
		return
	}
	saver, ok := s.store.(CLIDeletionManifestSaver)
	if !ok {
		writeAPIHTTPError(w, cliStoreUnavailableError())
		return
	}

	// Reject unknown fields: a typo in a narrowing filter key would
	// otherwise be silently dropped while a remaining broad criterion
	// stages far more messages than intended.
	dec := jsontext.NewDecoder(r.Body, json.RejectUnknownMembers(true))

	var req StageDeletionRequest
	if err := json.UnmarshalDecode(dec, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request",
			fmt.Sprintf("invalid JSON request body: %v", err))
		return
	}
	if !requireSingleJSONValue(w, dec, "invalid_request") {
		return
	}
	if req.Selection != nil && (!req.Filter.isEmpty() || len(req.MessageIDs) > 0) {
		writeError(w, http.StatusBadRequest, "invalid_request", "selection cannot be combined with filter or message_ids")
		return
	}
	if req.Filter.isEmpty() && len(req.MessageIDs) == 0 {
		if req.Selection == nil {
			writeError(w, http.StatusBadRequest, "empty_filter",
				"At least one filter criterion or message_ids entry is required; staging the entire archive is not supported")
			return
		}
	}
	if req.Selection == nil && !req.Filter.isEmpty() && !req.DryRun {
		writeError(w, http.StatusPreconditionRequired, "preflight_required",
			"Deletion staging requires the exact selection and operation token returned by preflight")
		return
	}

	var targets []query.DeletionTarget
	var claim *deletionOperationClaim
	var matched int
	if req.Selection != nil {
		var ok bool
		targets, claim, matched, ok = s.resolveAuthorizedDeletionSelection(w, r, *req.Selection, req.OperationToken)
		if !ok {
			return
		}
	} else {
		var httpErr *apiHTTPError
		targets, httpErr = s.resolveStageDeletionTargets(r.Context(), &req)
		if httpErr != nil {
			writeAPIHTTPError(w, httpErr)
			return
		}
	}
	if len(targets) == 0 {
		writeError(w, http.StatusBadRequest, "no_messages_matched", "No messages matched the given criteria")
		return
	}
	// Unsupported matches are counted before protection narrows the batch,
	// so skipped_count keeps meaning "cannot be deleted from its source".
	unsupported := max(matched-len(targets), 0)
	targets, protection, httpErr := s.applyDeletionProtection(r.Context(), targets, req.Protect)
	if httpErr != nil {
		writeAPIHTTPError(w, httpErr)
		return
	}

	source, httpErr := sourceReferenceForTargets(targets)
	if httpErr != nil {
		writeAPIHTTPError(w, httpErr)
		return
	}
	account := source.Identifier
	gmailIDs := deletion.SourceMessageIDs(targets)

	if req.DryRun {
		sample := gmailIDs
		if len(sample) > stageDeletionSampleSize {
			sample = sample[:stageDeletionSampleSize]
		}
		writeJSON(w, http.StatusOK, StageDeletionResponse{
			DryRun:         true,
			MessageCount:   len(gmailIDs),
			MatchedCount:   matched,
			SkippedCount:   unsupported,
			Account:        account,
			Source:         source,
			SampleGmailIDs: sample,
			Protection:     protection,
		})
		return
	}

	description := strings.TrimSpace(req.Description)
	if description == "" {
		description = "staged via API"
	}
	manifest := deletion.NewManifestForSource(description, gmailIDs, *source)
	manifest.CreatedBy = "api"
	manifest.Protect = req.Protect
	manifest.Filters = manifestFiltersFromRequest(req.Filter)
	// delete-staged selects the mailbox to execute against from
	// Filters.Account; without it an API-staged manifest cannot be
	// executed (or worse, could be forced onto the wrong account with
	// --account).
	manifest.Filters.Account = account
	raw, err := json.Marshal(req, json.Deterministic(true))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request is not serializable")
		return
	}
	manifest.RawFilter = raw

	// All deterministic validation is done; claim the one-shot token
	// only now, immediately before persistence. Reservation (not
	// deletion) keeps concurrent reuse of the token impossible while
	// still allowing a retry if persistence fails below.
	if claim != nil {
		if !claim.reserve() {
			writeError(w, http.StatusConflict, "operation_token_invalid",
				"Deletion preflight was already used or expired; run preflight again")
			return
		}
	}
	if err := saver.SaveCLIDeletionManifest(r.Context(), manifest); err != nil {
		if claim != nil {
			claim.rollback()
		}
		s.logger.Error("failed to save staged deletion manifest", "id", manifest.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "stage_deletion_failed", "Failed to save deletion manifest")
		return
	}
	if claim != nil {
		claim.commit()
	}
	writeJSON(w, http.StatusCreated, StageDeletionResponse{
		MessageCount: len(gmailIDs),
		MatchedCount: matched,
		SkippedCount: unsupported,
		Account:      account,
		ID:           manifest.ID,
		Status:       string(manifest.Status),
		Source:       source,
		Protection:   protection,
	})
}

// applyDeletionProtection counts the protected candidates and, when protect
// is set, leaves them out. Without a store that can check, nothing is
// reported, and protect is refused rather than silently ignored.
func (s *Server) applyDeletionProtection(
	ctx context.Context, targets []query.DeletionTarget, protect bool,
) ([]query.DeletionTarget, *DeletionProtectionSummary, *apiHTTPError) {
	checker, ok := s.store.(DeletionProtectionStore)
	if !ok {
		if protect {
			return nil, nil, newAPIHTTPError(http.StatusServiceUnavailable, "protection_unavailable",
				"This daemon cannot check deletion protection; stage without protect or upgrade the daemon")
		}
		return targets, nil, nil
	}
	ids := make([]int64, len(targets))
	for i, target := range targets {
		ids[i] = target.MessageID
	}
	protections, err := checker.DeletionProtectionsContext(ctx, ids)
	if err != nil {
		s.logger.Error("deletion protection check failed", "error", err)
		return nil, nil, newAPIHTTPError(http.StatusInternalServerError, "internal_error", "Deletion protection check failed")
	}
	summary := &DeletionProtectionSummary{Skipped: protect}
	kept := make([]query.DeletionTarget, 0, len(targets))
	for _, target := range targets {
		protection, found := protections[target.MessageID]
		if !found || !protection.Protected() {
			kept = append(kept, target)
			continue
		}
		summary.ProtectedCount++
		if protection.Starred {
			summary.Starred++
		}
		if protection.OwnerSent {
			summary.OwnerSent++
		}
		if protection.PersonSender {
			summary.PersonSender++
		}
		summary.SampleMessageIDs = append(summary.SampleMessageIDs, target.MessageID)
		if !protect {
			kept = append(kept, target)
		}
	}
	slices.Sort(summary.SampleMessageIDs)
	if len(summary.SampleMessageIDs) > deletionProtectionSampleSize {
		summary.SampleMessageIDs = summary.SampleMessageIDs[:deletionProtectionSampleSize]
	}
	if len(kept) == 0 {
		return nil, nil, newAPIHTTPError(http.StatusConflict, "all_messages_protected",
			"Every matched message is starred, sent by you, or from a person; protect leaves nothing to stage")
	}
	return kept, summary, nil
}

func sourceReferenceForTargets(targets []query.DeletionTarget) (*deletion.SourceReference, *apiHTTPError) {
	source, err := deletion.SourceReferenceForTargets(targets)
	switch {
	case err == nil:
		return &source, nil
	case errors.Is(err, deletion.ErrNoDeletionTargets):
		return nil, newAPIHTTPError(http.StatusConflict, "source_resolution_conflict", "selection has no deletion targets")
	case errors.Is(err, deletion.ErrIncompleteDeletionSource):
		return nil, newAPIHTTPError(http.StatusConflict, "source_resolution_conflict", "selected message has incomplete source provenance")
	case errors.Is(err, deletion.ErrMultipleDeletionSources):
		return nil, newAPIHTTPError(http.StatusBadRequest, "multi_account_selection",
			"selection spans multiple sources; scope the request with filter.source_id or stage each source separately")
	default:
		return nil, newAPIHTTPError(http.StatusConflict, "source_resolution_conflict", err.Error())
	}
}

// deletionOperationClaim defers one-shot consumption of a preflight
// operation token until the staged manifest has persisted. reserve
// atomically claims the grant (concurrent requests with the same token
// fail immediately), commit invalidates it permanently, and rollback
// restores it so a failed persistence attempt can be retried.
type deletionOperationClaim struct {
	state         *exploreServerState
	token         string
	selectionHash string
}

func (c *deletionOperationClaim) reserve() bool {
	_, ok := c.state.reserveOperation(c.token, c.selectionHash)
	return ok
}

func (c *deletionOperationClaim) commit() { c.state.commitOperation(c.token) }

func (c *deletionOperationClaim) rollback() { c.state.rollbackOperation(c.token) }

// resolveAuthorizedDeletionSelection validates the reviewed selection
// against the preflight grant without consuming it; the returned claim
// lets the caller reserve/commit/rollback the token around manifest
// persistence. The returned targets are the deletable subset of the
// reviewed match set, and the returned count is the full match set so the
// caller can report what was left out.
func (s *Server) resolveAuthorizedDeletionSelection(
	w http.ResponseWriter,
	r *http.Request,
	selection ExploreSelection,
	token string,
) ([]query.DeletionTarget, *deletionOperationClaim, int, bool) {
	if token == "" {
		writeError(w, http.StatusPreconditionRequired, "preflight_required",
			"operation_token from deletion preflight is required")
		return nil, nil, 0, false
	}
	if selection.Mode != "explicit" && selection.Mode != "all_matching" {
		writeError(w, http.StatusBadRequest, "invalid_selection", "selection mode must be explicit or all_matching")
		return nil, nil, 0, false
	}
	predicate, err := s.prepareResolvedExplorePredicate(r.Context(), selection.Predicate)
	if err != nil {
		s.writeExploreFilterError(w, err, "invalid_selection_predicate")
		return nil, nil, 0, false
	}
	if selection.CacheRevision == "" {
		writeError(w, http.StatusBadRequest, "invalid_selection", "cache_revision is required")
		return nil, nil, 0, false
	}
	if predicate.request.SearchMode == exploreSearchModeSemantic || predicate.request.SearchMode == exploreSearchModeHybrid {
		if selection.CandidateSnapshotID == "" {
			writeError(w, http.StatusBadRequest, "candidate_snapshot_required",
				"Semantic and hybrid deletion staging require the server-issued candidate snapshot")
			return nil, nil, 0, false
		}
		predicate.request.CandidateSnapshotID = selection.CandidateSnapshotID
	}
	searchSpec, _, resolved := s.resolveExploreSearch(r.Context(), w, predicate.request)
	if !resolved || !requireCompleteCandidatePool(w, searchSpec) {
		return nil, nil, 0, false
	}
	predicate.query.Search = searchSpec
	selectionRequest := query.ExploreSelectionRequest{
		Explore: predicate.query, ExcludedKeys: selection.Exclusions, IncludeDeletableMessageIDs: true,
	}
	if selection.Mode == "explicit" {
		if selection.RowKeys == nil {
			writeError(w, http.StatusBadRequest, "invalid_selection", "explicit selection requires row_keys")
			return nil, nil, 0, false
		}
		selectionRequest.IncludedKeys = selection.RowKeys
	}
	engine := s.queryEngineForContext(r.Context())
	analyzer, ok := engine.(query.Explorer)
	if !ok {
		s.writeExploreUnavailable(r.Context(), w, query.CacheAbsent)
		return nil, nil, 0, false
	}
	stats, err := analyzer.ExploreSelectionStats(r.Context(), selectionRequest)
	if err != nil {
		s.writeExploreError(r.Context(), w, err)
		return nil, nil, 0, false
	}
	if selection.CacheRevision != stats.CacheRevision {
		writeError(w, http.StatusConflict, "archive_revision_changed", "The committed analytical cache changed; run preflight again")
		return nil, nil, 0, false
	}
	if searchSpec.Mode != query.SearchNone && !sameSearchProvenance(selection.SearchProvenance, stats.SearchProvenance) {
		writeError(w, http.StatusConflict, "search_revision_changed", "The search index revision changed; run preflight again")
		return nil, nil, 0, false
	}
	selectionHash := hashCanonicalValue(&selection, false)
	state := s.exploreState
	if state == nil {
		writeError(w, http.StatusConflict, "operation_token_invalid", "Deletion preflight expired; run preflight again")
		return nil, nil, 0, false
	}
	grant, authorized := state.operation(token, selectionHash)
	if !authorized || grant.Revision != stats.CacheRevision || grant.Count != stats.Count {
		writeError(w, http.StatusConflict, "operation_token_invalid", "Deletion preflight expired or no longer matches; run preflight again")
		return nil, nil, 0, false
	}
	if stats.Count == 0 {
		writeError(w, http.StatusBadRequest, "no_messages_matched", "No messages matched the reviewed selection")
		return nil, nil, 0, false
	}
	// A mixed selection stages its deletable subset instead of failing as a
	// whole. Only a selection with nothing deletable is a dead end.
	if stats.DeletableCount == 0 {
		writeError(w, http.StatusConflict, "selection_not_deletable",
			"No item in the reviewed selection can be deleted from its source")
		return nil, nil, 0, false
	}
	resolver, ok := engine.(deletionMessageIDResolver)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "engine_unavailable",
			"selection deletion staging is not supported by this query engine")
		return nil, nil, 0, false
	}
	targets, err := resolver.GetDeletionTargetsByMessageIDs(r.Context(), stats.DeletableMessageIDs)
	if err != nil {
		s.logger.Error("stage deletion selection resolution failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Gmail ID query failed")
		return nil, nil, 0, false
	}
	if int64(len(targets)) != stats.DeletableCount {
		writeError(w, http.StatusConflict, "selection_changed",
			"The reviewed selection changed before staging; run preflight again")
		return nil, nil, 0, false
	}
	claim := &deletionOperationClaim{state: state, token: token, selectionHash: selectionHash}
	return targets, claim, int(stats.Count), true
}

// resolveStageDeletionTargets unions filter-resolved and explicitly selected
// rows without flattening away source provenance.
func (s *Server) resolveStageDeletionTargets(ctx context.Context, req *StageDeletionRequest) ([]query.DeletionTarget, *apiHTTPError) {
	engine := s.queryEngineForContext(ctx)
	var out []query.DeletionTarget
	seen := make(map[int64]struct{})
	appendTargets := func(targets []query.DeletionTarget) {
		for _, target := range targets {
			if _, dup := seen[target.MessageID]; dup {
				continue
			}
			seen[target.MessageID] = struct{}{}
			out = append(out, target)
		}
	}

	if !req.Filter.isEmpty() {
		mf, httpErr := req.Filter.toMessageFilter()
		if httpErr != nil {
			return nil, httpErr
		}
		targets, err := engine.GetDeletionTargetsByFilter(ctx, mf)
		if err != nil {
			s.logger.Error("stage deletion filter query failed", "error", err)
			return nil, newAPIHTTPError(http.StatusInternalServerError, "internal_error", "Gmail ID query failed")
		}
		appendTargets(targets)
	}
	if len(req.MessageIDs) > 0 {
		resolver, ok := engine.(deletionMessageIDResolver)
		if !ok {
			return nil, newAPIHTTPError(http.StatusServiceUnavailable, "engine_unavailable",
				"message_ids staging is not supported by this query engine")
		}
		targets, err := resolver.GetDeletionTargetsByMessageIDs(ctx, req.MessageIDs)
		if err != nil {
			s.logger.Error("stage deletion message-id query failed", "error", err)
			return nil, newAPIHTTPError(http.StatusInternalServerError, "internal_error", "Gmail ID query failed")
		}
		appendTargets(targets)
	}
	return out, nil
}

func (s *Server) handleListDeletions(w http.ResponseWriter, r *http.Request) {
	lister, ok := s.store.(DeletionManifestLister)
	if !ok {
		writeAPIHTTPError(w, cliStoreUnavailableError())
		return
	}
	var status deletion.Status
	if raw := r.URL.Query().Get("status"); raw != "" {
		status = deletion.Status(raw)
		if !deletion.IsValidStatus(status) {
			writeError(w, http.StatusBadRequest, "invalid_status",
				"status must be one of pending, in_progress, completed, failed, cancelled")
			return
		}
	}
	manifests, err := lister.ListDeletionManifests(r.Context(), status)
	if err != nil {
		s.logger.Error("failed to list deletion manifests", "error", err)
		writeError(w, http.StatusInternalServerError, "list_deletions_failed", "Failed to list deletion manifests")
		return
	}
	summaries := make([]DeletionManifestSummary, 0, len(manifests))
	for _, m := range manifests {
		summaries = append(summaries, DeletionManifestSummary{
			ID:           m.ID,
			Status:       string(m.Status),
			CreatedAt:    m.CreatedAt,
			CreatedBy:    m.CreatedBy,
			Description:  m.Description,
			MessageCount: len(m.GmailIDs),
		})
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		return summaries[i].CreatedAt.After(summaries[j].CreatedAt)
	})
	writeJSON(w, http.StatusOK, ListDeletionsResponse{Manifests: summaries})
}

func (s *Server) handleGetDeletion(w http.ResponseWriter, r *http.Request) {
	canceller, ok := s.store.(DeletionManifestCanceller)
	if !ok {
		writeAPIHTTPError(w, cliStoreUnavailableError())
		return
	}
	id := r.PathValue("id")
	if err := deletion.ValidateManifestID(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_manifest_id", err.Error())
		return
	}
	manifest, status, err := canceller.GetDeletionManifest(r.Context(), id)
	if errors.Is(err, deletion.ErrManifestNotFound) {
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("deletion manifest %q not found", id))
		return
	}
	if err != nil {
		s.logger.Error("failed to load deletion manifest", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load deletion manifest")
		return
	}
	detail := DeletionManifestDetail{
		ID: manifest.ID, Status: string(status), CreatedAt: manifest.CreatedAt,
		CreatedBy: manifest.CreatedBy, Description: manifest.Description,
		Account: manifest.Filters.Account, MessageCount: len(manifest.GmailIDs),
		Summary: manifest.Summary, Execution: manifest.Execution, Source: manifest.Source,
	}
	candidates, err := s.deletionKeepCandidates(r.Context(), manifest)
	if err != nil {
		s.logger.Error("failed to load possibly worth keeping messages", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load deletion review")
		return
	}
	detail.PossiblyWorthKeepingCount = len(candidates)
	if len(candidates) > deletionKeepCandidateLimit {
		candidates = candidates[:deletionKeepCandidateLimit]
	}
	detail.PossiblyWorthKeeping = candidates
	writeJSON(w, http.StatusOK, detail)
}

// deletionKeepCandidates lists the manifest's staged messages that look like
// personal or work mail. A manifest without a source reference, or a store
// that cannot look, has none.
func (s *Server) deletionKeepCandidates(ctx context.Context, manifest *deletion.Manifest) ([]deletion.KeepCandidate, error) {
	finder, ok := s.store.(DeletionKeepCandidateStore)
	if !ok || manifest.Source == nil || manifest.Source.ID <= 0 {
		return nil, nil
	}
	rows, err := finder.KeepCandidatesForSourceMessagesContext(
		ctx, manifest.Source.ID, manifest.GmailIDs, cleanupsuggest.KeepThreshold)
	if err != nil {
		return nil, err
	}
	return KeepCandidatesFromSuggestions(rows), nil
}

// KeepCandidatesFromSuggestions converts stored suggestions for display.
func KeepCandidatesFromSuggestions(rows []store.CleanupSuggestionRow) []deletion.KeepCandidate {
	candidates := make([]deletion.KeepCandidate, 0, len(rows))
	for _, row := range rows {
		from := row.FromEmail
		if row.FromName != "" && row.FromEmail != "" {
			from = row.FromName + " <" + row.FromEmail + ">"
		} else if from == "" {
			from = row.FromName
		}
		candidates = append(candidates, deletion.KeepCandidate{
			MessageID: row.MessageID, SourceMessageID: row.SourceMessageID, From: from,
			Subject: row.Subject, KeepProbability: row.KeepProbability,
		})
	}
	return candidates
}

func (s *Server) handleCancelDeletion(w http.ResponseWriter, r *http.Request) {
	canceller, ok := s.store.(DeletionManifestCanceller)
	if !ok {
		writeAPIHTTPError(w, cliStoreUnavailableError())
		return
	}
	id := r.PathValue("id")
	if err := deletion.ValidateManifestID(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_manifest_id", err.Error())
		return
	}
	_, status, err := canceller.GetDeletionManifest(r.Context(), id)
	if errors.Is(err, deletion.ErrManifestNotFound) {
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("deletion manifest %q not found", id))
		return
	}
	if err != nil {
		s.logger.Error("failed to load deletion manifest", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load deletion manifest")
		return
	}
	if status != deletion.StatusPending && status != deletion.StatusInProgress {
		writeError(w, http.StatusConflict, "not_cancellable",
			fmt.Sprintf("deletion manifest %q has status %q and cannot be cancelled", id, status))
		return
	}
	if err := canceller.CancelDeletionManifest(r.Context(), id); err != nil {
		s.logger.Error("failed to cancel deletion manifest", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "cancel_deletion_failed", "Failed to cancel deletion manifest")
		return
	}
	writeJSON(w, http.StatusOK, CancelDeletionResponse{ID: id, Status: string(deletion.StatusCancelled)})
}

// manifestFiltersFromRequest maps the request fields that
// deletion.Filters can represent; RawFilter preserves the rest.
func manifestFiltersFromRequest(f *StageDeletionFilter) deletion.Filters {
	var out deletion.Filters
	if f == nil {
		return out
	}
	if f.Sender != "" {
		out.Senders = []string{f.Sender}
	}
	if f.Domain != "" {
		out.SenderDomains = []string{f.Domain}
	}
	if f.Recipient != "" {
		out.Recipients = []string{f.Recipient}
	}
	if f.Label != "" {
		out.Labels = []string{f.Label}
	}
	if f.ListID != "" {
		out.ListIDs = []string{f.ListID}
	}
	out.After = f.After
	out.Before = f.Before
	return out
}
