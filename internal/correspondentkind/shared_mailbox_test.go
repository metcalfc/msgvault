package correspondentkind_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/msgvault/internal/correspondentkind"
)

func TestDetectSharedMailbox(t *testing.T) {
	role := correspondentkind.ReasonRoleAddress
	several := correspondentkind.ReasonSeveralNames
	tests := []struct {
		name    string
		address string
		names   []string
		reasons []correspondentkind.SharedMailboxReason
		people  []string
	}{
		{
			name: "two agents on one support desk", address: "support@shop.example.test",
			names:   []string{"Avery Stone", "Blake Rivera", "avery stone"},
			reasons: []correspondentkind.SharedMailboxReason{role, several},
			people:  []string{"Avery Stone", "Blake Rivera"},
		},
		{
			name: "two people on a personal-looking address", address: "desk@example.test",
			names:   []string{"Casey Morgan", "Drew Park"},
			reasons: []correspondentkind.SharedMailboxReason{several},
			people:  []string{"Casey Morgan", "Drew Park"},
		},
		{
			name: "case, quotes, and via are one person", address: "erin@example.test",
			names:   []string{"Erin Walsh", `"erin walsh"`, "Erin Walsh via Example Docs", "'ERIN WALSH'"},
			reasons: []correspondentkind.SharedMailboxReason{},
		},
		{
			name: "name order and initials are one person", address: "frank@example.test",
			names:   []string{"Frank Lee", "Lee, Frank", "Frank L.", "F. Lee", "Frank", "Franklin Lee"},
			reasons: []correspondentkind.SharedMailboxReason{},
		},
		{
			name: "a parenthetical organization tag is dropped", address: "gia@example.test",
			names:   []string{"Gia Romano (Example Co)", "Gia Romano [External]"},
			reasons: []correspondentkind.SharedMailboxReason{},
		},
		{
			name: "addresses and role words are not people's names", address: "hana@example.test",
			names:   []string{"hana@example.test", "Hana Ito", "Support Team", "hana"},
			reasons: []correspondentkind.SharedMailboxReason{},
		},
		{
			name: "a role address alone fires", address: "No-Reply+alerts@Example.test",
			names:   nil,
			reasons: []correspondentkind.SharedMailboxReason{role},
		},
		{
			name: "every role local part", address: "receipts@example.test",
			reasons: []correspondentkind.SharedMailboxReason{role},
		},
		{
			name: "a role word inside a longer local part is not a role address", address: "supportive.sam@example.test",
			names:   []string{"Sam Supportive"},
			reasons: []correspondentkind.SharedMailboxReason{},
		},
		{
			name: "a blank address and names", address: "", names: []string{"", "  "},
			reasons: []correspondentkind.SharedMailboxReason{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			signal := correspondentkind.DetectSharedMailbox(test.address, test.names)
			assert.Equal(t, test.reasons, signal.Reasons)
			assert.Equal(t, len(test.reasons) > 0, signal.Fires())
			assert.Equal(t, test.people, signal.Names)
		})
	}
}

func TestDetectSharedMailboxJudgesNameGroupsSeparately(t *testing.T) {
	messageNames := []string{"Jordan Blake"}
	cardNames := []string{"Grandpa"}
	signal := correspondentkind.DetectSharedMailbox("jordan@example.test", messageNames, cardNames)
	assert.False(t, signal.Fires(), "a card's nickname is not a second person")

	cardNames = []string{"Kai Mercer", "Lena Ortiz"}
	signal = correspondentkind.DetectSharedMailbox("desk@example.test", messageNames, cardNames)
	assert.Equal(t, []correspondentkind.SharedMailboxReason{correspondentkind.ReasonSeveralNames}, signal.Reasons)
	assert.Equal(t, []string{"Kai Mercer", "Lena Ortiz"}, signal.Names)
}

func TestDetectSharedMailboxWithoutNames(t *testing.T) {
	tests := []struct {
		name    string
		address string
		names   []string
		reasons []correspondentkind.SharedMailboxReason
		people  []string
	}{
		{name: "personal address", address: "mia@example.test", reasons: []correspondentkind.SharedMailboxReason{}},
		{name: "role address", address: "billing@example.test",
			reasons: []correspondentkind.SharedMailboxReason{correspondentkind.ReasonRoleAddress}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			signal := correspondentkind.DetectSharedMailbox(test.address, test.names)
			assert.Equal(t, test.reasons, signal.Reasons)
			assert.Equal(t, len(test.reasons) > 0, signal.Fires())
			assert.Equal(t, test.people, signal.Names)
		})
	}
}

func TestIsRoleAddressCoversEveryRoleLocalPart(t *testing.T) {
	for _, local := range correspondentkind.RoleLocalParts {
		assert.True(t, correspondentkind.IsRoleAddress(local+"@example.test"), local)
		assert.True(t, correspondentkind.IsRoleAddress(local+"+tag@example.test"), local)
	}
	assert.False(t, correspondentkind.IsRoleAddress("support"))
	assert.False(t, correspondentkind.IsRoleAddress("@example.test"))
}
