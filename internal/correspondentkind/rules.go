package correspondentkind

import (
	"slices"
	"strings"
)

// Signals is the deterministic evidence about one identity cluster that the
// rules read. The store gathers it from message metadata, labels, and the
// headers of a few sampled raw messages; nothing here reads message bodies.
type Signals struct {
	// Emails and Phones are the cluster's addresses as stored.
	Emails []string
	Phones []string
	// ProviderBot is true when a chat provider flagged a member as a bot,
	// webhook, or system account.
	ProviderBot bool
	// Sent counts messages the cluster sent; Received counts messages the
	// archive owner sent to it.
	Sent     int64
	Received int64
	// ListIDMessages counts messages the cluster sent that carry a List-Id.
	ListIDMessages int64
	// ListIDs are distinct List-Id values seen on the cluster's messages.
	ListIDs []string
	// Categories counts the cluster's sent messages per Gmail CATEGORY_*
	// label, keyed by label ID (for example CATEGORY_PROMOTIONS).
	Categories map[string]int64
	// Headers counts header presence over sampled raw messages.
	Headers HeaderCounts
}

// HeaderCounts counts, over the sampled raw messages a cluster sent, how
// many carried each header. Only presence is recorded, never values.
type HeaderCounts struct {
	Sampled         int `json:"sampled"`
	ListUnsubscribe int `json:"list_unsubscribe"`
	// AutoSubmitted counts Auto-Submitted values that mark generated mail
	// (auto-generated, auto-notified). auto-replied is left out: people's
	// out-of-office replies carry it.
	AutoSubmitted int `json:"auto_submitted"`
	// PrecedenceBulk counts Precedence: bulk, list, or junk.
	PrecedenceBulk int `json:"precedence_bulk"`
	ListID         int `json:"list_id"`
}

// Reason names the rule that decided a cluster.
type Reason string

const (
	ReasonProviderBot        Reason = "provider_bot"
	ReasonShortCode          Reason = "sms_short_code"
	ReasonNoReplyAddress     Reason = "noreply_address"
	ReasonAutoSubmitted      Reason = "auto_submitted"
	ReasonListAddress        Reason = "list_address"
	ReasonBulkUnsubscribe    Reason = "bulk_unsubscribe"
	ReasonPromotionsCategory Reason = "promotions_category"
)

// Decision is a rule's verdict for one cluster.
type Decision struct {
	Kind   Kind
	Reason Reason
}

// ruleMinimumSent is how many messages a cluster must have sent before the
// volume-based rules (bulk headers, promotions category) may decide.
const ruleMinimumSent = 3

// Classify applies the deterministic rules in order and returns a decision
// only when a signal is decisive. Identity rules (a provider bot flag, an SMS
// short code, a no-reply address, the list's own address) decide on their
// own. Volume rules need several messages, no message from the owner to the
// cluster, and no sign that the messages were relayed by a list, because a
// person writing through a list or a newsletter tool carries the same
// headers. Anything else is left for a person or Jev to decide.
func Classify(signals Signals) (Decision, bool) {
	switch {
	case signals.ProviderBot:
		return Decision{Kind: Automated, Reason: ReasonProviderBot}, true
	case len(signals.Emails) == 0 && len(signals.Phones) > 0 &&
		!slices.ContainsFunc(signals.Phones, func(phone string) bool { return !IsShortCode(phone) }):
		return Decision{Kind: Automated, Reason: ReasonShortCode}, true
	case len(signals.Emails) > 0 &&
		!slices.ContainsFunc(signals.Emails, func(email string) bool { return !IsNoReplyAddress(email) }):
		return Decision{Kind: Automated, Reason: ReasonNoReplyAddress}, true
	case slices.ContainsFunc(signals.Emails, func(email string) bool { return isListAddress(email, signals.ListIDs) }):
		return Decision{Kind: MailingList, Reason: ReasonListAddress}, true
	case signals.Headers.Sampled > 0 && signals.Headers.AutoSubmitted*2 > signals.Headers.Sampled:
		return Decision{Kind: Automated, Reason: ReasonAutoSubmitted}, true
	}
	if signals.Received > 0 || signals.Sent < ruleMinimumSent || signals.ListIDMessages*5 >= signals.Sent ||
		signals.Headers.ListID*5 > signals.Headers.Sampled {
		return Decision{}, false
	}
	headers := signals.Headers
	if headers.Sampled >= 2 && (headers.ListUnsubscribe*5 >= headers.Sampled*4 ||
		headers.PrecedenceBulk*5 >= headers.Sampled*4) {
		return Decision{Kind: Automated, Reason: ReasonBulkUnsubscribe}, true
	}
	// Gmail's Promotions label alone can be wrong about a person on a
	// custom domain, so it decides only with bulk-sending evidence in the
	// sampled headers and a sustained volume.
	if !slices.ContainsFunc(signals.Emails, func(email string) bool { return IsFreemailDomain(emailDomain(email)) }) &&
		signals.Sent >= promotionsMinimumSent && signals.Categories["CATEGORY_PROMOTIONS"]*5 >= signals.Sent*4 &&
		headers.ListUnsubscribe+headers.PrecedenceBulk > 0 {
		return Decision{Kind: Automated, Reason: ReasonPromotionsCategory}, true
	}
	return Decision{}, false
}

// promotionsMinimumSent is the volume the Promotions rule needs.
const promotionsMinimumSent = 10

// noReplyPrefixes start local parts that never reach a person.
var noReplyPrefixes = []string{"noreply", "no-reply", "no_reply", "donotreply", "do-not-reply", "do_not_reply", "bounce"}

// noReplyLocalParts are exact local parts of machine senders.
var noReplyLocalParts = []string{
	"mailer-daemon", "postmaster", "notifications", "notification", "alerts", "alert",
	"newsletter", "newsletters", "digest", "automated", "system",
}

// IsNoReplyAddress reports whether an email address's local part, ignoring a
// "+tag" and case, marks a machine sender such as noreply@ or
// mailer-daemon@.
func IsNoReplyAddress(address string) bool {
	local := emailLocalPart(address)
	if local == "" {
		return false
	}
	if slices.Contains(noReplyLocalParts, local) {
		return true
	}
	return slices.ContainsFunc(noReplyPrefixes, func(prefix string) bool { return strings.HasPrefix(local, prefix) })
}

// IsShortCode reports whether a phone number is an SMS short code: three to
// six digits with no country code.
func IsShortCode(phone string) bool {
	phone = strings.TrimSpace(phone)
	if strings.HasPrefix(phone, "+") {
		return false
	}
	digits := 0
	for _, r := range phone {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == ' ' || r == '-':
		default:
			return false
		}
	}
	return digits >= 3 && digits <= 6
}

// isListAddress reports whether address is the list's own posting address:
// RFC 2919 List-Ids are conventionally the posting address with "@" replaced
// by ".", as in <team.example.com> for team@example.com.
func isListAddress(address string, listIDs []string) bool {
	address = strings.ToLower(strings.TrimSpace(address))
	at := strings.LastIndex(address, "@")
	if at <= 0 || at == len(address)-1 {
		return false
	}
	dotted := address[:at] + "." + address[at+1:]
	for _, listID := range listIDs {
		if NormalizeListID(listID) == dotted {
			return true
		}
	}
	return false
}

// NormalizeListID reduces a List-Id header value to its identifier: the
// part inside angle brackets when present, lowercased and trimmed.
func NormalizeListID(value string) string {
	value = strings.TrimSpace(value)
	if open := strings.LastIndex(value, "<"); open >= 0 {
		if end := strings.Index(value[open:], ">"); end > 0 {
			value = value[open+1 : open+end]
		}
	}
	return strings.ToLower(strings.TrimSpace(value))
}

func emailDomain(address string) string {
	address = strings.ToLower(strings.TrimSpace(address))
	at := strings.LastIndex(address, "@")
	if at < 0 {
		return ""
	}
	return address[at+1:]
}

// SplitEmail returns an address's local part (without a "+tag") and domain,
// lowercased. Either is empty when the address is malformed.
func SplitEmail(address string) (string, string) {
	return emailLocalPart(address), emailDomain(address)
}
