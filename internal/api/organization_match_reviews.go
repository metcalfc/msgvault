package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/msgvault/internal/store"
)

const (
	organizationMatchReviewDefaultLimit = 50
	organizationMatchReviewMaxLimit     = 200
)

// OrganizationMatchReviewStore is the feature-local capability for deciding
// organization names the organization resolution judgment was unsure about.
type OrganizationMatchReviewStore interface {
	ListOrganizationMatchReviewsContext(ctx context.Context, limit int) ([]store.OrganizationMatchReview, error)
	AcceptOrganizationMatchReviewContext(ctx context.Context, id int64, actor string) (*store.OrganizationMatchDecision, error)
	RejectOrganizationMatchReviewContext(ctx context.Context, id int64, actor string) (*store.OrganizationMatchDecision, error)
}

// OrganizationMatchReviewsResponse lists organization names awaiting a
// decision.
type OrganizationMatchReviewsResponse struct {
	Reviews []store.OrganizationMatchReview `json:"reviews"`
	Limit   int                             `json:"limit"`
}

func (s *Server) registerOrganizationMatchReviewRoutes(api huma.API) {
	list := rawAPIV1Operation("listOrganizationMatchReviews", http.MethodGet,
		"/organization-match-reviews", "List organization names to confirm")
	list.Description = "Organization names the organization resolution judgment found possibly, " +
		"but not confidently, the same as an existing organization, newest first, with the " +
		"stored probability and model."
	list.Responses = jsonResponsesFor[OrganizationMatchReviewsResponse](api)
	addErrorResponses(api, list.Responses, http.StatusServiceUnavailable)
	registerRawHumaRoute(api, list, s.handleListOrganizationMatchReviews)

	accept := rawAPIV1Operation("acceptOrganizationMatchReview", http.MethodPost,
		"/organization-match-reviews/{id}/accept", "Confirm an organization match")
	accept.Description = "The user confirms the proposed name is the existing organization. A " +
		"separate organization already created for the name is merged into it, and the " +
		"organization answers to the name and domain from now on."
	accept.Responses = jsonResponsesFor[store.OrganizationMatchDecision](api)
	addErrorResponses(api, accept.Responses, http.StatusConflict, http.StatusNotFound,
		http.StatusServiceUnavailable)
	registerRawHumaRoute(api, accept, s.handleAcceptOrganizationMatchReview)

	reject := rawAPIV1Operation("rejectOrganizationMatchReview", http.MethodPost,
		"/organization-match-reviews/{id}/reject", "Reject an organization match")
	reject.Description = "The user says the proposed name is a different organization. The " +
		"existing organization is never proposed for that name again."
	reject.Responses = jsonResponsesFor[store.OrganizationMatchDecision](api)
	addErrorResponses(api, reject.Responses, http.StatusConflict, http.StatusNotFound,
		http.StatusServiceUnavailable)
	registerRawHumaRoute(api, reject, s.handleRejectOrganizationMatchReview)
}

func (s *Server) organizationMatchReviewStore(w http.ResponseWriter) (OrganizationMatchReviewStore, bool) {
	reviews, ok := s.store.(OrganizationMatchReviewStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "organization_reviews_unavailable",
			"Organization match review is unavailable")
	}
	return reviews, ok
}

func (s *Server) handleListOrganizationMatchReviews(w http.ResponseWriter, r *http.Request) {
	reviews, ok := s.organizationMatchReviewStore(w)
	if !ok {
		return
	}
	limit, _, err := queryInt(r, "limit")
	if err != nil {
		s.rejectBadParam(w, err)
		return
	}
	switch {
	case limit <= 0:
		limit = organizationMatchReviewDefaultLimit
	case limit > organizationMatchReviewMaxLimit:
		limit = organizationMatchReviewMaxLimit
	}
	listed, err := reviews.ListOrganizationMatchReviewsContext(r.Context(), limit)
	if err != nil {
		s.writeOrganizationMatchReviewError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, OrganizationMatchReviewsResponse{Reviews: listed, Limit: limit})
}

func (s *Server) handleAcceptOrganizationMatchReview(w http.ResponseWriter, r *http.Request) {
	s.decideOrganizationMatchReview(w, r, true)
}

func (s *Server) handleRejectOrganizationMatchReview(w http.ResponseWriter, r *http.Request) {
	s.decideOrganizationMatchReview(w, r, false)
}

func (s *Server) decideOrganizationMatchReview(w http.ResponseWriter, r *http.Request, accept bool) {
	reviews, ok := s.organizationMatchReviewStore(w)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_review_id",
			"Organization match review ID must be a positive integer")
		return
	}
	decide := reviews.RejectOrganizationMatchReviewContext
	if accept {
		decide = reviews.AcceptOrganizationMatchReviewContext
	}
	// An HTTP decision is always an explicit user action.
	decision, err := decide(r.Context(), id, "user")
	if err != nil {
		s.writeOrganizationMatchReviewError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, decision)
}

func (s *Server) writeOrganizationMatchReviewError(w http.ResponseWriter, err error) {
	if s.writeIfContextError(w, err) {
		return
	}
	switch {
	case errors.Is(err, store.ErrOrganizationMatchReviewNotFound):
		writeError(w, http.StatusNotFound, "organization_review_not_found",
			"Organization match review not found")
	case errors.Is(err, store.ErrOrganizationMatchReviewStateChanged):
		writeError(w, http.StatusConflict, "organization_review_state_changed",
			"The organization match is no longer awaiting a decision")
	case errors.Is(err, store.ErrOrganizationMatchReviewAmbiguous):
		writeError(w, http.StatusConflict, "organization_review_ambiguous",
			"More than one organization has the proposed name; merge them in the directory first")
	case errors.Is(err, store.ErrOrganizationMergeConflict), errors.Is(err, store.ErrOrganizationRevisionConflict),
		errors.Is(err, store.ErrOrganizationInvalid):
		writeError(w, http.StatusConflict, "organization_review_conflict",
			"The organizations changed or cannot be merged; review them in the directory")
	default:
		s.logger.Error("organization match review failed", "error", err)
		writeError(w, http.StatusInternalServerError, "organization_review_failed",
			"Organization match review failed")
	}
}
