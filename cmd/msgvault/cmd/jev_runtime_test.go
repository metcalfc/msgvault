package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/providercredentials"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestJevFeatureRegistryListsEveryFeature(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	specs := jevFeatureSpecs()
	require.Len(specs, 6)
	assert.Equal(jev.FeatureEnrichmentIdentity, specs[0].Name)
	assert.Equal(jev.FeatureOrganizationResolution, specs[1].Name)
	assert.Equal(jev.FeatureCorrespondentKind, specs[2].Name)
	assert.Equal(jev.FeatureCleanupSuggestions, specs[3].Name)
	assert.Equal(jev.FeatureSearchRerank, specs[4].Name)
	assert.Equal(jev.FeatureMeetingEventKind, specs[5].Name)
	cfg := config.NewDefaultConfig()
	for _, spec := range specs {
		require.NoError(spec.Validate())
		policy, err := spec.Policy(cfg.Jev)
		require.NoError(err)
		assert.Len(policy.Fingerprint, 64)
		assert.Equal(jev.DefaultEndpoint, policy.Endpoint)
		_, known := cfg.Jev.FeatureConfigFor(spec.Name)
		assert.True(known, "every registered feature has a [jev] section")
	}
}

func TestNewJevKindJudgeIsNilUntilJevAndTheFeatureAreOn(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	cfg := config.NewDefaultConfig()
	cfg.HomeDir = t.TempDir()
	cfg.Data.DataDir = cfg.HomeDir

	judge, err := newJevKindJudge(cfg, st)
	require.NoError(err)
	assert.Nil(judge, "everything off means rules only")
	cfg.Jev.Enabled = true
	judge, err = newJevKindJudge(cfg, st)
	require.NoError(err)
	assert.Nil(judge, "the feature switch is separate from the [jev] switch")
	cfg.Jev.CorrespondentKind.Enabled = true
	judge, err = newJevKindJudge(cfg, st)
	require.NoError(err)
	assert.NotNil(judge)
}

func TestNewJevCleanupJudgeIsNilUntilJevAndTheFeatureAreOn(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	cfg := config.NewDefaultConfig()
	cfg.HomeDir = t.TempDir()
	cfg.Data.DataDir = cfg.HomeDir

	cfg.Jev.CleanupSuggestions.Enabled = true
	judge, err := newJevCleanupJudge(cfg, st)
	require.NoError(err)
	assert.Nil(judge, "the feature switch alone is not enough")
	cfg.Jev.Enabled = true
	judge, err = newJevCleanupJudge(cfg, st)
	require.NoError(err)
	assert.NotNil(judge)
}

func TestNewJevEventKindJudgeIsNilUntilJevAndTheFeatureAreOn(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	cfg := config.NewDefaultConfig()
	cfg.HomeDir = t.TempDir()
	cfg.Data.DataDir = cfg.HomeDir

	judge, err := newJevEventKindJudge(cfg, st)
	require.NoError(err)
	assert.Nil(judge, "everything off means plain rules only")
	cfg.Jev.Enabled = true
	judge, err = newJevEventKindJudge(cfg, st)
	require.NoError(err)
	assert.Nil(judge, "the feature switch is separate from the [jev] switch")
	cfg.Jev.MeetingEventKind.Enabled = true
	judge, err = newJevEventKindJudge(cfg, st)
	require.NoError(err)
	assert.NotNil(judge)
}

func TestNewJevOrganizationPreparerIsNilUntilJevAndTheFeatureAreOn(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	cfg := config.NewDefaultConfig()
	cfg.HomeDir = t.TempDir()
	cfg.Data.DataDir = cfg.HomeDir

	preparer, err := newJevOrganizationPreparer(cfg, st, true)
	require.NoError(err)
	assert.Nil(preparer, "everything off means the exact organization lookup alone")

	cfg.Jev.Enabled = true
	preparer, err = newJevOrganizationPreparer(cfg, st, true)
	require.NoError(err)
	assert.Nil(preparer, "the feature switch is separate from the [jev] switch")

	cfg.Jev.OrganizationResolution.Enabled = true
	preparer, err = newJevOrganizationPreparer(cfg, st, true)
	require.NoError(err)
	assert.NotNil(preparer)
}

func TestJevCredentialRevisionTracksTheStoreFile(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	cfg := config.NewDefaultConfig()
	cfg.HomeDir = t.TempDir()
	cfg.Data.DataDir = cfg.HomeDir
	probe := jevCredentialRevision(cfg)
	absent, err := probe()
	require.NoError(err)
	assert.Equal("absent", absent)
	snapshot, err := providercredentials.Read(cfg.TokensDir())
	require.NoError(err)
	_, err = providercredentials.Put(cfg.TokensDir(), snapshot.ETag, providercredentials.JevID, cfg.Jev.Endpoint, "first-key")
	require.NoError(err)
	first, err := probe()
	require.NoError(err)
	assert.NotEqual(absent, first)
	snapshot, err = providercredentials.Read(cfg.TokensDir())
	require.NoError(err)
	_, err = providercredentials.Put(cfg.TokensDir(), snapshot.ETag, providercredentials.JevID, cfg.Jev.Endpoint, "second-key-longer")
	require.NoError(err)
	second, err := probe()
	require.NoError(err)
	assert.NotEqual(first, second, "a rewritten store changes the revision")
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
