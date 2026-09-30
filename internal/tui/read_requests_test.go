package tui

import (
	"context"
	"testing"
	"testing/synctest"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/peoplebrowser"
	"go.kenn.io/msgvault/internal/query"
)

func TestReadReplacementCancelsPreviousWithoutLosingNewOwner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		started := make(chan context.Context)
		releaseOld := make(chan struct{})
		engine := newMockEngine(MockConfig{})
		engine.GetMessageFunc = func(ctx context.Context, id int64) (*query.MessageDetail, error) {
			started <- ctx
			if id == 1 {
				<-releaseOld // A transport may finish after its cancellation.
			} else {
				<-ctx.Done()
			}
			return nil, ctx.Err()
		}
		model := New(engine, Options{Context: t.Context()})
		defer model.Close()
		oldResult := make(chan tea.Msg, 1)
		oldCommand := model.loadMessageDetail(1)
		go func() { oldResult <- oldCommand() }()
		oldContext := <-started

		model.detailRequestID++
		newCommand := model.loadMessageDetail(2)
		assert.ErrorIs(oldContext.Err(), context.Canceled)
		newResult := make(chan tea.Msg, 1)
		go func() { newResult <- newCommand() }()
		newContext := <-started
		close(releaseOld)
		stale := <-oldResult
		require.NoError(t, newContext.Err(), "old completion must not cancel the current read")
		model = sendMsg(t, model, stale)
		assert.NoError(model.err, "superseded cancellation must not become a visible error")

		model.Close()
		assert.ErrorIs(newContext.Err(), context.Canceled, "new ownership survives old completion")
		<-newResult
	})
}

type cancellableScopeLister struct {
	started chan context.Context
}

func (l cancellableScopeLister) ListCollectionScopes(ctx context.Context) ([]query.CollectionScope, error) {
	l.started <- ctx
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestModeChangeCancelsPresentationReadsButKeepsSharedScopes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		started := make(chan context.Context)
		scopeStarted := make(chan context.Context)
		engine := newMockEngine(MockConfig{})
		engine.GetMessageFunc = func(ctx context.Context, _ int64) (*query.MessageDetail, error) {
			started <- ctx
			<-ctx.Done()
			return nil, ctx.Err()
		}
		model := New(engine, Options{Context: t.Context(), CollectionScopeLister: cancellableScopeLister{scopeStarted}})
		defer model.Close()
		result := make(chan tea.Msg, 1)
		detail := model.loadMessageDetail(1)
		go func() { result <- detail() }()
		detailContext := <-started
		scopes := model.loadCollectionScopes()
		scopeResult := make(chan tea.Msg, 1)
		go func() { scopeResult <- scopes() }()
		scopeContext := <-scopeStarted

		model, _ = sendKey(t, model, key('m'))
		assert.ErrorIs(detailContext.Err(), context.Canceled)
		require.NoError(t, scopeContext.Err(), "shared account scopes remain useful in another mode")
		model = sendMsg(t, model, <-result)
		assert.NoError(model.err)
		assert.NotEqual(modalError, model.modal)
		model.Close()
		assert.ErrorIs(scopeContext.Err(), context.Canceled)
		<-scopeResult
	})
}

func TestSessionCancellationReachesIndependentReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		started := make(chan context.Context)
		engine := newMockEngine(MockConfig{})
		engine.GetMessageFunc = func(ctx context.Context, _ int64) (*query.MessageDetail, error) {
			started <- ctx
			<-ctx.Done()
			return nil, ctx.Err()
		}
		engine.GetTotalStatsFunc = func(ctx context.Context, _ query.StatsOptions) (*query.TotalStats, error) {
			started <- ctx
			<-ctx.Done()
			return nil, ctx.Err()
		}
		model := New(engine, Options{Context: ctx})
		defer model.Close()
		detail, stats := model.loadMessageDetail(1), model.loadStats()
		done := make(chan struct{}, 2)
		go func() { detail(); done <- struct{}{} }()
		detailContext := <-started
		go func() { stats(); done <- struct{}{} }()
		statsContext := <-started
		assert.NoError(detailContext.Err(), "a separate stats read must not cancel message details")
		cancel()
		assert.ErrorIs(detailContext.Err(), context.Canceled)
		assert.ErrorIs(statsContext.Err(), context.Canceled)
		<-done
		<-done
	})
}

type cancellablePeopleBackend struct {
	peoplebrowser.Backend

	started chan context.Context
}

func (b cancellablePeopleBackend) Complete(ctx context.Context, _ peoplebrowser.CompletionRequest) (*peoplebrowser.CompletionPage, error) {
	b.started <- ctx
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestPeopleCompletionReplacementCancelsRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		started := make(chan context.Context)
		model := New(newMockEngine(MockConfig{}), Options{PeopleBackend: cancellablePeopleBackend{started: started}})
		defer model.Close()
		command := model.loadPeopleCompletions("first")
		done := make(chan tea.Msg, 1)
		go func() { done <- command() }()
		ctx := <-started
		_ = model.loadPeopleCompletions("second")
		assert.ErrorIs(ctx.Err(), context.Canceled)
		<-done
	})
}
