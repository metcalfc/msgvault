// Package jevtest provides a local System One endpoint and a gated Jev
// service wired to it, so feature tests exercise the real client, gates,
// consent, and accounting without reaching the real API. Every request body
// is recorded so tests can assert exactly what left the machine.
package jevtest

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/store"
)

// Answerer returns the wire answer for one question of one request. The
// state is the request's decoded state. Use Noul and Choice to build it.
type Answerer func(questionID string, question map[string]any, state map[string]any) map[string]any

// Noul is a wire Noul answer.
func Noul(probability float64) map[string]any {
	return map[string]any{"type": "noul", "noul": probability}
}

// Choice is a wire Choice answer whose confidence is the chosen option's
// probability.
func Choice(choice string, probabilities map[string]float64) map[string]any {
	return map[string]any{
		"type": "choice", "choice": choice, "probabilities": probabilities, "confidence": probabilities[choice],
	}
}

// Server is a fake System One endpoint.
type Server struct {
	URL    string
	mu     sync.Mutex
	bodies []map[string]any
	answer Answerer
}

// NewServer starts a fake endpoint that answers with answer.
func NewServer(t *testing.T, answer Answerer) *Server {
	t.Helper()
	fake := &Server{answer: answer}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		var body map[string]any
		if err == nil {
			err = json.Unmarshal(raw, &body)
		}
		state, stateOK := body["state"].(map[string]any)
		questions, questionsOK := body["questions"].(map[string]any)
		if err != nil || !stateOK || !questionsOK {
			http.Error(w, "malformed request", http.StatusBadRequest)
			return
		}
		fake.mu.Lock()
		fake.bodies = append(fake.bodies, body)
		fake.mu.Unlock()
		answers := map[string]any{}
		for id, question := range questions {
			questionMap, _ := question.(map[string]any)
			answers[id] = fake.answer(id, questionMap, state)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": jev.DefaultModel, "answers": answers,
			"usage": map[string]any{"input_tokens": 500, "output_tokens": 20},
		})
	}))
	t.Cleanup(server.Close)
	fake.URL = server.URL
	return fake
}

// Requests returns every recorded request body.
func (s *Server) Requests() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.bodies...)
}

// Service returns a real Jev service pointed at the fake endpoint with the
// configuration enable produced from the defaults ([jev] enabled). The key
// always resolves.
func (s *Server) Service(t *testing.T, st *store.Store, enable func(*jev.Config)) (*jev.Service, jev.Config) {
	t.Helper()
	cfg := jev.DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = s.URL
	if enable != nil {
		enable(&cfg)
	}
	service, err := jev.NewService(jev.ServiceOptions{
		Config:     func() (jev.Config, error) { return cfg, nil },
		Consents:   st,
		Ledger:     st,
		Credential: func(string, string) (string, bool, error) { return "fake-key", true, nil },
		Logger:     slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	return service, cfg
}

// GrantConsent records consent for spec's current policy under cfg.
func GrantConsent(t *testing.T, st *store.Store, cfg jev.Config, spec jev.FeatureSpec) {
	t.Helper()
	policy, err := spec.Policy(cfg)
	require.NoError(t, err)
	_, _, err = st.GrantJevFeatureConsent(t.Context(), spec.Name, policy.Fingerprint, "test")
	require.NoError(t, err)
}
