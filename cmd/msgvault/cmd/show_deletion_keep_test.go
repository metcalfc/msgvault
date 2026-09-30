package cmd

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/deletion"
	"go.kenn.io/msgvault/internal/store"
)

func TestShowDeletionListsPossiblyWorthKeepingMessages(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	configuration := lifecycleTestConfig(t.TempDir())
	testCtx := withStoreResolverConfig(t, configuration)
	t.Setenv(daemonCLISubprocessEnv, strconv.Itoa(os.Getppid()))

	st, err := store.Open(configuration.DatabaseDSN())
	require.NoError(err)
	require.NoError(st.InitSchema())
	source, err := st.GetOrCreateSource("gmail", "owner@example.com")
	require.NoError(err)
	conversation, err := st.EnsureConversation(source.ID, "keep", "Keep")
	require.NoError(err)
	casey, err := st.EnsureParticipant("casey@example.net", "Casey Example", "example.net")
	require.NoError(err)
	ids := map[string]int64{}
	for _, providerID := range []string{"gm-photos", "gm-sale"} {
		id, err := st.UpsertMessage(&store.Message{
			ConversationID: conversation, SourceID: source.ID, SourceMessageID: providerID, MessageType: "email",
			SenderID: sql.NullInt64{Int64: casey, Valid: true},
			Subject:  sql.NullString{String: "Subject of " + providerID, Valid: true},
		})
		require.NoError(err)
		ids[providerID] = id
	}
	_, err = st.WriteCleanupSuggestionsContext(testCtx, []store.CleanupSuggestion{
		{MessageID: ids["gm-photos"], Category: "personal", Model: "jev-test", KeepProbability: 0.8},
		{MessageID: ids["gm-sale"], Category: "marketing_or_newsletter", Model: "jev-test", KeepProbability: 0.1},
	})
	require.NoError(err)
	require.NoError(st.Close())

	manager, err := deletion.NewManager(filepath.Join(configuration.Data.DataDir, "deletions"))
	require.NoError(err)
	manifest := deletion.NewManifestForSource("sale cleanup", []string{"gm-photos", "gm-sale"},
		deletion.SourceReference{ID: source.ID, Type: "gmail", Identifier: "owner@example.com"})
	require.NoError(manager.SaveManifest(manifest))

	var out bytes.Buffer
	command := &cobra.Command{Use: "show-deletion <batch-id>", Args: cobra.ExactArgs(1), RunE: runShowDeletion}
	command.SetContext(testCtx)
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs([]string{manifest.ID})
	require.NoError(command.ExecuteContext(testCtx))

	text := out.String()
	assert.Contains(text, "Deletion Batch: "+manifest.ID)
	assert.Contains(text, "Possibly worth keeping: 1 staged message(s) look like personal or work mail\n")
	assert.Contains(text, strconv.FormatInt(ids["gm-photos"], 10)+
		"  0.80  Casey Example <casey@example.net>  \"Subject of gm-photos\"\n")
	assert.NotContains(text, "Subject of gm-sale")
}
