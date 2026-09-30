package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCLIRunCommandAllowedMeetingsJudge pins what the daemon runs for
// `meetings`: judge with its bounded flags and nothing else.
func TestCLIRunCommandAllowedMeetingsJudge(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	for _, args := range [][]string{
		{"meetings", "judge"},
		{"meetings", "judge", "--json"},
		{"meetings", "judge", "--limit=50"},
		{"meetings", "judge", "--limit=0", "--log-level=debug"},
	} {
		assert.True(cliRunCommandAllowed(args), args)
	}
	for _, args := range [][]string{
		{"meetings"},
		{"meetings", "actions"},
		{"meetings", "judge", "extra"},
		{"meetings", "judge", "--limit=-1"},
		{"meetings", "judge", "--json=maybe"},
		{"meetings", "judge", "--force"},
	} {
		assert.False(cliRunCommandAllowed(args), args)
	}
}
