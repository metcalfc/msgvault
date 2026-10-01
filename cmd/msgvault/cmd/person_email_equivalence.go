package cmd

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/daemonclient"
	apiclient "go.kenn.io/msgvault/pkg/client"
	"go.kenn.io/msgvault/pkg/client/generated"
)

// newPersonLinkEquivalentAddressesCommand links archive addresses that
// deliver to the same mailbox. It goes through the daemon, which also runs
// the same pass after every successful sync or import.
func newPersonLinkEquivalentAddressesCommand() *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "link-equivalent-addresses",
		Short: "Link addresses that deliver to the same mailbox",
		Long: "Link archive addresses that deliver to the same mailbox, across the whole\n" +
			"archive. Anything after + is ignored on every domain, and Gmail also ignores\n" +
			"dots and treats googlemail.com as gmail.com. Non-Gmail addresses that differ\n" +
			"only by dots, and addresses on two different profiles, wait in Reviews.\n" +
			"Pairs you unlinked or rejected stay apart. Repeating the command is safe; the\n" +
			"daemon also runs it after each successful sync or import.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, _, err := OpenHTTPStore(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			resp, err := daemonclient.APIResponse(cmd.Context(), client,
				func(api *apiclient.Client) (*generated.LinkEquivalentEmailAddressesResp, error) {
					return api.LinkEquivalentEmailAddressesWithResponse(cmd.Context())
				})
			if err != nil {
				return err
			}
			if jsonOutput {
				return json.MarshalEncode(jsontext.NewEncoder(cmd.OutOrStdout()), resp.JSON200,
					json.Deterministic(true))
			}
			result := resp.JSON200
			_, err = fmt.Fprintf(cmd.OutOrStdout(),
				"Email participants: %d\nLinked: %d\nAlready linked: %d\n"+
					"Suggested for review: %d\nDifferent profiles (in Reviews): %d\n"+
					"Kept apart: %d\n",
				result.Participants, result.Linked, result.AlreadyLinked,
				result.Suggested, result.Conflicts, result.Suppressed)
			if err != nil {
				return fmt.Errorf("write equivalence result: %w", err)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&jsonOutput, flagJSON, false, "Output as JSON")
	return command
}
