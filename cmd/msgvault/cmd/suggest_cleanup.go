package cmd

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/cleanupsuggest"
	"go.kenn.io/msgvault/internal/store"
)

// defaultSuggestCleanupShow is how many stored suggestions the command
// lists.
const defaultSuggestCleanupShow = 25

// suggestCleanupOutput is the --json shape.
type suggestCleanupOutput struct {
	Report      *cleanupsuggest.Report   `json:"report,omitempty"`
	JevWired    bool                     `json:"jev_wired"`
	MinScore    float64                  `json:"min_score"`
	Suggestions []suggestCleanupListItem `json:"suggestions"`
}

type suggestCleanupListItem struct {
	MessageID       int64              `json:"message_id"`
	Score           float64            `json:"score"`
	Category        string             `json:"category"`
	Categories      map[string]float64 `json:"category_probabilities"`
	Impersonation   float64            `json:"impersonation"`
	Pressure        float64            `json:"pressure"`
	KeepProbability float64            `json:"keep_probability"`
	Signals         []string           `json:"signals"`
	From            string             `json:"from"`
	Subject         string             `json:"subject"`
	SentAt          *time.Time         `json:"sent_at,omitempty"`
	Model           string             `json:"model"`
}

func newSuggestCleanupCommand() *cobra.Command {
	var limit, show int
	var minScore float64
	var listOnly, rejudge, jsonOutput bool
	command := &cobra.Command{
		Use:   "suggest-cleanup",
		Short: "Find likely phishing among spam and promotional mail (never stages or deletes)",
		Long: `Looks for junk worth deleting and lists it; it never stages or deletes anything.

The pool is live email labeled SPAM or CATEGORY_PROMOTIONS, in a conversation
you never wrote in, from a sender not classified as a person, with at least
one link. When [jev] and [jev.cleanup_suggestions] are enabled, an API key
resolves, and 'msgvault jev consent cleanup_suggestions' has been given, up to
--limit of those messages are sent to Jev four per request (sender name and
domain, reply-to domain, link hosts, SPF/DKIM/DMARC results, whether you were
a visible recipient, system labels, the sender's kind, the subject, and the
first 500 characters of the text). A score combines the answers with those
authentication results; messages at or above 0.80 are listed as suspected
phishing. To act on them, stage explicitly with msgvault stage-delete --ids.

--list-only skips judging and lists what is already stored.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if limit < 1 {
				return usageErr(command, errors.New("--limit must be at least 1"))
			}
			if show < 1 {
				return usageErr(command, errors.New("--show must be at least 1"))
			}
			if minScore < 0 || minScore > 1 {
				return usageErr(command, errors.New("--min-score must be between 0 and 1"))
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
			output := suggestCleanupOutput{MinScore: minScore}
			if !listOnly {
				judge, err := newJevCleanupJudge(state.cfg, st)
				if err != nil {
					return err
				}
				output.JevWired = judge != nil
				report, err := cleanupsuggest.Run(command.Context(), st, cleanupsuggest.Options{
					Limit: limit, Rejudge: rejudge, Judge: judge,
					TrustedAuthservIDs: state.cfg.Jev.CleanupSuggestions.TrustedAuthservIDs,
				})
				if err != nil {
					return err
				}
				output.Report = &report
			}
			rows, err := st.ListCleanupSuggestionsContext(command.Context(), store.CleanupSuggestionFilter{
				MinScore: minScore, Limit: show,
			})
			if err != nil {
				return err
			}
			output.Suggestions = make([]suggestCleanupListItem, 0, len(rows))
			for _, row := range rows {
				output.Suggestions = append(output.Suggestions, suggestCleanupListItem{
					MessageID: row.MessageID, Score: row.Score, Category: row.Category,
					Categories: row.CategoryProbabilities, Impersonation: row.Impersonation,
					Pressure: row.Pressure, KeepProbability: row.KeepProbability, Signals: row.Signals,
					From: formatSuggestionSender(row.FromName, row.FromEmail), Subject: row.Subject,
					SentAt: row.SentAt, Model: row.Model,
				})
			}
			if jsonOutput {
				return json.MarshalEncode(jsontext.NewEncoder(command.OutOrStdout()), output, json.Deterministic(true))
			}
			writeSuggestCleanup(command.OutOrStdout(), output)
			return nil
		},
	}
	command.Flags().IntVar(&limit, "limit", cleanupsuggest.DefaultLimit, "Judge at most this many pool messages")
	command.Flags().IntVar(&show, "show", defaultSuggestCleanupShow, "List at most this many stored suggestions")
	command.Flags().Float64Var(&minScore, "min-score", cleanupsuggest.SuspectThreshold,
		"List stored suggestions at or above this score")
	command.Flags().BoolVar(&listOnly, "list-only", false, "List stored suggestions without judging new messages")
	command.Flags().BoolVar(&rejudge, "rejudge", false, "Judge messages again even if they already have a suggestion")
	command.Flags().BoolVar(&jsonOutput, flagJSON, false, "Output structured JSON")
	return command
}

func formatSuggestionSender(name, email string) string {
	switch {
	case name != "" && email != "":
		return name + " <" + email + ">"
	case email != "":
		return email
	default:
		return name
	}
}

func writeSuggestCleanup(w io.Writer, output suggestCleanupOutput) {
	if report := output.Report; report != nil {
		_, _ = fmt.Fprintf(w, "Pool messages read: %d (%d from people and %d without links left out)\n",
			report.Visited, report.PersonSenders, report.NoLinks)
		switch {
		case !output.JevWired:
			_, _ = fmt.Fprintf(w, "Jev: off; %d message(s) are ready to judge once [jev.cleanup_suggestions] is enabled\n",
				report.Eligible)
		case report.Skipped != "":
			_, _ = fmt.Fprintf(w, "Jev: skipped:%s after %d request(s); judged %d of %d\n",
				report.Skipped, report.Requests, report.Judged, report.Eligible)
		default:
			_, _ = fmt.Fprintf(w, "Jev: %d request(s), judged %d: %d suspected phishing, %d possibly worth keeping\n",
				report.Requests, report.Judged, report.Suspected, report.Keep)
		}
	}
	if len(output.Suggestions) == 0 {
		_, _ = fmt.Fprintf(w, "No stored suggestions score %.2f or higher.\n", output.MinScore)
		return
	}
	heading := "Suspected phishing"
	if output.MinScore < cleanupsuggest.SuspectThreshold {
		heading = "Suggestions"
	}
	_, _ = fmt.Fprintf(w, "%s (score >= %.2f):\n", heading, output.MinScore)
	ids := make([]string, 0, len(output.Suggestions))
	for _, item := range output.Suggestions {
		signals := ""
		if len(item.Signals) > 0 {
			signals = " [" + strings.Join(item.Signals, ", ") + "]"
		}
		_, _ = fmt.Fprintf(w, "  %d  %.2f  %s  %s  %q%s\n",
			item.MessageID, item.Score, item.Category, item.From, item.Subject, signals)
		ids = append(ids, strconv.FormatInt(item.MessageID, 10))
	}
	_, _ = fmt.Fprintf(w, "Nothing was staged. To stage these after review: msgvault stage-delete --ids %s\n",
		strings.Join(ids, ","))
}

func init() {
	rootCmd.AddCommand(newSuggestCleanupCommand())
}
