package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCLIRunCommandAllowedPersonJudge pins what the daemon runs for
// `person judge`: its bounded flags and nothing else.
func TestCLIRunCommandAllowedPersonJudge(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	for _, args := range [][]string{
		{"person", "judge"},
		{"person", "judge", "--json"},
		{"person", "judge", "--limit=50"},
		{"person", "judge", "--limit=0", "--log-level=debug"},
	} {
		assert.True(cliRunCommandAllowed(args), args)
	}
	for _, args := range [][]string{
		{"person"},
		{"person", "judge", "extra"},
		{"person", "judge", "--limit=-1"},
		{"person", "judge", "--json=maybe"},
		{"person", "judge", "--force"},
	} {
		assert.False(cliRunCommandAllowed(args), args)
	}
}
