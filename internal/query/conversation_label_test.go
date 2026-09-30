package query

import (
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/testutil/dbtest"
)

// conversationLabelFixture seeds untitled conversations whose labels
// exercise the policy: a curated person name beats the participant's own
// name, an unnamed participant falls back to its address, the owner's
// messages exclude them, extra names collapse into "+N", a conversation
// without membership rows uses its senders, nothing names a conversation
// whose only member has neither name nor identifier, and an owner who is a
// member but never sent (matched by account identity email or by a
// non-email identifier) is still excluded.
func conversationLabelFixture(t *testing.T) *sql.DB {
	t.Helper()
	tdb := dbtest.NewTestDB(t, "../store/schema.sql")
	_, err := tdb.DB.Exec(`
		INSERT INTO sources (id, source_type, identifier) VALUES (7, 'whatsapp', 'owner@example.com');
		INSERT INTO participants (id, phone_number, email_address, display_name) VALUES
			(11, '+15550000011', NULL, 'Avery Observed'),
			(12, NULL, 'blake@example.com', NULL),
			(13, '+15550000013', NULL, 'Owner Example'),
			(14, NULL, NULL, 'Casey Example'),
			(15, NULL, NULL, 'Drew Example'),
			(16, NULL, NULL, 'Emery Example'),
			(17, NULL, NULL, NULL),
			(18, NULL, 'Owner@Example.com', 'Owner Mailbox'),
			(19, NULL, NULL, 'Owner Phone');
		INSERT INTO participant_identifiers (participant_id, identifier_type, identifier_value) VALUES
			(19, 'phone', '+15550000099');
		INSERT INTO account_identities (source_id, address) VALUES
			(7, 'owner@example.com'), (7, '+15550000099');
		INSERT INTO persons (id, vcard_uid, display_name) VALUES (1, 'synthetic-uid-1', 'Avery Curated');
		INSERT INTO person_participants (person_id, participant_id) VALUES (1, 11);
		INSERT INTO conversations (id, source_id, source_conversation_id, conversation_type, title) VALUES
			(701, 7, 'c-701', 'direct_chat', NULL),
			(702, 7, 'c-702', 'group_chat', ''),
			(703, 7, 'c-703', 'direct_chat', NULL),
			(704, 7, 'c-704', 'group_chat', 'Named chat'),
			(705, 7, 'c-705', 'direct_chat', NULL),
			(706, 7, 'c-706', 'group_chat', NULL);
		INSERT INTO messages (id, conversation_id, source_id, source_message_id, message_type, sent_at, snippet, sender_id, is_from_me) VALUES
			(801, 701, 7, 'm-801', 'whatsapp', '2026-08-20 10:00:00', 'hi', 11, FALSE),
			(802, 701, 7, 'm-802', 'whatsapp', '2026-08-20 10:01:00', 'hello', 13, TRUE),
			(803, 702, 7, 'm-803', 'whatsapp', '2026-08-20 11:00:00', 'group', 14, FALSE),
			(804, 703, 7, 'm-804', 'whatsapp', '2026-08-20 12:00:00', 'direct', 14, FALSE),
			(805, 703, 7, 'm-805', 'whatsapp', '2026-08-20 12:01:00', 'reply', 13, TRUE),
			(806, 704, 7, 'm-806', 'whatsapp', '2026-08-20 13:00:00', 'titled', 14, FALSE),
			(807, 705, 7, 'm-807', 'whatsapp', '2026-08-20 14:00:00', 'anonymous', 17, FALSE),
			(808, 706, 7, 'm-808', 'whatsapp', '2026-08-20 15:00:00', 'owner silent', 15, FALSE);
		INSERT INTO conversation_participants (conversation_id, participant_id) VALUES
			(701, 11), (701, 12), (701, 13),
			(702, 11), (702, 12), (702, 14), (702, 15), (702, 16),
			(704, 14),
			(705, 17),
			(706, 18), (706, 19), (706, 15);
	`)
	require.NoError(t, err)
	return tdb.DB
}

var wantConversationLabels = map[int64]string{
	701: "Avery Curated, blake@example.com",
	702: "Avery Curated, blake@example.com, Casey Example +2",
	703: "Casey Example",
	704: "",
	705: "",
	706: "Drew Example",
}

func conversationLabelsByID(rows []ConversationRow) map[int64]string {
	labels := make(map[int64]string, len(rows))
	for _, row := range rows {
		labels[row.ConversationID] = row.ParticipantLabel
	}
	return labels
}

func TestSQLiteConversationParticipantLabels(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	db := conversationLabelFixture(t)
	engine := NewSQLiteEngine(db)
	sourceID := int64(7)
	filter := TextFilter{SourceID: &sourceID}

	listed, err := engine.ListConversations(t.Context(), filter)
	require.NoError(err)
	assert.Equal(wantConversationLabels, conversationLabelsByID(listed))

	snapshot, _, err := engine.ListConversationsSnapshot(t.Context(), filter)
	require.NoError(err)
	assert.Equal(wantConversationLabels, conversationLabelsByID(snapshot))
}

// TestConversationLabelNamesANotAPersonParticipantByItsOwnName pins that a
// participant marked as not a person (here a shared mailbox still bound to
// a curated person) reads as its own name, not that person's.
func TestConversationLabelNamesANotAPersonParticipantByItsOwnName(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	engine := NewSQLiteEngine(conversationLabelFixture(t))
	sourceID := int64(7)
	ctx := WithNotPersonParticipants(t.Context(), map[int64]correspondentkind.Kind{
		11: correspondentkind.SharedMailbox,
	})

	listed, err := engine.ListConversations(ctx, TextFilter{SourceID: &sourceID})
	require.NoError(err)
	labels := conversationLabelsByID(listed)
	assert.Equal("Avery Observed, blake@example.com", labels[701])
	assert.Equal("Avery Observed, blake@example.com, Casey Example +2", labels[702])
}

func TestDuckDBConversationParticipantLabelsComeFromTheArchive(t *testing.T) {
	db := conversationLabelFixture(t)
	b := NewTestDataBuilder(t)
	sourceID := b.AddSourceWithType("owner@example.com", "whatsapp")
	for _, conversationID := range []int64{701, 702, 703, 704, 705, 706} {
		title := ""
		if conversationID == 704 {
			title = "Named chat"
		}
		b.AddMessage(MessageOpt{
			SourceID: sourceID, ConversationID: conversationID, MessageType: "whatsapp",
			ConversationType: "direct_chat", ConversationTitle: title,
		})
	}
	analyticsDir, cleanup := b.Build()
	t.Cleanup(cleanup)
	engine, err := NewDuckDBEngine(analyticsDir, "", db)
	require.NoError(t, err)
	t.Cleanup(func() { _ = engine.Close() })

	rows, err := engine.ListConversations(t.Context(), TextFilter{})
	require.NoError(t, err)
	assert.Equal(t, wantConversationLabels, conversationLabelsByID(rows))
}

func TestConversationParticipantLabelSummarizesExtraNames(t *testing.T) {
	for _, test := range []struct {
		names []string
		want  string
	}{
		{names: nil, want: ""},
		{names: []string{"Avery"}, want: "Avery"},
		{names: []string{"Avery", "Avery", "Blake"}, want: "Avery, Blake"},
		{names: []string{"Avery", "Blake", "Casey", "Drew"}, want: "Avery, Blake, Casey +1"},
	} {
		assert.Equal(t, test.want, conversationParticipantLabel(test.names))
	}
}

// TestConversationLabelQueryUsesIndexes pins that the label query reads
// messages through idx_messages_conversation and membership through its
// primary key, rather than scanning either table.
func TestConversationLabelQueryUsesIndexes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	db := conversationLabelFixture(t)
	rows, err := db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+sqlConversationLabelQuery(2), int64(701), int64(703))
	require.NoError(err)
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(rows.Scan(&id, &parent, &unused, &detail))
		plan = append(plan, detail)
	}
	require.NoError(rows.Err())
	joined := strings.Join(plan, "\n")
	assert.Contains(joined, "idx_messages_conversation (conversation_id=? AND sent_at>?)",
		"recent messages are an index range scan")
	assert.Contains(joined, "sqlite_autoindex_conversation_participants_1")
	for _, line := range plan {
		assert.NotRegexp(`^SCAN (m|lm|messages)\b`, line, "messages must never be fully scanned")
		assert.NotRegexp(`^SCAN (cp|cpx)\b`, line, "membership must be read by key")
	}
}

// TestConversationSenderFallbackReadsOnlyRecentMessages pins the bound on
// the sender fallback: a sender older than the most recent
// conversationLabelRecentMessages messages is not read.
func TestConversationSenderFallbackReadsOnlyRecentMessages(t *testing.T) {
	require := require.New(t)
	db := conversationLabelFixture(t)
	_, err := db.Exec(`
		INSERT INTO conversations (id, source_id, source_conversation_id, conversation_type, title)
			VALUES (707, 7, 'c-707', 'direct_chat', NULL);
		INSERT INTO messages (id, conversation_id, source_id, source_message_id, message_type, sent_at, snippet, sender_id)
			VALUES (900, 707, 7, 'm-900', 'whatsapp', '2026-01-01 00:00:00', 'old', 16)`)
	require.NoError(err)
	for i := range conversationLabelRecentMessages {
		_, err = db.Exec(`INSERT INTO messages (id, conversation_id, source_id, source_message_id, message_type, sent_at, snippet, sender_id)
			VALUES (?, 707, 7, ?, 'whatsapp', ?, 'recent', 14)`,
			901+i, "m-recent-"+strconv.Itoa(i), time.Date(2026, 9, 1, 0, i, 0, 0, time.UTC).Format("2006-01-02 15:04:05"))
		require.NoError(err)
	}
	rows := []ConversationRow{{ConversationID: 707}}
	require.NoError(NewSQLiteEngine(db).fillConversationParticipantLabels(t.Context(), rows))
	assert.Equal(t, "Casey Example", rows[0].ParticipantLabel)
}

// TestConversationLabelWindowIgnoresUndatedMessages pins that undated
// messages never occupy the recent window: a conversation with more undated
// messages than the window still reads its dated senders.
func TestConversationLabelWindowIgnoresUndatedMessages(t *testing.T) {
	require := require.New(t)
	db := conversationLabelFixture(t)
	_, err := db.Exec(`
		INSERT INTO conversations (id, source_id, source_conversation_id, conversation_type, title)
			VALUES (708, 7, 'c-708', 'direct_chat', NULL);
		INSERT INTO messages (id, conversation_id, source_id, source_message_id, message_type, sent_at, snippet, sender_id)
			VALUES (1000, 708, 7, 'm-1000', 'whatsapp', '2026-09-01 00:00:00', 'dated', 14)`)
	require.NoError(err)
	for i := range conversationLabelRecentMessages + 10 {
		_, err = db.Exec(`INSERT INTO messages (id, conversation_id, source_id, source_message_id, message_type, sent_at, snippet, sender_id)
			VALUES (?, 708, 7, ?, 'whatsapp', NULL, 'undated', 16)`, 1001+i, "m-undated-"+strconv.Itoa(i))
		require.NoError(err)
	}
	rows := []ConversationRow{{ConversationID: 708}}
	require.NoError(NewSQLiteEngine(db).fillConversationParticipantLabels(t.Context(), rows))
	assert.Equal(t, "Casey Example", rows[0].ParticipantLabel)
}
