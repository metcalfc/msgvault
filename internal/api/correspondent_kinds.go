package api

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

// CorrespondentKindStore is the feature-local capability for marking
// identity clusters as not a person.
type CorrespondentKindStore interface {
	SetCorrespondentKindContext(
		ctx context.Context, input store.SetCorrespondentKindInput,
	) (*store.SetCorrespondentKindResult, error)
	GetCorrespondentKindContext(ctx context.Context, participantID int64) (*store.CorrespondentKindRecord, error)
	ListCorrespondentKindsContext(
		ctx context.Context, filter store.CorrespondentKindListFilter,
	) ([]store.CorrespondentKindRecord, error)
	CorrespondentKindsForParticipantsContext(
		ctx context.Context, participantIDs []int64,
	) (map[int64]store.CorrespondentKindAssignment, error)
}

// SetCorrespondentKindRequest classifies an identity cluster.
type SetCorrespondentKindRequest struct {
	Kind             correspondentkind.Kind `json:"kind" enum:"person,organization,shared_mailbox,ignored,automated,mailing_list" doc:"person clears the classification (\"this is a person\"); organization, shared_mailbox, ignored, automated, and mailing_list mark the cluster as not a person."`
	OrganizationID   *int64                 `json:"organization_id,omitzero" nullable:"false" doc:"Organization to group the cluster under. Only for kind organization."`
	OrganizationName *string                `json:"organization_name,omitzero" nullable:"false" doc:"Name of the organization to find or create. Only for kind organization; defaults to the cluster's display name, then its email domain."`
}

// CorrespondentKindsResponse lists classified identity clusters.
type CorrespondentKindsResponse struct {
	Records []store.CorrespondentKindRecord `json:"records"`
}

const correspondentKindDescription = "A correspondent kind says whether an archive identity " +
	"cluster is a person. organization groups its messages under an Organization and attaches " +
	"its email addresses as organization contact points; shared_mailbox keeps it for messages " +
	"while the people who wrote from it keep their own profiles; automated marks a machine " +
	"sender; mailing_list marks a list or group address; ignored hides a record the user " +
	"does not need. A user decision always outranks rule and jev classifications. Every kind " +
	"other than person leaves the cluster out of contact matching and enrichment; every kind " +
	"except shared_mailbox also leaves People lists and relationship rankings. A user decision " +
	"resolves the cluster's open identity match candidates with reason not_a_person. Messages " +
	"stay searchable. Setting person restores everything. Saved people are never deleted here: " +
	"the response names a profile that exists only for this cluster so a client can offer an " +
	"explicit delete."

func (s *Server) registerCorrespondentKindRoutes(api huma.API) {
	list := rawAPIV1Operation("listCorrespondentKinds", http.MethodGet,
		"/identity/correspondent-kinds", "List identity clusters marked as not a person")
	list.Description = "Lists every identity cluster whose effective kind is not a person, " +
		"newest first, with its addresses, organization, and any saved person bound to it. " +
		"kind=unclear lists the Jev judgments waiting for review instead: they carry the Jev " +
		"probabilities and are never included without that filter. " + correspondentKindDescription
	list.Responses = jsonResponsesFor[CorrespondentKindsResponse](api)
	addErrorResponses(api, list.Responses, http.StatusServiceUnavailable)
	registerRawHumaRoute(api, list, s.handleListCorrespondentKinds)

	get := rawAPIV1Operation("getCorrespondentKind", http.MethodGet,
		"/identity/correspondent-kinds/{id}", "Get the correspondent kind of a participant's cluster")
	get.Description = "Returns the effective classification of the cluster containing the " +
		"participant; kind person with no source means it was never classified."
	get.Responses = jsonResponsesFor[store.CorrespondentKindRecord](api)
	addErrorResponses(api, get.Responses, http.StatusNotFound, http.StatusServiceUnavailable)
	registerRawHumaRoute(api, get, s.handleGetCorrespondentKind)

	set := rawAPIV1Operation("setCorrespondentKind", http.MethodPut,
		"/identity/correspondent-kinds/{id}", "Mark a participant's cluster as a person or not a person")
	set.Description = correspondentKindDescription
	set.RequestBody = jsonRequestBodyFor[SetCorrespondentKindRequest](api)
	set.Responses = jsonResponsesFor[store.SetCorrespondentKindResult](api)
	addErrorResponses(api, set.Responses, http.StatusConflict, http.StatusNotFound,
		http.StatusServiceUnavailable)
	registerRawHumaRoute(api, set, s.handleSetCorrespondentKind)

	clearKind := rawAPIV1Operation("clearCorrespondentKind", http.MethodDelete,
		"/identity/correspondent-kinds/{id}", "Mark a participant's cluster as a person again")
	clearKind.Description = "Equivalent to setting kind person: the cluster returns to People " +
		"lists, rankings, matching, and enrichment, and candidates resolved as not a person " +
		"return to review. remove_organization_id undoes an organization classification " +
		"completely: when the cluster was grouped under that organization and nothing else " +
		"refers to it (employments, merges, other classified clusters, active contact points, " +
		"profile data, attributes, aliases, reviews, or fact decisions), the organization is " +
		"deleted and organization_removed is true. Otherwise it is kept."
	clearKind.Responses = jsonResponsesFor[store.SetCorrespondentKindResult](api)
	addErrorResponses(api, clearKind.Responses, http.StatusNotFound, http.StatusServiceUnavailable)
	registerRawHumaRoute(api, clearKind, s.handleClearCorrespondentKind)
}

func (s *Server) correspondentKindStore(w http.ResponseWriter) (CorrespondentKindStore, bool) {
	kinds, ok := s.store.(CorrespondentKindStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "correspondent_kinds_unavailable",
			"Marking records as not a person is unavailable")
	}
	return kinds, ok
}

func correspondentKindParticipantID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_participant_id",
			"Participant ID must be a positive integer")
		return 0, false
	}
	return id, true
}

func (s *Server) handleListCorrespondentKinds(w http.ResponseWriter, r *http.Request) {
	kinds, ok := s.correspondentKindStore(w)
	if !ok {
		return
	}
	filter := store.CorrespondentKindListFilter{
		Kind: correspondentkind.Kind(strings.TrimSpace(r.URL.Query().Get("kind"))),
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("organization_id")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_organization_id",
				"organization_id must be a positive integer")
			return
		}
		filter.OrganizationID = &id
	}
	records, err := kinds.ListCorrespondentKindsContext(r.Context(), filter)
	if err != nil {
		s.writeCorrespondentKindError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, CorrespondentKindsResponse{Records: records})
}

func (s *Server) handleGetCorrespondentKind(w http.ResponseWriter, r *http.Request) {
	kinds, ok := s.correspondentKindStore(w)
	if !ok {
		return
	}
	id, ok := correspondentKindParticipantID(w, r)
	if !ok {
		return
	}
	record, err := kinds.GetCorrespondentKindContext(r.Context(), id)
	if err != nil {
		s.writeCorrespondentKindError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) handleSetCorrespondentKind(w http.ResponseWriter, r *http.Request) {
	kinds, ok := s.correspondentKindStore(w)
	if !ok {
		return
	}
	id, ok := correspondentKindParticipantID(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid request body")
		return
	}
	var request SetCorrespondentKindRequest
	decoder := jsontext.NewDecoder(bytes.NewReader(body), json.RejectUnknownMembers(true))
	if err := json.UnmarshalDecode(decoder, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON request body: "+err.Error())
		return
	}
	if !requireSingleJSONValue(w, decoder, "invalid_request") {
		return
	}
	s.setCorrespondentKind(w, r, kinds, store.SetCorrespondentKindInput{
		ParticipantID: id, Kind: request.Kind,
		OrganizationID: request.OrganizationID, OrganizationName: request.OrganizationName,
		Actor: string(store.ProvenanceUser),
	})
}

func (s *Server) handleClearCorrespondentKind(w http.ResponseWriter, r *http.Request) {
	kinds, ok := s.correspondentKindStore(w)
	if !ok {
		return
	}
	id, ok := correspondentKindParticipantID(w, r)
	if !ok {
		return
	}
	input := store.SetCorrespondentKindInput{
		ParticipantID: id, Kind: correspondentkind.Person, Actor: string(store.ProvenanceUser),
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("remove_organization_id")); raw != "" {
		organizationID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || organizationID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_organization_id",
				"remove_organization_id must be a positive integer")
			return
		}
		input.RemoveOrganizationID = &organizationID
	}
	s.setCorrespondentKind(w, r, kinds, input)
}

func (s *Server) setCorrespondentKind(
	w http.ResponseWriter, r *http.Request, kinds CorrespondentKindStore, input store.SetCorrespondentKindInput,
) {
	result, err := kinds.SetCorrespondentKindContext(r.Context(), input)
	if err != nil {
		s.writeCorrespondentKindError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) writeCorrespondentKindError(w http.ResponseWriter, err error) {
	if s.writeIfContextError(w, err) {
		return
	}
	switch {
	case errors.Is(err, store.ErrParticipantNotFound):
		writeError(w, http.StatusNotFound, "participant_not_found", "Participant not found")
	case errors.Is(err, store.ErrInvalidParticipantID):
		writeError(w, http.StatusBadRequest, "invalid_participant_id", err.Error())
	case errors.Is(err, store.ErrCorrespondentKindInvalid), errors.Is(err, store.ErrOrganizationInvalid):
		writeError(w, http.StatusBadRequest, "invalid_correspondent_kind", err.Error())
	case errors.Is(err, store.ErrOrganizationNotFound):
		writeError(w, http.StatusNotFound, "organization_not_found", "Organization not found")
	case errors.Is(err, store.ErrCorrespondentKindOwner):
		writeError(w, http.StatusConflict, "correspondent_kind_owner",
			"Your own identities are always a person")
	case errors.Is(err, store.ErrCorrespondentKindOrganizationAmbiguous):
		writeError(w, http.StatusConflict, "organization_ambiguous",
			"More than one organization has that name; choose one by id")
	default:
		s.logger.Error("correspondent kind operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "correspondent_kind_failed",
			"Could not update the record")
	}
}

// attachCorrespondentKinds marks each participant summary whose identity
// cluster is classified as not a person. A missing capability or a failed
// lookup leaves the rows unmarked rather than failing the request.
func (s *Server) attachCorrespondentKinds(ctx context.Context, rows []*querySummaryRef) {
	if len(rows) == 0 {
		return
	}
	kinds, ok := s.store.(CorrespondentKindStore)
	if !ok {
		return
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.id)
	}
	found, err := kinds.CorrespondentKindsForParticipantsContext(ctx, ids)
	if err != nil {
		s.logger.Error("correspondent kind lookup failed", "error", err, "participant_count", len(ids))
		return
	}
	for _, row := range rows {
		if assignment, ok := found[row.id]; ok {
			*row.target = &assignment
		}
	}
}

// querySummaryRef points at one summary's correspondent kind field.
type querySummaryRef struct {
	id     int64
	target **store.CorrespondentKindAssignment
}

// SenderKindStore resolves a correspondent kind to the participants whose
// cluster carries it, for sender aggregates filtered by kind.
type SenderKindStore interface {
	ParticipantsWithCorrespondentKindContext(ctx context.Context, kind correspondentkind.Kind) ([]int64, error)
}

// resolveSenderKind applies the sender_kind query parameter: it is valid
// only for the senders view and resolves against the archive's current
// classifications, so the analytical cache never holds a stale kind. It
// writes the error and returns false when the request cannot proceed.
func (s *Server) resolveSenderKind(
	w http.ResponseWriter, r *http.Request, view query.ViewType, opts *query.AggregateOptions,
) bool {
	raw := strings.TrimSpace(r.URL.Query().Get("sender_kind"))
	if raw == "" {
		return true
	}
	kind := correspondentkind.Kind(raw)
	if !kind.Known() || kind == correspondentkind.Person {
		writeError(w, http.StatusBadRequest, "invalid_sender_kind",
			"sender_kind must be organization, shared_mailbox, ignored, automated, mailing_list, or unclear")
		return false
	}
	if view != query.ViewSenders {
		writeError(w, http.StatusBadRequest, "invalid_sender_kind",
			"sender_kind applies only to view_type=senders")
		return false
	}
	kinds, ok := s.store.(SenderKindStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "correspondent_kinds_unavailable",
			"Correspondent kinds are unavailable")
		return false
	}
	ids, err := kinds.ParticipantsWithCorrespondentKindContext(r.Context(), kind)
	if err != nil {
		if s.writeIfContextError(w, err) {
			return false
		}
		s.logger.Error("sender kind lookup failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not read correspondent kinds")
		return false
	}
	if ids == nil {
		ids = []int64{}
	}
	opts.SenderKind = raw
	opts.SenderParticipantIDs = ids
	return true
}
