package cmd

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

func TestSuggestCleanupListsSuggestionsAndNeverStages(t *testing.T) {
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
	spam, err := st.EnsureLabel(source.ID, "SPAM", "SPAM", "system")
	require.NoError(err)
	bank, err := st.EnsureParticipant("alerts@bank.example", "Example Bank", "bank.example")
	require.NoError(err)
	ids := []int64{}
	for i := range 2 {
		conversation, err := st.EnsureConversation(source.ID, fmt.Sprintf("cleanup-%d", i), "Cleanup")
		require.NoError(err)
		id, err := st.UpsertMessage(&store.Message{
			ConversationID: conversation, SourceID: source.ID, SourceMessageID: fmt.Sprintf("cleanup-%d", i),
			MessageType: "email", SenderID: sql.NullInt64{Int64: bank, Valid: true},
			Subject: sql.NullString{String: fmt.Sprintf("Account locked %d", i), Valid: true},
		})
		require.NoError(err)
		require.NoError(st.AddMessageLabels(id, []int64{spam}))
		require.NoError(st.UpsertMessageBody(id, sql.NullString{String: "https://login.bank-verify.example/x", Valid: true}, sql.NullString{}))
		ids = append(ids, id)
	}
	_, err = st.WriteCleanupSuggestionsContext(testCtx, []store.CleanupSuggestion{{
		MessageID: ids[0], Score: 0.92, Impersonation: 0.9, Pressure: 0.8, Category: "phishing_or_scam",
		CategoryProbabilities: map[string]float64{"phishing_or_scam": 0.9}, Signals: []string{"dmarc_fail"},
		Model: "jev-test",
	}})
	require.NoError(err)
	require.NoError(st.Close())

	var out bytes.Buffer
	command := newSuggestCleanupCommand()
	command.SetContext(testCtx)
	command.SetOut(&out)
	command.SetErr(&out)
	require.NoError(command.ExecuteContext(testCtx))

	text := out.String()
	assert.Contains(text, "Pool messages read: 1 (0 from people and 0 without links left out)")
	assert.Contains(text, "Jev: off; 1 message(s) are ready to judge")
	assert.Contains(text, fmt.Sprintf("  %d  0.92  phishing_or_scam  Example Bank <alerts@bank.example>  \"Account locked 0\" [dmarc_fail]", ids[0]))
	assert.Contains(text, fmt.Sprintf("Nothing was staged. To stage these after review: msgvault stage-delete --ids %d", ids[0]))

	command = newSuggestCleanupCommand()
	command.SetContext(testCtx)
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs([]string{"--limit=0"})
	require.Error(command.ExecuteContext(testCtx))
}
