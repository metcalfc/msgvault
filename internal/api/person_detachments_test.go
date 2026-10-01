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

func TestPersonDetachHTTPDetachAndUndo(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newIdentityLinkTestServer(t)
	owner := st.mustParticipant(t, "cam@example.com", "Cam Example", "example.com")
	robot := st.mustParticipant(t, "builds@example.com", "Cam Example", "example.com")
	_, err := st.LinkParticipants(owner, robot)
	require.NoError(err)
	person, _, err := st.CreatePersonFromParticipant(owner)
	require.NoError(err)
	path := fmt.Sprintf("/api/v1/people/%d/participants/", person.ID)

	missing := personMergeAPIRequest(t, srv, http.MethodPost, path+"detach",
		fmt.Appendf(nil, `{"participant_ids":[%d]}`, robot), nil)
	assert.Equal(http.StatusPreconditionRequired, missing.Code, missing.Body.String())
	empty := personMergeAPIRequest(t, srv, http.MethodPost, path+"detach",
		[]byte(`{"participant_ids":[]}`), map[string]string{"If-Match": personETag(*person)})
	assert.Equal(http.StatusBadRequest, empty.Code, empty.Body.String())

	detachResponse := personMergeAPIRequest(t, srv, http.MethodPost, path+"detach",
		fmt.Appendf(nil, `{"participant_ids":[%d]}`, robot),
		map[string]string{"If-Match": personETag(*person)})
	require.Equal(http.StatusOK, detachResponse.Code, detachResponse.Body.String())
	var detached store.PersonParticipantDetachResult
	require.NoError(json.Unmarshal(detachResponse.Body.Bytes(), &detached))
	assert.Equal([]int64{owner}, detached.Person.ParticipantIDs)
	assert.Equal([]int64{robot}, detached.Detachment.ParticipantIDs)
	assert.Equal(identityCacheStateReady, detached.CacheState)
	assert.Equal(1, st.refreshCalls)
	assert.Equal(personETag(detached.Person), detachResponse.Header().Get("ETag"))

	stale := personMergeAPIRequest(t, srv, http.MethodPost, path+"detach",
		fmt.Appendf(nil, `{"participant_ids":[%d]}`, owner),
		map[string]string{"If-Match": personETag(*person)})
	assert.Equal(http.StatusConflict, stale.Code, stale.Body.String())
	notBound := personMergeAPIRequest(t, srv, http.MethodPost, path+"detach",
		fmt.Appendf(nil, `{"participant_ids":[%d]}`, robot),
		map[string]string{"If-Match": personETag(detached.Person)})
	assert.Equal(http.StatusConflict, notBound.Code, notBound.Body.String())
	assert.Contains(notBound.Body.String(), "person_participant_not_bound")

	undoResponse := personMergeAPIRequest(t, srv, http.MethodPost, path+"reattach",
		fmt.Appendf(nil, `{"detachment_id":%d}`, detached.Detachment.ID),
		map[string]string{"If-Match": detachResponse.Header().Get("ETag")})
	require.Equal(http.StatusOK, undoResponse.Code, undoResponse.Body.String())
	var undone store.PersonParticipantDetachResult
	require.NoError(json.Unmarshal(undoResponse.Body.Bytes(), &undone))
	assert.Equal([]int64{owner, robot}, undone.Person.ParticipantIDs)
	assert.NotNil(undone.Detachment.ReattachedAt)

	again := personMergeAPIRequest(t, srv, http.MethodPost, path+"reattach",
		fmt.Appendf(nil, `{"detachment_id":%d}`, detached.Detachment.ID),
		map[string]string{"If-Match": undoResponse.Header().Get("ETag")})
	assert.Equal(http.StatusConflict, again.Code, again.Body.String())
	assert.Contains(again.Body.String(), "person_detachment_reattached")
	unknown := personMergeAPIRequest(t, srv, http.MethodPost, path+"reattach",
		[]byte(`{"detachment_id":999}`),
		map[string]string{"If-Match": undoResponse.Header().Get("ETag")})
	assert.Equal(http.StatusNotFound, unknown.Code, unknown.Body.String())
}
