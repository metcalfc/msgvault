package store_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func duplicateParticipant(t *testing.T, st *store.Store, email, name string) int64 {
	t.Helper()
	id, err := st.EnsureParticipant(email, name, "")
	require.NoError(t, err)
	return id
}

func proposalPairs(proposals []store.PersonDuplicateProposal) [][2]int64 {
	pairs := make([][2]int64, 0, len(proposals))
	for _, proposal := range proposals {
		pairs = append(pairs, [2]int64{proposal.Left.ParticipantID, proposal.Right.ParticipantID})
	}
	return pairs
}

func TestPersonDuplicateProposalsFindSharedNamesAndLocalParts(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	jane := duplicateParticipant(t, st, "jane@example.com", "Jane Doe")
	janeWork := duplicateParticipant(t, st, "jdoe@example.org", "Doe, Jane")
	robin := duplicateParticipant(t, st, "robin.example@example.com", "Robin")
	robinWork := duplicateParticipant(t, st, "robin.example+news@example.net", "R.")
	duplicateParticipant(t, st, "casey@example.com", "Casey Example")
	duplicateParticipant(t, st, "info@example.com", "Info")
	duplicateParticipant(t, st, "info@example.org", "Info")

	proposals, err := st.PersonDuplicateProposalsContext(t.Context(), 0)
	require.NoError(err)
	assert.ElementsMatch([][2]int64{{jane, janeWork}, {robin, robinWork}}, proposalPairs(proposals),
		"role local parts and single-word names propose nothing")
	for _, proposal := range proposals {
		assert.NotEmpty(proposal.Fingerprint)
		switch proposal.Left.ParticipantID {
		case jane:
			assert.Equal([]store.PersonDuplicateSignal{store.PersonDuplicateSameName}, proposal.Signals)
			assert.Equal("doe jane", proposal.SharedValue)
			assert.Equal([]string{"Jane Doe"}, proposal.Left.Names)
			assert.Equal([]string{"Doe, Jane"}, proposal.Right.Names)
			assert.Equal([]string{"jdoe@example.org"}, proposal.Right.Addresses)
		case robin:
			assert.Equal([]store.PersonDuplicateSignal{store.PersonDuplicateSameLocalPart}, proposal.Signals)
			assert.Equal("robin.example", proposal.SharedValue)
			assert.Empty(proposal.Left.Names)
		}
	}
}

func TestPersonDuplicateProposalsLeaveOutOwnersNonPeopleAndCommonNames(t *testing.T) {
	require := require.New(t)
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("gmail", "owner@example.com")
	require.NoError(err)
	duplicateParticipant(t, st, "owner@example.com", "Avery Owner")
	duplicateParticipant(t, st, "avery@example.org", "Avery Owner")
	require.NoError(st.AddAccountIdentityContext(t.Context(), source.ID, "owner@example.com", "manual"))

	desk := duplicateParticipant(t, st, "desk@example.com", "Riley Desk")
	duplicateParticipant(t, st, "riley@example.org", "Riley Desk")
	_, err = st.SetCorrespondentKindContext(t.Context(), store.SetCorrespondentKindInput{
		ParticipantID: desk, Kind: correspondentkind.Organization,
	})
	require.NoError(err)

	for i := range 6 {
		duplicateParticipant(t, st, fmt.Sprintf("sam%d@example.com", i), "Sam Common")
	}
	duplicateParticipant(t, st, "news@example.com", "Example Support Team")
	duplicateParticipant(t, st, "help@example.org", "Example Support Team")

	proposals, err := st.PersonDuplicateProposalsContext(t.Context(), 0)
	require.NoError(err)
	assert.Empty(t, proposalPairs(proposals))
}

func TestPersonDuplicateProposalsSkipLinkedBoundAndDecidedPairs(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	linkedA := duplicateParticipant(t, st, "morgan@example.com", "Morgan Example")
	linkedB := duplicateParticipant(t, st, "morgan@example.org", "Morgan Example")
	_, err := st.LinkParticipants(linkedA, linkedB)
	require.NoError(err)

	boundA := duplicateParticipant(t, st, "taylor@example.com", "Taylor Example")
	boundB := duplicateParticipant(t, st, "tay@example.org", "Taylor Example")
	person, _, err := st.CreatePersonFromParticipantContext(t.Context(), boundA)
	require.NoError(err)
	_, err = st.DB().Exec(`INSERT INTO person_participants (person_id, participant_id) VALUES (?, ?)`, person.ID, boundB)
	require.NoError(err)

	rejectedA := duplicateParticipant(t, st, "jordan@example.com", "Jordan Example")
	rejectedB := duplicateParticipant(t, st, "jordan.e@example.org", "Jordan Example")
	value := "jordan example"
	candidate, _, err := st.UpsertIdentityMatchCandidateContext(t.Context(), store.IdentityMatchCandidateInput{
		LeftKind: store.IdentityMatchParticipant, LeftID: rejectedA,
		RightKind: store.IdentityMatchParticipant, RightID: rejectedB,
		Basis: store.IdentityMatchDisplayName, NormalizedValue: &value,
		State: store.IdentityMatchStateCandidate, Source: store.ProvenanceArchiveObservation,
	})
	require.NoError(err)
	_, err = st.DecideIdentityMatchCandidateContext(t.Context(), candidate.ID,
		store.IdentityMatchStateRejected, "user", nil)
	require.NoError(err)

	openA := duplicateParticipant(t, st, "alex@example.com", "Alex Example")
	openB := duplicateParticipant(t, st, "alex.e@example.org", "Alex Example")

	proposals, err := st.PersonDuplicateProposalsContext(t.Context(), 0)
	require.NoError(err)
	assert.Equal([][2]int64{{openA, openB}}, proposalPairs(proposals))
}

func TestRecordPersonDuplicateJudgmentsWritesReviewableCandidatesOnly(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	likelyA := duplicateParticipant(t, st, "jane@example.com", "Jane Doe")
	likelyB := duplicateParticipant(t, st, "jdoe@example.org", "Jane Doe")
	unlikelyA := duplicateParticipant(t, st, "chris@example.com", "Chris Lee")
	unlikelyB := duplicateParticipant(t, st, "clee@example.org", "Chris Lee")

	proposals, err := st.PersonDuplicateProposalsContext(t.Context(), 0)
	require.NoError(err)
	require.Len(proposals, 2)
	judgments := make([]store.PersonDuplicateJudgment, 0, len(proposals))
	for _, proposal := range proposals {
		probability := 0.1
		if proposal.Left.ParticipantID == likelyA {
			probability = 0.83
		}
		judgments = append(judgments, store.PersonDuplicateJudgment{
			Proposal: proposal, Probability: probability, Model: "jev-test", Propose: probability >= 0.30,
		})
	}
	result, err := st.RecordPersonDuplicateJudgmentsContext(t.Context(), judgments)
	require.NoError(err)
	assert.Equal(store.PersonDuplicateWriteResult{Recorded: 2, Candidates: 1}, result)

	candidates, err := st.ListPersonDuplicateCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	require.Len(candidates, 1)
	candidate := candidates[0]
	assert.Equal(store.IdentityMatchParticipant, candidate.LeftKind)
	assert.Equal(likelyA, candidate.LeftID)
	assert.Equal(likelyB, candidate.RightID)
	assert.Equal(store.IdentityMatchDisplayName, candidate.Basis)
	assert.Equal(store.IdentityMatchStateCandidate, candidate.State, "never accepted automatically")
	assert.Equal(store.ProvenanceSystem, candidate.Source)
	require.NotNil(candidate.SourceRef)
	assert.Equal(store.PersonDuplicateSourceRef, *candidate.SourceRef)
	require.NotNil(candidate.Confidence)
	assert.InDelta(0.83, *candidate.Confidence, 1e-9)
	require.NotNil(candidate.NormalizedValue)
	assert.Equal("doe jane", *candidate.NormalizedValue)
	require.Len(candidate.Evidence, 2)
	assert.Equal(store.PersonDuplicateEvidenceKind, candidate.Evidence[0].EvidenceKind)

	again, err := st.PersonDuplicateProposalsContext(t.Context(), 0)
	require.NoError(err)
	assert.Empty(again, "judged pairs are not proposed again until they change")

	third := duplicateParticipant(t, st, "chris.lee@example.net", "Chris Lee")
	grown, err := st.PersonDuplicateProposalsContext(t.Context(), 0)
	require.NoError(err)
	assert.ElementsMatch([][2]int64{{unlikelyA, third}, {unlikelyB, third}}, proposalPairs(grown),
		"only the new pairs are asked")

	alias := duplicateParticipant(t, st, "chris.alias@example.com", "")
	_, err = st.LinkParticipants(unlikelyA, alias)
	require.NoError(err)
	changed, err := st.PersonDuplicateProposalsContext(t.Context(), 0)
	require.NoError(err)
	pairs := proposalPairs(changed)
	assert.Contains(pairs, [2]int64{unlikelyA, unlikelyB}, "a pair whose cluster changed is asked again")
	assert.NotContains(pairs, [2]int64{likelyA, likelyB}, "a pair with a candidate is never proposed again")

	all, err := st.ListIdentityMatchCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	assert.Len(all, 1)
}

func TestPersonDuplicateCandidateLinksOnlyOnAUserAccept(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	left := duplicateParticipant(t, st, "jane@example.com", "Jane Doe")
	right := duplicateParticipant(t, st, "jdoe@example.org", "Jane Doe")
	proposals, err := st.PersonDuplicateProposalsContext(t.Context(), 0)
	require.NoError(err)
	require.Len(proposals, 1)
	_, err = st.RecordPersonDuplicateJudgmentsContext(t.Context(), []store.PersonDuplicateJudgment{{
		Proposal: proposals[0], Probability: 0.9, Model: "jev-test", Propose: true,
	}})
	require.NoError(err)
	candidates, err := st.ListPersonDuplicateCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	require.Len(candidates, 1)

	_, _, err = st.AcceptIdentityMatchCandidateContext(t.Context(), candidates[0].ID, "system", nil)
	require.ErrorIs(err, store.ErrIdentityMatchNotAcceptable, "a display-name match is never accepted by the system")
	clusters, err := st.ParticipantClusters()
	require.NoError(err)
	_, linked := clusters[left]
	assert.False(linked, "nothing is linked before the user accepts")

	accepted, _, err := st.AcceptIdentityMatchCandidateContext(t.Context(), candidates[0].ID, "user", nil)
	require.NoError(err)
	assert.Equal(store.IdentityMatchStateAccepted, accepted.State)
	clusters, err = st.ParticipantClusters()
	require.NoError(err)
	require.Contains(clusters, left)
	assert.Equal(clusters[left], clusters[right], "the user's accept links the two identities")
}

func TestRecordPersonDuplicateJudgmentsRejectsMalformedInput(t *testing.T) {
	st := testutil.NewTestStore(t)
	_, err := st.RecordPersonDuplicateJudgmentsContext(t.Context(), []store.PersonDuplicateJudgment{{
		Proposal: store.PersonDuplicateProposal{
			Left:  store.PersonDuplicateIdentity{ParticipantID: 5},
			Right: store.PersonDuplicateIdentity{ParticipantID: 3}, Fingerprint: "x",
		},
		Probability: 0.5, Model: "jev-test",
	}})
	assert.ErrorIs(t, err, store.ErrPersonDuplicateInvalid)
}
