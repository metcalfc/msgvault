package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCLIRunCommandAllowedKinds pins what the daemon runs for `kinds`:
// build with its bounded flags and nothing else.
func TestCLIRunCommandAllowedKinds(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	for _, args := range [][]string{
		{"kinds", "build"},
		{"kinds", "build", "--json"},
		{"kinds", "build", "--min-messages=3", "--limit=50"},
		{"kinds", "build", "--limit=0", "--rules-only"},
		{"kinds", "build", "--rules-only=false", "--log-level=debug"},
	} {
		assert.True(cliRunCommandAllowed(args), args)
	}
	for _, args := range [][]string{
		{"kinds"},
		{"kinds", "reset"},
		{"kinds", "build", "extra"},
		{"kinds", "build", "--min-messages=0"},
		{"kinds", "build", "--limit=-1"},
		{"kinds", "build", "--rules-only=maybe"},
		{"kinds", "build", "--force"},
		{"kinds", "build", "--json", "--json"},
	} {
		assert.False(cliRunCommandAllowed(args), args)
	}
}
