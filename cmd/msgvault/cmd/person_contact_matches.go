package cmd

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/daemonclient"
	"go.kenn.io/msgvault/internal/textutil"
	apiclient "go.kenn.io/msgvault/pkg/client"
	"go.kenn.io/msgvault/pkg/client/generated"
)

const contactMatchOrigin = "contact_match"

// newPersonContactMatchesCommand reviews contact profiles (for example,
// imported address-book cards) whose exact email or phone matches an archive
// identity. Every subcommand goes through the daemon.
func newPersonContactMatchesCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "contact-matches",
		Short: "Review contact profiles that match archive identities",
		Long: "Contact profiles with no archive identity (for example, imported contacts)\n" +
			"are matched to archive participants by exact email or phone. Each match is a\n" +
			"reviewable candidate: accepting a bind links the archive identity to the\n" +
			"contact profile, and a match whose identity belongs to another profile asks\n" +
			"for an explicit person merge. Nothing is accepted automatically.",
	}
	command.AddCommand(newPersonContactMatchesListCommand(),
		newPersonContactMatchesDecideCommand("accept"),
		newPersonContactMatchesDecideCommand("reject"),
		newPersonContactMatchesBuildCommand())
	return command
}

func newPersonContactMatchesListCommand() *cobra.Command {
	var state string
	var limit, offset int64
	var jsonOutput bool
	command := &cobra.Command{
		Use:   cmdUseList,
		Short: "List contact matches with both sides named",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, _, err := OpenHTTPStore(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			origin := contactMatchOrigin
			query := generated.ListIdentityMatchCandidatesQuery{Origin: &origin}
			if state = strings.TrimSpace(state); state != "" {
				query.State = &state
			}
			if limit > 0 {
				query.Limit = &limit
			}
			if offset > 0 {
				query.Offset = &offset
			}
			resp, err := daemonclient.APIResponse(client,
				func(api *apiclient.Client) (*generated.ListIdentityMatchCandidatesResp, error) {
					return api.ListIdentityMatchCandidatesWithResponse(cmd.Context(),
						&generated.ListIdentityMatchCandidatesRequestOptions{Query: &query})
				})
			if err != nil {
				return err
			}
			if jsonOutput {
				return json.MarshalEncode(jsontext.NewEncoder(cmd.OutOrStdout()), resp.JSON200,
					json.Deterministic(true))
			}
			return writeContactMatches(cmd, resp.JSON200)
		},
	}
	command.Flags().StringVar(&state, "state", "candidate",
		"Candidate state: candidate, accepted, rejected, or conflict (empty for all)")
	command.Flags().Int64Var(&limit, "limit", 0, "Maximum matches to list (default 100, max 500)")
	command.Flags().Int64Var(&offset, "offset", 0, "Zero-based match offset")
	command.Flags().BoolVar(&jsonOutput, flagJSON, false, "Output as JSON")
	return command
}

func writeContactMatches(cmd *cobra.Command, page *generated.IdentityMatchCandidatesResponse) error {
	endpoints := map[string]generated.IdentityMatchEndpointSummary{}
	for _, endpoint := range page.Endpoints {
		endpoints[fmt.Sprintf("%s:%d", endpoint.Kind, endpoint.ID)] = endpoint
	}
	statuses := map[int64]generated.ContactMatchStatus{}
	for _, status := range page.ContactMatches {
		statuses[status.CandidateID] = status
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tSTATE\tACTION\tMATCHED\tARCHIVE IDENTITY\tCONTACT PROFILE")
	for _, candidate := range page.Candidates {
		action := "-"
		if status, ok := statuses[candidate.ID]; ok {
			action = string(status.Classification)
			if status.BlockedReason != nil {
				action += " (blocked: " + string(*status.BlockedReason) + ")"
			}
		}
		matched := candidate.Basis
		if candidate.NormalizedValue != nil {
			matched += " " + *candidate.NormalizedValue
		}
		_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", candidate.ID, candidate.State,
			action, textutil.SanitizeTerminal(matched),
			contactMatchEndpointLabel(endpoints, candidate.LeftKind, candidate.LeftID),
			contactMatchEndpointLabel(endpoints, candidate.RightKind, candidate.RightID))
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush contact matches: %w", err)
	}
	return nil
}

func contactMatchEndpointLabel(
	endpoints map[string]generated.IdentityMatchEndpointSummary, kind string, id int64,
) string {
	endpoint, ok := endpoints[fmt.Sprintf("%s:%d", kind, id)]
	label := fmt.Sprintf("%s %d", kind, id)
	if !ok {
		return label
	}
	parts := []string{}
	if endpoint.DisplayName != nil && strings.TrimSpace(*endpoint.DisplayName) != "" {
		parts = append(parts, strings.TrimSpace(*endpoint.DisplayName))
	}
	if len(endpoint.Addresses) > 0 {
		parts = append(parts, "<"+endpoint.Addresses[0]+">")
	}
	if endpoint.PersonID != nil && kind != "person" {
		parts = append(parts, fmt.Sprintf("[person %d]", *endpoint.PersonID))
	}
	if len(parts) == 0 {
		return label
	}
	return textutil.SanitizeTerminal(strings.Join(parts, " ") + " (" + label + ")")
}

// contactMatchMergeRequired is the daemon's 409 body when accepting needs an
// explicit person merge first.
type contactMatchMergeRequired struct {
	Error    string `json:"error"`
	Message  string `json:"message"`
	Profiles []struct {
		Person struct {
			ID          int64   `json:"id"`
			Revision    int64   `json:"revision"`
			DisplayName *string `json:"display_name"`
		} `json:"person"`
	} `json:"profiles"`
}

func newPersonContactMatchesDecideCommand(decision string) *cobra.Command {
	var notes string
	var jsonOutput bool
	short := "Accept a contact match (bind the archive identity to the contact)"
	if decision == "reject" {
		short = "Reject a contact match so it is not proposed again"
	}
	command := &cobra.Command{
		Use:   decision + " <candidate-id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			candidateID, err := positivePersonCLIArg(cmd, args[0], "candidate")
			if err != nil {
				return err
			}
			client, _, err := OpenHTTPStore(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			body := generated.DecideIdentityMatchRequest{}
			if trimmed := strings.TrimSpace(notes); trimmed != "" {
				body.Notes = &trimmed
			}
			if decision == "reject" {
				resp, err := daemonclient.APIResponse(client,
					func(api *apiclient.Client) (*generated.RejectIdentityMatchCandidateResp, error) {
						return api.RejectIdentityMatchCandidateWithResponse(cmd.Context(),
							&generated.RejectIdentityMatchCandidateRequestOptions{
								PathParams: &generated.RejectIdentityMatchCandidatePath{ID: candidateID},
								Body:       &body,
							})
					})
				if err != nil {
					return err
				}
				if jsonOutput {
					return json.MarshalEncode(jsontext.NewEncoder(cmd.OutOrStdout()), resp.JSON200,
						json.Deterministic(true))
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Contact match %d: %s\n",
					resp.JSON200.Candidate.ID, resp.JSON200.Candidate.State)
				return nil
			}
			// The generated client does not decode the 409 merge offer, so the
			// raw body is read here to name both profiles.
			api, err := client.GeneratedClient()
			if err != nil {
				return err
			}
			resp, requestErr := api.AcceptIdentityMatchCandidateWithResponse(cmd.Context(),
				&generated.AcceptIdentityMatchCandidateRequestOptions{
					PathParams: &generated.AcceptIdentityMatchCandidatePath{ID: candidateID},
					Body:       &body,
				})
			if resp == nil {
				if requestErr == nil {
					requestErr = errors.New("accept contact match: empty response")
				}
				return requestErr
			}
			if resp.StatusCode == http.StatusConflict && len(resp.Body) > 0 {
				return contactMatchConflictError(candidateID, resp.Body)
			}
			if err := daemonclient.APIResponseError(resp, requestErr); err != nil {
				return err
			}
			if jsonOutput {
				return json.MarshalEncode(jsontext.NewEncoder(cmd.OutOrStdout()), resp.JSON200,
					json.Deterministic(true))
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"Contact match %d: %s\nIdentity revision: %d\nCache state: %s\n",
				resp.JSON200.Candidate.ID, resp.JSON200.Candidate.State,
				resp.JSON200.IdentityRevision, resp.JSON200.CacheState)
			return nil
		},
	}
	command.Flags().StringVar(&notes, "notes", "", "Optional decision notes")
	command.Flags().BoolVar(&jsonOutput, flagJSON, false, "Output as JSON")
	return command
}

// contactMatchConflictError turns a 409 into guidance: a required merge names
// both profiles and the exact merge command; any other conflict keeps the
// daemon's message.
func contactMatchConflictError(candidateID int64, body []byte) error {
	var conflict contactMatchMergeRequired
	if err := json.Unmarshal(body, &conflict); err != nil {
		return fmt.Errorf("contact match %d conflicts with current state", candidateID)
	}
	if conflict.Error != "person_merge_required" || len(conflict.Profiles) != 2 {
		message := strings.TrimSpace(conflict.Message)
		if message == "" {
			message = "the match conflicts with current state"
		}
		return fmt.Errorf("contact match %d: %s", candidateID, textutil.SanitizeTerminal(message))
	}
	lines := []string{fmt.Sprintf(
		"contact match %d needs an explicit person merge: the archive identity belongs to another profile.",
		candidateID)}
	for _, profile := range conflict.Profiles {
		name := fmt.Sprintf("Person %d", profile.Person.ID)
		if profile.Person.DisplayName != nil && strings.TrimSpace(*profile.Person.DisplayName) != "" {
			name = textutil.SanitizeTerminal(strings.TrimSpace(*profile.Person.DisplayName))
		}
		lines = append(lines, fmt.Sprintf("  person %d, revision %d: %s",
			profile.Person.ID, profile.Person.Revision, name))
	}
	first, second := conflict.Profiles[0].Person, conflict.Profiles[1].Person
	lines = append(lines,
		"Choose the survivor, merge, then accept again:",
		fmt.Sprintf("  msgvault person merge %d %d --survivor-revision %d --absorbed-revision %d",
			first.ID, second.ID, first.Revision, second.Revision),
		fmt.Sprintf("  msgvault person contact-matches accept %d", candidateID))
	return errors.New(strings.Join(lines, "\n"))
}

func newPersonContactMatchesBuildCommand() *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "build",
		Short: "Refresh contact matches now",
		Long:  "Refresh contact matches now. Refreshing never accepts a match.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, _, err := OpenHTTPStore(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			resp, err := daemonclient.APIResponse(client,
				func(api *apiclient.Client) (*generated.BuildContactMatchCandidatesResp, error) {
					return api.BuildContactMatchCandidatesWithResponse(cmd.Context())
				})
			if err != nil {
				return err
			}
			if jsonOutput {
				return json.MarshalEncode(jsontext.NewEncoder(cmd.OutOrStdout()), resp.JSON200,
					json.Deterministic(true))
			}
			result := resp.JSON200
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"Matches: %d (new %d, existing %d)\nBind: %d\nMerge: %d\nAmbiguous: %d\nBlocked: %d\nEvidence added: %d\n",
				result.Matches, result.Created, result.Existing, result.Bind, result.Merge,
				result.Ambiguous, result.Blocked, result.EvidenceAdded)
			return nil
		},
	}
	command.Flags().BoolVar(&jsonOutput, flagJSON, false, "Output as JSON")
	return command
}
