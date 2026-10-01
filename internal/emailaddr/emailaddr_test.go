package emailaddr_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/msgvault/internal/emailaddr"
)

func TestMailbox(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		address string
		want    string
		wantOK  bool
	}{
		{"plain address", "user@example.com", "user@example.com", true},
		{"no plus or dot on gmail", "user@gmail.com", "user@gmail.com", true},
		{"case folded", "User@Example.COM", "user@example.com", true},
		{"surrounding space trimmed", "  user@example.com ", "user@example.com", true},
		{"plus tag on example.com", "user+news@example.com", "user@example.com", true},
		{"plus tag on example.org", "user+receipts@example.org", "user@example.org", true},
		{"plus tag on subdomain", "team+alerts@mail.example.net", "team@mail.example.net", true},
		{"plus tag on gmail", "user+shopping@gmail.com", "user@gmail.com", true},
		{"empty plus tag", "user+@example.com", "user@example.com", true},
		{"multiple plus signs", "user+a+b@example.com", "user@example.com", true},
		{"leading plus kept whole", "+tag@example.com", "+tag@example.com", true},
		{"leading plus on gmail kept whole", "+tag@gmail.com", "+tag@gmail.com", true},
		{"non-gmail dots kept", "first.last@example.com", "first.last@example.com", true},
		{"gmail dots removed", "f.i.r.s.t@gmail.com", "first@gmail.com", true},
		{"gmail dots and plus", "first.last+tag@gmail.com", "firstlast@gmail.com", true},
		{"dots inside plus tag ignored", "user+a.b@gmail.com", "user@gmail.com", true},
		{"googlemail mapped to gmail", "user@googlemail.com", "user@gmail.com", true},
		{"googlemail dots and case", "First.Last@GoogleMail.com", "firstlast@gmail.com", true},
		{"non-gmail plus keeps dots", "first.last+tag@example.com", "first.last@example.com", true},
		{"quoted local part literal", `"user+tag"@example.com`, `"user+tag"@example.com`, true},
		{"empty", "", "", false},
		{"no at sign", "user.example.com", "", false},
		{"empty local part", "@example.com", "", false},
		{"empty domain", "user@", "", false},
		{"two at signs", "user@host@example.com", "", false},
		{"inner whitespace", "us er@example.com", "", false},
		{"display-name form", "User <user@example.com>", "", false},
		{"domain starts with dot", "user@.example.com", "", false},
		{"gmail local only dots", "...@gmail.com", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := emailaddr.Mailbox(tt.address)
			assert.Equal(t, tt.wantOK, ok, "Mailbox(%q) ok", tt.address)
			assert.Equal(t, tt.want, got, "Mailbox(%q)", tt.address)
		})
	}
}

func TestDotInsensitiveMailbox(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		address string
		want    string
		wantOK  bool
	}{
		{"non-gmail dots removed", "first.last@example.com", "firstlast@example.com", true},
		{"non-gmail dots and plus removed", "first.last+tag@example.com", "firstlast@example.com", true},
		{"gmail already dotless", "first.last@gmail.com", "firstlast@gmail.com", true},
		{"no dots", "user@example.com", "user@example.com", true},
		{"invalid", "user", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := emailaddr.DotInsensitiveMailbox(tt.address)
			assert.Equal(t, tt.wantOK, ok, "DotInsensitiveMailbox(%q) ok", tt.address)
			assert.Equal(t, tt.want, got, "DotInsensitiveMailbox(%q)", tt.address)
		})
	}
}

func TestCompare(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b string
		want emailaddr.Equivalence
	}{
		{"identical", "user@example.com", "user@example.com", emailaddr.SameMailbox},
		{"case only", "USER@Example.com", "user@example.com", emailaddr.SameMailbox},
		{"plus tag example.com", "user+news@example.com", "user@example.com", emailaddr.SameMailbox},
		{"plus tag example.org", "user@example.org", "user+bills@example.org", emailaddr.SameMailbox},
		{"two plus tags", "user+a@example.net", "user+b@example.net", emailaddr.SameMailbox},
		{"multiple plus", "user+a+b@example.com", "user@example.com", emailaddr.SameMailbox},
		{"gmail dots", "first.last@gmail.com", "firstlast@gmail.com", emailaddr.SameMailbox},
		{"gmail dots and plus", "f.irst+tag@gmail.com", "first@gmail.com", emailaddr.SameMailbox},
		{"googlemail and gmail", "user@googlemail.com", "user@gmail.com", emailaddr.SameMailbox},
		{"googlemail dots and gmail plus", "u.ser@googlemail.com", "user+x@gmail.com", emailaddr.SameMailbox},
		{"non-gmail dots suggest only", "first.last@example.com", "firstlast@example.com", emailaddr.DotVariant},
		{"non-gmail dots and plus suggest only", "first.last+tag@example.com", "firstlast@example.com", emailaddr.DotVariant},
		{"leading plus only matches itself", "+tag@example.com", "tag@example.com", emailaddr.Different},
		{"leading plus differs from bare domain", "+a@example.com", "+b@example.com", emailaddr.Different},
		{"different local parts", "alice@example.com", "bob@example.com", emailaddr.Different},
		{"same local different domain", "user@example.com", "user@example.org", emailaddr.Different},
		{"gmail versus other domain", "user@gmail.com", "user@example.com", emailaddr.Different},
		{"quoted plus is literal", `"user+tag"@example.com`, "user@example.com", emailaddr.Different},
		{"invalid left", "not-an-address", "user@example.com", emailaddr.Different},
		{"invalid both", "", "", emailaddr.Different},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, emailaddr.Compare(tt.a, tt.b), "Compare(%q, %q)", tt.a, tt.b)
			assert.Equal(t, tt.want, emailaddr.Compare(tt.b, tt.a), "Compare(%q, %q)", tt.b, tt.a)
		})
	}
}

func TestGmailAccount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		address string
		want    string
	}{
		{"user@gmail.com", "user@gmail.com"},
		{"User@Gmail.Com", "user@gmail.com"},
		{"first.last@gmail.com", "firstlast@gmail.com"},
		{"user@googlemail.com", "user@gmail.com"},
		{"f.i.r.s.t@googlemail.com", "first@gmail.com"},
		{"user+tag@gmail.com", "user@gmail.com"},
		{"user+@gmail.com", "user@gmail.com"},
		{"f.o.o+bar@googlemail.com", "foo@gmail.com"},
		{"user+tag@example.com", ""},
		{"user@example.com", ""},
		{"noatsign", ""},
	}
	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, emailaddr.GmailAccount(tt.address), "GmailAccount(%q)", tt.address)
		})
	}
}

func TestIsAutomatedMailbox(t *testing.T) {
	t.Parallel()

	tests := []struct {
		address string
		want    bool
	}{
		{"reply+abc123@reply.example.net", true},
		{"reply@reply.example.net", true},
		{"Replies+t1@example.com", true},
		{"bounce+x@example.com", true},
		{"bounces+42-abc@mail.example.org", true},
		{"noreply+alerts@example.com", true},
		{"No-Reply@example.com", true},
		{"do-not-reply+a@example.com", true},
		{"DoNotReply@example.com", true},
		{"notifications+thread@example.com", true},
		{"notification@example.com", true},
		{"MAILER-DAEMON@example.com", true},
		{"postmaster+x@example.com", true},
		{"return+abc@example.com", true},
		{"verp+abc@example.com", true},
		{"no.reply@example.com", true},
		{"pat+news@example.com", false},
		{"replyall+x@example.com", false},
		{"pat.reply@example.com", false},
		{"+reply@example.com", false},
		{"not-an-address", false},
	}
	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, emailaddr.IsAutomatedMailbox(tt.address),
				"IsAutomatedMailbox(%q)", tt.address)
		})
	}
}

func TestIsGmailDomain(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	assert.True(emailaddr.IsGmailDomain("gmail.com"))
	assert.True(emailaddr.IsGmailDomain("GoogleMail.com"))
	assert.False(emailaddr.IsGmailDomain("example.com"))
	assert.False(emailaddr.IsGmailDomain("mail.gmail.com"))
}

func TestEquivalenceString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "same_mailbox", emailaddr.SameMailbox.String())
	assert.Equal(t, "dot_variant", emailaddr.DotVariant.String())
	assert.Equal(t, "different", emailaddr.Different.String())
}
