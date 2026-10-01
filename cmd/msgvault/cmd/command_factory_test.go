package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// rootCmd is a test-only catalog, initialized after all factories register.
var rootCmd *cobra.Command

// freshCommandForTest selects a real command and detaches it for direct execution.
func freshCommandForTest(t *testing.T, root *cobra.Command, path ...string) *cobra.Command {
	t.Helper()
	command, remaining, err := root.Find(path)
	require.NoError(t, err)
	require.Empty(t, remaining)
	if parent := command.Parent(); parent != nil {
		parent.RemoveCommand(command)
	}
	return command
}
