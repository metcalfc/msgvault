package profilejudge

import (
	"strings"
	"unicode"

	"go.kenn.io/msgvault/internal/personenrichment"
	"go.kenn.io/msgvault/internal/store"
	"golang.org/x/text/unicode/norm"
	"golang.org/x/text/width"
)

// conflictOutcome is what code decides about a merge conflict before Jev.
type conflictOutcome int

const (
	// conflictAsk: free text or a URL that still differs after
	// normalization. Only this kind may be sent to Jev.
	conflictAsk conflictOutcome = iota
	// conflictEqual: equal after the field's normalization; code settles it.
	conflictEqual
	// conflictDifferent: a value with one canonical form (number, boolean,
	// date, timestamp, or a select option) that differs. Two such values are
	// different facts; the conflict stays with the user and is never sent.
	conflictDifferent
)

// classifyConflict applies the rule for the attribute's value and field
// type:
//
//   - integer, real, boolean, date, timestamp: stored in one canonical form,
//     compared exactly.
//   - select: option values are exact identifiers, compared exactly, never
//     folded.
//   - email: compared as personenrichment's email-v1 identifier (trimmed,
//     lower-cased), the form msgvault matches addresses by.
//   - phone: compared as E.164 (textimport.NormalizePhone).
//   - url: compared as personenrichment's canonical public URL (scheme and
//     host case, default port, dot segments, fragment, and tracking
//     parameters); the path and other parameters keep their case.
//   - text and textarea: free text, compared after foldText.
//
// A phone or URL pair that does not both normalize is compared as free
// text.
func classifyConflict(candidate store.MergeConflictCandidate) conflictOutcome {
	left, right := candidate.Survivor, candidate.Absorbed
	switch candidate.ValueType {
	case store.AttributeValueText:
	case store.AttributeValueInteger, store.AttributeValueReal, store.AttributeValueBoolean,
		store.AttributeValueDate, store.AttributeValueTimestamp:
		return exactOutcome(left, right)
	default:
		return conflictAsk
	}
	if candidate.FieldType == store.AttributeFieldSelect || candidate.FieldType == store.AttributeFieldMultiselect {
		return exactOutcome(left, right)
	}
	if class, ok := identifierFields[candidate.FieldType]; ok {
		// Both values read as identifiers of the field's kind: their
		// normal forms decide, and nothing more is folded.
		a, errA := personenrichment.NormalizeIdentifier(class, left)
		b, errB := personenrichment.NormalizeIdentifier(class, right)
		if errA == nil && errB == nil {
			if a.Value == b.Value {
				return conflictEqual
			}
			return conflictAsk
		}
	}
	if foldText(left) == foldText(right) {
		return conflictEqual
	}
	return conflictAsk
}

// identifierFields maps the text widgets that hold identifiers to the
// normalizer msgvault already matches that identifier by.
var identifierFields = map[store.AttributeFieldType]personenrichment.IdentifierClass{
	store.AttributeFieldEmail: personenrichment.IdentifierEmail,
	store.AttributeFieldPhone: personenrichment.IdentifierPhone,
	store.AttributeFieldURL:   personenrichment.IdentifierPublicProfileURL,
}

func exactOutcome(left, right string) conflictOutcome {
	if left == right {
		return conflictEqual
	}
	return conflictDifferent
}

// foldText is the comparison form of free text and names:
//
//   - canonical composition (NFC) and width folding, so a full-width or
//     half-width character reads as its ordinary form. Compatibility
//     folding is not applied, so superscripts, subscripts, and ligatures
//     stay themselves ("2⁵" is not "25");
//   - simple case folding, one character for one, so "ß" is not "ss";
//   - separating punctuation (period, comma, semicolon, colon, question and
//     exclamation marks, quotes and apostrophes, brackets, ellipsis, middle
//     dot, and dashes) read as a space, unless a number character is next
//     to it, so "1.000" and "1,000", "3.14" and "3:14", "(5)" and "5", and
//     "-5" and "5" stay different;
//   - runs of space collapsed.
//
// Letters, digits, accents, symbols, and other punctuation (%, #, &, @, /,
// *) are kept as they are.
func foldText(value string) string {
	runes := []rune(foldCase(width.Fold.String(norm.NFC.String(value))))
	var folded strings.Builder
	for i, r := range runes {
		if separatorPunct(r) && !nextToNumber(runes, i) {
			folded.WriteRune(' ')
			continue
		}
		folded.WriteRune(r)
	}
	return strings.Join(strings.Fields(folded.String()), " ")
}

// foldCase applies simple case folding rune by rune. Turkish dotted and
// dotless I have no simple folding and stay as they are.
func foldCase(value string) string {
	return strings.Map(func(r rune) rune {
		if r == 'ı' || r == 'İ' {
			return r
		}
		return unicode.ToLower(unicode.ToUpper(r))
	}, value)
}

func separatorPunct(r rune) bool {
	if unicode.In(r, unicode.Pd, unicode.Ps, unicode.Pe, unicode.Pi, unicode.Pf) {
		return true
	}
	return strings.ContainsRune(".,;:!?'\"`…·¿¡", r)
}

// nextToNumber reports whether a number character, superscripts included,
// is on either side of runes[i].
func nextToNumber(runes []rune, i int) bool {
	return (i > 0 && unicode.IsNumber(runes[i-1])) || (i+1 < len(runes) && unicode.IsNumber(runes[i+1]))
}

// mixedCase reports whether a name has both upper- and lower-case letters.
func mixedCase(name string) bool {
	return strings.ContainsFunc(name, unicode.IsUpper) && strings.ContainsFunc(name, unicode.IsLower)
}

// preferredSpelling picks a name's spelling among its case-only variants:
// the given one when it is mixed case, else the first mixed-case variant
// (so "John Smith" wins over "JOHN SMITH" or "john smith"), else the given
// one.
func preferredSpelling(name string, variants []string) string {
	if mixedCase(name) {
		return name
	}
	key := foldCase(strings.Join(strings.Fields(name), " "))
	for _, variant := range variants {
		if mixedCase(variant) && foldCase(strings.Join(strings.Fields(variant), " ")) == key {
			return variant
		}
	}
	return name
}

// keepAbsorbed reports which equal value survives: the user-declared one first,
// then the merge rule's choice, the survivor's.
func keepAbsorbed(candidate store.MergeConflictCandidate) bool {
	return candidate.AbsorbedSource.IsDeclared() && !candidate.SurvivorSource.IsDeclared()
}

// roleKey is a role's comparison form: organization and title, each folded
// as free text. The start date is left out: it does not change what a
// primary role shows.
func roleKey(role store.PrimaryRoleOption) string {
	return foldText(role.Organization) + "\x00" + foldText(role.Title)
}
