package jev

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"
)

// FeatureEnrichmentIdentity is the enrichment identity verification feature.
const FeatureEnrichmentIdentity = "enrichment_identity"

// Gate outcomes. Each is an expected administrative state, not a fault: the
// caller falls back to its pre-Jev decision and reports the category.
var (
	ErrDisabled           = errors.New("jev is disabled; set [jev] enabled = true")
	ErrFeatureDisabled    = errors.New("jev feature is disabled in [jev]")
	ErrAutomaticDisabled  = errors.New("jev feature does not allow automatic use; set automatic = true")
	ErrConsentRequired    = errors.New("jev feature requires active consent for its current policy; run `msgvault jev consent <feature>`")
	ErrCredentialMissing  = errors.New("jev API key is not configured")
	ErrPolicyUnavailable  = errors.New("jev policy is unavailable")
	ErrUnknownFeature     = errors.New("jev feature is not configured in [jev]")
	errServiceUnavailable = errors.New("jev service is not configured")
)

// FeatureConfigFor returns the [jev] sub-section that gates a feature name.
func (c Config) FeatureConfigFor(name string) (FeatureConfig, bool) {
	switch name {
	case FeatureEnrichmentIdentity:
		return c.IdentityVerification, true
	default:
		return FeatureConfig{}, false
	}
}

// ConsentChecker is the narrow store authority the gate consults on every
// request. *store.Store implements it.
type ConsentChecker interface {
	HasActiveJevFeatureConsent(ctx context.Context, feature, fingerprint string) (bool, error)
}

// ConfigSource returns the current configuration. In a long-running process
// it must not be a startup snapshot.
type ConfigSource func() (Config, error)

// CredentialSource resolves the API key for an endpoint. It returns ok=false
// when nothing is configured and never logs the value.
type CredentialSource func(endpoint, apiKeyEnv string) (key string, ok bool, err error)

// Inactive reports the expected administrative states that skip a judgment
// without being a provider fault.
func Inactive(err error) bool {
	return errors.Is(err, ErrDisabled) || errors.Is(err, ErrFeatureDisabled) ||
		errors.Is(err, ErrAutomaticDisabled) || errors.Is(err, ErrConsentRequired) ||
		errors.Is(err, ErrCredentialMissing) || errors.Is(err, ErrUnknownFeature)
}

// Skipped classifies why a judgment did not happen, for the caller's
// `jev: skipped:<category>` report. It never includes state.
func Skipped(err error) string {
	switch {
	case errors.Is(err, ErrDisabled):
		return "disabled"
	case errors.Is(err, ErrFeatureDisabled), errors.Is(err, ErrUnknownFeature):
		return "feature_disabled"
	case errors.Is(err, ErrAutomaticDisabled):
		return "manual_only"
	case errors.Is(err, ErrConsentRequired):
		return "consent_required"
	case errors.Is(err, ErrCredentialMissing):
		return "credential_missing"
	case errors.Is(err, ErrPolicyUnavailable), errors.Is(err, errServiceUnavailable):
		return "policy_unavailable"
	case errors.Is(err, ErrBreakerOpen):
		return "breaker_open"
	case errors.Is(err, ErrDayRequestLimit), errors.Is(err, ErrRequestLimit):
		return "request_limit"
	case errors.Is(err, ErrDayCostStop), errors.Is(err, ErrCostStop), errors.Is(err, ErrUsageUnknown):
		return "cost_limit"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	case errors.Is(err, ErrRequestBounds):
		return "request_bounds"
	case errors.Is(err, ErrInvalidResponse):
		return "invalid_response"
	default:
		return "provider_error"
	}
}

// ServiceOptions wire a Service to configuration, consent, accounting, and
// credentials. Budget and Transport are optional.
type ServiceOptions struct {
	Config     ConfigSource
	Consents   ConsentChecker
	Ledger     Ledger
	Credential CredentialSource
	Transport  http.RoundTripper
	Budget     *Budget
	Now        func() time.Time
	Logger     *slog.Logger
}

// Service is the one door every feature goes through. It rechecks
// configuration, consent, and credentials immediately before each request,
// charges the feature's daily counters, and logs only safe metadata.
type Service struct {
	options ServiceOptions
	mu      sync.Mutex
	client  *Client
	bound   clientBinding
}

type clientBinding struct {
	endpoint string
	model    string
	key      string
	timeout  time.Duration
	limits   DayLimits
	inputUSD float64
	outUSD   float64
}

// NewService validates the wiring. Construction performs no I/O.
func NewService(options ServiceOptions) (*Service, error) {
	if options.Config == nil || options.Consents == nil || options.Credential == nil {
		return nil, errors.New("jev service requires config, consent, and credential sources")
	}
	if options.Budget == nil {
		options.Budget = &Budget{MaxRequests: 1 << 30}
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Service{options: options}, nil
}

// Policy returns the feature's current policy without checking enablement,
// so a disabled feature stays auditable and revocable.
func (s *Service) Policy(spec FeatureSpec) (Policy, error) {
	if s == nil {
		return Policy{}, errServiceUnavailable
	}
	cfg, err := s.options.Config()
	if err != nil {
		return Policy{}, fmt.Errorf("%w: %w", ErrPolicyUnavailable, err)
	}
	return spec.Policy(cfg)
}

// Check reports whether the feature may send right now and returns the
// policy the request would run under. automatic marks unattended callers.
func (s *Service) Check(ctx context.Context, spec FeatureSpec, automatic bool) (Policy, error) {
	if s == nil {
		return Policy{}, errServiceUnavailable
	}
	cfg, err := s.options.Config()
	if err != nil {
		return Policy{}, fmt.Errorf("%w: %w", ErrPolicyUnavailable, err)
	}
	policy, err := spec.Policy(cfg)
	if err != nil {
		return Policy{}, fmt.Errorf("%w: %w", ErrPolicyUnavailable, err)
	}
	if !cfg.Enabled {
		return policy, ErrDisabled
	}
	feature, known := cfg.FeatureConfigFor(spec.Name)
	if !known {
		return policy, fmt.Errorf("%w: %s", ErrUnknownFeature, spec.Name)
	}
	if !feature.Enabled {
		return policy, fmt.Errorf("%w: %s", ErrFeatureDisabled, spec.Name)
	}
	if automatic && !feature.Automatic {
		return policy, fmt.Errorf("%w: %s", ErrAutomaticDisabled, spec.Name)
	}
	if _, ok, err := s.options.Credential(cfg.Endpoint, cfg.APIKeyEnv); err != nil {
		return policy, fmt.Errorf("%w: resolve credential: %w", ErrPolicyUnavailable, err)
	} else if !ok {
		return policy, ErrCredentialMissing
	}
	active, err := s.options.Consents.HasActiveJevFeatureConsent(ctx, spec.Name, policy.Fingerprint)
	if err != nil {
		return policy, fmt.Errorf("%w: check consent: %w", ErrPolicyUnavailable, err)
	}
	if !active {
		return policy, fmt.Errorf("%w (fingerprint %s)", ErrConsentRequired, policy.Fingerprint)
	}
	return policy, nil
}

// Judge asks the feature's questions about one state. Every gate is
// rechecked first; nothing leaves the process on any error. The state must
// contain only the fields the spec discloses; that is the caller's contract.
func (s *Service) Judge(ctx context.Context, spec FeatureSpec, automatic bool, state any, deadline time.Time) (Response, error) {
	policy, err := s.Check(ctx, spec, automatic)
	if err != nil {
		return Response{}, err
	}
	cfg, err := s.options.Config()
	if err != nil {
		return Response{}, fmt.Errorf("%w: %w", ErrPolicyUnavailable, err)
	}
	key, ok, err := s.options.Credential(cfg.Endpoint, cfg.APIKeyEnv)
	if err != nil {
		return Response{}, fmt.Errorf("%w: resolve credential: %w", ErrPolicyUnavailable, err)
	}
	if !ok {
		return Response{}, ErrCredentialMissing
	}
	client, err := s.clientFor(cfg, key)
	if err != nil {
		return Response{}, err
	}
	started := s.options.Now()
	response, err := client.Ask(ctx, Request{
		State: state, Questions: policy.Questions, Deadline: deadline, Feature: spec.Name,
	})
	latency := s.options.Now().Sub(started)
	if err != nil {
		s.options.Logger.Debug("jev judgment failed",
			"feature", spec.Name, "category", Skipped(err), "latency_ms", latency.Milliseconds(),
			"budget", client.BudgetState())
		return response, err
	}
	s.options.Logger.Debug("jev judgment",
		"feature", spec.Name, "model", response.Model, "latency_ms", latency.Milliseconds(),
		"input_tokens", tokenValue(response.Usage.InputTokens),
		"output_tokens", tokenValue(response.Usage.OutputTokens),
		"answers", SafeAnswers(response.Answers), "budget", client.BudgetState())
	return response, nil
}

// BudgetState snapshots the live client's budget, or a zero state when no
// request has been built yet.
func (s *Service) BudgetState() BudgetState {
	if s == nil {
		return BudgetState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil {
		return s.options.Budget.State()
	}
	return s.client.BudgetState()
}

func (s *Service) clientFor(cfg Config, key string) (*Client, error) {
	binding := clientBinding{
		endpoint: cfg.Endpoint, model: cfg.Model, key: key, timeout: cfg.RequestTimeout,
		limits: cfg.DayLimits(), inputUSD: cfg.InputUSDPerMillionTokens, outUSD: cfg.OutputUSDPerMillionTokens,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil && s.bound == binding {
		return s.client, nil
	}
	s.options.Budget.mu.Lock()
	s.options.Budget.InputUSDPerM = cfg.InputUSDPerMillionTokens
	s.options.Budget.OutputUSDPerM = cfg.OutputUSDPerMillionTokens
	// The in-process cost stop mirrors the daily cap: the budget accounts
	// spend per UTC day, so a long-running daemon is never disabled for good.
	s.options.Budget.StopUSD = cfg.MaxCostUSDPerDay
	s.options.Budget.mu.Unlock()
	client, err := NewClient(Options{
		Endpoint: cfg.Endpoint, Model: cfg.Model, APIKey: key, Transport: s.options.Transport,
		Budget: s.options.Budget, Ledger: s.options.Ledger, DayLimits: binding.limits,
		Now: s.options.Now, RequestTimeout: cfg.RequestTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPolicyUnavailable, err)
	}
	s.client = client
	s.bound = binding
	return client, nil
}

func tokenValue(value *int64) int64 {
	if value == nil {
		return -1
	}
	return *value
}

// SafeAnswer is the loggable shape of one answer: type and numbers only.
type SafeAnswer struct {
	ID            string             `json:"id"`
	Type          QuestionType       `json:"type"`
	Noul          float64            `json:"noul,omitzero"`
	Choice        string             `json:"choice,omitzero"`
	Score         float64            `json:"score,omitzero"`
	Confidence    float64            `json:"confidence,omitzero"`
	Probabilities map[string]float64 `json:"probabilities,omitzero"`
}

// SafeAnswers renders answers for logs in a stable order. Option keys are
// the caller's own labels, never state content.
func SafeAnswers(answers map[string]Answer) []SafeAnswer {
	out := make([]SafeAnswer, 0, len(answers))
	for id, answer := range answers {
		out = append(out, SafeAnswer{
			ID: id, Type: answer.Type, Noul: answer.Noul, Choice: answer.Choice,
			Score: answer.Score, Confidence: answer.Confidence, Probabilities: answer.Probabilities,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
