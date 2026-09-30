package rerank

import (
	"strings"
	"time"
	"unicode/utf8"

	"go.kenn.io/msgvault/internal/vector/embed"
)

// Header field bounds inside one candidate. The body fills what remains of
// MaxCandidateBytes, so the whole candidate never exceeds it.
const (
	maxSubjectBytes = 300
	maxFromBytes    = 200
)

// Message is what one reranking candidate is built from: the message's
// subject, sender, date, and stored body. Nothing else is read.
type Message struct {
	Subject     string
	FromName    string
	FromEmail   string
	SentAt      time.Time
	MessageType string
	BodyText    string
	BodyHTML    string
}

// Sender is the From line: the sender's display name, or the address when
// there is no name. Phone numbers are never used.
func (m Message) Sender() string {
	if name := strings.TrimSpace(m.FromName); name != "" {
		return name
	}
	return strings.TrimSpace(m.FromEmail)
}

// Candidate renders one message as the text Jev judges: a Subject, From,
// and Date header followed by the body after the same cleaning the
// embedding pipeline applies (quotes, signatures, HTML, base64, and tracking
// parameters removed per preprocess). The result is at most
// MaxCandidateBytes and always valid UTF-8, so the body is under 2 KiB.
func Candidate(message Message, preprocess embed.PreprocessConfig) string {
	var header strings.Builder
	header.WriteString("Subject: ")
	header.WriteString(TruncateUTF8Bytes(singleLine(message.Subject), maxSubjectBytes))
	header.WriteString("\nFrom: ")
	header.WriteString(TruncateUTF8Bytes(singleLine(message.Sender()), maxFromBytes))
	header.WriteString("\nDate: ")
	if !message.SentAt.IsZero() {
		header.WriteString(message.SentAt.UTC().Format(time.DateOnly))
	}
	header.WriteString("\n\n")
	body := embed.HydrationBodyText(message.MessageType, message.BodyText, message.BodyHTML)
	cleaned, _ := embed.Preprocess("", body, 0, preprocess)
	return TruncateUTF8Bytes(header.String()+cleaned, MaxCandidateBytes)
}

// TruncateUTF8Bytes cuts value to at most limit bytes on a rune boundary.
func TruncateUTF8Bytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func singleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
