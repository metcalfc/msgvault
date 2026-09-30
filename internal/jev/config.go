package jev

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	// DefaultAPIKeyEnv names the environment variable read when no stored
	// credential exists.
	DefaultAPIKeyEnv = "TYPESAFE_API_KEY" // #nosec G101 -- variable name, not a credential.
	// DefaultMaxRequestsPerDay caps every feature's requests per UTC day.
	DefaultMaxRequestsPerDay = 500
	// DefaultMaxCostUSDPerDay caps every feature's measured spend per UTC day.
	DefaultMaxCostUSDPerDay = 1.0
)

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Config is the [jev] section. Everything is off by default; a feature also
// needs its own section enabled and an active consent before it may send.
//
// Decode over DefaultConfig so an omitted daily limit takes its default while
// an explicit 0 keeps its documented meaning of "no cap". ApplyDefaults never
// rewrites a numeric limit.
//
//nolint:recvcheck // defaults mutate while validation reads the resulting value.
type Config struct {
	Enabled                   bool          `toml:"enabled"`
	Endpoint                  string        `toml:"endpoint"`
	Model                     string        `toml:"model"`
	APIKeyEnv                 string        `toml:"api_key_env"`
	RequestTimeout            time.Duration `toml:"request_timeout"`
	MaxRequestsPerDay         int64         `toml:"max_requests_per_day"`
	MaxCostUSDPerDay          float64       `toml:"max_cost_usd_per_day"`
	InputUSDPerMillionTokens  float64       `toml:"input_usd_per_million_tokens"`
	OutputUSDPerMillionTokens float64       `toml:"output_usd_per_million_tokens"`
	// IdentityVerification is [jev.identity_verification]: the enrichment
	// identity check.
	IdentityVerification FeatureConfig `toml:"identity_verification"`
	// OrganizationResolution is [jev.organization_resolution]: matching a
	// missed organization name to an existing organization and deciding
	// whether two job titles name the same role.
	OrganizationResolution FeatureConfig `toml:"organization_resolution"`
	// CorrespondentKind is [jev.correspondent_kind]: classifying which
	// archive identities are people, lists, shared mailboxes, or automated
	// senders.
	CorrespondentKind FeatureConfig `toml:"correspondent_kind"`
	// MeetingEventKind is [jev.meeting_event_kind]: classifying a calendar
	// series as a one-on-one, a working meeting, an all-hands, a webinar, a
	// hold, or a social event, so relationship rankings weigh it fairly.
	MeetingEventKind FeatureConfig `toml:"meeting_event_kind"`
	// MeetingActionAssignee is [jev.meeting_action_assignee]: inferring
	// which attendee owns a meeting action item the meeting tool left
	// unassigned.
	MeetingActionAssignee FeatureConfig `toml:"meeting_action_assignee"`
	// SweepEvidenceRerank is [jev.sweep_evidence_rerank]: asking which
	// retrieved messages bear on a fact before the people sweep sends them
	// to its chat model. Automatic covers the daemon's scheduled sweeps.
	SweepEvidenceRerank FeatureConfig `toml:"sweep_evidence_rerank"`
	// CleanupSuggestions is [jev.cleanup_suggestions]: judging junk and
	// phishing candidates for `msgvault suggest-cleanup`. It only ever runs
	// on request, so its automatic switch has no effect.
	CleanupSuggestions CleanupSuggestionsConfig `toml:"cleanup_suggestions"`
	// Rerank is [jev.rerank]: reordering the leading hybrid search results
	// by asking Jev which candidates answer the query. It never runs for
	// full-text or automatic searches, so it has no automatic switch.
	Rerank RerankConfig `toml:"rerank"`
	// QueryUnderstanding is [jev.query_understanding]: suggesting Explore
	// filters for a typed search. Only the Web UI asks, for a query the
	// person typed, so it has no automatic switch.
	QueryUnderstanding QueryUnderstandingConfig `toml:"query_understanding"`
}

// QueryUnderstandingConfig is [jev.query_understanding]. Enabled is off by
// default.
type QueryUnderstandingConfig struct {
	Enabled bool `toml:"enabled"`
}

// CleanupSuggestionsConfig is [jev.cleanup_suggestions]. Beyond the feature
// switches it names the receiving servers whose Authentication-Results are
// trusted for non-Gmail sources.
type CleanupSuggestionsConfig struct {
	Enabled   bool `toml:"enabled"`
	Automatic bool `toml:"automatic"`
	// TrustedAuthservIDs are authserv-ids (for example mx.example.com) whose
	// Authentication-Results headers are believed for any source. Gmail
	// sources always trust mx.google.com; everything else is unknown.
	TrustedAuthservIDs []string `toml:"trusted_authserv_ids"`
}

// Feature returns the feature switches.
func (c CleanupSuggestionsConfig) Feature() FeatureConfig {
	return FeatureConfig{Enabled: c.Enabled, Automatic: c.Automatic}
}

// Rerank request shapes. Batched asks about every candidate in one request;
// per_candidate sends one request per candidate.
const (
	RerankShapeBatched      = "batched"
	RerankShapePerCandidate = "per_candidate"
	// MaxRerankTop is the most leading results one search may rerank, and
	// the default.
	MaxRerankTop = 30
)

// RerankConfig is [jev.rerank]. Enabled is off by default.
type RerankConfig struct {
	Enabled bool `toml:"enabled"`
	// Shape is batched or per_candidate.
	Shape string `toml:"shape"`
	// Top is how many leading hybrid results are reranked, 2 to 30.
	Top int `toml:"top"`
	// MessageTypesExcluded lists message types (for example "whatsapp")
	// whose text is never sent; such results keep their fused position.
	MessageTypesExcluded []string `toml:"message_types_excluded"`
	// MCP lets MCP clients' hybrid searches ask for reranking. It is off by
	// default because an assistant may search unattended.
	MCP bool `toml:"mcp"`
}

// FeatureConfig gates one Jev-backed feature. Automatic additionally allows
// unattended paths (scheduled jobs, sync, cache builds) to send.
type FeatureConfig struct {
	Enabled   bool `toml:"enabled"`
	Automatic bool `toml:"automatic"`
}

// DefaultConfig returns the section with every default filled, including
// the daily limits. It is the decode target, so only an omitted limit takes
// the default and an explicit 0 survives as "no cap".
func DefaultConfig() Config {
	cfg := Config{
		MaxRequestsPerDay: DefaultMaxRequestsPerDay,
		MaxCostUSDPerDay:  DefaultMaxCostUSDPerDay,
	}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults fills the pinned endpoint and model, the key variable, and
// the request timeout. It leaves the daily limits alone: 0 means no cap and
// the defaults come from DefaultConfig before decoding.
func (c *Config) ApplyDefaults() {
	if c.Endpoint == "" {
		c.Endpoint = DefaultEndpoint
	}
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.APIKeyEnv == "" {
		c.APIKeyEnv = DefaultAPIKeyEnv
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = DefaultRequestTimeout
	}
	if c.Rerank.Shape == "" {
		c.Rerank.Shape = RerankShapeBatched
	}
	if c.Rerank.Top == 0 {
		c.Rerank.Top = MaxRerankTop
	}
}

// Validate checks the section regardless of Enabled so a misconfigured
// endpoint cannot wait until the first request to fail.
func (c Config) Validate() error {
	if err := ValidateEndpoint(c.Endpoint); err != nil {
		return fmt.Errorf("[jev] endpoint: %w", err)
	}
	if c.Model == "" || strings.TrimSpace(c.Model) != c.Model {
		return errors.New("[jev] model is required and must not have surrounding whitespace")
	}
	if !environmentNamePattern.MatchString(c.APIKeyEnv) {
		return fmt.Errorf("invalid [jev] api_key_env %q", c.APIKeyEnv)
	}
	if c.RequestTimeout <= 0 {
		return fmt.Errorf("invalid [jev] request_timeout %s: must be positive", c.RequestTimeout)
	}
	if c.MaxRequestsPerDay < 0 {
		return fmt.Errorf("invalid [jev] max_requests_per_day %d: must be non-negative", c.MaxRequestsPerDay)
	}
	if c.MaxCostUSDPerDay < 0 || c.InputUSDPerMillionTokens < 0 || c.OutputUSDPerMillionTokens < 0 {
		return errors.New("[jev] prices and cost limits must be non-negative")
	}
	return c.Rerank.Validate()
}

// Validate checks [jev.rerank] regardless of Enabled.
func (c RerankConfig) Validate() error {
	if c.Shape != RerankShapeBatched && c.Shape != RerankShapePerCandidate {
		return fmt.Errorf("invalid [jev.rerank] shape %q: want %s or %s", c.Shape, RerankShapeBatched, RerankShapePerCandidate)
	}
	if c.Top < 2 || c.Top > MaxRerankTop {
		return fmt.Errorf("invalid [jev.rerank] top %d: must be between 2 and %d", c.Top, MaxRerankTop)
	}
	for _, messageType := range c.MessageTypesExcluded {
		if strings.TrimSpace(messageType) == "" {
			return errors.New("invalid [jev.rerank] message_types_excluded: entries must not be empty")
		}
	}
	return nil
}

// Excludes reports whether results of messageType are never sent for
// reranking. Matching ignores case and surrounding space.
func (c RerankConfig) Excludes(messageType string) bool {
	messageType = strings.ToLower(strings.TrimSpace(messageType))
	for _, excluded := range c.MessageTypesExcluded {
		if strings.ToLower(strings.TrimSpace(excluded)) == messageType {
			return true
		}
	}
	return false
}

// Priced reports whether token prices are configured, which turns on cost
// accounting and the daily cost cap.
func (c Config) Priced() bool {
	return c.InputUSDPerMillionTokens > 0 || c.OutputUSDPerMillionTokens > 0
}

// DayLimits derives the per-feature daily caps. A zero limit is no cap. The
// cost cap only applies when prices are configured; without prices spend is
// unknowable.
func (c Config) DayLimits() DayLimits {
	limits := DayLimits{MaxRequests: c.MaxRequestsPerDay}
	if c.Priced() {
		limits.MaxCostUSDMicros = int64(c.MaxCostUSDPerDay * 1e6)
	}
	return limits
}
