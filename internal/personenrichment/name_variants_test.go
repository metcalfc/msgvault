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
		{name: "plain name has no variants", in: "Susie Singh", want: nil},
		{name: "middle initial dropped", in: "Susie Q. Singh", want: []string{"susie singh"}},
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
	assert.True(nameIdentifierMatch("Susie Singh", "susie singh"))
	assert.True(nameIdentifierMatch("Susie Q. Singh", "Susie Singh"), "the returned name may drop the middle initial")
	assert.True(nameIdentifierMatch("Rivera, Alex", "Alex Rivera"))
	assert.True(nameIdentifierMatch("Alex Rivera Jr.", "Alex Rivera"))
	assert.False(nameIdentifierMatch("Susie Singh", "Susie S."), "an abbreviated surname is not a code variant")
	assert.False(nameIdentifierMatch("Susie Singh", "Susie Q. Singh"), "variants never run from returned to requested")
	assert.False(nameIdentifierMatch("Susie Singh", "Susan Singh"))
	assert.False(nameIdentifierMatch("", "Susie Singh"))
	assert.False(nameIdentifierMatch("Susie Singh", ""))
}
