package cmd

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/meetingjudge"
	"go.kenn.io/msgvault/internal/meetingweight"
	"go.kenn.io/msgvault/internal/store"
)

// meetingsJudgeReport is what `meetings judge` reports. It never contains
// state content.
type meetingsJudgeReport struct {
	EventKinds meetingjudge.EventKindReport `json:"event_kinds"`
	// EventKindJev is false when [jev.meeting_event_kind] is off.
	EventKindJev bool `json:"event_kind_jev"`
}

func newMeetingsJudgeCommand() *cobra.Command {
	var limit int
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "judge",
		Short: "Ask Jev what kind of meeting each calendar series is",
		Long: `Visits calendar series (a recurring series or a standalone event) that have no
kind yet. A series none of whose events is a meeting (cancelled, declined,
out of office, focus time, working location, or marked free) is recorded
without asking anyone. When [jev] and [jev.meeting_event_kind] are enabled,
an API key resolves, and 'msgvault jev consent meeting_event_kind' has been
given, the rest are sent to Jev ten series per request, each asked once. A
kind at or above 0.60 sets how much the series counts as a meeting in
relationship rankings; below it the attendee count does.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
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
			report, err := runMeetingsJudge(command.Context(), state.cfg, st, limit, false, nil)
			if err != nil {
				return err
			}
			if jsonOutput {
				return json.MarshalEncode(jsontext.NewEncoder(command.OutOrStdout()), report, json.Deterministic(true))
			}
			writeMeetingsJudgeReport(command.OutOrStdout(), report)
			return nil
		},
	}
	command.Flags().IntVar(&limit, "limit", 0, "Visit at most this many calendar series (0 means all)")
	command.Flags().BoolVar(&jsonOutput, flagJSON, false, "Output structured JSON")
	return command
}

// runMeetingsJudge runs the meeting judgments that are wired. automatic marks
// unattended callers such as the cache build.
func runMeetingsJudge(
	ctx context.Context, cfg *config.Config, st *store.Store, limit int, automatic bool, logger *slog.Logger,
) (meetingsJudgeReport, error) {
	var report meetingsJudgeReport
	judge, err := newJevEventKindJudge(cfg, st)
	if err != nil {
		return report, err
	}
	report.EventKindJev = judge != nil
	options := meetingjudge.EventKindOptions{Limit: limit, Automatic: automatic, Logger: logger}
	if judge != nil {
		options.Judge = judge
	}
	report.EventKinds, err = meetingjudge.RunEventKinds(ctx, st, options)
	if err != nil {
		return report, err
	}
	return report, nil
}

func writeMeetingsJudgeReport(w io.Writer, report meetingsJudgeReport) {
	kinds := report.EventKinds
	_, _ = fmt.Fprintf(w, "Calendar series visited: %d\n", kinds.Candidates)
	_, _ = fmt.Fprintf(w, "Not meetings (recorded without Jev): %d\n", kinds.NotMeetings)
	switch {
	case !report.EventKindJev:
		_, _ = fmt.Fprintln(w, "Event kinds: Jev off")
	case kinds.Skipped != "":
		_, _ = fmt.Fprintf(w, "Event kinds: skipped:%s after %d request(s); judged %d\n",
			kinds.Skipped, kinds.Requests, kinds.Judged)
	default:
		_, _ = fmt.Fprintf(w, "Event kinds: %d request(s), judged %d (%d confident): %s\n",
			kinds.Requests, kinds.Judged, kinds.Confident, eventKindCounts(kinds.Kinds))
	}
}

func eventKindCounts(counts map[meetingweight.Kind]int) string {
	kinds := make([]meetingweight.Kind, 0, len(counts))
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
