package textutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.kenn.io/msgvault/internal/textutil"
)

func TestStripLabelEmoji(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"leading emoji", "🎉 Ana", "Ana"},
		{"trailing sparkle", "Ana ✨", "Ana"},
		{"emoji between words keeps separator", "Ana 🌴 | Design", "Ana | Design"},
		{"separator stranded at end", "Design | 🌴", "Design"},
		{"separator stranded at start", "🌴 · Ana Pérez", "Ana Pérez"},
		{"doubled separator collapses", "Ana | 🚀 | Design", "Ana | Design"},
		{"emoji joined to words", "Ana❤️Lopez", "Ana Lopez"},
		{"ZWJ family", "Ana 👨‍👩‍👧‍👦 Lopez", "Ana Lopez"},
		{"ZWJ profession", "Ana 👩🏽‍💻", "Ana"},
		{"skin tone", "Ana 👋🏿", "Ana"},
		{"stray skin tone", "Ana 🏽", "Ana"},
		{"regional indicator flag", "Ana Lopez 🇲🇽", "Ana Lopez"},
		{"tag sequence flag", "Ana 🏴󠁧󠁢󠁳󠁣󠁴󠁿", "Ana"},
		{"keycap", "Ana 1️⃣", "Ana"},
		{"keycap hash without VS16", "Ana #⃣", "Ana"},
		{"text symbol with emoji presentation", "Ana ❤️", "Ana"},
		{"copyright as emoji", "©️ Ana", "Ana"},
		{"empty brackets", "Ana (🌴)", "Ana"},
		{"spaced brackets", "[ 🎉 ] Ana", "Ana"},
		{"black star", "★ Ana ★", "Ana"},
		{"emoji only", "🎉🎉", ""},
		{"emoji and separators only", "🎉 | 🎉", ""},
		{"ZWJ family only", "👨‍👩‍👧", ""},
		{"CJK unchanged", "山田太郎", "山田太郎"},
		{"CJK with emoji stays unspaced", "山田🌸太郎", "山田太郎"},
		{"Cyrillic unchanged", "Анна Петрова", "Анна Петрова"},
		{"accented unchanged", "José Ñúñez-Zoë", "José Ñúñez-Zoë"},
		{"apostrophe unchanged", "Sam O'Neil", "Sam O'Neil"},
		{"hyphen unchanged", "Anne-Marie Dupont", "Anne-Marie Dupont"},
		{"digits and hash unchanged", "Team #1 (2024)", "Team #1 (2024)"},
		{"registered and trademark kept", "Example® Labs™ ©", "Example® Labs™ ©"},
		{"arrow kept", "Sales ↔ Ops", "Sales ↔ Ops"},
		{"Devanagari ZWJ kept", "क्‍ष", "क्‍ष"},
		{"no emoji keeps spacing", "  Ana   Lopez ", "  Ana   Lopez "},
		{"C++ kept after strip", "C++ 🚀", "C++"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, textutil.StripLabelEmoji(tt.input))
		})
	}
}
