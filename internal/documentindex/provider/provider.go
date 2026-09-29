// Package provider defines the provider-neutral seam between msgvault's
// document extraction lane and the vendor packages that perform extraction.
//
// The worker, configuration, CLI, and setup code depend only on this package.
// One adapter package per provider (for example mistralprovider) translates
// these types to and from a vendor client. Adding a provider means writing an
// adapter and registering it; nothing outside the adapter names vendor types.
//
// Error classification reuses docbank's provider-neutral ocr vocabulary so a
// future adapter built on an ocr.Processor needs no translation.
package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/document/ocr"
)

// Retention and training postures are msgvault vocabulary persisted with
// extraction profiles and consents. Adapters map them to vendor terms.
const (
	RetentionUnknown  = "unknown"
	RetentionStandard = "standard"
	RetentionZDR      = "zdr"

	TrainingUnknown       = "unknown"
	TrainingDefaultOptOut = "default-opt-out"
	TrainingOptedOut      = "opted-out"
)

// Source is one authoritative document stream handed to a Processor.
type Source = ocr.Source

// NewSource validates source metadata without reading content.
func NewSource(content io.ReadCloser, mediaType string, size int64, sha256Hex string) (Source, error) {
	return ocr.NewSource(content, mediaType, size, sha256Hex) //nolint:wrapcheck // transparent alias of the neutral constructor
}

// RequestMetrics describes provider work for one logical source.
type RequestMetrics = ocr.RequestMetrics

// Error carries a stable neutral classification, request accounting, and the
// vendor cause. Adapters return it from Process; the worker switches on Kind.
type Error = ocr.ProviderError

// ErrorKind is the neutral scheduling and reporting classification.
type ErrorKind = ocr.ErrorKind

const (
	// ErrorInvalidInput marks a local source the provider cannot accept.
	ErrorInvalidInput = ocr.ErrorInvalidInput
	// ErrorCapacity marks a retryable local staging quota or free-space refusal.
	ErrorCapacity = ocr.ErrorCapacity
	// ErrorTransient marks a retryable provider, transport, or staging failure.
	ErrorTransient = ocr.ErrorTransient
	// ErrorRejected marks a permanent provider rejection.
	ErrorRejected = ocr.ErrorRejected
	// ErrorResponseTooLarge marks a response above the policy limit.
	ErrorResponseTooLarge = ocr.ErrorResponseTooLarge
	// ErrorCapabilityChanged marks provider behavior that contradicts the
	// evidence used to authorize the format.
	ErrorCapabilityChanged = ocr.ErrorCapabilityChanged
	// ErrorMalformedOutput marks unusable provider output.
	ErrorMalformedOutput = ocr.ErrorMalformedOutput
)

// ErrorKindOf returns the first neutral classification in err, or "".
func ErrorKindOf(err error) ErrorKind { return ocr.ErrorKindOf(err) }

// MetricsFromError recovers request accounting from a failed Process call.
func MetricsFromError(err error) RequestMetrics { return ocr.MetricsFromError(err) }

// IsRetryable reports whether the source may be scheduled again later.
func IsRetryable(err error) bool { return ocr.IsRetryable(err) }

// Result is the provider-neutral outcome of one successful Process call. The
// worker normalizes Document itself; adapters return provider Markdown units.
type Result struct {
	Document       document.SourceDocument
	ReturnedModel  string
	UnitsProcessed int
	ProviderBytes  *int64
	Metrics        RequestMetrics
	// CleanupError reports a failure to remove provider-owned staging after a
	// successful request. It never fails the extraction.
	CleanupError error
}

// Processor extracts one source. Process takes ownership of Source.Content
// and closes it on every path. Errors are *Error values whose Kind drives
// retry and reason-code classification.
type Processor interface {
	Process(ctx context.Context, source Source) (Result, error)
	// PolicyFingerprint is Policy.Fingerprint(manifest) for the exact policy
	// and capability evidence the processor was built from. A worker refuses
	// a processor whose fingerprint differs from its own.
	PolicyFingerprint() string
}

// Defaults are the configuration values a provider supplies for omitted
// fields.
type Defaults struct {
	Region         string
	Model          string
	APIKeyEnv      string
	RequestTimeout time.Duration
	MaxRetries     int
}

// Limits are the hard safety bounds a provider accepts.
type Limits struct {
	MaxDocumentBytes  int64
	MaxResponseBytes  int64
	MaxUnits          int
	MaxRequestTimeout time.Duration
	MaxRetries        int
}

// PolicyConfig is the reusable processing and privacy policy derived from
// application configuration. Run budgets and storage choices are not policy.
type PolicyConfig struct {
	Region           string
	Model            string
	Retention        string
	Training         string
	MaxDocumentBytes int64
	MaxResponseBytes int64
	MaxUnits         int
	ExtractHeader    bool
	ExtractFooter    bool
	NormalizePolicy  document.NormalizePolicy
}

// PolicyValues is a read-only copy of every effective policy value.
type PolicyValues struct {
	Provider         string
	Endpoint         string
	Region           string
	Model            string
	Retention        string
	Training         string
	MaxDocumentBytes int64
	MaxResponseBytes int64
	MaxUnits         int
	ExtractHeader    bool
	ExtractFooter    bool
	Normalization    document.NormalizePolicyIdentity
}

// Format describes one document format a provider can detect. A format does
// not authorize an upload; Policy.Authorize does.
type Format struct {
	ID        string
	Family    string
	MediaType string
	UnitKind  string
	// RawUploadBounded is false when the provider can never enforce a unit
	// bound on the format as uploaded, so it is routed only through a local
	// conversion (see Policy.ConversionTarget) and never as-is.
	RawUploadBounded bool
}

// Manifest is a provider's validated capability evidence. Its concrete type
// belongs to the adapter that decoded it; other packages hold it opaquely.
type Manifest interface {
	// MaxUnits is the per-document unit authority the evidence covers.
	MaxUnits() int
}

// Authorization is non-persistable evidence that one format has enforceable
// upload authority under a policy and manifest. Its concrete type belongs to
// the adapter that issued it; NewProcessor consumes the set a build resolved
// so authority is derived once per build, not per document.
type Authorization interface {
	Format() Format
	// PolicyFingerprint is Policy.Fingerprint(manifest) for the pair that
	// issued the authorization.
	PolicyFingerprint() string
}

// Policy is an immutable processing policy bound to one provider.
type Policy interface {
	Values() PolicyValues
	NormalizePolicy() document.NormalizePolicy
	// Formats lists every candidate format in the provider's stable order.
	Formats() []Format
	FormatByID(id string) (Format, bool)
	// ConversionTarget names the format the provider receives when a source
	// of sourceMediaType is converted locally before upload.
	ConversionTarget(sourceMediaType string) (Format, bool)
	// Authorize derives formatID's upload authority from manifest, or reports
	// why the manifest grants none.
	Authorize(manifest Manifest, formatID string) (Authorization, error)
	// Fingerprint digests the policy together with the manifest evidence.
	Fingerprint(manifest Manifest) (string, error)
}

// Staging bounds the provider's private local staging area.
type Staging struct {
	Directory    string
	MaxBytes     int64
	MinFreeBytes int64
}

// ClientConfig contains bounded operational settings outside policy identity.
type ClientConfig struct {
	APIKey     string
	Timeout    time.Duration
	MaxRetries int
	// HTTPClient overrides the transport; nil uses the adapter default.
	HTTPClient *http.Client
}

// ProbeFixtureConfig identifies the private fixture matrix a capability probe
// uploads and its dedicated staging area.
type ProbeFixtureConfig struct {
	FixtureDirectory string
	Staging          Staging
}

// ProbeConfig controls one explicit authenticated capability probe. A zero
// ObservedAt means now.
type ProbeConfig struct {
	Fixtures   ProbeFixtureConfig
	ObservedAt time.Time
}

// Provider is one document extraction backend.
type Provider interface {
	// Name is the configuration value that selects the provider.
	Name() string
	// DisplayName is the vendor name as shown in user-facing messages.
	DisplayName() string
	Defaults() Defaults
	Limits() Limits
	NewPolicy(config PolicyConfig) (Policy, error)
	DecodeManifest(reader io.Reader) (Manifest, error)
	EncodeManifest(writer io.Writer, manifest Manifest) error
	// NewProcessor binds a policy, its capability evidence, the upload
	// authority a build resolved under them, credentials, and staging bounds
	// into a Processor. It derives Policy.Fingerprint(manifest) itself and
	// refuses any authorization issued under a different pair, so the
	// worker's fingerprint check and the processor's authority agree. It
	// makes no network request.
	NewProcessor(policy Policy, manifest Manifest, authorizations []Authorization, client ClientConfig, staging Staging) (Processor, error)
	// ValidateProbeFixtures stages the fixture matrix locally without
	// credentials or network access.
	ValidateProbeFixtures(ctx context.Context, policy Policy, fixtures ProbeFixtureConfig) error
	// RunCapabilityProbe uploads the fixture matrix and returns sanitized
	// evidence.
	RunCapabilityProbe(ctx context.Context, policy Policy, client ClientConfig, probe ProbeConfig) (Manifest, error)
	// ScavengeStaging removes stale provider-owned staging files and returns
	// how many were removed.
	ScavengeStaging(directory string, staleBefore time.Time) (int, error)
}

// ErrUnknownProvider is returned by Registry.Lookup for an unregistered name.
var ErrUnknownProvider = errors.New("unknown document provider")
