package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/providercredentials"
	"go.kenn.io/msgvault/internal/testutil"
)

func jevTestSpec() jev.FeatureSpec {
	return jev.FeatureSpec{
		Name: jev.FeatureEnrichmentIdentity, Title: "Enrichment identity check",
		Purpose: "Decide whether a partially matching enrichment result is the requested person.",
		Questions: []jev.Question{{
			ID: "name_compatible", Type: jev.QuestionNoul,
			Instructions: "Could `returned.name` be the same person as `requested.name`?",
			Criteria:     jev.NoulCriteria{True: "Same name or abbreviation.", False: "A different person."},
		}},
		StateFields: []string{"requested.name", "returned.name"},
	}
}

func jevTestDeps(t *testing.T, cfg *config.Config, subprocess bool) (jevCommandDeps, *[]string) {
	t.Helper()
	st := testutil.NewTestStore(t)
	proxied := make([]string, 0)
	return jevCommandDeps{
		config: func() *config.Config { return cfg },
		openStore: func() (jevStore, func(), error) {
			return st, func() {}, nil
		},
		features: func() []jev.FeatureSpec { return []jev.FeatureSpec{jevTestSpec()} },
		credentialState: func(*config.Config) (providercredentials.State, error) {
			return providercredentials.State{Configured: true, Source: providercredentials.SourceEnvironment}, nil
		},
		isDaemonSubprocess: func() bool { return subprocess },
		proxyArgs: func(_ *cobra.Command, args []string, _ map[string]string) error {
			proxied = append(proxied, args...)
			return nil
		},
		now: func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) },
	}, &proxied
}

func executeJevCommand(t *testing.T, deps jevCommandDeps, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "msgvault", SilenceErrors: true, SilenceUsage: true}
	root.AddCommand(newJevCommand(deps))
	root.SetArgs(append([]string{"jev"}, args...))
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	err := root.ExecuteContext(context.Background())
	return stdout.String(), err
}

func jevTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.NewDefaultConfig()
	cfg.HomeDir = t.TempDir()
	cfg.Data.DataDir = cfg.HomeDir
	cfg.Jev.Enabled = true
	cfg.Jev.IdentityVerification.Enabled = true
	return cfg
}

func TestJevConsentDisclosesPolicyAndRequiresYes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	deps, _ := jevTestDeps(t, jevTestConfig(t), true)

	out, err := executeJevCommand(t, deps, "consent", "enrichment_identity")
	require.ErrorContains(err, "--yes")
	assert.Contains(out, "Jev feature disclosure: Enrichment identity check (enrichment_identity)")
	assert.Contains(out, "Destination: "+jev.DefaultEndpoint)
	assert.Contains(out, "Model: "+jev.DefaultModel)
	assert.Contains(out, "- requested.name\n- returned.name\n")
	assert.Contains(out, "- name_compatible (noul): Could `returned.name` be the same person as `requested.name`?")
	assert.Contains(out, "Feature switch: enabled=on automatic=off")

	out, err = executeJevCommand(t, deps, "status", "--json")
	require.NoError(err)
	var before jevStatusOutput
	require.NoError(json.Unmarshal([]byte(out), &before))
	require.Len(before.Features, 1)
	assert.False(before.Features[0].Consent.Active, "the disclosure alone grants nothing")

	out, err = executeJevCommand(t, deps, "consent", "enrichment_identity", "--yes")
	require.NoError(err)
	assert.Contains(out, "Consent: active (")

	out, err = executeJevCommand(t, deps, "status", "--json")
	require.NoError(err)
	var after jevStatusOutput
	require.NoError(json.Unmarshal([]byte(out), &after))
	assert.True(after.Enabled)
	assert.Equal(jev.DefaultEndpoint, after.Endpoint)
	assert.Equal("environment", string(after.Credential.Source))
	require.Len(after.Features, 1)
	feature := after.Features[0]
	assert.Equal("enrichment_identity", feature.Name)
	assert.True(feature.Enabled)
	assert.False(feature.Automatic)
	assert.True(feature.Consent.Active)
	assert.Equal(feature.Fingerprint, feature.Consent.Fingerprint)
	assert.Equal(jev.DayCounters{Feature: "enrichment_identity", UTCDay: "2026-09-28"}, feature.Today)

	out, err = executeJevCommand(t, deps, "status")
	require.NoError(err)
	assert.Contains(out, "Jev: on")
	assert.Contains(out, "consent active")

	out, err = executeJevCommand(t, deps, "revoke", "enrichment_identity")
	require.NoError(err)
	assert.Contains(out, "Consent revoked for enrichment_identity: 1 grant(s)")
	out, err = executeJevCommand(t, deps, "status", "--json")
	require.NoError(err)
	var revoked jevStatusOutput
	require.NoError(json.Unmarshal([]byte(out), &revoked))
	assert.False(revoked.Features[0].Consent.Active)

	_, err = executeJevCommand(t, deps, "consent", "enrichment_identity", "--yes")
	require.NoError(err)
	out, err = executeJevCommand(t, deps, "revoke", "--all", "--json")
	require.NoError(err)
	assert.Contains(out, `"revoked":1`)

	_, err = executeJevCommand(t, deps, "consent", "search_rerank", "--yes")
	require.ErrorContains(err, "unknown Jev feature")
	_, err = executeJevCommand(t, deps, "consent", "Not A Feature", "--yes")
	require.ErrorContains(err, "invalid Jev feature name")
	_, err = executeJevCommand(t, deps, "revoke")
	require.ErrorContains(err, "exactly one feature name or --all")
	_, err = executeJevCommand(t, deps, "revoke", "enrichment_identity", "--all")
	require.ErrorContains(err, "exactly one feature name or --all")
}

func TestJevStatusSurfacesCredentialStoreErrors(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	cfg := jevTestConfig(t)
	deps, _ := jevTestDeps(t, cfg, true)
	deps.credentialState = func(*config.Config) (providercredentials.State, error) {
		return providercredentials.State{Source: providercredentials.SourceNone}, providercredentials.ErrOriginMismatch
	}
	out, err := executeJevCommand(t, deps, "status")
	require.NoError(err)
	assert.Contains(out, "Credential: unavailable: "+providercredentials.ErrOriginMismatch.Error())
	out, err = executeJevCommand(t, deps, "status", "--json")
	require.NoError(err)
	var status jevStatusOutput
	require.NoError(json.Unmarshal([]byte(out), &status))
	assert.Equal(providercredentials.ErrOriginMismatch.Error(), status.CredentialError)
	assert.False(status.Credential.Configured)
}

func TestJevCredentialStateReportsOriginMismatchAndUnreadableStores(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	cfg := jevTestConfig(t)
	state, err := jevCredentialState(cfg)
	require.NoError(err, "an absent store is simply not configured")
	assert.Equal(providercredentials.SourceNone, state.Source)

	_, err = providercredentials.Put(cfg.TokensDir(), mustJevCredentialETag(t, cfg), providercredentials.JevID,
		"https://other.example.test/v1/systemone", "stored-key")
	require.NoError(err)
	_, err = jevCredentialState(cfg)
	require.ErrorIs(err, providercredentials.ErrOriginMismatch, "a key bound elsewhere is reported, not hidden")

	require.NoError(os.WriteFile(filepath.Join(cfg.TokensDir(), providercredentials.Filename), []byte("{not json"), 0o600))
	_, err = jevCredentialState(cfg)
	require.ErrorIs(err, providercredentials.ErrUnavailable)
}

func mustJevCredentialETag(t *testing.T, cfg *config.Config) string {
	t.Helper()
	snapshot, err := providercredentials.Read(cfg.TokensDir())
	require.NoError(t, err)
	return snapshot.ETag
}

func TestJevCommandsProxyToTheDaemonOutsideASubprocess(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	deps, proxied := jevTestDeps(t, jevTestConfig(t), false)
	_, err := executeJevCommand(t, deps, "consent", "enrichment_identity", "--yes")
	require.NoError(err)
	assert.Equal([]string{"jev", "consent", "--yes", "enrichment_identity"}, *proxied, "flags precede positionals in proxied args")
	*proxied = (*proxied)[:0]
	_, err = executeJevCommand(t, deps, "status", "--json")
	require.NoError(err)
	assert.Equal([]string{"jev", "status", "--json"}, *proxied)
}
