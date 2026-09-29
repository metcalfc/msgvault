// Package mistralprovider adapts docbank's Mistral OCR client to the
// provider-neutral document extraction seam. It is the only msgvault package
// that imports go.kenn.io/docbank/document/mistral.
package mistralprovider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/document/mistral"
	"go.kenn.io/msgvault/internal/documentindex/provider"
)

const (
	// Name is the configuration value that selects this provider.
	Name = "mistral"
	// DefaultModel is the pinned Mistral OCR model.
	DefaultModel = mistral.DefaultModel
	// RegionEU is the only supported Mistral processing region.
	RegionEU = mistral.RegionEU
	// MinUnits is the smallest per-document unit limit the vendor policy accepts.
	MinUnits = mistral.MinUnits

	defaultAPIKeyEnv = "MISTRAL_API_KEY" // #nosec G101 -- environment variable name, not a credential.

	// csvFormatID is the only candidate format Mistral cannot bound as a raw
	// upload; it is routed through local CSV-to-PDF conversion instead.
	csvFormatID        = "csv"
	csvMediaType       = "text/csv"
	conversionTargetID = "pdf"
)

// Provider is the Mistral OCR adapter. The zero value is ready to use.
type Provider struct{}

var _ provider.Provider = Provider{}

// New returns the Mistral adapter.
func New() Provider { return Provider{} }

// Name implements provider.Provider.
func (Provider) Name() string { return Name }

// DisplayName implements provider.Provider.
func (Provider) DisplayName() string { return "Mistral" }

// Defaults implements provider.Provider.
func (Provider) Defaults() provider.Defaults {
	return provider.Defaults{
		Region: RegionEU, Model: DefaultModel, APIKeyEnv: defaultAPIKeyEnv,
		RequestTimeout: mistral.DefaultTimeout, MaxRetries: mistral.DefaultMaxRetries,
	}
}

// Limits implements provider.Provider.
func (Provider) Limits() provider.Limits {
	return provider.Limits{
		MaxDocumentBytes: mistral.MaxDocumentBytes, MaxResponseBytes: mistral.MaxResponseBytes,
		MaxUnits: mistral.MaxUnits, MaxRequestTimeout: mistral.MaxTimeout, MaxRetries: mistral.MaxRetries,
	}
}

// CandidateFormats lists every format the vendor can detect locally.
func CandidateFormats() []provider.Format {
	return neutralFormats(mistral.CandidateFormats())
}

// NewPolicy implements provider.Provider.
func (Provider) NewPolicy(config provider.PolicyConfig) (provider.Policy, error) {
	policy, err := mistral.NewPolicy(mistral.PolicyConfig{
		Region: config.Region, Model: config.Model,
		Retention: config.Retention, Training: config.Training,
		MaxDocumentBytes: config.MaxDocumentBytes, MaxResponseBytes: config.MaxResponseBytes,
		MaxUnits: config.MaxUnits, ExtractHeader: config.ExtractHeader, ExtractFooter: config.ExtractFooter,
		NormalizePolicy: config.NormalizePolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("configure Mistral document policy: %w", err)
	}
	return Policy{policy: policy}, nil
}

// DecodeManifest implements provider.Provider. It strictly decodes and
// validates a complete capability manifest.
func (Provider) DecodeManifest(reader io.Reader) (provider.Manifest, error) {
	manifest, err := mistral.DecodeCapabilityManifest(reader)
	if err != nil {
		return nil, err //nolint:wrapcheck // vendor decode errors already name the operation and reach the CLI verbatim
	}
	return Manifest{manifest: manifest}, nil
}

// EncodeManifest implements provider.Provider.
func (Provider) EncodeManifest(writer io.Writer, manifest provider.Manifest) error {
	vendor, err := vendorManifestRef(manifest)
	if err != nil {
		return err
	}
	return mistral.EncodeCapabilityManifest(writer, vendor) //nolint:wrapcheck // callers add the write context
}

// NewProcessor implements provider.Provider. The authorizations are the
// authority a build already resolved; nothing is re-derived here.
func (Provider) NewProcessor(
	policy provider.Policy,
	authorizations []provider.Authorization,
	client provider.ClientConfig,
	staging provider.Staging,
) (provider.Processor, error) {
	vendorPolicy, err := VendorPolicy(policy)
	if err != nil {
		return nil, err
	}
	if staging.Directory == "" || staging.MaxBytes < vendorPolicy.Values().MaxDocumentBytes || staging.MinFreeBytes <= 0 {
		return nil, errors.New("mistral document staging bounds are invalid")
	}
	if len(authorizations) == 0 {
		return nil, errors.New("mistral document processor requires at least one authorized format")
	}
	byFormat := make(map[string]mistral.FormatAuthorization, len(authorizations))
	fingerprint := ""
	for _, candidate := range authorizations {
		wrapped, ok := candidate.(Authorization)
		if !ok {
			return nil, fmt.Errorf("document authorization %T does not belong to the %s provider", candidate, Name)
		}
		if fingerprint == "" {
			fingerprint = wrapped.PolicyFingerprint()
		}
		if wrapped.PolicyFingerprint() == "" || wrapped.PolicyFingerprint() != fingerprint {
			return nil, errors.New("mistral document authorizations belong to different policies or manifests")
		}
		byFormat[wrapped.authorization.Format().ID] = wrapped.authorization
	}
	vendorClient, err := newClient(vendorPolicy, client)
	if err != nil {
		return nil, err
	}
	return &Processor{
		client: vendorClient, policy: vendorPolicy, staging: staging,
		authorizations: byFormat, policyFingerprint: fingerprint,
	}, nil
}

// ValidateProbeFixtures implements provider.Provider.
func (Provider) ValidateProbeFixtures(
	ctx context.Context,
	policy provider.Policy,
	fixtures provider.ProbeFixtureConfig,
) error {
	vendorPolicy, err := VendorPolicy(policy)
	if err != nil {
		return err
	}
	return mistral.ValidateProbeFixtures(ctx, vendorPolicy, fixtureConfig(fixtures)) //nolint:wrapcheck // probe output shows the vendor message verbatim
}

// RunCapabilityProbe implements provider.Provider.
func (Provider) RunCapabilityProbe(
	ctx context.Context,
	policy provider.Policy,
	client provider.ClientConfig,
	probe provider.ProbeConfig,
) (provider.Manifest, error) {
	vendorPolicy, err := VendorPolicy(policy)
	if err != nil {
		return nil, err
	}
	vendorClient, err := newClient(vendorPolicy, client)
	if err != nil {
		return nil, err
	}
	manifest, err := mistral.RunCapabilityProbe(ctx, vendorClient, mistral.ProbeConfig{
		Fixtures: fixtureConfig(probe.Fixtures), ObservedAt: probe.ObservedAt,
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // probe output shows the vendor message verbatim
	}
	return Manifest{manifest: manifest}, nil
}

// ScavengeStaging implements provider.Provider.
func (Provider) ScavengeStaging(directory string, staleBefore time.Time) (int, error) {
	return mistral.ScavengeSpoolDirectory(directory, staleBefore) //nolint:wrapcheck // callers add the scavenge context
}

// WriteProbeFixtures builds the private synthetic fixture matrix consumed by
// the capability probe. It makes no provider request.
func WriteProbeFixtures(ctx context.Context, destination, seedDirectory string) error {
	return mistral.WriteProbeFixtures(ctx, destination, mistral.FixtureOptions{SeedDirectory: seedDirectory}) //nolint:wrapcheck // the fixture builder adds its own context
}

func newClient(policy mistral.Policy, config provider.ClientConfig) (*mistral.Client, error) {
	client, err := mistral.NewClient(policy, mistral.ClientConfig{
		APIKey: config.APIKey, Timeout: config.Timeout,
		MaxRetries: config.MaxRetries, HTTPClient: config.HTTPClient,
	})
	if err != nil {
		return nil, fmt.Errorf("configure mistral document client: %w", err)
	}
	return client, nil
}

func fixtureConfig(fixtures provider.ProbeFixtureConfig) mistral.ProbeFixtureConfig {
	return mistral.ProbeFixtureConfig{
		FixtureDirectory: fixtures.FixtureDirectory, SpoolDirectory: fixtures.Staging.Directory,
		MaxSpoolBytes: fixtures.Staging.MaxBytes, MinFreeBytes: fixtures.Staging.MinFreeBytes,
	}
}

// Policy wraps the immutable vendor policy.
type Policy struct {
	policy mistral.Policy
}

var _ provider.Policy = Policy{}

// VendorPolicy unwraps a policy produced by this adapter. Only the adapter and
// its test helpers should need the vendor type.
func VendorPolicy(policy provider.Policy) (mistral.Policy, error) {
	wrapped, ok := policy.(Policy)
	if !ok {
		return mistral.Policy{}, fmt.Errorf("document policy %T does not belong to the %s provider", policy, Name)
	}
	return wrapped.policy, nil
}

// Values implements provider.Policy. The neutral and vendor value structs
// share one field set, so the conversion fails to compile if either drifts.
func (p Policy) Values() provider.PolicyValues {
	return provider.PolicyValues(p.policy.Values())
}

// NormalizePolicy implements provider.Policy.
func (p Policy) NormalizePolicy() document.NormalizePolicy { return p.policy.NormalizePolicy() }

// Formats implements provider.Policy.
func (Policy) Formats() []provider.Format { return CandidateFormats() }

// FormatByID implements provider.Policy.
func (Policy) FormatByID(id string) (provider.Format, bool) {
	format, found := mistral.CandidateFormatByID(id)
	if !found {
		return provider.Format{}, false
	}
	return neutralFormat(format), true
}

// ConversionTarget implements provider.Policy. CSV is the only source
// converted locally; the generated PDF carries an enforceable page bound.
func (p Policy) ConversionTarget(sourceMediaType string) (provider.Format, bool) {
	if sourceMediaType != csvMediaType {
		return provider.Format{}, false
	}
	return p.FormatByID(conversionTargetID)
}

// Authorize implements provider.Policy.
func (p Policy) Authorize(manifest provider.Manifest, formatID string) (provider.Authorization, error) {
	vendorManifest, err := vendorManifestRef(manifest)
	if err != nil {
		return nil, err
	}
	authorization, err := p.policy.Authorize(vendorManifest, formatID)
	if err != nil {
		return nil, err //nolint:wrapcheck // callers decide on presence or add route context; the vendor text is the contract
	}
	return Authorization{authorization: authorization}, nil
}

// Fingerprint implements provider.Policy.
func (p Policy) Fingerprint(manifest provider.Manifest) (string, error) {
	vendorManifest, err := vendorManifestRef(manifest)
	if err != nil {
		return "", err
	}
	return p.policy.Fingerprint(vendorManifest) //nolint:wrapcheck // callers add the fingerprint context
}

// Authorization wraps the vendor's opaque format authority.
type Authorization struct {
	authorization mistral.FormatAuthorization
}

var _ provider.Authorization = Authorization{}

// Format implements provider.Authorization.
func (a Authorization) Format() provider.Format { return neutralFormat(a.authorization.Format()) }

// PolicyFingerprint implements provider.Authorization.
func (a Authorization) PolicyFingerprint() string { return a.authorization.PolicyFingerprint() }

// Manifest wraps validated vendor capability evidence.
type Manifest struct {
	manifest mistral.CapabilityManifest
}

var _ provider.Manifest = Manifest{}

// NewManifest wraps vendor evidence without validating it. Authorize and
// Fingerprint validate before granting authority.
func NewManifest(manifest mistral.CapabilityManifest) Manifest {
	return Manifest{manifest: cloneManifest(manifest)}
}

// VendorManifest unwraps evidence produced by this adapter. The copy is
// independent, so callers may mutate it.
func VendorManifest(manifest provider.Manifest) (mistral.CapabilityManifest, error) {
	vendor, err := vendorManifestRef(manifest)
	if err != nil {
		return mistral.CapabilityManifest{}, err
	}
	return cloneManifest(vendor), nil
}

// vendorManifestRef unwraps evidence without copying, for read-only use.
func vendorManifestRef(manifest provider.Manifest) (mistral.CapabilityManifest, error) {
	wrapped, ok := manifest.(Manifest)
	if !ok {
		return mistral.CapabilityManifest{}, fmt.Errorf(
			"document capability manifest %T does not belong to the %s provider", manifest, Name,
		)
	}
	return wrapped.manifest, nil
}

// MaxUnits implements provider.Manifest.
func (m Manifest) MaxUnits() int { return m.manifest.MaxUnits }

func cloneManifest(manifest mistral.CapabilityManifest) mistral.CapabilityManifest {
	clone := manifest
	clone.Results = make([]mistral.CapabilityResult, len(manifest.Results))
	copy(clone.Results, manifest.Results)
	for index := range clone.Results {
		if clone.Results[index].ProviderBytes != nil {
			providerBytes := *clone.Results[index].ProviderBytes
			clone.Results[index].ProviderBytes = &providerBytes
		}
	}
	return clone
}

func neutralFormats(formats []mistral.CandidateFormat) []provider.Format {
	neutral := make([]provider.Format, len(formats))
	for index, format := range formats {
		neutral[index] = neutralFormat(format)
	}
	return neutral
}

func neutralFormat(format mistral.CandidateFormat) provider.Format {
	return provider.Format{
		ID: format.ID, Family: format.Family, MediaType: format.MediaType, UnitKind: format.UnitKind,
		RawUploadBounded: format.ID != csvFormatID,
	}
}

// Processor stages one source in a private spool, checks its detected format
// against the authority the build resolved, sends it, and removes the spool.
//
// This mirrors docbank's own mistral.Processor pipeline rather than wrapping
// it: the vendor processor re-derives authority per upload, joins cleanup
// errors unconditionally, and normalizes internally, each of which conflicts
// with the seam's contract. Its error classification is reproduced verbatim
// in classifyProcessorError and pinned by tests.
type Processor struct {
	client            *mistral.Client
	policy            mistral.Policy
	staging           provider.Staging
	authorizations    map[string]mistral.FormatAuthorization
	policyFingerprint string
}

var _ provider.Processor = (*Processor)(nil)

// PolicyFingerprint implements provider.Processor.
func (p *Processor) PolicyFingerprint() string {
	if p == nil {
		return ""
	}
	return p.policyFingerprint
}

// Process implements provider.Processor. Raw provider JSON is never persisted.
func (p *Processor) Process(ctx context.Context, source provider.Source) (result provider.Result, err error) {
	if p == nil {
		closeSource(source)
		return provider.Result{}, errors.New("mistral document processor is nil")
	}
	if validateErr := source.Validate(); validateErr != nil {
		closeSource(source)
		return provider.Result{}, &provider.Error{Kind: provider.ErrorInvalidInput, Cause: validateErr}
	}
	prepared, err := mistral.Prepare(ctx, source.Content, p.policy, mistral.PrepareOptions{
		Directory: p.staging.Directory, DeclaredMediaType: source.MediaType,
		ExpectedSize: source.Size, ExpectedSHA256: source.SHA256,
		MaxSpoolBytes: p.staging.MaxBytes, MinFreeBytes: p.staging.MinFreeBytes,
	})
	if err != nil {
		return provider.Result{}, classifyProcessorError(ctx, err, mistral.RequestMetrics{})
	}
	defer func() {
		cleanupErr := prepared.Release()
		if cleanupErr == nil {
			return
		}
		// A failed request keeps its *provider.Error at the head of the chain
		// unless cleanup also failed; then both are reported.
		if err != nil {
			err = errors.Join(err, cleanupErr)
			return
		}
		result.CleanupError = cleanupErr
	}()
	formatID := prepared.Format().ID
	authorization, authorized := p.authorizations[formatID]
	if !authorized {
		return provider.Result{}, &provider.Error{
			Kind:  provider.ErrorCapabilityChanged,
			Cause: fmt.Errorf("format %q has no upload authority under the capability manifest", formatID),
		}
	}
	response, err := p.client.Process(ctx, prepared, authorization)
	if err != nil {
		return provider.Result{}, classifyProcessorError(ctx, err, mistral.MetricsFromError(err))
	}
	return provider.Result{
		Document: response.Document, ReturnedModel: response.ReturnedModel,
		UnitsProcessed: response.UnitsProcessed, ProviderBytes: response.ProviderBytes,
		Metrics: provider.RequestMetrics(response.Metrics),
	}, nil
}

func closeSource(source provider.Source) {
	if source.Content != nil {
		_ = source.Content.Close()
	}
}

// classifyProcessorError reproduces docbank's mistral.Processor classification
// exactly: an interrupted context outranks every kind; the staging and
// response sentinels map to their neutral kinds; a failure with no provider
// request behind it (Requests == 0, such as a spool open error) is transient
// and retried; anything else is malformed output. The neutral and vendor
// metrics structs share one field set, so the conversion fails to compile if
// either drifts.
func classifyProcessorError(ctx context.Context, err error, metrics mistral.RequestMetrics) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return &provider.Error{Metrics: provider.RequestMetrics(metrics), Cause: errors.Join(ctxErr, err)}
	}
	kind := provider.ErrorMalformedOutput
	switch {
	case errors.Is(err, mistral.ErrSpoolCapacity):
		kind = provider.ErrorCapacity
	case errors.Is(err, mistral.ErrSpoolUnavailable):
		kind = provider.ErrorTransient
	case errors.Is(err, mistral.ErrInvalidSource):
		kind = provider.ErrorInvalidInput
	case errors.Is(err, mistral.ErrTransientResponse):
		kind = provider.ErrorTransient
	case errors.Is(err, mistral.ErrPermanentResponse):
		kind = provider.ErrorRejected
	case errors.Is(err, mistral.ErrResponseTooLarge):
		kind = provider.ErrorResponseTooLarge
	case errors.Is(err, mistral.ErrCapabilityContract):
		kind = provider.ErrorCapabilityChanged
	case metrics.Requests == 0:
		kind = provider.ErrorTransient
	}
	return &provider.Error{Kind: kind, Metrics: provider.RequestMetrics(metrics), Cause: err}
}
