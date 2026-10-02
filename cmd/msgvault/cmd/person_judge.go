package cmd

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/persondedup"
	"go.kenn.io/msgvault/internal/profilejudge"
	"go.kenn.io/msgvault/internal/store"
)

// personJudgeReport is what `person judge` reports. It never contains names,
// addresses, or other state.
type personJudgeReport struct {
	Duplicates persondedup.Report `json:"duplicates"`
	// DuplicatesJev is false when [jev.person_duplicates] is off.
	DuplicatesJev bool                `json:"duplicates_jev"`
	Profiles      profilejudge.Report `json:"profiles"`
	// ProfilesJev is false when [jev.person_profile_choices] is off.
	ProfilesJev bool `json:"profiles_jev"`
}

func newPersonJudgeCommand() *cobra.Command {
	var limit int
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "judge",
		Short: "Ask Jev about duplicate people and small profile choices",
		Long: `Duplicate people: code proposes pairs of identity clusters with an email
address that share an address delivering to the same mailbox, a phone number,
or a provider account, a display name (at least two words, in any order) on
different addresses, or the same distinctive address name (the part before @)
at different domains. Your own identities, clusters classified as anything but
a person, shared mailboxes, pairs already bound to one person, and pairs with
an existing identity match candidate or rejection are left out. A pair that
shares a mailbox, phone number, or provider account is decided in code: it
becomes a candidate under Reviews > Possible duplicate people without asking
Jev, whether or not Jev is on. For the other pairs, when [jev] and
[jev.person_duplicates] are enabled, an API key resolves, and 'msgvault jev
consent person_duplicates' has been given, pairs with a display name on both
sides are sent to Jev twenty per request with their display names and whether
each side's addresses are personal or at an organization, never the addresses.
A pair judged at least 0.30 likely to be one person becomes a candidate.
Nothing is linked or merged until you accept it. Each pair is taken up once
until either side changes.

Profile choices: code first settles what normalization decides, with or
without Jev and over every eligible item; --limit caps only what is sent to
Jev. Roles that are all the same organization and title, or names
that are all the same apart from case, spacing, and punctuation, keep the
rule's choice, preferring a single proper-case spelling such as "John Smith"
over "JOHN SMITH". A pending attribute conflict left by a person merge whose two
values are equal after the field's normalization is closed, keeping your
value if only the absorbed one is yours, else the survivor's. When
[jev.person_profile_choices] is enabled and consented, the rest is asked. A person with two to six current roles, all
found automatically and none pinned, is asked which role is primary; a
person promoted from identities that use two to six different names, whose
name has not changed since, is asked which name to show; and a pending
attribute conflict left by a person merge is asked whether both values say
the same thing. A role or name at 0.80 or more is written; two values at 0.95
or more keep the survivor's value. Your own values, choices, and pins are
never changed.`,
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
			report, err := runPersonJudge(command.Context(), state.cfg, st, limit, false, nil)
			if err != nil {
				return err
			}
			if jsonOutput {
				return json.MarshalEncode(jsontext.NewEncoder(command.OutOrStdout()), report, json.Deterministic(true))
			}
			writePersonJudgeReport(command.OutOrStdout(), report)
			return nil
		},
	}
	command.Flags().IntVar(&limit, "limit", 0,
		"Judge at most this many pairs, people per profile question, and merge conflicts (0 means all; code settlement of profile choices is not capped)")
	command.Flags().BoolVar(&jsonOutput, flagJSON, false, "Output structured JSON")
	return command
}

// runPersonJudge runs the person judgments that are wired. automatic marks
// unattended callers such as the cache build.
func runPersonJudge(
	ctx context.Context, cfg *config.Config, st *store.Store, limit int, automatic bool, logger *slog.Logger,
) (personJudgeReport, error) {
	var report personJudgeReport
	judge, err := newJevDuplicatePeopleJudge(cfg, st)
	if err != nil {
		return report, err
	}
	report.DuplicatesJev = judge != nil
	options := persondedup.Options{Limit: limit, Automatic: automatic, Logger: logger}
	if judge != nil {
		options.Judge = judge
	}
	report.Duplicates, err = persondedup.Run(ctx, st, options)
	if err != nil {
		return report, err
	}
	profilesJudge, err := newJevProfileChoicesJudge(cfg, st)
	if err != nil {
		return report, err
	}
	report.ProfilesJev = profilesJudge != nil
	profileOptions := profilejudge.Options{Limit: limit, Automatic: automatic, Logger: logger}
	if profilesJudge != nil {
		profileOptions.Judge = profilesJudge
	}
	report.Profiles, err = profilejudge.Run(ctx, st, profileOptions)
	if err != nil {
		return report, err
	}
	return report, nil
}

func writePersonJudgeReport(w io.Writer, report personJudgeReport) {
	duplicates := report.Duplicates
	_, _ = fmt.Fprintf(w, "Possible duplicate pairs: %d (%d decided in code)\n", duplicates.Proposals, duplicates.Matched)
	switch {
	case !report.DuplicatesJev:
		_, _ = fmt.Fprintf(w, "Duplicate people: Jev off; %d new candidate(s) for review\n", duplicates.Candidates)
	case duplicates.Skipped != "":
		_, _ = fmt.Fprintf(w, "Duplicate people: skipped:%s after %d request(s); judged %d, %d new candidate(s)\n",
			duplicates.Skipped, duplicates.Requests, duplicates.Judged, duplicates.Candidates)
	default:
		_, _ = fmt.Fprintf(w, "Duplicate people: %d request(s), judged %d, %d new candidate(s) for review\n",
			duplicates.Requests, duplicates.Judged, duplicates.Candidates)
	}
	profiles := report.Profiles
	switch {
	case !report.ProfilesJev:
		_, _ = fmt.Fprintf(w, "Profile choices: Jev off; %d settled in code\n", profiles.SettledInCode)
	default:
		skipped := ""
		if profiles.Skipped != "" {
			skipped = " (skipped:" + profiles.Skipped + ")"
		}
		_, _ = fmt.Fprintf(w,
			"Profile choices: %d settled in code; %d request(s)%s; primary roles %d judged, %d set; "+
				"display names %d judged, %d set; merge conflicts %d judged, %d settled\n",
			profiles.SettledInCode, profiles.Requests, skipped, profiles.PrimaryRoles, profiles.PrimaryRolesSet,
			profiles.DisplayNames, profiles.DisplayNamesSet, profiles.MergeConflicts, profiles.ConflictsSettled)
	}
}
