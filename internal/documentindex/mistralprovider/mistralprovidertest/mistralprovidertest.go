// Package mistralprovidertest builds synthetic Mistral capability evidence and
// documents for tests that exercise the provider-neutral seam.
//
// Synthetic manifests are fabricated inputs, not observations from an
// authenticated probe, and must never serve as production upload authority.
package mistralprovidertest

import (
	"strings"

	"go.kenn.io/docbank/document/mistral"
	"go.kenn.io/docbank/document/mistral/mistraltest"
	"go.kenn.io/msgvault/internal/documentindex/mistralprovider"
	"go.kenn.io/msgvault/internal/documentindex/provider"
)

// Mutation reshapes synthetic evidence before it is wrapped.
type Mutation func(*mistral.CapabilityManifest)

// Manifest returns complete, validated evidence in which PDF has
// provider-request upload authority and every other format is unbounded.
// Fixture digests are zeroed so fingerprints stay stable across docbank
// fixture changes.
func Manifest(policy provider.Policy, mutations ...Mutation) (provider.Manifest, error) {
	manifest, err := UnvalidatedManifest(policy, mutations...)
	if err != nil {
		return nil, err
	}
	if err := Validate(manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

// UnvalidatedManifest is Manifest without the final validation, for evidence
// that is expected to fail it. Validate reports the validation error.
func UnvalidatedManifest(policy provider.Policy, mutations ...Mutation) (provider.Manifest, error) {
	vendorPolicy, err := mistralprovider.VendorPolicy(policy)
	if err != nil {
		return nil, err
	}
	manifest, err := mistraltest.SyntheticManifest(vendorPolicy, true)
	if err != nil {
		return nil, err //nolint:wrapcheck // synthetic fixture errors are test setup failures
	}
	for index := range manifest.Results {
		manifest.Results[index].FixtureDigest = strings.Repeat("0", 16)
	}
	for _, mutate := range mutations {
		mutate(&manifest)
	}
	return mistralprovider.NewManifest(manifest), nil
}

// Validate runs the vendor's complete-manifest validation on wrapped evidence.
func Validate(manifest provider.Manifest) error {
	vendorManifest, err := mistralprovider.VendorManifest(manifest)
	if err != nil {
		return err
	}
	return vendorManifest.ValidateComplete() //nolint:wrapcheck // tests assert the vendor validation text
}

// WithLocalExactPPTX grants PPTX local-exact upload authority, mirroring the
// row shape an authenticated probe records.
func WithLocalExactPPTX() Mutation {
	return func(manifest *mistral.CapabilityManifest) {
		for index := range manifest.Results {
			result := &manifest.Results[index]
			if result.FormatID == "pptx" {
				result.ReasonCode = ""
				result.UnitBoundMethod = mistral.UnitBoundLocalExact
				result.LocalUnits = result.UnitsProcessed
			}
		}
	}
}

// WithLegacyUnboundedPPTX marks PPTX as passed without any unit bound, the
// shape older manifests recorded. The result fails validation.
func WithLegacyUnboundedPPTX() Mutation {
	return func(manifest *mistral.CapabilityManifest) {
		for index := range manifest.Results {
			row := &manifest.Results[index]
			if row.FormatID == "pptx" {
				row.UnitBoundMethod = mistral.UnitBoundNone
				row.ReasonCode = ""
				row.FixtureUnits, row.BoundRequestedUnits, row.BoundUnitsProcessed, row.LocalUnits = 0, 0, 0, 0
			}
		}
	}
}

// WithObservedOn sets the observation date (YYYY-MM-DD).
func WithObservedOn(date string) Mutation {
	return func(manifest *mistral.CapabilityManifest) { manifest.ObservedOn = date }
}

// WithPDFFixtureDigest replaces the PDF fixture digest so evidence differs
// without changing authority.
func WithPDFFixtureDigest(digest string) Mutation {
	return func(manifest *mistral.CapabilityManifest) {
		for index := range manifest.Results {
			if manifest.Results[index].FormatID == "pdf" {
				manifest.Results[index].FixtureDigest = digest
				return
			}
		}
	}
}

// MinimalPDF returns deterministic one-page PDF bytes.
func MinimalPDF(label string) []byte { return mistraltest.MinimalPDF(label) }
