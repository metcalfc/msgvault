package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/query/querytest"
	"go.kenn.io/msgvault/internal/queryunderstand"
	"go.kenn.io/msgvault/internal/store"
)

// recordingQueryJudge answers from a table, or blocks until its context
// ends when block is set. It records every state it was asked about.
type recordingQueryJudge struct {
	mu        sync.Mutex
	states    []queryunderstand.State
	questions [][]string
	answers   map[string]jev.Answer
	block     bool
}

func (j *recordingQueryJudge) JudgeQuestions(
	ctx context.Context, _ jev.FeatureSpec, automatic bool, state any, questionIDs []string, _ time.Time,
) (jev.Response, error) {
	j.mu.Lock()
	typed, _ := state.(queryunderstand.State)
	j.states = append(j.states, typed)
	j.questions = append(j.questions, questionIDs)
	j.mu.Unlock()
	if automatic {
		return jev.Response{}, jev.ErrAutomaticDisabled
	}
	if j.block {
		<-ctx.Done()
		return jev.Response{}, ctx.Err()
	}
	return jev.Response{Model: jev.DefaultModel, Answers: j.answers}, nil
}

type queryUnderstandingStore struct {
	*completionAPIStore
}

func (s *queryUnderstandingStore) GetParticipantIdentityContext(
	_ context.Context, ids []int64,
) (*store.ParticipantIdentityContext, error) {
	details := &store.ParticipantIdentityContext{}
	for _, id := range ids {
		details.Members = append(details.Members, store.ParticipantIdentityMember{
			ParticipantID: id, DisplayName: "Jane Doe", Email: map[int64]string{7: "jane.doe@example.com", 11: "jd@example.org"}[id],
		})
	}
	return details, nil
}

func newQueryUnderstandingServer(t *testing.T, judge queryunderstand.Judge) *Server {
	t.Helper()
	engine := &peopleAPIEngine{
		MockEngine: &querytest.MockEngine{Accounts: []query.AccountInfo{
			{ID: 1, SourceType: "gmail", Identifier: "owner@example.com", DisplayName: "Work"},
			{ID: 2, SourceType: "imap", Identifier: "owner@example.net"},
		}},
		completionResult: &query.PeopleCompletionResponse{Rows: []query.PeopleCompletion{
			{ParticipantID: 7, DisplayLabel: "Jane Doe", Kind: query.PeopleCompletionName,
				Value: "Jane Doe", MatchValue: "jane doe", Source: "observed"},
		}},
	}
	st := &queryUnderstandingStore{completionAPIStore: &completionAPIStore{
		mockStore: &mockStore{}, members: map[int64][]int64{7: {7, 11}},
	}}
	server := NewServerWithOptions(ServerOptions{
		Config: &config.Config{Server: config.ServerConfig{APIPort: 8080}},
		Store:  st, Engine: engine, Logger: testLogger(), QueryUnderstanding: judge,
	})
	server.clock = func() time.Time { return time.Date(2026, time.September, 30, 18, 0, 0, 0, time.UTC) }
	return server
}

func postQueryUnderstanding(t *testing.T, server *Server, body string) (int, ExploreQueryUnderstandingResponse, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/explore/query-understanding", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Router().ServeHTTP(recorder, request)
	var response ExploreQueryUnderstandingResponse
	if recorder.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	}
	return recorder.Code, response, recorder.Body.String()
}

func choiceAnswer(option string, probability float64) jev.Answer {
	return jev.Answer{Type: jev.QuestionChoice, Choice: option, Confidence: probability,
		Probabilities: map[string]float64{option: probability, queryunderstand.OptionNone: 1 - probability}}
}

func TestExploreQueryUnderstandingSuggestsFilters(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	judge := &recordingQueryJudge{answers: map[string]jev.Answer{
		queryunderstand.QuestionMessageType: choiceAnswer(queryunderstand.TypeEmail, 0.92),
		queryunderstand.QuestionTimeWindow:  choiceAnswer(queryunderstand.WindowKey(0), 0.88),
		queryunderstand.QuestionPerson:      choiceAnswer(queryunderstand.PersonKey(0), 0.95),
		queryunderstand.QuestionPersonRole:  choiceAnswer(queryunderstand.RoleSender, 0.90),
		queryunderstand.QuestionAccount:     choiceAnswer(queryunderstand.AccountKey(0), 0.50),
		queryunderstand.QuestionNaturalLanguage: {
			Type: jev.QuestionNoul, Noul: 0.75,
		},
	}}
	server := newQueryUnderstandingServer(t, judge)

	code, response, raw := postQueryUnderstanding(t, server,
		`{"query":"emails from Jane Doe about the lease in my work account yesterday","timezone":"America/Los_Angeles"}`)
	require.Equal(http.StatusOK, code, raw)

	assert.Equal("judged", response.Status)
	assert.True(response.OfferHybrid)
	require.NotNil(response.NaturalLanguage)
	assert.InDelta(0.75, *response.NaturalLanguage, 1e-9)
	assert.Equal([]ExploreQuerySuggestion{
		{
			Kind: "time_window", Label: "Yesterday (Sep 29, 2026)", Span: "yesterday", SpanStart: 56, SpanEnd: 65,
			Probability: 0.88,
			Filters: []ExploreFilter{
				{Dimension: "after", Values: []string{"2026-09-29T00:00:00-07:00"}},
				{Dimension: "before", Values: []string{"2026-09-29T23:59:59.999-07:00"}},
			},
			QueryOperators: []string{},
		},
		{
			// Two addresses: from: operators would be AND-ed, so the person
			// filter stays.
			Kind: "person", Label: "With Jane Doe", Span: "from Jane Doe", SpanStart: 7, SpanEnd: 20, Probability: 0.95,
			Filters:        []ExploreFilter{{Dimension: "participant", Values: []string{"7"}}},
			QueryOperators: []string{},
		},
		{
			Kind: "message_type", Label: "Email", Span: "emails", SpanStart: 0, SpanEnd: 6, Probability: 0.92,
			Filters:        []ExploreFilter{{Dimension: "message_type", Values: []string{"email"}}},
			QueryOperators: []string{},
		},
	}, response.Suggestions, "the unsure account answer is not offered; days are read in the browser's zone")

	require.Len(judge.states, 1)
	state := judge.states[0]
	assert.Equal("emails from Jane Doe about the lease in my work account yesterday", state.Query.Text)
	assert.Equal(map[string]queryunderstand.LabelState{"person_1": {Label: "Jane Doe"}}, state.People)
	assert.Equal(map[string]queryunderstand.LabelState{
		"account_1": {Label: "gmail account named Work at example.com"},
	}, state.Accounts)
}

func TestExploreQueryUnderstandingSkipsAndDropsLateJudgments(t *testing.T) {
	t.Run("disabled without a judge", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		code, response, raw := postQueryUnderstanding(t, newQueryUnderstandingServer(t, nil), `{"query":"lease from Jane Doe"}`)
		require.Equal(http.StatusOK, code, raw)
		assert.Equal(ExploreQueryUnderstandingResponse{
			Status: "skipped", Reason: "disabled", Suggestions: []ExploreQuerySuggestion{}, ElapsedMS: response.ElapsedMS,
		}, response)
	})

	t.Run("nothing worth asking", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		judge := &recordingQueryJudge{}
		code, response, raw := postQueryUnderstanding(t, newQueryUnderstandingServer(t, judge), `{"query":"lease renewal"}`)
		require.Equal(http.StatusOK, code, raw)
		assert.Equal("skipped", response.Status)
		assert.Equal("no_candidates", response.Reason)
		assert.Empty(judge.states)
	})

	t.Run("late judgments are dropped within the budget", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		judge := &recordingQueryJudge{block: true}
		started := time.Now()
		code, response, raw := postQueryUnderstanding(t, newQueryUnderstandingServer(t, judge),
			`{"query":"what did the landlord say about the deposit"}`)
		require.Equal(http.StatusOK, code, raw)
		assert.Equal("late", response.Status)
		assert.Empty(response.Suggestions)
		assert.Less(time.Since(started), 10*time.Second, "the request ends when the budget does")
	})

	t.Run("invalid input", func(t *testing.T) {
		assert := assert.New(t)
		server := newQueryUnderstandingServer(t, &recordingQueryJudge{})
		code, _, _ := postQueryUnderstanding(t, server, `{"query":"   "}`)
		assert.Equal(http.StatusBadRequest, code)
		code, _, _ = postQueryUnderstanding(t, server, `{"query":"lease","timezone":"Mars/Olympus"}`)
		assert.Equal(http.StatusBadRequest, code)
	})
}

func TestUTF16OffsetCountsJavaScriptIndexes(t *testing.T) {
	query := "Zoë 😀 last week"
	start := strings.Index(query, "last week")
	assert.Equal(t, 7, utf16Offset(query, start), "ë is one unit and the emoji two")
	assert.Equal(t, 16, utf16Offset(query, len(query)))
	assert.Equal(t, 0, utf16Offset(query, 0))
}
