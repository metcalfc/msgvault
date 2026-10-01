package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/msgvault/internal/store"
)

// PersonDetachmentStore is the optional store surface for removing archive
// identities from a person and undoing that removal.
type PersonDetachmentStore interface {
	DetachPersonParticipantsContext(
		ctx context.Context, request store.PersonParticipantDetachRequest,
	) (*store.PersonParticipantDetachResult, error)
	ReattachPersonParticipantsContext(
		ctx context.Context, request store.PersonParticipantReattachRequest,
	) (*store.PersonParticipantDetachResult, error)
}

type DetachPersonParticipantsRequest struct {
	ParticipantIDs []int64 `json:"participant_ids" minItems:"1" maxItems:"200"`
}

type ReattachPersonParticipantsRequest struct {
	DetachmentID int64 `json:"detachment_id" minimum:"1"`
}

func (s *Server) registerPersonDetachmentRoutes(api huma.API) {
	detach := rawAPIV1Operation("detachPersonParticipants", http.MethodPost,
		"/people/{id}/participants/detach",
		"Remove archive identities from a person")
	detach.Description = "Unbinds the named participants from the person, cuts their identity " +
		"links to the person's other participants, and records the decision as rejected " +
		"identity match candidates so automatic matching cannot attach them again. " +
		"Messages are unchanged. Undo with POST /people/{id}/participants/reattach."
	addPersonIDParameter(&detach)
	addPersonIfMatchParameter(&detach)
	detach.RequestBody = jsonRequestBodyFor[DetachPersonParticipantsRequest](api)
	detach.Responses = jsonResponsesFor[store.PersonParticipantDetachResult](api)
	addPersonETagHeader(detach.Responses[httpStatusKey(http.StatusOK)])
	addErrorResponses(api, detach.Responses, http.StatusBadRequest, http.StatusConflict,
		http.StatusNotFound, http.StatusPreconditionRequired, http.StatusInternalServerError,
		http.StatusServiceUnavailable)
	registerRawHumaRoute(api, detach, s.handleDetachPersonParticipants)

	reattach := rawAPIV1Operation("reattachPersonParticipants", http.MethodPost,
		"/people/{id}/participants/reattach",
		"Undo a removal of archive identities from a person")
	reattach.Description = "Reverses one detachment: rebinds its participants, restores the " +
		"identity links it cut, returns the identity match candidates it rejected to their " +
		"earlier decisions, and deletes the rejections it created."
	addPersonIDParameter(&reattach)
	addPersonIfMatchParameter(&reattach)
	reattach.RequestBody = jsonRequestBodyFor[ReattachPersonParticipantsRequest](api)
	reattach.Responses = jsonResponsesFor[store.PersonParticipantDetachResult](api)
	addPersonETagHeader(reattach.Responses[httpStatusKey(http.StatusOK)])
	addErrorResponses(api, reattach.Responses, http.StatusBadRequest, http.StatusConflict,
		http.StatusNotFound, http.StatusPreconditionRequired, http.StatusInternalServerError,
		http.StatusServiceUnavailable)
	registerRawHumaRoute(api, reattach, s.handleReattachPersonParticipants)
}

func (s *Server) personDetachmentStore(w http.ResponseWriter) (PersonDetachmentStore, bool) {
	detachments, ok := s.store.(PersonDetachmentStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "persons_unavailable", "Person profiles are unavailable")
	}
	return detachments, ok
}

func (s *Server) handleDetachPersonParticipants(w http.ResponseWriter, r *http.Request) {
	detachments, ok := s.personDetachmentStore(w)
	if !ok {
		return
	}
	personID, ok := personProfileID(w, r)
	if !ok {
		return
	}
	var body DetachPersonParticipantsRequest
	if !decodeEntityRequest(w, r, &body, "person detach") {
		return
	}
	if len(body.ParticipantIDs) == 0 || len(body.ParticipantIDs) > store.MaxPersonParticipantDetachIDs {
		writeError(w, http.StatusBadRequest, "invalid_participant_id",
			"participant_ids must name 1 to 200 participants")
		return
	}
	revision, ok := personIfMatch(w, r, personID)
	if !ok {
		return
	}
	result, err := detachments.DetachPersonParticipantsContext(r.Context(),
		store.PersonParticipantDetachRequest{
			PersonID: personID, ParticipantIDs: body.ParticipantIDs,
			ExpectedRevision: revision, Actor: apiPersonMergeActor,
		})
	if err != nil {
		s.writePersonDetachmentError(w, err)
		return
	}
	s.writePersonDetachmentResult(w, r, result)
}

func (s *Server) handleReattachPersonParticipants(w http.ResponseWriter, r *http.Request) {
	detachments, ok := s.personDetachmentStore(w)
	if !ok {
		return
	}
	personID, ok := personProfileID(w, r)
	if !ok {
		return
	}
	var body ReattachPersonParticipantsRequest
	if !decodeEntityRequest(w, r, &body, "person reattach") {
		return
	}
	if body.DetachmentID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_detachment_id",
			"detachment_id must be a positive integer")
		return
	}
	revision, ok := personIfMatch(w, r, personID)
	if !ok {
		return
	}
	result, err := detachments.ReattachPersonParticipantsContext(r.Context(),
		store.PersonParticipantReattachRequest{
			PersonID: personID, DetachmentID: body.DetachmentID,
			ExpectedRevision: revision, Actor: apiPersonMergeActor,
		})
	if err != nil {
		s.writePersonDetachmentError(w, err)
		return
	}
	s.writePersonDetachmentResult(w, r, result)
}

func (s *Server) writePersonDetachmentResult(
	w http.ResponseWriter, r *http.Request, result *store.PersonParticipantDetachResult,
) {
	// The binding change has committed; refresh identity datasets so the
	// next people query sees it, keeping the result if the refresh fails.
	result.CacheState = s.refreshIdentityCacheState(r.Context())
	w.Header().Set(etagHeaderName, personETag(result.Person))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) writePersonDetachmentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrPersonParticipantNotBound):
		writeError(w, http.StatusConflict, "person_participant_not_bound",
			"That identity is no longer part of this person; reload and retry")
	case errors.Is(err, store.ErrPersonDetachmentNotFound):
		writeError(w, http.StatusNotFound, "person_detachment_not_found",
			"Person identity removal not found")
	case errors.Is(err, store.ErrPersonDetachmentReattached):
		writeError(w, http.StatusConflict, "person_detachment_reattached",
			"That removal was already undone")
	case errors.Is(err, store.ErrPersonBindingConflict):
		writeError(w, http.StatusConflict, "person_binding_conflict",
			"The identity now belongs to another person")
	default:
		s.writePersonError(w, err)
	}
}
