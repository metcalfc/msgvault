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

func (a *storeAPIAdapter) MessageRemoteImagesBlockedContext(ctx context.Context, messageID int64) (bool, error) {
	return a.store.MessageRemoteImagesBlockedContext(ctx, messageID)
}
