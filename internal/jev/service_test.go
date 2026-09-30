package jev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testSpec() FeatureSpec {
	return FeatureSpec{
		Name: FeatureEnrichmentIdentity, Title: "Test identity", Purpose: "Decide whether two names match.",
		Questions: []Question{{
			ID: "same", Type: QuestionNoul,
			Instructions: "Is `left` the same person as `right`?",
			Criteria:     NoulCriteria{True: "Same person.", False: "Different people."},
		}},
		StateFields: []string{"left", "right"},
	}
}

type fakeConsents struct {
	mu     sync.Mutex
	active map[string]string
	err    error
}

func (c *fakeConsents) HasActiveJevFeatureConsent(_ context.Context, feature, fingerprint string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return false, c.err
	}
	return c.active[feature] == fingerprint, nil
}

type recordedRequest struct {
	Authorization string
	Body          map[string]any
}

func newFakeJev(t *testing.T, response string) (*httptest.Server, *[]recordedRequest) {
	t.Helper()
	var mu sync.Mutex
	recorded := make([]recordedRequest, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		recorded = append(recorded, recordedRequest{Authorization: r.Header.Get("Authorization"), Body: decoded})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)
	return server, &recorded
}

func serviceConfig(endpoint string) Config {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Endpoint = endpoint
	cfg.IdentityVerification = FeatureConfig{Enabled: true}
	return cfg
}

func TestPolicyFingerprintTracksWordingFieldsModelAndEndpoint(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	cfg := serviceConfig("https://api.typesafe.ai/v1/systemone")
	base, err := testSpec().Policy(cfg)
	require.NoError(err)
	assert.Len(base.Fingerprint, 64)
	same, err := testSpec().Policy(cfg)
	require.NoError(err)
	assert.Equal(base.Fingerprint, same.Fingerprint, "the fingerprint is deterministic")

	reordered := testSpec()
	reordered.StateFields = []string{"right", "left"}
	policy, err := reordered.Policy(cfg)
	require.NoError(err)
	assert.Equal(base.Fingerprint, policy.Fingerprint, "field order does not matter")

	worded := testSpec()
	worded.Questions[0].Instructions = "Could `left` be `right`?"
	policy, err = worded.Policy(cfg)
	require.NoError(err)
	assert.NotEqual(base.Fingerprint, policy.Fingerprint, "wording changes the policy")

	fields := testSpec()
	fields.StateFields = append(fields.StateFields, "email")
	policy, err = fields.Policy(cfg)
	require.NoError(err)
	assert.NotEqual(base.Fingerprint, policy.Fingerprint, "a new disclosed field changes the policy")

	model := cfg
	model.Model = "jev-2.0.0"
	policy, err = testSpec().Policy(model)
	require.NoError(err)
	assert.NotEqual(base.Fingerprint, policy.Fingerprint, "the model changes the policy")

	endpoint := cfg
	endpoint.Endpoint = "https://proxy.example.test/v1/systemone"
	policy, err = testSpec().Policy(endpoint)
	require.NoError(err)
	assert.NotEqual(base.Fingerprint, policy.Fingerprint, "the destination changes the policy")

	_, err = FeatureSpec{Name: "x"}.Policy(cfg)
	require.Error(err)
}

func TestServiceJudgeSendsExactPolicyOnlyWhenEveryGatePasses(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server, recorded := newFakeJev(t, `{"model":"jev-1.13.0","answers":{"same":{"type":"noul","noul":0.93}},"usage":{"input_tokens":50,"output_tokens":5}}`)
	cfg := serviceConfig(server.URL)
	consents := &fakeConsents{active: map[string]string{}}
	credential := "secret-key"
	var credentialReads atomic.Int32
	service, err := NewService(ServiceOptions{
		Config:   func() (Config, error) { return cfg, nil },
		Consents: consents,
		Credential: func(endpoint, apiKeyEnv string) (string, bool, error) {
			credentialReads.Add(1)
			assert.Equal(server.URL, endpoint)
			assert.Equal(DefaultAPIKeyEnv, apiKeyEnv)
			return credential, credential != "", nil
		},
	})
	require.NoError(err)
	state := map[string]any{"left": "Priya Ramanathan", "right": "Priya R."}

	_, err = service.Judge(context.Background(), testSpec(), false, state, time.Time{})
	require.ErrorIs(err, ErrConsentRequired)
	assert.Equal("consent_required", Skipped(err))
	assert.Empty(*recorded, "no consent means nothing is sent")

	policy, err := testSpec().Policy(cfg)
	require.NoError(err)
	consents.active[FeatureEnrichmentIdentity] = policy.Fingerprint

	_, err = service.Judge(context.Background(), testSpec(), true, state, time.Time{})
	require.ErrorIs(err, ErrAutomaticDisabled)
	assert.Equal("manual_only", Skipped(err))
	assert.Empty(*recorded, "automatic callers need automatic = true")

	credentialReads.Store(0)
	response, err := service.Judge(context.Background(), testSpec(), false, state, time.Time{})
	require.NoError(err)
	assert.InDelta(0.93, response.Answers["same"].Noul, 1e-9)
	assert.Equal(int32(1), credentialReads.Load(), "one judgment reads the credential store once")
	require.Len(*recorded, 1)
	sent := (*recorded)[0]
	assert.Equal("Bearer secret-key", sent.Authorization)
	assert.Equal(map[string]any{"left": "Priya Ramanathan", "right": "Priya R."}, sent.Body["state"])
	assert.Equal(DefaultModel, sent.Body["model"])
	assert.Equal(map[string]any{"same": map[string]any{
		"type": "noul", "instructions": "Is `left` the same person as `right`?",
		"criteria": map[string]any{"true": "Same person.", "false": "Different people."},
	}}, sent.Body["questions"], "the consented wording is what goes out")

	cfg.IdentityVerification.Automatic = true
	_, err = service.Judge(context.Background(), testSpec(), true, state, time.Time{})
	require.NoError(err)
	assert.Len(*recorded, 2)

	credential = ""
	_, err = service.Judge(context.Background(), testSpec(), false, state, time.Time{})
	require.ErrorIs(err, ErrCredentialMissing)
	assert.Equal("credential_missing", Skipped(err))
	credential = "secret-key"

	cfg.IdentityVerification.Enabled = false
	_, err = service.Judge(context.Background(), testSpec(), false, state, time.Time{})
	require.ErrorIs(err, ErrFeatureDisabled)
	cfg.IdentityVerification.Enabled = true

	cfg.Enabled = false
	_, err = service.Judge(context.Background(), testSpec(), false, state, time.Time{})
	require.ErrorIs(err, ErrDisabled)
	cfg.Enabled = true

	cfg.Model = "jev-2.0.0"
	_, err = service.Judge(context.Background(), testSpec(), false, state, time.Time{})
	require.ErrorIs(err, ErrConsentRequired, "a model change invalidates the consent")
	cfg.Model = DefaultModel

	consents.err = errors.New("database locked")
	_, err = service.Judge(context.Background(), testSpec(), false, state, time.Time{})
	require.ErrorIs(err, ErrPolicyUnavailable)
	assert.Equal("policy_unavailable", Skipped(err))
	assert.Len(*recorded, 2, "gate failures never reach the provider")
}

func TestServiceCachesTheCredentialAndPolicyUntilTheRevisionChanges(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server, recorded := newFakeJev(t, `{"model":"jev-1.13.0","answers":{"same":{"type":"noul","noul":0.5}},"usage":{"input_tokens":5,"output_tokens":5}}`)
	cfg := serviceConfig(server.URL)
	spec := testSpec()
	policy, err := spec.Policy(cfg)
	require.NoError(err)
	revision := "rev-1"
	var reads atomic.Int32
	service, err := NewService(ServiceOptions{
		Config:   func() (Config, error) { return cfg, nil },
		Consents: &fakeConsents{active: map[string]string{FeatureEnrichmentIdentity: policy.Fingerprint}},
		Credential: func(string, string) (string, bool, error) {
			reads.Add(1)
			return "key-" + revision, true, nil
		},
		CredentialRevision: func() (string, error) { return revision, nil },
	})
	require.NoError(err)
	state := map[string]any{"left": "a", "right": "b"}
	for range 3 {
		_, err = service.Judge(context.Background(), spec, false, state, time.Time{})
		require.NoError(err)
	}
	assert.Equal(int32(1), reads.Load(), "an unchanged revision reuses the resolved key")
	assert.Equal("Bearer key-rev-1", (*recorded)[2].Authorization)

	revision = "rev-2"
	_, err = service.Judge(context.Background(), spec, false, state, time.Time{})
	require.NoError(err)
	assert.Equal(int32(2), reads.Load(), "a changed revision rereads the store")
	assert.Equal("Bearer key-rev-2", (*recorded)[3].Authorization, "the new key is what gets sent")

	service.mu.Lock()
	cachedPolicies := len(service.policies)
	service.mu.Unlock()
	assert.Equal(1, cachedPolicies, "the policy is hashed once per feature and binding")
}

func TestServiceJudgeReportsProviderFailuresAsCategories(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "private provider body", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	cfg := serviceConfig(server.URL)
	spec := testSpec()
	policy, err := spec.Policy(cfg)
	require.NoError(err)
	budget := &Budget{MaxRequests: 100, FailureThreshold: 2, Cooldown: time.Hour}
	service, err := NewService(ServiceOptions{
		Config:     func() (Config, error) { return cfg, nil },
		Consents:   &fakeConsents{active: map[string]string{FeatureEnrichmentIdentity: policy.Fingerprint}},
		Credential: func(string, string) (string, bool, error) { return "k", true, nil },
		Budget:     budget,
	})
	require.NoError(err)
	for range 2 {
		_, err = service.Judge(context.Background(), spec, false, map[string]any{"left": "a", "right": "b"}, time.Time{})
		require.Error(err)
		assert.Equal("provider_error", Skipped(err))
		assert.NotContains(err.Error(), "private provider body")
	}
	_, err = service.Judge(context.Background(), spec, false, map[string]any{"left": "a", "right": "b"}, time.Time{})
	require.ErrorIs(err, ErrBreakerOpen)
	assert.Equal("breaker_open", Skipped(err))
	assert.Equal(2, budget.State().ConsecutiveFailures)
}

func TestSafeAnswersAndQuestionText(t *testing.T) {
	assert := assert.New(t)
	answers := SafeAnswers(map[string]Answer{
		"b": {Type: QuestionChoice, Choice: "x", Probabilities: map[string]float64{"x": 0.9, "y": 0.1}, Confidence: 0.8},
		"a": {Type: QuestionNoul, Noul: 0.4},
	})
	assert.Equal("a", answers[0].ID)
	assert.Equal("b", answers[1].ID)
	assert.Equal("x", answers[1].Choice)
	assert.Equal("plain text", QuestionText("plain text"))
	assert.JSONEq(`{"focus":"names","question":"Same?"}`, QuestionText(map[string]any{"question": "Same?", "focus": "names"}))
}

// TestServiceRebindingPricesDoesNotRaceInFlightLedgerWrites runs judgments
// concurrently while the configured prices alternate, which makes the
// service rebind the shared budget's prices under its lock on almost every
// call. Under -race, a client that read those prices unlocked to price its
// ledger write fails here. Each recorded cost must match one price whole.
func TestServiceRebindingPricesDoesNotRaceInFlightLedgerWrites(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server, _ := newFakeJev(t, `{"model":"jev-1.13.0","answers":{"same":{"type":"noul","noul":0.5}},"usage":{"input_tokens":100,"output_tokens":10}}`)
	base := serviceConfig(server.URL)
	spec := testSpec()
	policy, err := spec.Policy(base)
	require.NoError(err)
	var calls atomic.Int64
	ledger := &fakeLedger{}
	service, err := NewService(ServiceOptions{
		Config: func() (Config, error) {
			cfg := base
			if calls.Add(1)%2 == 0 {
				cfg.InputUSDPerMillionTokens, cfg.OutputUSDPerMillionTokens = 1, 1
			} else {
				cfg.InputUSDPerMillionTokens, cfg.OutputUSDPerMillionTokens = 2, 4
			}
			return cfg, nil
		},
		Consents:   &fakeConsents{active: map[string]string{FeatureEnrichmentIdentity: policy.Fingerprint}},
		Credential: func(string, string) (string, bool, error) { return "k", true, nil },
		Ledger:     ledger,
	})
	require.NoError(err)
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			for range 4 {
				_, judgeErr := service.Judge(context.Background(), spec, false, map[string]any{"left": "a", "right": "b"}, time.Time{})
				assert.NoError(judgeErr)
			}
		})
	}
	group.Wait()
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	require.Len(ledger.usage, 64)
	for _, usage := range ledger.usage {
		assert.Contains([]int64{110, 240}, usage.CostUSDMicros, "each request is priced by exactly one binding")
	}
}

func TestSkippedClassifiesEveryGateAndBudgetOutcome(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{ErrDisabled, "disabled"},
		{ErrFeatureDisabled, "feature_disabled"},
		{ErrUnknownFeature, "feature_disabled"},
		{ErrAutomaticDisabled, "manual_only"},
		{ErrConsentRequired, "consent_required"},
		{ErrCredentialMissing, "credential_missing"},
		{ErrPolicyUnavailable, "policy_unavailable"},
		{ErrBreakerOpen, "breaker_open"},
		{ErrRunHalted, "run_halted"},
		{ErrRequestLimit, "request_limit"},
		{ErrDayRequestLimit, "request_limit"},
		{ErrCostStop, "cost_limit"},
		{ErrDayCostStop, "cost_limit"},
		{ErrUsageUnknown, "cost_limit"},
		{context.DeadlineExceeded, "timeout"},
		{ErrRequestBounds, "request_bounds"},
		{ErrInvalidResponse, "invalid_response"},
		{httpStatusError(503), "provider_error"},
	}
	for _, tc := range cases {
		t.Run(tc.err.Error(), func(t *testing.T) {
			assert.Equal(t, tc.want, Skipped(fmt.Errorf("jev requests failed: %w", tc.err)))
		})
	}
}

func TestServiceJudgeQuestionsSendsOnlyTheNamedConsentedQuestions(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server, recorded := newFakeJev(t, `{"model":"jev-1.13.0","answers":{"second":{"type":"noul","noul":0.4}}}`)
	cfg := serviceConfig(server.URL)
	cfg.OrganizationResolution = FeatureConfig{Enabled: true}
	spec := FeatureSpec{
		Name: FeatureOrganizationResolution, Title: "Test pairs", Purpose: "Compare slots.",
		Questions: []Question{
			{ID: "first", Type: QuestionNoul, Instructions: "Is `slot_1` true?"},
			{ID: "second", Type: QuestionNoul, Instructions: "Is `slot_2` true?"},
		},
		StateFields: []string{"slot_1", "slot_2"},
	}
	policy, err := spec.Policy(cfg)
	require.NoError(err)
	service, err := NewService(ServiceOptions{
		Config:     func() (Config, error) { return cfg, nil },
		Consents:   &fakeConsents{active: map[string]string{FeatureOrganizationResolution: policy.Fingerprint}},
		Credential: func(string, string) (string, bool, error) { return "secret-key", true, nil },
	})
	require.NoError(err)

	response, err := service.JudgeQuestions(t.Context(), spec, false,
		map[string]any{"slot_2": "yes"}, []string{"second"}, time.Time{})
	require.NoError(err)
	assert.InDelta(0.4, response.Answers["second"].Noul, 1e-9)
	require.Len(*recorded, 1)
	assert.Equal(map[string]any{"second": map[string]any{
		"type": "noul", "instructions": "Is `slot_2` true?",
	}}, (*recorded)[0].Body["questions"])

	for name, ids := range map[string][]string{
		"unknown question":  {"third"},
		"repeated question": {"second", "second"},
	} {
		_, err = service.JudgeQuestions(t.Context(), spec, false, map[string]any{}, ids, time.Time{})
		require.ErrorIs(err, ErrRequestBounds, name)
	}
	assert.Len(*recorded, 1, "a question outside the policy never leaves")
}
