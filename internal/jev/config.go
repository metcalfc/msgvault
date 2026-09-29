package jev

import (
	"errors"
	"fmt"
	"regexp"
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
}

// FeatureConfig gates one Jev-backed feature. Automatic additionally allows
// unattended paths (scheduled jobs, sync, cache builds) to send.
type FeatureConfig struct {
	Enabled   bool `toml:"enabled"`
	Automatic bool `toml:"automatic"`
}

// ApplyDefaults fills the pinned endpoint and model, the key variable, and
// conservative daily limits.
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
	if c.MaxRequestsPerDay == 0 {
		c.MaxRequestsPerDay = DefaultMaxRequestsPerDay
	}
	if c.MaxCostUSDPerDay == 0 {
		c.MaxCostUSDPerDay = DefaultMaxCostUSDPerDay
	}
}

// Validate checks the section regardless of Enabled so a misconfigured
// endpoint cannot wait until the first request to fail.
func (c Config) Validate() error {
	if err := ValidateEndpoint(c.Endpoint); err != nil {
		return fmt.Errorf("[jev] endpoint: %w", err)
	}
	if c.Model == "" {
		return errors.New("[jev] model is required")
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
	return nil
}

// Priced reports whether token prices are configured, which turns on cost
// accounting and the daily cost cap.
func (c Config) Priced() bool {
	return c.InputUSDPerMillionTokens > 0 || c.OutputUSDPerMillionTokens > 0
}

// DayLimits derives the per-feature daily caps. The cost cap only applies
// when prices are configured; without prices spend is unknowable.
func (c Config) DayLimits() DayLimits {
	limits := DayLimits{MaxRequests: c.MaxRequestsPerDay}
	if c.Priced() {
		limits.MaxCostUSDMicros = int64(c.MaxCostUSDPerDay * 1e6)
	}
	return limits
}
