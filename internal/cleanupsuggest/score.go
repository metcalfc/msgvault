package cleanupsuggest

import (
	"strings"

	"golang.org/x/net/publicsuffix"
)

// Hard signals: facts computed in code that move the composite score.
const (
	SignalDMARCFail        = "dmarc_fail"
	SignalSPFFail          = "spf_fail"
	SignalDKIMFail         = "dkim_fail"
	SignalReplyToMismatch  = "reply_to_mismatch"
	SignalLinkHostMismatch = "link_host_mismatch"
	SignalSpamLabel        = "spam_label"
	SignalAuthenticated    = "authenticated"
)

// Weights of the composite score. The judged part tops out at 1.0; the hard
// signals add up to 0.60 more and a fully authenticated sender takes 0.15
// off, so hard signals alone never reach SuspectThreshold, and mail that
// passes SPF, DKIM, and DMARC reaches it only on a near-certain judgment.
const (
	weightImpersonation = 0.45
	weightPressure      = 0.20
	weightPhishing      = 0.35

	boostDMARCFail        = 0.20
	boostSPFFail          = 0.10
	boostDKIMFail         = 0.10
	boostReplyToMismatch  = 0.10
	boostLinkHostMismatch = 0.05
	boostSpamLabel        = 0.05
	creditAuthenticated   = 0.15
)

// Judgment is one message's Jev answers.
type Judgment struct {
	Impersonation float64
	Pressure      float64
	Categories    map[string]float64
}

// Score combines a message's judgment with hard signals from its state into
// one phishing score in [0, 1], and names the signals that moved it.
func Score(message Message, judgment Judgment) (float64, []string) {
	score := weightImpersonation*judgment.Impersonation +
		weightPressure*judgment.Pressure +
		weightPhishing*judgment.Categories[CategoryPhishing]
	signals := []string{}
	add := func(signal string, weight float64) {
		score += weight
		signals = append(signals, signal)
	}
	auth := message.Authentication
	if auth.DMARC == AuthFail {
		add(SignalDMARCFail, boostDMARCFail)
	}
	if auth.SPF == AuthFail || auth.SPF == "softfail" {
		add(SignalSPFFail, boostSPFFail)
	}
	if auth.DKIM == AuthFail {
		add(SignalDKIMFail, boostDKIMFail)
	}
	from := organizationalDomain(message.FromDomain)
	if replyTo := organizationalDomain(message.ReplyToDomain); replyTo != "" && from != "" && replyTo != from {
		add(SignalReplyToMismatch, boostReplyToMismatch)
	}
	if from != "" && len(message.LinkHosts) > 0 && !anyHostIn(message.LinkHosts, from) {
		add(SignalLinkHostMismatch, boostLinkHostMismatch)
	}
	for _, label := range message.Labels {
		if label == "SPAM" || label == "JUNK" {
			add(SignalSpamLabel, boostSpamLabel)
			break
		}
	}
	if auth.SPF == AuthPass && auth.DKIM == AuthPass && auth.DMARC == AuthPass {
		score -= creditAuthenticated
		signals = append(signals, SignalAuthenticated)
	}
	return clamp(score), signals
}

// KeepProbability is the probability a message is personal or work mail.
func KeepProbability(judgment Judgment) float64 {
	return clamp(judgment.Categories[CategoryPersonal] + judgment.Categories[CategoryWork])
}

// organizationalDomain reduces a host to its registrable domain, so
// mail.example.com and links.example.com count as example.com.
func organizationalDomain(host string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return ""
	}
	domain, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return host
	}
	return domain
}

func anyHostIn(hosts []string, domain string) bool {
	for _, host := range hosts {
		if organizationalDomain(host) == domain {
			return true
		}
	}
	return false
}

func clamp(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}
