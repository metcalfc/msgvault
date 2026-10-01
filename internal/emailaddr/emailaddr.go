// Package emailaddr decides when two email addresses reach the same mailbox.
//
// msgvault links addresses that deliver to one mailbox into one identity. The
// rule is deliberately small:
//
//   - Plus tags, every domain: everything from the first "+" in the local
//     part onward is ignored, so user+news@example.com is user@example.com.
//   - Dots, Gmail only: for gmail.com and googlemail.com, dots in the local
//     part are ignored and googlemail.com is gmail.com.
//   - Dots, other domains: addresses that match only after removing dots are
//     a dot variant. Some providers deliver them to one mailbox and some do
//     not, so a dot variant is a suggestion for review, never a link.
//
// Comparison is case-insensitive on the whole address.
package emailaddr

import "strings"

// Equivalence is how two addresses relate under the mailbox rule.
type Equivalence int

const (
	// Different addresses reach different mailboxes, or one is invalid.
	Different Equivalence = iota
	// SameMailbox addresses deliver to one mailbox and may be linked
	// automatically. Identical addresses are SameMailbox too.
	SameMailbox
	// DotVariant addresses differ only by dots in a non-Gmail local part.
	// They may be the same mailbox, so they are suggested for review.
	DotVariant
)

func (e Equivalence) String() string {
	switch e {
	case SameMailbox:
		return "same_mailbox"
	case DotVariant:
		return "dot_variant"
	default:
		return "different"
	}
}

const (
	gmailDomain      = "gmail.com"
	googlemailDomain = "googlemail.com"
	plusTagSeparator = "+"
	localPartDot     = "."
)

// IsGmailDomain reports whether domain is gmail.com or googlemail.com,
// ignoring case.
func IsGmailDomain(domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	return domain == gmailDomain || domain == googlemailDomain
}

// split lowercases and validates an address, returning its local part and
// domain. An address is valid when it has exactly one "@" with a non-empty
// local part and domain and no whitespace or angle brackets. A quoted local
// part is valid but reported as quoted, because "+" and "." inside quotes
// are literal characters rather than tag or dot syntax.
func split(address string) (local, domain string, quoted, ok bool) {
	address = strings.ToLower(strings.TrimSpace(address))
	if address == "" || strings.ContainsAny(address, " \t\r\n<>") {
		return "", "", false, false
	}
	at := strings.LastIndexByte(address, '@')
	if at <= 0 || at == len(address)-1 {
		return "", "", false, false
	}
	local, domain = address[:at], address[at+1:]
	quoted = len(local) >= 2 && strings.HasPrefix(local, `"`) && strings.HasSuffix(local, `"`)
	if !quoted && strings.Contains(local, "@") {
		return "", "", false, false
	}
	if strings.Contains(domain, "@") || strings.HasPrefix(domain, ".") ||
		strings.HasSuffix(domain, ".") {
		return "", "", false, false
	}
	return local, domain, quoted, true
}

// stripPlusTag removes the tag from the first "+" onward. A local part that
// starts with "+" has no mailbox name before the tag, so it is kept whole and
// only ever matches itself.
func stripPlusTag(local string) string {
	if plus := strings.Index(local, plusTagSeparator); plus > 0 {
		return local[:plus]
	}
	return local
}

// Mailbox returns the canonical key of the mailbox an address delivers to:
// the lowercased address with any plus tag removed and, for Gmail, dots
// removed and googlemail.com mapped to gmail.com. Two addresses with the same
// key are SameMailbox. ok is false for an invalid address.
func Mailbox(address string) (string, bool) {
	local, domain, quoted, ok := split(address)
	if !ok {
		return "", false
	}
	if quoted {
		return local + "@" + domain, true
	}
	local = stripPlusTag(local)
	if IsGmailDomain(domain) {
		domain = gmailDomain
		local = strings.ReplaceAll(local, localPartDot, "")
		if local == "" {
			return "", false
		}
	}
	return local + "@" + domain, true
}

// DotInsensitiveMailbox returns the Mailbox key with every dot removed from
// the local part, for every domain. Addresses with different Mailbox keys but
// the same dot-insensitive key are DotVariant. ok is false for an invalid
// address.
func DotInsensitiveMailbox(address string) (string, bool) {
	mailbox, ok := Mailbox(address)
	if !ok {
		return "", false
	}
	at := strings.LastIndexByte(mailbox, '@')
	local, domain := mailbox[:at], mailbox[at+1:]
	if strings.HasPrefix(local, `"`) {
		return mailbox, true
	}
	dotless := strings.ReplaceAll(local, localPartDot, "")
	if dotless == "" {
		return mailbox, true
	}
	return dotless + "@" + domain, true
}

// Compare reports how two addresses relate under the mailbox rule.
func Compare(a, b string) Equivalence {
	mailboxA, okA := Mailbox(a)
	mailboxB, okB := Mailbox(b)
	if !okA || !okB {
		return Different
	}
	if mailboxA == mailboxB {
		return SameMailbox
	}
	dotlessA, _ := DotInsensitiveMailbox(a)
	dotlessB, _ := DotInsensitiveMailbox(b)
	if dotlessA == dotlessB {
		return DotVariant
	}
	return Different
}

// GmailAccount returns the canonical Google account address for a gmail.com
// or googlemail.com address, and "" for any other or invalid address. Google
// sign-in uses it to accept the alias spellings Gmail delivers to the same
// account; it never relaxes matching for non-Gmail domains.
func GmailAccount(address string) string {
	_, domain, _, ok := split(address)
	if !ok || !IsGmailDomain(domain) {
		return ""
	}
	mailbox, ok := Mailbox(address)
	if !ok {
		return ""
	}
	return mailbox
}
