package cmd

import (
	"context"

	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/store"
)

var (
	_ api.DeletionProtectionStore = (*storeAPIAdapter)(nil)
	_ api.RemoteImagePolicyStore  = (*storeAPIAdapter)(nil)
)

func (a *storeAPIAdapter) DeletionProtectionsContext(
	ctx context.Context, messageIDs []int64,
) (map[int64]store.DeletionProtection, error) {
	return a.store.DeletionProtectionsContext(ctx, messageIDs)
}

func (a *storeAPIAdapter) MessageRemoteImagesBlockedContext(ctx context.Context, messageID int64) (bool, error) {
	return a.store.MessageRemoteImagesBlockedContext(ctx, messageID)
}
