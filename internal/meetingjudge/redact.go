package meetingjudge

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Placeholders that replace identifiers inside text sent to Jev.
const (
	EmailPlaceholder = "[email]"
	PhonePlaceholder = "[phone]"
)

// minPhoneDigits is how many digits a number needs to read as a phone
// number rather than a time, a date, or a count.
const minPhoneDigits = 7

var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)+`)
	// phoneCandidatePattern finds digit runs with phone punctuation; only
	// runs with at least minPhoneDigits digits are treated as numbers.
	phoneCandidatePattern = regexp.MustCompile(`\+?\(?\d[\d\s().\-/]*\d`)
	angleAddressPattern   = regexp.MustCompile(`<[^<>]*>`)
	datePattern           = regexp.MustCompile(`\d{4}-\d{1,2}-\d{1,2}|\d{1,2}/\d{1,2}/\d{2,4}`)
)

// isPhoneNumber reports whether a digit run reads as a phone number: enough
// digits and not a calendar date.
func isPhoneNumber(match string) bool {
	return countDigits(match) >= minPhoneDigits && !datePattern.MatchString(match)
}

// RedactText replaces email addresses and phone numbers inside free text
// (titles, action items) with placeholders, so no identifier leaves the
// machine inside a title a person typed.
func RedactText(text string) string {
	text = emailPattern.ReplaceAllString(text, EmailPlaceholder)
	text = phoneCandidatePattern.ReplaceAllStringFunc(text, func(match string) string {
		if !isPhoneNumber(match) {
			return match
		}
		return PhonePlaceholder
	})
	return strings.Join(strings.Fields(text), " ")
}

// AttendeeLabel turns a stored display label into the label sent for the
// i-th (zero-based) attendee: an embedded "<address>" is dropped, a bare
// address becomes its local part, other addresses and phone numbers are
// removed, and an empty result becomes "attendee N".
func AttendeeLabel(raw string, i int) string {
	label := angleAddressPattern.ReplaceAllStringFunc(raw, func(match string) string {
		if strings.Contains(match, "@") || isPhoneNumber(match) {
			return " "
		}
		return match
	})
	label = strings.Trim(strings.Join(strings.Fields(label), " "), `"' ,;`)
	if emailPattern.FindString(label) == label && label != "" {
		local, _, _ := strings.Cut(label, "@")
		label = local
	}
	label = emailPattern.ReplaceAllString(label, " ")
	label = phoneCandidatePattern.ReplaceAllStringFunc(label, func(match string) string {
		if !isPhoneNumber(match) {
			return match
		}
		return " "
	})
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
