package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/query"
)

func newListSendersCommand() *cobra.Command {
	var options aggregateFlags
	var senderKind string
	command := &cobra.Command{
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
			if senderKind != "" {
				kind := correspondentkind.Kind(senderKind)
				if !kind.Known() || kind == correspondentkind.Person {
					return usageErr(cmd, fmt.Errorf(
						"--kind must be organization, shared_mailbox, ignored, automated, mailing_list, or unclear, got %q",
						senderKind))
				}
			}
			return runAggregateListCommand(cmd, options, query.ViewSenders, "No senders found.", "Sender", "sender",
				func(opts *query.AggregateOptions) { opts.SenderKind = senderKind })
		},
	}
	addCommonAggregateFlags(command, &options)
	command.Flags().StringVar(&senderKind, "kind", "", "Only senders of this correspondent kind (organization, shared_mailbox, ignored, automated, mailing_list, unclear)")
	return command
}

func init() { registerCommandFactory(newListSendersCommand) }
