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
	// MessageID names the message the image belongs to so the daemon can
	// refuse spam and trash. The Web UI always sends it.
	MessageID int64 `json:"message_id,omitzero" doc:"The message the image appears in. The daemon refuses images of spam and trash messages with 403 remote_images_blocked."`
}

// RemoteImagePolicyStore reports whether a message's remote images must
// never be fetched. Implemented by the serve daemon's store adapter.
type RemoteImagePolicyStore interface {
	MessageRemoteImagesBlockedContext(ctx context.Context, messageID int64) (bool, error)
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
	if req.MessageID < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "message_id must be positive")
		return
	}
	if req.MessageID > 0 && !s.remoteImagesAllowed(r.Context(), w, req.MessageID) {
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

// remoteImagesAllowed refuses a message whose labels block remote images.
// It fails closed: a daemon that cannot check never fetches for a message.
func (s *Server) remoteImagesAllowed(ctx context.Context, w http.ResponseWriter, messageID int64) bool {
	policy, ok := s.store.(RemoteImagePolicyStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "remote_image_policy_unavailable",
			"This daemon cannot check whether the message allows remote images")
		return false
	}
	blocked, err := policy.MessageRemoteImagesBlockedContext(ctx, messageID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "Message not found")
		return false
	}
	if err != nil {
		s.logger.Error("remote image policy check failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Remote image policy check failed")
		return false
	}
	if blocked {
		writeError(w, http.StatusForbidden, "remote_images_blocked",
			"Remote images are never loaded for spam or trash messages")
		return false
	}
	return true
}
