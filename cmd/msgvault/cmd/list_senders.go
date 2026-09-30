package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/query"
)

var listSendersCmd = &cobra.Command{
	Use:   "list-senders",
	Short: "List top senders by message count",
	Long: `List email senders ranked by message count, size, or attachment size.

Use this command to see who sends you the most email. Results can be filtered
by date range and correspondent kind, and output as JSON for programmatic use.

--kind lists only senders whose identity is classified as that kind (see
'msgvault kinds build' and 'msgvault person kind set'): organization,
shared_mailbox, ignored, automated, mailing_list, or unclear.

Examples:
  msgvault list-senders --limit 20
  msgvault list-senders --after 2024-01-01 --before 2024-06-01
  msgvault list-senders --kind automated
  msgvault list-senders --json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if listSendersKind != "" {
			kind := correspondentkind.Kind(listSendersKind)
			if !kind.Known() || kind == correspondentkind.Person {
				return usageErr(cmd, fmt.Errorf(
					"--kind must be organization, shared_mailbox, ignored, automated, mailing_list, or unclear, got %q",
					listSendersKind))
			}
		}
		return runAggregateListCommand(cmd, query.ViewSenders, "No senders found.", "Sender", "sender",
			func(opts *query.AggregateOptions) { opts.SenderKind = listSendersKind })
	},
}

// listSendersKind is the --kind filter; the daemon resolves it against the
// archive's current classifications.
var listSendersKind string

func init() {
	rootCmd.AddCommand(listSendersCmd)
	addCommonAggregateFlags(listSendersCmd)
	listSendersCmd.Flags().StringVar(&listSendersKind, "kind", "",
		"Only senders of this correspondent kind (organization, shared_mailbox, ignored, automated, mailing_list, unclear)")
}
