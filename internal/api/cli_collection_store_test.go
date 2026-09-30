package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/collectionops"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestCLICollectionRequestsHonorCancellation(t *testing.T) {
	must := require.New(t)
	check := assert.New(t)
	st := testutil.NewTestStore(t)
	first, err := st.GetOrCreateSource("gmail", "first@example.com")
	must.NoError(err)
	second, err := st.GetOrCreateSource("gmail", "second@example.com")
	must.NoError(err)
	_, err = st.CreateCollection("Team", "", []int64{first.ID})
	must.NoError(err)
	srv := NewServer(&config.Config{}, st, nil, testLogger())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	t.Run("list", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/cli/collections", nil).WithContext(ctx)
		response := httptest.NewRecorder()
		srv.handleCLICollections(response, request)
		assert.Equal(t, http.StatusInternalServerError, response.Code)
	})
	t.Run("get", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/cli/collection?name=Team", nil).WithContext(ctx)
		response := httptest.NewRecorder()
		srv.handleCLICollection(response, request)
		assert.Equal(t, http.StatusInternalServerError, response.Code)
	})
	mutations := []struct {
		name string
		run  func() error
	}{
		{"create", func() error {
			_, err := srv.createCLICollection(ctx, collectionops.CreateRequest{Name: "New", Accounts: []string{first.Identifier}})
			return err
		}},
		{"add", func() error {
			_, err := srv.addCLICollectionSources(ctx, "Team", collectionops.SourcesRequest{Accounts: []string{second.Identifier}})
			return err
		}},
		{"remove", func() error {
			_, err := srv.removeCLICollectionSources(ctx, "Team", collectionops.SourcesRequest{Accounts: []string{first.Identifier}})
			return err
		}},
		{"delete", func() error { _, err := srv.deleteCLICollection(ctx, "Team"); return err }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) { require.Error(t, mutation.run()) })
	}
	coll, err := st.GetCollectionByName("Team")
	must.NoError(err)
	check.Equal([]int64{first.ID}, coll.SourceIDs)
	_, err = st.GetCollectionByName("New")
	check.ErrorIs(err, store.ErrCollectionNotFound)
}
