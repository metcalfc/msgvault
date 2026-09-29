package personenrichment

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNameVariantsAreDeterministicAndBounded(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{name: "plain name has no variants", in: "Priya Ramanathan", want: nil},
		{name: "middle initial dropped", in: "Priya Q. Ramanathan", want: []string{"priya ramanathan"}},
		{name: "middle name dropped", in: "Alex Morgan Rivera", want: []string{"alex rivera"}},
		{name: "suffix dropped then middle", in: "Alex Morgan Rivera Jr.", want: []string{"alex morgan rivera", "alex rivera"}},
		{name: "credential suffix after comma", in: "Dana Example, PhD", want: []string{"dana example"}},
		{name: "last comma first", in: "Rivera, Alex", want: []string{"alex rivera"}},
		{name: "last comma first middle", in: "Rivera, Alex M.", want: []string{"alex m. rivera", "alex rivera"}},
		{name: "empty", in: "   ", want: nil},
		{name: "single token", in: "Cher", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, NameVariants(tc.in))
		})
	}
}

func TestNameIdentifierMatchAcceptsOnlyRequestVariants(t *testing.T) {
	assert := assert.New(t)
	assert.True(nameIdentifierMatch("Priya Ramanathan", "priya ramanathan"))
	assert.True(nameIdentifierMatch("Priya Q. Ramanathan", "Priya Ramanathan"), "the returned name may drop the middle initial")
	assert.True(nameIdentifierMatch("Rivera, Alex", "Alex Rivera"))
	assert.True(nameIdentifierMatch("Alex Rivera Jr.", "Alex Rivera"))
	assert.False(nameIdentifierMatch("Priya Ramanathan", "Priya R."), "an abbreviated surname is not a code variant")
	assert.False(nameIdentifierMatch("Priya Ramanathan", "Priya Q. Ramanathan"), "variants never run from returned to requested")
	assert.False(nameIdentifierMatch("Priya Ramanathan", "Priyanka Ramanathan"))
	assert.False(nameIdentifierMatch("", "Priya Ramanathan"))
	assert.False(nameIdentifierMatch("Priya Ramanathan", ""))
}
