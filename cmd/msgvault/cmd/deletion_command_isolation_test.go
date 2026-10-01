package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeletionCommandConstructorsIsolateInvocationOptions(t *testing.T) {
	plans := make(chan daemonCLIDeleteStagedPlanTestRequest, 2)
	server, executions, _ := newDaemonCLIDeleteStagedTestServer(t, func(request daemonCLIDeleteStagedPlanTestRequest) {
		plans <- request
	}, nil, nil)
	ctx := configureRemoteDaemonForTest(t, server.URL)
	first := newDeleteStagedCommand()
	first.SetContext(ctx)
	first.SetArgs([]string{"--dry-run", "--account", "first@example.test", "first-batch"})
	// Constructing another command must not reset flags already parsed on the first.
	require.NoError(t, first.ParseFlags([]string{"--dry-run", "--account", "first@example.test"}))
	second := newDeleteStagedCommand()
	second.SetContext(ctx)
	second.SetArgs([]string{"second-batch"})
	require.NoError(t, first.Execute())
	require.NoError(t, second.Execute())
	firstPlan, secondPlan := <-plans, <-plans
	assert.True(t, firstPlan.DryRun)
	assert.Equal(t, "first@example.test", firstPlan.Account)
	assert.Equal(t, "first-batch", firstPlan.BatchID)
	assert.False(t, secondPlan.DryRun)
	assert.Empty(t, secondPlan.Account)
	assert.Equal(t, "second-batch", secondPlan.BatchID)
	assert.Zero(t, executions.Load(), "planning alone must not execute deletions")
}
