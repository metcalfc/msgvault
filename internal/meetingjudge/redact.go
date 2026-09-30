package meetingjudge

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

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
	// phonePartSeparator splits a digit run into numbers that cannot share
	// one: a slash between two numbers.
	phonePartSeparator = regexp.MustCompile(`\s*/\s*`)
)

// normalizedText is text rewritten so identifiers are easy to find, with
// the original byte range behind every normalized byte. Matching happens on
// the normalized text; redaction happens on the original, so characters
// outside an identifier are sent exactly as written.
type normalizedText struct {
	text  string
	start []int
	end   []int
}

// normalizeWithSpans percent-decodes printable escapes, applies NFKC per
// character, maps Unicode dashes and spaces to ASCII, and drops zero-width
// characters, recording where each output byte came from.
func normalizeWithSpans(original string) normalizedText {
	var builder strings.Builder
	var starts, ends []int
	emit := func(value string, from, to int) {
		builder.WriteString(value)
		for range len(value) {
			starts = append(starts, from)
			ends = append(ends, to)
		}
	}
	for offset := 0; offset < len(original); {
		if escape := percentEscape.FindString(original[offset:min(offset+3, len(original))]); escape != "" {
			if value, err := strconv.ParseUint(escape[1:], 16, 8); err == nil && value >= 0x20 && value <= 0x7e {
				emit(string(rune(value)), offset, offset+3)
				offset += 3
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(original[offset:])
		for _, folded := range norm.NFKC.String(string(r)) {
			if replacement, ok := asciiSubstitutes[folded]; ok {
				if replacement < 0 {
					continue
				}
				folded = replacement
			}
			emit(string(folded), offset, offset+size)
		}
		offset += size
	}
	return normalizedText{text: builder.String(), start: starts, end: ends}
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

// identifierKind says which placeholder replaces a span.
type identifierKind int

const (
	kindEmail identifierKind = iota
	kindPhone
)

// identifierSpan is a byte range of the normalized text holding one
// identifier.
type identifierSpan struct {
	start, end int
	kind       identifierKind
}

// findIdentifiers returns the address and phone number spans of normalized
// text. Each found span is blanked before the next pattern runs, so no text
// is claimed twice.
func findIdentifiers(text string) []identifierSpan {
	work := []byte(text)
	var spans []identifierSpan
	blank := func(start, end int) {
		for i := start; i < end; i++ {
			work[i] = 0
		}
	}
	for _, pass := range []struct {
		pattern *regexp.Regexp
		kind    identifierKind
	}{
		{mailtoPattern, kindEmail}, {telPattern, kindPhone},
		{emailPattern, kindEmail}, {obfuscatedEmailPattern, kindEmail},
	} {
		for _, match := range pass.pattern.FindAllIndex(work, -1) {
			spans = append(spans, identifierSpan{start: match[0], end: match[1], kind: pass.kind})
			blank(match[0], match[1])
		}
	}
	// Dates and dotted versions are shielded one match at a time on a copy,
	// so they never join a digit run and never hide a neighbour.
	shielded := append([]byte(nil), work...)
	for _, match := range protectedPattern.FindAllIndex(shielded, -1) {
		for i := match[0]; i < match[1]; i++ {
			shielded[i] = 0
		}
	}
	for _, match := range phoneCandidatePattern.FindAllIndex(shielded, -1) {
		spans = append(spans, phoneSpans(shielded, match[0], match[1])...)
	}
	return spans
}

// phoneSpans evaluates one digit run. It is split where two numbers meet (a
// slash); each part with 7 to 15 digits is a phone number, and a part with
// more digits is redacted too, failing closed rather than letting two
// adjacent numbers through as one long run.
func phoneSpans(text []byte, start, end int) []identifierSpan {
	run := string(text[start:end])
	var spans []identifierSpan
	partStart := 0
	parts := phonePartSeparator.FindAllStringIndex(run, -1)
	bounds := make([][2]int, 0, len(parts)+1)
	for _, separator := range parts {
		bounds = append(bounds, [2]int{partStart, separator[0]})
		partStart = separator[1]
	}
	bounds = append(bounds, [2]int{partStart, len(run)})
	for _, bound := range bounds {
		part := run[bound[0]:bound[1]]
		if countDigits(part) >= minPhoneDigits {
			spans = append(spans, identifierSpan{start: start + bound[0], end: start + bound[1], kind: kindPhone})
		}
	}
	if countDigits(run) > maxPhoneDigits && len(spans) == 0 {
		spans = append(spans, identifierSpan{start: start, end: end, kind: kindPhone})
	}
	return spans
}

// replaceSpans rewrites the original text, replacing the original bytes
// behind each normalized span with its replacement and keeping every other
// byte as written.
func replaceSpans(original string, normalized normalizedText, spans []identifierSpan, replace func(identifierKind) string) string {
	if len(spans) == 0 {
		return original
	}
	type originalSpan struct {
		start, end int
		kind       identifierKind
	}
	mapped := make([]originalSpan, 0, len(spans))
	for _, span := range spans {
		if span.end <= span.start {
			continue
		}
		mapped = append(mapped, originalSpan{
			start: normalized.start[span.start], end: normalized.end[span.end-1], kind: span.kind,
		})
	}
	slices.SortFunc(mapped, func(a, b originalSpan) int { return a.start - b.start })
	var builder strings.Builder
	cursor := 0
	for _, span := range mapped {
		if span.end <= cursor {
			continue
		}
		if span.start < cursor {
			span.start = cursor
		}
		builder.WriteString(original[cursor:span.start])
		builder.WriteString(replace(span.kind))
		cursor = span.end
	}
	builder.WriteString(original[cursor:])
	return builder.String()
}

// RedactText replaces email addresses and phone numbers inside free text
// (titles, action items) with placeholders, so no identifier leaves the
// machine inside a title a person typed. Everything else is sent as
// written.
func RedactText(text string) string {
	normalized := normalizeWithSpans(text)
	redacted := replaceSpans(text, normalized, findIdentifiers(normalized.text), func(kind identifierKind) string {
		if kind == kindPhone {
			return PhonePlaceholder
		}
		return EmailPlaceholder
	})
	return strings.TrimSpace(redacted)
}

// AttendeeLabel turns a stored display label into the label sent for the
// i-th (zero-based) attendee: an embedded "<address>" is dropped, a bare
// address becomes its local part, other addresses and phone numbers are
// removed, and an empty result becomes "attendee N".
func AttendeeLabel(raw string, i int) string {
	if label := IdentifierFreeLabel(raw); label != "" {
		return label
	}
	return "attendee " + strconv.Itoa(i+1)
}

// IdentifierFreeLabel turns a stored display label into one that carries no
// email address or phone number: an embedded "<address>" is dropped, a bare
// address becomes its local part, other addresses and phone numbers are
// removed, and a result without a letter is empty. Other features that send
// a person's or account's label use it too.
func IdentifierFreeLabel(raw string) string {
	label := raw
	normalized := normalizeWithSpans(label)
	trimmed := strings.TrimSpace(normalized.text)
	if match := emailPattern.FindStringIndex(trimmed); match != nil && match[0] == 0 && match[1] == len(trimmed) {
		// The whole label is an address: keep its local part.
		offset := strings.Index(normalized.text, trimmed)
		at := strings.IndexByte(trimmed, '@')
		label = raw[normalized.start[offset]:normalized.end[offset+at-1]]
		normalized = normalizeWithSpans(label)
	}
	var spans []identifierSpan
	for _, match := range angleAddressPattern.FindAllStringIndex(normalized.text, -1) {
		if len(findIdentifiers(normalized.text[match[0]+1:match[1]-1])) > 0 {
			spans = append(spans, identifierSpan{start: match[0], end: match[1], kind: kindEmail})
		}
	}
	spans = append(spans, findIdentifiers(normalized.text)...)
	label = replaceSpans(label, normalized, spans, func(identifierKind) string { return " " })
	label = strings.Trim(strings.Join(strings.Fields(label), " "), `"' ,;()`)
	if !hasLetter(label) {
		return ""
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
