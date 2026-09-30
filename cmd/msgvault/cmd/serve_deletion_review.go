package cmd

import (
	"context"

	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/store"
)

var (
	_ api.DeletionProtectionStore    = (*storeAPIAdapter)(nil)
	_ api.RemoteImagePolicyStore     = (*storeAPIAdapter)(nil)
	_ api.DeletionKeepCandidateStore = (*storeAPIAdapter)(nil)
	_ api.RemoteImageBlockStore      = (*storeAPIAdapter)(nil)
)

func (a *storeAPIAdapter) KeepCandidatesForSourceMessagesContext(
	ctx context.Context, sourceID int64, sourceMessageIDs []string, minKeep float64,
) ([]store.CleanupSuggestionRow, error) {
	return a.store.KeepCandidatesForSourceMessagesContext(ctx, sourceID, sourceMessageIDs, minKeep)
}

func (a *storeAPIAdapter) DeletionProtectionsContext(
	ctx context.Context, messageIDs []int64,
) (map[int64]store.DeletionProtection, error) {
	return a.store.DeletionProtectionsContext(ctx, messageIDs)
}

func (a *storeAPIAdapter) RemoteImageStateContext(ctx context.Context, messageID int64) (store.RemoteImageState, error) {
	return a.store.RemoteImageStateContext(ctx, messageID)
}

func (a *storeAPIAdapter) RemoteImageBodiesContext(ctx context.Context, messageID int64) (string, string, error) {
	return a.store.RemoteImageBodiesContext(ctx, messageID)
}

func (a *storeAPIAdapter) RemoteImagesBlockedMessagesContext(ctx context.Context, messageIDs []int64) (map[int64]bool, error) {
	return a.store.RemoteImagesBlockedMessagesContext(ctx, messageIDs)
}
