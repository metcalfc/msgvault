package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/store"
)

// seedMatchCandidate creates a participant-pair candidate with the given basis
// and returns it along with the two participant IDs.
func seedMatchCandidate(
	t *testing.T, st *stubIdentityCacheStore, basis store.IdentityMatchBasis,
) (*store.IdentityMatchCandidate, int64, int64) {
	t.Helper()
	require := require.New(t)

	alice := st.mustParticipant(t, "alice@example.com", "Alice Example", "example.com")
	bob := st.mustParticipant(t, "bob@example.com", "Bob Example", "example.com")
	value := "beeper:@alice:beeper.local"
	candidate, _, err := st.UpsertIdentityMatchCandidateContext(
		context.Background(), store.IdentityMatchCandidateInput{
			LeftKind: store.IdentityMatchParticipant, LeftID: alice,
			RightKind: store.IdentityMatchParticipant, RightID: bob,
			Basis: basis, NormalizedValue: &value,
			State: store.IdentityMatchStateCandidate, Source: store.ProvenanceArchiveObservation,
		})
	require.NoError(err, "UpsertIdentityMatchCandidateContext")
	return candidate, alice, bob
}

func acceptPath(id int64) string {
	return fmt.Sprintf("/api/v1/identity/match-candidates/%d/accept", id)
}

func rejectPath(id int64) string {
	return fmt.Sprintf("/api/v1/identity/match-candidates/%d/reject", id)
}

func TestListIdentityMatchCandidatesFiltersByState(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newIdentityLinkTestServer(t)
	candidate, _, _ := seedMatchCandidate(t, st, store.IdentityMatchServiceScopeUsername)

	all := personRequest(t, srv, http.MethodGet, "/api/v1/identity/match-candidates", nil, "")
	require.Equal(http.StatusOK, all.Code, all.Body.String())
	assert.Equal("no-store", all.Header().Get("Cache-Control"))
	var listed IdentityMatchCandidatesResponse
	require.NoError(json.Unmarshal(all.Body.Bytes(), &listed), all.Body.String())
	require.Len(listed.Candidates, 1)
	assert.Equal(candidate.ID, listed.Candidates[0].ID)
	assert.Equal(store.IdentityMatchServiceScopeUsername, listed.Candidates[0].Basis)
	assert.Equal(100, listed.Limit, "the default page size is echoed")
	assert.Equal(0, listed.Offset)

	filtered := personRequest(t, srv, http.MethodGet,
		"/api/v1/identity/match-candidates?state=conflict", nil, "")
	require.Equal(http.StatusOK, filtered.Code, filtered.Body.String())
	var conflicts IdentityMatchCandidatesResponse
	require.NoError(json.Unmarshal(filtered.Body.Bytes(), &conflicts), filtered.Body.String())
	assert.Empty(conflicts.Candidates, "the state filter must actually filter")
}

func TestAcceptIdentityMatchCandidateLinksAndReportsCacheState(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newIdentityLinkTestServer(t)
	candidate, alice, bob := seedMatchCandidate(t, st, store.IdentityMatchServiceScopeUsername)

	response := personRequest(t, srv, http.MethodPost, acceptPath(candidate.ID),
		[]byte(`{"notes":"same person, confirmed in review"}`), "")
	require.Equal(http.StatusOK, response.Code, response.Body.String())

	var accepted IdentityMatchAcceptResponse
	require.NoError(json.Unmarshal(response.Body.Bytes(), &accepted), response.Body.String())
	assert.Equal(store.IdentityMatchStateAccepted, accepted.Candidate.State)
	assert.Equal("user", *accepted.Candidate.DecidedBy,
		"an HTTP accept is always an explicit user decision")
	assert.Equal("same person, confirmed in review", *accepted.Candidate.Notes)
	assert.Positive(accepted.IdentityRevision)
	assert.Equal(identityCacheStateReady, accepted.CacheState)
	assert.Equal(1, st.refreshCalls, "a new link must trigger the identity cache refresh")

	members, err := st.ClusterMembers(alice)
	require.NoError(err, "ClusterMembers")
	assert.Contains(members, bob, "accepting must apply the link, not only record it")
}

func TestAcceptIdentityMatchCandidateAcrossPersonsReturnsPersonMergeRequired(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newIdentityLinkTestServer(t)
	candidate, alice, bob := seedMatchCandidate(t, st, store.IdentityMatchStableProviderID)
	ctx := context.Background()
	left, _, err := st.CreatePersonFromParticipantContext(ctx, alice)
	require.NoError(err, "promote alice")
	right, _, err := st.CreatePersonFromParticipantContext(ctx, bob)
	require.NoError(err, "promote bob")
	beforeIdentityRevision, err := st.IdentityRevision()
	require.NoError(err)

	response := personRequest(t, srv, http.MethodPost, acceptPath(candidate.ID), nil, "")
	require.Equal(http.StatusConflict, response.Code, response.Body.String())
	assertPersonMergeRequiredResponse(t, response, *left, *right)

	reloaded, err := st.GetIdentityMatchCandidateContext(ctx, candidate.ID)
	require.NoError(err, "GetIdentityMatchCandidateContext")
	assert.Equal(candidate.State, reloaded.State, "a merge offer must not decide the candidate")
	assert.Equal(candidate.UpdatedAt, reloaded.UpdatedAt)
	assert.False(linkedParticipants(t, st, alice, bob))
	afterIdentityRevision, err := st.IdentityRevision()
	require.NoError(err)
	assert.Equal(beforeIdentityRevision, afterIdentityRevision)
	for _, before := range []*store.Person{left, right} {
		after, getErr := st.GetPersonContext(ctx, before.ID)
		require.NoError(getErr)
		assert.Equal(before.Revision, after.Revision)
	}
}

// seedContactProfileCandidate creates a profile with no participants and a
// participant-to-person candidate that points a participant at it.
func seedContactProfileCandidate(
	t *testing.T, st *stubIdentityCacheStore, participantID int64,
) (*store.IdentityMatchCandidate, int64) {
	t.Helper()
	var personID int64
	require.NoError(t, st.DB().QueryRow(
		`INSERT INTO persons (vcard_uid, display_name) VALUES (?, ?) RETURNING id`,
		fmt.Sprintf("contact-%d", participantID), "Contact Example",
	).Scan(&personID))
	_, err := st.AddPersonContactPointContext(context.Background(), personID, store.PersonContactPointInput{
		AddressKind: store.ContactAddressEmail, OriginalValue: "casey@example.com",
		Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceCardDAVImport},
	})
	require.NoError(t, err)
	value := "casey@example.com"
	candidate, _, err := st.UpsertIdentityMatchCandidateContext(
		context.Background(), store.IdentityMatchCandidateInput{
			LeftKind: store.IdentityMatchParticipant, LeftID: participantID,
			RightKind: store.IdentityMatchPerson, RightID: personID,
			Basis: store.IdentityMatchEmail, NormalizedValue: &value,
			State: store.IdentityMatchStateCandidate, Source: store.ProvenanceSystem,
		})
	require.NoError(t, err)
	return candidate, personID
}

func TestAcceptParticipantPersonCandidateBindsUnboundCluster(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newIdentityLinkTestServer(t)
	participant := st.mustParticipant(t, "casey@example.com", "Contact", "example.com")
	candidate, personID := seedContactProfileCandidate(t, st, participant)

	response := personRequest(t, srv, http.MethodPost, acceptPath(candidate.ID), nil, "")
	require.Equal(http.StatusOK, response.Code, response.Body.String())
	var accepted IdentityMatchAcceptResponse
	require.NoError(json.Unmarshal(response.Body.Bytes(), &accepted), response.Body.String())
	assert.Equal(store.IdentityMatchStateAccepted, accepted.Candidate.State)
	person, err := st.GetPersonContext(context.Background(), personID)
	require.NoError(err)
	assert.Equal([]int64{participant}, person.ParticipantIDs)
}

func TestAcceptParticipantPersonCandidateOwnedElsewhereReturnsPersonMergeRequired(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newIdentityLinkTestServer(t)
	participant := st.mustParticipant(t, "casey@example.com", "Contact", "example.com")
	existing, _, err := st.CreatePersonFromParticipantContext(context.Background(), participant)
	require.NoError(err)
	candidate, personID := seedContactProfileCandidate(t, st, participant)
	contact, err := st.GetPersonContext(context.Background(), personID)
	require.NoError(err)

	response := personRequest(t, srv, http.MethodPost, acceptPath(candidate.ID), nil, "")
	require.Equal(http.StatusConflict, response.Code, response.Body.String())
	assertPersonMergeRequiredResponse(t, response, *existing, *contact)
	reloaded, err := st.GetIdentityMatchCandidateContext(context.Background(), candidate.ID)
	require.NoError(err)
	assert.Equal(store.IdentityMatchStateCandidate, reloaded.State)
}

func TestListIdentityMatchCandidatesResolvesEndpointsAndFiltersContactMatches(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newIdentityLinkTestServer(t)
	pair, _, _ := seedMatchCandidate(t, st, store.IdentityMatchServiceScopeUsername)
	participant := st.mustParticipant(t, "casey@example.com", "Contact Sender", "example.com")
	var personID int64
	require.NoError(st.DB().QueryRow(
		`INSERT INTO persons (vcard_uid, display_name) VALUES ('contact-card', 'Contact Card') RETURNING id`,
	).Scan(&personID))
	_, err := st.AddPersonContactPointContext(context.Background(), personID, store.PersonContactPointInput{
		AddressKind: store.ContactAddressEmail, OriginalValue: "casey@example.com",
		Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceCardDAVImport},
	})
	require.NoError(err)
	built, err := st.BuildContactMatchCandidatesContext(context.Background())
	require.NoError(err)
	require.Equal(1, built.Created)

	response := personRequest(t, srv, http.MethodGet,
		"/api/v1/identity/match-candidates?origin=contact_match", nil, "")
	require.Equal(http.StatusOK, response.Code, response.Body.String())
	var listed IdentityMatchCandidatesResponse
	require.NoError(json.Unmarshal(response.Body.Bytes(), &listed), response.Body.String())
	require.Len(listed.Candidates, 1)
	assert.NotEqual(pair.ID, listed.Candidates[0].ID)
	require.Len(listed.ContactMatches, 1)
	assert.Equal(listed.Candidates[0].ID, listed.ContactMatches[0].CandidateID)
	assert.Equal(store.ContactMatchBind, listed.ContactMatches[0].Classification)
	require.Len(listed.Endpoints, 2)
	byKind := map[store.IdentityMatchEndpointKind]store.IdentityMatchEndpointSummary{}
	for _, endpoint := range listed.Endpoints {
		byKind[endpoint.Kind] = endpoint
	}
	sender := byKind[store.IdentityMatchParticipant]
	assert.Equal(participant, sender.ID)
	require.NotNil(sender.DisplayName)
	assert.Equal("Contact Sender", *sender.DisplayName)
	assert.Equal([]string{"casey@example.com"}, sender.Addresses)
	assert.Nil(sender.PersonID)
	card := byKind[store.IdentityMatchPerson]
	assert.True(card.Found)
	require.NotNil(card.DisplayName)
	assert.Equal("Contact Card", *card.DisplayName)
	assert.Equal([]string{"casey@example.com"}, card.Addresses)

	all := personRequest(t, srv, http.MethodGet, "/api/v1/identity/match-candidates", nil, "")
	require.Equal(http.StatusOK, all.Code, all.Body.String())
	var everything IdentityMatchCandidatesResponse
	require.NoError(json.Unmarshal(all.Body.Bytes(), &everything), all.Body.String())
	assert.Len(everything.Candidates, 2)
	assert.Len(everything.Endpoints, 4)

	invalid := personRequest(t, srv, http.MethodGet,
		"/api/v1/identity/match-candidates?origin=elsewhere", nil, "")
	assert.Equal(http.StatusBadRequest, invalid.Code, invalid.Body.String())
}

func TestListIdentityMatchCandidatesFiltersPersonDuplicates(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newIdentityLinkTestServer(t)
	pair, _, _ := seedMatchCandidate(t, st, store.IdentityMatchServiceScopeUsername)
	jane := st.mustParticipant(t, "jane@example.net", "Jane Doe", "example.net")
	janeWork := st.mustParticipant(t, "jdoe@example.org", "Jane Doe", "example.org")
	proposals, err := st.PersonDuplicateProposalsContext(context.Background(), 0)
	require.NoError(err)
	require.Len(proposals, 1)
	_, err = st.RecordPersonDuplicateJudgmentsContext(context.Background(), []store.PersonDuplicateJudgment{{
		Proposal: proposals[0], Probability: 0.7, Model: "jev-test", Propose: true,
	}})
	require.NoError(err)

	response := personRequest(t, srv, http.MethodGet,
		"/api/v1/identity/match-candidates?origin=person_duplicate", nil, "")
	require.Equal(http.StatusOK, response.Code, response.Body.String())
	var listed IdentityMatchCandidatesResponse
	require.NoError(json.Unmarshal(response.Body.Bytes(), &listed), response.Body.String())
	require.Len(listed.Candidates, 1)
	assert.NotEqual(pair.ID, listed.Candidates[0].ID)
	assert.Equal(jane, listed.Candidates[0].LeftID)
	assert.Equal(janeWork, listed.Candidates[0].RightID)
	assert.Equal(store.IdentityMatchDisplayName, listed.Candidates[0].Basis)
	require.NotNil(listed.Candidates[0].Confidence)
	assert.InDelta(0.7, *listed.Candidates[0].Confidence, 1e-9)
	assert.Len(listed.Endpoints, 2)
	assert.Empty(listed.ContactMatches)
}

func TestBuildContactMatchCandidatesReportsCounts(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newIdentityLinkTestServer(t)
	st.mustParticipant(t, "casey@example.com", "Contact", "example.com")
	var personID int64
	require.NoError(st.DB().QueryRow(
		`INSERT INTO persons (vcard_uid) VALUES ('contact-build') RETURNING id`).Scan(&personID))
	_, err := st.AddPersonContactPointContext(context.Background(), personID, store.PersonContactPointInput{
		AddressKind: store.ContactAddressEmail, OriginalValue: "Casey@Example.com",
		Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceCardDAVImport},
	})
	require.NoError(err)

	response := personRequest(t, srv, http.MethodPost, "/api/v1/identity/contact-matches/build", nil, "")
	require.Equal(http.StatusOK, response.Code, response.Body.String())
	assert.Equal("no-store", response.Header().Get("Cache-Control"))
	var result store.ContactMatchBuildResult
	require.NoError(json.Unmarshal(response.Body.Bytes(), &result), response.Body.String())
	assert.Equal(store.ContactMatchBuildResult{Matches: 1, Created: 1, EvidenceAdded: 1, Bind: 1}, result)

	again := personRequest(t, srv, http.MethodPost, "/api/v1/identity/contact-matches/build", nil, "")
	require.Equal(http.StatusOK, again.Code, again.Body.String())
	require.NoError(json.Unmarshal(again.Body.Bytes(), &result), again.Body.String())
	assert.Equal(store.ContactMatchBuildResult{Matches: 1, Existing: 1, Bind: 1}, result)
}

func TestRejectIdentityMatchCandidateRetainsTheRow(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newIdentityLinkTestServer(t)
	candidate, alice, bob := seedMatchCandidate(t, st, store.IdentityMatchServiceScopeUsername)

	response := personRequest(t, srv, http.MethodPost, rejectPath(candidate.ID),
		[]byte(`{"notes":"different people"}`), "")
	require.Equal(http.StatusOK, response.Code, response.Body.String())

	var rejected IdentityMatchRejectResponse
	require.NoError(json.Unmarshal(response.Body.Bytes(), &rejected), response.Body.String())
	assert.Equal(store.IdentityMatchStateRejected, rejected.Candidate.State)
	assert.Equal("user", *rejected.Candidate.DecidedBy)
	assert.Zero(rejected.IdentityRevision,
		"state-only rejection does not change the identity revision")
	assert.Equal(identityCacheStateReady, rejected.CacheState)
	assert.Equal(1, st.refreshCalls,
		"state-only rejection must still verify that the cache is current")
	assert.False(linkedParticipants(t, st, alice, bob), "rejecting must not link anyone")

	listed := personRequest(t, srv, http.MethodGet,
		"/api/v1/identity/match-candidates?state=rejected", nil, "")
	require.Equal(http.StatusOK, listed.Code, listed.Body.String())
	var page IdentityMatchCandidatesResponse
	require.NoError(json.Unmarshal(listed.Body.Bytes(), &page), listed.Body.String())
	assert.Len(page.Candidates, 1,
		"a rejected suggestion is retained so the same inference is not repeated")
}

func TestRejectAcceptedSystemIdentityMatchUnlinksAndRetainsRejection(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newIdentityLinkTestServer(t)
	candidate, alice, bob := seedMatchCandidate(t, st, store.IdentityMatchStableProviderID)

	_, _, err := st.AcceptIdentityMatchCandidateContext(
		context.Background(), candidate.ID, "system", nil,
	)
	require.NoError(err, "system acceptance")
	require.True(linkedParticipants(t, st, alice, bob), "precondition: accept linked the pair")

	rejected := personRequest(t, srv, http.MethodPost, rejectPath(candidate.ID),
		[]byte(`{"notes":"not the same person"}`), "")
	require.Equal(http.StatusOK, rejected.Code, rejected.Body.String())
	var body IdentityMatchRejectResponse
	require.NoError(json.Unmarshal(rejected.Body.Bytes(), &body), rejected.Body.String())
	assert.Equal(store.IdentityMatchStateRejected, body.Candidate.State)
	assert.Positive(body.IdentityRevision,
		"removing an owned automated edge must report the bumped revision")
	assert.Equal(identityCacheStateReady, body.CacheState)
	assert.Equal(1, st.refreshCalls,
		"removing an owned automated edge must refresh the identity cache")

	reloaded, err := st.GetIdentityMatchCandidateContext(context.Background(), candidate.ID)
	require.NoError(err, "reload candidate")
	assert.Equal(store.IdentityMatchStateRejected, reloaded.State)
	assert.False(linkedParticipants(t, st, alice, bob),
		"rejecting an automated match must remove its direct identity edge")
}

func TestRejectAcceptedSystemIdentityMatchRetryRepairsStaleCache(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newIdentityLinkTestServer(t)
	candidate, _, _ := seedMatchCandidate(t, st, store.IdentityMatchStableProviderID)

	_, _, err := st.AcceptIdentityMatchCandidateContext(
		context.Background(), candidate.ID, "system", nil,
	)
	require.NoError(err, "system acceptance")
	st.refreshErr = errors.New("cache refresh unavailable")

	first := personRequest(t, srv, http.MethodPost, rejectPath(candidate.ID), nil, "")
	require.Equal(http.StatusOK, first.Code, first.Body.String())
	var firstBody IdentityMatchRejectResponse
	require.NoError(json.Unmarshal(first.Body.Bytes(), &firstBody), first.Body.String())
	assert.Equal(identityCacheStateStale, firstBody.CacheState)
	assert.Equal(1, st.refreshCalls)

	second := personRequest(t, srv, http.MethodPost, rejectPath(candidate.ID), nil, "")
	require.Equal(http.StatusOK, second.Code, second.Body.String())
	var secondBody IdentityMatchRejectResponse
	require.NoError(json.Unmarshal(second.Body.Bytes(), &secondBody), second.Body.String())
	assert.Equal(firstBody.IdentityRevision, secondBody.IdentityRevision,
		"retrying the decision must not mutate identity state again")
	assert.Equal(identityCacheStateStale, secondBody.CacheState,
		"retry must not report ready while the cache refresh still fails")
	assert.Equal(2, st.refreshCalls,
		"retry must re-attempt the stale cache refresh without re-mutating")
}

func TestRejectAcceptedSystemIdentityMatchPreservesManualEdgeWithoutBumpingRevision(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newIdentityLinkTestServer(t)
	candidate, alice, bob := seedMatchCandidate(t, st, store.IdentityMatchStableProviderID)

	_, err := st.LinkParticipants(alice, bob)
	require.NoError(err, "manual link")
	_, _, err = st.AcceptIdentityMatchCandidateContext(
		context.Background(), candidate.ID, "system", nil,
	)
	require.NoError(err, "system acceptance of already-linked pair")
	before, err := st.IdentityRevision()
	require.NoError(err, "identity revision before rejection")

	rejected := personRequest(t, srv, http.MethodPost, rejectPath(candidate.ID), nil, "")
	require.Equal(http.StatusOK, rejected.Code, rejected.Body.String())
	var body IdentityMatchRejectResponse
	require.NoError(json.Unmarshal(rejected.Body.Bytes(), &body), rejected.Body.String())
	assert.Equal(before, body.IdentityRevision,
		"preserving a pre-existing manual edge must not bump the revision")
	assert.Equal(identityCacheStateReady, body.CacheState)
	assert.Equal(1, st.refreshCalls,
		"unchanged identity state must still verify that the cache is current")
	assert.True(linkedParticipants(t, st, alice, bob),
		"manual edge must survive rejection")
}

func TestIdentityMatchStateConflictErrorsUseStableAPICodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		wantCode string
	}{
		{
			name: "accepted snapshot changed", err: store.ErrIdentityMatchNotAccepted,
			wantCode: "identity_match_state_changed",
		},
		{
			name: "applied match changed", err: store.ErrIdentityMatchAlreadyApplied,
			wantCode: "identity_match_already_applied",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requirements := require.New(t)
			assertions := assert.New(t)
			srv, _ := newIdentityLinkTestServer(t)
			response := httptest.NewRecorder()

			srv.writeIdentityMatchError(response, test.err)

			requirements.Equal(http.StatusConflict, response.Code, response.Body.String())
			var body ErrorResponse
			requirements.NoError(json.Unmarshal(response.Body.Bytes(), &body))
			assertions.Equal(test.wantCode, body.Error)
		})
	}
}

// linkedParticipants reports whether two participants share a cluster.
func linkedParticipants(t *testing.T, st *stubIdentityCacheStore, a, b int64) bool {
	t.Helper()
	require := require.New(t)

	members, err := st.ClusterMembers(a)
	require.NoError(err, "ClusterMembers")
	return slices.Contains(members, b)
}

func TestIdentityMatchCandidateRoutesValidateInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		method     string
		path       string
		body       []byte
		wantStatus int
		wantCode   string
	}{
		{
			name: "unknown state value", method: http.MethodGet,
			path:       "/api/v1/identity/match-candidates?state=maybe",
			wantStatus: http.StatusBadRequest, wantCode: "state",
		},
		{
			name: "non numeric limit", method: http.MethodGet,
			path:       "/api/v1/identity/match-candidates?limit=lots",
			wantStatus: http.StatusBadRequest, wantCode: "limit",
		},
		{
			name: "non numeric candidate id", method: http.MethodPost,
			path:       "/api/v1/identity/match-candidates/abc/accept",
			wantStatus: http.StatusBadRequest, wantCode: "invalid_candidate_id",
		},
		{
			name: "unknown candidate", method: http.MethodPost,
			path:       "/api/v1/identity/match-candidates/999999/accept",
			wantStatus: http.StatusNotFound, wantCode: "identity_match_not_found",
		},
		{
			name: "unknown request field", method: http.MethodPost,
			path:       "/api/v1/identity/match-candidates/1/accept",
			body:       []byte(`{"note":"typo"}`),
			wantStatus: http.StatusBadRequest, wantCode: "invalid_request",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			srv, _ := newIdentityLinkTestServer(t)

			response := personRequest(t, srv, test.method, test.path, test.body, "")
			require.Equal(test.wantStatus, response.Code, response.Body.String())
			assert.Contains(response.Body.String(), test.wantCode)
		})
	}
}

func TestAcceptEmptyBodyIsAllowed(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newIdentityLinkTestServer(t)
	candidate, _, _ := seedMatchCandidate(t, st, store.IdentityMatchStableProviderID)

	response := personRequest(t, srv, http.MethodPost, acceptPath(candidate.ID), nil, "")
	require.Equal(http.StatusOK, response.Code, response.Body.String())

	var accepted IdentityMatchAcceptResponse
	require.NoError(json.Unmarshal(response.Body.Bytes(), &accepted), response.Body.String())
	assert.Nil(accepted.Candidate.Notes, "notes are optional")
}

func TestLinkEquivalentEmailAddressesEndpointLinksSharedMailbox(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	srv, st := newIdentityLinkTestServer(t)
	primary := st.mustParticipant(t, "pat@example.com", "Pat Example", "example.com")
	tagged := st.mustParticipant(t, "pat+news@example.com", "", "example.com")

	response := personRequest(t, srv, http.MethodPost,
		"/api/v1/identity/email-equivalence/link", nil, "")
	require.Equal(http.StatusOK, response.Code, response.Body.String())
	var first store.EmailEquivalenceResult
	require.NoError(json.Unmarshal(response.Body.Bytes(), &first), response.Body.String())
	assert.Equal(store.EmailEquivalenceResult{
		Participants: 2, Linked: 1, CacheState: identityCacheStateReady,
	}, first)
	assert.Equal(1, st.refreshCalls, "new links refresh the identity datasets")
	members, err := st.ClusterMembers(primary)
	require.NoError(err)
	assert.Equal([]int64{primary, tagged}, members)

	// A repeat request rescans the archive and changes nothing.
	response = personRequest(t, srv, http.MethodPost,
		"/api/v1/identity/email-equivalence/link", nil, "")
	require.Equal(http.StatusOK, response.Code, response.Body.String())
	var repeat store.EmailEquivalenceResult
	require.NoError(json.Unmarshal(response.Body.Bytes(), &repeat), response.Body.String())
	assert.Equal(store.EmailEquivalenceResult{Participants: 2}, repeat)
	assert.Equal(1, st.refreshCalls, "a pass that links nothing leaves the cache alone")
}

// partialEquivalenceStore runs the real pass, then reports a failure as a
// later batch would after earlier batches committed their links. A real
// mid-run batch failure cannot be produced deterministically.
type partialEquivalenceStore struct {
	*stubIdentityCacheStore
}

func (s *partialEquivalenceStore) LinkEquivalentEmailAddressesContext(
	ctx context.Context, force bool,
) (*store.EmailEquivalenceResult, error) {
	result, err := s.Store.LinkEquivalentEmailAddressesContext(ctx, force)
	if err != nil {
		return result, err
	}
	return result, errors.New("later batch failed")
}

func TestLinkEquivalentEmailAddressesEndpointRefreshesAfterPartialRun(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	_, st := newIdentityLinkTestServer(t)
	partial := &partialEquivalenceStore{stubIdentityCacheStore: st}
	srv := NewServer(&config.Config{Server: config.ServerConfig{APIPort: 8080}}, partial, nil, testLogger())
	primary := st.mustParticipant(t, "pat@example.com", "Pat Example", "example.com")
	tagged := st.mustParticipant(t, "pat+news@example.com", "", "example.com")

	response := personRequest(t, srv, http.MethodPost,
		"/api/v1/identity/email-equivalence/link", nil, "")
	require.Equal(http.StatusInternalServerError, response.Code, response.Body.String())
	members, err := st.ClusterMembers(primary)
	require.NoError(err)
	require.Equal([]int64{primary, tagged}, members, "the committed batch kept its link")
	assert.Equal(1, st.refreshCalls, "committed links refresh the cache even when the run fails")
}
