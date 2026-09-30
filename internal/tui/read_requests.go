package tui

import (
	"context"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
)

type ownedRead struct {
	cancel  context.CancelFunc
	session bool
}

// readRequests is shared by Bubble Tea's model copies. A slot owns one
// replaceable read; writes and exports retain their separate lifetimes.
type readRequests struct {
	mu     sync.Mutex
	root   context.Context
	cancel context.CancelFunc
	active map[string]*ownedRead
}

func newReadRequests(parent context.Context) *readRequests {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &readRequests{root: ctx, cancel: cancel, active: make(map[string]*ownedRead)}
}

func (r *readRequests) begin(slot string, session bool) (context.Context, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if previous := r.active[slot]; previous != nil {
		previous.cancel()
	}
	ctx, cancel := context.WithCancel(r.root)
	read := &ownedRead{cancel: cancel, session: session}
	r.active[slot] = read
	return ctx, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.active[slot] == read {
			delete(r.active, slot)
		}
		cancel()
	}
}

func (r *readRequests) cancelPresentation() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for slot, read := range r.active {
		if read.session {
			continue
		}
		read.cancel()
		delete(r.active, slot)
	}
}

func (m Model) readCommand(slot string, run func(context.Context) tea.Msg, onPanic func(any) tea.Msg) tea.Cmd {
	return m.scopedReadCommand(slot, survivesPresentation(slot), run, onPanic)
}

// Email and Meetings apply a read's result while their mode is parked, and
// re-entering them does not reload, so a mode change must not cancel their
// reads. People and Texts drop results from another mode and reload on
// re-entry; their reads stop when the presentation changes.
func survivesPresentation(slot string) bool {
	return strings.HasPrefix(slot, "email.") || strings.HasPrefix(slot, "meetings.")
}

// Accounts and collections are shared by every presentation and remain useful
// after a mode change. They still stop when replaced or the session closes.
func (m Model) sessionReadCommand(slot string, run func(context.Context) tea.Msg, onPanic func(any) tea.Msg) tea.Cmd {
	return m.scopedReadCommand(slot, true, run, onPanic)
}

func (m Model) scopedReadCommand(slot string, session bool, run func(context.Context) tea.Msg, onPanic func(any) tea.Msg) tea.Cmd {
	ctx := context.Background()
	finish := func() {}
	if m.reads != nil {
		ctx, finish = m.reads.begin(slot, session)
	}
	return safeCmdWithPanic(func() tea.Msg {
		defer finish()
		return run(ctx)
	}, onPanic)
}

func (m *Model) beginPresentation() {
	if m.reads != nil {
		m.reads.cancelPresentation()
	}
	m.presentationGeneration++
}

// Close cancels pending reads when the terminal session ends. Callers embedding
// the model should defer Close after constructing it with New.
func (m Model) Close() {
	if m.reads != nil {
		m.reads.cancel()
	}
}
