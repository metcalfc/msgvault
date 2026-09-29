package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/peoplebrowser"
	"go.kenn.io/msgvault/internal/store"
)

// labelingPeopleBackend adds the entity-label lookup the daemon client
// provides to the recording people backend.
type labelingPeopleBackend struct {
	recordingPeopleBackend
	labels   map[int64]string
	requests []store.EntityLabelRequest
}

func (b *labelingPeopleBackend) EntityLabels(
	_ context.Context, request store.EntityLabelRequest,
) (store.EntityLabels, error) {
	b.requests = append(b.requests, request)
	people := make(map[int64]string)
	for _, id := range request.PersonIDs {
		if label, ok := b.labels[id]; ok {
			people[id] = label
		}
	}
	return store.EntityLabels{People: people}, nil
}

func TestMCPGetPersonProfileUsesDurableAndRecordLabels(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	recordType := string(store.AttributeObjectPerson)
	referencedID := int64(12)
	backend := &profileReadingPeopleBackend{profile: &peoplebrowser.PersonProfile{
		Person:       store.Person{ID: 9, VCardUID: "person-9"},
		Label:        "Riley Example",
		RecordLabels: map[int64]string{referencedID: "Jordan Example"},
		Attributes: []peoplebrowser.AttributeGroup{{
			Definition: store.AttributeDefinition{
				UniversalID: "user-assistant", Slug: "assistant", Label: "Assistant",
				ValueType: store.AttributeValueRecordReference, Cardinality: store.AttributeCardinalitySingle,
			},
			Current: []store.PersonAttributeValue{{
				ID: 90, PersonID: 9, DefinitionSlug: "assistant", Source: store.ProvenanceUser,
				Value: store.AttributeValue{
					Type: store.AttributeValueRecordReference, RecordType: &recordType, RecordID: &referencedID,
				},
			}},
		}},
	}}

	result := rawCallTool(t, peopleToolOptions(backend), ToolGetPersonProfile, map[string]any{"person_id": 9})
	assert.NotEqual(true, result["isError"], "result: %#v", result)
	structured := toolStructuredContent(t, result)
	assert.Equal("Riley Example", structured["display_name"])

	attribute := firstStructuredRow(t, structured, "attributes")
	current, ok := attribute["current"].([]any)
	require.True(ok, "current: %#v", attribute["current"])
	require.Len(current, 1)
	row, ok := current[0].(map[string]any)
	require.True(ok)
	assert.InDelta(float64(90), row["id"], 0, "value metadata is kept alongside the value")
	value, ok := row["value"].(map[string]any)
	require.True(ok, "value: %#v", row["value"])
	assert.Equal(recordType, value["record_type"])
	assert.InDelta(float64(referencedID), value["record_id"], 0)
	assert.Equal("Jordan Example", value["record_label"])
}

func TestMCPSearchPeopleNamesProfilesWithoutTheirUID(t *testing.T) {
	for _, test := range []struct {
		name    string
		labeled bool
		want    string
	}{
		{name: "durable label from the lookup", labeled: true, want: "Riley Example"},
		{name: "no lookup available", labeled: false, want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			recording := recordingPeopleBackend{
				profiles:   []store.Person{{ID: 9, VCardUID: "person-9", ParticipantIDs: []int64{11}}},
				searchPage: &peoplebrowser.SearchPage{},
			}
			var backend peoplebrowser.Backend = &recording
			if test.labeled {
				backend = &labelingPeopleBackend{
					recordingPeopleBackend: recording, labels: map[int64]string{9: "Riley Example"},
				}
			}
			result := rawCallTool(t, peopleToolOptions(backend), ToolSearchPeople, map[string]any{})
			assert.NotEqual(t, true, result["isError"], "result: %#v", result)
			row := firstStructuredRow(t, toolStructuredContent(t, result), "rows")
			assert.Equal(t, test.want, row["display_label"])
			assert.NotEqual(t, "person-9", row["display_label"], "a vCard UID is never a label")
		})
	}
}
