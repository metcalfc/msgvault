package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCLIRunCommandAllowedSuggestCleanup pins what the daemon runs for
// `suggest-cleanup`: its bounded flags and nothing else.
func TestCLIRunCommandAllowedSuggestCleanup(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	for _, args := range [][]string{
		{"suggest-cleanup"},
		{"suggest-cleanup", "--json"},
		{"suggest-cleanup", "--limit=20", "--show=5"},
		{"suggest-cleanup", "--list-only", "--min-score=0.5"},
		{"suggest-cleanup", "--rejudge=false", "--log-level=debug"},
	} {
		assert.True(cliRunCommandAllowed(args), args)
	}
	for _, args := range [][]string{
		{"suggest-cleanup", "extra"},
		{"suggest-cleanup", "--limit=0"},
		{"suggest-cleanup", "--show=-1"},
		{"suggest-cleanup", "--min-score=1.5"},
		{"suggest-cleanup", "--stage"},
		{"suggest-cleanup", "--json", "--json"},
	} {
		assert.False(cliRunCommandAllowed(args), args)
	}
}
