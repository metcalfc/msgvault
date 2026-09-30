package api

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/query"
)

// RelationshipsHTTPRequest scopes and pages a relationship ranking request.
// Filters follow the same predicate shape as /explore; there is no text
// query because ranking is over reciprocity signals, not lexical/semantic
// search candidates.
type RelationshipsHTTPRequest struct {
	Filters []ExploreFilter `json:"filters,omitempty"`
	ShowAll bool            `json:"show_all,omitzero"`
	Cursor  string          `json:"cursor,omitempty"`
	Limit   int             `json:"limit,omitzero" minimum:"0" maximum:"500"`
	Sort    string          `json:"sort,omitempty" enum:"score,last_contact" doc:"Row order: score (the reciprocity ranking, the default) or last_contact (newest last interaction first)."`
	// UnsavedOnly lists only clusters not yet saved to the Directory, so
	// the People list can merge them with saved people without duplicates.
	UnsavedOnly bool `json:"unsaved_only,omitzero" doc:"List only counterparts whose cluster is not bound to a saved Directory person."`
	// IncludeNotPeople keeps clusters that are not known to be people;
	// rankings leave them out by default.
	IncludeNotPeople bool `json:"include_not_people,omitzero" doc:"Include counterparts whose identity cluster is an organization, an automated sender, a mailing list, ignored, or an unclear Jev judgment. They are left out by default: a Jev-classified cluster is ranked only when its individual_person probability is at least 0.60, and a user decision always wins. A shared mailbox is always listed, marked by correspondent_kind."`
}

// RelationshipsHTTPResponse echoes both revisions a page was computed
// against so clients can detect archive or identity drift across pages.
type RelationshipsHTTPResponse struct {
	Rows             []query.RelationshipRow `json:"rows"`
	TotalCount       int64                   `json:"total_count"`
	CacheRevision    string                  `json:"cache_revision"`
	IdentityRevision int64                   `json:"identity_revision"`
	NextCursor       string                  `json:"next_cursor,omitempty"`
}

type RelationshipCalendarHTTPRequest struct {
	Year     int    `json:"year" minimum:"1970"`
	Timezone string `json:"timezone,omitempty"`
}

type RelationshipCalendarHTTPResponse struct {
	*query.RelationshipCalendarResponse

	ParticipantID int64 `json:"participant_id"`
}

func (s *Server) registerRelationshipRoutes(api huma.API) {
	registerExploreRoute[RelationshipsHTTPRequest, RelationshipsHTTPResponse](
		api, "listRelationships", "/relationships", "Rank counterparts by reciprocity-weighted interaction", s.handleRelationships,
	)
	registerExploreRoute[RelationshipTimelineHTTPRequest, RelationshipTimelineHTTPResponse](
		api, "getRelationshipTimeline", "/relationships/{id}/timeline",
		"Get one counterpart's interaction timeline, with chat grouped into local-day bursts", s.handleRelationshipTimeline,
	)
	calendar := rawAPIV1Operation("getRelationshipCalendar", http.MethodPost,
		"/relationships/{id}/calendar",
		"Get one counterpart's timezone-aware relationship calendar")
	calendar.Tags = []string{"Exploration"}
	calendar.RequestBody = jsonRequestBodyFor[RelationshipCalendarHTTPRequest](api)
	calendar.Responses = jsonResponsesFor[RelationshipCalendarHTTPResponse](api)
	addErrorResponses(api, calendar.Responses, http.StatusBadRequest,
		http.StatusNotFound, http.StatusServiceUnavailable)
	calendar.Responses[httpStatusKey(http.StatusServiceUnavailable)] = exploreUnavailableResponseFor(api)
	registerRawHumaRoute(api, calendar, s.handleRelationshipCalendar)
}

func (s *Server) handleRelationshipCalendar(w http.ResponseWriter, r *http.Request) {
	participantID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || participantID < 1 {
		writeError(w, http.StatusBadRequest, "invalid_participant_id",
			"participant ID must be a positive integer")
		return
	}
	var request RelationshipCalendarHTTPRequest
	if !decodeExploreJSON(w, r, &request) {
		return
	}
	engine := s.queryEngineForContext(r.Context())
	resolver, ok := engine.(query.RelationshipCanonicalResolver)
	if !ok {
		s.writeExploreUnavailable(r.Context(), w, query.CacheAbsent)
		return
	}
	analyzer, ok := engine.(query.RelationshipCalendarAnalyzer)
	if !ok {
		s.writeExploreUnavailable(r.Context(), w, query.CacheAbsent)
		return
	}
	canonicalID, err := resolver.ResolveCanonicalParticipant(r.Context(), participantID)
	if err != nil {
		s.writeExploreError(r.Context(), w, err)
		return
	}
	result, err := analyzer.RelationshipCalendar(r.Context(), query.RelationshipCalendarRequest{
		CanonicalID: canonicalID, Year: request.Year, Timezone: request.Timezone,
	})
	if err != nil {
		switch {
		case errors.Is(err, query.ErrInvalidRelationshipYear):
			writeError(w, http.StatusBadRequest, "invalid_year", err.Error())
		case errors.Is(err, query.ErrInvalidRelationshipTimezone):
			writeError(w, http.StatusBadRequest, "invalid_timezone", err.Error())
		case errors.Is(err, query.ErrRelationshipPersonNotFound):
			writeError(w, http.StatusNotFound, "participant_not_found", "Participant cluster not found")
		default:
			s.writeExploreError(r.Context(), w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, RelationshipCalendarHTTPResponse{
		ParticipantID: participantID, RelationshipCalendarResponse: result,
	})
}

func (s *Server) handleRelationships(w http.ResponseWriter, r *http.Request) {
	var request RelationshipsHTTPRequest
	if !decodeExploreJSON(w, r, &request) {
		return
	}
	canonicalizeRelationshipFilters(request.Filters)
	analyticalContext, err := exploreContext(request.Filters)
	if err != nil {
		s.writeExploreFilterError(w, err, "invalid_filter")
		return
	}
	if err := s.resolveExploreIdentityContext(r.Context(), request.Filters, &analyticalContext); err != nil {
		s.writeExploreFilterError(w, err, "invalid_filter")
		return
	}
	if request.Limit == 0 {
		request.Limit = exploreDefaultLimit
	}
	if request.Limit < 1 || request.Limit > exploreMaxLimit {
		writeError(w, http.StatusBadRequest, "invalid_limit", fmt.Sprintf("limit must be between 1 and %d", exploreMaxLimit))
		return
	}
	canonical := request
	canonical.Cursor = ""
	requestHash := hashCanonicalValue(canonical, false)
	offset, ok := s.parseExploreCursor(w, request.Cursor, requestHash)
	if !ok {
		return
	}
	var cursor exploreCursor
	if request.Cursor != "" {
		cursor, _ = s.decodeExploreCursor(request.Cursor)
	}
	decayDate, ok := s.relationshipDecayDate(w, cursor)
	if !ok {
		return
	}

	analyzer, ok := s.queryEngineForContext(r.Context()).(query.RelationshipAnalyzer)
	if !ok {
		s.writeExploreUnavailable(r.Context(), w, query.CacheAbsent)
		return
	}
	if request.Sort != "" && request.Sort != "score" && request.Sort != "last_contact" {
		writeError(w, http.StatusBadRequest, "invalid_sort", "sort must be score or last_contact")
		return
	}
	var exclude map[int64]struct{}
	savedPeople := ""
	if request.UnsavedOnly {
		bound, ok := s.store.(BoundParticipantStore)
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "saved_people_unavailable",
				"Saved people are unavailable, so unsaved contacts cannot be listed")
			return
		}
		ids, err := bound.BoundParticipantIDsContext(r.Context())
		if err != nil {
			s.logger.Error("bound participant lookup failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Could not read saved people")
			return
		}
		exclude = make(map[int64]struct{}, len(ids))
		for _, id := range ids {
			exclude[id] = struct{}{}
		}
		savedPeople = boundParticipantsFingerprint(ids)
		if request.Cursor != "" && cursor.SavedPeople != savedPeople {
			writeError(w, http.StatusConflict, "saved_people_changed",
				"Saved people changed; restart pagination")
			return
		}
	}
	notPeople := ""
	if !request.IncludeNotPeople {
		hidden, fingerprint, ok := s.rankingHiddenParticipantSet(r.Context(), w)
		if !ok {
			return
		}
		if len(hidden) > 0 && exclude == nil {
			exclude = make(map[int64]struct{}, len(hidden))
		}
		for id := range hidden {
			exclude[id] = struct{}{}
		}
		notPeople = fingerprint
		if request.Cursor != "" && cursor.NotPeople != notPeople {
			writeError(w, http.StatusConflict, "not_people_changed",
				"Records marked as not a person changed; restart pagination")
			return
		}
	}
	result, err := analyzer.Relationships(r.Context(), query.RelationshipsRequest{
		Context: analyticalContext, ShowAll: request.ShowAll, Limit: request.Limit, Offset: offset, Now: decayDate,
		SortByLastContact: request.Sort == "last_contact", ExcludeParticipants: exclude,
	})
	if err != nil {
		s.writeExploreError(r.Context(), w, err)
		return
	}
	if request.Cursor != "" {
		// Check identity drift before the archive-revision comparison:
		// CacheSyncState.Revision() folds IdentityRevision into its hash, so
		// any identity-only change (a link/unlink/merge) also changes
		// CacheRevision. Checking archive revision first would make
		// identity_revision_changed unreachable — every identity drift
		// would surface as archive_revision_changed instead.
		if cursor.IdentityRevision != result.IdentityRevision {
			writeError(w, http.StatusConflict, "identity_revision_changed", "Identity clusters changed; restart pagination")
			return
		}
		if cursor.Revision != result.CacheRevision {
			writeError(w, http.StatusConflict, "archive_revision_changed", "The committed analytical cache changed; restart pagination")
			return
		}
	}
	s.attachRelationshipRowProfiles(r.Context(), result.Rows)
	refs := make([]*querySummaryRef, 0, len(result.Rows))
	for i := range result.Rows {
		refs = append(refs, &querySummaryRef{id: result.Rows[i].CanonicalID, target: &result.Rows[i].CorrespondentKind})
	}
	s.attachCorrespondentKinds(r.Context(), refs)
	response := RelationshipsHTTPResponse{
		Rows: result.Rows, TotalCount: result.TotalCount,
		CacheRevision: result.CacheRevision, IdentityRevision: result.IdentityRevision,
	}
	if next := offset + len(result.Rows); next < int(result.TotalCount) {
		response.NextCursor = s.encodeExploreCursor(exploreCursor{
			Offset: next, Request: requestHash, Revision: result.CacheRevision, IdentityRevision: result.IdentityRevision,
			DecayDate: decayDate.Format(time.DateOnly), SavedPeople: savedPeople, NotPeople: notPeople,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

// relationshipDecayDate returns the UTC decay date every page of one
// relationship listing must rank with: midnight UTC of the current date for a
// first page, or the date pinned in the cursor for subsequent pages, so
// pagination crossing UTC midnight cannot re-rank rows mid-listing. The
// indexed ranking depends only on the UTC date of the timestamp, so midnight
// is equivalent to any instant on the same date. Cursors minted before the
// field existed carry no date and fall back to the current date — the prior
// behavior. Cursors are HMAC-signed, so the bounds check below is
// defense-in-depth against server bugs, not against tampering: dates before
// the Unix epoch or more than a day ahead of the clock are rejected.
func (s *Server) relationshipDecayDate(w http.ResponseWriter, cursor exploreCursor) (time.Time, bool) {
	now := s.clockNow().UTC()
	if cursor.DecayDate == "" {
		return now.Truncate(24 * time.Hour), true
	}
	pinned, err := time.Parse(time.DateOnly, cursor.DecayDate)
	if err != nil || pinned.Before(time.Unix(0, 0).UTC()) || pinned.After(now.AddDate(0, 0, 1)) {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "cursor decay date is invalid")
		return time.Time{}, false
	}
	return pinned, true
}

// RelationshipTimelineHTTPRequest scopes and pages one counterpart's
// interaction timeline. Timezone is an IANA name used to bucket chat
// messages into local-day bursts; "" means UTC. The counterpart is fixed by
// the {id} path segment, which accepts any member of that identity's
// cluster; a "participant" filter dimension in Filters further restricts
// entries within that cluster rather than replacing the cluster scope (see
// query.RelationshipTimeline).
type RelationshipTimelineHTTPRequest struct {
	Timezone string          `json:"timezone,omitempty"`
	Filters  []ExploreFilter `json:"filters,omitempty"`
	Cursor   string          `json:"cursor,omitempty"`
	Limit    int             `json:"limit,omitzero" minimum:"0" maximum:"500"`
}

// RelationshipTimelineHTTPResponse echoes the canonical cluster ID the
// {id} path segment resolved to, plus both revisions the page was computed
// against so clients can detect archive or identity drift across pages.
type RelationshipTimelineHTTPResponse struct {
	CanonicalID      int64               `json:"canonical_id"`
	Rows             []query.TimelineRow `json:"rows"`
	TotalCount       int64               `json:"total_count"`
	CacheRevision    string              `json:"cache_revision"`
	IdentityRevision int64               `json:"identity_revision"`
	NextCursor       string              `json:"next_cursor,omitempty"`
}

func (s *Server) handleRelationshipTimeline(w http.ResponseWriter, r *http.Request) {
	participantID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || participantID < 1 {
		writeError(w, http.StatusBadRequest, "invalid_participant_id", "participant ID must be a positive integer")
		return
	}
	var request RelationshipTimelineHTTPRequest
	if !decodeExploreJSON(w, r, &request) {
		return
	}
	canonicalizeRelationshipFilters(request.Filters)
	analyticalContext, err := exploreContext(request.Filters)
	if err != nil {
		s.writeExploreFilterError(w, err, "invalid_filter")
		return
	}
	if err := s.resolveExploreIdentityContext(r.Context(), request.Filters, &analyticalContext); err != nil {
		s.writeExploreFilterError(w, err, "invalid_filter")
		return
	}
	if request.Limit == 0 {
		request.Limit = exploreDefaultLimit
	}
	if request.Limit < 1 || request.Limit > exploreMaxLimit {
		writeError(w, http.StatusBadRequest, "invalid_limit", fmt.Sprintf("limit must be between 1 and %d", exploreMaxLimit))
		return
	}

	analyzer, ok := s.queryEngineForContext(r.Context()).(query.RelationshipAnalyzer)
	if !ok {
		s.writeExploreUnavailable(r.Context(), w, query.CacheAbsent)
		return
	}
	canonicalID, err := analyzer.ResolveCanonicalParticipant(r.Context(), participantID)
	if err != nil {
		s.writeExploreError(r.Context(), w, err)
		return
	}

	canonical := request
	canonical.Cursor = ""
	requestHash := hashCanonicalValue(canonical, false)

	offset := 0
	if request.Cursor != "" {
		cursor, err := s.decodeExploreCursor(request.Cursor)
		if err != nil || cursor.Offset < 0 || cursor.Request != requestHash ||
			cursor.Timezone != request.Timezone || cursor.CanonicalID != canonicalID {
			writeError(w, http.StatusConflict, "cursor_invalidated", "The timeline context changed; restart pagination")
			return
		}
		offset = cursor.Offset
	}

	result, err := analyzer.RelationshipTimeline(r.Context(), query.RelationshipTimelineRequest{
		CanonicalID: canonicalID, Timezone: request.Timezone, Context: analyticalContext,
		Limit: request.Limit, Offset: offset,
	})
	if err != nil {
		s.writeExploreError(r.Context(), w, err)
		return
	}
	if request.Cursor != "" {
		cursor, _ := s.decodeExploreCursor(request.Cursor)
		if cursor.Revision != result.CacheRevision || cursor.IdentityRevision != result.IdentityRevision {
			writeError(w, http.StatusConflict, "cursor_invalidated", "The timeline context changed; restart pagination")
			return
		}
	}
	response := RelationshipTimelineHTTPResponse{
		CanonicalID: canonicalID, Rows: result.Rows, TotalCount: result.TotalCount,
		CacheRevision: result.CacheRevision, IdentityRevision: result.IdentityRevision,
	}
	if next := offset + len(result.Rows); next < int(result.TotalCount) {
		response.NextCursor = s.encodeExploreCursor(exploreCursor{
			Offset: next, Request: requestHash, Revision: result.CacheRevision, IdentityRevision: result.IdentityRevision,
			Timezone: request.Timezone, CanonicalID: canonicalID,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

func canonicalizeRelationshipFilters(filters []ExploreFilter) {
	canonicalizeExploreFilters(filters)
}

// BoundParticipantStore lists every participant bound to a saved Directory
// person, for listings that show only contacts not yet saved.
type BoundParticipantStore interface {
	BoundParticipantIDsContext(ctx context.Context) ([]int64, error)
}

// RankingHiddenParticipantStore reports the participants in clusters that
// relationship rankings leave out by default: organizations, automated
// senders, mailing lists, ignored records, and unclear Jev judgments, each
// under any user decision.
type RankingHiddenParticipantStore interface {
	RankingHiddenParticipantsContext(ctx context.Context) (map[int64]correspondentkind.Kind, error)
}

// rankingHiddenParticipantSet returns every participant in a cluster that
// rankings leave out, with a fingerprint of the set for cursor drift checks.
// Shared mailboxes stay as labelled rows. A store without the capability
// classifies nothing.
func (s *Server) rankingHiddenParticipantSet(
	ctx context.Context, w http.ResponseWriter,
) (map[int64]correspondentkind.Kind, string, bool) {
	kinds, ok := s.store.(RankingHiddenParticipantStore)
	if !ok {
		return nil, "", true
	}
	hidden, err := kinds.RankingHiddenParticipantsContext(ctx)
	if err != nil {
		s.logger.Error("correspondent kind lookup failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not read records marked as not a person")
		return nil, "", false
	}
	for id, kind := range hidden {
		if !kind.LeavesRankings() {
			delete(hidden, id)
		}
	}
	if len(hidden) == 0 {
		return hidden, "", true
	}
	ids := make([]int64, 0, len(hidden))
	for id := range hidden {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return hidden, boundParticipantsFingerprint(ids), true
}

// attachRelationshipRowProfiles marks each ranked row whose cluster has been
// saved to the Directory with that person, so the People list can open the
// saved person instead of a second page for the same human. A cluster is
// bound all-or-none, so its canonical member resolves the person. A missing
// capability or failed lookup leaves rows unmarked rather than failing.
func (s *Server) attachRelationshipRowProfiles(ctx context.Context, rows []query.RelationshipRow) {
	if len(rows) == 0 {
		return
	}
	profiles, ok := s.store.(PersonProfileBatchStore)
	if !ok {
		return
	}
	ids := make([]int64, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].CanonicalID)
		ids = append(ids, rows[i].MemberIDs...)
	}
	found, err := profiles.PersonsForParticipantsContext(ctx, ids)
	if err != nil {
		s.logger.Error("relationship profile lookup failed", "error", err, "participant_count", len(ids))
		return
	}
	for i := range rows {
		person := found[rows[i].CanonicalID]
		for _, id := range rows[i].MemberIDs {
			if person != nil {
				break
			}
			person = found[id]
		}
		if person == nil {
			continue
		}
		rows[i].Profile = &query.PersonProfile{ID: person.ID, DisplayName: person.DisplayName, Revision: person.Revision}
	}
}

// boundParticipantsFingerprint summarizes the set of saved-person bindings
// an unsaved-only listing excluded. The IDs arrive sorted, so equal sets
// produce equal fingerprints.
func boundParticipantsFingerprint(ids []int64) string {
	hash := sha256.New()
	buffer := make([]byte, 8)
	for _, id := range ids {
		binary.BigEndian.PutUint64(buffer, uint64(id))
		hash.Write(buffer)
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}
