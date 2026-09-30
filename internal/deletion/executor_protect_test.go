package deletion

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

// protectArchive archives three Gmail messages and stages them in a v2
// manifest, optionally with protect.
func protectArchive(t *testing.T, c *TestContext, protect bool) (*Manifest, int64, map[string]int64) {
	t.Helper()
	require := require.New(t)
	source, err := c.Store.GetOrCreateSource("gmail", "owner@example.com")
	require.NoError(err)
	conversation, err := c.Store.EnsureConversation(source.ID, "protect", "Protect")
	require.NoError(err)
	ids := []string{"gm-a", "gm-b", "gm-c"}
	messages := map[string]int64{}
	for _, providerID := range ids {
		id, err := c.Store.UpsertMessage(&store.Message{
			ConversationID: conversation, SourceID: source.ID, SourceMessageID: providerID, MessageType: "email",
			Subject: sql.NullString{String: "Sale " + providerID, Valid: true},
		})
		require.NoError(err)
		messages[providerID] = id
	}
	manifest := NewManifestForSource("protect test", ids,
		SourceReference{ID: source.ID, Type: "gmail", Identifier: "owner@example.com"})
	manifest.Protect = protect
	require.NoError(c.Mgr.SaveManifest(manifest))
	return manifest, source.ID, messages
}

// starAfterStaging stars gm-b, as a sync would after the batch was staged.
func starAfterStaging(t *testing.T, c *TestContext, sourceID, messageID int64) {
	t.Helper()
	require := require.New(t)
	starred, err := c.Store.EnsureLabel(sourceID, "STARRED", "STARRED", "system")
	require.NoError(err)
	require.NoError(c.Store.AddMessageLabels(messageID, []int64{starred}))
}

func TestProtectBatchSkipsAMessageStarredAfterStaging(t *testing.T) {
	for _, tt := range []struct {
		name    string
		execute func(c *TestContext, id string) error
		deleted func(c *TestContext) []string
	}{
		{
			name:    "trash",
			execute: func(c *TestContext, id string) error { return c.ExecuteWithOpts(id, trashOpts(2)) },
			deleted: func(c *TestContext) []string { return c.MockAPI.TrashCalls },
		},
		{
			name:    "batch delete",
			execute: func(c *TestContext, id string) error { return c.ExecuteBatch(id) },
			deleted: func(c *TestContext) []string {
				all := []string{}
				for _, batch := range c.MockAPI.BatchDeleteCalls {
					all = append(all, batch...)
				}
				return all
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			c := NewTestContext(t)
			manifest, sourceID, messages := protectArchive(t, c, true)
			starAfterStaging(t, c, sourceID, messages["gm-b"])

			require.NoError(tt.execute(c, manifest.ID))

			assert.Equal([]string{"gm-a", "gm-c"}, tt.deleted(c), "the starred message is never sent to Gmail")
			done, _, err := c.Mgr.GetManifest(manifest.ID)
			require.NoError(err)
			require.NotNil(done.Execution)
			assert.Equal([]string{"gm-b"}, done.Execution.ProtectedIDs)
			assert.Equal(2, done.Execution.Succeeded)
			assert.Zero(done.Execution.Failed)
			assert.Contains(done.FormatSummary(), "Skipped as protected: 1")

			id := messages["gm-b"]
			protections, err := c.Store.DeletionProtectionsContext(context.Background(), []int64{id})
			require.NoError(err)
			assert.True(protections[id].Starred)
		})
	}
}

func TestBatchWithoutProtectDeletesStarredMessages(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	c := NewTestContext(t)
	manifest, sourceID, messages := protectArchive(t, c, false)
	starAfterStaging(t, c, sourceID, messages["gm-b"])

	require.NoError(c.ExecuteWithOpts(manifest.ID, trashOpts(2)))

	assert.Equal([]string{"gm-a", "gm-b", "gm-c"}, c.MockAPI.TrashCalls)
	done, _, err := c.Mgr.GetManifest(manifest.ID)
	require.NoError(err)
	assert.Empty(done.Execution.ProtectedIDs)
}
