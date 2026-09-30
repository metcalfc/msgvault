package meetingjudge

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAttendeeLabelNeverCarriesAnIdentifier(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"Casey Example", "Casey Example"},
		{"casey@example.com", "casey"},
		{"Casey <casey@example.com>", "Casey"},
		{`"Casey Example" <casey@example.com>`, "Casey Example"},
		{"<casey@example.com>", "attendee 3"},
		{"+1 (555) 010-0199", "attendee 3"},
		{"Casey +1 555 010 0199", "Casey"},
		{"Casey (casey@example.com)", "Casey"},
		{"Team 42", "Team 42"},
		{"Casey <casey at example dot com>", "Casey"},
		{"casey%40example.com", "casey"},
		{"Casey tel:+44‑20‑7946‑0958", "Casey"},
		{"", "attendee 3"},
		{"   ", "attendee 3"},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			assert.Equal(t, test.want, AttendeeLabel(test.raw, 2))
		})
	}
}

func TestRedactTextRemovesAddressesAndPhoneNumbers(t *testing.T) {
	tests := []struct {
		text string
		want string
	}{
		{"Sync with casey@example.com", "Sync with [email]"},
		{"Call +1 (555) 010-0199 at 3", "Call [phone] at 3"},
		{"Dial 555.010.0199 then 555-0100", "Dial [phone] then [phone]"},
		{"Room 4012, 30 people", "Room 4012, 30 people"},
		{"Review 5/4/2026", "Review 5/4/2026"},
		{"Ask alex at example dot com", "Ask [email]"},
		{"Ask alex [at] example [dot] com", "Ask [email]"},
		{"Ask alex(at)example", "Ask [email]"},
		{"Meet at noon", "Meet at noon"},
		{"Write mailto:alex%40example.com", "Write [email]"},
		{"Call tel:+44‑20‑7946‑0958", "Call [phone]"},
		{"Call +44 20 7946 0958", "Call [phone]"},
		{"2026-09-30 020 7946 0958", "2026-09-30 [phone]"},
		{"Release 1.2.3 and 123.456.789", "Release 1.2.3 and 123.456.789"},
		{"meeting ID 123 456 7890", "meeting ID [phone]"},
		{"Order 1234567890123456", "Order [phone]"},
		{"Call 555-010-0199 / 555-010-0200", "Call [phone] / [phone]"},
		{"Call 555-010-0199 555-010-0200", "Call [phone]"},
		{"Compare x² and x2", "Compare x² and x2"},
		{"Keep this‑dash", "Keep this‑dash"},
		{"Ｃａｌｌ ５５５ ０１０ ０１９９", "\uff23\uff41\uff4c\uff4c [phone]"},
		{"Q3 planning 2026-05-04 10:00", "Q3 planning 2026-05-04 10:00"},
		{"Weekly  sync", "Weekly  sync"},
	}
	for _, test := range tests {
		t.Run(test.text, func(t *testing.T) {
			assert.Equal(t, test.want, RedactText(test.text))
		})
	}
}
