package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/queryunderstand"
	"go.kenn.io/msgvault/internal/store"
)

// Query understanding statuses.
const (
	queryUnderstandingJudged  = "judged"
	queryUnderstandingSkipped = "skipped"
	queryUnderstandingLate    = "late"
)

// maxQueryUnderstandingPeople bounds the people index rows read per phrase.
const maxQueryUnderstandingPeople = 10

// ExploreQueryUnderstandingRequest is one typed Explore query. The Web UI
// sends it alongside the search itself, only for a query the person typed.
type ExploreQueryUnderstandingRequest struct {
	Query string `json:"query" minLength:"1" maxLength:"2000"`
	// Timezone is the browser's IANA time zone; date phrases are read in
	// it. Empty means UTC.
	Timezone string `json:"timezone,omitempty" maxLength:"64"`
}

// ExploreQuerySuggestion is one filter the query seems to ask for.
type ExploreQuerySuggestion struct {
	Kind  string `json:"kind" enum:"time_window,person,message_type,account"`
	Label string `json:"label"`
	// Span is the exact text of the query the suggestion replaces. Applying
	// the suggestion removes it; empty removes nothing.
	Span string `json:"span,omitempty"`
	// SpanStart and SpanEnd locate Span in the trimmed query, in UTF-16
	// code units (JavaScript string indexes), so a client removes that
	// occurrence and not an earlier copy of the same words.
	SpanStart   int     `json:"span_start" minimum:"0"`
	SpanEnd     int     `json:"span_end" minimum:"0"`
	Probability float64 `json:"probability"`
	// Filters are Explore filters to add; QueryOperators are search
	// operators (from:, to:) to add to the query text.
	Filters        []ExploreFilter `json:"filters" nullable:"false"`
	QueryOperators []string        `json:"query_operators" nullable:"false"`
}

// ExploreQueryUnderstandingResponse reports suggested filters. A skipped or
// late judgment is a normal 200 with no suggestions.
type ExploreQueryUnderstandingResponse struct {
	Status string `json:"status" enum:"judged,skipped,late"`
	// Reason says why nothing was judged: disabled, not_interactive,
	// no_candidates, query_too_long, or a Jev skip category such as
	// consent_required or request_limit.
	Reason      string                   `json:"reason,omitempty"`
	Model       string                   `json:"model,omitempty"`
	Suggestions []ExploreQuerySuggestion `json:"suggestions" nullable:"false"`
	// NaturalLanguage is the probability the query is a natural-language
	// question, when it was asked.
	NaturalLanguage *float64 `json:"natural_language,omitzero" nullable:"false"`
	// OfferHybrid is true when an empty full-text search should offer
	// hybrid search for this query.
	OfferHybrid bool  `json:"offer_hybrid"`
	ElapsedMS   int64 `json:"elapsed_ms"`
}

func (s *Server) registerExploreQueryUnderstandingRoute(api huma.API) {
	op := rawAPIV1Operation("understandExploreQuery", http.MethodPost, "/explore/query-understanding",
		"Suggest Explore filters for a typed query")
	op.Tags = []string{"Exploration"}
	op.Description = "Finds candidate time windows, message types, accounts, and people in a typed query and, " +
		"when [jev.query_understanding] is enabled and consented, asks Jev which of them the query means. " +
		"The whole exchange is bounded to 800 ms; a late judgment is dropped (status late). Only the query " +
		"text and candidate labels leave the machine, with addresses and phone numbers removed. Delegated " +
		"agent callers are always skipped. It never runs the search; call it alongside POST /explore."
	op.RequestBody = jsonRequestBodyFor[ExploreQueryUnderstandingRequest](api)
	op.Responses = jsonResponsesFor[ExploreQueryUnderstandingResponse](api)
	addErrorResponses(api, op.Responses, http.StatusBadRequest)
	registerRawHumaRoute(api, op, s.handleExploreQueryUnderstanding)
}

func (s *Server) handleExploreQueryUnderstanding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	started := time.Now()
	var request ExploreQueryUnderstandingRequest
	if !decodeExploreJSON(w, r, &request) {
		return
	}
	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" {
		writeError(w, http.StatusBadRequest, "invalid_query", "query must not be blank")
		return
	}
	location := time.UTC
	if zone := strings.TrimSpace(request.Timezone); zone != "" {
		loaded, err := time.LoadLocation(zone)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_timezone", "timezone must be an IANA time zone name")
			return
		}
		location = loaded
	}
	respond := func(response ExploreQueryUnderstandingResponse) {
		if response.Suggestions == nil {
			response.Suggestions = []ExploreQuerySuggestion{}
		}
		response.ElapsedMS = time.Since(started).Milliseconds()
		writeJSON(w, http.StatusOK, response)
	}
	skipped := func(reason string) {
		respond(ExploreQueryUnderstandingResponse{Status: queryUnderstandingSkipped, Reason: reason})
	}
	judge := s.queryUnderstanding
	if judge == nil {
		skipped("disabled")
		return
	}
	// An agent may search unattended; only a person's own typed query is
	// judged.
	if s.requestAuthentication(r).Grant != nil {
		skipped("not_interactive")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryunderstand.Budget)
	defer cancel()
	deadline, _ := ctx.Deadline()
	late := func(err error) bool {
		return ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded)
	}
	// Admit checks consent, the credential, and the budget before any
	// archive read, so a disabled or unconsented feature costs nothing.
	if admitter, ok := judge.(interface {
		Admit(ctx context.Context, spec jev.FeatureSpec, automatic bool) (string, error)
	}); ok {
		if _, err := admitter.Admit(ctx, queryunderstand.JevFeature(), false); err != nil {
			skipped(jev.Skipped(err))
			return
		}
	}
	candidates, err := queryunderstand.Generate(ctx, queryunderstand.Input{
		Query:    request.Query,
		Now:      s.clockNow().In(location),
		Accounts: s.queryUnderstandingAccounts(ctx),
		People:   s.queryUnderstandingPeople,
	})
	switch {
	case errors.Is(err, queryunderstand.ErrQueryTooLong):
		skipped("query_too_long")
		return
	case err != nil && late(err):
		respond(ExploreQueryUnderstandingResponse{Status: queryUnderstandingLate, Reason: "timeout"})
		return
	case err != nil:
		s.logger.Warn("explore query understanding: candidates unavailable", "error", err)
		skipped("candidates_unavailable")
		return
	}
	outcome, asked, err := queryunderstand.Understand(ctx, judge, candidates, s.queryUnderstandingAddresses, deadline)
	switch {
	case !asked:
		skipped("no_candidates")
		return
	case err != nil && (late(err) || jev.Skipped(err) == "timeout"):
		respond(ExploreQueryUnderstandingResponse{Status: queryUnderstandingLate, Reason: "timeout"})
		return
	case err != nil:
		skipped(jev.Skipped(err))
		return
	}
	response := ExploreQueryUnderstandingResponse{
		Status: queryUnderstandingJudged, Model: outcome.Model,
		NaturalLanguage: outcome.NaturalLanguage, OfferHybrid: outcome.OfferHybrid(),
		Suggestions: make([]ExploreQuerySuggestion, 0, len(outcome.Suggestions)),
	}
	for _, suggestion := range outcome.Suggestions {
		filters := make([]ExploreFilter, 0, len(suggestion.Filters))
		for _, filter := range suggestion.Filters {
			filters = append(filters, ExploreFilter{Dimension: filter.Dimension, Values: slices.Clone(filter.Values)})
		}
		operators := slices.Clone(suggestion.QueryOperators)
		if operators == nil {
			operators = []string{}
		}
		response.Suggestions = append(response.Suggestions, ExploreQuerySuggestion{
			Kind: suggestion.Kind, Label: suggestion.Label, Span: suggestion.Span,
			SpanStart:   utf16Offset(candidates.Query, suggestion.At.Start),
			SpanEnd:     utf16Offset(candidates.Query, suggestion.At.End),
			Probability: suggestion.Probability, Filters: filters, QueryOperators: operators,
		})
	}
	respond(response)
}

// queryUnderstandingAccounts lists the archived accounts, or none when the
// query engine cannot say.
func (s *Server) queryUnderstandingAccounts(ctx context.Context) []queryunderstand.AccountInput {
	engine := s.queryEngineForContext(ctx)
	if engine == nil {
		return nil
	}
	accounts, err := engine.ListAccounts(ctx)
	if err != nil {
		return nil
	}
	inputs := make([]queryunderstand.AccountInput, 0, len(accounts))
	for _, account := range accounts {
		inputs = append(inputs, queryunderstand.AccountInput{
			SourceID: account.ID, SourceType: account.SourceType,
			Identifier: account.Identifier, DisplayName: account.DisplayName,
		})
	}
	return inputs
}

// queryUnderstandingPeople looks a phrase up in the people completion
// index (observed people merged with curated profiles) and keeps name
// matches. An unavailable index yields no people rather than an error;
// only a cancelled or expired request is an error.
func (s *Server) queryUnderstandingPeople(ctx context.Context, phrase string) ([]queryunderstand.PersonMatch, error) {
	completer, ok := s.queryEngineForContext(ctx).(query.PeopleCompleter)
	if !ok {
		return nil, nil
	}
	observed, err := completer.CompletePeople(ctx, query.PeopleCompletionRequest{
		Query: phrase, Limit: query.MaxPeopleCompletionLimit,
	})
	if err != nil || observed == nil {
		return nil, ctx.Err()
	}
	var curated []store.PersonCompletion
	if profiles, ok := s.store.(PersonCompletionStore); ok {
		curated, err = profiles.CompletePersonProfilesContext(ctx, store.PersonCompletionQuery{
			Query: phrase, Limit: store.MaxPersonCompletionLimit,
		})
		if err != nil {
			return nil, ctx.Err()
		}
	}
	merged, err := s.mergeParticipantCompletions(phrase, observed.Rows, curated)
	if err != nil {
		return nil, ctx.Err()
	}
	matches := make([]queryunderstand.PersonMatch, 0, maxQueryUnderstandingPeople)
	for _, row := range merged {
		if row.Kind != query.PeopleCompletionName || len(matches) >= maxQueryUnderstandingPeople {
			continue
		}
		matches = append(matches, queryunderstand.PersonMatch{ParticipantID: row.ParticipantID, DisplayLabel: row.DisplayLabel})
	}
	return matches, nil
}

// queryUnderstandingAddresses returns the email addresses of a
// participant's identity cluster.
func (s *Server) queryUnderstandingAddresses(ctx context.Context, participantID int64) ([]string, error) {
	identities, ok := s.store.(ParticipantIdentityContextStore)
	if !ok {
		return nil, nil
	}
	members := s.clusterMemberIDs(participantID)
	if len(members) == 0 {
		members = []int64{participantID}
	}
	details, err := identities.GetParticipantIdentityContext(ctx, members)
	if err != nil {
		return nil, err
	}
	addresses := make([]string, 0, len(details.Members))
	for _, member := range details.Members {
		if address := strings.TrimSpace(member.Email); address != "" {
			addresses = append(addresses, address)
		}
	}
	return addresses, nil
}

// utf16Offset converts a byte offset into query into a UTF-16 index.
func utf16Offset(query string, byteOffset int) int {
	if byteOffset <= 0 {
		return 0
	}
	byteOffset = min(byteOffset, len(query))
	return len(utf16.Encode([]rune(query[:byteOffset])))
}
