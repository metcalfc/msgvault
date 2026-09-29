package mistralprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/document/mistral"
	"go.kenn.io/docbank/document/mistral/mistraltest"
	"go.kenn.io/msgvault/internal/documentindex/provider"
	"go.kenn.io/msgvault/internal/fileutil"
)

func TestDefaultsAndLimitsMirrorVendorConstants(t *testing.T) {
	assert := assert.New(t)
	adapter := New()
	assert.Equal("mistral", adapter.Name())
	assert.Equal(provider.Defaults{
		Region: mistral.RegionEU, Model: mistral.DefaultModel, APIKeyEnv: "MISTRAL_API_KEY",
		RequestTimeout: mistral.DefaultTimeout, MaxRetries: mistral.DefaultMaxRetries,
	}, adapter.Defaults())
	assert.Equal(provider.Limits{
		MaxDocumentBytes: mistral.MaxDocumentBytes, MaxResponseBytes: mistral.MaxResponseBytes,
		MaxUnits: mistral.MaxUnits, MaxRequestTimeout: mistral.MaxTimeout, MaxRetries: mistral.MaxRetries,
	}, adapter.Limits())
	assert.Len(CandidateFormats(), len(mistral.CandidateFormats()))
}

func TestPolicyExposesVendorValuesAndFormats(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	policy := testPolicy(t)
	values := policy.Values()
	assert.Equal("mistral", values.Provider)
	assert.Equal("https://api.eu.mistral.ai/v1/ocr", values.Endpoint)
	assert.Equal(mistral.DefaultModel, values.Model)
	assert.Equal(100, values.MaxUnits)
	assert.Positive(values.Normalization.Version)
	pdf, found := policy.FormatByID("pdf")
	require.True(found)
	assert.Equal(provider.Format{ID: "pdf", Family: "pdf", MediaType: "application/pdf", UnitKind: "page"}, pdf)
	_, found = policy.FormatByID("unknown")
	assert.False(found)
	assert.Equal(CandidateFormats(), policy.Formats())
}

func TestNewPolicyRejectsUnknownPostures(t *testing.T) {
	normalizePolicy, err := document.NewNormalizePolicy(1_000_000)
	require.NoError(t, err)
	_, err = New().NewPolicy(provider.PolicyConfig{
		Region: RegionEU, Model: DefaultModel, Retention: "unknown", Training: provider.TrainingOptedOut,
		MaxDocumentBytes: 1 << 20, MaxResponseBytes: 1 << 20, MaxUnits: 100, NormalizePolicy: normalizePolicy,
	})
	require.ErrorContains(t, err, "configure Mistral document policy")
}

func TestManifestRoundTripsThroughEncodeAndDecode(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	policy := testPolicy(t)
	manifest := testManifest(t, policy)
	var encoded bytes.Buffer
	require.NoError(New().EncodeManifest(&encoded, manifest))
	decoded, err := New().DecodeManifest(bytes.NewReader(encoded.Bytes()))
	require.NoError(err)
	assert.Equal(100, decoded.MaxUnits())
	require.NoError(policy.Authorize(decoded, "pdf"))
	require.ErrorContains(policy.Authorize(decoded, "docx"), "no enforceable unit bound")
	first, err := policy.Fingerprint(manifest)
	require.NoError(err)
	second, err := policy.Fingerprint(decoded)
	require.NoError(err)
	assert.Equal(first, second)
}

type foreignManifest struct{}

func (foreignManifest) MaxUnits() int { return 1 }

type foreignPolicy struct{ provider.Policy }

func TestAdapterRejectsForeignPolicyAndManifest(t *testing.T) {
	require := require.New(t)
	policy := testPolicy(t)
	manifest := testManifest(t, policy)
	staging := provider.Staging{Directory: t.TempDir(), MaxBytes: 2 << 20, MinFreeBytes: 1}
	client := provider.ClientConfig{APIKey: "synthetic-key"}

	require.ErrorContains(policy.Authorize(foreignManifest{}, "pdf"), "does not belong to the mistral provider")
	_, err := policy.Fingerprint(foreignManifest{})
	require.ErrorContains(err, "does not belong to the mistral provider")
	require.ErrorContains(New().EncodeManifest(io.Discard, foreignManifest{}), "does not belong to the mistral provider")
	_, err = New().NewProcessor(foreignPolicy{}, manifest, client, staging)
	require.ErrorContains(err, "does not belong to the mistral provider")
	_, err = New().NewProcessor(policy, foreignManifest{}, client, staging)
	require.ErrorContains(err, "does not belong to the mistral provider")
	_, err = New().NewProcessor(policy, manifest, client, provider.Staging{Directory: "", MaxBytes: 2 << 20, MinFreeBytes: 1})
	require.ErrorContains(err, "staging bounds are invalid")
	_, err = New().NewProcessor(policy, manifest, provider.ClientConfig{APIKey: " padded "}, staging)
	require.ErrorContains(err, "configure mistral document client")
	require.ErrorContains(New().ValidateProbeFixtures(t.Context(), foreignPolicy{}, provider.ProbeFixtureConfig{}), "does not belong")
	_, err = New().RunCapabilityProbe(t.Context(), foreignPolicy{}, client, provider.ProbeConfig{})
	require.ErrorContains(err, "does not belong")
}

func TestTranslateStagingErrorClassifiesLocalFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		kind provider.ErrorKind
	}{
		{name: "capacity", err: fmt.Errorf("%w: quota", mistral.ErrSpoolCapacity), kind: provider.ErrorCapacity},
		{name: "unavailable", err: fmt.Errorf("%w: io", mistral.ErrSpoolUnavailable), kind: provider.ErrorTransient},
		{name: "invalid source", err: fmt.Errorf("%w: hash", mistral.ErrInvalidSource), kind: provider.ErrorInvalidInput},
		{name: "unknown local failure", err: errors.New("count units failed"), kind: provider.ErrorInvalidInput},
		{name: "canceled", err: context.Canceled, kind: provider.ErrorTransient},
		{name: "deadline", err: fmt.Errorf("wrapped: %w", context.DeadlineExceeded), kind: provider.ErrorTransient},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			translated := translateStagingError(test.err)
			assert.Equal(t, test.kind, provider.ErrorKindOf(translated))
			assert.Equal(t, provider.RequestMetrics{}, provider.MetricsFromError(translated))
			require.ErrorIs(t, translated, test.err)
		})
	}
}

func TestTranslateRequestErrorClassifiesProviderFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		kind provider.ErrorKind
	}{
		{name: "transient", err: fmt.Errorf("%w: 503", mistral.ErrTransientResponse), kind: provider.ErrorTransient},
		{name: "permanent", err: fmt.Errorf("%w: 400", mistral.ErrPermanentResponse), kind: provider.ErrorRejected},
		{name: "too large", err: mistral.ErrResponseTooLarge, kind: provider.ErrorResponseTooLarge},
		{name: "capability", err: fmt.Errorf("drift: %w", mistral.ErrCapabilityContract), kind: provider.ErrorCapabilityChanged},
		{name: "canceled", err: context.Canceled, kind: provider.ErrorTransient},
		{name: "unrecognized", err: errors.New("authorization belongs to a different policy"), kind: provider.ErrorMalformedOutput},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			translated := translateRequestError(test.err)
			assert.Equal(t, test.kind, provider.ErrorKindOf(translated))
			require.ErrorIs(t, translated, test.err)
		})
	}
}

func TestProcessorStagesAuthorizesSendsAndCleansUp(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	content := mistraltest.MinimalPDF("adapter test")
	transport := &syntheticTransport{processed: 1, markdown: "# Adapter\nevidence", sourceLen: len(content)}
	processor, spoolDirectory := testProcessor(t, transport)
	source, closed := pdfSource(t, content)

	result, err := processor.Process(t.Context(), source)
	require.NoError(err)
	assert.Equal(int32(1), transport.calls.Load())
	assert.Equal(mistral.DefaultModel, result.ReturnedModel)
	assert.Equal(1, result.UnitsProcessed)
	require.Len(result.Document.Units, 1)
	assert.Equal("# Adapter\nevidence", result.Document.Units[0].Markdown)
	assert.Equal(1, result.Metrics.Requests)
	require.NotNil(result.ProviderBytes)
	assert.Equal(int64(len(content)), *result.ProviderBytes)
	require.NoError(result.CleanupError)
	assert.Equal(int32(1), closed.closes.Load(), "the processor owns and closes the source")
	entries, err := os.ReadDir(spoolDirectory)
	require.NoError(err)
	assert.Len(entries, 1, "only the package reservation lock remains after the request")
}

func TestProcessorReportsInvalidLocalSourceWithoutRequest(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	content := mistraltest.MinimalPDF("adapter test")
	transport := &syntheticTransport{processed: 1, markdown: "unreachable", sourceLen: len(content)}
	processor, spoolDirectory := testProcessor(t, transport)
	wrongHash := strings.Repeat("a", 64)
	closed := &countingReadCloser{Reader: bytes.NewReader(content)}
	source, err := provider.NewSource(closed, "application/pdf", int64(len(content)), wrongHash)
	require.NoError(err)

	_, err = processor.Process(t.Context(), source)
	require.Error(err)
	assert.Equal(provider.ErrorInvalidInput, provider.ErrorKindOf(err))
	require.ErrorIs(err, mistral.ErrInvalidSource)
	assert.Zero(transport.calls.Load())
	assert.Equal(int32(1), closed.closes.Load())
	entries, err := os.ReadDir(spoolDirectory)
	require.NoError(err)
	assert.Len(entries, 1, "the failed spool file is removed")
}

func TestProcessorRejectsUnvalidatedSourceAndClosesIt(t *testing.T) {
	require := require.New(t)
	processor, _ := testProcessor(t, &syntheticTransport{})
	content := &countingReadCloser{Reader: bytes.NewReader([]byte("x"))}
	_, err := processor.Process(t.Context(), provider.Source{Content: content, MediaType: "application/pdf", Size: 0})
	require.Error(err)
	assert.Equal(t, provider.ErrorInvalidInput, provider.ErrorKindOf(err))
	assert.Equal(t, int32(1), content.closes.Load())
}

func TestProcessorTranslatesProviderRejection(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	content := mistraltest.MinimalPDF("adapter test")
	transport := &syntheticTransport{status: http.StatusBadRequest, sourceLen: len(content)}
	processor, _ := testProcessor(t, transport)

	source, _ := pdfSource(t, content)
	_, err := processor.Process(t.Context(), source)
	require.Error(err)
	assert.Equal(provider.ErrorRejected, provider.ErrorKindOf(err))
	assert.Equal(1, provider.MetricsFromError(err).Requests)
	assert.False(provider.IsRetryable(err))
}

func TestProcessorRefusesFormatsWithoutAuthority(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	content := []byte("plain text is a candidate format without probe authority")
	transport := &syntheticTransport{processed: 1, markdown: "unreachable", sourceLen: len(content)}
	processor, _ := testProcessor(t, transport)
	digest := sha256.Sum256(content)
	source, err := provider.NewSource(&countingReadCloser{Reader: bytes.NewReader(content)}, "text/plain", int64(len(content)), hex.EncodeToString(digest[:]))
	require.NoError(err)

	_, err = processor.Process(t.Context(), source)
	require.Error(err)
	assert.Equal(provider.ErrorCapabilityChanged, provider.ErrorKindOf(err))
	assert.Zero(transport.calls.Load())
}

func TestNilProcessorClosesSource(t *testing.T) {
	var processor *Processor
	content := &countingReadCloser{Reader: bytes.NewReader([]byte("x"))}
	_, err := processor.Process(t.Context(), provider.Source{Content: content})
	require.ErrorContains(t, err, "nil")
	assert.Equal(t, int32(1), content.closes.Load())
}

func TestScavengeStagingRemovesStaleSpoolFiles(t *testing.T) {
	require := require.New(t)
	directory := filepath.Join(t.TempDir(), "spool")
	require.NoError(fileutil.SecureMkdirAll(directory, 0o700))
	stale := filepath.Join(directory, ".mistral-ocr-stale")
	require.NoError(os.WriteFile(stale, []byte("stale"), 0o600))
	old := time.Now().Add(-3 * time.Hour)
	require.NoError(os.Chtimes(stale, old, old))
	removed, err := New().ScavengeStaging(directory, time.Now().Add(-2*time.Hour))
	require.NoError(err)
	assert.Equal(t, 1, removed)
	_, err = os.Stat(stale)
	require.ErrorIs(err, os.ErrNotExist)
}

func testPolicy(t *testing.T) provider.Policy {
	t.Helper()
	normalizePolicy, err := document.NewNormalizePolicy(1_000_000)
	require.NoError(t, err)
	policy, err := New().NewPolicy(provider.PolicyConfig{
		Region: RegionEU, Model: DefaultModel,
		Retention: provider.RetentionZDR, Training: provider.TrainingOptedOut,
		MaxDocumentBytes: 1 << 20, MaxResponseBytes: 1 << 20, MaxUnits: 100,
		ExtractHeader: true, ExtractFooter: true, NormalizePolicy: normalizePolicy,
	})
	require.NoError(t, err)
	return policy
}

func testManifest(t *testing.T, policy provider.Policy) provider.Manifest {
	t.Helper()
	vendorPolicy, err := VendorPolicy(policy)
	require.NoError(t, err)
	manifest, err := mistraltest.SyntheticManifest(vendorPolicy, true)
	require.NoError(t, err)
	return NewManifest(manifest)
}

func testProcessor(t *testing.T, transport http.RoundTripper) (provider.Processor, string) {
	t.Helper()
	spoolDirectory := filepath.Join(t.TempDir(), "spool")
	require.NoError(t, fileutil.SecureMkdirAll(spoolDirectory, 0o700))
	policy := testPolicy(t)
	processor, err := New().NewProcessor(policy, testManifest(t, policy), provider.ClientConfig{
		APIKey: "synthetic-key", MaxRetries: 1, HTTPClient: &http.Client{Transport: transport},
	}, provider.Staging{Directory: spoolDirectory, MaxBytes: 2 << 20, MinFreeBytes: 1})
	require.NoError(t, err)
	return processor, spoolDirectory
}

// pdfSource returns a validated PDF source and the close-counting stream
// behind it.
func pdfSource(t *testing.T, content []byte) (provider.Source, *countingReadCloser) {
	t.Helper()
	digest := sha256.Sum256(content)
	closed := &countingReadCloser{Reader: bytes.NewReader(content)}
	source, err := provider.NewSource(closed, "application/pdf", int64(len(content)), hex.EncodeToString(digest[:]))
	require.NoError(t, err)
	return source, closed
}

type countingReadCloser struct {
	io.Reader

	closes atomic.Int32
}

func (c *countingReadCloser) Close() error {
	c.closes.Add(1)
	return nil
}

// syntheticTransport answers the Mistral OCR request shape without a network.
type syntheticTransport struct {
	processed int
	markdown  string
	sourceLen int
	status    int
	calls     atomic.Int32
}

func (s *syntheticTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	s.calls.Add(1)
	_, readErr := io.Copy(io.Discard, request.Body)
	if err := errors.Join(readErr, request.Body.Close()); err != nil {
		return nil, err
	}
	if s.status != 0 && s.status != http.StatusOK {
		return &http.Response{
			StatusCode: s.status, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"message":"synthetic rejection"}`)),
		}, nil
	}
	pages := make([]map[string]any, s.processed)
	for index := range pages {
		pages[index] = map[string]any{"index": index, "markdown": s.markdown}
	}
	body, err := json.Marshal(map[string]any{
		"model": mistral.DefaultModel, "pages": pages,
		"usage_info": map[string]any{"pages_processed": s.processed, "doc_size_bytes": s.sourceLen},
	})
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(bytes.NewReader(body)),
	}, nil
}
