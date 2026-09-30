package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

func TestOrganizationMatchReviewRoutesListAcceptAndReject(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newIdentityLinkTestServer(t)
	create := func(name string) *store.Organization {
		organization, err := st.CreateOrganizationContext(t.Context(), store.OrganizationInput{
			Name: name, Kind: store.OrganizationKindCompany,
		})
		require.NoError(err)
		return organization
	}
	labs := create("Example Labs")
	created := create("Example Labs Europe")
	northwind := create("Northwind Traders")
	for _, input := range []store.OrganizationMatchReviewInput{
		{OrganizationID: labs.ID, Name: "Example Labs Europe", Model: "jev-1.13.0", Probability: 0.7},
		{OrganizationID: northwind.ID, Name: "Northwind Trading", Model: "jev-1.13.0", Probability: 0.6},
	} {
		_, err := st.RecordOrganizationMatchReviewContext(t.Context(), input)
		require.NoError(err)
	}

	list := personRequest(t, srv, http.MethodGet, "/api/v1/organization-match-reviews", nil, "")
	require.Equal(http.StatusOK, list.Code, list.Body.String())
	assert.Equal("no-store", list.Header().Get("Cache-Control"))
	var listed OrganizationMatchReviewsResponse
	require.NoError(json.Unmarshal(list.Body.Bytes(), &listed), list.Body.String())
	require.Len(listed.Reviews, 2)
	assert.Equal(50, listed.Limit)
	reviewFor := map[int64]store.OrganizationMatchReview{}
	for _, review := range listed.Reviews {
		reviewFor[review.OrganizationID] = review
	}
	require.NotNil(reviewFor[labs.ID].ProposedOrganizationID)
	assert.Equal(created.ID, *reviewFor[labs.ID].ProposedOrganizationID)

	accept := personRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/organization-match-reviews/%d/accept", reviewFor[labs.ID].ID), nil, "")
	require.Equal(http.StatusOK, accept.Code, accept.Body.String())
	var accepted store.OrganizationMatchDecision
	require.NoError(json.Unmarshal(accept.Body.Bytes(), &accepted))
	assert.Equal("accepted", accepted.Decision)
	assert.Equal(labs.ID, accepted.OrganizationID)
	require.NotNil(accepted.MergedOrganizationID)
	assert.Equal(created.ID, *accepted.MergedOrganizationID)

	again := personRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/organization-match-reviews/%d/accept", reviewFor[labs.ID].ID), nil, "")
	assert.Equal(http.StatusConflict, again.Code, again.Body.String())
	assert.Contains(again.Body.String(), "organization_review_state_changed")

	reject := personRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/organization-match-reviews/%d/reject", reviewFor[northwind.ID].ID), nil, "")
	require.Equal(http.StatusOK, reject.Code, reject.Body.String())
	var rejected store.OrganizationMatchDecision
	require.NoError(json.Unmarshal(reject.Body.Bytes(), &rejected))
	assert.Equal("rejected", rejected.Decision)
	assert.Nil(rejected.MergedOrganizationID)

	missing := personRequest(t, srv, http.MethodPost, "/api/v1/organization-match-reviews/999999/reject", nil, "")
	assert.Equal(http.StatusNotFound, missing.Code, missing.Body.String())
	invalid := personRequest(t, srv, http.MethodPost, "/api/v1/organization-match-reviews/zero/reject", nil, "")
	assert.Equal(http.StatusBadRequest, invalid.Code, invalid.Body.String())

	empty := personRequest(t, srv, http.MethodGet, "/api/v1/organization-match-reviews", nil, "")
	require.Equal(http.StatusOK, empty.Code)
	require.NoError(json.Unmarshal(empty.Body.Bytes(), &listed))
	assert.Empty(listed.Reviews)
}
