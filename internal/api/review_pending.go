package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/msgvault/internal/store"
)

// PendingReviewStore is the feature-local capability for asking whether any
// Reviews queue has an item waiting.
type PendingReviewStore interface {
	PendingReviewKindsContext(ctx context.Context) ([]store.PendingReviewKind, error)
}

// PendingReviewsResponse says whether anything waits in Reviews.
type PendingReviewsResponse struct {
	Pending bool                      `json:"pending" doc:"True when at least one Reviews queue has an item waiting."`
	Kinds   []store.PendingReviewKind `json:"kinds" enum:"identity,enrichment,organization,correspondent" doc:"The queues with an item waiting, in Reviews order."`
}

func (s *Server) registerPendingReviewRoutes(api huma.API) {
	pending := rawAPIV1Operation("getPendingReviews", http.MethodGet,
		"/reviews/pending", "Check whether any review is waiting")
	pending.Description = "Answers whether any Reviews queue has an item waiting, and which: " +
		"identity (open identity match candidates, including contact matches and possible " +
		"duplicate people), enrichment (uncertain enrichment identities), organization " +
		"(organization names to confirm), and correspondent (identities Jev could not classify). " +
		"It reports presence, not counts: each queue costs one indexed lookup, so a client can " +
		"poll it for a navigation hint. The correspondent check works per identity, so it can " +
		"report a queue whose only item an identity cluster decision already settled."
	pending.Responses = jsonResponsesFor[PendingReviewsResponse](api)
	addErrorResponses(api, pending.Responses, http.StatusServiceUnavailable)
	registerRawHumaRoute(api, pending, s.handleGetPendingReviews)
}

func (s *Server) handleGetPendingReviews(w http.ResponseWriter, r *http.Request) {
	reviews, ok := s.store.(PendingReviewStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "pending_reviews_unavailable",
			"Checking for pending reviews is unavailable")
		return
	}
	kinds, err := reviews.PendingReviewKindsContext(r.Context())
	if err != nil {
		s.logger.Error("pending review check failed", "error", err)
		writeError(w, http.StatusInternalServerError, "pending_reviews_failed",
			"Could not check for pending reviews")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, PendingReviewsResponse{Pending: len(kinds) > 0, Kinds: kinds})
}
