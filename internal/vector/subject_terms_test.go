package vector

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSubjectBoostTerms(t *testing.T) {
	tests := []struct {
		name  string
		terms []string
		want  []string
	}{
		{"lowercases and drops stopwords", []string{"The", "Budget", "for", "Q3"}, []string{"budget", "q3"}},
		{"keeps a phrase with a content word", []string{"plan for the offsite"}, []string{"plan for the offsite"}},
		{"drops a phrase of stopwords", []string{"to be or not to be"}, []string{}},
		{"drops punctuation and repeats", []string{"?", "budget", "BUDGET"}, []string{"budget"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SubjectBoostTerms(tt.terms))
		})
	}
}

func TestSubjectHasTerm(t *testing.T) {
	tests := []struct {
		subject string
		term    string
		want    bool
	}{
		{"q3 plan review", "plan", true},
		{"new plans attached", "plan", true},
		{"the approaches", "approach", true},
		{"an explanation", "plan", false},
		{"planning offsite", "plan", false},
		{"re: plan", "plan", true},
		{"budget-plan v2", "plan", true},
		{"quarterly plan review", "plan review", true},
		{"airplane plan", "plan", true},
		{"", "plan", false},
		{"plan", "", false},
	}
	for _, tt := range tests {
		assert.Equalf(t, tt.want, SubjectHasTerm(tt.subject, tt.term), "%q in %q", tt.term, tt.subject)
	}
}

func TestContentTerms(t *testing.T) {
	assert.Equal(t, []string{"Tacos", "itinerary"}, ContentTerms([]string{"the", "Tacos", "and", "itinerary", "?"}))
	assert.Empty(t, ContentTerms([]string{"of", "the"}))
}
