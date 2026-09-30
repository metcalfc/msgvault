package rerank_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
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
	"go.kenn.io/msgvault/internal/testutil/storetest"
	"go.kenn.io/msgvault/internal/vector/embed"
	"go.kenn.io/msgvault/internal/vector/hybrid"
	"go.kenn.io/msgvault/internal/vector/rerank"
)

type rerankArchive struct {
	f      *storetest.Fixture
	lease  int64
	digest int64
	chat   int64
}

func newRerankArchive(t *testing.T) *rerankArchive {
	t.Helper()
	f := storetest.New(t)
	casey := f.EnsureParticipant("casey@example.com", "Casey Example", "example.com")
	quiet := f.EnsureParticipant("quiet@example.org", "", "example.org")
	sent := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	add := func(sourceID, subject, messageType string, sender int64, body string) int64 {
		sent = sent.Add(24 * time.Hour)
		id, err := f.Store.UpsertMessage(&store.Message{
			ConversationID: f.ConvID, SourceID: f.Source.ID, SourceMessageID: sourceID, MessageType: messageType,
			SentAt:   sql.NullTime{Time: sent, Valid: true},
			SenderID: sql.NullInt64{Int64: sender, Valid: true},
			Subject:  sql.NullString{String: subject, Valid: true},
		})
		require.NoError(t, err)
		require.NoError(t, f.Store.ReplaceMessageRecipients(id, "from", []int64{sender}, []string{""}))
		require.NoError(t, f.Store.UpsertMessageBody(id, sql.NullString{String: body, Valid: true}, sql.NullString{}))
		return id
	}
	return &rerankArchive{
		f:      f,
		lease:  add("lease", "Lease renewal", "email", casey, "The lease renewal is signed.\n\n> older quoted discussion\n"),
		digest: add("digest", "Weekly digest", "email", quiet, "Unrelated weekly figures."),
		chat:   add("chat", "Chat", "whatsapp", casey, "private chat text about the lease"),
	}
}

// fakeJev is a local System One server: it answers every Noul with 0.9 when
// the candidate text mentions "lease" and 0.2 otherwise, and records every
// request body. It stands in for the provider API, an external contract the
// tests cannot reach.
type fakeJev struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (j *fakeJev) server(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var body map[string]any
		require.NoError(t, json.Unmarshal(raw, &body))
		j.mu.Lock()
		j.bodies = append(j.bodies, body)
		j.mu.Unlock()
		state := body["state"].(map[string]any)
		score := func(text string) float64 {
			if strings.Contains(strings.ToLower(text), "lease") {
				return 0.9
			}
			return 0.2
		}
		answers := map[string]any{}
		for id := range body["questions"].(map[string]any) {
			text, _ := state["candidate"].(string)
			if id != "matches" {
				var index int
				_, err := fmt.Sscanf(id, "candidate_%d", &index)
				require.NoError(t, err)
				text = state["candidates"].([]any)[index].(string)
			}
			answers[id] = map[string]any{"type": "noul", "noul": score(text)}
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"model": jev.DefaultModel, "answers": answers,
			"usage": map[string]any{"input_tokens": 300, "output_tokens": 10},
		}))
	}))
	t.Cleanup(server.Close)
	return server
}

func (j *fakeJev) requests() []map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]map[string]any(nil), j.bodies...)
}

func rerankService(t *testing.T, endpoint string, st *store.Store) (*jev.Service, jev.Config) {
	t.Helper()
	cfg := jev.DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = endpoint
	cfg.Rerank.Enabled = true
	service, err := jev.NewService(jev.ServiceOptions{
		Config:     func() (jev.Config, error) { return cfg, nil },
		Consents:   st,
		Ledger:     st,
		Credential: func(string, string) (string, bool, error) { return "fake-key", true, nil },
	})
	require.NoError(t, err)
	return service, cfg
}

func grantRerankConsent(t *testing.T, st *store.Store, cfg jev.Config) jev.Policy {
	t.Helper()
	policy, err := rerank.JevFeature().Policy(cfg)
	require.NoError(t, err)
	_, _, err = st.GrantJevFeatureConsent(t.Context(), jev.FeatureSearchRerank, policy.Fingerprint, "test")
	require.NoError(t, err)
	return policy
}

func newTestStage(t *testing.T, service *jev.Service, st *store.Store, shape string) *rerank.Stage {
	t.Helper()
	scorer, err := rerank.NewServiceScorer(service, shape)
	require.NoError(t, err)
	cfg := jev.RerankConfig{MessageTypesExcluded: []string{"WhatsApp"}}
	stage, err := rerank.NewStage(rerank.StageOptions{
		Scorer: scorer, Loader: st, Shape: shape, Model: jev.DefaultModel, Top: 30,
		Excludes:   cfg.Excludes,
		Preprocess: embed.PreprocessConfig{StripQuotes: true, CollapseWhitespace: true},
		Timeout:    5 * time.Second,
	})
	require.NoError(t, err)
	return stage
}

func TestStageSendsOnlyDisclosedCandidateTextWithConsent(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	a := newRerankArchive(t)
	fake := &fakeJev{}
	service, cfg := rerankService(t, fake.server(t).URL, a.f.Store)
	policy := grantRerankConsent(t, a.f.Store, cfg)
	stage := newTestStage(t, service, a.f.Store, rerank.ShapeBatched)

	scores, err := stage.Rerank(t.Context(), "lease renewal", []int64{a.digest, a.lease, a.chat})
	require.NoError(err)
	assert.Equal(jev.DefaultModel, scores.Model)
	assert.Equal(map[int64]float64{a.digest: 0.2, a.lease: 0.9}, scores.Scores,
		"an excluded message type is neither sent nor scored")

	requests := fake.requests()
	require.Len(requests, 1, "the batched shape sends one request")
	state := requests[0]["state"].(map[string]any)
	assert.Equal("lease renewal", state["query"])
	assert.Equal([]any{
		"Subject: Weekly digest\nFrom: quiet@example.org\nDate: 2026-03-03\n\nUnrelated weekly figures.",
		"Subject: Lease renewal\nFrom: Casey Example\nDate: 2026-03-02\n\nThe lease renewal is signed.",
	}, state["candidates"], "subject, sender, date, and the cleaned body are all that is sent")
	assert.Len(state, 2, "the state carries only the query and the candidates")

	questions := requests[0]["questions"].(map[string]any)
	require.Len(questions, 2)
	for _, question := range policy.Questions {
		sent, ok := questions[question.ID]
		if !ok {
			continue
		}
		assert.Equal(question.Instructions, sent.(map[string]any)["instructions"], "wording is the consented policy's")
	}
	assert.Contains(questions, "candidate_0")
	assert.Contains(questions, "candidate_1")

	status, err := a.f.Store.GetJevFeatureConsentStatus(t.Context(), jev.FeatureSearchRerank, policy.Fingerprint)
	require.NoError(err)
	assert.True(status.Active)
}

func TestStagePerCandidateShapeSendsOneRequestPerMessage(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	a := newRerankArchive(t)
	fake := &fakeJev{}
	service, cfg := rerankService(t, fake.server(t).URL, a.f.Store)
	grantRerankConsent(t, a.f.Store, cfg)
	stage := newTestStage(t, service, a.f.Store, rerank.ShapePerCandidate)

	scores, err := stage.Rerank(t.Context(), "lease renewal", []int64{a.digest, a.lease})
	require.NoError(err)
	assert.Equal(map[int64]float64{a.digest: 0.2, a.lease: 0.9}, scores.Scores)
	requests := fake.requests()
	require.Len(requests, 2)
	for _, request := range requests {
		assert.Contains(request["questions"], "matches")
		assert.Contains(request["state"], "candidate")
	}
}

func TestStageWithoutConsentSendsNothing(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	a := newRerankArchive(t)
	fake := &fakeJev{}
	service, _ := rerankService(t, fake.server(t).URL, a.f.Store)
	stage := newTestStage(t, service, a.f.Store, rerank.ShapeBatched)

	_, err := stage.Rerank(t.Context(), "lease renewal", []int64{a.digest, a.lease})
	require.Error(err)
	var reasoner hybrid.RerankReasoner
	require.ErrorAs(err, &reasoner)
	assert.Equal("consent_required", reasoner.RerankReason())
	assert.Empty(fake.requests(), "nothing leaves the machine without consent")
}

func TestStageNeedsTwoSendableMessages(t *testing.T) {
	a := newRerankArchive(t)
	fake := &fakeJev{}
	service, cfg := rerankService(t, fake.server(t).URL, a.f.Store)
	grantRerankConsent(t, a.f.Store, cfg)
	stage := newTestStage(t, service, a.f.Store, rerank.ShapeBatched)

	_, err := stage.Rerank(t.Context(), "lease renewal", []int64{a.lease, a.chat})
	var reasoner hybrid.RerankReasoner
	require.ErrorAs(t, err, &reasoner)
	assert.Equal(t, hybrid.RerankReasonTooFewCandidates, reasoner.RerankReason())
	assert.Empty(t, fake.requests())
}

func TestCandidateIsBoundedAndCleaned(t *testing.T) {
	assert := assert.New(t)
	text := rerank.Candidate(rerank.Message{
		Subject:   strings.Repeat("界", 400),
		FromName:  "  Casey\nExample  ",
		FromEmail: "casey@example.com",
		SentAt:    time.Date(2026, 1, 2, 23, 0, 0, 0, time.FixedZone("west", -5*3600)),
		BodyHTML:  "<p>Hello <b>there</b></p>" + strings.Repeat("word ", 2000),
	}, embed.PreprocessConfig{StripHTML: true, CollapseWhitespace: true})
	assert.LessOrEqual(len(text), rerank.MaxCandidateBytes)
	assert.True(strings.HasPrefix(text, "Subject: 界"))
	assert.Contains(text, "\nFrom: Casey Example\nDate: 2026-01-03\n\nHello there\nword")
	assert.NotContains(text, "casey@example.com", "a named sender is sent by name only")
	assert.NotContains(text, "<p>")

	assert.Equal("Subject: \nFrom: \nDate: \n\n", rerank.Candidate(rerank.Message{}, embed.PreprocessConfig{}))
	assert.Equal("abc", rerank.TruncateUTF8Bytes("abc", 2048))
	assert.Equal("a", rerank.TruncateUTF8Bytes("a界", 3), "a cut never splits a rune")
}

func TestFeatureDisclosesBodyText(t *testing.T) {
	assert := assert.New(t)
	spec := rerank.JevFeature()
	require.NoError(t, spec.Validate())
	assert.Equal(jev.FeatureSearchRerank, spec.Name)
	assert.Contains(spec.BodyNotice, "body text")
	policy, err := spec.Policy(jev.DefaultConfig())
	require.NoError(t, err)
	assert.Equal(spec.BodyNotice, policy.BodyNotice)
	assert.Len(policy.Questions, rerank.MaxCandidates+1)
}
