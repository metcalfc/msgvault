package jev

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigDefaultsArePinnedAndOff(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	cfg := DefaultConfig()
	require.NoError(cfg.Validate())
	assert.False(cfg.Enabled)
	assert.False(cfg.IdentityVerification.Enabled)
	assert.False(cfg.IdentityVerification.Automatic)
	assert.Equal(DefaultEndpoint, cfg.Endpoint)
	assert.Equal(DefaultModel, cfg.Model)
	assert.Equal(DefaultAPIKeyEnv, cfg.APIKeyEnv)
	assert.Equal(DefaultRequestTimeout, cfg.RequestTimeout)
	assert.Equal(int64(DefaultMaxRequestsPerDay), cfg.MaxRequestsPerDay)
	assert.InDelta(DefaultMaxCostUSDPerDay, cfg.MaxCostUSDPerDay, 1e-9)
	assert.False(cfg.Priced())
	assert.Equal(DayLimits{MaxRequests: DefaultMaxRequestsPerDay}, cfg.DayLimits(),
		"without prices the cost cap is not applied")

	cfg.InputUSDPerMillionTokens = 0.5
	assert.True(cfg.Priced())
	assert.Equal(DayLimits{MaxRequests: DefaultMaxRequestsPerDay, MaxCostUSDMicros: 1_000_000}, cfg.DayLimits())
}

func TestConfigZeroLimitsMeanNoCapAndOnlyOmittedLimitsDefault(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	explicit := DefaultConfig()
	explicit.MaxRequestsPerDay = 0
	explicit.MaxCostUSDPerDay = 0
	explicit.InputUSDPerMillionTokens = 1
	explicit.ApplyDefaults()
	require.NoError(explicit.Validate())
	assert.Zero(explicit.MaxRequestsPerDay, "ApplyDefaults must not rewrite an explicit 0")
	assert.Equal(DayLimits{}, explicit.DayLimits(), "0 is no cap on both dimensions")

	var omitted Config
	omitted.ApplyDefaults()
	assert.Zero(omitted.MaxRequestsPerDay, "a bare ApplyDefaults never invents a limit")
	assert.Equal(int64(DefaultMaxRequestsPerDay), DefaultConfig().MaxRequestsPerDay,
		"decoding over DefaultConfig is what supplies the default")
}

func TestConfigValidateRejectsUnsafeValues(t *testing.T) {
	cases := map[string]func(*Config){
		"plain http endpoint":   func(c *Config) { c.Endpoint = "http://api.typesafe.ai/v1/systemone" },
		"endpoint credentials":  func(c *Config) { c.Endpoint = "https://user:pw@api.typesafe.ai/v1/systemone" },
		"endpoint query":        func(c *Config) { c.Endpoint = "https://api.typesafe.ai/v1/systemone?x=1" },
		"padded endpoint":       func(c *Config) { c.Endpoint = " https://api.typesafe.ai/v1/systemone" },
		"endpoint newline":      func(c *Config) { c.Endpoint = "https://api.typesafe.ai/v1/systemone\n" },
		"empty model":           func(c *Config) { c.Model = "" },
		"padded model":          func(c *Config) { c.Model = " jev-1.13.0 " },
		"bad key variable":      func(c *Config) { c.APIKeyEnv = "not a name" },
		"zero timeout":          func(c *Config) { c.RequestTimeout = 0 },
		"negative requests":     func(c *Config) { c.MaxRequestsPerDay = -1 },
		"negative cost":         func(c *Config) { c.MaxCostUSDPerDay = -1 },
		"negative input price":  func(c *Config) { c.InputUSDPerMillionTokens = -1 },
		"negative output price": func(c *Config) { c.OutputUSDPerMillionTokens = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var cfg Config
			cfg.ApplyDefaults()
			cfg.RequestTimeout = time.Second
			mutate(&cfg)
			assert.Error(t, cfg.Validate())
		})
	}
}
