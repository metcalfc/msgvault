package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/msgvault/internal/jev"
)

// A feature whose state carries message or note text must say so: without a
// BodyNotice the disclosure ends with "no message bodies", which would be false.
func TestJevFeaturesThatSendTextCarryABodyNotice(t *testing.T) {
	textMarkers := []string{"body", "excerpt", "description"}
	for _, spec := range jevFeatureSpecs() {
		sendsText := false
		for _, field := range spec.StateFields {
			lower := strings.ToLower(field)
			for _, marker := range textMarkers {
				if strings.Contains(lower, marker) {
					sendsText = true
				}
			}
		}
		if !sendsText {
			continue
		}
		assert.NotEmpty(t, spec.BodyNotice, "%s sends message text but has no BodyNotice", spec.Name)

		policy, err := spec.Policy(jev.Config{Endpoint: jev.DefaultEndpoint, Model: jev.DefaultModel})
		require.NoError(t, err, spec.Name)
		var out bytes.Buffer
		printJevDisclosure(&out, policy, jev.FeatureConfig{Enabled: true})
		assert.NotContains(t, out.String(), "no message bodies", spec.Name)
	}
}
