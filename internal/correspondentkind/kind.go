// Package correspondentkind defines what kind of correspondent an archive
// identity cluster is: a person, an organization, a shared mailbox, an
// automated sender, a mailing list, or a record the user does not need. The
// vocabulary, the precedence between sources, and the deterministic rules
// live here so the store, the API, and the Jev classifier agree on one
// definition.
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
	// Automated is a sender that is not a person writing: notifications,
	// receipts, marketing, bots, and SMS short codes.
	Automated Kind = "automated"
	// MailingList is a list or group address whose messages are relayed from
	// many senders.
	MailingList Kind = "mailing_list"
	// Unclear is a Jev judgment that could not tell what the cluster is. It
	// is never set by a user or a rule. The cluster stays in People lists and
	// enrichment, is held out of relationship rankings, and waits in review.
	Unclear Kind = "unclear"
)

// UserKinds is the ordered vocabulary a user may set. Every kind other than
// Person is treated as "not a person".
var UserKinds = []Kind{Person, Organization, SharedMailbox, Ignored, Automated, MailingList}

// StoredKinds is every kind a correspondent_kinds row may carry: the user
// vocabulary plus kinds only a derived source writes.
var StoredKinds = []Kind{Person, Organization, SharedMailbox, Ignored, Automated, MailingList, Unclear}

// Valid reports whether k is a kind a user may set.
func (k Kind) Valid() bool {
	return slices.Contains(UserKinds, k)
}

// Known reports whether k is any stored kind, including derived-only ones.
func (k Kind) Known() bool {
	return slices.Contains(StoredKinds, k)
}

// IsPerson reports whether k leaves the cluster in People lists, contact
// matching, and enrichment. Unclear counts as a person there: an undecided
// judgment never removes anyone.
func (k Kind) IsPerson() bool {
	return k == Person || k == "" || k == Unclear
}

// LeavesPeopleLists reports whether k removes the cluster, and a saved
// profile made only of such clusters, from People lists and relationship
// rankings. Organizations, ignored records, automated senders, and mailing
// lists leave them. A shared mailbox stays as a labelled non-person row in
// sender views and keeps any saved profile listed, because the people who
// wrote from it are still correspondents.
func (k Kind) LeavesPeopleLists() bool {
	return k == Organization || k == Ignored || k == Automated || k == MailingList
}

// LeavesRankings reports whether k keeps the cluster out of relationship
// rankings by default: every kind that leaves People lists, plus an unclear
// judgment, since a ranking of relationships should show only clusters no
// one doubts are people.
func (k Kind) LeavesRankings() bool {
	return k.LeavesPeopleLists() || k == Unclear
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
	case Automated:
		return "Automated sender"
	case MailingList:
		return "Mailing list"
	case Unclear:
		return "Unclear"
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
	// SourceRule is a deterministic classifier (see Classify).
	SourceRule Source = "rule"
	// SourceJev is a Jev (System One) judgment.
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

// freemailDomains are consumer mail providers, including regional and ISP
// mail, whose domain says nothing about an organization, so an organization
// is never given one as its domain. zoho.com stays listed for its consumer
// mail even though Zoho is also a company.
var freemailDomains = map[string]struct{}{
	"gmail.com": {}, "googlemail.com": {}, "yahoo.com": {}, "ymail.com": {},
	"outlook.com": {}, "hotmail.com": {}, "live.com": {}, "msn.com": {},
	"icloud.com": {}, "me.com": {}, "mac.com": {}, "aol.com": {},
	"proton.me": {}, "protonmail.com": {}, "fastmail.com": {}, "gmx.com": {},
	"mail.com": {}, "zoho.com": {}, "yandex.com": {}, "hey.com": {},
	// Regional consumer mail.
	"yahoo.co.uk": {}, "yahoo.fr": {}, "yahoo.de": {}, "yahoo.co.jp": {},
	"hotmail.co.uk": {}, "hotmail.fr": {}, "hotmail.de": {}, "hotmail.it": {},
	"live.co.uk": {}, "live.fr": {}, "outlook.fr": {}, "outlook.de": {},
	"gmx.de": {}, "gmx.net": {}, "web.de": {}, "t-online.de": {},
	"pm.me": {}, "protonmail.ch": {}, "qq.com": {}, "163.com": {}, "126.com": {},
	"mail.ru": {}, "yandex.ru": {}, "naver.com": {}, "daum.net": {},
	"orange.fr": {}, "free.fr": {}, "libero.it": {}, "rediffmail.com": {},
	// Internet service provider mail.
	"comcast.net": {}, "att.net": {}, "verizon.net": {}, "sbcglobal.net": {},
	"btinternet.com": {},
}

// IsFreemailDomain reports whether domain belongs to a consumer mail
// provider.
func IsFreemailDomain(domain string) bool {
	_, ok := freemailDomains[domain]
	return ok
}
