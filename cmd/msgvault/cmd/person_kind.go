package cmd

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/daemonclient"
	"go.kenn.io/msgvault/internal/textutil"
	apiclient "go.kenn.io/msgvault/pkg/client"
	"go.kenn.io/msgvault/pkg/client/generated"
)

// newPersonKindCommand marks archive identities as not a person: an
// organization, a shared mailbox, or a record the user does not need. Every
// subcommand goes through the daemon.
func newPersonKindCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "kind",
		Short: "Mark archive identities as a person or not a person",
		Long: "Many archive identities are not people. Mark an identity cluster as:\n\n" +
			"  organization    a business or institution; its messages are grouped under an\n" +
			"                  organization record and its addresses join that organization\n" +
			"  shared_mailbox  an address several people write from, such as a support desk;\n" +
			"                  the people who wrote from it keep their own profiles\n" +
			"  automated       a machine sender: notifications, receipts, newsletters, bots\n" +
			"  mailing_list    a list or group address that relays many senders\n" +
			"  ignored         a record you do not need as a contact\n" +
			"  person          this is a person (overrides any rule or Jev classification)\n\n" +
			"Anything other than person leaves the identity out of contact matching and\n" +
			"enrichment and resolves its open identity matches; every kind except\n" +
			"shared_mailbox also leaves People lists and relationship rankings. Your decision\n" +
			"always outranks 'msgvault kinds build'. Messages stay searchable. Saved profiles\n" +
			"are never deleted.",
	}
	command.AddCommand(newPersonKindSetCommand(), newPersonKindListCommand())
	return command
}

func newPersonKindSetCommand() *cobra.Command {
	var organizationID int64
	var organizationName string
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "set <participant-id> organization|shared_mailbox|automated|mailing_list|ignored|person",
		Short: "Classify the identity cluster containing a participant",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			participantID, err := positivePersonCLIArg(cmd, args[0], "participant")
			if err != nil {
				return err
			}
			kind := correspondentkind.Kind(strings.TrimSpace(args[1]))
			if !kind.Valid() {
				return usageErr(cmd, fmt.Errorf(
					"kind must be organization, shared_mailbox, automated, mailing_list, ignored, or person, got %q", args[1]))
			}
			organizationSet := cmd.Flags().Changed("organization")
			nameSet := cmd.Flags().Changed("organization-name")
			if (organizationSet || nameSet) && kind != correspondentkind.Organization {
				return usageErr(cmd, errors.New("--organization and --organization-name apply only to kind organization"))
			}
			if organizationSet && nameSet {
				return usageErr(cmd, errors.New("use --organization or --organization-name, not both"))
			}
			if organizationSet && organizationID <= 0 {
				return usageErr(cmd, errors.New("--organization must be a positive organization ID"))
			}
			body := generated.SetCorrespondentKindRequest{Kind: generated.SetCorrespondentKindRequestKind(kind)}
			if organizationSet {
				body.OrganizationID = &organizationID
			}
			if nameSet {
				name := strings.TrimSpace(organizationName)
				if name == "" {
					return usageErr(cmd, errors.New("--organization-name must not be blank"))
				}
				body.OrganizationName = &name
			}
			client, _, err := OpenHTTPStore(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			resp, err := daemonclient.APIResponse(cmd.Context(), client,
				func(api *apiclient.Client) (*generated.SetCorrespondentKindResp, error) {
					return api.SetCorrespondentKindWithResponse(cmd.Context(),
						&generated.SetCorrespondentKindRequestOptions{
							PathParams: &generated.SetCorrespondentKindPath{ID: participantID},
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
			return writePersonKindResult(cmd.OutOrStdout(), resp.JSON200)
		},
	}
	command.Flags().Int64Var(&organizationID, "organization", 0,
		"Organization ID to group an organization under")
	command.Flags().StringVar(&organizationName, "organization-name", "",
		"Organization name to find or create (default: the identity's name, then its email domain)")
	command.Flags().BoolVar(&jsonOutput, flagJSON, false, "Output as JSON")
	return command
}

func writePersonKindResult(out io.Writer, result *generated.SetCorrespondentKindResult) error {
	record := result.Record
	_, _ = fmt.Fprintf(out, "Identity %d (%s): %s\n", record.CanonicalID,
		personKindRecordLabel(record), personKindLabel(string(record.Kind)))
	if record.OrganizationName != nil {
		created := ""
		if result.OrganizationCreated {
			created = " (created)"
		}
		_, _ = fmt.Fprintf(out, "Organization: %s%s\n",
			textutil.SanitizeTerminal(*record.OrganizationName), created)
	}
	if result.ResolvedCandidates > 0 {
		_, _ = fmt.Fprintf(out, "Resolved identity matches: %d\n", result.ResolvedCandidates)
	}
	if result.RestoredCandidates > 0 {
		_, _ = fmt.Fprintf(out, "Identity matches back in review: %d\n", result.RestoredCandidates)
	}
	if person := record.Person; person != nil && person.OnlyThisCluster &&
		string(record.Kind) != string(correspondentkind.Person) {
		_, _ = fmt.Fprintf(out,
			"Saved profile %d describes only this identity. It was kept; delete it with:\n"+
				"  msgvault person delete %d\n", person.ID, person.ID)
	}
	return nil
}

func newPersonKindListCommand() *cobra.Command {
	var kind string
	var jsonOutput bool
	command := &cobra.Command{
		Use:   cmdUseList,
		Short: "List identities marked as not a person, or Jev judgments awaiting review",
		Long: "Lists identities whose kind is not a person. --kind unclear lists the\n" +
			"identities Jev could not classify instead, with the probability it gave each\n" +
			"option; decide them with 'msgvault person kind set'.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := generated.ListCorrespondentKindsQuery{}
			if kind = strings.TrimSpace(kind); kind != "" {
				value := correspondentkind.Kind(kind)
				if !value.Known() || value == correspondentkind.Person {
					return usageErr(cmd, fmt.Errorf(
						"--kind must be organization, shared_mailbox, automated, mailing_list, ignored, or unclear, got %q", kind))
				}
				typed := generated.ListCorrespondentKindsQueryKind(kind)
				query.Kind = &typed
			}
			client, _, err := OpenHTTPStore(cmd.Context())
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			resp, err := daemonclient.APIResponse(cmd.Context(), client,
				func(api *apiclient.Client) (*generated.ListCorrespondentKindsResp, error) {
					return api.ListCorrespondentKindsWithResponse(cmd.Context(),
						&generated.ListCorrespondentKindsRequestOptions{Query: &query})
				})
			if err != nil {
				return err
			}
			if jsonOutput {
				return json.MarshalEncode(jsontext.NewEncoder(cmd.OutOrStdout()), resp.JSON200,
					json.Deterministic(true))
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "PARTICIPANT\tKIND\tSOURCE\tIDENTITY\tORGANIZATION\tSAVED PROFILE")
			for _, record := range resp.JSON200.Records {
				organization := "-"
				if record.OrganizationName != nil {
					organization = textutil.SanitizeTerminal(*record.OrganizationName)
				}
				profile := "-"
				if record.Person != nil {
					profile = strconv.FormatInt(record.Person.ID, 10)
				}
				source := "-"
				if record.Source != nil {
					source = *record.Source
				}
				if probability, ok := record.Probabilities["individual_person"]; ok {
					source += " (person " + strconv.FormatFloat(probability, 'f', 2, 64) + ")"
				}
				_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", record.CanonicalID, record.Kind, source,
					personKindRecordLabel(record), organization, profile)
			}
			if err := w.Flush(); err != nil {
				return fmt.Errorf("flush identity kinds: %w", err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&kind, "kind", "",
		"Only organization, shared_mailbox, automated, mailing_list, ignored, or unclear (Jev judgments awaiting review)")
	command.Flags().BoolVar(&jsonOutput, flagJSON, false, "Output as JSON")
	return command
}

func personKindRecordLabel(record generated.CorrespondentKindRecord) string {
	parts := []string{}
	if record.DisplayName != nil && strings.TrimSpace(*record.DisplayName) != "" {
		parts = append(parts, strings.TrimSpace(*record.DisplayName))
	}
	if len(record.Addresses) > 0 {
		parts = append(parts, "<"+record.Addresses[0]+">")
	}
	if len(parts) == 0 {
		return fmt.Sprintf("participant %d", record.CanonicalID)
	}
	return textutil.SanitizeTerminal(strings.Join(parts, " "))
}

func personKindLabel(kind string) string {
	return correspondentkind.Kind(kind).Label()
}
