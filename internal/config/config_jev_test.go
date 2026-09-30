package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/jev"
)

func TestLoadJevSectionDefaultsAndOverrides(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	tmpDir := t.TempDir()
	t.Setenv("MSGVAULT_HOME", tmpDir)
	configPath := filepath.Join(tmpDir, "config.toml")

	require.NoError(os.WriteFile(configPath, []byte("[data]\n"), 0o644))
	cfg, err := Load(configPath, "")
	require.NoError(err)
	assert.False(cfg.Jev.Enabled, "Jev is off unless configured")
	assert.Equal(jev.DefaultEndpoint, cfg.Jev.Endpoint)
	assert.Equal(jev.DefaultModel, cfg.Jev.Model)
	assert.Equal(jev.DefaultAPIKeyEnv, cfg.Jev.APIKeyEnv)
	assert.Equal(jev.DefaultRequestTimeout, cfg.Jev.RequestTimeout)
	assert.False(cfg.Jev.IdentityVerification.Enabled)
	assert.False(cfg.Jev.OrganizationResolution.Enabled)
	assert.False(cfg.Jev.CorrespondentKind.Enabled)
	assert.False(cfg.Jev.CorrespondentKind.Automatic)

	require.NoError(os.WriteFile(configPath, []byte(`
[jev]
enabled = true
api_key_env = "MY_TYPESAFE_KEY"
request_timeout = "4s"
max_requests_per_day = 25
max_cost_usd_per_day = 0.25
input_usd_per_million_tokens = 0.5
output_usd_per_million_tokens = 1.5

[jev.identity_verification]
enabled = true
automatic = true

[jev.organization_resolution]
enabled = true

[jev.correspondent_kind]
enabled = true
`), 0o644))
	cfg, err = Load(configPath, "")
	require.NoError(err)
	assert.True(cfg.Jev.Enabled)
	assert.Equal("MY_TYPESAFE_KEY", cfg.Jev.APIKeyEnv)
	assert.Equal(4*time.Second, cfg.Jev.RequestTimeout)
	assert.Equal(int64(25), cfg.Jev.MaxRequestsPerDay)
	assert.Equal(jev.DayLimits{MaxRequests: 25, MaxCostUSDMicros: 250_000}, cfg.Jev.DayLimits())
	assert.True(cfg.Jev.IdentityVerification.Enabled)
	assert.True(cfg.Jev.IdentityVerification.Automatic)
	assert.True(cfg.Jev.OrganizationResolution.Enabled)
	assert.False(cfg.Jev.OrganizationResolution.Automatic, "automatic use stays off unless set")
	assert.True(cfg.Jev.CorrespondentKind.Enabled)
	assert.False(cfg.Jev.CorrespondentKind.Automatic, "automatic stays off unless set")

	require.NoError(os.WriteFile(configPath, []byte(`
[jev]
max_requests_per_day = 0
max_cost_usd_per_day = 0
`), 0o644))
	cfg, err = Load(configPath, "")
	require.NoError(err)
	assert.Zero(cfg.Jev.MaxRequestsPerDay, "an explicit 0 means no cap, not the default")
	assert.Zero(cfg.Jev.MaxCostUSDPerDay)
	assert.Equal(jev.DayLimits{}, cfg.Jev.DayLimits())

	require.NoError(os.WriteFile(configPath, []byte(`
[jev]
endpoint = "http://api.typesafe.ai/v1/systemone"
`), 0o644))
	_, err = Load(configPath, "")
	require.ErrorContains(err, "[jev] endpoint")
}
