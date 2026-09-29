package api

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/msgvault/internal/store"
)

func TestGetEntityLabels(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()
	srv, st := newOrganizationTestServerWithStore(t)

	survivor := mustAPIPerson(t, st, "survivor@example.com", "Survivor Name")
	absorbed := mustAPIPerson(t, st, "absorbed@example.com", "Absorbed Name")
	_, err := st.MergePersonsContext(ctx, store.PersonMergeRequest{
		SurvivorID: survivor.ID, AbsorbedID: absorbed.ID,
		ExpectedSurvivorRevision: survivor.Revision,
		ExpectedAbsorbedRevision: absorbed.Revision,
		IdempotencyKey:           "api-entity-labels-merge", Actor: "test",
	})
	require.NoError(err)
	unnamedParticipant, err := st.EnsureParticipant("unnamed@example.com", "", "example.com")
	require.NoError(err)
	organization, err := st.CreateOrganizationContext(ctx, store.OrganizationInput{
		Name: "Example Org", Kind: store.OrganizationKindCompany,
	})
	require.NoError(err)

	path := fmt.Sprintf("/api/v1/entity-labels?person=%d&person=%d,%d&person=987654"+
		"&participant=%d&participant=%d&organization=%d&organization=876543",
		absorbed.ID, survivor.ID, absorbed.ID,
		unnamedParticipant, survivor.ParticipantIDs[0], organization.ID)
	response := organizationRequest(t, srv, http.MethodGet, path, nil, "")
	require.Equal(http.StatusOK, response.Code, response.Body.String())

	var body EntityLabelsResponse
	require.NoError(json.Unmarshal(response.Body.Bytes(), &body))
	wantPeople := []EntityLabel{{ID: survivor.ID, Label: "Survivor Name"}, {ID: absorbed.ID, Label: "Absorbed Name"}}
	if absorbed.ID < survivor.ID {
		wantPeople[0], wantPeople[1] = wantPeople[1], wantPeople[0]
	}
	assert.Equal(wantPeople, body.People, "a merged-away person keeps its snapshot name")
	wantParticipants := []EntityLabel{
		{ID: survivor.ParticipantIDs[0], Label: "Survivor Name"},
		{ID: unnamedParticipant, Label: "unnamed@example.com"},
	}
	if unnamedParticipant < survivor.ParticipantIDs[0] {
		wantParticipants[0], wantParticipants[1] = wantParticipants[1], wantParticipants[0]
	}
	assert.Equal(wantParticipants, body.Participants)
	assert.Equal([]EntityLabel{{ID: organization.ID, Label: "Example Org"}}, body.Organizations)
}

func TestGetEntityLabelsEmptyRequest(t *testing.T) {
	srv, _ := newOrganizationTestServerWithStore(t)
	response := organizationRequest(t, srv, http.MethodGet, "/api/v1/entity-labels", nil, "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.JSONEq(t, `{"people":[],"participants":[],"organizations":[]}`, response.Body.String())
}

func TestGetEntityLabelsRejectsInvalidRequests(t *testing.T) {
	srv, _ := newOrganizationTestServerWithStore(t)
	overCap := make([]string, 0, store.MaxEntityLabelIDs+1)
	for id := 1; id <= store.MaxEntityLabelIDs+1; id++ {
		overCap = append(overCap, strconv.Itoa(id))
	}
	tests := []struct {
		name  string
		query string
		code  string
	}{
		{"non-integer", "person=abc", "invalid_person"},
		{"zero", "participant=0", "invalid_participant"},
		{"negative", "organization=-4", "invalid_organization"},
		{"over cap", "participant=" + strings.Join(overCap, ","), "entity_label_request_too_large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := organizationRequest(t, srv, http.MethodGet, "/api/v1/entity-labels?"+tt.query, nil, "")
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), `"`+tt.code+`"`)
		})
	}
}
