package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/msgvault/internal/peoplebrowser"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

// untitledConversationRows covers the three title sources: a real title, a
// participant label for an untitled conversation, and neither.
func untitledConversationRows() []query.ConversationRow {
	return []query.ConversationRow{
		{ConversationID: 701, Title: "Weekend plans", MessageCount: 3},
		{ConversationID: 702, ParticipantLabel: "Avery Example, Blake Example", MessageCount: 2},
		{ConversationID: 703, MessageCount: 1},
	}
}

func TestConversationListsNeverLabelByConversationID(t *testing.T) {
	textModel := New(newMockEngine(MockConfig{}), Options{TextEngine: meetingModeTextEngine{}})
	textModel.width, textModel.height, textModel.pageSize = 100, 24, 10
	textModel.loading = false
	textModel.mode = modeTexts
	textModel.textState.level = textLevelConversations
	textModel.textState.conversations = untitledConversationRows()

	contact := inboxTestContact()
	source := query.PersonInboxRow{SourceID: 21, SourceType: "beeper", SourceIdentifier: sourceTypeWhatsApp}
	peopleView := peopleModel(&fakePeopleBackend{})
	peopleView.mode = modePeople
	peopleView.peopleState.level = peopleLevelConversations
	peopleView.peopleState.tab = peopleTabInboxes
	peopleView.peopleState.participantID = contact.ID
	peopleView.peopleState.contact = &contact
	peopleView.peopleState.selectedInboxSource = &source
	peopleView.peopleState.conversations = untitledConversationRows()

	for _, test := range []struct {
		name     string
		rendered string
	}{
		{name: "texts conversation list", rendered: stripANSI(textModel.renderTextView())},
		{name: "people inbox conversations", rendered: strings.Join(peopleView.peopleInboxLines(), "\n")},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Contains(t, test.rendered, "Weekend plans")
			assert.Contains(t, test.rendered, "Avery Example, Blake Example",
				"an untitled conversation is named by its other participants")
			assert.Contains(t, test.rendered, untitledConversationLabel)
			for _, forbidden := range []string{"(conv 70", "Conversation 70", "702", "703"} {
				assert.NotContains(t, test.rendered, forbidden)
			}
		})
	}
}

func TestPeopleAttributesNameRecordReferences(t *testing.T) {
	definition := editablePeopleDefinition("assistant", "Assistant",
		store.AttributeValueRecordReference, store.AttributeFieldPerson, store.AttributeCardinalitySingle)
	recordType := string(store.AttributeObjectPerson)
	named, unnamed := int64(42), int64(43)
	for _, test := range []struct {
		name     string
		recordID int64
		want     string
	}{
		{name: "named person", recordID: named, want: "Jordan Example"},
		{name: "person without a label", recordID: unnamed, want: "Unknown person"},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := peopleAttributesModel(&fakePeopleAttributesBackend{}, peoplebrowser.AttributeGroup{
				Definition: definition,
				Current: []store.PersonAttributeValue{{
					ID: 1, DefinitionSlug: "assistant",
					Value: store.AttributeValue{
						Type: store.AttributeValueRecordReference, RecordType: &recordType, RecordID: &test.recordID,
					},
				}},
			})
			model.peopleState.attributes.RecordLabels = map[int64]string{named: "Jordan Example"}
			rendered := strings.Join(model.peopleAttributesLines(), "\n")
			assert.Contains(t, rendered, test.want)
			assert.NotContains(t, rendered, "—")
		})
	}
}
