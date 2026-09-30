package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func newCorrespondentKindTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st := testutil.NewTestStore(t)
	srv := NewServer(&config.Config{Server: config.ServerConfig{APIPort: 8080}}, st, nil, testLogger())
	return srv, st
}

func TestCorrespondentKindRoutesSetListAndClear(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newCorrespondentKindTestServer(t)
	desk, err := st.EnsureParticipant("desk@shop.example.test", "Example Shop", "shop.example.test")
	require.NoError(err)
	person, err := st.EnsureParticipant("rowan@example.test", "Rowan Example", "example.test")
	require.NoError(err)

	path := fmt.Sprintf("/api/v1/identity/correspondent-kinds/%d", desk)
	unclassified := personRequest(t, srv, http.MethodGet, path, nil, "")
	require.Equal(http.StatusOK, unclassified.Code, unclassified.Body.String())
	var record store.CorrespondentKindRecord
	require.NoError(json.Unmarshal(unclassified.Body.Bytes(), &record))
	assert.Equal(correspondentkind.Person, record.Kind)
	assert.Nil(record.Source)

	set := personRequest(t, srv, http.MethodPut, path,
		[]byte(`{"kind":"organization","organization_name":"Example Shop"}`), "")
	require.Equal(http.StatusOK, set.Code, set.Body.String())
	assert.Equal("no-store", set.Header().Get("Cache-Control"))
	var result store.SetCorrespondentKindResult
	require.NoError(json.Unmarshal(set.Body.Bytes(), &result))
	assert.True(result.OrganizationCreated)
	assert.Equal(correspondentkind.Organization, result.Record.Kind)
	require.NotNil(result.Record.OrganizationName)
	assert.Equal("Example Shop", *result.Record.OrganizationName)

	listed := personRequest(t, srv, http.MethodGet, "/api/v1/identity/correspondent-kinds?kind=organization", nil, "")
	require.Equal(http.StatusOK, listed.Code, listed.Body.String())
	var records CorrespondentKindsResponse
	require.NoError(json.Unmarshal(listed.Body.Bytes(), &records))
	require.Len(records.Records, 1)
	assert.Equal(desk, records.Records[0].CanonicalID)
	none := personRequest(t, srv, http.MethodGet, "/api/v1/identity/correspondent-kinds?kind=ignored", nil, "")
	require.Equal(http.StatusOK, none.Code)
	require.NoError(json.Unmarshal(none.Body.Bytes(), &records))
	assert.Empty(records.Records)

	// Participant summaries (detail, search rows) are marked through the
	// shared attach step.
	summaries := []query.PersonSummary{{ID: desk}, {ID: person}}
	srv.attachSearchRowProfiles(t.Context(), summaries)
	require.NotNil(summaries[0].CorrespondentKind)
	assert.Equal(correspondentkind.Organization, summaries[0].CorrespondentKind.Kind)
	assert.Nil(summaries[1].CorrespondentKind)

	cleared := personRequest(t, srv, http.MethodDelete, path, nil, "")
	require.Equal(http.StatusOK, cleared.Code, cleared.Body.String())
	require.NoError(json.Unmarshal(cleared.Body.Bytes(), &result))
	assert.Equal(correspondentkind.Person, result.Record.Kind)
	listed = personRequest(t, srv, http.MethodGet, "/api/v1/identity/correspondent-kinds", nil, "")
	require.NoError(json.Unmarshal(listed.Body.Bytes(), &records))
	assert.Empty(records.Records)
}

func TestCorrespondentKindRoutesRejectInvalidRequests(t *testing.T) {
	t.Parallel()
	setup := require.New(t)
	srv, st := newCorrespondentKindTestServer(t)
	participant, err := st.EnsureParticipant("quinn@example.test", "Quinn", "example.test")
	setup.NoError(err)
	source, err := st.GetOrCreateSource("gmail", "owner@example.test")
	setup.NoError(err)
	owner, err := st.EnsureParticipant("owner@example.test", "Owner", "example.test")
	setup.NoError(err)
	setup.NoError(st.AddAccountIdentity(source.ID, "owner@example.test", "manual"))
	path := fmt.Sprintf("/api/v1/identity/correspondent-kinds/%d", participant)

	tests := []struct {
		name, method, path, body, code string
		status                         int
	}{
		{"unknown kind", http.MethodPut, path, `{"kind":"robot"}`, "invalid_correspondent_kind", http.StatusBadRequest},
		{"unknown field", http.MethodPut, path, `{"kind":"ignored","note":"x"}`, "invalid_request", http.StatusBadRequest},
		{"organization on ignored", http.MethodPut, path, `{"kind":"ignored","organization_name":"X"}`,
			"invalid_correspondent_kind", http.StatusBadRequest},
		{"missing organization", http.MethodPut, path, `{"kind":"organization","organization_id":987654}`,
			"organization_not_found", http.StatusNotFound},
		{"missing participant", http.MethodPut, "/api/v1/identity/correspondent-kinds/987654", `{"kind":"ignored"}`,
			"participant_not_found", http.StatusNotFound},
		{"bad participant id", http.MethodGet, "/api/v1/identity/correspondent-kinds/zero", "",
			"invalid_participant_id", http.StatusBadRequest},
		{"owner", http.MethodPut, fmt.Sprintf("/api/v1/identity/correspondent-kinds/%d", owner),
			`{"kind":"ignored"}`, "correspondent_kind_owner", http.StatusConflict},
		{"bad list kind", http.MethodGet, "/api/v1/identity/correspondent-kinds?kind=person", "",
			"invalid_correspondent_kind", http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var body []byte
			if test.body != "" {
				body = []byte(test.body)
			}
			response := personRequest(t, srv, test.method, test.path, body, "")
			assert.Equal(t, test.status, response.Code, response.Body.String())
			var payload struct {
				Error string `json:"error"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload), response.Body.String())
			assert.Equal(t, test.code, payload.Error)
		})
	}
}

func TestDirectoryPeopleNotPeopleParameter(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newCorrespondentKindTestServer(t)
	shop, err := st.EnsureParticipant("orders@shop.example.test", "Example Shop", "shop.example.test")
	require.NoError(err)
	shopPerson, _, err := st.CreatePersonFromParticipantContext(t.Context(), shop)
	require.NoError(err)
	human, err := st.EnsureParticipant("sky@example.test", "Sky Example", "example.test")
	require.NoError(err)
	humanPerson, _, err := st.CreatePersonFromParticipantContext(t.Context(), human)
	require.NoError(err)
	_, err = st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: shop, Kind: correspondentkind.Organization,
	})
	require.NoError(err)

	ids := func(query string) []int64 {
		response := personRequest(t, srv, http.MethodGet, "/api/v1/people/directory"+query, nil, "")
		require.Equal(http.StatusOK, response.Code, response.Body.String())
		var page DirectoryPeopleResponse
		require.NoError(json.Unmarshal(response.Body.Bytes(), &page))
		result := []int64{}
		for _, person := range page.People {
			result = append(result, person.ID)
		}
		return result
	}
	assert.Equal([]int64{humanPerson.ID}, ids(""))
	assert.Equal([]int64{shopPerson.ID}, ids("?not_people=only"))
	assert.ElementsMatch([]int64{shopPerson.ID, humanPerson.ID}, ids("?not_people=include"))
	invalid := personRequest(t, srv, http.MethodGet, "/api/v1/people/directory?not_people=all", nil, "")
	assert.Equal(http.StatusBadRequest, invalid.Code)
}
