package query

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/testutil/dbtest"
)

// conversationLabelFixture seeds untitled conversations whose labels
// exercise the policy: a curated person name beats the participant's own
// name, an unnamed participant falls back to its address, the owner's
// messages exclude them, extra names collapse into "+N", a conversation
// without membership rows uses its senders, and nothing names a
// conversation whose only member has neither name nor identifier.
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
			(17, NULL, NULL, NULL);
		INSERT INTO persons (id, vcard_uid, display_name) VALUES (1, 'synthetic-uid-1', 'Avery Curated');
		INSERT INTO person_participants (person_id, participant_id) VALUES (1, 11);
		INSERT INTO conversations (id, source_id, source_conversation_id, conversation_type, title) VALUES
			(701, 7, 'c-701', 'direct_chat', NULL),
			(702, 7, 'c-702', 'group_chat', ''),
			(703, 7, 'c-703', 'direct_chat', NULL),
			(704, 7, 'c-704', 'group_chat', 'Named chat'),
			(705, 7, 'c-705', 'direct_chat', NULL);
		INSERT INTO messages (id, conversation_id, source_id, source_message_id, message_type, sent_at, snippet, sender_id, is_from_me) VALUES
			(801, 701, 7, 'm-801', 'whatsapp', '2026-08-20 10:00:00', 'hi', 11, FALSE),
			(802, 701, 7, 'm-802', 'whatsapp', '2026-08-20 10:01:00', 'hello', 13, TRUE),
			(803, 702, 7, 'm-803', 'whatsapp', '2026-08-20 11:00:00', 'group', 14, FALSE),
			(804, 703, 7, 'm-804', 'whatsapp', '2026-08-20 12:00:00', 'direct', 14, FALSE),
			(805, 703, 7, 'm-805', 'whatsapp', '2026-08-20 12:01:00', 'reply', 13, TRUE),
			(806, 704, 7, 'm-806', 'whatsapp', '2026-08-20 13:00:00', 'titled', 14, FALSE),
			(807, 705, 7, 'm-807', 'whatsapp', '2026-08-20 14:00:00', 'anonymous', 17, FALSE);
		INSERT INTO conversation_participants (conversation_id, participant_id) VALUES
			(701, 11), (701, 12), (701, 13),
			(702, 11), (702, 12), (702, 14), (702, 15), (702, 16),
			(704, 14),
			(705, 17);
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
}

func conversationLabelsByID(rows []ConversationRow) map[int64]string {
	labels := make(map[int64]string, len(rows))
	for _, row := range rows {
		labels[row.ConversationID] = row.ParticipantLabel
	}
	return labels
}

func TestSQLiteConversationParticipantLabels(t *testing.T) {
	db := conversationLabelFixture(t)
	engine := NewSQLiteEngine(db)
	sourceID := int64(7)
	filter := TextFilter{SourceID: &sourceID}

	listed, err := engine.ListConversations(t.Context(), filter)
	require.NoError(t, err)
	assert.Equal(t, wantConversationLabels, conversationLabelsByID(listed))

	snapshot, _, err := engine.ListConversationsSnapshot(t.Context(), filter)
	require.NoError(t, err)
	assert.Equal(t, wantConversationLabels, conversationLabelsByID(snapshot))
}

func TestDuckDBConversationParticipantLabelsComeFromTheArchive(t *testing.T) {
	db := conversationLabelFixture(t)
	b := NewTestDataBuilder(t)
	sourceID := b.AddSourceWithType("owner@example.com", "whatsapp")
	for _, conversationID := range []int64{701, 702, 703, 704, 705} {
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
