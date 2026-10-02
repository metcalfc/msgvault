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
	"strings"

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
	EventKindJev bool                        `json:"event_kind_jev"`
	Assignees    meetingjudge.AssigneeReport `json:"assignees"`
	// AssigneeJev is false when [jev.meeting_action_assignee] is off.
	AssigneeJev bool `json:"assignee_jev"`
}

func newMeetingsJudgeCommand() *cobra.Command {
	var limit int
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "judge",
		Short: "Ask Jev what kind of meeting each calendar series is and who owns action items",
		Long: `Event kinds: visits calendar series (a recurring series or a standalone event)
that have no kind yet. Two rules record a series without asking anyone,
whether or not Jev is enabled:

  - None of its events is a meeting (cancelled, declined, out of office,
    focus time, working location, or marked free).
  - Its invite lists settle the kind: you organized every event and no one
    else is invited (a personal hold), or every event is a timed meeting you
    organized for you and exactly one other person (a one-on-one).

A rule kind is decided again after calendar sync changes the series. When
[jev] and [jev.meeting_event_kind] are enabled, an API key resolves, and
'msgvault jev consent meeting_event_kind' has been given, the rest are sent
to Jev ten series per request, each asked once. A Jev kind at or above 0.60
sets how much the series counts as a meeting in relationship rankings;
otherwise the attendee count does.

Action item assignees: when [jev.meeting_action_assignee] is enabled and
consented, meeting action items the meeting tool left without an assignee
are sent to Jev one meeting at a time, with the meeting title and attendee
labels. An attendee or you at 0.80 or more is stored as the inferred
assignee; the meeting tool's own assignee is never replaced.`,
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
	command.Flags().IntVar(&limit, "limit", 0,
		"Visit at most this many calendar series and this many meetings (0 means all)")
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
	assigneeJudge, err := newJevAssigneeJudge(cfg, st)
	if err != nil {
		return report, err
	}
	report.AssigneeJev = assigneeJudge != nil
	assigneeOptions := meetingjudge.AssigneeOptions{Limit: limit, Automatic: automatic, Logger: logger}
	if assigneeJudge != nil {
		assigneeOptions.Judge = assigneeJudge
	}
	report.Assignees, err = meetingjudge.RunAssignees(ctx, st, assigneeOptions)
	if err != nil {
		return report, err
	}
	return report, nil
}

func writeMeetingsJudgeReport(w io.Writer, report meetingsJudgeReport) {
	kinds := report.EventKinds
	_, _ = fmt.Fprintf(w, "Calendar series visited: %d\n", kinds.Candidates)
	_, _ = fmt.Fprintf(w, "Not meetings (recorded without Jev): %d\n", kinds.NotMeetings)
	_, _ = fmt.Fprintf(w, "Settled by invite list (recorded without Jev): %d\n", kinds.Settled)
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
	assignees := report.Assignees
	switch {
	case !report.AssigneeJev:
		_, _ = fmt.Fprintln(w, "Action item assignees: Jev off")
	case assignees.Skipped != "":
		_, _ = fmt.Fprintf(w, "Action item assignees: skipped:%s after %d request(s)\n",
			assignees.Skipped, assignees.Requests)
	default:
		_, _ = fmt.Fprintf(w,
			"Action item assignees: %d meeting(s), %d request(s); %d to an attendee, %d to you, %d unclear\n",
			assignees.Meetings, assignees.Requests, assignees.Attendees, assignees.Owner, assignees.Unclear)
	}
	if assignees.TooManyAttendees > 0 {
		_, _ = fmt.Fprintf(w, "Meetings not sent (more than %d attendees): %d\n",
			meetingjudge.MaxAssigneeAttendees, assignees.TooManyAttendees)
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
	parts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		parts = append(parts, fmt.Sprintf("%d %s", counts[kind], kind))
	}
	return strings.Join(parts, ", ")
}
