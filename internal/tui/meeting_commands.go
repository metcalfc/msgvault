package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/search"
)

type meetingMessagesLoadedMsg struct {
	messages               []query.MessageSummary
	err                    error
	requestID              uint64
	append                 bool
	presentationGeneration uint64
}

type meetingSearchLoadedMsg struct {
	messages               []query.MessageSummary
	err                    error
	requestID              uint64
	offset                 int
	presentationGeneration uint64
}

type meetingDetailLoadedMsg struct {
	detail                 *query.MessageDetail
	err                    error
	requestID              uint64
	presentationGeneration uint64
}

func (m Model) loadMeetingMessages() tea.Cmd {
	return m.loadMeetingMessagesWithOffset(0, false)
}

func (m Model) loadMeetingMessagesWithOffset(offset int, appendResults bool) tea.Cmd {
	engine := m.engine
	filter := m.meetingMessageFilter()
	filter.Pagination.Offset = offset
	requestID := m.meetingState.requestID
	presentationGeneration := m.presentationGeneration
	return m.readCommand("meetings.list",
		func(ctx context.Context) tea.Msg {
			messages, err := engine.ListMessages(ctx, filter)
			return meetingMessagesLoadedMsg{
				messages: messages, err: err, requestID: requestID, append: appendResults,
				presentationGeneration: presentationGeneration,
			}
		},
		func(r any) tea.Msg {
			return meetingMessagesLoadedMsg{
				err:                    fmt.Errorf("meeting messages panic: %v", r),
				requestID:              requestID,
				append:                 appendResults,
				presentationGeneration: presentationGeneration,
			}
		},
	)
}

func (m Model) handleMeetingMessagesLoaded(msg meetingMessagesLoadedMsg) (tea.Model, tea.Cmd) {
	state := m.meetingWorkspaceState()
	if msg.requestID != state.requestID {
		return m, nil
	}
	active := m.finishMeetingPresentation(msg.presentationGeneration, &state.listLoading)
	state.listLoadingMore = false
	if msg.err != nil {
		if active {
			m.err = query.HintRepairEncoding(msg.err)
			m.modal = modalError
			m.modalResult = m.err.Error()
		}
		return m, nil
	}
	if active {
		m.err = nil
	}
	state.initialized = true
	if msg.append {
		state.messages = append(state.messages, msg.messages...)
	} else {
		state.messages = msg.messages
		state.cursor = 0
		state.scrollOffset = 0
	}
	state.listOffset = len(state.messages)
	state.listComplete = len(msg.messages) < messageListPageSize
	state.searchSnapshotInvalid = false
	return m, nil
}

func (m Model) loadMeetingSearch(queryString string, offset int, _ bool) tea.Cmd {
	engine := m.engine
	filter := m.meetingMessageFilter()
	requestID := m.meetingState.searchRequestID
	presentationGeneration := m.presentationGeneration
	return m.readCommand("meetings.search",
		func(ctx context.Context) tea.Msg {
			parsed := search.Parse(queryString)
			if err := parsed.Err(); err != nil {
				return meetingSearchLoadedMsg{
					err: err, requestID: requestID, offset: offset,
					presentationGeneration: presentationGeneration,
				}
			}
			merged := query.MergeFilterIntoQuery(parsed, filter)
			messages, err := engine.Search(ctx, merged, searchPageSize, offset)
			return meetingSearchLoadedMsg{
				messages: messages,
				err:      err, requestID: requestID, offset: offset,
				presentationGeneration: presentationGeneration,
			}
		},
		func(r any) tea.Msg {
			return meetingSearchLoadedMsg{
				err: fmt.Errorf("meeting search panic: %v", r), requestID: requestID, offset: offset,
				presentationGeneration: presentationGeneration,
			}
		},
	)
}

func (m Model) handleMeetingSearchLoaded(msg meetingSearchLoadedMsg) (tea.Model, tea.Cmd) {
	state := m.meetingWorkspaceState()
	if msg.requestID != state.searchRequestID {
		return m, nil
	}
	active := m.finishMeetingPresentation(msg.presentationGeneration, &state.searchLoading)
	state.listLoadingMore = false
	if msg.err != nil {
		if active {
			m.err = query.HintRepairEncoding(msg.err)
			m.modal = modalError
			m.modalResult = m.err.Error()
		}
		return m, nil
	}
	if active {
		m.err = nil
	}
	state.initialized = true
	if msg.offset > 0 {
		state.messages = append(state.messages, msg.messages...)
	} else {
		state.messages = msg.messages
		state.cursor = 0
		state.scrollOffset = 0
	}
	state.searchOffset = len(state.messages)
	state.searchComplete = len(msg.messages) < searchPageSize
	return m, nil
}

func (m Model) loadMeetingDetail(id int64) tea.Cmd {
	engine := m.engine
	requestID := m.meetingState.detailRequestID
	presentationGeneration := m.presentationGeneration
	return m.readCommand("meetings.detail",
		func(ctx context.Context) tea.Msg {
			detail, err := engine.GetMessage(ctx, id)
			return meetingDetailLoadedMsg{
				detail: detail, err: err, requestID: requestID,
				presentationGeneration: presentationGeneration,
			}
		},
		func(r any) tea.Msg {
			return meetingDetailLoadedMsg{
				err: fmt.Errorf("meeting detail panic: %v", r), requestID: requestID,
				presentationGeneration: presentationGeneration,
			}
		},
	)
}

func (m Model) handleMeetingDetailLoaded(msg meetingDetailLoadedMsg) (tea.Model, tea.Cmd) {
	state := m.meetingWorkspaceState()
	if msg.requestID != state.detailRequestID {
		return m, nil
	}
	active := m.finishMeetingPresentation(msg.presentationGeneration, &state.detailLoading)
	if msg.err != nil {
		if active {
			m.err = query.HintRepairEncoding(msg.err)
			m.modal = modalError
			m.modalResult = m.err.Error()
		}
		return m, nil
	}
	if active {
		m.err = nil
	}
	*state = m.withMeetingDetail(*state, msg.detail)
	return m, nil
}

// meetingWorkspaceState identifies the Meetings workspace even while People
// borrows the active meeting reader for a contact's meeting. Reads keep their
// workspace owner when the presentation changes.
func (m *Model) meetingWorkspaceState() *meetingState {
	if m.mode == modePeople {
		return &m.peopleState.parkedMeetingState
	}
	return &m.meetingState
}

func (m Model) withMeetingDetail(state meetingState, detail *query.MessageDetail) meetingState {
	m.meetingState = state
	m.meetingState.detail = detail
	if m.meetingState.detailSearchQuery != "" {
		m.findMeetingDetailMatches()
		if len(m.meetingState.detailSearchMatches) > 0 {
			m.scrollToMeetingDetailMatch()
		}
	}
	return m.meetingState
}

func (m *Model) finishMeetingPresentation(generation uint64, owner *bool) bool {
	*owner = false
	if m.mode != modeMeetings || m.presentationGeneration != generation {
		return false
	}
	m.updateMeetingLoading()
	return true
}

func (m *Model) updateMeetingLoading() {
	if m.mode != modeMeetings {
		return
	}
	m.loading = m.meetingState.listLoading ||
		m.meetingState.searchLoading || m.meetingState.detailLoading
	if !m.loading {
		m.transitionBuffer = ""
	}
}
