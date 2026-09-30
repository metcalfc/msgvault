// Package correspondentkind defines what kind of correspondent an archive
// identity cluster is: a person, an organization, a shared mailbox, or a
// record the user does not need. The vocabulary and the precedence between
// sources live here so the store, the API, and later rule or Jev classifiers
// agree on one definition.
package correspondentkind

import "slices"

// Kind is the correspondent kind of one participant identity cluster.
type Kind string

const (
	// Person is the default: the cluster is an individual human. Storing it
	// as a user override means "this is a person", which wins over any
	// derived classification.
	Person Kind = "person"
	// Organization is a business or institution. Its messages are kept and
	// grouped under an Organization record.
	Organization Kind = "organization"
	// SharedMailbox is an address several people write from, such as a
	// support desk. Its messages are kept; the individuals keep their own
	// profiles.
	SharedMailbox Kind = "shared_mailbox"
	// Ignored is a record the user does not need as a contact. Its messages
	// stay searchable.
	Ignored Kind = "ignored"
)

// UserKinds is the ordered vocabulary a user may set. Derived sources may
// later add kinds (for example automated senders or mailing lists); every
// kind other than Person is treated as "not a person".
var UserKinds = []Kind{Person, Organization, SharedMailbox, Ignored}

// Valid reports whether k is a kind a user may set.
func (k Kind) Valid() bool {
	return slices.Contains(UserKinds, k)
}

// IsPerson reports whether k leaves the cluster in People lists.
func (k Kind) IsPerson() bool {
	return k == Person || k == ""
}

// HiddenFromPeople reports whether k removes the cluster from People lists,
// relationship rankings, and people-oriented tools. A shared mailbox stays
// visible in sender views with its own label, but it is not a person either.
func (k Kind) HiddenFromPeople() bool {
	return !k.IsPerson()
}

// Label is the short human label for k.
func (k Kind) Label() string {
	switch k {
	case Organization:
		return "Organization"
	case SharedMailbox:
		return "Shared mailbox"
	case Ignored:
		return "Ignored"
	case Person:
		return "Person"
	default:
		return string(k)
	}
}

// Source records who classified a cluster.
type Source string

const (
	// SourceUser is an explicit user decision. It always wins.
	SourceUser Source = "user"
	// SourceRule is a deterministic classifier (reserved for later work).
	SourceRule Source = "rule"
	// SourceJev is a model judgment (reserved for later work).
	SourceJev Source = "jev"
)

// Valid reports whether s is a known source.
func (s Source) Valid() bool {
	return s == SourceUser || s == SourceRule || s == SourceJev
}

// Precedence orders sources: a higher value wins when one cluster carries
// classifications from several sources.
func (s Source) Precedence() int {
	switch s {
	case SourceUser:
		return 3
	case SourceRule:
		return 2
	case SourceJev:
		return 1
	default:
		return 0
	}
}

// NotAPersonReason is the decision note recorded on identity match
// candidates that a classification resolved, so they are not proposed again
// and can be restored when the classification is cleared.
const NotAPersonReason = "not_a_person"

// freemailDomains are consumer mail providers whose domain says nothing about
// an organization, so an organization is never given one as its domain.
var freemailDomains = map[string]struct{}{
	"gmail.com": {}, "googlemail.com": {}, "yahoo.com": {}, "ymail.com": {},
	"outlook.com": {}, "hotmail.com": {}, "live.com": {}, "msn.com": {},
	"icloud.com": {}, "me.com": {}, "mac.com": {}, "aol.com": {},
	"proton.me": {}, "protonmail.com": {}, "fastmail.com": {}, "gmx.com": {},
	"mail.com": {}, "zoho.com": {}, "yandex.com": {}, "hey.com": {},
}

// IsFreemailDomain reports whether domain belongs to a consumer mail
// provider.
func IsFreemailDomain(domain string) bool {
	_, ok := freemailDomains[domain]
	return ok
}
