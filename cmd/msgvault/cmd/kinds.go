package cmd

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/kindclassify"
)

// newKindsCommand groups correspondent kind maintenance. Classification
// runs inside the daemon, which owns the archive and the [jev] snapshot.
func newKindsCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "kinds",
		Short: "Classify archive identities as people, lists, shared mailboxes, or automated senders",
		Long: `Correspondent kinds say which archive identities are people. A user decision
(msgvault person kind set) always wins. Below it, deterministic rules and, when
[jev.correspondent_kind] is enabled and consented, Jev judgments classify the
rest. Automated senders and mailing lists leave People lists, relationship
rankings, and enrichment; an unclear judgment only leaves rankings and waits
for review in msgvault person kind list --kind unclear.`,
	}
	command.AddCommand(newKindsBuildCommand())
	return command
}

func newKindsBuildCommand() *cobra.Command {
	var minMessages int64
	var limit int
	var rulesOnly bool
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "build",
		Short: "Classify unclassified identities above a message floor",
		Long: `Visits identity clusters with at least --min-messages messages (sent by them
plus sent to them by you) that no user, rule, or Jev judgment has classified.
Rules decide what they can from message metadata and the headers of a few
sampled messages. When [jev] and [jev.correspondent_kind] are enabled, an API
key resolves, and 'msgvault jev consent correspondent_kind' has been given, the
remainder is sent to Jev ten identities per request. Without Jev, the rules
run alone. Your own identities are never classified or sent.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if minMessages < 1 {
				return usageErr(command, errors.New("--min-messages must be at least 1"))
			}
			if limit < 0 {
				return usageErr(command, errors.New("--limit must not be negative"))
			}
			if !isDaemonCLISubprocess() {
				proxied, err := daemonCLIArgsFromCobra(command, args)
				if err != nil {
					return err
				}
				return runDaemonCLICommandHTTPWithEnv(command, proxied, nil, false, false)
			}
			state := invocationFromContext(command.Context())
			if state == nil || state.cfg == nil {
				return errors.New("configuration is unavailable")
			}
			st, cleanup, err := openWritableStoreAndInitForInvocation(state)
			if err != nil {
				return err
			}
			defer cleanup()
			options := kindclassify.Options{MinMessages: minMessages, Limit: limit}
			if !rulesOnly {
				judge, err := newJevKindJudge(state.cfg, st)
				if err != nil {
					return err
				}
				options.Judge = judge
			}
			report, err := kindclassify.Run(command.Context(), st, options)
			if err != nil {
				return err
			}
			if jsonOutput {
				return json.MarshalEncode(jsontext.NewEncoder(command.OutOrStdout()), report, json.Deterministic(true))
			}
			writeKindsReport(command.OutOrStdout(), report, options.Judge != nil)
			return nil
		},
	}
	command.Flags().Int64Var(&minMessages, "min-messages", kindclassify.DefaultMinMessages,
		"Only classify identities with at least this many messages")
	command.Flags().IntVar(&limit, "limit", 0, "Classify at most this many identities (0 means all)")
	command.Flags().BoolVar(&rulesOnly, "rules-only", false, "Apply the deterministic rules only; never ask Jev")
	command.Flags().BoolVar(&jsonOutput, flagJSON, false, "Output structured JSON")
	return command
}

func writeKindsReport(w io.Writer, report kindclassify.Report, jevWired bool) {
	_, _ = fmt.Fprintf(w, "Identities visited: %d\n", report.Candidates)
	_, _ = fmt.Fprintf(w, "Rules decided: %s\n", kindCounts(report.Rule))
	switch {
	case !jevWired:
		_, _ = fmt.Fprintln(w, "Jev: off (rules only)")
	case report.Jev.Skipped != "":
		_, _ = fmt.Fprintf(w, "Jev: skipped:%s after %d request(s); judged %d\n",
			report.Jev.Skipped, report.Jev.Requests, report.Jev.Judged)
	default:
		_, _ = fmt.Fprintf(w, "Jev: %d request(s), judged %d: %s\n",
			report.Jev.Requests, report.Jev.Judged, kindCounts(report.Jev.Kinds))
	}
	_, _ = fmt.Fprintf(w, "Left unclassified: %d\n", report.Undecided)
	if report.Jev.Kinds[correspondentkind.Unclear] > 0 {
		_, _ = fmt.Fprintln(w, "Review unclear identities with: msgvault person kind list --kind unclear")
	}
}

func kindCounts(counts map[correspondentkind.Kind]int) string {
	kinds := make([]correspondentkind.Kind, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	if len(kinds) == 0 {
		return "none"
	}
	slices.Sort(kinds)
	text := ""
	for i, kind := range kinds {
		if i > 0 {
			text += ", "
		}
		text += fmt.Sprintf("%d %s", counts[kind], kind)
	}
	return text
}

func init() {
	rootCmd.AddCommand(newKindsCommand())
}
