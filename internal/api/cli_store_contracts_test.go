package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/accountops"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/identityops"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestCLIStatsRequiresOnlyItsContextCapabilities(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	_, err := st.GetOrCreateSource("gmail", "archive@example.com")
	require.NoError(err)
	// Expose only the capabilities that scoped statistics actually uses.
	limited := struct {
		MessageStore
		CLIScopeStore
		CLIStatsStore
	}{st, st, st}
	srv := NewServer(&config.Config{}, limited, nil, testLogger())
	t.Cleanup(func() { require.NoError(srv.Shutdown(context.Background())) })
	request := httptest.NewRequest(http.MethodGet, "/api/v1/cli/stats?account=archive@example.com", nil)
	response := httptest.NewRecorder()
	srv.handleCLIStats(response, request)
	assert.Equal(http.StatusOK, response.Code, response.Body.String())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	response = httptest.NewRecorder()
	srv.handleCLIStats(response, request.WithContext(ctx))
	assert.NotEqual(http.StatusOK, response.Code, "scope lookup must honor request cancellation")
}

func TestCLIAccountAndIdentityMutationsHonorCancellation(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("gmail", "archive@example.com")
	require.NoError(err)
	srv := NewServer(&config.Config{}, st, nil, testLogger())
	t.Cleanup(func() { require.NoError(srv.Shutdown(context.Background())) })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = srv.updateCLIAccount(ctx, accountops.UpdateRequest{SourceID: source.ID, DisplayName: "Changed"})
	require.Error(err)
	_, err = srv.addCLIIdentity(ctx, identityops.AddRequest{
		SourceID: source.ID, Identifier: "alias@example.com",
	})
	require.Error(err)
	persisted, err := st.GetSourceByIDContext(t.Context(), source.ID)
	require.NoError(err)
	assert.Equal(source.DisplayName, persisted.DisplayName)
	identities, err := st.ListAccountIdentitiesContext(t.Context(), source.ID)
	require.NoError(err)
	assert.Empty(identities)
}

type blockingCLIIndexStore struct {
	*mockStore

	started chan context.Context
}

func (s *blockingCLIIndexStore) NeedsFTSBackfillContext(ctx context.Context) bool {
	s.started <- ctx
	<-ctx.Done()
	return false
}

func TestCLIIndexEnsureOutlivesRequestAndStopsAtShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		require := require.New(t)
		assert := assert.New(t)
		st := &blockingCLIIndexStore{mockStore: &mockStore{}, started: make(chan context.Context, 1)}
		srv := NewServer(&config.Config{}, st, nil, testLogger())
		defer func() { require.NoError(srv.Shutdown(context.Background())) }()
		requestCtx, cancel := context.WithCancel(t.Context())
		assert.Equal(cliSearchIndexStateChecking, srv.ensureCLISearchIndexAsync(requestCtx, st))
		workerCtx := <-st.started
		cancel()
		require.NoError(workerCtx.Err(), "shared indexing must survive a completed search request")
		require.NoError(srv.Shutdown(t.Context()))
		require.ErrorIs(workerCtx.Err(), context.Canceled)
		assert.False(srv.ftsEnsureRunning.Load(), "shutdown waits for the index worker")
		assert.False(srv.ftsIndexComplete.Load(), "a canceled probe cannot mark the index complete")
		assert.Empty(srv.ensureCLISearchIndexAsync(t.Context(), st), "shutdown cannot launch another worker")
	})
}
