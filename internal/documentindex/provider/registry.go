package provider

import (
	"errors"
	"fmt"
	"slices"
)

// Registry resolves the configured provider name to its adapter.
type Registry struct {
	providers map[string]Provider
	names     []string
}

// NewRegistry builds a registry from providers whose names must be unique
// and nonempty.
func NewRegistry(providers ...Provider) (*Registry, error) {
	registry := &Registry{providers: make(map[string]Provider, len(providers))}
	for _, candidate := range providers {
		if candidate == nil {
			return nil, errors.New("register document provider: nil provider")
		}
		name := candidate.Name()
		if name == "" {
			return nil, errors.New("register document provider: empty name")
		}
		if _, exists := registry.providers[name]; exists {
			return nil, fmt.Errorf("register document provider %q: already registered", name)
		}
		registry.providers[name] = candidate
		registry.names = append(registry.names, name)
	}
	slices.Sort(registry.names)
	return registry, nil
}

// MustRegistry is NewRegistry for package-level wiring of fixed providers.
func MustRegistry(providers ...Provider) *Registry {
	registry, err := NewRegistry(providers...)
	if err != nil {
		panic(err)
	}
	return registry
}

// Lookup returns the provider registered under name.
func (r *Registry) Lookup(name string) (Provider, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, name)
	}
	candidate, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, name)
	}
	return candidate, nil
}

// Names lists registered provider names in sorted order.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	return slices.Clone(r.names)
}
