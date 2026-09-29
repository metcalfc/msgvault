package cmd

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/daemonclient"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

// entityLabelTestDaemon serves the production API router over a real store,
// through the same storeAPIAdapter the daemon uses, so CLI commands resolve
// labels from GET /api/v1/entity-labels exactly as they do in production.
type entityLabelTestDaemon struct {
	store *store.Store
	ctx   context.Context
}

func newEntityLabelTestDaemon(t *testing.T) entityLabelTestDaemon {
	t.Helper()
	st := testutil.NewTestStore(t)
	engine := query.NewEngine(st.DB(), st.IsPostgreSQL())
	t.Cleanup(func() { _ = engine.Close() })
	dataDir := t.TempDir()
	configured := config.NewDefaultConfig()
	configured.HomeDir = dataDir
	configured.Data.DataDir = dataDir
	configured.Server.APIKey = "synthetic-entity-label-key"
	server := api.NewServerWithOptions(api.ServerOptions{
		Config: configured, Store: &storeAPIAdapter{store: st}, Engine: engine,
		Logger: slog.New(slog.DiscardHandler),
	})
	httpServer := httptest.NewServer(server.Router())
	t.Cleanup(httpServer.Close)
	configured.Remote = config.RemoteConfig{
		URL: httpServer.URL, APIKey: configured.Server.APIKey, AllowInsecure: true,
	}
	return entityLabelTestDaemon{store: st, ctx: withStoreResolverConfig(t, configured)}
}

// person creates a durable person named by its participant's display name.
func (d entityLabelTestDaemon) person(t *testing.T, email, name string) (*store.Person, int64) {
	t.Helper()
	participantID, err := d.store.EnsureParticipantByIdentifier("email", email, name)
	require.NoError(t, err)
	person, _, err := d.store.CreatePersonFromParticipantContext(t.Context(), participantID)
	require.NoError(t, err)
	return person, participantID
}

// run executes a copy of a command template with its flags reset, so the
// package-level flag variables of one test cannot leak into another.
func (d entityLabelTestDaemon) run(t *testing.T, template *cobra.Command, args ...string) string {
	t.Helper()
	savedPersonJSON, savedEmploymentJSON := personJSON, employmentJSON
	savedAttributesJSON, savedOrganizationJSON := personAttributesJSONOutput, organizationJSON
	personJSON, employmentJSON, personAttributesJSONOutput, organizationJSON = false, false, false, false
	t.Cleanup(func() {
		personJSON, employmentJSON = savedPersonJSON, savedEmploymentJSON
		personAttributesJSONOutput, organizationJSON = savedAttributesJSON, savedOrganizationJSON
	})
	command := cloneEmploymentCommand(template)
	command.SetContext(d.ctx)
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(args)
	require.NoError(t, command.Execute(), output.String())
	return output.String()
}

func idText(value int64) string { return strconv.FormatInt(value, 10) }

// withoutEntityLabels stands in for a daemon released before the
// entity-labels endpoint: that route answers 404 and every other request
// reaches the test's handler. Commands must still succeed and print IDs.
func withoutEntityLabels(handler http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/entity-labels" {
			http.NotFound(w, r)
			return
		}
		handler(w, r)
	})
}

func TestEmploymentOutputNamesPeopleAndOrganizations(t *testing.T) {
	daemon := newEntityLabelTestDaemon(t)
	avery, _ := daemon.person(t, "avery@example.com", "Avery Example")
	blake, _ := daemon.person(t, "blake@example.com", "Blake Example")
	organization, err := daemon.store.CreateOrganizationContext(t.Context(), store.OrganizationInput{Name: "Example Widgets"})
	require.NoError(t, err)
	title := "Staff Engineer"
	employment, err := daemon.store.AddEmploymentContext(t.Context(), store.EmploymentInput{
		PersonID: avery.ID, OrganizationID: organization.ID, Title: &title, Source: store.ProvenanceUser,
	})
	require.NoError(t, err)
	_, err = daemon.store.AddEmploymentContext(t.Context(), store.EmploymentInput{
		PersonID: blake.ID, OrganizationID: organization.ID, Source: store.ProvenanceUser,
	})
	require.NoError(t, err)

	tests := []struct {
		name     string
		template *cobra.Command
		args     []string
		want     []string
	}{
		{
			name: "organization-scoped list names employees", template: employmentListCmd,
			args: []string{"--organization", idText(organization.ID)},
			want: []string{"Avery Example (" + idText(avery.ID) + ")", "Blake Example (" + idText(blake.ID) + ")"},
		},
		{
			name: "person-scoped list names the employer", template: employmentListCmd,
			args: []string{"--person", idText(avery.ID)},
			want: []string{"Example Widgets (" + idText(organization.ID) + ")"},
		},
		{
			name: "show names both sides", template: employmentShowCmd,
			args: []string{idText(employment.ID)},
			want: []string{
				"Person: Avery Example (" + idText(avery.ID) + ")",
				"Organization: Example Widgets (" + idText(organization.ID) + ")",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := daemon.run(t, test.template, test.args...)
			for _, want := range test.want {
				assert.Contains(t, output, want)
			}
		})
	}
}

func TestPersonRelationshipOutputNamesBothPeople(t *testing.T) {
	daemon := newEntityLabelTestDaemon(t)
	parent, _ := daemon.person(t, "casey@example.com", "Casey Example")
	child, _ := daemon.person(t, "drew@example.com", "Drew Example")

	output := daemon.run(t, personRelationshipAddCmd, idText(parent.ID), "parent", idText(child.ID))
	assert.Contains(t, output,
		"Casey Example ("+idText(parent.ID)+") is the parent of Drew Example ("+idText(child.ID)+")")

	listed := daemon.run(t, personRelationshipListCmd, idText(child.ID))
	assert.Contains(t, listed, "Casey Example ("+idText(parent.ID)+")")
	assert.NotContains(t, listed, parent.VCardUID, "a vCard UID is never a counterpart label")
}

func TestPersonRelationshipReviewsNameThePeople(t *testing.T) {
	daemon := newEntityLabelTestDaemon(t)
	owner, _ := daemon.person(t, "emery@example.com", "Emery Example")
	matched, _ := daemon.person(t, "finley@example.com", "Finley Example")
	_, err := daemon.store.DB().ExecContext(t.Context(), daemon.store.Rebind(`
		INSERT INTO person_relationship_reviews
			(person_id, raw_related_value, raw_related_type, value_kind, matched_person_id, source)
		VALUES (?, 'Finley', 'friend', 'text', ?, 'system')`), owner.ID, matched.ID)
	require.NoError(t, err)

	output := daemon.run(t, personRelationshipReviewsCmd)
	assert.Contains(t, output, "Emery Example ("+idText(owner.ID)+")")
	assert.Contains(t, output, "Finley Example ("+idText(matched.ID)+")")
}

func TestPersonOutputNamesParticipantsAndMergeLineage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	daemon := newEntityLabelTestDaemon(t)
	survivor, survivorParticipant := daemon.person(t, "gray@example.com", "Gray Example")
	absorbed, absorbedParticipant := daemon.person(t, "harper@example.com", "Harper Example")

	got := daemon.run(t, personGetCmd, idText(survivor.ID))
	assert.Contains(got, "Participants: Gray Example ("+idText(survivorParticipant)+")")

	merged := daemon.run(t, newPersonMergeCommand(), idText(survivor.ID), idText(absorbed.ID),
		"--survivor-revision", idText(survivor.Revision), "--absorbed-revision", idText(absorbed.Revision),
		"--idempotency-key", "synthetic-merge")
	assert.Contains(merged, "Survivor: Gray Example ("+idText(survivor.ID)+")")
	assert.Contains(merged, "Absorbed: Harper Example ("+idText(absorbed.ID)+")",
		"the daemon names an absorbed person from its merge snapshot")

	history := daemon.run(t, newPersonMergeHistoryCommand(), idText(survivor.ID))
	assert.Contains(history, "Gray Example ("+idText(survivor.ID)+")")
	mergeLine := strings.Fields(strings.Split(strings.TrimSpace(history), "\n")[1])[0]

	detail := daemon.run(t, newPersonMergeShowCommand(), mergeLine)
	assert.Contains(detail, "Survivor: Gray Example ("+idText(survivor.ID)+")")
	assert.Contains(detail, "Current person: Gray Example ("+idText(survivor.ID)+")")
	assert.Contains(detail, "Absorbed: Harper Example ("+idText(absorbed.ID)+")")

	current, err := daemon.store.GetPersonContext(t.Context(), survivor.ID)
	require.NoError(err)
	split := daemon.run(t, newPersonSplitCommand(), idText(survivor.ID), "--merge-id", mergeLine,
		"--revision", idText(current.Revision), "--participant", idText(absorbedParticipant),
		"--idempotency-key", "synthetic-split")
	assert.Contains(split, "Source person: Gray Example ("+idText(survivor.ID)+")")
	assert.Regexp(`New person: Harper Example \(\d+\)`, split)
}

func TestPersonAttributeRecordReferenceNamesThePerson(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	daemon := newEntityLabelTestDaemon(t)
	owner, _ := daemon.person(t, "indigo@example.com", "Indigo Example")
	referenced, _ := daemon.person(t, "jordan@example.com", "Jordan Example")
	_, err := daemon.store.CreateAttributeDefinitionContext(t.Context(), store.AttributeDefinitionInput{
		UniversalID: "test-assistant", ObjectType: store.AttributeObjectPerson, Slug: "assistant",
		Label: "Assistant", ValueType: store.AttributeValueRecordReference,
		FieldType: store.AttributeFieldPerson, RecordTarget: new(personValue),
		Cardinality: store.AttributeCardinalitySingle, Ownership: store.AttributeOwnershipUser,
		UICreatable: true, UIEditable: true, APIMutable: true, IsAudited: true, IsDeletable: true,
	})
	require.NoError(err)
	_, err = daemon.store.SetPersonAttributeValueContext(t.Context(), store.PersonAttributeValueInput{
		PersonID: owner.ID, DefinitionSlug: "assistant", Source: store.ProvenanceUser,
		Value: store.AttributeValue{
			Type: store.AttributeValueRecordReference, RecordType: new(personValue), RecordID: &referenced.ID,
		},
	})
	require.NoError(err)

	output := daemon.run(t, personAttributesListCmd, idText(owner.ID))
	assert.Contains(output, "Jordan Example ("+idText(referenced.ID)+")")
	assert.NotContains(output, "person:"+idText(referenced.ID))
}

func TestDaemonEntityLabelsBatchesPastTheServerCap(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	daemon := newEntityLabelTestDaemon(t)
	total := store.MaxEntityLabelIDs + 2
	ids := make([]int64, 0, total)
	for i := range total {
		participantID, err := daemon.store.EnsureParticipantByIdentifier(
			"email", "batch-"+strconv.Itoa(i)+"@example.com", "Batch Person "+strconv.Itoa(i))
		require.NoError(err)
		ids = append(ids, participantID)
	}
	client, _, err := OpenHTTPStore(daemon.ctx)
	require.NoError(err)
	t.Cleanup(func() { _ = client.Close() })

	labels, err := client.EntityLabels(t.Context(), store.EntityLabelRequest{ParticipantIDs: ids})
	require.NoError(err, "a request over the per-kind cap is split, not rejected")
	assert.Len(labels.Participants, total)
	assert.Equal("Batch Person "+strconv.Itoa(total-1), labels.Participants[ids[total-1]])
}

// The TUI and MCP server read through the daemon client in daemon mode, so
// these cover the labels they show arriving over the production API.
func TestDaemonClientCarriesLabelsTheTUIAndMCPShow(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	daemon := newEntityLabelTestDaemon(t)
	owner, _ := daemon.person(t, "kai@example.com", "Kai Example")
	referenced, _ := daemon.person(t, "lane@example.com", "Lane Example")
	_, err := daemon.store.CreateAttributeDefinitionContext(t.Context(), store.AttributeDefinitionInput{
		UniversalID: "test-mentor", ObjectType: store.AttributeObjectPerson, Slug: "mentor",
		Label: "Mentor", ValueType: store.AttributeValueRecordReference,
		FieldType: store.AttributeFieldPerson, RecordTarget: new(personValue),
		Cardinality: store.AttributeCardinalitySingle, Ownership: store.AttributeOwnershipUser,
		UICreatable: true, UIEditable: true, APIMutable: true, IsAudited: true, IsDeletable: true,
	})
	require.NoError(err)
	_, err = daemon.store.SetPersonAttributeValueContext(t.Context(), store.PersonAttributeValueInput{
		PersonID: owner.ID, DefinitionSlug: "mentor", Source: store.ProvenanceUser,
		Value: store.AttributeValue{
			Type: store.AttributeValueRecordReference, RecordType: new(personValue), RecordID: &referenced.ID,
		},
	})
	require.NoError(err)

	source, err := daemon.store.GetOrCreateSource("whatsapp", "+15550000000")
	require.NoError(err)
	conversationID, err := daemon.store.EnsureConversation(source.ID, "untitled-chat", "")
	require.NoError(err)
	senderID, err := daemon.store.EnsureParticipantByIdentifier("email", "morgan@example.com", "Morgan Example")
	require.NoError(err)
	_, err = daemon.store.UpsertMessage(&store.Message{
		SourceID: source.ID, ConversationID: conversationID, SourceMessageID: "untitled-1",
		MessageType: "whatsapp", SenderID: sql.NullInt64{Int64: senderID, Valid: true},
		SentAt: sql.NullTime{Time: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC), Valid: true},
	})
	require.NoError(err)

	client, _, err := OpenHTTPStore(daemon.ctx)
	require.NoError(err)
	t.Cleanup(func() { _ = client.Close() })
	browser := daemonclient.NewPeopleBrowser(daemonclient.NewEngineAdapter(client))

	attributes, err := browser.ListAttributes(t.Context(), owner.ID)
	require.NoError(err)
	assert.Equal("Lane Example", attributes.RecordLabels[referenced.ID])

	profile, err := browser.GetPersonProfile(t.Context(), owner.ID)
	require.NoError(err)
	assert.Equal("Kai Example", profile.Label, "the MCP profile names an uncurated person by its durable label")
	assert.Equal("Lane Example", profile.RecordLabels[referenced.ID])

	page, err := browser.ListConversations(t.Context(), query.TextFilter{})
	require.NoError(err)
	require.Len(page.Rows, 1)
	assert.Empty(page.Rows[0].Title)
	assert.Equal("Morgan Example", page.Rows[0].ParticipantLabel)
}
