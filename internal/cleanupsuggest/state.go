package cleanupsuggest

import (
	"bufio"
	"bytes"
	"net/mail"
	"net/textproto"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"go.kenn.io/msgvault/internal/mime"
	"go.kenn.io/msgvault/internal/store"
)

// State limits: what one message may carry.
const (
	maxFromNameRunes = 120
	maxSubjectRunes  = 200
	maxLinkHosts     = 10
	maxLabels        = 10
)

// Authentication result values. Anything the receiving server did not
// report, or a header block that could not be read, is unknown.
const (
	AuthUnknown = "unknown"
	AuthPass    = "pass"
	AuthFail    = "fail"
)

// Addressing values: whether an owner address was a visible recipient.
const (
	AddressedToOrCc           = "to_or_cc"
	AddressedBccOrUndisclosed = "bcc_or_undisclosed"
)

// Authentication is the receiving server's SPF, DKIM, and DMARC verdicts.
type Authentication struct {
	SPF   string `json:"spf"`
	DKIM  string `json:"dkim"`
	DMARC string `json:"dmarc"`
}

// Message is the state sent about one message. Its fields are exactly
// StateFields.
type Message struct {
	FromName       string         `json:"from_name"`
	FromDomain     string         `json:"from_domain"`
	ReplyToDomain  string         `json:"reply_to_domain"`
	LinkHosts      []string       `json:"link_hosts"`
	Authentication Authentication `json:"authentication"`
	AddressedAs    string         `json:"addressed_as"`
	Labels         []string       `json:"labels"`
	ThreadReplied  bool           `json:"thread_replied"`
	SenderKind     string         `json:"sender_kind"`
	Subject        string         `json:"subject"`
	BodyStart      string         `json:"body_start"`
}

// State is one request's state.
type State struct {
	Messages []Message `json:"messages"`
}

// GmailAuthservID is the authserv-id Gmail's receiving servers stamp on
// Authentication-Results.
const GmailAuthservID = "mx.google.com"

// TrustedAuthservIDs returns the authserv-ids whose Authentication-Results
// are believed for a source: Gmail's own for Gmail sources, plus the ones
// the owner configured for any source. Nothing else is trusted, because a
// sender can write an Authentication-Results header of its own.
func TrustedAuthservIDs(sourceType string, configured []string) []string {
	trusted := []string{}
	if strings.EqualFold(sourceType, "gmail") {
		trusted = append(trusted, GmailAuthservID)
	}
	for _, id := range configured {
		if id = strings.ToLower(strings.TrimSpace(id)); id != "" {
			trusted = append(trusted, id)
		}
	}
	return trusted
}

// MessageState builds the state for one message from its evidence and its
// sender's kind. Only system labels are sent; the owner's own label names
// never leave the machine. Authentication results come only from a header
// stamped by one of the trusted authserv-ids.
func MessageState(evidence store.CleanupEvidence, senderKind string, trustedAuthservIDs []string) Message {
	replyTo, auth := parseHeaderBlock(evidence.HeaderBlock, trustedAuthservIDs)
	message := Message{
		FromName:       truncateRunes(collapse(evidence.FromName), maxFromNameRunes),
		FromDomain:     emailDomain(evidence.FromEmail),
		ReplyToDomain:  replyTo,
		LinkHosts:      linkHosts(evidence.BodyText, evidence.BodyHTML),
		Authentication: auth,
		AddressedAs:    AddressedBccOrUndisclosed,
		Labels:         systemLabels(evidence.Labels),
		ThreadReplied:  evidence.ThreadReplied,
		SenderKind:     senderKind,
		Subject:        truncateRunes(collapse(evidence.Subject), maxSubjectRunes),
		BodyStart:      bodyStart(evidence.BodyText, evidence.BodyHTML),
	}
	if evidence.AddressedToOwner {
		message.AddressedAs = AddressedToOrCc
	}
	if message.SenderKind == "" {
		message.SenderKind = "unclassified"
	}
	return message
}

var authResultPattern = regexp.MustCompile(`(?i)\b(spf|dkim|dmarc)\s*=\s*([a-z]+)`)

// parseHeaderBlock reads Reply-To and the topmost Authentication-Results
// header whose authserv-id is trusted. A receiving server removes forged
// copies of its own authserv-id (RFC 8601 section 5), so any header with
// another authserv-id may come from the sender and is ignored. A missing or
// unreadable block, or no trusted header, yields unknown verdicts.
func parseHeaderBlock(block []byte, trustedAuthservIDs []string) (string, Authentication) {
	auth := Authentication{SPF: AuthUnknown, DKIM: AuthUnknown, DMARC: AuthUnknown}
	if len(block) == 0 {
		return "", auth
	}
	headers, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(block))).ReadMIMEHeader()
	if err != nil && len(headers) == 0 {
		return "", auth
	}
	replyTo := ""
	if value := headers.Get("Reply-To"); value != "" {
		if addresses, err := mail.ParseAddressList(value); err == nil && len(addresses) > 0 {
			replyTo = emailDomain(addresses[0].Address)
		} else if at := strings.LastIndex(value, "@"); at >= 0 {
			replyTo = emailDomain("x" + strings.Trim(value[at:], " <>\"'"))
		}
	}
	if result, ok := trustedResult(headers.Values("Authentication-Results"), trustedAuthservIDs); ok {
		for _, match := range authResultPattern.FindAllStringSubmatch(result, -1) {
			method, verdict := strings.ToLower(match[1]), strings.ToLower(match[2])
			switch method {
			case "spf":
				if auth.SPF == AuthUnknown {
					auth.SPF = verdict
				}
			case "dkim":
				if auth.DKIM == AuthUnknown {
					auth.DKIM = verdict
				}
			case "dmarc":
				if auth.DMARC == AuthUnknown {
					auth.DMARC = verdict
				}
			}
		}
	}
	return replyTo, auth
}

// trustedResult returns the topmost header whose authserv-id, the token
// before the first semicolon, is trusted. The results after it are returned.
func trustedResult(values []string, trusted []string) (string, bool) {
	for _, value := range values {
		authserv, results, found := strings.Cut(value, ";")
		if !found {
			continue
		}
		fields := strings.Fields(authserv)
		if len(fields) == 0 {
			continue
		}
		if slices.Contains(trusted, strings.ToLower(fields[0])) {
			return results, true
		}
	}
	return "", false
}

var linkPattern = regexp.MustCompile(`(?i)https?://[^\s"'<>()\[\]{}]+`)

// linkHosts lists the distinct hosts of http(s) links in the bodies, in
// order of first appearance.
func linkHosts(text, html string) []string {
	hosts := []string{}
	seen := map[string]struct{}{}
	for _, body := range []string{html, text} {
		for _, raw := range linkPattern.FindAllString(body, -1) {
			if len(hosts) == maxLinkHosts {
				return hosts
			}
			parsed, err := url.Parse(raw)
			if err != nil {
				continue
			}
			host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
			if host == "" {
				continue
			}
			if _, ok := seen[host]; ok {
				continue
			}
			seen[host] = struct{}{}
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func bodyStart(text, html string) string {
	body := collapse(text)
	if body == "" && html != "" {
		body = collapse(mime.StripHTML(html))
	}
	return truncateRunes(body, BodyChars)
}

func systemLabels(labels []string) []string {
	result := []string{}
	for _, label := range labels {
		if len(result) == maxLabels {
			break
		}
		upper := strings.ToUpper(strings.TrimSpace(label))
		if store.IsSystemLabel(upper) || upper == "JUNK" {
			result = append(result, upper)
		}
	}
	return result
}

func emailDomain(address string) string {
	at := strings.LastIndex(address, "@")
	if at < 0 || at == len(address)-1 {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(address[at+1:])), ".")
}

func collapse(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}
