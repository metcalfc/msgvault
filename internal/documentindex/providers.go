package documentindex

import (
	"fmt"
	"strings"

	"go.kenn.io/msgvault/internal/documentindex/mistralprovider"
	"go.kenn.io/msgvault/internal/documentindex/provider"
)

const (
	// ProviderMistral is the default and currently only document provider.
	ProviderMistral = mistralprovider.Name
	// ModelMistralOCR is the pinned model the Mistral provider serves.
	ModelMistralOCR = mistralprovider.DefaultModel
	// RegionMistralEU is the only region the Mistral provider serves.
	RegionMistralEU = mistralprovider.RegionEU

	defaultProviderName = ProviderMistral
)

// providers lists every document extraction backend msgvault can configure.
// Adding a provider means registering its adapter here.
var providers = provider.MustRegistry(mistralprovider.New())

// LookupProvider resolves a configured provider name to its adapter.
func LookupProvider(name string) (provider.Provider, error) {
	return providers.Lookup(name)
}

// ProviderNames lists the registered provider names in sorted order.
func ProviderNames() []string {
	return providers.Names()
}

func defaultProvider() provider.Provider {
	registered, err := LookupProvider(defaultProviderName)
	if err != nil {
		panic(fmt.Sprintf("default document provider is not registered: %v", err))
	}
	return registered
}

// providerDefaults returns the defaults for name, falling back to the default
// provider so an unknown name still decodes and then fails Validate.
func providerDefaults(name string) provider.Defaults {
	registered, err := LookupProvider(name)
	if err != nil {
		registered = defaultProvider()
	}
	return registered.Defaults()
}

func supportedProviderList() string {
	names := ProviderNames()
	quoted := make([]string, len(names))
	for index, name := range names {
		quoted[index] = fmt.Sprintf("%q", name)
	}
	return strings.Join(quoted, " or ")
}
