package query_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/search"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

// Subject case matching must also work on the production engine with FTS available.
func TestQueryEngine_CaseInsensitiveSearch_Subject(t *testing.T) {
	must := require.New(t)
	f := storetest.New(t)
	wanted := f.NewMessage().WithSourceMessageID("subject-case-match").
		WithSubject("Quarterly Invoice").Create(t, f.Store)
	f.NewMessage().WithSourceMessageID("subject-case-decoy").
		WithSubject("Unrelated meeting").Create(t, f.Store)
	_, err := f.Store.BackfillFTS(nil)
	must.NoError(err)
	engine := query.NewEngine(f.Store.DB())

	for _, term := range []string{"invoice", "INVOICE", "Invoice"} {
		t.Run(term, func(t *testing.T) {
			got, err := engine.Search(t.Context(), &search.Query{SubjectTerms: []string{term}}, 50, 0)
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, wanted, got[0].ID)
		})
	}
}

// Two authors matching a sender filter must not multiply a message or its count.
func TestQueryEngine_MultiFromNoDuplication(t *testing.T) {
	must := require.New(t)
	f := storetest.New(t)
	const domain, name = "dup.example", "Shared Sender"
	first := f.EnsureParticipant("first@dup.example", name, domain)
	second := f.EnsureParticipant("second@dup.example", name, domain)
	other := f.EnsureParticipant("other@other.example", "Other", "other.example")
	sent := time.Date(2024, 7, 1, 9, 0, 0, 0, time.UTC)
	wanted := f.NewMessage().WithSourceMessageID("multi-from").
		WithSubject("Two authors").WithSentAt(sent).Create(t, f.Store)
	must.NoError(f.Store.ReplaceMessageRecipients(wanted, "from",
		[]int64{first, second}, []string{name, name}))
	decoy := f.NewMessage().WithSourceMessageID("single-from").
		WithSubject("Other author").WithSentAt(sent).Create(t, f.Store)
	must.NoError(f.Store.ReplaceMessageRecipients(decoy, "from", []int64{other}, []string{"Other"}))
	engine := query.NewEngine(f.Store.DB())

	for _, tc := range []struct {
		name   string
		filter query.MessageFilter
	}{
		{"domain", query.MessageFilter{Domain: domain}},
		{"sender_name", query.MessageFilter{SenderName: name}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := engine.ListMessages(t.Context(), tc.filter)
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, wanted, got[0].ID)
		})
	}

	// Year grouping is single-valued, isolating any duplication from the filter.
	rows, err := engine.SubAggregate(t.Context(), query.MessageFilter{Domain: domain},
		query.ViewTime, query.AggregateOptions{
			TimeGranularity: query.TimeYear,
			SortField:       query.SortByCount,
			SortDirection:   query.SortDesc,
			Limit:           50,
		})
	must.NoError(err)
	must.Len(rows, 1)
	assert.Equal(t, int64(1), rows[0].Count)
}
