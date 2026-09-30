package cleanupsuggest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/msgvault/internal/store"
)

func TestParseHeaderBlockTrustsOnlyTheTopmostAuthenticationResults(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		block   string
		replyTo string
		auth    Authentication
	}{
		{
			name: "receiving server verdicts win over a forged lower header",
			block: "Authentication-Results: mx.example.net;\r\n spf=softfail smtp.mailfrom=bank.example;\r\n" +
				" dkim=none; dmarc=fail header.from=bank.example\r\n" +
				"Authentication-Results: forged.example; spf=pass; dkim=pass; dmarc=pass\r\n" +
				"Reply-To: \"Support\" <help@collector.example.org>\r\n\r\n",
			replyTo: "collector.example.org",
			auth:    Authentication{SPF: "softfail", DKIM: "none", DMARC: AuthFail},
		},
		{
			name:  "missing results are unknown",
			block: "Subject: Hello\r\n\r\n",
			auth:  Authentication{SPF: AuthUnknown, DKIM: AuthUnknown, DMARC: AuthUnknown},
		},
		{
			name:  "no header block at all",
			block: "",
			auth:  Authentication{SPF: AuthUnknown, DKIM: AuthUnknown, DMARC: AuthUnknown},
		},
		{
			name:    "an unparseable reply-to still yields its domain",
			block:   "Reply-To: billing@Pay.Example.COM>\n\n",
			replyTo: "pay.example.com",
			auth:    Authentication{SPF: AuthUnknown, DKIM: AuthUnknown, DMARC: AuthUnknown},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			replyTo, auth := parseHeaderBlock([]byte(tt.block))
			assert.Equal(t, tt.replyTo, replyTo)
			assert.Equal(t, tt.auth, auth)
		})
	}
}

func TestMessageStateSendsSystemLabelsHostsAndAtMost500Characters(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	long := strings.Repeat("word ", 200)
	message := MessageState(store.CleanupEvidence{
		FromName: "  Example   Bank ", FromEmail: "alerts@Mail.Bank.Example",
		Subject: "Verify your account", Labels: []string{"SPAM", "Therapy notes", "CATEGORY_PROMOTIONS"},
		BodyText: "Click https://login.bank-verify.example/now or http://login.bank-verify.example/again " + long,
		BodyHTML: `<a href="https://tracker.example.net/p?id=1">x</a>`,
	}, "")

	assert.Equal("Example Bank", message.FromName)
	assert.Equal("mail.bank.example", message.FromDomain)
	assert.Equal([]string{"SPAM", "CATEGORY_PROMOTIONS"}, message.Labels, "your own label names never leave")
	assert.Equal([]string{"tracker.example.net", "login.bank-verify.example"}, message.LinkHosts)
	assert.Equal(AddressedBccOrUndisclosed, message.AddressedAs)
	assert.Equal("unclassified", message.SenderKind)
	assert.Len([]rune(message.BodyStart), BodyChars)

	htmlOnly := MessageState(store.CleanupEvidence{BodyHTML: "<p>Hello <b>there</b></p>", AddressedToOwner: true}, "automated")
	assert.Equal("Hello there", htmlOnly.BodyStart)
	assert.Equal(AddressedToOrCc, htmlOnly.AddressedAs)
	assert.Empty(htmlOnly.LinkHosts)
}

func TestScoreCombinesJudgmentWithHardSignals(t *testing.T) {
	t.Parallel()
	failing := Message{
		FromDomain: "mail.bank.example", ReplyToDomain: "collector.example.org",
		LinkHosts:      []string{"login.bank-verify.example"},
		Authentication: Authentication{SPF: "softfail", DKIM: AuthFail, DMARC: AuthFail},
		Labels:         []string{"SPAM"},
	}
	authenticated := Message{
		FromDomain: "news.shop.example", ReplyToDomain: "shop.example",
		LinkHosts:      []string{"links.shop.example"},
		Authentication: Authentication{SPF: AuthPass, DKIM: AuthPass, DMARC: AuthPass},
		Labels:         []string{"CATEGORY_PROMOTIONS"},
	}
	for _, tt := range []struct {
		name     string
		message  Message
		judgment Judgment
		score    float64
		signals  []string
	}{
		{
			name: "hard signals alone stay below the suspect threshold", message: failing,
			score: 0.60,
			signals: []string{SignalDMARCFail, SignalSPFFail, SignalDKIMFail, SignalReplyToMismatch,
				SignalLinkHostMismatch, SignalSpamLabel},
		},
		{
			name: "a moderate judgment with failed authentication is suspected", message: failing,
			judgment: Judgment{Impersonation: 0.5, Pressure: 0.5, Categories: map[string]float64{CategoryPhishing: 0.3}},
			score:    1,
			signals: []string{SignalDMARCFail, SignalSPFFail, SignalDKIMFail, SignalReplyToMismatch,
				SignalLinkHostMismatch, SignalSpamLabel},
		},
		{
			name: "authenticated marketing with the same judgment is not", message: authenticated,
			judgment: Judgment{Impersonation: 0.5, Pressure: 0.5, Categories: map[string]float64{CategoryPhishing: 0.3}},
			score:    0.45*0.5 + 0.20*0.5 + 0.35*0.3 - 0.15,
			signals:  []string{SignalAuthenticated},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			score, signals := Score(tt.message, tt.judgment)
			assert.InDelta(t, tt.score, score, 1e-9)
			assert.Equal(t, tt.signals, signals)
		})
	}
	assert.InDelta(t, 0.9, KeepProbability(Judgment{Categories: map[string]float64{
		CategoryPersonal: 0.3, CategoryWork: 0.6, CategoryMarketing: 0.1,
	}}), 1e-9)
}
