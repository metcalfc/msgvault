package correspondentkind

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyDecidesOnlyDecisiveSignals(t *testing.T) {
	bulk := HeaderCounts{Sampled: 5, ListUnsubscribe: 5}
	cases := []struct {
		name    string
		signals Signals
		want    Decision
		decided bool
	}{
		{"provider bot", Signals{ProviderBot: true, Emails: []string{"casey@example.com"}},
			Decision{Automated, ReasonProviderBot}, true},
		{"short code", Signals{Phones: []string{"72975"}}, Decision{Automated, ReasonShortCode}, true},
		{"full phone number", Signals{Phones: []string{"+15555550123"}}, Decision{Person, ReasonPhoneNumber}, true},
		{"short code beside a full number", Signals{Phones: []string{"72975", "+15555550123"}}, Decision{}, false},
		{"full number linked to an email", Signals{Phones: []string{"+15555550123"}, Emails: []string{"casey@example.com"}},
			Decision{}, false},
		{"noreply address", Signals{Emails: []string{"No-Reply+tag@shop.example.com"}},
			Decision{Automated, ReasonNoReplyAddress}, true},
		{"noreply linked to a person", Signals{Emails: []string{"noreply@example.com", "casey@example.com"}},
			Decision{}, false},
		{"list posting address", Signals{Emails: []string{"team@example.com"}, ListIDs: []string{"Team <team.example.com>"}},
			Decision{MailingList, ReasonListAddress}, true},
		{"person posting through a list", Signals{Emails: []string{"casey@example.com"}, ListIDs: []string{"<team.example.com>"},
			Sent: 10, ListIDMessages: 10, Headers: HeaderCounts{Sampled: 3, ListUnsubscribe: 3, ListID: 3}},
			Decision{}, false},
		{"auto generated", Signals{Emails: []string{"billing@example.com"}, Headers: HeaderCounts{Sampled: 3, AutoSubmitted: 2}},
			Decision{Automated, ReasonAutoSubmitted}, true},
		{"bulk sender the owner never wrote to", Signals{Emails: []string{"news@shop.example.com"}, Sent: 8, Headers: bulk},
			Decision{Automated, ReasonBulkUnsubscribe}, true},
		{"bulk sender the owner wrote to", Signals{Emails: []string{"news@shop.example.com"}, Sent: 8, Received: 1, Headers: bulk},
			Decision{}, false},
		{"too few messages", Signals{Emails: []string{"news@shop.example.com"}, Sent: 2, Headers: bulk}, Decision{}, false},
		{"promotions with bulk evidence", Signals{Emails: []string{"deals@shop.example.com"}, Sent: 10,
			Categories: map[string]int64{"CATEGORY_PROMOTIONS": 9}, Headers: HeaderCounts{Sampled: 5, ListUnsubscribe: 2}},
			Decision{Automated, ReasonPromotionsCategory}, true},
		{"person whose mail is mostly Promotions", Signals{Emails: []string{"casey@studio.example.com"}, Sent: 20,
			Categories: map[string]int64{"CATEGORY_PROMOTIONS": 18}, Headers: HeaderCounts{Sampled: 5}},
			Decision{}, false},
		{"promotions from freemail", Signals{Emails: []string{"casey@gmail.com"}, Sent: 10,
			Categories: map[string]int64{"CATEGORY_PROMOTIONS": 10}}, Decision{}, false},
		{"team alias with nothing decisive", Signals{Emails: []string{"team@example.com"}, Sent: 12, Received: 1},
			Decision{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, decided := Classify(tc.signals)
			assert.Equal(t, tc.decided, decided)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestAddHeadersCountsPresenceOnly(t *testing.T) {
	var counts HeaderCounts
	counts.AddHeaders([]byte("From: Shop <deals@shop.example.com>\r\nList-Unsubscribe: <mailto:u@shop.example.com>\r\n" +
		"Precedence: bulk\r\nAuto-Submitted: auto-generated\r\n\r\nList-Id: <not.a.header>\r\n"))
	counts.AddHeaders([]byte("From: Casey <casey@example.com>\r\nAuto-Submitted: auto-replied\r\nList-Id: <team.example.com>\r\n\r\nhello"))
	counts.AddHeaders([]byte("not a header block"))
	assert.Equal(t, HeaderCounts{Sampled: 3, ListUnsubscribe: 1, AutoSubmitted: 1, PrecedenceBulk: 1, ListID: 1}, counts)
}

func TestNormalizeListIDAndShortCodes(t *testing.T) {
	assert := assert.New(t)
	assert.Equal("team.example.com", NormalizeListID(" Team List <Team.Example.com> "))
	assert.Equal("team.example.com", NormalizeListID("team.example.com"))
	assert.True(IsShortCode("262-966"))
	assert.False(IsShortCode("12"))
	assert.False(IsShortCode("+72975"))
	local, domain := SplitEmail("Casey+news@Example.COM")
	assert.Equal("casey", local)
	assert.Equal("example.com", domain)
}

func TestKindPredicates(t *testing.T) {
	assert := assert.New(t)
	assert.True(Unclear.IsPerson(), "an undecided judgment removes no one")
	assert.False(Unclear.Valid(), "users never set unclear")
	assert.True(Unclear.Known())
	assert.True(Unclear.LeavesRankings())
	assert.False(Unclear.LeavesPeopleLists())
	for _, kind := range []Kind{Automated, MailingList} {
		assert.True(kind.Valid())
		assert.True(kind.LeavesPeopleLists())
		assert.False(kind.IsPerson())
	}
	assert.False(SharedMailbox.LeavesRankings(), "shared mailboxes stay as labelled rows")
}
