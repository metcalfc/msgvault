package cmd

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/store"
)

func TestCacheBuildClassifiesKindsOnlyWhenAutomatic(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	configuration := lifecycleTestConfig(t.TempDir())
	testCtx := withStoreResolverConfig(t, configuration)
	// A Jev endpoint that counts requests: without consent none may arrive.
	var requests atomic.Int32
	jevServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(jevServer.Close)
	configuration.Jev = jev.DefaultConfig()
	configuration.Jev.Enabled = true
	configuration.Jev.Endpoint = jevServer.URL
	t.Setenv(configuration.Jev.APIKeyEnv, "test-key")

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
	casey, err := st.EnsureParticipant("casey@example.com", "Casey Example", "example.com")
	require.NoError(err)
	for i := range 12 {
		sender := receipts
		if i%2 == 1 {
			sender = casey
		}
		id, err := st.UpsertMessage(&store.Message{
			ConversationID: conversation, SourceID: source.ID, SourceMessageID: fmt.Sprintf("kind-cache-%d", i),
			MessageType: "email", SenderID: sql.NullInt64{Int64: sender, Valid: true},
			SentAt:  sql.NullTime{Time: time.Date(2026, 4, 1+i, 9, 0, 0, 0, time.UTC), Valid: true},
			Subject: sql.NullString{String: "Update", Valid: true},
		})
		require.NoError(err)
		require.NoError(st.ReplaceMessageRecipients(id, "from", []int64{sender}, []string{""}))
		require.NoError(st.ReplaceMessageRecipients(id, "to", []int64{owner}, []string{""}))
	}
	require.NoError(st.Close())
	state := invocationFromContext(testCtx)
	kindOf := func(participant int64) *store.CorrespondentKindRecord {
		st, err := store.Open(configuration.DatabaseDSN())
		require.NoError(err)
		defer func() { _ = st.Close() }()
		record, err := st.GetCorrespondentKindContext(testCtx, participant)
		require.NoError(err)
		return record
	}

	configuration.Jev.CorrespondentKind.Enabled = true
	require.NoError(runBuildCacheLocalMode(buildCacheModeFull, state))
	assert.Nil(kindOf(receipts).Source, "automatic off classifies nothing")

	configuration.Jev.CorrespondentKind.Automatic = true
	// An identity edit waits on the derived refresh, so it judges nothing.
	require.NoError(runBuildCacheLocalMode(buildCacheModeDerived, state))
	assert.Nil(kindOf(receipts).Source, "a derived-only refresh leaves classification to the next build")

	require.NoError(runBuildCacheLocalMode(buildCacheModeDefault, state))
	record := kindOf(receipts)
	assert.Equal(correspondentkind.Automated, record.Kind)
	require.NotNil(record.Source)
	assert.Equal(correspondentkind.SourceRule, *record.Source)
	assert.Nil(kindOf(casey).Source, "what no rule decides waits for a consented Jev")
	assert.Zero(requests.Load(), "Jev without consent sends nothing")
}
