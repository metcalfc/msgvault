package api

import (
	"context"

	"go.kenn.io/msgvault/internal/collectionops"
	"go.kenn.io/msgvault/internal/store"
)

// CLICollectionStore is the request-aware database contract for collection
// routes. Unlike the older CLIStore bridge, these routes require cancellation
// support from every implementation.
type CLICollectionStore interface {
	GetCollectionByNameContext(context.Context, string) (*store.CollectionWithSources, error)
	ListCollectionsContext(context.Context) ([]*store.CollectionWithSources, error)
	CreateCollectionContext(context.Context, string, string, []int64) (*store.Collection, error)
	AddSourcesToCollectionContext(context.Context, string, []int64) error
	RemoveSourcesFromCollectionContext(context.Context, string, []int64) error
	DeleteCollectionContext(context.Context, string) error
	GetSourceByIDContext(context.Context, int64) (*store.Source, error)
	GetSourcesByIdentifierOrDisplayNameContext(context.Context, string) ([]*store.Source, error)
	GetSourcesByTypeAndAccountContext(context.Context, string, string) ([]*store.Source, error)
}

func (s *Server) cliCollectionStore() (CLICollectionStore, *apiHTTPError) {
	st, ok := s.store.(CLICollectionStore)
	if !ok {
		return nil, cliStoreUnavailableError()
	}
	return st, nil
}

// Bind the request once for the shared source-selection and collection
// operations, whose in-process interface is also used outside the HTTP API.
type requestCLICollectionStore struct {
	store CLICollectionStore
	ctx   context.Context
}

var _ collectionops.Store = (*requestCLICollectionStore)(nil)

func (s *requestCLICollectionStore) GetCollectionByName(name string) (*store.CollectionWithSources, error) {
	return s.store.GetCollectionByNameContext(s.ctx, name)
}
func (s *requestCLICollectionStore) GetSourceByID(id int64) (*store.Source, error) {
	return s.store.GetSourceByIDContext(s.ctx, id)
}
func (s *requestCLICollectionStore) GetSourcesByIdentifierOrDisplayName(query string) ([]*store.Source, error) {
	return s.store.GetSourcesByIdentifierOrDisplayNameContext(s.ctx, query)
}
func (s *requestCLICollectionStore) GetSourcesByTypeAndAccount(sourceType, account string) ([]*store.Source, error) {
	return s.store.GetSourcesByTypeAndAccountContext(s.ctx, sourceType, account)
}
func (s *requestCLICollectionStore) CreateCollection(name, description string, sourceIDs []int64) (*store.Collection, error) {
	return s.store.CreateCollectionContext(s.ctx, name, description, sourceIDs)
}
func (s *requestCLICollectionStore) AddSourcesToCollection(name string, sourceIDs []int64) error {
	return s.store.AddSourcesToCollectionContext(s.ctx, name, sourceIDs)
}
func (s *requestCLICollectionStore) RemoveSourcesFromCollection(name string, sourceIDs []int64) error {
	return s.store.RemoveSourcesFromCollectionContext(s.ctx, name, sourceIDs)
}
func (s *requestCLICollectionStore) DeleteCollection(name string) error {
	return s.store.DeleteCollectionContext(s.ctx, name)
}
