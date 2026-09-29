package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCLIRunCommandAllowedJev pins the Jev consent boundary the daemon will
// run on a client's behalf: status, consent for one named feature, and
// revoke for one feature or --all. Anything else, including free-text
// feature names, is refused before it reaches a subprocess.
func TestCLIRunCommandAllowedJev(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	for _, args := range [][]string{
		{"jev", "status"},
		{"jev", "status", "--json"},
		{"jev", "consent", "enrichment_identity"},
		{"jev", "consent", "enrichment_identity", "--yes"},
		{"jev", "consent", "enrichment_identity", "--yes", "--json"},
		{"jev", "revoke", "enrichment_identity"},
		{"jev", "revoke", "--all"},
		{"jev", "revoke", "--all", "--json"},
		{"jev", "revoke", "--all=true"},
		{"jev", "revoke", "--all=1"},
		{"jev", "revoke", "enrichment_identity", "--all=false"},
		{"jev", "revoke", "enrichment_identity", "--all=0", "--json=false"},
		{"jev", "status", "--log-level=debug"},
	} {
		assert.True(cliRunCommandAllowed(args), args)
	}
	for _, args := range [][]string{
		{"jev"},
		{"jev", "judge"},
		{"jev", "status", "extra"},
		{"jev", "consent"},
		{"jev", "consent", "Enrichment Identity", "--yes"},
		{"jev", "consent", "enrichment_identity", "--force"},
		{"jev", "revoke"},
		{"jev", "revoke", "enrichment_identity", "--all"},
		{"jev", "revoke", "--all=false"},
		{"jev", "revoke", "enrichment_identity", "--all=true"},
		{"jev", "revoke", "enrichment_identity", "--all=maybe"},
		{"jev", "status", "--json=yes please"},
		{"jev", "consent", "enrichment_identity", "--yes=sure"},
		{"jev", "revoke", "a", "b"},
		{"jev", "__internal"},
	} {
		assert.False(cliRunCommandAllowed(args), args)
	}
}
