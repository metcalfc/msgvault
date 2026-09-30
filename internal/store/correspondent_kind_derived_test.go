package store_test

import (
	"bytes"
	"compress/zlib"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

func TestDerivedCorrespondentKindsStayBelowUserDecisionsAndOwners(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := storetest.New(t)
	st := f.Store
	require.NoError(st.AddAccountIdentity(f.Source.ID, "owner@example.com", "manual"))
	owner := f.EnsureParticipant("owner@example.com", "Owner Example", "example.com")
	ownerAlias := f.EnsureParticipant("owner-alias@example.com", "", "example.com")
	_, err := st.LinkParticipants(owner, ownerAlias)
	require.NoError(err)
	alerts := f.EnsureParticipant("alerts@example.com", "Example Alerts", "example.com")
	alertsAlias := f.EnsureParticipant("alerts-eu@example.com", "", "example.com")
	_, err = st.LinkParticipants(alerts, alertsAlias)
	require.NoError(err)
	decided := f.EnsureParticipant("casey@example.com", "Casey Example", "example.com")
	_, err = st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: decided, Kind: correspondentkind.Person,
	})
	require.NoError(err)
	before, err := st.CorrespondentKindRevision()
	require.NoError(err)

	confidence := 0.9
	written, err := st.WriteDerivedCorrespondentKindsContext(t.Context(), []store.DerivedCorrespondentKind{
		{ParticipantID: alertsAlias, Source: correspondentkind.SourceRule, Kind: correspondentkind.Automated, Actor: "rule:noreply_address"},
		{ParticipantID: ownerAlias, Source: correspondentkind.SourceRule, Kind: correspondentkind.Automated},
		{ParticipantID: decided, Source: correspondentkind.SourceJev, Kind: correspondentkind.MailingList,
			Confidence: &confidence, Probabilities: map[string]float64{"mailing_list_or_group": 0.9}},
	})
	require.NoError(err)
	assert.Equal(1, written, "owner clusters and user decisions are never overwritten")
	after, err := st.CorrespondentKindRevision()
	require.NoError(err)
	assert.Equal(before+1, after)

	record, err := st.GetCorrespondentKindContext(t.Context(), alerts)
	require.NoError(err)
	assert.Equal(correspondentkind.Automated, record.Kind)
	assert.Equal([]int64{alerts, alertsAlias}, record.MemberIDs)
	ids, err := st.ParticipantsWithCorrespondentKindContext(t.Context(), correspondentkind.Automated)
	require.NoError(err)
	assert.Equal([]int64{alerts, alertsAlias}, ids)
	_, err = st.ParticipantsWithCorrespondentKindContext(t.Context(), correspondentkind.Person)
	require.ErrorIs(err, store.ErrCorrespondentKindInvalid)

	rows, err := st.CorrespondentKindExportRowsContext(t.Context())
	require.NoError(err)
	assert.Equal([]store.CorrespondentKindExportRow{
		{ParticipantID: alerts, Kind: correspondentkind.Automated, Source: correspondentkind.SourceRule},
		{ParticipantID: alertsAlias, Kind: correspondentkind.Automated, Source: correspondentkind.SourceRule},
		{ParticipantID: decided, Kind: correspondentkind.Person, Source: correspondentkind.SourceUser},
	}, rows)

	// The user overrides the rule; the derived row stays underneath.
	_, err = st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: alerts, Kind: correspondentkind.Person,
	})
	require.NoError(err)
	record, err = st.GetCorrespondentKindContext(t.Context(), alerts)
	require.NoError(err)
	assert.Equal(correspondentkind.Person, record.Kind)
}

func TestDerivedCorrespondentKindsRejectKindsTheirSourceMayNotWrite(t *testing.T) {
	f := storetest.New(t)
	participant := f.EnsureParticipant("desk@example.com", "Desk", "example.com")
	cases := map[string]store.DerivedCorrespondentKind{
		"user source":         {ParticipantID: participant, Source: correspondentkind.SourceUser, Kind: correspondentkind.Automated},
		"organization":        {ParticipantID: participant, Source: correspondentkind.SourceJev, Kind: correspondentkind.Organization},
		"rule unclear":        {ParticipantID: participant, Source: correspondentkind.SourceRule, Kind: correspondentkind.Unclear},
		"unknown kind":        {ParticipantID: participant, Source: correspondentkind.SourceRule, Kind: "robot"},
		"missing participant": {ParticipantID: 0, Source: correspondentkind.SourceRule, Kind: correspondentkind.Automated},
	}
	for name, kind := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.Store.WriteDerivedCorrespondentKindsContext(t.Context(), []store.DerivedCorrespondentKind{kind})
			require.ErrorIs(t, err, store.ErrDerivedCorrespondentKindInvalid)
		})
	}
}

func TestUnclearJudgmentNeverHoldsBackAnOwnerIdentity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := storetest.New(t)
	later := f.EnsureParticipant("later-me@example.com", "Later Me", "example.com")
	_, err := f.Store.WriteDerivedCorrespondentKindsContext(t.Context(), []store.DerivedCorrespondentKind{{
		ParticipantID: later, Source: correspondentkind.SourceJev, Kind: correspondentkind.Unclear,
	}})
	require.NoError(err)
	hidden, err := f.Store.RankingHiddenParticipantsContext(t.Context())
	require.NoError(err)
	assert.Equal(map[int64]correspondentkind.Kind{later: correspondentkind.Unclear}, hidden)

	// The address turns out to be the owner's own.
	require.NoError(f.Store.AddAccountIdentity(f.Source.ID, "later-me@example.com", "manual"))
	hidden, err = f.Store.RankingHiddenParticipantsContext(t.Context())
	require.NoError(err)
	assert.Empty(hidden)
}

// Only a user decision resolves or refuses identity matches; a rule or Jev
// classification changes rankings and enrichment but leaves matching alone.
func TestDerivedKindsNeverSuppressContactMatches(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := newContactMatchFixture(t)
	participant := f.emailParticipant("sam@example.test", "Sam")
	people := f.importCards(f.card("card-sam", "Sam Contact", []string{"sam@example.test"}, nil))
	_, err := f.st.WriteDerivedCorrespondentKindsContext(t.Context(), []store.DerivedCorrespondentKind{{
		ParticipantID: participant, Source: correspondentkind.SourceRule, Kind: correspondentkind.Automated,
	}})
	require.NoError(err)

	matches, err := f.st.FindContactMatchesContext(t.Context())
	require.NoError(err)
	assert.Len(matches, 1, "a rule classification does not hold the match back")
	candidate := f.buildCandidate(people["card-sam"])
	_, _, err = f.st.AcceptIdentityMatchCandidateContext(t.Context(), candidate.ID, "user", nil)
	assert.NotErrorIs(err, store.ErrContactMatchNotAPerson)
}

func TestHeaderSamplingReadsOnlyTheHeaderBlockOfLargeMessages(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	f := storetest.New(t)
	sender := f.EnsureParticipant("news@letters.example.com", "Example Letters", "letters.example.com")
	id := f.CreateMessage("large-news")
	_, err := f.Store.DB().ExecContext(t.Context(), f.Store.Rebind(`UPDATE messages SET sender_id = ? WHERE id = ?`), sender, id)
	require.NoError(err)
	body := make([]byte, 4<<20)
	for i := range body {
		body[i] = byte('a' + i%26)
	}
	raw := append([]byte("From: news@letters.example.com\r\nList-Unsubscribe: <mailto:leave@letters.example.com>\r\n"+
		"Precedence: bulk\r\n\r\n"), body...)
	require.NoError(f.Store.UpsertMessageRaw(id, raw))

	evidence, err := f.Store.CorrespondentKindEvidenceContext(t.Context(), []int64{sender},
		store.CorrespondentKindEvidenceOptions{HeaderSample: 5})
	require.NoError(err)
	assert.Equal(correspondentkind.HeaderCounts{Sampled: 1, ListUnsubscribe: 1, PrecedenceBulk: 1}, evidence.Headers)
}

// A sample counts as header evidence only when the header block's closing
// blank line was decoded; anything else is a failed sample with no signals.
func TestHeaderSamplingRejectsHeadersWithoutTheirTerminator(t *testing.T) {
	require := require.New(t)
	f := storetest.New(t)
	sender := f.EnsureParticipant("alerts@example.com", "Example Alerts", "example.com")
	headers := "From: alerts@example.com\r\nList-Unsubscribe: <mailto:leave@example.com>\r\nPrecedence: bulk\r\n"
	save := func(sourceMessageID string, raw []byte) int64 {
		id := f.CreateMessage(sourceMessageID)
		_, err := f.Store.DB().ExecContext(t.Context(), f.Store.Rebind(`UPDATE messages SET sender_id = ? WHERE id = ?`), sender, id)
		require.NoError(err)
		require.NoError(f.Store.UpsertMessageRaw(id, raw))
		return id
	}
	save("unterminated", []byte(headers))
	corrupt := save("corrupt", append([]byte(headers+"\r\n"), make([]byte, 64<<10)...))
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	_, err := writer.Write([]byte(headers))
	require.NoError(err)
	require.NoError(writer.Close())
	// Keep the decodable start of the headers and corrupt the rest.
	damaged := append(compressed.Bytes()[:len(compressed.Bytes())/2], 0xff, 0x00, 0xff, 0x13, 0x37)
	_, err = f.Store.DB().ExecContext(t.Context(), f.Store.Rebind(
		`UPDATE message_raw SET raw_data = ?, compression = 'zlib' WHERE message_id = ?`), damaged, corrupt)
	require.NoError(err)

	evidence, err := f.Store.CorrespondentKindEvidenceContext(t.Context(), []int64{sender},
		store.CorrespondentKindEvidenceOptions{HeaderSample: 5})
	require.NoError(err)
	assert.Equal(t, correspondentkind.HeaderCounts{Sampled: 2}, evidence.Headers)
}

// headerSample stores one raw message from a fresh sender, lets fix adjust
// the stored row, and returns the sender's sampled header counts.
func headerSample(t *testing.T, raw []byte, fix func(f *storetest.Fixture, messageID int64)) correspondentkind.HeaderCounts {
	t.Helper()
	require := require.New(t)
	f := storetest.New(t)
	sender := f.EnsureParticipant("alerts@example.com", "Example Alerts", "example.com")
	id := f.CreateMessage("sample")
	_, err := f.Store.DB().ExecContext(t.Context(), f.Store.Rebind(`UPDATE messages SET sender_id = ? WHERE id = ?`), sender, id)
	require.NoError(err)
	require.NoError(f.Store.UpsertMessageRaw(id, raw))
	fix(f, id)
	evidence, err := f.Store.CorrespondentKindEvidenceContext(t.Context(), []int64{sender},
		store.CorrespondentKindEvidenceOptions{HeaderSample: 5})
	require.NoError(err)
	return evidence.Headers
}

func TestHeaderSamplingReadsAnUncompressedHeaderBlockJustUnderTheCap(t *testing.T) {
	var header bytes.Buffer
	header.WriteString("From: alerts@example.com\r\nList-Unsubscribe: <mailto:leave@example.com>\r\n")
	for header.Len() < 63<<10 {
		header.WriteString("X-Padding: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\r\n")
	}
	raw := append(header.Bytes(), []byte("\r\n")...)
	raw = append(raw, make([]byte, 128<<10)...)
	counts := headerSample(t, raw, func(f *storetest.Fixture, id int64) {
		_, err := f.Store.DB().ExecContext(t.Context(), f.Store.Rebind(
			`UPDATE message_raw SET raw_data = ?, compression = 'none' WHERE message_id = ?`), raw, id)
		require.NoError(t, err)
	})
	assert.Equal(t, correspondentkind.HeaderCounts{Sampled: 1, ListUnsubscribe: 1}, counts)
}

func TestHeaderSamplingRejectsAShortMessageWithABadChecksum(t *testing.T) {
	raw := []byte("From: alerts@example.com\r\nList-Unsubscribe: <mailto:leave@example.com>\r\n\r\nHello.\r\n")
	counts := headerSample(t, raw, func(f *storetest.Fixture, id int64) {
		var compressed bytes.Buffer
		writer := zlib.NewWriter(&compressed)
		_, err := writer.Write(raw)
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		damaged := compressed.Bytes()
		damaged[len(damaged)-1] ^= 0xff // the trailing Adler-32 checksum
		_, err = f.Store.DB().ExecContext(t.Context(), f.Store.Rebind(
			`UPDATE message_raw SET raw_data = ?, compression = 'zlib' WHERE message_id = ?`), damaged, id)
		require.NoError(t, err)
	})
	assert.Equal(t, correspondentkind.HeaderCounts{Sampled: 1}, counts, "a decoder error yields no signals")
}
