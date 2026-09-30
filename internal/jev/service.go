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

// FeatureOrganizationResolution is the organization resolution and job
// title equivalence feature.
const FeatureOrganizationResolution = "organization_resolution"

// FeatureCorrespondentKind is the correspondent kind classification feature.
const FeatureCorrespondentKind = "correspondent_kind"

// FeatureCleanupSuggestions is the cleanup suggestion (junk and phishing)
// feature.
const FeatureCleanupSuggestions = "cleanup_suggestions"

// FeatureMeetingEventKind is the calendar event kind feature.
const FeatureMeetingEventKind = "meeting_event_kind"

// FeatureSearchRerank is the hybrid search reranking feature. It sends
// message text, so its consent disclosure says so.
const FeatureSearchRerank = "search_rerank"

// FeatureMeetingActionAssignee is the meeting action item assignee feature.
const FeatureMeetingActionAssignee = "meeting_action_assignee"

// FeatureQueryUnderstanding is the Explore query understanding feature.
const FeatureQueryUnderstanding = "query_understanding"

// FeatureSweepEvidenceRerank is the people sweep context relevance feature.
// It sends message excerpts, so its consent disclosure says so.
const FeatureSweepEvidenceRerank = "sweep_evidence_rerank"

// FeatureSweepClaimGrounding is the people sweep claim grounding feature. It
// sends message excerpts, so its consent disclosure says so.
const FeatureSweepClaimGrounding = "sweep_claim_grounding"

// FeatureDuplicatePeople is the duplicate-person candidate feature. It sends
// display names and email addresses, so its consent disclosure says so.
const FeatureDuplicatePeople = "person_duplicates"

// FeaturePersonProfileChoices is the person profile choices feature: the
// primary current role, a promoted person's display name, and merge
// attribute conflicts.
const FeaturePersonProfileChoices = "person_profile_choices"

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
	case FeatureOrganizationResolution:
		return c.OrganizationResolution, true
	case FeatureCorrespondentKind:
		return c.CorrespondentKind, true
	case FeatureCleanupSuggestions:
		return c.CleanupSuggestions.Feature(), true
	case FeatureSearchRerank:
		// Reranking only ever runs for a person's own interactive search.
		return FeatureConfig{Enabled: c.Rerank.Enabled}, true
	case FeatureMeetingEventKind:
		return c.MeetingEventKind, true
	case FeatureMeetingActionAssignee:
		return c.MeetingActionAssignee, true
	case FeatureQueryUnderstanding:
		// Only a person's own typed Explore search asks, so there is no
		// automatic use.
		return FeatureConfig{Enabled: c.QueryUnderstanding.Enabled}, true
	case FeatureSweepEvidenceRerank:
		return c.SweepEvidenceRerank, true
	case FeatureSweepClaimGrounding:
		return c.SweepClaimGrounding, true
	case FeatureDuplicatePeople:
		return c.PersonDuplicates, true
	case FeaturePersonProfileChoices:
		return c.PersonProfileChoices, true
	default:
		return FeatureConfig{}, false
	}
}

// ConsentChecker is the narrow store authority the gate consults on every
// request. *store.Store implements it.
type ConsentChecker interface {
	HasActiveJevFeatureConsent(ctx context.Context, feature, fingerprint string) (bool, error)
}

// ConfigSource returns the [jev] configuration the service runs under. The
// daemon supplies the snapshot it started with: [jev] edits take effect
// after a restart, which is what the settings API reports for every jev.*
// key. Consent and the credential are not part of the snapshot; both are
// rechecked live on every request, so `msgvault jev revoke` and a key
// pasted in Settings act at once.
type ConfigSource func() (Config, error)

// CredentialSource resolves the API key for an endpoint. It returns ok=false
// when nothing is configured and never logs the value.
type CredentialSource func(endpoint, apiKeyEnv string) (key string, ok bool, err error)

// CredentialRevision is a cheap probe for "has the credential store changed":
// any string that differs whenever the stored key may differ (for example the
// store file's size and modification time). When a service has one, the
// resolved key is cached until the revision changes; without one the store
// is read on every judgment.
type CredentialRevision func() (string, error)

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
	case errors.Is(err, ErrRunHalted):
		return "run_halted"
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
// credentials. Budget, Transport, and CredentialRevision are optional.
type ServiceOptions struct {
	Config             ConfigSource
	Consents           ConsentChecker
	Ledger             Ledger
	Credential         CredentialSource
	CredentialRevision CredentialRevision
	Transport          http.RoundTripper
	Budget             *Budget
	Now                func() time.Time
	Logger             *slog.Logger
}

// Service is the one door every feature goes through. It rechecks
// configuration, consent, and credentials immediately before each request,
// charges the feature's daily counters, and logs only safe metadata. The
// policy fingerprint and the resolved key are cached per binding: a policy
// is rehashed only when the endpoint or model changes, and the credential
// store is reread only when its revision changes.
type Service struct {
	options    ServiceOptions
	mu         sync.Mutex
	client     *Client
	bound      clientBinding
	policies   map[policyKey]Policy
	credential cachedCredential
}

// policyKey is what a feature's fingerprint depends on besides the spec.
type policyKey struct {
	feature  string
	endpoint string
	model    string
}

// cachedCredential is one resolved key with the store revision and endpoint
// binding it was resolved under.
type cachedCredential struct {
	valid     bool
	revision  string
	endpoint  string
	apiKeyEnv string
	key       string
	ok        bool
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
	return &Service{options: options, policies: make(map[policyKey]Policy)}, nil
}

// policyFor returns the feature's policy under cfg, hashing it once per
// (feature, endpoint, model).
func (s *Service) policyFor(spec FeatureSpec, cfg Config) (Policy, error) {
	key := policyKey{feature: spec.Name, endpoint: cfg.Endpoint, model: cfg.Model}
	s.mu.Lock()
	policy, ok := s.policies[key]
	s.mu.Unlock()
	if ok {
		return policy, nil
	}
	policy, err := spec.Policy(cfg)
	if err != nil {
		return Policy{}, err
	}
	s.mu.Lock()
	s.policies[key] = policy
	s.mu.Unlock()
	return policy, nil
}

// credentialFor resolves the key for cfg's endpoint, reusing the last
// resolution while the credential store's revision and the binding are
// unchanged. Without a revision probe every call reads the store.
func (s *Service) credentialFor(cfg Config) (string, bool, error) {
	revision := ""
	if s.options.CredentialRevision != nil {
		probed, err := s.options.CredentialRevision()
		if err != nil {
			return "", false, err
		}
		revision = probed
		s.mu.Lock()
		cached := s.credential
		s.mu.Unlock()
		if cached.valid && cached.revision == revision &&
			cached.endpoint == cfg.Endpoint && cached.apiKeyEnv == cfg.APIKeyEnv {
			return cached.key, cached.ok, nil
		}
	}
	key, ok, err := s.options.Credential(cfg.Endpoint, cfg.APIKeyEnv)
	if err != nil {
		return "", false, err
	}
	if s.options.CredentialRevision != nil {
		s.mu.Lock()
		s.credential = cachedCredential{
			valid: true, revision: revision, endpoint: cfg.Endpoint, apiKeyEnv: cfg.APIKeyEnv, key: key, ok: ok,
		}
		s.mu.Unlock()
	}
	return key, ok, nil
}

// clearance is everything one passed gate resolved: the configuration and
// policy the request runs under and the credential it sends. Judge reuses it
// so the credential store and the fingerprint are read once per request.
type clearance struct {
	config Config
	policy Policy
	key    string
}

// check runs every gate once and hands back what it resolved. On error the
// policy is still returned when it could be computed, so callers can report
// the fingerprint consent is missing for.
func (s *Service) check(ctx context.Context, spec FeatureSpec, automatic bool) (clearance, error) {
	if s == nil {
		return clearance{}, errServiceUnavailable
	}
	cfg, err := s.options.Config()
	if err != nil {
		return clearance{}, fmt.Errorf("%w: %w", ErrPolicyUnavailable, err)
	}
	policy, err := s.policyFor(spec, cfg)
	if err != nil {
		return clearance{}, fmt.Errorf("%w: %w", ErrPolicyUnavailable, err)
	}
	cleared := clearance{config: cfg, policy: policy}
	if !cfg.Enabled {
		return cleared, ErrDisabled
	}
	feature, known := cfg.FeatureConfigFor(spec.Name)
	if !known {
		return cleared, fmt.Errorf("%w: %s", ErrUnknownFeature, spec.Name)
	}
	if !feature.Enabled {
		return cleared, fmt.Errorf("%w: %s", ErrFeatureDisabled, spec.Name)
	}
	if automatic && !feature.Automatic {
		return cleared, fmt.Errorf("%w: %s", ErrAutomaticDisabled, spec.Name)
	}
	key, ok, err := s.credentialFor(cfg)
	if err != nil {
		return cleared, fmt.Errorf("%w: resolve credential: %w", ErrPolicyUnavailable, err)
	}
	if !ok || key == "" {
		return cleared, ErrCredentialMissing
	}
	active, err := s.options.Consents.HasActiveJevFeatureConsent(ctx, spec.Name, policy.Fingerprint)
	if err != nil {
		return cleared, fmt.Errorf("%w: check consent: %w", ErrPolicyUnavailable, err)
	}
	if !active {
		return cleared, fmt.Errorf("%w (fingerprint %s)", ErrConsentRequired, policy.Fingerprint)
	}
	cleared.key = key
	return cleared, nil
}

// Judge asks the feature's questions about one state. Every gate is
// rechecked first; nothing leaves the process on any error. The state must
// contain only the fields the spec discloses; that is the caller's contract.
func (s *Service) Judge(ctx context.Context, spec FeatureSpec, automatic bool, state any, deadline time.Time) (Response, error) {
	return s.JudgeQuestions(ctx, spec, automatic, state, nil, deadline)
}

// JudgeQuestions is Judge restricted to some of the feature's consented
// questions, for a feature whose state does not need every question each
// time (for example a fixed number of pair slots of which only a few are
// filled). questionIDs must name questions of the spec; nil or empty asks
// every question. The questions are sent exactly as the policy words them,
// so a subset stays within the consented policy.
func (s *Service) JudgeQuestions(
	ctx context.Context, spec FeatureSpec, automatic bool, state any, questionIDs []string, deadline time.Time,
) (Response, error) {
	cleared, err := s.check(ctx, spec, automatic)
	if err != nil {
		return Response{}, err
	}
	policy := cleared.policy
	questions, err := policyQuestionSubset(policy, questionIDs)
	if err != nil {
		return Response{}, err
	}
	client, err := s.clientFor(cleared.config, cleared.key)
	if err != nil {
		return Response{}, err
	}
	started := s.options.Now()
	response, err := client.Ask(ctx, Request{
		State: state, Questions: questions, Deadline: deadline, Feature: spec.Name,
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

// Admit runs every gate for one caller without sending anything: the
// configuration, the feature switch, the credential, active consent for the
// current policy, and the in-process budget (breaker, run halt, cost stop).
// It returns the admitted policy fingerprint.
func (s *Service) Admit(ctx context.Context, spec FeatureSpec, automatic bool) (string, error) {
	cleared, err := s.check(ctx, spec, automatic)
	if err != nil {
		return "", err
	}
	if err := s.options.Budget.preflight(1); err != nil {
		return "", err
	}
	return cleared.policy.Fingerprint, nil
}

// Judgment is one state and the consented questions to ask about it, for
// JudgeAll. Nil or empty QuestionIDs asks every question of the policy.
type Judgment struct {
	State       any
	QuestionIDs []string
}

// JudgeAll asks several independent judgments of one feature concurrently,
// after one pass through every gate. Each judgment's questions are taken
// from the consented policy by ID, so no other wording can be sent. The
// result aligns with judgments; usage covers every attempted request even
// on error. Like Judge, nothing leaves the process on a gate error.
func (s *Service) JudgeAll(
	ctx context.Context, spec FeatureSpec, automatic bool, judgments []Judgment, deadline time.Time,
) (BatchResult, error) {
	cleared, err := s.check(ctx, spec, automatic)
	if err != nil {
		return emptyBatch(), err
	}
	requests := make([]Request, len(judgments))
	for i, judgment := range judgments {
		questions, err := policyQuestionSubset(cleared.policy, judgment.QuestionIDs)
		if err != nil {
			return emptyBatch(), err
		}
		requests[i] = Request{State: judgment.State, Questions: questions, Deadline: deadline, Feature: spec.Name}
	}
	client, err := s.clientFor(cleared.config, cleared.key)
	if err != nil {
		return emptyBatch(), err
	}
	started := s.options.Now()
	result, err := client.AskAll(ctx, requests)
	latency := s.options.Now().Sub(started)
	if err != nil {
		s.options.Logger.Debug("jev judgments failed",
			"feature", spec.Name, "category", Skipped(err), "requests", result.Usage.Requests,
			"latency_ms", latency.Milliseconds(), "budget", client.BudgetState())
		return result, err
	}
	s.options.Logger.Debug("jev judgments",
		"feature", spec.Name, "requests", result.Usage.Requests, "latency_ms", latency.Milliseconds(),
		"input_tokens", tokenValue(result.Usage.InputTokens),
		"output_tokens", tokenValue(result.Usage.OutputTokens), "budget", client.BudgetState())
	return result, nil
}

// policyQuestionSubset returns the policy's questions named by ids, in
// policy order. An unknown or repeated id is a request-bounds error so a
// caller can never send wording the policy does not cover.
func policyQuestionSubset(policy Policy, ids []string) ([]Question, error) {
	if len(ids) == 0 {
		return policy.Questions, nil
	}
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := wanted[id]; duplicate {
			return nil, fmt.Errorf("%w: question %q requested twice", ErrRequestBounds, id)
		}
		wanted[id] = struct{}{}
	}
	questions := make([]Question, 0, len(ids))
	for _, question := range policy.Questions {
		if _, ok := wanted[question.ID]; ok {
			questions = append(questions, question)
		}
	}
	if len(questions) != len(wanted) {
		return nil, fmt.Errorf("%w: a requested question is not in the %s policy", ErrRequestBounds, policy.Feature)
	}
	return questions, nil
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
