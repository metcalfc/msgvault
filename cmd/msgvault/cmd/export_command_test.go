package cmd

import (
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"strconv"
	"testing"
)

func newExportAttachmentTestCommand(t *testing.T, output string, jsonOutput, base64Output bool) *cobra.Command {
	t.Helper()
	cmd := newExportAttachmentCmd()
	require.NoError(t, cmd.Flags().Set("output", output))
	require.NoError(t, cmd.Flags().Set("json", strconv.FormatBool(jsonOutput)))
	require.NoError(t, cmd.Flags().Set("base64", strconv.FormatBool(base64Output)))
	return cmd
}
func newExportAttachmentsTestCommand(t *testing.T, output string) *cobra.Command {
	t.Helper()
	cmd := newExportAttachmentsCmd()
	require.NoError(t, cmd.Flags().Set("output", output))
	return cmd
}

func newCreateSubsetTestCommand(t *testing.T, output string, profiles, resources bool) *cobra.Command {
	t.Helper()
	cmd := newCreateSubsetCmd()
	require.NoError(t, cmd.Flags().Set("output", output))
	require.NoError(t, cmd.Flags().Set("rows", "1"))
	require.NoError(t, cmd.Flags().Set("include-profiles", strconv.FormatBool(profiles)))
	require.NoError(t, cmd.Flags().Set("include-vcard-resources", strconv.FormatBool(resources)))
	return cmd
}
