package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestPersonDuplicateProposalsRequireAPersonalLocalPart(t *testing.T) {
	tests := []struct {
		name    string
		local   string
		propose bool
	}{
		{"bare first name", "michael", false},
		{"department word", "engineering", false},
		{"long single word", "christopher", false},
		{"first dot last", "pat.example", true},
		{"first underscore last", "pat_example", true},
		{"initial hyphen last", "p-example", true},
		{"letters with digits", "pexample42", true},
		{"name with digits after a dot", "pat.12345", true},
		{"hyphenated word with a dangling separator", "michael-", false},
		{"team word part", "team.example", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testutil.NewTestStore(t)
			left := duplicateParticipant(t, st, tt.local+"@example.com", "")
			right := duplicateParticipant(t, st, tt.local+"@example.net", "")

			proposals, err := st.PersonDuplicateProposalsContext(t.Context(), 0)
			require.NoError(t, err)
			if !tt.propose {
				assert.Empty(t, proposalPairs(proposals))
				return
			}
			assert.Equal(t, [][2]int64{{left, right}}, proposalPairs(proposals))
			require.Len(t, proposals, 1)
			assert.Equal(t, tt.local, proposals[0].SharedValue)
		})
	}
}

func TestPersonDuplicateProposalsPairALocalPartOnlyBetweenTwoClusters(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	for _, address := range []string{"lee.example@example.com", "lee.example@example.net", "lee.example@example.org"} {
		duplicateParticipant(t, st, address, "")
	}
	left := duplicateParticipant(t, st, "kim.example@example.com", "")
	right := duplicateParticipant(t, st, "kim.example@example.org", "")

	proposals, err := st.PersonDuplicateProposalsContext(t.Context(), 0)
	require.NoError(err)
	assert.Equal([][2]int64{{left, right}}, proposalPairs(proposals),
		"an address name on three clusters is a common name, not one person")
}

// recordNameCandidates proposes and records every proposal as a pending
// Jev-judged candidate.
func recordNameCandidates(t *testing.T, st *store.Store) {
	t.Helper()
	proposals, err := st.PersonDuplicateProposalsContext(t.Context(), 0)
	require.NoError(t, err)
	judgments := make([]store.PersonDuplicateJudgment, len(proposals))
	for i, proposal := range proposals {
		judgments[i] = store.PersonDuplicateJudgment{
			Proposal: proposal, Probability: 0.8, Model: "jev-test", Propose: true,
		}
	}
	_, err = st.RecordPersonDuplicateJudgmentsContext(t.Context(), judgments)
	require.NoError(t, err)
}

func duplicateCandidateByPair(
	t *testing.T, st *store.Store, left, right int64,
) store.IdentityMatchCandidate {
	t.Helper()
	candidates, err := st.ListPersonDuplicateCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(t, err)
	for _, candidate := range candidates {
		if candidate.LeftID == left && candidate.RightID == right {
			return candidate
		}
	}
	require.Failf(t, "candidate not found", "no duplicate-person candidate for %d and %d", left, right)
	return store.IdentityMatchCandidate{}
}

func TestRejectPersonDuplicateCandidateRejectsTheSameNameOnEitherSide(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	a := duplicateParticipant(t, st, "sam@example.com", "Sam Example")
	b := duplicateParticipant(t, st, "sam@example.net", "Sam Example")
	c := duplicateParticipant(t, st, "sam@example.org", "Sam Example")
	d := duplicateParticipant(t, st, "sam@example.edu", "Sam Example")
	// A second name on a's cluster pairs it with another cluster.
	aAlias := duplicateParticipant(t, st, "lee.other@example.com", "Lee Other")
	_, err := st.LinkParticipants(a, aAlias)
	require.NoError(err)
	other := duplicateParticipant(t, st, "lee.other@example.net", "Lee Other")
	recordNameCandidates(t, st)

	rejected := duplicateCandidateByPair(t, st, a, b)
	notes := "different people"
	rejection, err := st.RejectIdentityMatchCandidateContext(t.Context(), rejected.ID, "user", &notes)
	require.NoError(err)
	require.NotNil(rejection.Candidate)
	assert.Equal(store.IdentityMatchStateRejected, rejection.Candidate.State)
	require.NotNil(rejection.Candidate.Notes)
	assert.Equal(notes, *rejection.Candidate.Notes, "the user's own note stays on the rejected row")

	group := []store.IdentityMatchCandidate{
		duplicateCandidateByPair(t, st, a, c), duplicateCandidateByPair(t, st, a, d),
		duplicateCandidateByPair(t, st, b, c), duplicateCandidateByPair(t, st, b, d),
	}
	wantIDs := make([]int64, len(group))
	for i, candidate := range group {
		wantIDs[i] = candidate.ID
		assert.Equal(store.IdentityMatchStateRejected, candidate.State)
		require.NotNil(candidate.DecidedBy)
		assert.Equal("user", *candidate.DecidedBy)
		require.NotNil(candidate.Notes)
		assert.Contains(*candidate.Notes, "same shared name")
	}
	assert.ElementsMatch(wantIDs, rejection.AlsoRejected)

	untouched := duplicateCandidateByPair(t, st, c, d)
	assert.Equal(store.IdentityMatchStateCandidate, untouched.State, "neither side was rejected")
	assert.Nil(untouched.DecidedBy)
	otherName := duplicateCandidateByPair(t, st, a, other)
	assert.Equal(store.IdentityMatchStateCandidate, otherName.State, "a different name is a different question")
}

func TestRejectPersonDuplicateCandidateLeavesDecidedRowsAndSystemRejections(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	a := duplicateParticipant(t, st, "sam@example.com", "Sam Example")
	b := duplicateParticipant(t, st, "sam@example.net", "Sam Example")
	c := duplicateParticipant(t, st, "sam@example.org", "Sam Example")
	recordNameCandidates(t, st)

	accepted := duplicateCandidateByPair(t, st, a, c)
	_, _, err := st.AcceptIdentityMatchCandidateContext(t.Context(), accepted.ID, "user", nil)
	require.NoError(err)

	systemReject := duplicateCandidateByPair(t, st, b, c)
	rejection, err := st.RejectIdentityMatchCandidateContext(t.Context(), systemReject.ID, "system", nil)
	require.NoError(err)
	assert.Empty(rejection.AlsoRejected, "only a user's rejection decides for the group")

	userReject := duplicateCandidateByPair(t, st, a, b)
	rejection, err = st.RejectIdentityMatchCandidateContext(t.Context(), userReject.ID, "user", nil)
	require.NoError(err)
	assert.Empty(rejection.AlsoRejected)
	assert.Equal(store.IdentityMatchStateAccepted, duplicateCandidateByPair(t, st, a, c).State,
		"an accepted row is never changed")
	assert.Equal(store.IdentityMatchStateRejected, duplicateCandidateByPair(t, st, b, c).State)
}

func TestRetireStalePersonDuplicateCandidatesWithdrawsOnlyUndecidedNameCandidates(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	insert := func(left, right int64, basis store.IdentityMatchBasis, value string) store.IdentityMatchCandidate {
		t.Helper()
		sourceRef := store.PersonDuplicateSourceRef
		confidence := 0.4
		candidate, _, err := st.UpsertIdentityMatchCandidateContext(t.Context(), store.IdentityMatchCandidateInput{
			LeftKind: store.IdentityMatchParticipant, LeftID: left,
			RightKind: store.IdentityMatchParticipant, RightID: right,
			Basis: basis, NormalizedValue: &value, State: store.IdentityMatchStateCandidate,
			Confidence: &confidence, Source: store.ProvenanceSystem, SourceRef: &sourceRef,
		})
		require.NoError(err)
		_, err = st.DB().Exec(`INSERT INTO person_duplicate_judgments
			(left_participant_id, right_participant_id, inputs_fingerprint, probability, model)
			VALUES (?, ?, 'legacy', 0.4, 'jev-test')`, left, right)
		require.NoError(err)
		return *candidate
	}
	// Proposed under the old rules on a bare first name.
	stale := insert(
		duplicateParticipant(t, st, "michael@example.com", ""),
		duplicateParticipant(t, st, "michael@example.net", ""),
		store.IdentityMatchDisplayName, "michael")
	// A personal address name now on three clusters.
	common := insert(
		duplicateParticipant(t, st, "lee.example@example.com", ""),
		duplicateParticipant(t, st, "lee.example@example.net", ""),
		store.IdentityMatchDisplayName, "lee.example")
	duplicateParticipant(t, st, "lee.example@example.org", "")
	// Still a qualifying name pair.
	kept := insert(
		duplicateParticipant(t, st, "jane@example.com", "Jane Doe"),
		duplicateParticipant(t, st, "jdoe@example.org", "Jane Doe"),
		store.IdentityMatchDisplayName, "doe jane")
	// Rejected by the user under the old rules.
	decided := insert(
		duplicateParticipant(t, st, "scott@example.com", ""),
		duplicateParticipant(t, st, "scott@example.net", ""),
		store.IdentityMatchDisplayName, "scott")
	_, err := st.DecideIdentityMatchCandidateContext(t.Context(), decided.ID, store.IdentityMatchStateRejected, "user", nil)
	require.NoError(err)
	// Decided in code on another basis.
	exact := insert(
		duplicateParticipant(t, st, "riley@example.com", ""),
		duplicateParticipant(t, st, "riley@example.net", ""),
		store.IdentityMatchPhone, "+15555550100")

	retired, err := st.RetireStalePersonDuplicateCandidatesContext(t.Context())
	require.NoError(err)
	assert.Equal(2, retired)

	candidates, err := st.ListPersonDuplicateCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	states := map[int64]store.IdentityMatchState{}
	for _, candidate := range candidates {
		states[candidate.ID] = candidate.State
	}
	assert.Equal(map[int64]store.IdentityMatchState{
		kept.ID:    store.IdentityMatchStateCandidate,
		decided.ID: store.IdentityMatchStateRejected,
		exact.ID:   store.IdentityMatchStateCandidate,
	}, states, "stale candidates leave review without becoming rejections")
	assert.NotContains(states, stale.ID)
	assert.NotContains(states, common.ID)

	var judgments int
	require.NoError(st.DB().QueryRow(`SELECT COUNT(*) FROM person_duplicate_judgments
		WHERE left_participant_id IN (?, ?)`, stale.LeftID, common.LeftID).Scan(&judgments))
	assert.Equal(0, judgments, "a retired pair is asked afresh if it qualifies again")

	again, err := st.RetireStalePersonDuplicateCandidatesContext(t.Context())
	require.NoError(err)
	assert.Equal(0, again)
}
