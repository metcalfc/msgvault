package cmd

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/daemonclient"
	"go.kenn.io/msgvault/internal/textutil"
	apiclient "go.kenn.io/msgvault/pkg/client"
	"go.kenn.io/msgvault/pkg/client/generated"
)

// newPersonEnrichmentReviewCommand decides enrichment attempts whose identity
// check was uncertain. Every subcommand goes through the daemon.
func newPersonEnrichmentReviewCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "review",
		Short: "Confirm or reject enrichment identities the identity check could not decide",
		Long: "Enrichment attempts whose identity check was uncertain wait here with no claim\n" +
			"applied. accept confirms the returned identity is this person: its claims are\n" +
			"applied as verified and its provider identity is attached. reject says it is\n" +
			"someone else: that provider identity is never proposed for this person again.",
	}
	command.AddCommand(newPersonEnrichmentReviewListCommand(),
		newPersonEnrichmentReviewDecideCommand(true),
		newPersonEnrichmentReviewDecideCommand(false))
	return command
}

func newPersonEnrichmentReviewListCommand() *cobra.Command {
	var limit int64
	var jsonOutput bool
	command := &cobra.Command{
		Use:   cmdUseList,
		Short: "List enrichment identities to confirm",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, _, err := OpenHTTPStore(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			query := generated.ListPersonEnrichmentIdentityReviewsQuery{}
			if limit > 0 {
				query.Limit = &limit
			}
			resp, err := daemonclient.APIResponse(cmd.Context(), client,
				func(api *apiclient.Client) (*generated.ListPersonEnrichmentIdentityReviewsResp, error) {
					return api.ListPersonEnrichmentIdentityReviewsWithResponse(cmd.Context(),
						&generated.ListPersonEnrichmentIdentityReviewsRequestOptions{Query: &query})
				})
			if err != nil {
				return err
			}
			if jsonOutput {
				return json.MarshalEncode(jsontext.NewEncoder(cmd.OutOrStdout()), resp.JSON200,
					json.Deterministic(true))
			}
			return writePersonEnrichmentReviews(cmd, resp.JSON200.Reviews)
		},
	}
	command.Flags().Int64Var(&limit, "limit", 0, "Maximum attempts to list (default 50, max 200)")
	command.Flags().BoolVar(&jsonOutput, flagJSON, false, "Output as JSON")
	return command
}

func writePersonEnrichmentReviews(cmd *cobra.Command, reviews []generated.PersonEnrichmentIdentityReview) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ATTEMPT\tPERSON\tPROVIDER\tRETURNED\tNAME\tCOMPANY\tCONFLICT")
	for _, review := range reviews {
		person := strconv.FormatInt(review.PersonID, 10)
		if review.PersonDisplayName != nil && strings.TrimSpace(*review.PersonDisplayName) != "" {
			person += " " + strings.TrimSpace(*review.PersonDisplayName)
		}
		_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%.2f\t%.2f\t%.2f\n", review.AttemptID,
			textutil.SanitizeTerminal(person), textutil.SanitizeTerminal(review.ProviderName),
			textutil.SanitizeTerminal(returnedIdentitySummary(review.Returned)),
			review.NameCompatible, review.CompanySame, review.NameConflict)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush enrichment reviews: %w", err)
	}
	return nil
}

func returnedIdentitySummary(returned generated.PersonEnrichmentReturnedIdentity) string {
	parts := []string{}
	if returned.Name != nil && strings.TrimSpace(*returned.Name) != "" {
		parts = append(parts, strings.TrimSpace(*returned.Name))
	}
	for _, role := range returned.CurrentRoles {
		title, company := derefString(role.Title), derefString(role.Company)
		switch {
		case title != "" && company != "":
			parts = append(parts, title+" at "+company)
		case company != "":
			parts = append(parts, company)
		case title != "":
			parts = append(parts, title)
		}
	}
	if returned.Location != nil && strings.TrimSpace(*returned.Location) != "" {
		parts = append(parts, strings.TrimSpace(*returned.Location))
	}
	if returned.ProfileURLHost != nil && *returned.ProfileURLHost != "" {
		parts = append(parts, *returned.ProfileURLHost)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "; ")
}

func newPersonEnrichmentReviewDecideCommand(confirm bool) *cobra.Command {
	var jsonOutput bool
	use, short := "reject <attempt-id>", "Reject: the returned identity is someone else"
	if confirm {
		use, short = "accept <attempt-id>", "Accept: the returned identity is this person"
	}
	command := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			attemptID, err := positivePersonCLIArg(cmd, args[0], "attempt")
			if err != nil {
				return err
			}
			client, _, err := OpenHTTPStore(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			var decision *generated.PersonEnrichmentIdentityDecision
			if confirm {
				resp, err := daemonclient.APIResponse(cmd.Context(), client,
					func(api *apiclient.Client) (*generated.ConfirmPersonEnrichmentIdentityResp, error) {
						return api.ConfirmPersonEnrichmentIdentityWithResponse(cmd.Context(),
							&generated.ConfirmPersonEnrichmentIdentityRequestOptions{
								PathParams: &generated.ConfirmPersonEnrichmentIdentityPath{ID: attemptID},
							})
					})
				if err != nil {
					return err
				}
				decision = resp.JSON200
			} else {
				resp, err := daemonclient.APIResponse(cmd.Context(), client,
					func(api *apiclient.Client) (*generated.RejectPersonEnrichmentIdentityResp, error) {
						return api.RejectPersonEnrichmentIdentityWithResponse(cmd.Context(),
							&generated.RejectPersonEnrichmentIdentityRequestOptions{
								PathParams: &generated.RejectPersonEnrichmentIdentityPath{ID: attemptID},
							})
					})
				if err != nil {
					return err
				}
				decision = resp.JSON200
			}
			if jsonOutput {
				return json.MarshalEncode(jsontext.NewEncoder(cmd.OutOrStdout()), decision,
					json.Deterministic(true))
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"Attempt %d: %s (%s)\nPerson: %d\nAttempt state: %s\n",
				decision.AttemptID, decision.Decision, decision.Reason, decision.PersonID,
				decision.AttemptState)
			if confirm {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Values applied: %d\nProvider identities attached: %d\n",
					decision.Projections, decision.ProviderIdentitiesAttached)
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Negatives recorded: %d\n", decision.Negatives)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&jsonOutput, flagJSON, false, "Output as JSON")
	return command
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
