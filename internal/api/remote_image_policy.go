package api

import "context"

// RemoteImageBlockStore reports which messages are in spam, junk, or trash
// folders. Implemented by the serve daemon's store adapter.
type RemoteImageBlockStore interface {
	RemoteImagesBlockedMessagesContext(ctx context.Context, messageIDs []int64) (map[int64]bool, error)
}

// attachRemoteImagePolicy marks message details whose remote images the
// reader must not offer. The proxy enforces the same rule, so a lookup
// failure only costs the reader its hint.
func (s *Server) attachRemoteImagePolicy(ctx context.Context, details []MessageDetail) {
	policies, ok := s.store.(RemoteImageBlockStore)
	if !ok || len(details) == 0 {
		return
	}
	ids := make([]int64, len(details))
	for i, detail := range details {
		ids[i] = detail.ID
	}
	blocked, err := policies.RemoteImagesBlockedMessagesContext(ctx, ids)
	if err != nil {
		s.logger.Warn("remote image policy lookup failed", "error", err)
		return
	}
	for i := range details {
		details[i].RemoteImagesBlocked = blocked[details[i].ID]
	}
}
