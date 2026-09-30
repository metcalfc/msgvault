package textutil

import (
	"strings"
	"unicode"
)

// StripLabelEmoji removes emoji and pictographs from a short human-facing
// label such as a person's display name, job title, company, or location.
// Importers call it before they store a label they derived from a source, so
// "🎉 Ana" and "Ana ✨" both become "Ana".
//
// It removes Extended_Pictographic characters, emoji presentation and
// skin-tone modifiers, zero-width joiners inside emoji sequences, variation
// selectors, keycap sequences such as 1️⃣, regional-indicator flags, and tag
// sequences. After a removal it collapses whitespace, drops separators and
// empty brackets left at the ends or doubled up ("Ana 🌴 | Design" keeps its
// separator, "Design | 🌴" becomes "Design"), and returns "" when no letter or
// digit remains. Callers treat "" as "no label" and fall back to their next
// best label.
//
// Letters and marks in every script, digits, '#', and text-style symbols
// such as ©, ®, and ™ are kept; one of those symbols is removed only when a
// variation selector asks for its emoji presentation. A label with nothing to
// remove is returned unchanged, including its original spacing.
func StripLabelEmoji(label string) string {
	runes := []rune(label)
	out := make([]rune, 0, len(runes))
	removed := false
	lastDropped := false
	gap := false
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		next := runeAt(runes, i+1)
		drop := false
		switch {
		case isKeycapBase(r) && (next == 0x20E3 || (isVariationSelector(next) && runeAt(runes, i+2) == 0x20E3)):
			if next != 0x20E3 {
				i++
			}
			i++
			drop = true
		case r == 0x20E3, isRegionalIndicator(r), isEmojiModifier(r), isTagCharacter(r):
			drop = true
		case isVariationSelector(r):
			// Invisible on its own; VS16 after a text-style symbol is handled
			// with the symbol below.
			drop = true
		case r == 0x200D:
			drop = lastDropped || isEmojiBase(next)
		case unicode.Is(extendedPictographic, r):
			drop = !isTextDefaultSymbol(r) || next == 0xFE0F
		}
		if drop {
			removed = true
			lastDropped = true
			gap = true
			continue
		}
		if gap && len(out) > 0 && needsJoiningSpace(out[len(out)-1], r) {
			out = append(out, ' ')
		}
		lastDropped = false
		gap = false
		out = append(out, r)
	}
	if !removed {
		return label
	}
	return tidyStrippedLabel(string(out))
}

func runeAt(runes []rune, i int) rune {
	if i < 0 || i >= len(runes) {
		return -1
	}
	return runes[i]
}

func isKeycapBase(r rune) bool {
	return r == '#' || r == '*' || (r >= '0' && r <= '9')
}

func isVariationSelector(r rune) bool { return r == 0xFE0E || r == 0xFE0F }

func isRegionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

func isEmojiModifier(r rune) bool { return r >= 0x1F3FB && r <= 0x1F3FF }

func isTagCharacter(r rune) bool { return r >= 0xE0020 && r <= 0xE007F }

func isEmojiBase(r rune) bool {
	return r >= 0 && (unicode.Is(extendedPictographic, r) || isRegionalIndicator(r) || isEmojiModifier(r))
}

// isTextDefaultSymbol reports a pictographic character that renders as an
// ordinary text symbol unless followed by VS16: ©, ®, ™, arrows, and similar
// typography below the Miscellaneous Symbols block, plus a few in later
// blocks. Characters with default emoji presentation (⌚, ⏳, ◾) are not here.
func isTextDefaultSymbol(r rune) bool {
	switch r {
	case 0x231A, 0x231B, 0x23E9, 0x23EA, 0x23EB, 0x23EC, 0x23F0, 0x23F3, 0x25FD, 0x25FE:
		return false
	case 0x2934, 0x2935, 0x2B05, 0x2B06, 0x2B07, 0x3030, 0x303D, 0x3297, 0x3299:
		return true
	}
	return r < 0x2600
}

// needsJoiningSpace keeps words apart when an emoji sat between them without
// spaces ("Ana❤️Lopez"). Scripts written without spaces stay joined.
func needsJoiningSpace(before, after rune) bool {
	if !isWordRune(before) || !isWordRune(after) {
		return false
	}
	return !isUnspacedScript(before) || !isUnspacedScript(after)
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r)
}

func isUnspacedScript(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Thai, unicode.Lao,
		unicode.Khmer, unicode.Myanmar)
}

// labelSeparators are the characters a label uses between parts. Stripping an
// emoji can leave one stranded at an end or next to another.
const labelSeparators = "|/\\-–—·•,;:~"

func isSeparatorToken(token string) bool {
	return token != "" && strings.Trim(token, labelSeparators) == ""
}

func isEmptyBracketToken(token string) bool {
	switch token {
	case "()", "[]", "{}", "<>", "（）", "【】":
		return true
	}
	return false
}

func tidyStrippedLabel(label string) string {
	fields := strings.Fields(label)
	// Rejoin brackets split by the removal, so "( )" reads as one empty token.
	joined := make([]string, 0, len(fields))
	for _, field := range fields {
		if n := len(joined); n > 0 && isEmptyBracketToken(joined[n-1]+field) {
			joined[n-1] += field
			continue
		}
		joined = append(joined, field)
	}
	tokens := make([]string, 0, len(joined))
	for _, token := range joined {
		if isEmptyBracketToken(token) {
			continue
		}
		if isSeparatorToken(token) && (len(tokens) == 0 || isSeparatorToken(tokens[len(tokens)-1])) {
			continue
		}
		tokens = append(tokens, token)
	}
	for len(tokens) > 0 && isSeparatorToken(tokens[len(tokens)-1]) {
		tokens = tokens[:len(tokens)-1]
	}
	result := strings.Join(tokens, " ")
	result = strings.TrimLeft(result, labelSeparators+" ")
	result = strings.TrimRight(result, labelSeparators+" ")
	if !strings.ContainsFunc(result, isWordRune) {
		return ""
	}
	return result
}

// extendedPictographic is the Unicode 15.1 Extended_Pictographic property
// from emoji-data.txt. It includes reserved code points in the emoji blocks so
// that emoji added by later Unicode versions are removed too.
var extendedPictographic = &unicode.RangeTable{
	R16: []unicode.Range16{
		{0x00A9, 0x00A9, 1}, {0x00AE, 0x00AE, 1}, {0x203C, 0x203C, 1}, {0x2049, 0x2049, 1},
		{0x2122, 0x2122, 1}, {0x2139, 0x2139, 1}, {0x2194, 0x2199, 1}, {0x21A9, 0x21AA, 1},
		{0x231A, 0x231B, 1}, {0x2328, 0x2328, 1}, {0x2388, 0x2388, 1}, {0x23CF, 0x23CF, 1},
		{0x23E9, 0x23F3, 1}, {0x23F8, 0x23FA, 1}, {0x24C2, 0x24C2, 1}, {0x25AA, 0x25AB, 1},
		{0x25B6, 0x25B6, 1}, {0x25C0, 0x25C0, 1}, {0x25FB, 0x25FE, 1}, {0x2600, 0x2605, 1},
		{0x2607, 0x2612, 1}, {0x2614, 0x2685, 1}, {0x2690, 0x2705, 1}, {0x2708, 0x2712, 1},
		{0x2714, 0x2714, 1}, {0x2716, 0x2716, 1}, {0x271D, 0x271D, 1}, {0x2721, 0x2721, 1},
		{0x2728, 0x2728, 1}, {0x2733, 0x2734, 1}, {0x2744, 0x2744, 1}, {0x2747, 0x2747, 1},
		{0x274C, 0x274C, 1}, {0x274E, 0x274E, 1}, {0x2753, 0x2755, 1}, {0x2757, 0x2757, 1},
		{0x2763, 0x2767, 1}, {0x2795, 0x2797, 1}, {0x27A1, 0x27A1, 1}, {0x27B0, 0x27B0, 1},
		{0x27BF, 0x27BF, 1}, {0x2934, 0x2935, 1}, {0x2B05, 0x2B07, 1}, {0x2B1B, 0x2B1C, 1},
		{0x2B50, 0x2B50, 1}, {0x2B55, 0x2B55, 1}, {0x3030, 0x3030, 1}, {0x303D, 0x303D, 1},
		{0x3297, 0x3297, 1}, {0x3299, 0x3299, 1},
	},
	R32: []unicode.Range32{
		{0x1F000, 0x1F0FF, 1}, {0x1F10D, 0x1F10F, 1}, {0x1F12F, 0x1F12F, 1}, {0x1F16C, 0x1F171, 1},
		{0x1F17E, 0x1F17F, 1}, {0x1F18E, 0x1F18E, 1}, {0x1F191, 0x1F19A, 1}, {0x1F1AD, 0x1F1E5, 1},
		{0x1F201, 0x1F20F, 1}, {0x1F21A, 0x1F21A, 1}, {0x1F22F, 0x1F22F, 1}, {0x1F232, 0x1F23A, 1},
		{0x1F23C, 0x1F23F, 1}, {0x1F249, 0x1F3FA, 1}, {0x1F400, 0x1F53D, 1}, {0x1F546, 0x1F64F, 1},
		{0x1F680, 0x1F6FF, 1}, {0x1F774, 0x1F77F, 1}, {0x1F7D5, 0x1F7FF, 1}, {0x1F80C, 0x1F80F, 1},
		{0x1F848, 0x1F84F, 1}, {0x1F85A, 0x1F85F, 1}, {0x1F888, 0x1F88F, 1}, {0x1F8AE, 0x1F8FF, 1},
		{0x1F90C, 0x1F93A, 1}, {0x1F93C, 0x1F945, 1}, {0x1F947, 0x1FAFF, 1}, {0x1FC00, 0x1FFFD, 1},
	},
	LatinOffset: 2,
}
