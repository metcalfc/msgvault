package api

import (
	"context"

	"go.kenn.io/msgvault/internal/accountops"
	"go.kenn.io/msgvault/internal/collectionops"
	"go.kenn.io/msgvault/internal/identityops"
	"go.kenn.io/msgvault/internal/store"
)

// CLIScopeStore resolves archive scopes for search and CLI routes. HTTP
// capabilities require context-aware methods, independently of other routes.
type CLIScopeStore interface {
	GetSourceByIDContext(ctx context.Context, id int64) (*store.Source, error)
	GetSourcesByIdentifierOrDisplayNameContext(ctx context.Context, query string) ([]*store.Source, error)
	GetSourcesByTypeAndAccountContext(ctx context.Context, sourceType, account string) ([]*store.Source, error)
	GetCollectionByNameContext(ctx context.Context, name string) (*store.CollectionWithSources, error)
}

type CLIStatsStore interface {
	GetStatsForScopeContext(ctx context.Context, sourceIDs []int64) (*store.Stats, error)
}

type CLIAccountStore interface {
	CLIScopeStore
	ListSourcesContext(ctx context.Context, sourceType string) ([]*store.Source, error)
	UpdateSourceDisplayNameContext(ctx context.Context, sourceID int64, displayName string) error
	CountMessagesForSourceContext(ctx context.Context, sourceID int64) (int64, error)
	CountSourceDeletedMessagesContext(ctx context.Context, sourceIDs ...int64) (int64, error)
}

type CLIIdentityStore interface {
	CLIScopeStore
	ListSourcesContext(ctx context.Context, sourceType string) ([]*store.Source, error)
	ListAccountIdentitiesContext(ctx context.Context, sourceID int64) ([]store.AccountIdentity, error)
	AddAccountIdentityContext(ctx context.Context, sourceID int64, address, signal string) error
	RemoveAccountIdentityContext(ctx context.Context, sourceID int64, address string) (int64, error)
}

type CLIIdentityDiscoveryStore interface {
	CLIIdentityStore
	CountIdentityDiscoveryMessagesContext(ctx context.Context, sourceID int64) (int64, error)
	ScanIdentityDiscoveryPageContext(ctx context.Context, sourceID, afterID int64, limit int) (store.IdentityDiscoveryPage, error)
	ScanIdentityObservationsForSourceMessageIDsContext(ctx context.Context, sourceID int64, sourceMessageIDs []string) ([]store.IdentityObservation, error)
	AddAccountIdentitiesBatchContext(ctx context.Context, sourceID int64, candidates []store.IdentityConfirmation) ([]store.IdentityConfirmationOutcome, error)
	MergeConfirmedAccountIdentitySignalsContext(ctx context.Context, sourceID int64, candidates []store.IdentityConfirmation) ([]store.IdentityConfirmationOutcome, error)
}

type CLIIndexStore interface {
	NeedsFTSBackfillContext(ctx context.Context) bool
	NeedsFTSBackfillQuickContext(ctx context.Context) bool
	BackfillFTSContext(ctx context.Context, progress func(done, total int64)) (int64, error)
	RebuildFTSContext(ctx context.Context, progress func(done, total int64)) (int64, error)
}

func cliCapability[T any](s *Server) (T, *apiHTTPError) {
	capability, ok := s.store.(T)
	if !ok {
		var zero T
		return zero, cliStoreUnavailableError()
	}
	return capability, nil
}

// The shared in-process operations still accept context-free source resolvers.
// These adapters bind a required context-aware capability once; no HTTP route
// falls back to a context-free implementation.
type requestCLIScopeStore struct {
	store CLIScopeStore
	ctx   context.Context
}

var _ collectionops.AccountResolverStore = (*requestCLIScopeStore)(nil)

func (s *Server) cliScopeStore(ctx context.Context) (*requestCLIScopeStore, *apiHTTPError) {
	st, err := cliCapability[CLIScopeStore](s)
	if err != nil {
		return nil, err
	}
	return &requestCLIScopeStore{st, ctx}, nil
}

func (s *requestCLIScopeStore) GetSourceByID(id int64) (*store.Source, error) {
	return s.store.GetSourceByIDContext(s.ctx, id)
}
func (s *requestCLIScopeStore) GetSourcesByIdentifierOrDisplayName(input string) ([]*store.Source, error) {
	return s.store.GetSourcesByIdentifierOrDisplayNameContext(s.ctx, input)
}
func (s *requestCLIScopeStore) GetSourcesByTypeAndAccount(sourceType, account string) ([]*store.Source, error) {
	return s.store.GetSourcesByTypeAndAccountContext(s.ctx, sourceType, account)
}
func (s *requestCLIScopeStore) GetCollectionByName(name string) (*store.CollectionWithSources, error) {
	return s.store.GetCollectionByNameContext(s.ctx, name)
}

type requestCLIAccountStore struct {
	requestCLIScopeStore

	accountStore CLIAccountStore
}

var _ accountops.Store = (*requestCLIAccountStore)(nil)

func (s *requestCLIAccountStore) UpdateSourceDisplayName(id int64, name string) error {
	return s.accountStore.UpdateSourceDisplayNameContext(s.ctx, id, name)
}

type requestCLIIdentityStore struct {
	requestCLIScopeStore

	identityStore CLIIdentityStore
}

var _ identityops.Store = (*requestCLIIdentityStore)(nil)

func (s *Server) cliIdentityStore(ctx context.Context) (*requestCLIIdentityStore, *apiHTTPError) {
	st, err := cliCapability[CLIIdentityStore](s)
	if err != nil {
		return nil, err
	}
	return &requestCLIIdentityStore{requestCLIScopeStore{st, ctx}, st}, nil
}

func (s *requestCLIIdentityStore) ListSources(sourceType string) ([]*store.Source, error) {
	return s.identityStore.ListSourcesContext(s.ctx, sourceType)
}
func (s *requestCLIIdentityStore) ListAccountIdentities(id int64) ([]store.AccountIdentity, error) {
	return s.identityStore.ListAccountIdentitiesContext(s.ctx, id)
}
func (s *requestCLIIdentityStore) AddAccountIdentity(id int64, address, signal string) error {
	return s.identityStore.AddAccountIdentityContext(s.ctx, id, address, signal)
}
func (s *requestCLIIdentityStore) RemoveAccountIdentity(id int64, address string) (int64, error) {
	return s.identityStore.RemoveAccountIdentityContext(s.ctx, id, address)
}

type requestCLIIdentityDiscoveryStore struct {
	requestCLIIdentityStore
	CLIIdentityDiscoveryStore
}

var _ identityops.DiscoveryStore = (*requestCLIIdentityDiscoveryStore)(nil)

func (s *Server) cliDiscoveryStore(ctx context.Context) (*requestCLIIdentityDiscoveryStore, *apiHTTPError) {
	st, err := cliCapability[CLIIdentityDiscoveryStore](s)
	if err != nil {
		return nil, err
	}
	return &requestCLIIdentityDiscoveryStore{
		requestCLIIdentityStore{requestCLIScopeStore{st, ctx}, st}, st,
	}, nil
}
