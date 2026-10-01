package mime

import (
	"strings"

	"go.kenn.io/msgvault/internal/textutil"
)

// NormalizeMessageID returns the canonical form used for RFC822 Message-ID
// comparison and storage. It unwraps one structurally valid angle-bracket pair,
// rejects malformed bracket structure, and makes invalid bytes safe for SQL
// TEXT. It intentionally validates only bracket structure: historical archives
// contain useful bare IDs that do not satisfy the full RFC grammar.
func NormalizeMessageID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if strings.ContainsAny(id, "<>") {
		if len(id) <= 2 || id[0] != '<' || id[len(id)-1] != '>' {
			return ""
		}
		id = id[1 : len(id)-1]
		if strings.TrimSpace(id) != id || strings.ContainsAny(id, "<>") {
			return ""
		}
	}
	return textutil.SanitizeUTF8(id)
}

// ParseMessageIDs extracts canonical message and reply IDs from the top-level
// headers without decoding attachments or accepting header-shaped body text.
func ParseMessageIDs(raw []byte) (messageID, inReplyTo string) {
	headers := tokenizeHeaders(raw)
	return NormalizeMessageID(firstHeader(headers, "message-id")),
		NormalizeMessageID(firstHeader(headers, "in-reply-to"))
}

// MessageIDList extracts angle-bracketed IDs in header order. Bare tokens and
// surrounding comments are ignored, matching the live-sync threading contract.
func MessageIDList(header string) []string {
	var ids []string
	for {
		open := strings.IndexByte(header, '<')
		if open < 0 {
			return ids
		}
		end := strings.IndexByte(header[open+1:], '>')
		if end < 0 {
			return ids
		}
		if id := header[open+1 : open+1+end]; id != "" {
			ids = append(ids, id)
		}
		header = header[open+end+2:]
	}
}
