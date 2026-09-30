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
	personEnrichmentReviewDefaultLimit = 50
	personEnrichmentReviewMaxLimit     = 200
)

// PersonEnrichmentIdentityReviewStore is the feature-local capability for
// deciding enrichment attempts whose identity check was uncertain.
type PersonEnrichmentIdentityReviewStore interface {
	ListPersonEnrichmentIdentityReviewsContext(
		ctx context.Context, limit int,
	) ([]store.PersonEnrichmentIdentityReview, error)
	ConfirmPersonEnrichmentIdentityContext(
		ctx context.Context, attemptID int64, actor string,
	) (*store.PersonEnrichmentIdentityDecision, error)
	RejectPersonEnrichmentIdentityContext(
		ctx context.Context, attemptID int64, actor string,
	) (*store.PersonEnrichmentIdentityDecision, error)
}

// PersonEnrichmentIdentityReviewsResponse lists attempts awaiting a decision.
type PersonEnrichmentIdentityReviewsResponse struct {
	Reviews []store.PersonEnrichmentIdentityReview `json:"reviews"`
	Limit   int                                    `json:"limit"`
}

func (s *Server) registerPersonEnrichmentIdentityReviewRoutes(api huma.API) {
	list := rawAPIV1Operation("listPersonEnrichmentIdentityReviews", http.MethodGet,
		"/person-enrichment/identity-reviews", "List enrichment identities to confirm")
	list.Description = "Enrichment attempts whose identity check was uncertain, newest first, " +
		"with the stored judgment probabilities and the returned identity as far as the archive " +
		"kept it. No claim from these attempts has been applied."
	list.Responses = jsonResponsesFor[PersonEnrichmentIdentityReviewsResponse](api)
	addErrorResponses(api, list.Responses, http.StatusServiceUnavailable)
	registerRawHumaRoute(api, list, s.handleListPersonEnrichmentIdentityReviews)

	confirm := rawAPIV1Operation("confirmPersonEnrichmentIdentity", http.MethodPost,
		"/person-enrichment/identity-reviews/{id}/confirm", "Confirm an enrichment identity")
	confirm.Description = "The user confirms the returned identity is this person. The attempt's " +
		"stored claims are applied at the verified identity score, its provider person IDs are " +
		"attached, and the attempt succeeds."
	confirm.Responses = jsonResponsesFor[store.PersonEnrichmentIdentityDecision](api)
	addErrorResponses(api, confirm.Responses, http.StatusConflict, http.StatusNotFound,
		http.StatusServiceUnavailable)
	registerRawHumaRoute(api, confirm, s.handleConfirmPersonEnrichmentIdentity)

	reject := rawAPIV1Operation("rejectPersonEnrichmentIdentity", http.MethodPost,
		"/person-enrichment/identity-reviews/{id}/reject", "Reject an enrichment identity")
	reject.Description = "The user says the returned identity is someone else. The attempt is " +
		"identity-rejected and that provider identity is never proposed for this person again."
	reject.Responses = jsonResponsesFor[store.PersonEnrichmentIdentityDecision](api)
	addErrorResponses(api, reject.Responses, http.StatusConflict, http.StatusNotFound,
		http.StatusServiceUnavailable)
	registerRawHumaRoute(api, reject, s.handleRejectPersonEnrichmentIdentity)
}

func (s *Server) personEnrichmentReviewStore(w http.ResponseWriter) (PersonEnrichmentIdentityReviewStore, bool) {
	reviews, ok := s.store.(PersonEnrichmentIdentityReviewStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "enrichment_reviews_unavailable",
			"Enrichment identity review is unavailable")
	}
	return reviews, ok
}

func (s *Server) handleListPersonEnrichmentIdentityReviews(w http.ResponseWriter, r *http.Request) {
	reviews, ok := s.personEnrichmentReviewStore(w)
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
		limit = personEnrichmentReviewDefaultLimit
	case limit > personEnrichmentReviewMaxLimit:
		limit = personEnrichmentReviewMaxLimit
	}
	listed, err := reviews.ListPersonEnrichmentIdentityReviewsContext(r.Context(), limit)
	if err != nil {
		s.writePersonEnrichmentReviewError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, PersonEnrichmentIdentityReviewsResponse{Reviews: listed, Limit: limit})
}

func (s *Server) handleConfirmPersonEnrichmentIdentity(w http.ResponseWriter, r *http.Request) {
	s.decidePersonEnrichmentIdentity(w, r, true)
}

func (s *Server) handleRejectPersonEnrichmentIdentity(w http.ResponseWriter, r *http.Request) {
	s.decidePersonEnrichmentIdentity(w, r, false)
}

func (s *Server) decidePersonEnrichmentIdentity(w http.ResponseWriter, r *http.Request, confirm bool) {
	reviews, ok := s.personEnrichmentReviewStore(w)
	if !ok {
		return
	}
	attemptID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || attemptID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_attempt_id",
			"Enrichment attempt ID must be a positive integer")
		return
	}
	decide := reviews.RejectPersonEnrichmentIdentityContext
	if confirm {
		decide = reviews.ConfirmPersonEnrichmentIdentityContext
	}
	// An HTTP decision is always an explicit user action.
	decision, err := decide(r.Context(), attemptID, "user")
	if err != nil {
		s.writePersonEnrichmentReviewError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, decision)
}

func (s *Server) writePersonEnrichmentReviewError(w http.ResponseWriter, err error) {
	if s.writeIfContextError(w, err) {
		return
	}
	switch {
	case errors.Is(err, store.ErrPersonEnrichmentReviewNotFound):
		writeError(w, http.StatusNotFound, "enrichment_attempt_not_found",
			"Enrichment attempt not found")
	case errors.Is(err, store.ErrPersonEnrichmentReviewStateChanged):
		writeError(w, http.StatusConflict, "enrichment_review_state_changed",
			"The attempt is no longer awaiting an identity decision")
	case errors.Is(err, store.ErrPersonEnrichmentIdentityOwnedElsewhere):
		writeError(w, http.StatusConflict, "enrichment_identity_owned_elsewhere",
			"This provider identity is already attached to another person")
	case errors.Is(err, store.ErrPersonFactPersonNotTracked):
		writeError(w, http.StatusConflict, "person_not_tracked",
			"Track the person before confirming enrichment claims")
	default:
		s.logger.Error("enrichment identity review failed", "error", err)
		writeError(w, http.StatusInternalServerError, "enrichment_review_failed",
			"Enrichment identity review failed")
	}
}
