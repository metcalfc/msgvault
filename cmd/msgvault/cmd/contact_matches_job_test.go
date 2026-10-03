package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/msgvault/internal/scheduler"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestRegisterContactMatchJobRefreshesCandidatesDaily(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	participantID, err := st.EnsureParticipant("dee@example.test", "Dee Sender", "example.test")
	require.NoError(err)
	personID := contactProfile(t, st, "card-dee", "Dee Contact", store.ContactAddressEmail, "dee@example.test")
	refreshes := 0
	st.SetIdentityDatasetsRefresher(func(context.Context) error {
		refreshes++
		return nil
	})

	sched := scheduler.New(func(context.Context, string) error { return nil })
	t.Cleanup(func() { <-sched.Stop().Done() })
	require.NoError(registerContactMatchJob(sched, st))
	require.True(sched.IsJobScheduled(contactMatchJob))

	require.NoError(sched.TriggerJob(contactMatchJob))
	jobs := sched.JobStatus()
	require.Len(jobs, 1)
	assert.Equal("41 4 * * *", jobs[0].Schedule)
	assert.Empty(jobs[0].LastError)
	assert.False(jobs[0].LastRun.IsZero(), "the refresh ran")

	candidates, err := st.ListContactMatchCandidatesContext(t.Context(), nil, 100, 0)
	require.NoError(err)
	require.Len(candidates, 1)
	assert.Equal(participantID, candidates[0].LeftID)
	assert.Equal(personID, candidates[0].RightID)
	assert.Equal(store.IdentityMatchStateAccepted, candidates[0].State,
		"the daily refresh links an exact email match")
	person, err := st.GetPersonContext(t.Context(), personID)
	require.NoError(err)
	assert.Equal([]int64{participantID}, person.ParticipantIDs)
	assert.Equal(1, refreshes, "the automatic link refreshes identity analytics")
}
