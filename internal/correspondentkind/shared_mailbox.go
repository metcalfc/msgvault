package correspondentkind

import (
	"slices"
	"strings"
	"unicode"
)

// RoleLocalParts are mailbox names that belong to a function rather than a
// person. An address whose local part is one of them is treated as a shared
// mailbox candidate.
var RoleLocalParts = []string{
	"support", "help", "info", "hello", "team", "contact", "sales", "billing",
	"noreply", "no-reply", "notifications", "admin", "office", "service",
	"customercare", "care", "feedback", "orders", "receipts", "accounts",
}

// SharedMailboxReason says why an address looks shared.
type SharedMailboxReason string

const (
	// ReasonRoleAddress: the local part names a role, such as support@.
	ReasonRoleAddress SharedMailboxReason = "role_address"
	// ReasonSeveralNames: two or more different people's names were seen
	// on the address.
	ReasonSeveralNames SharedMailboxReason = "several_names"
)

// SharedMailboxSignal is the verdict for one address.
type SharedMailboxSignal struct {
	Address string                `json:"address"`
	Reasons []SharedMailboxReason `json:"reasons" enum:"role_address,several_names"`
	// Names are the distinct people's names seen on the address, as first
	// written, when the several_names reason fired.
	Names []string `json:"names,omitempty"`
}

// Fires reports whether the address looks like a shared mailbox.
func (s SharedMailboxSignal) Fires() bool {
	return len(s.Reasons) > 0
}

// DetectSharedMailbox decides whether one email address looks like a
// mailbox several people write from. It fires when the local part is a role
// name (support@, billing@, no-reply@, ...) or when one group of display
// names seen on the address, after dropping trivial variants, names two or
// more different people. Groups are judged separately, so names from
// different sources (for example, the names messages carried and the names
// of contact cards that list the address) are never compared with each
// other: a contact card may call someone by a nickname the messages never
// use. Trivial variants are case, surrounding quotes, a
// trailing "via X" or parenthetical, name order ("Last, First"), and
// initials or short forms of a longer name ("Sam R." and "Samuel Rivera").
// Names that
// only restate the address or its role are not people's names.
func DetectSharedMailbox(address string, nameGroups ...[]string) SharedMailboxSignal {
	address = strings.TrimSpace(address)
	signal := SharedMailboxSignal{Address: address, Reasons: []SharedMailboxReason{}}
	if IsRoleAddress(address) {
		signal.Reasons = append(signal.Reasons, ReasonRoleAddress)
	}
	for _, displayNames := range nameGroups {
		if names := DistinctPersonNames(address, displayNames); len(names) >= 2 {
			signal.Reasons = append(signal.Reasons, ReasonSeveralNames)
			signal.Names = names
			break
		}
	}
	return signal
}

// IsRoleAddress reports whether an email address's local part, ignoring a
// "+tag" and case, is one of RoleLocalParts.
func IsRoleAddress(address string) bool {
	local := emailLocalPart(address)
	if local == "" {
		return false
	}
	return slices.Contains(RoleLocalParts, local)
}

func emailLocalPart(address string) string {
	address = strings.ToLower(strings.TrimSpace(address))
	at := strings.LastIndex(address, "@")
	if at <= 0 {
		return ""
	}
	local := address[:at]
	if plus := strings.Index(local, "+"); plus > 0 {
		local = local[:plus]
	}
	return local
}

type personName struct {
	display string
	tokens  []string
}

// DistinctPersonNames returns one display form per different person named
// in displayNames, in first-seen order. See DetectSharedMailbox for the
// variants treated as the same person.
func DistinctPersonNames(address string, displayNames []string) []string {
	local := emailLocalPart(address)
	groups := []personName{}
	for _, raw := range displayNames {
		display, tokens := personNameTokens(raw, local)
		if len(tokens) == 0 {
			continue
		}
		merged := false
		for i := range groups {
			if sameNamedPerson(groups[i].tokens, tokens) {
				// Keep the fuller spelling as the group's tokens so a
				// later name is compared against the most specific one.
				if len(tokens) > len(groups[i].tokens) {
					groups[i].tokens = tokens
				}
				merged = true
				break
			}
		}
		if !merged {
			groups = append(groups, personName{display: display, tokens: tokens})
		}
	}
	names := make([]string, 0, len(groups))
	for _, group := range groups {
		names = append(names, group.display)
	}
	return names
}

// personNameTokens normalizes one display name to sorted lowercase tokens
// and the trimmed display form. It returns no tokens for a value that is not
// a person's name: blank, an address, or only the mailbox or role words.
func personNameTokens(raw, local string) (string, []string) {
	display := strings.TrimSpace(raw)
	display = strings.Trim(display, "\"'`“”‘’ ")
	lower := strings.ToLower(display)
	if index := strings.Index(lower, " via "); index > 0 {
		display = strings.TrimSpace(display[:index])
	}
	display = stripBracketed(display)
	display = strings.Trim(display, "\"'`“”‘’ ")
	if display == "" || strings.Contains(display, "@") {
		return "", nil
	}
	fields := strings.FieldsFunc(strings.ToLower(display), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	})
	tokens := make([]string, 0, len(fields))
	onlyRoles := true
	for _, field := range fields {
		field = strings.Trim(field, "'")
		if field == "" {
			continue
		}
		tokens = append(tokens, field)
		if field != local && !slices.Contains(RoleLocalParts, field) {
			onlyRoles = false
		}
	}
	if len(tokens) == 0 || onlyRoles {
		return "", nil
	}
	slices.Sort(tokens)
	return display, slices.Compact(tokens)
}

func stripBracketed(value string) string {
	var builder strings.Builder
	depth := 0
	for _, r := range value {
		switch r {
		case '(', '[', '<':
			depth++
			continue
		case ')', ']', '>':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth == 0 {
			builder.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

// sameNamedPerson reports whether every token of the shorter name matches a
// distinct token of the longer one. Tokens match when one is a prefix of the
// other, so an initial or a short form ("Sam" for "Samuel") never makes a
// second person; a false "same person" only withholds the signal.
func sameNamedPerson(a, b []string) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	used := make([]bool, len(b))
	for _, token := range a {
		matched := false
		for i, other := range b {
			if used[i] {
				continue
			}
			if strings.HasPrefix(other, token) || strings.HasPrefix(token, other) {
				used[i] = true
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
