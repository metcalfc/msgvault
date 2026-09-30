package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

func TestPersonEnrichmentIdentityReviewRoutesListConfirmAndReject(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newIdentityLinkTestServer(t)
	confirmPerson, confirmAttempt := storetest.UncertainEnrichmentAttempt(t, st.Store, "ada@example.test", "Ada Example")
	_, rejectAttempt := storetest.UncertainEnrichmentAttempt(t, st.Store, "bo@example.test", "Bo Example")

	list := personRequest(t, srv, http.MethodGet, "/api/v1/person-enrichment/identity-reviews", nil, "")
	require.Equal(http.StatusOK, list.Code, list.Body.String())
	assert.Equal("no-store", list.Header().Get("Cache-Control"))
	var listed PersonEnrichmentIdentityReviewsResponse
	require.NoError(json.Unmarshal(list.Body.Bytes(), &listed), list.Body.String())
	require.Len(listed.Reviews, 2)
	assert.Equal(50, listed.Limit)
	for _, review := range listed.Reviews {
		assert.True(review.ProviderPersonIDKnown)
		require.NotNil(review.Returned.ProfileURLHost)
		assert.Equal("profiles.example.test", *review.Returned.ProfileURLHost)
	}

	confirm := personRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/person-enrichment/identity-reviews/%d/confirm", confirmAttempt), nil, "")
	require.Equal(http.StatusOK, confirm.Code, confirm.Body.String())
	var confirmed store.PersonEnrichmentIdentityDecision
	require.NoError(json.Unmarshal(confirm.Body.Bytes(), &confirmed))
	assert.Equal("confirmed", confirmed.Decision)
	assert.Equal(confirmPerson, confirmed.PersonID)
	assert.Equal("succeeded", confirmed.AttemptState)

	again := personRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/person-enrichment/identity-reviews/%d/confirm", confirmAttempt), nil, "")
	assert.Equal(http.StatusConflict, again.Code, again.Body.String())
	assert.Contains(again.Body.String(), "enrichment_review_state_changed")

	reject := personRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/person-enrichment/identity-reviews/%d/reject", rejectAttempt), nil, "")
	require.Equal(http.StatusOK, reject.Code, reject.Body.String())
	var rejected store.PersonEnrichmentIdentityDecision
	require.NoError(json.Unmarshal(reject.Body.Bytes(), &rejected))
	assert.Equal("rejected", rejected.Decision)
	assert.Equal(1, rejected.Negatives)

	missing := personRequest(t, srv, http.MethodPost,
		"/api/v1/person-enrichment/identity-reviews/999999/reject", nil, "")
	assert.Equal(http.StatusNotFound, missing.Code, missing.Body.String())
	invalid := personRequest(t, srv, http.MethodPost,
		"/api/v1/person-enrichment/identity-reviews/zero/reject", nil, "")
	assert.Equal(http.StatusBadRequest, invalid.Code, invalid.Body.String())

	empty := personRequest(t, srv, http.MethodGet, "/api/v1/person-enrichment/identity-reviews", nil, "")
	require.Equal(http.StatusOK, empty.Code)
	require.NoError(json.Unmarshal(empty.Body.Bytes(), &listed))
	assert.Empty(listed.Reviews)
}
