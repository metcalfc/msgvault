package correspondentkind

import (
	"bufio"
	"bytes"
	"net/textproto"
	"strings"
)

// maxHeaderBytes bounds how much of a raw message is read to find its
// header block. The body is never read.
const maxHeaderBytes = 256 << 10

// AddHeaders records which classification headers one raw MIME message
// carries. It reads only the header block, stops at the first blank line,
// and keeps presence, never values. A message whose header block cannot be
// parsed still counts as sampled with no headers.
func (h *HeaderCounts) AddHeaders(raw []byte) {
	h.Sampled++
	if len(raw) > maxHeaderBytes {
		raw = raw[:maxHeaderBytes]
	}
	header, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(raw))).ReadMIMEHeader()
	if err != nil && len(header) == 0 {
		return
	}
	if strings.TrimSpace(header.Get("List-Unsubscribe")) != "" {
		h.ListUnsubscribe++
	}
	if strings.TrimSpace(header.Get("List-Id")) != "" {
		h.ListID++
	}
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(header.Get("Auto-Submitted"), ";", 2)[0])) {
	case "auto-generated", "auto-notified":
		h.AutoSubmitted++
	}
	switch strings.ToLower(strings.TrimSpace(header.Get("Precedence"))) {
	case "bulk", "list", "junk":
		h.PrecedenceBulk++
	}
}
