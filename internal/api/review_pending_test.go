package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/store"
)

func TestPendingReviewsRouteReportsWaitingQueues(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newCorrespondentKindTestServer(t)

	read := func() PendingReviewsResponse {
		response := personRequest(t, srv, http.MethodGet, "/api/v1/reviews/pending", nil, "")
		require.Equal(http.StatusOK, response.Code, response.Body.String())
		assert.Equal("no-store", response.Header().Get("Cache-Control"))
		var body PendingReviewsResponse
		require.NoError(json.Unmarshal(response.Body.Bytes(), &body))
		return body
	}
	assert.Equal(PendingReviewsResponse{Pending: false, Kinds: []store.PendingReviewKind{}}, read())

	desk, err := st.EnsureParticipant("desk@example.test", "Front Desk", "example.test")
	require.NoError(err)
	_, err = st.WriteDerivedCorrespondentKindsContext(t.Context(), []store.DerivedCorrespondentKind{{
		ParticipantID: desk, Source: correspondentkind.SourceJev, Kind: correspondentkind.Unclear,
	}})
	require.NoError(err)
	assert.Equal(PendingReviewsResponse{
		Pending: true, Kinds: []store.PendingReviewKind{store.PendingReviewCorrespondent},
	}, read())
}

func TestClearCorrespondentKindRemovesTheOrganizationItCreated(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newCorrespondentKindTestServer(t)
	desk, err := st.EnsureParticipant("desk@shop.example.test", "Example Shop", "shop.example.test")
	require.NoError(err)
	path := fmt.Sprintf("/api/v1/identity/correspondent-kinds/%d", desk)

	set := personRequest(t, srv, http.MethodPut, path, []byte(`{"kind":"organization"}`), "")
	require.Equal(http.StatusOK, set.Code, set.Body.String())
	var marked store.SetCorrespondentKindResult
	require.NoError(json.Unmarshal(set.Body.Bytes(), &marked))
	require.True(marked.OrganizationCreated)
	require.NotNil(marked.Record.OrganizationID)

	invalid := personRequest(t, srv, http.MethodDelete, path+"?remove_organization_id=zero", nil, "")
	assert.Equal(http.StatusBadRequest, invalid.Code)

	cleared := personRequest(t, srv, http.MethodDelete,
		fmt.Sprintf("%s?remove_organization_id=%d", path, *marked.Record.OrganizationID), nil, "")
	require.Equal(http.StatusOK, cleared.Code, cleared.Body.String())
	var result store.SetCorrespondentKindResult
	require.NoError(json.Unmarshal(cleared.Body.Bytes(), &result))
	assert.True(result.OrganizationRemoved)
	assert.Equal(correspondentkind.Person, result.Record.Kind)
	_, err = st.GetOrganizationContext(t.Context(), *marked.Record.OrganizationID)
	assert.ErrorIs(err, store.ErrOrganizationNotFound)
}
