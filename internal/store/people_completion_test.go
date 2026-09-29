package store_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

func TestCompletePersonProfilesReturnsOnlyCurrentCuratedPrimitives(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	st := storetest.New(t).Store
	participantID, err := st.EnsureParticipant("alice@example.test", "Alice Observed", "example.test")
	require.NoError(err)
	person, _, err := st.CreatePersonFromParticipantContext(ctx, participantID)
	require.NoError(err)
	_, err = st.UpdatePersonDisplayNameContext(ctx, person.ID, person.Revision, new("Alice Profile"))
	require.NoError(err)

	for _, input := range []store.PersonNameInput{
		{NameKind: store.PersonNameFormatted, Formatted: new("Alice Example"), Envelope: completionEnvelope()},
		{NameKind: store.PersonNameNickname, Formatted: new("Ally"), Envelope: completionEnvelope()},
		{NameKind: store.PersonNamePhonetic, Formatted: new("Arisu"), Envelope: completionEnvelope()},
		{NameKind: store.PersonNameNickname, Formatted: new("Percent% Name"), Envelope: completionEnvelope()},
		{NameKind: store.PersonNameNickname, Formatted: new("Under_score"), Envelope: completionEnvelope()},
		{NameKind: store.PersonNameSort, SortAs: new("Example, Alice"), Envelope: completionEnvelope()},
	} {
		_, err = st.AddPersonNameContext(ctx, person.ID, input)
		require.NoError(err)
	}
	for _, input := range []store.PersonContactPointInput{
		{AddressKind: store.ContactAddressEmail, OriginalValue: "Alice@Example.test", Envelope: completionEnvelope()},
		{AddressKind: store.ContactAddressPhone, OriginalValue: "+1 (202) 555-0147", Envelope: completionEnvelope()},
		{AddressKind: store.ContactAddressUsername, ServiceSlug: new("telegram"), OriginalValue: "@alice", Envelope: completionEnvelope()},
		{AddressKind: store.ContactAddressIMPP, OriginalValue: "xmpp:alice@example.test", Envelope: completionEnvelope()},
		{AddressKind: store.ContactAddressURL, OriginalValue: "https://example.test/alice", Envelope: completionEnvelope()},
	} {
		_, err = st.AddPersonContactPointContext(ctx, person.ID, input)
		require.NoError(err)
	}

	currentOrg, err := st.CreateOrganizationContext(ctx, store.OrganizationInput{
		Name: "Example Industries", Kind: store.OrganizationKindCompany,
	})
	require.NoError(err)
	endedOrg, err := st.CreateOrganizationContext(ctx, store.OrganizationInput{
		Name: "Former Example", Kind: store.OrganizationKindCompany,
	})
	require.NoError(err)
	_, err = st.AddEmploymentContext(ctx, store.EmploymentInput{
		PersonID: person.ID, OrganizationID: currentOrg.ID,
		Title: new("Staff Engineer"), Role: new("Technical Lead"),
		Source: store.ProvenanceUser,
	})
	require.NoError(err)
	_, err = st.AddEmploymentContext(ctx, store.EmploymentInput{
		PersonID: person.ID, OrganizationID: endedOrg.ID,
		Title: new("Former Engineer"), Role: new("Former Role"),
		IsCurrent: new(false), Source: store.ProvenanceUser,
	})
	require.NoError(err)

	tests := []struct {
		query string
		want  []store.PersonCompletion
	}{
		{"ally", []store.PersonCompletion{{ParticipantID: participantID, DisplayLabel: "Alice Profile", Kind: "name", Value: "Ally", MatchValue: "ally", Source: "nickname"}}},
		{"arisu", []store.PersonCompletion{{ParticipantID: participantID, DisplayLabel: "Alice Profile", Kind: "name", Value: "Arisu", MatchValue: "arisu", Source: "phonetic"}}},
		{"%", []store.PersonCompletion{{ParticipantID: participantID, DisplayLabel: "Alice Profile", Kind: "name", Value: "Percent% Name", MatchValue: "percent% name", Source: "nickname"}}},
		{"_", []store.PersonCompletion{{ParticipantID: participantID, DisplayLabel: "Alice Profile", Kind: "name", Value: "Under_score", MatchValue: "under_score", Source: "nickname"}}},
		{"2025550147", []store.PersonCompletion{{ParticipantID: participantID, DisplayLabel: "Alice Profile", Kind: "phone", Value: "+1 (202) 555-0147", MatchValue: "+12025550147", Source: "profile"}}},
		{"@alice", []store.PersonCompletion{{ParticipantID: participantID, DisplayLabel: "Alice Profile", Kind: "username", Value: "@alice", MatchValue: "alice", Source: "telegram"}}},
		{"xmpp", []store.PersonCompletion{{ParticipantID: participantID, DisplayLabel: "Alice Profile", Kind: "impp", Value: "xmpp:alice@example.test", MatchValue: "xmpp:alice@example.test", Source: "profile"}}},
		{"industries", []store.PersonCompletion{{ParticipantID: participantID, DisplayLabel: "Alice Profile", Kind: "organization", Value: "Example Industries", MatchValue: "example industries", Source: "profile"}}},
		{"staff", []store.PersonCompletion{{ParticipantID: participantID, DisplayLabel: "Alice Profile", Kind: "title", Value: "Staff Engineer", MatchValue: "staff engineer", Source: "profile"}}},
		{"technical", []store.PersonCompletion{{ParticipantID: participantID, DisplayLabel: "Alice Profile", Kind: "role", Value: "Technical Lead", MatchValue: "technical lead", Source: "profile"}}},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			rows, err := st.CompletePersonProfilesContext(ctx, store.PersonCompletionQuery{
				Query: test.query, Limit: 20,
			})
			assert.NoError(t, err) //nolint:testifylint // The parent test's required local helper shadows require.
			assert.Equal(t, test.want, rows)
		})
	}

	for _, excluded := range []string{"example, alice", "former example", "former engineer", "former role", "https://"} {
		rows, err := st.CompletePersonProfilesContext(ctx, store.PersonCompletionQuery{
			Query: excluded, Limit: 20,
		})
		require.NoError(err)
		assert.Empty(t, rows, excluded)
	}
}

func TestCompletePersonProfilesValidatesAndCapsResults(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := context.Background()
	st := storetest.New(t).Store
	for index := range 25 {
		participantID, err := st.EnsureParticipant(
			"person-"+string(rune('a'+index))+"@example.test", "Synthetic", "example.test")
		require.NoError(err)
		person, _, err := st.CreatePersonFromParticipantContext(ctx, participantID)
		require.NoError(err)
		_, err = st.AddPersonNameContext(ctx, person.ID, store.PersonNameInput{
			NameKind: store.PersonNameNickname, Formatted: new("Common Name"),
			Envelope: completionEnvelope(),
		})
		require.NoError(err)
	}

	rows, err := st.CompletePersonProfilesContext(ctx, store.PersonCompletionQuery{
		Query: "common", Limit: 20,
	})
	require.NoError(err)
	assert.Len(rows, 20)

	for _, query := range []store.PersonCompletionQuery{
		{Query: " ", Limit: 8}, {Query: "common", Limit: -1}, {Query: "common", Limit: 21},
	} {
		_, err := st.CompletePersonProfilesContext(ctx, query)
		assert.ErrorIs(err, store.ErrInvalidPersonCompletionQuery)
	}
}

func completionEnvelope() store.ValueEnvelopeInput {
	return store.ValueEnvelopeInput{Source: store.ProvenanceUser}
}

// TestCompletePersonProfilesLabelsUnnamedPeopleByPerson pins that an unnamed
// person's completion row is labelled by the durable person label, and that
// an employer, title, or role match never becomes the person's label.
func TestCompletePersonProfilesLabelsUnnamedPeopleByPerson(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	st := storetest.New(t).Store

	addressParticipant, err := st.EnsureParticipant("unnamed@example.test", "", "example.test")
	require.NoError(err)
	addressed, _, err := st.CreatePersonFromParticipantContext(ctx, addressParticipant)
	require.NoError(err)
	require.Nil(addressed.DisplayName)

	var bareParticipant int64
	require.NoError(st.DB().QueryRowContext(ctx, st.Rebind(
		`INSERT INTO participants (display_name) VALUES (NULL) RETURNING id`)).Scan(&bareParticipant))
	bare, _, err := st.CreatePersonFromParticipantContext(ctx, bareParticipant)
	require.NoError(err)
	require.Nil(bare.DisplayName)
	_, err = st.AddPersonNameContext(ctx, bare.ID, store.PersonNameInput{
		NameKind: store.PersonNameNickname, Formatted: new("Quill"), Envelope: completionEnvelope(),
	})
	require.NoError(err)

	organization, err := st.CreateOrganizationContext(ctx, store.OrganizationInput{
		Name: "Acme Corp", Kind: store.OrganizationKindCompany,
	})
	require.NoError(err)
	for _, personID := range []int64{addressed.ID, bare.ID} {
		_, err = st.AddEmploymentContext(ctx, store.EmploymentInput{
			PersonID: personID, OrganizationID: organization.ID,
			Title: new("Chief Widget Officer"), Source: store.ProvenanceUser,
		})
		require.NoError(err)
	}

	tests := []struct {
		query string
		want  []store.PersonCompletion
	}{
		{"acme", []store.PersonCompletion{
			{ParticipantID: bareParticipant, DisplayLabel: "Unknown person", Kind: "organization", Value: "Acme Corp", MatchValue: "acme corp", Source: "profile"},
			{ParticipantID: addressParticipant, DisplayLabel: "unnamed@example.test", Kind: "organization", Value: "Acme Corp", MatchValue: "acme corp", Source: "profile"},
		}},
		{"widget", []store.PersonCompletion{
			{ParticipantID: bareParticipant, DisplayLabel: "Unknown person", Kind: "title", Value: "Chief Widget Officer", MatchValue: "chief widget officer", Source: "profile"},
			{ParticipantID: addressParticipant, DisplayLabel: "unnamed@example.test", Kind: "title", Value: "Chief Widget Officer", MatchValue: "chief widget officer", Source: "profile"},
		}},
		{"quill", []store.PersonCompletion{
			{ParticipantID: bareParticipant, DisplayLabel: "Quill", Kind: "name", Value: "Quill", MatchValue: "quill", Source: "nickname"},
		}},
	}
	for _, test := range tests {
		rows, err := st.CompletePersonProfilesContext(ctx, store.PersonCompletionQuery{Query: test.query})
		require.NoError(err)
		assert.Equal(t, test.want, rows, test.query)
	}
}
