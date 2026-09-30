package meetingjudge

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Placeholders that replace identifiers inside text sent to Jev.
const (
	EmailPlaceholder = "[email]"
	PhonePlaceholder = "[phone]"
)

// Phone numbers have 7 to 15 digits (E.164 allows at most 15). Fewer read as
// times, counts, or room numbers; more as account or order numbers.
const (
	minPhoneDigits = 7
	maxPhoneDigits = 15
)

// Privacy wins over context: an ambiguous digit run such as a meeting ID
// with a phone number's shape is redacted. Dates and dotted version strings
// are not.
var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)+`)
	// obfuscatedEmailPattern finds "name at domain dot com", "name [at]
	// domain [dot] com", and "name(at)domain". A spelled-out " at " needs a
	// dotted domain; a bracketed (at) does not.
	obfuscatedEmailPattern = regexp.MustCompile(`(?i)[A-Za-z0-9._%+\-]+\s*[\[({]\s*at\s*[\])}]\s*` +
		`[A-Za-z0-9\-]+(?:\s*(?:[\[({]\s*dot\s*[\])}]|\.|\s+dot\s+)\s*[A-Za-z0-9\-]+)*` +
		`|[A-Za-z0-9._%+\-]+\s+at\s+[A-Za-z0-9\-]+` +
		`(?:\s*(?:[\[({]\s*dot\s*[\])}]|\.|\s+dot\s+)\s*[A-Za-z0-9\-]+)*` +
		`\s*(?:[\[({]\s*dot\s*[\])}]|\.|\s+dot\s+)\s*[A-Za-z]{2,}\b`)
	mailtoPattern = regexp.MustCompile(`(?i)\bmailto:[^\s<>"']+`)
	telPattern    = regexp.MustCompile(`(?i)\b(?:tel|callto|sms):[^\s<>"']+`)
	// phoneCandidatePattern finds digit runs with phone punctuation; only
	// runs with 7 to 15 digits are treated as numbers.
	phoneCandidatePattern = regexp.MustCompile(`\+?\(?\d[\d\s().\-/]*\d`)
	angleAddressPattern   = regexp.MustCompile(`<[^<>]*>`)
	// protectedPattern finds dates and dotted version strings (every group
	// at most three digits, such as 1.2.3 or 123.456.789); each match is
	// shielded on its own so a date next to a phone number shields nothing
	// else.
	protectedPattern = regexp.MustCompile(`\b\d{4}-\d{1,2}-\d{1,2}\b|\b\d{1,2}/\d{1,2}/\d{2,4}\b|` +
		`\b\d{1,3}(?:\.\d{1,3}){2,}\b`)
	percentEscape = regexp.MustCompile(`%[0-9A-Fa-f]{2}`)
)

// normalizeForRedaction undoes the encodings that would hide an identifier
// from the patterns: percent-escapes, compatibility forms (NFKC), and
// Unicode dashes and spaces, which become ASCII.
func normalizeForRedaction(text string) string {
	text = percentEscape.ReplaceAllStringFunc(text, func(escape string) string {
		value, err := strconv.ParseUint(escape[1:], 16, 8)
		if err != nil || value < 0x20 || value > 0x7e {
			return escape
		}
		return string(rune(value))
	})
	text = norm.NFKC.String(text)
	return strings.Map(func(r rune) rune {
		if replacement, ok := asciiSubstitutes[r]; ok {
			return replacement
		}
		return r
	}, text)
}

// asciiSubstitutes maps Unicode dashes to '-', Unicode spaces to ' ', and
// zero-width characters to nothing (-1). Code points are numeric so the
// source stays ASCII.
var asciiSubstitutes = func() map[rune]rune {
	substitutes := map[rune]rune{}
	for _, dash := range []rune{0x2010, 0x2011, 0x2012, 0x2013, 0x2014, 0x2015, 0x2212, 0xFE58, 0xFE63, 0xFF0D} {
		substitutes[dash] = '-'
	}
	for _, space := range []rune{0x00A0, 0x2002, 0x2003, 0x2004, 0x2005, 0x2006, 0x2007, 0x2008, 0x2009,
		0x200A, 0x202F, 0x205F, 0x3000} {
		substitutes[space] = ' '
	}
	for _, invisible := range []rune{0x200B, 0x200C, 0x200D, 0x2060, 0xFEFF} {
		substitutes[invisible] = -1
	}
	return substitutes
}()

// redactIdentifiers replaces addresses and phone numbers in normalized text
// with the given replacements.
func redactIdentifiers(text, email, phone string) string {
	text = mailtoPattern.ReplaceAllString(text, email)
	text = telPattern.ReplaceAllString(text, phone)
	text = emailPattern.ReplaceAllString(text, email)
	text = obfuscatedEmailPattern.ReplaceAllString(text, email)
	return redactPhones(text, phone)
}

// redactPhones replaces 7-15 digit runs, after shielding each date and
// dotted version string individually.
func redactPhones(text, phone string) string {
	var shielded []string
	masked := protectedPattern.ReplaceAllStringFunc(text, func(match string) string {
		shielded = append(shielded, match)
		// A letter token keeps the shielded text out of digit runs.
		return "\x00p" + strconv.Itoa(len(shielded)-1) + "q\x00"
	})
	masked = phoneCandidatePattern.ReplaceAllStringFunc(masked, func(match string) string {
		digits := countDigits(match)
		if digits < minPhoneDigits || digits > maxPhoneDigits {
			return match
		}
		return phone
	})
	for i, original := range shielded {
		masked = strings.Replace(masked, "\x00p"+strconv.Itoa(i)+"q\x00", original, 1)
	}
	return masked
}

// RedactText replaces email addresses and phone numbers inside free text
// (titles, action items) with placeholders, so no identifier leaves the
// machine inside a title a person typed.
func RedactText(text string) string {
	text = redactIdentifiers(normalizeForRedaction(text), EmailPlaceholder, PhonePlaceholder)
	return strings.Join(strings.Fields(text), " ")
}

// AttendeeLabel turns a stored display label into the label sent for the
// i-th (zero-based) attendee: an embedded "<address>" is dropped, a bare
// address becomes its local part, other addresses and phone numbers are
// removed, and an empty result becomes "attendee N".
func AttendeeLabel(raw string, i int) string {
	label := normalizeForRedaction(raw)
	label = angleAddressPattern.ReplaceAllStringFunc(label, func(match string) string {
		inner := strings.Trim(match, "<>")
		if redactIdentifiers(inner, "\x01", "\x01") != inner {
			return " "
		}
		return match
	})
	label = strings.Trim(strings.Join(strings.Fields(label), " "), `"' ,;`)
	if label != "" && emailPattern.FindString(label) == label {
		local, _, _ := strings.Cut(label, "@")
		label = local
	}
	label = redactIdentifiers(label, " ", " ")
	label = strings.Trim(strings.Join(strings.Fields(label), " "), `"' ,;()`)
	if !hasLetter(label) {
		return "attendee " + strconv.Itoa(i+1)
	}
	return label
}

func countDigits(value string) int {
	count := 0
	for _, r := range value {
		if unicode.IsDigit(r) {
			count++
		}
	}
	return count
}

func hasLetter(value string) bool {
	for _, r := range value {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}
