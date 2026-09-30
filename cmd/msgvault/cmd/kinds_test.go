package cmd

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/store"
)

func TestKindsBuildAppliesRulesInsideTheDaemon(t *testing.T) {
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
	require.NoError(st.AddAccountIdentity(source.ID, "owner@example.com", "manual"))
	conversation, err := st.EnsureConversation(source.ID, "kinds", "Kinds")
	require.NoError(err)
	owner, err := st.EnsureParticipant("owner@example.com", "Owner Example", "example.com")
	require.NoError(err)
	receipts, err := st.EnsureParticipant("noreply@shop.example.com", "Example Shop", "shop.example.com")
	require.NoError(err)
	for i := range 5 {
		id, err := st.UpsertMessage(&store.Message{
			ConversationID: conversation, SourceID: source.ID, SourceMessageID: fmt.Sprintf("receipt-%d", i),
			MessageType: "email", SenderID: sql.NullInt64{Int64: receipts, Valid: true},
			SentAt:  sql.NullTime{Time: time.Date(2026, 4, 1+i, 9, 0, 0, 0, time.UTC), Valid: true},
			Subject: sql.NullString{String: "Your receipt", Valid: true},
		})
		require.NoError(err)
		require.NoError(st.ReplaceMessageRecipients(id, "to", []int64{owner}, []string{""}))
	}
	require.NoError(st.Close())

	var out bytes.Buffer
	command := newKindsCommand()
	command.SetContext(testCtx)
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs([]string{"build", "--json"})
	require.NoError(command.ExecuteContext(testCtx))
	var report struct {
		Candidates int                            `json:"candidates"`
		Rule       map[correspondentkind.Kind]int `json:"rule"`
		Undecided  int                            `json:"undecided"`
	}
	require.NoError(json.Unmarshal(out.Bytes(), &report), out.String())
	assert.Equal(1, report.Candidates)
	assert.Equal(map[correspondentkind.Kind]int{correspondentkind.Automated: 1}, report.Rule)
	assert.Zero(report.Undecided)

	st, err = store.Open(configuration.DatabaseDSN())
	require.NoError(err)
	defer func() { _ = st.Close() }()
	record, err := st.GetCorrespondentKindContext(testCtx, receipts)
	require.NoError(err)
	assert.Equal(correspondentkind.Automated, record.Kind)

	command = newKindsCommand()
	command.SetContext(testCtx)
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs([]string{"build", "--min-messages=0"})
	require.Error(command.ExecuteContext(testCtx))
}
