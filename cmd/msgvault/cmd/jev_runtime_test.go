package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestJevFeatureRegistryIncludesEnrichmentIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	specs := jevFeatureSpecs()
	require.Len(specs, 1)
	assert.Equal(jev.FeatureEnrichmentIdentity, specs[0].Name)
	require.NoError(specs[0].Validate())
	cfg := config.NewDefaultConfig()
	policy, err := specs[0].Policy(cfg.Jev)
	require.NoError(err)
	assert.Len(policy.Fingerprint, 64)
	assert.Equal(jev.DefaultEndpoint, policy.Endpoint)
}

func TestNewJevIdentityJudgeIsNilUntilJevAndTheFeatureAreOn(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	cfg := config.NewDefaultConfig()
	cfg.HomeDir = t.TempDir()
	cfg.Data.DataDir = cfg.HomeDir

	judge, err := newJevIdentityJudge(cfg, st, true)
	require.NoError(err)
	assert.Nil(judge, "everything off means the exact rule alone")
	assert.Nil(exaIdentityReviewOptions(judge), "no judge means no partial-match pass-through")

	cfg.Jev.Enabled = true
	judge, err = newJevIdentityJudge(cfg, st, true)
	require.NoError(err)
	assert.Nil(judge, "the feature switch is separate from the [jev] switch")

	cfg.Jev.IdentityVerification.Enabled = true
	judge, err = newJevIdentityJudge(cfg, st, true)
	require.NoError(err)
	require.NotNil(judge)
	assert.Len(exaIdentityReviewOptions(judge), 1)

	service, err := newJevService(cfg, st)
	require.NoError(err)
	state := map[string]any{"requested": map[string]any{}, "returned": map[string]any{}}
	_, err = service.Judge(t.Context(), jevFeatureSpecs()[0], true, state, time.Time{})
	require.ErrorIs(err, jev.ErrAutomaticDisabled, "scheduled runs stay off until automatic = true")
	cfg.Jev.IdentityVerification.Automatic = true
	_, err = service.Judge(t.Context(), jevFeatureSpecs()[0], true, state, time.Time{})
	require.ErrorIs(err, jev.ErrCredentialMissing, "no key resolves in a fresh home, and nothing is sent")
}
