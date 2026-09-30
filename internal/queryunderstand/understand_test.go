package queryunderstand

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

// fakeJev is a local System One endpoint answering from a table keyed by
// question ID. Every request body is recorded so tests assert exactly what
// left the machine.
type fakeJev struct {
	mu      sync.Mutex
	raw     []string
	bodies  []map[string]any
	answers map[string]map[string]any
	delay   time.Duration
}

func (f *fakeJev) server(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		var body map[string]any
		if err == nil {
			err = json.Unmarshal(raw, &body)
		}
		questions, ok := body["questions"].(map[string]any)
		if err != nil || !ok {
			http.Error(w, "malformed request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.raw = append(f.raw, string(raw))
		f.bodies = append(f.bodies, body)
		delay := f.delay
		f.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		answers := map[string]any{}
		for id := range questions {
			if answer, ok := f.answers[id]; ok {
				answers[id] = answer
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": jev.DefaultModel, "answers": answers,
			"usage": map[string]any{"input_tokens": 300, "output_tokens": 20},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func (f *fakeJev) requests() ([]map[string]any, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.bodies...), append([]string(nil), f.raw...)
}

func choice(option string, probability float64, others ...string) map[string]any {
	probabilities := map[string]float64{option: probability}
	for _, other := range others {
		probabilities[other] = (1 - probability) / float64(len(others))
	}
	return map[string]any{"type": "choice", "choice": option, "probabilities": probabilities, "confidence": probability}
}

func noul(probability float64) map[string]any {
	return map[string]any{"type": "noul", "noul": probability}
}

func newService(t *testing.T, endpoint string, consent bool) (*jev.Service, *store.Store) {
	t.Helper()
	st := testutil.NewTestStore(t)
	cfg := jev.DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = endpoint
	cfg.QueryUnderstanding.Enabled = true
	service, err := jev.NewService(jev.ServiceOptions{
		Config:     func() (jev.Config, error) { return cfg, nil },
		Consents:   st,
		Ledger:     st,
		Credential: func(string, string) (string, bool, error) { return "fake-key", true, nil },
		Logger:     slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	if consent {
		policy, err := JevFeature().Policy(cfg)
		require.NoError(t, err)
		_, _, err = st.GrantJevFeatureConsent(t.Context(), jev.FeatureQueryUnderstanding, policy.Fingerprint, "test")
		require.NoError(t, err)
	}
	return service, st
}

func sampleCandidates(t *testing.T) Candidates {
	t.Helper()
	candidates, err := Generate(t.Context(), Input{
		Query: "texts from Jane Doe about the lease last week, call +1 555 010 0100",
		Now:   now,
		People: func(_ context.Context, phrase string) ([]PersonMatch, error) {
			if phrase == "jane doe" {
				return []PersonMatch{{ParticipantID: 7, DisplayLabel: "Jane Doe <jane.doe@example.com>"}}, nil
			}
			return nil, nil
		},
	})
	require.NoError(t, err)
	return candidates
}

func TestUnderstandSendsOnlyQueryAndLabelsAndSuggestsConfidentFilters(t *testing.T) {
	fake := &fakeJev{answers: map[string]map[string]any{
		QuestionMessageType:     choice(TypeTextMessage, 0.93, OptionNone),
		QuestionTimeWindow:      choice(WindowKey(1), 0.85, WindowKey(0), OptionNone),
		QuestionPerson:          choice(PersonKey(0), 0.97, OptionNone),
		QuestionPersonRole:      choice(RoleSender, 0.91, RoleRecipient, RoleEither),
		QuestionNaturalLanguage: noul(0.82),
	}}
	service, _ := newService(t, fake.server(t).URL, true)
	addresses := func(_ context.Context, participantID int64) ([]string, error) {
		assert.Equal(t, int64(7), participantID)
		return []string{"Jane.Doe@example.com", "jane@example.org"}, nil
	}

	outcome, asked, err := Understand(t.Context(), service, sampleCandidates(t), addresses, time.Now().Add(Budget))
	require.NoError(t, err)
	require.True(t, asked)

	bodies, raw := fake.requests()
	require.Len(t, bodies, 1)
	assert.Equal(t, map[string]any{
		"query": map[string]any{"text": "texts from Jane Doe about the lease last week, call [phone]"},
		"time_windows": map[string]any{
			"window_1": map[string]any{"label": "Previous calendar week (Sep 21 to Sep 27, 2026)"},
			"window_2": map[string]any{"label": "Past 7 days (Sep 24 to Sep 30, 2026)"},
		},
		"people": map[string]any{"person_1": map[string]any{"label": "Jane Doe"}},
	}, bodies[0]["state"], "only the redacted query and candidate labels leave the machine")
	questions, ok := bodies[0]["questions"].(map[string]any)
	require.True(t, ok)
	askedIDs := make([]string, 0, len(questions))
	for id := range questions {
		askedIDs = append(askedIDs, id)
	}
	assert.ElementsMatch(t, []string{
		QuestionMessageType, QuestionTimeWindow, QuestionPerson, QuestionPersonRole, QuestionNaturalLanguage,
	}, askedIDs, "no account question without account candidates")
	assert.NotContains(t, raw[0], "example.com")
	assert.NotContains(t, raw[0], "555")

	require.NotNil(t, outcome.NaturalLanguage)
	assert.InDelta(t, 0.82, *outcome.NaturalLanguage, 1e-9)
	assert.True(t, outcome.OfferHybrid())
	assert.Equal(t, []Suggestion{
		{
			Kind: KindTimeWindow, Label: "Past 7 days (Sep 24 to Sep 30, 2026)", Span: "last week", Probability: 0.85,
			Filters: []Filter{
				{Dimension: "after", Values: []string{"2026-09-24T00:00:00Z"}},
				{Dimension: "before", Values: []string{"2026-09-30T23:59:59.999Z"}},
			},
		},
		{
			Kind: KindPerson, Label: "From Jane Doe", Span: "from Jane Doe", Probability: 0.97,
			QueryOperators: []string{"from:jane.doe@example.com", "from:jane@example.org"},
		},
		{
			Kind: KindMessageType, Label: "Texts", Span: "texts", Probability: 0.93,
			Filters: []Filter{{Dimension: "message_type", Values: []string{"sms", "mms", "imessage", "rcs", "google_voice_text"}}},
		},
	}, outcome.Suggestions)
}

func TestUnderstandDropsUnsureAndNoneAnswers(t *testing.T) {
	fake := &fakeJev{answers: map[string]map[string]any{
		QuestionMessageType:     choice(TypeTextMessage, 0.79, OptionNone),
		QuestionTimeWindow:      choice(OptionNone, 0.95, WindowKey(0)),
		QuestionPerson:          choice(PersonKey(0), 0.90, OptionNone),
		QuestionPersonRole:      choice(RoleSender, 0.60, RoleRecipient, RoleEither),
		QuestionNaturalLanguage: noul(0.40),
	}}
	service, _ := newService(t, fake.server(t).URL, true)
	outcome, _, err := Understand(t.Context(), service, sampleCandidates(t), nil, time.Now().Add(Budget))
	require.NoError(t, err)
	assert.False(t, outcome.OfferHybrid())
	assert.Equal(t, []Suggestion{{
		Kind: KindPerson, Label: "With Jane Doe", Span: "from Jane Doe", Probability: 0.90,
		Filters: []Filter{{Dimension: "participant", Values: []string{"7"}}},
	}}, outcome.Suggestions, "an unsure role keeps the person filter in any role")
}

func TestUnderstandSendsNothingWithoutConsentOrCandidates(t *testing.T) {
	fake := &fakeJev{}
	service, _ := newService(t, fake.server(t).URL, false)
	_, asked, err := Understand(t.Context(), service, sampleCandidates(t), nil, time.Now().Add(Budget))
	require.ErrorIs(t, err, jev.ErrConsentRequired)
	assert.True(t, asked)

	consented, _ := newService(t, fake.server(t).URL, true)
	candidates, err := Generate(t.Context(), Input{Query: "lease renewal", Now: now})
	require.NoError(t, err)
	_, asked, err = Understand(t.Context(), consented, candidates, nil, time.Now().Add(Budget))
	require.NoError(t, err)
	assert.False(t, asked, "two keywords with no candidates ask nothing")

	bodies, _ := fake.requests()
	assert.Empty(t, bodies)
}

func TestUnderstandIsDroppedWhenLate(t *testing.T) {
	fake := &fakeJev{delay: 2 * time.Second, answers: map[string]map[string]any{
		QuestionNaturalLanguage: noul(0.9),
	}}
	service, _ := newService(t, fake.server(t).URL, true)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	candidates, err := Generate(ctx, Input{Query: "what did the landlord say", Now: now})
	require.NoError(t, err)
	_, asked, err := Understand(ctx, service, candidates, nil, time.Now().Add(50*time.Millisecond))
	require.Error(t, err)
	assert.True(t, asked)
	assert.Equal(t, "timeout", jev.Skipped(err))
}

func TestFeatureDisclosesOnlyQueryAndLabels(t *testing.T) {
	spec := JevFeature()
	require.NoError(t, spec.Validate())
	assert.Equal(t, jev.FeatureQueryUnderstanding, spec.Name)
	assert.Equal(t, StateFields, spec.StateFields)
	for _, field := range spec.StateFields {
		assert.True(t, field == "query.text" || strings.HasSuffix(field, ".label"), field)
	}
}
