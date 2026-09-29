package documentindex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document/csvpdf"
	"go.kenn.io/msgvault/internal/documentindex/mistralprovider"
	"go.kenn.io/msgvault/internal/documentindex/mistralprovider/mistralprovidertest"
	"go.kenn.io/msgvault/internal/documentindex/provider"
)

func TestDocumentsConfigDefaultsAreValidAndOptIn(t *testing.T) {
	assert := assert.New(t)
	config := DefaultDocumentsConfig()

	require.NoError(t, config.Validate())
	assert.False(config.Enabled)
	assert.Equal(mistralprovider.Name, config.Provider)
	assert.Equal(mistralprovider.RegionEU, config.Region)
	assert.Equal(mistralprovider.DefaultModel, config.Model)
	assert.Equal(RetentionUnknown, config.RetentionPosture)
	assert.Equal(TrainingUnknown, config.TrainingPosture)
	assert.True(config.LexicalEnabled())
	assert.True(config.StoresChunkText())
	assert.False(config.Index.Embeddings.Enabled)
	assert.Equal("vector.embeddings", config.Index.Embeddings.Profile)
	assert.Equal(int64(512<<20), config.MaxSpoolBytes)
	assert.Equal(int64(1<<30), config.MinFreeSpaceBytes)
}

func TestDocumentsConfigDoesNotDefaultExplicitZeroSafetyLimits(t *testing.T) {
	config := DefaultDocumentsConfig()
	config.MaxFileBytes = 0
	config.ApplyDefaults()

	require.ErrorContains(t, config.Validate(), "max_file_bytes: must be positive")
	assert.Zero(t, config.MaxFileBytes)
}

func TestDocumentsConfigRejectsUnavailableIndexOptOut(t *testing.T) {
	lexical := false
	store := false
	config := DefaultDocumentsConfig()
	config.Index = IndexConfig{Lexical: &lexical, StoreChunkText: &store}

	require.ErrorContains(t, config.Validate(), "must both be true")
	assert.False(t, config.LexicalEnabled())
	assert.False(t, config.StoresChunkText())
}

func TestDocumentsConfigAllowsNamedEmbeddingProfileWithLexicalFallback(t *testing.T) {
	config := DefaultDocumentsConfig()
	config.Index.Embeddings.Enabled = true
	config.Index.Embeddings.Profile = "vector.embeddings"

	require.NoError(t, config.Validate())
	assert.True(t, config.LexicalEnabled())
	assert.True(t, config.StoresChunkText())
}

func TestCSVPolicyCapsGeneratedPDFAtFileLimit(t *testing.T) {
	config := DefaultDocumentsConfig()
	config.MaxFileBytes = 1 << 20
	policy, err := config.CSVPolicy()
	require.NoError(t, err)

	limits := csvpdf.DefaultLimits()
	limits.MaxSourceBytes = 1 << 20
	limits.MaxPDFBytes = 1 << 20
	expected, err := csvpdf.NewPolicy(limits)
	require.NoError(t, err)
	assert.Equal(t, expected.Fingerprint(), policy.Fingerprint())
}

func TestDocumentsConfigRejectsUnknownEmbeddingProfile(t *testing.T) {
	config := DefaultDocumentsConfig()
	config.Index.Embeddings.Enabled = true
	config.Index.Embeddings.Profile = "other.embeddings"

	require.ErrorContains(t, config.Validate(), "profile must be \"vector.embeddings\"")
}

// tightProvider is a fake backend whose limits sit below the default
// provider's defaults, so seeding numeric defaults before the provider is
// known would make an untouched configuration fail validation.
type tightProvider struct {
	provider.Provider
}

func (tightProvider) Name() string { return "tight" }

func (p tightProvider) Defaults() provider.Defaults {
	defaults := p.Provider.Defaults()
	defaults.RequestTimeout = 30 * time.Second
	defaults.MaxRetries = 1
	defaults.APIKeyEnv = "TIGHT_API_KEY"
	return defaults
}

func (p tightProvider) Limits() provider.Limits {
	limits := p.Provider.Limits()
	limits.MaxRequestTimeout = time.Minute
	limits.MaxRetries = 2
	return limits
}

func withTightProvider(t *testing.T) {
	t.Helper()
	previous := providers
	providers = provider.MustRegistry(mistralprovider.New(), tightProvider{Provider: mistralprovider.New()})
	t.Cleanup(func() { providers = previous })
}

func TestApplyDefaultsResolvesNumericDefaultsForTheDecodedProvider(t *testing.T) {
	withTightProvider(t)
	require := require.New(t)
	assert := assert.New(t)

	decoded := DocumentsConfigDecodeTarget()
	decoded.Provider = "tight"
	decoded.ApplyDefaults()
	require.NoError(decoded.Validate())
	assert.Equal(30*time.Second, decoded.RequestTimeout)
	assert.Equal(1, decoded.MaxRetries)
	assert.Equal("TIGHT_API_KEY", decoded.APIKeyEnv)

	mistralDefaults := DefaultDocumentsConfig()
	mistralDefaults.Provider = "tight"
	require.ErrorContains(mistralDefaults.Validate(), "request_timeout: exceeds hard safety limit",
		"the default provider's timeout is above the tight limit, so it must not be seeded before the provider is known")

	explicitZero := DocumentsConfigDecodeTarget()
	explicitZero.Provider = "tight"
	explicitZero.RequestTimeout = 0
	explicitZero.ApplyDefaults()
	require.ErrorContains(explicitZero.Validate(), "request_timeout: must be positive")

	explicit := DocumentsConfigDecodeTarget()
	explicit.Provider = "tight"
	explicit.MaxRetries = 2
	explicit.ApplyDefaults()
	explicit.ApplyDefaults()
	require.NoError(explicit.Validate())
	assert.Equal(2, explicit.MaxRetries, "an explicit value survives repeated ApplyDefaults")

	unresolved := DocumentsConfigDecodeTarget()
	require.Error(unresolved.Validate(), "a decode target never validates before ApplyDefaults")
	unresolved.Provider, unresolved.Model, unresolved.Region, unresolved.APIKeyEnv = "tight", mistralprovider.DefaultModel, mistralprovider.RegionEU, "TIGHT_API_KEY"
	unresolved.RetentionPosture, unresolved.TrainingPosture = RetentionUnknown, TrainingUnknown
	require.ErrorContains(unresolved.Validate(), "request_timeout: must be positive", "numeric sentinels never validate")
}

func TestDocumentsConfigRejectsUnsafePolicy(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DocumentsConfig)
		want   string
	}{
		{name: "provider", mutate: func(c *DocumentsConfig) { c.Provider = "other" }, want: "provider"},
		{name: "model", mutate: func(c *DocumentsConfig) { c.Model = "latest" }, want: "pinned"},
		{name: "region", mutate: func(c *DocumentsConfig) { c.Region = "us" }, want: "unknown region"},
		{name: "environment", mutate: func(c *DocumentsConfig) { c.APIKeyEnv = "bad-name" }, want: "environment variable"},
		{name: "negative cap", mutate: func(c *DocumentsConfig) { c.MaxFileBytes = -1 }, want: "must be positive"},
		{name: "unbounded cap", mutate: func(c *DocumentsConfig) { c.MaxResponseBytes = mistralprovider.New().Limits().MaxResponseBytes + 1 }, want: "hard safety limit"},
		{name: "spool below file", mutate: func(c *DocumentsConfig) { c.MaxSpoolBytes = c.MaxFileBytes - 1 }, want: "at least max_file_bytes"},
		{name: "spool hard cap", mutate: func(c *DocumentsConfig) { c.MaxSpoolBytes = hardMaxSpoolBytes + 1 }, want: "hard safety limit"},
		{name: "free space hard cap", mutate: func(c *DocumentsConfig) { c.MinFreeSpaceBytes = hardMaxFreeSpaceBytes + 1 }, want: "hard safety limit"},
		{name: "timeout", mutate: func(c *DocumentsConfig) {
			c.RequestTimeout = mistralprovider.New().Limits().MaxRequestTimeout + time.Second
		}, want: "hard safety limit"},
		{name: "cost", mutate: func(c *DocumentsConfig) { c.MaxEstimatedCostUSDPerRun = hardMaxEstimatedCostUSD + 1 }, want: "hard safety limit"},
		{name: "pricing pair", mutate: func(c *DocumentsConfig) { c.EstimatedCostUSDPerKUnits = 4 }, want: "pricing assumption requires both"},
		{name: "pricing date", mutate: func(c *DocumentsConfig) { c.EstimatedCostUSDPerKUnits = 4; c.PricingAssumptionOn = "today" }, want: "YYYY-MM-DD"},
		{name: "retention", mutate: func(c *DocumentsConfig) { c.RetentionPosture = "no-retention" }, want: "retention_posture"},
		{name: "training", mutate: func(c *DocumentsConfig) { c.TrainingPosture = "never" }, want: "training_posture"},
		{name: "lexical without text", mutate: func(c *DocumentsConfig) { disabled := false; c.Index.StoreChunkText = &disabled }, want: "must both be true"},
		{name: "stored text without lexical", mutate: func(c *DocumentsConfig) { disabled := false; c.Index.Lexical = &disabled }, want: "must both be true"},
		{name: "unknown embedding profile", mutate: func(c *DocumentsConfig) {
			c.Index.Embeddings.Enabled = true
			c.Index.Embeddings.Profile = "other.embeddings"
		}, want: "profile must be \"vector.embeddings\""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultDocumentsConfig()
			test.mutate(&config)
			require.ErrorContains(t, config.Validate(), test.want)
		})
	}
}

func TestDocumentsConfigReservesWorstCaseRunBudget(t *testing.T) {
	config := DefaultDocumentsConfig()
	config.MaxPagesPerDocument = 500
	config.MaxPagesPerRun = 2_000
	config.MaxEstimatedCostUSDPerRun = 3
	config.EstimatedCostUSDPerKUnits = 4
	config.PricingAssumptionOn = "2026-08-13"

	limit, err := config.MaxDocumentsWithinRunBudget(100)
	require.NoError(t, err)
	assert.Equal(t, 1, limit, "the cost cap reserves two dollars for every worst-case request")
	config.MaxEstimatedCostUSDPerRun = 1
	_, err = config.MaxDocumentsWithinRunBudget(100)
	require.ErrorContains(t, err, "smaller than one")
}

func TestDocumentsProfileFingerprintIsDeterministicAndPolicyBound(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	config := DefaultDocumentsConfig()
	config.RetentionPosture = RetentionZDR
	config.TrainingPosture = TrainingOptedOut
	config.Scope.MessageTypes = []string{"email", "chat"}
	config.ApplyDefaults()
	policy, err := config.ExtractionPolicy()
	require.NoError(err)
	manifest := testCapabilityManifest(t, policy)

	first, err := config.ProfileFingerprint(manifest, []string{"text/csv", "application/pdf", "text/csv"})
	require.NoError(err)
	second, err := config.ProfileFingerprint(manifest, []string{"application/pdf", "text/csv"})
	require.NoError(err)
	assert.Equal(first, second)
	assert.Regexp(`^[0-9a-f]{64}$`, first)

	changed := config
	changed.RetentionPosture = RetentionStandard
	third, err := changed.ProfileFingerprint(manifest, []string{"application/pdf", "text/csv"})
	require.NoError(err)
	assert.NotEqual(first, third)

	fourth, err := config.ProfileFingerprint(manifest, []string{"application/pdf"})
	require.NoError(err)
	assert.NotEqual(first, fourth)

	changed = config
	changed.MaxSpoolBytes++
	fifth, err := changed.ProfileFingerprint(manifest, []string{"application/pdf", "text/csv"})
	require.NoError(err)
	assert.NotEqual(first, fifth)

	changed = config
	changed.MinFreeSpaceBytes++
	sixth, err := changed.ProfileFingerprint(manifest, []string{"application/pdf", "text/csv"})
	require.NoError(err)
	assert.NotEqual(first, sixth)

	changedManifest, err := mistralprovidertest.Manifest(policy, mistralprovidertest.WithPDFFixtureDigest(strings.Repeat("1", 16)))
	require.NoError(err)
	seventh, err := config.ProfileFingerprint(changedManifest, []string{"application/pdf", "text/csv"})
	require.NoError(err)
	assert.NotEqual(first, seventh)
}

func TestDocumentsProfileFingerprintExcludesEmbeddingOptIn(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	config := DefaultDocumentsConfig()
	config.RetentionPosture = RetentionZDR
	config.TrainingPosture = TrainingOptedOut
	policy, err := config.ExtractionPolicy()
	requirements.NoError(err)
	manifest := testCapabilityManifest(t, policy)

	before, err := config.ProfileFingerprint(manifest, []string{"application/pdf"})
	requirements.NoError(err)
	beforePolicy, err := config.ProfilePolicyJSON(manifest, []string{"application/pdf"})
	requirements.NoError(err)

	config.Index.Embeddings.Enabled = true
	config.Index.Embeddings.Profile = "vector.embeddings"
	after, err := config.ProfileFingerprint(manifest, []string{"application/pdf"})
	requirements.NoError(err)
	afterPolicy, err := config.ProfilePolicyJSON(manifest, []string{"application/pdf"})
	requirements.NoError(err)

	assertions.Equal(before, after)
	assertions.Equal(string(beforePolicy), string(afterPolicy))
}

func TestDocumentsProfilePolicyJSONRemainsByteStable(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	config := DefaultDocumentsConfig()
	config.RetentionPosture = RetentionZDR
	config.TrainingPosture = TrainingOptedOut
	config.Scope.MessageTypes = []string{"email", "chat", "email"}
	config.ApplyDefaults()
	policy, err := config.ExtractionPolicy()
	require.NoError(err)
	manifest := testCapabilityManifest(t, policy)

	policyJSON, err := config.ProfilePolicyJSON(manifest, []string{"text/csv", "application/pdf", "text/csv"})
	require.NoError(err)
	expected := `{"version":1,"provider":"mistral","endpoint":"https://api.eu.mistral.ai/v1/ocr","model":"mistral-ocr-4-0","retention":"zdr","training":"opted-out","max_file_bytes":52428800,"max_pages_per_document":500,"max_response_bytes":67108864,"max_normalized_chars":25000000,"max_spool_bytes":536870912,"min_free_space_bytes":1073741824,"request_timeout_nanos":300000000000,"max_retries":3,"max_pages_per_run":10000,"max_estimated_cost_usd_per_run":50,"message_types":["chat","email"],"allowed_media_types":["application/pdf","text/csv"],"document_policy_fingerprint":"466816abfedf47e64d25db7b38262b15d3a52077fd0856340fedd0227b600843","lexical":true,"store_chunk_text":true,"extract_header":true,"extract_footer":true,"normalization_version":3,"max_unit_chars":1000000,"max_source_unit_bytes":4000000,"max_metadata_source_bytes":65536,"max_link_chars":2048,"max_chunk_runes":4000,"chunk_overlap":200,"max_chunks":20000}`
	assert.JSONEq(expected, string(policyJSON))

	fingerprint, err := config.ProfileFingerprint(manifest, []string{"application/pdf", "text/csv"})
	require.NoError(err)
	digest := sha256.Sum256([]byte(expected))
	assert.Equal(hex.EncodeToString(digest[:]), fingerprint)

	config.Scope.MessageTypes = nil
	policyJSON, err = config.ProfilePolicyJSON(manifest, []string{"application/pdf", "text/csv"})
	require.NoError(err)
	expected = strings.Replace(expected, `"message_types":["chat","email"]`, `"message_types":null`, 1)
	assert.JSONEq(expected, string(policyJSON))
	fingerprint, err = config.ProfileFingerprint(manifest, []string{"application/pdf", "text/csv"})
	require.NoError(err)
	digest = sha256.Sum256([]byte(expected))
	assert.Equal(hex.EncodeToString(digest[:]), fingerprint)
}

func TestCSVConversionIsOptInAndBindsPDFRouteAndProfile(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	config := DefaultDocumentsConfig()
	config.RetentionPosture = RetentionZDR
	config.TrainingPosture = TrainingOptedOut
	policy, err := config.ExtractionPolicy()
	require.NoError(err)
	manifest := testCapabilityManifest(t, policy)

	disabled, err := config.ProfilePolicyJSON(manifest, []string{"application/pdf"})
	require.NoError(err)
	resolved, err := ResolveInputPolicy(&config, manifest)
	require.NoError(err)
	assert.NotContains(resolved.AllowedMediaTypes, "text/csv")
	var disabledPayload map[string]any
	require.NoError(json.Unmarshal(disabled, &disabledPayload))
	assert.NotContains(disabledPayload, "csv_conversion")

	config.Conversion.CSV.Enabled = true
	resolved, err = ResolveInputPolicy(&config, manifest)
	require.NoError(err)
	assert.Contains(resolved.AllowedMediaTypes, "application/pdf")
	assert.Contains(resolved.AllowedMediaTypes, "text/csv")
	assert.Equal("application/pdf", resolved.Routes["text/csv"].Format.MediaType)
	require.NotNil(resolved.Routes["text/csv"].Conversion)

	enabled, err := config.ProfilePolicyJSON(manifest, resolved.AllowedMediaTypes)
	require.NoError(err)
	var enabledPayload map[string]any
	require.NoError(json.Unmarshal(enabled, &enabledPayload))
	csvConversion, ok := enabledPayload["csv_conversion"].(map[string]any)
	require.True(ok)
	assert.Equal("text/csv", csvConversion["source_media_type"])
	assert.NotEqual(string(disabled), string(enabled))
}

const pptxMediaType = "application/vnd.openxmlformats-officedocument.presentationml.presentation"

func TestResolvedInputPolicyAuthorizationsFollowFormatOrderAndRejectConflicts(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	config := DefaultDocumentsConfig()
	config.RetentionPosture = RetentionZDR
	config.TrainingPosture = TrainingOptedOut
	config.Conversion.CSV.Enabled = true
	policy, err := config.ExtractionPolicy()
	require.NoError(err)
	manifest := testPPTXCapabilityManifest(t, policy)
	resolved, err := ResolveInputPolicy(&config, manifest)
	require.NoError(err)
	require.Len(resolved.Routes, 3, "direct PDF, PPTX, and CSV-to-PDF")

	for range 20 {
		authorizations, err := resolved.Authorizations(policy)
		require.NoError(err)
		ids := make([]string, len(authorizations))
		for index, authorization := range authorizations {
			ids[index] = authorization.Format().ID
		}
		assert.Equal([]string{"pdf", "pptx"}, ids, "the shared PDF authority appears once, in format order")
	}

	other, err := mistralprovidertest.Manifest(policy, mistralprovidertest.WithPDFFixtureDigest("1111111111111111"))
	require.NoError(err)
	conflicting, err := policy.Authorize(other, "pdf")
	require.NoError(err)
	csvRoute := resolved.Routes["text/csv"]
	csvRoute.Authorization = conflicting
	resolved.Routes["text/csv"] = csvRoute
	_, err = resolved.Authorizations(policy)
	require.ErrorContains(err, `conflicting upload authority for format "pdf"`)

	_, err = resolved.Authorizations(nil)
	require.ErrorContains(err, "requires a policy")
}

// testPPTXCapabilityManifest adds PPTX local-exact authority in the row shape
// an authenticated probe records.
func testPPTXCapabilityManifest(t *testing.T, policy provider.Policy) provider.Manifest {
	t.Helper()
	manifest, err := mistralprovidertest.Manifest(policy, mistralprovidertest.WithLocalExactPPTX())
	require.NoError(t, err)
	return manifest
}

func TestResolveInputPolicyAuthorizesPPTXFromLocalExactManifest(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	config := DefaultDocumentsConfig()
	config.RetentionPosture = RetentionZDR
	config.TrainingPosture = TrainingOptedOut
	policy, err := config.ExtractionPolicy()
	require.NoError(err)
	manifest := testPPTXCapabilityManifest(t, policy)
	resolved, err := ResolveInputPolicy(&config, manifest)
	require.NoError(err)
	assert.Equal([]string{"application/pdf", pptxMediaType}, resolved.AllowedMediaTypes)
	route := resolved.Routes[pptxMediaType]
	assert.Equal("pptx", route.Format.ID)
	assert.Equal(pptxMediaType, route.Format.MediaType)
	assert.Nil(route.Conversion)
	t.Logf("allowed=%v", resolved.AllowedMediaTypes)
}

func TestDocumentsProfileBindsPPTXMediaTypeAndManifestEvidence(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	config := DefaultDocumentsConfig()
	config.RetentionPosture = RetentionZDR
	config.TrainingPosture = TrainingOptedOut
	policy, err := config.ExtractionPolicy()
	require.NoError(err)
	pdfOnly := testCapabilityManifest(t, policy)
	withPPTX := testPPTXCapabilityManifest(t, policy)
	var payloads [2]map[string]any
	var fingerprints [2]string
	for i, manifest := range []provider.Manifest{pdfOnly, withPPTX} {
		resolved, err := ResolveInputPolicy(&config, manifest)
		require.NoError(err)
		policyJSON, err := config.ProfilePolicyJSON(manifest, resolved.AllowedMediaTypes)
		require.NoError(err)
		require.NoError(json.Unmarshal(policyJSON, &payloads[i]))
		fingerprints[i], err = config.ProfileFingerprint(manifest, resolved.AllowedMediaTypes)
		require.NoError(err)
	}
	assert.Equal([]any{"application/pdf", pptxMediaType}, payloads[1]["allowed_media_types"])
	assert.Equal([]any{"application/pdf"}, payloads[0]["allowed_media_types"])
	assert.NotEqual(payloads[0]["document_policy_fingerprint"], payloads[1]["document_policy_fingerprint"])
	assert.NotEqual(fingerprints[0], fingerprints[1])
	assert.Equal("466816abfedf47e64d25db7b38262b15d3a52077fd0856340fedd0227b600843", payloads[0]["document_policy_fingerprint"])
	t.Logf("PDF-only document_policy_fingerprint=%s; PPTX admitted routes=1", payloads[0]["document_policy_fingerprint"])
}

func TestResolveInputPolicyKeepsUnprovedFormatsBlocked(t *testing.T) {
	for _, csvEnabled := range []bool{false, true} {
		name := "pptx_bound_unverified"
		if csvEnabled {
			name += "_csv_enabled"
		}
		t.Run(name, func(t *testing.T) {
			config := DefaultDocumentsConfig()
			config.RetentionPosture = RetentionZDR
			config.TrainingPosture = TrainingOptedOut
			config.Conversion.CSV.Enabled = csvEnabled
			policy, err := config.ExtractionPolicy()
			require.NoError(t, err)
			resolved, err := ResolveInputPolicy(&config, testCapabilityManifest(t, policy))
			require.NoError(t, err)
			want := []string{"application/pdf"}
			if csvEnabled {
				want = append(want, "text/csv")
			}
			assert.Equal(t, want, resolved.AllowedMediaTypes)
			t.Logf("allowed=%v", resolved.AllowedMediaTypes)
		})
	}
	t.Run("unbounded_rows_stay_blocked", func(t *testing.T) {
		require := require.New(t)
		assert := assert.New(t)
		config := DefaultDocumentsConfig()
		config.RetentionPosture = RetentionZDR
		config.TrainingPosture = TrainingOptedOut
		policy, err := config.ExtractionPolicy()
		require.NoError(err)
		resolved, err := ResolveInputPolicy(&config, testPPTXCapabilityManifest(t, policy))
		require.NoError(err)
		for _, id := range []string{"docx", "xlsx", "ppt", "odt", "epub", "txt"} {
			format, found := policy.FormatByID(id)
			require.True(found)
			assert.NotContains(resolved.Routes, format.MediaType)
		}
	})
	t.Run("legacy_passing_unbounded_pptx", func(t *testing.T) {
		require := require.New(t)
		assert := assert.New(t)
		config := DefaultDocumentsConfig()
		config.RetentionPosture = RetentionZDR
		config.TrainingPosture = TrainingOptedOut
		policy, err := config.ExtractionPolicy()
		require.NoError(err)
		manifest, err := mistralprovidertest.UnvalidatedManifest(policy, mistralprovidertest.WithLegacyUnboundedPPTX())
		require.NoError(err)
		validationErr := mistralprovidertest.Validate(manifest)
		resolved, err := ResolveInputPolicy(&config, manifest)
		t.Logf("legacy validation=%v allowed=%v err=%v", validationErr, resolved.AllowedMediaTypes, err)
		require.Error(validationErr)
		require.ErrorContains(err, "no format has authorized upload authority")
		assert.Empty(resolved.AllowedMediaTypes)
		config.Conversion.CSV.Enabled = true
		_, err = ResolveInputPolicy(&config, manifest)
		require.ErrorContains(err, "no format has authorized upload authority")
	})
}

func TestDocumentsConfigResolvesAPIKeyOnlyOnDemand(t *testing.T) {
	config := DefaultDocumentsConfig()
	t.Setenv(config.APIKeyEnv, "synthetic-secret")

	key, err := config.ResolveAPIKey()
	require.NoError(t, err)
	assert.Equal(t, "synthetic-secret", key)

	config.APIKeyEnv = "MISSING_SYNTHETIC_MISTRAL_KEY"
	_, err = config.ResolveAPIKey()
	require.ErrorContains(t, err, config.APIKeyEnv)
}
