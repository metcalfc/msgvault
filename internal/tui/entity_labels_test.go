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
			assert := assert.New(t)
			assert.Contains(test.rendered, "Weekend plans")
			assert.Contains(test.rendered, "Avery Example, Blake Example",
				"an untitled conversation is named by its other participants")
			assert.Contains(test.rendered, untitledConversationLabel)
			for _, forbidden := range []string{"(conv 70", "Conversation 70", "702", "703"} {
				assert.NotContains(test.rendered, forbidden)
			}
		})
	}
}

func TestPeopleAttributesNameRecordReferences(t *testing.T) {
	personType := string(store.AttributeObjectPerson)
	organizationType := string(store.AttributeObjectOrganization)
	named, unnamed := int64(42), int64(43)
	for _, test := range []struct {
		name       string
		fieldType  store.AttributeFieldType
		recordType string
		recordID   int64
		want       string
		notWant    string
	}{
		{name: "named person", fieldType: store.AttributeFieldPerson, recordType: personType,
			recordID: named, want: "Jordan Example", notWant: "Example Works"},
		{name: "person without a label", fieldType: store.AttributeFieldPerson, recordType: personType,
			recordID: unnamed, want: "Unknown person"},
		{name: "named organization sharing a person's ID", fieldType: store.AttributeFieldOrganization,
			recordType: organizationType, recordID: named, want: "Example Works", notWant: "Jordan Example"},
		{name: "organization without a label", fieldType: store.AttributeFieldOrganization,
			recordType: organizationType, recordID: unnamed, want: "Unknown organization", notWant: "Unknown person"},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := editablePeopleDefinition("assistant", "Assistant",
				store.AttributeValueRecordReference, test.fieldType, store.AttributeCardinalitySingle)
			model := peopleAttributesModel(&fakePeopleAttributesBackend{}, peoplebrowser.AttributeGroup{
				Definition: definition,
				Current: []store.PersonAttributeValue{{
					ID: 1, DefinitionSlug: "assistant",
					Value: store.AttributeValue{
						Type: store.AttributeValueRecordReference, RecordType: &test.recordType, RecordID: &test.recordID,
					},
				}},
			})
			model.peopleState.attributes.RecordLabels = map[int64]string{named: "Jordan Example"}
			model.peopleState.attributes.RecordOrganizationLabels = map[int64]string{named: "Example Works"}
			rendered := strings.Join(model.peopleAttributesLines(), "\n")
			assert := assert.New(t)
			assert.Contains(rendered, test.want)
			assert.NotContains(rendered, "—")
			if test.notWant != "" {
				assert.NotContains(rendered, test.notWant)
			}
		})
	}
}
