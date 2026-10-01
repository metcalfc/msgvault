package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogsCommandUsesDaemonRunner(t *testing.T) {
	assert := assert.New(t)

	server, requests := newDaemonCLIRunnerTestServer(t, func(req daemonCLIRunTestRequest) {
		assert.Equal([]string{
			"logs",
			"--all",
			"--grep=sync",
			"--level=warn",
			"--lines=25",
			"--run-id=abc123",
		}, req.Args, "args")
	}, `{"type":"stdout","data":"12:00:00 WARN abc123 sync failed\n"}`, `{"type":"stderr","data":"tail warning\n"}`, `{"type":"complete"}`)
	testCtx := configureRemoteDaemonForTest(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := newLogsCommand()
	cmd.SetContext(testCtx)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{
		"--all",
		"--grep", "sync",
		"--level", "warn",
		"--lines", "25",
		"--run-id", "abc123",
	})

	require.NoError(t, cmd.Execute(), "logs")
	assert.Equal(1, int(requests.Load()), "runner endpoint calls")
	assert.Equal("12:00:00 WARN abc123 sync failed\n", stdout.String(), "stdout")
	assert.Equal("tail warning\n", stderr.String(), "stderr")
}
