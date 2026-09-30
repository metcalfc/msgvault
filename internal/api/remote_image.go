package api

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/netip"

	"go.kenn.io/msgvault/internal/netguard"
	"go.kenn.io/msgvault/internal/remoteimage"
	"go.kenn.io/msgvault/internal/store"
)

const (
	remoteImagePath = "/api/v1/content/remote-image"

	remoteImageMaxRequestBytes = 16 << 10 // JSON body carries one bounded URL
)

// prohibitedRemoteIP retains the proxy's policy-test seam.
func prohibitedRemoteIP(addr netip.Addr) bool { return netguard.ProhibitedIP(addr) }

// RemoteImageRequest is the JSON body of POST /api/v1/content/remote-image.
type RemoteImageRequest struct {
	URL string `json:"url" doc:"Absolute http(s) URL of the consented remote image"`
	// MessageID names the message the image appears in. The proxy fetches
	// only images that message references and never for junk or trash.
	MessageID int64 `json:"message_id" minimum:"1" doc:"The message the image appears in (required). The URL must be an image of that message's stored body: otherwise 403 remote_image_not_referenced. Spam, junk, and trash messages are refused with 403 remote_images_blocked."`
}

// RemoteImagePolicyStore loads a message's remote image policy: whether its
// folder blocks remote images and the bodies its images may come from.
// Implemented by the serve daemon's store adapter.
type RemoteImagePolicyStore interface {
	RemoteImagePolicyContext(ctx context.Context, messageID int64) (store.RemoteImagePolicy, error)
}

// handleRemoteImage serves POST /api/v1/content/remote-image. Success passes
// through only the upstream Content-Type plus the bytes; the API-wide
// Cache-Control: no-store middleware covers caching.
func (s *Server) handleRemoteImage(w http.ResponseWriter, r *http.Request) {
	var req RemoteImageRequest
	decoder := jsontext.NewDecoder(http.MaxBytesReader(w, r.Body, remoteImageMaxRequestBytes))
	if err := json.UnmarshalDecode(decoder, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Request body must be a JSON object with a 'url' field")
		return
	}
	if !requireSingleJSONValue(w, decoder, "invalid_request") {
		return
	}
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "missing_url", "Missing 'url' in request body")
		return
	}
	if req.MessageID <= 0 {
		writeError(w, http.StatusBadRequest, "missing_message_id",
			"Missing 'message_id': remote images are fetched only for the message that references them")
		return
	}
	if !s.remoteImageAllowed(r.Context(), w, req.MessageID, req.URL) {
		return
	}
	fetcher := s.remoteImages
	if fetcher == nil {
		fetcher = remoteimage.NewFetcher()
	}
	ctx, cancel := context.WithTimeout(r.Context(), remoteimage.Timeout)
	defer cancel()
	contentType, body, ferr := fetcher.Fetch(ctx, req.URL)
	if ferr != nil {
		writeError(w, ferr.Status, ferr.Code, ferr.Message)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Not XSS-reachable: readImageBody enforced a non-SVG image/* content
	// type, nosniff pins it, and the frontend consumes the bytes as a blob
	// URL inside a sandboxed srcdoc frame.
	_, _ = w.Write(body)
}

// remoteImageAllowed refuses a message whose folder blocks remote images and
// a URL the message does not reference. It fails closed: a daemon that
// cannot check never fetches.
func (s *Server) remoteImageAllowed(ctx context.Context, w http.ResponseWriter, messageID int64, target string) bool {
	policies, ok := s.store.(RemoteImagePolicyStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "remote_image_policy_unavailable",
			"This daemon cannot check whether the message allows remote images")
		return false
	}
	policy, err := policies.RemoteImagePolicyContext(ctx, messageID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "Message not found")
		return false
	}
	if err != nil {
		s.logger.Error("remote image policy check failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Remote image policy check failed")
		return false
	}
	if policy.Blocked {
		writeError(w, http.StatusForbidden, "remote_images_blocked",
			"Remote images are never loaded for spam, junk, or trash messages")
		return false
	}
	// A URL no <img src> could name falls through to Fetch, whose own
	// validation rejects it before any network use.
	if remoteimage.Referenceable(target) && !remoteimage.Referenced(target, policy.BodyHTML, policy.BodyText) {
		writeError(w, http.StatusForbidden, "remote_image_not_referenced",
			"The message does not reference this image")
		return false
	}
	return true
}
