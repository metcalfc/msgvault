package provider

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubProvider satisfies Provider with only a name; the registry never calls
// the other methods.
type stubProvider struct {
	Provider

	name string
}

func (s stubProvider) Name() string { return s.name }

func TestRegistryLooksUpRegisteredProvidersByName(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	registry, err := NewRegistry(stubProvider{name: "zeta"}, stubProvider{name: "alpha"})
	require.NoError(err)

	found, err := registry.Lookup("alpha")
	require.NoError(err)
	assert.Equal("alpha", found.Name())
	assert.Equal([]string{"alpha", "zeta"}, registry.Names())

	_, err = registry.Lookup("missing")
	require.ErrorIs(err, ErrUnknownProvider)
	assert.ErrorContains(err, `"missing"`)
}

func TestRegistryRejectsInvalidRegistrations(t *testing.T) {
	tests := []struct {
		name      string
		providers []Provider
		want      string
	}{
		{name: "nil", providers: []Provider{nil}, want: "nil provider"},
		{name: "empty name", providers: []Provider{stubProvider{}}, want: "empty name"},
		{name: "duplicate", providers: []Provider{stubProvider{name: "a"}, stubProvider{name: "a"}}, want: "already registered"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRegistry(test.providers...)
			require.ErrorContains(t, err, test.want)
			assert.Panics(t, func() { MustRegistry(test.providers...) })
		})
	}
}

func TestNilRegistryKnowsNoProvider(t *testing.T) {
	var registry *Registry
	_, err := registry.Lookup("any")
	require.ErrorIs(t, err, ErrUnknownProvider)
	assert.Nil(t, registry.Names())
}

func TestErrorHelpersReadThroughWrapping(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	cause := errors.New("synthetic cause")
	metrics := RequestMetrics{Requests: 2, Retries: 1, Latency: time.Second}
	wrapped := fmt.Errorf("outer: %w", errors.Join(&Error{Kind: ErrorCapacity, Metrics: metrics, Cause: cause}, errors.New("cleanup")))

	assert.Equal(ErrorCapacity, ErrorKindOf(wrapped))
	assert.Equal(metrics, MetricsFromError(wrapped))
	assert.True(IsRetryable(wrapped))
	require.ErrorIs(wrapped, cause)

	assert.Empty(ErrorKindOf(cause))
	assert.Equal(RequestMetrics{}, MetricsFromError(cause))
	assert.False(IsRetryable(cause))
	assert.False(IsRetryable(&Error{Kind: ErrorRejected}))
	assert.True(IsRetryable(&Error{Kind: ErrorTransient}))
}

func TestNewSourceValidatesMetadataWithoutReading(t *testing.T) {
	require := require.New(t)
	content := io.NopCloser(errReader{})
	_, err := NewSource(content, "text/csv; charset=utf-8", 1, "00")
	require.ErrorContains(err, "canonical")
	source, err := NewSource(content, "application/pdf", 1, validSHA256())
	require.NoError(err)
	assert.Equal(t, "application/pdf", source.MediaType)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("must not read") }

func validSHA256() string {
	digest := make([]byte, 32)
	return hex.EncodeToString(digest)
}

// stubProcessor proves the Processor contract is satisfiable by a test fake.
type stubProcessor struct {
	fingerprint string
}

func (stubProcessor) Process(context.Context, Source) (Result, error) { return Result{}, nil }

func (s stubProcessor) PolicyFingerprint() string { return s.fingerprint }

var _ Processor = stubProcessor{}
