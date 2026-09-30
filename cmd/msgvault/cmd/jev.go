package cmd

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/kindclassify"
	"go.kenn.io/msgvault/internal/orgresolution"
	"go.kenn.io/msgvault/internal/personenrichment"
	"go.kenn.io/msgvault/internal/providercredentials"
	"go.kenn.io/msgvault/internal/store"
)

const jevConsentActor = "cli"

// jevFeatureSpecs lists every Jev-backed feature the CLI can report on and
// consent to. Feature packages own their specs; this is the only registry.
var jevFeatureSpecs = func() []jev.FeatureSpec {
	return []jev.FeatureSpec{
		personenrichment.JevIdentityFeature(), orgresolution.Feature(), kindclassify.JevFeature(),
	}
}

// jevCredentialState reports whether a key resolves and from where, without
// the value. A store that cannot be read or a stored key bound to another
// endpoint origin is reported as the error text, not as "not configured".
func jevCredentialState(cfg *config.Config) (providercredentials.State, error) {
	none := providercredentials.State{Source: providercredentials.SourceNone}
	if cfg == nil {
		return none, errors.New("configuration is unavailable")
	}
	snapshot, err := providercredentials.Read(cfg.TokensDir())
	if err != nil {
		return none, err
	}
	_, state, err := snapshot.Resolve(providercredentials.JevID, cfg.Jev.Endpoint, cfg.Jev.APIKeyEnv, os.LookupEnv)
	if err != nil {
		return none, err
	}
	return state, nil
}

type jevStore interface {
	jev.ConsentChecker
	jev.Ledger
	GrantJevFeatureConsent(ctx context.Context, feature, fingerprint, actor string) (*store.JevFeatureConsent, bool, error)
	RevokeJevFeatureConsent(ctx context.Context, feature, actor string) (int64, error)
	RevokeAllJevFeatureConsents(ctx context.Context, actor string) (int64, error)
	GetJevFeatureConsentStatus(ctx context.Context, feature, fingerprint string) (*store.JevFeatureConsentStatus, error)
}

type jevCommandDeps struct {
	bind               func(context.Context) jevCommandDeps
	config             func() *config.Config
	openStore          func() (jevStore, func(), error)
	features           func() []jev.FeatureSpec
	credentialState    func(*config.Config) (providercredentials.State, error)
	isDaemonSubprocess func() bool
	proxyArgs          func(*cobra.Command, []string, map[string]string) error
	now                func() time.Time
}

func defaultJevCommandDeps(contexts ...context.Context) jevCommandDeps {
	if len(contexts) > 0 {
		deps := defaultJevCommandDeps()
		if invocationFromContext(contexts[0]) != nil {
			return deps.bind(contexts[0])
		}
	}
	return jevCommandDeps{
		bind: func(ctx context.Context) jevCommandDeps {
			deps := defaultJevCommandDeps()
			state := invocationFromContext(ctx)
			var currentCfg *config.Config
			if state != nil && state.cfg != nil {
				currentCfg = state.cfg
			}
			deps.config = func() *config.Config { return currentCfg }
			deps.openStore = func() (jevStore, func(), error) {
				st, cleanup, err := openWritableStoreAndInitForInvocation(state)
				if err != nil {
					return nil, nil, err
				}
				return st, cleanup, nil
			}
			return deps
		},
		config: func() *config.Config { return nil },
		openStore: func() (jevStore, func(), error) {
			return nil, nil, errors.New("configuration is unavailable")
		},
		features:           jevFeatureSpecs,
		credentialState:    jevCredentialState,
		isDaemonSubprocess: isDaemonCLISubprocess,
		proxyArgs: func(command *cobra.Command, args []string, env map[string]string) error {
			return runDaemonCLICommandHTTPWithEnv(command, args, env, false, false)
		},
		now: time.Now,
	}
}

func newJevCommand(deps jevCommandDeps) *cobra.Command {
	command := &cobra.Command{
		Use:   "jev",
		Short: "Manage consent and limits for Jev (System One) judgments",
		Long: `Jev judgments replace brittle string matching with narrow, typed questions
sent to TypeSafe's System One model. Nothing leaves the machine until [jev]
is enabled, the feature is enabled, an API key resolves, and this command has
recorded consent for the feature's exact policy.`,
	}
	command.AddCommand(
		newJevStatusCommand(deps),
		newJevConsentCommand(deps),
		newJevRevokeCommand(deps),
	)
	return command
}

func proxyJevCommand(command *cobra.Command, args []string, deps jevCommandDeps) error {
	proxied, err := daemonCLIArgsFromCobra(command, args)
	if err != nil {
		return err
	}
	return deps.proxyArgs(command, proxied, nil)
}

func bindJevDeps(command *cobra.Command, deps jevCommandDeps) jevCommandDeps {
	if invocationFromContext(command.Context()) != nil && deps.bind != nil {
		return deps.bind(command.Context())
	}
	return deps
}

type jevFeatureStatusOutput struct {
	Name        string                         `json:"name"`
	Title       string                         `json:"title"`
	Enabled     bool                           `json:"enabled"`
	Automatic   bool                           `json:"automatic"`
	Fingerprint string                         `json:"fingerprint"`
	Consent     *store.JevFeatureConsentStatus `json:"consent"`
	Today       jev.DayCounters                `json:"today"`
}

type jevStatusOutput struct {
	Enabled    bool                      `json:"enabled"`
	Endpoint   string                    `json:"endpoint"`
	Model      string                    `json:"model"`
	APIKeyEnv  string                    `json:"api_key_env"`
	Credential providercredentials.State `json:"credential"`
	// CredentialError explains why no key resolves when the cause is not a
	// missing key: an unreadable credential store or a stored key bound to a
	// different endpoint origin.
	CredentialError   string                   `json:"credential_error,omitempty"`
	RequestTimeout    string                   `json:"request_timeout"`
	MaxRequestsPerDay int64                    `json:"max_requests_per_day"`
	MaxCostUSDPerDay  float64                  `json:"max_cost_usd_per_day"`
	Priced            bool                     `json:"priced"`
	Features          []jevFeatureStatusOutput `json:"features"`
}

func newJevStatusCommand(deps jevCommandDeps) *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use: "status", Args: cobra.NoArgs,
		Short: "Show Jev configuration, credential state, consent, and today's counters",
		RunE: func(command *cobra.Command, args []string) error {
			deps = bindJevDeps(command, deps)
			if !deps.isDaemonSubprocess() {
				return proxyJevCommand(command, args, deps)
			}
			return runJevStatus(command, deps, jsonOutput)
		},
	}
	command.Flags().BoolVar(&jsonOutput, flagJSON, false, "Output structured JSON")
	return command
}

func runJevStatus(command *cobra.Command, deps jevCommandDeps, jsonOutput bool) error {
	cfg := deps.config()
	if cfg == nil {
		return errors.New("configuration is unavailable")
	}
	st, cleanup, err := deps.openStore()
	if err != nil {
		return err
	}
	defer cleanup()
	credential, credentialErr := deps.credentialState(cfg)
	output := jevStatusOutput{
		Enabled: cfg.Jev.Enabled, Endpoint: cfg.Jev.Endpoint, Model: cfg.Jev.Model,
		APIKeyEnv: cfg.Jev.APIKeyEnv, Credential: credential,
		RequestTimeout: cfg.Jev.RequestTimeout.String(), MaxRequestsPerDay: cfg.Jev.MaxRequestsPerDay,
		MaxCostUSDPerDay: cfg.Jev.MaxCostUSDPerDay, Priced: cfg.Jev.Priced(),
		Features: make([]jevFeatureStatusOutput, 0),
	}
	if credentialErr != nil {
		output.CredentialError = credentialErr.Error()
	}
	day := jev.UTCDay(deps.now())
	for _, spec := range deps.features() {
		policy, err := spec.Policy(cfg.Jev)
		if err != nil {
			return err
		}
		consent, err := st.GetJevFeatureConsentStatus(command.Context(), spec.Name, policy.Fingerprint)
		if err != nil {
			return err
		}
		counters, err := st.JevDayCounters(command.Context(), spec.Name, day)
		if err != nil {
			return err
		}
		feature, _ := cfg.Jev.FeatureConfigFor(spec.Name)
		output.Features = append(output.Features, jevFeatureStatusOutput{
			Name: spec.Name, Title: spec.Title, Enabled: feature.Enabled, Automatic: feature.Automatic,
			Fingerprint: policy.Fingerprint, Consent: consent, Today: counters,
		})
	}
	if jsonOutput {
		return json.MarshalEncode(jsontext.NewEncoder(command.OutOrStdout()), output, json.Deterministic(true))
	}
	w := command.OutOrStdout()
	_, _ = fmt.Fprintf(w, "Jev: %s\n", onOff(output.Enabled))
	_, _ = fmt.Fprintf(w, "Destination: %s (model %s)\n", output.Endpoint, output.Model)
	_, _ = fmt.Fprintf(w, "Credential: %s\n", jevCredentialDescription(output.Credential, output.APIKeyEnv, output.CredentialError))
	cost := "not priced (requests counted only)"
	if output.Priced {
		cost = fmt.Sprintf("max %.4f USD per day", output.MaxCostUSDPerDay)
	}
	_, _ = fmt.Fprintf(w, "Limits: %d requests per day, %s, %s per request\n",
		output.MaxRequestsPerDay, cost, output.RequestTimeout)
	if len(output.Features) == 0 {
		_, _ = fmt.Fprintln(w, "Features: none registered")
		return nil
	}
	_, _ = fmt.Fprintln(w, "Features:")
	for _, feature := range output.Features {
		consent := "consent required"
		switch {
		case feature.Consent != nil && feature.Consent.Active:
			consent = "consent active"
		case feature.Consent != nil && feature.Consent.Superseded:
			consent = "consent required (policy changed)"
		}
		_, _ = fmt.Fprintf(w, "- %s (%s): enabled=%s automatic=%s, %s, fingerprint %s\n",
			feature.Name, feature.Title, onOff(feature.Enabled), onOff(feature.Automatic),
			consent, feature.Fingerprint)
		_, _ = fmt.Fprintf(w, "  today: %d requests, %d input tokens, %d output tokens, %d micro-USD\n",
			feature.Today.Requests, feature.Today.InputTokens, feature.Today.OutputTokens,
			feature.Today.CostUSDMicros)
	}
	return nil
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func jevCredentialDescription(state providercredentials.State, apiKeyEnv, failure string) string {
	if failure != "" {
		return "unavailable: " + failure
	}
	switch state.Source {
	case providercredentials.SourceStored:
		return "stored (Settings)"
	case providercredentials.SourceEnvironment:
		return "environment variable " + apiKeyEnv
	default:
		return "not configured (store a key in Settings or set " + apiKeyEnv + ")"
	}
}

func newJevConsentCommand(deps jevCommandDeps) *cobra.Command {
	var confirmed bool
	var jsonOutput bool
	command := &cobra.Command{
		Use: "consent <feature>", Args: cobra.ExactArgs(1),
		Short: "Consent to a feature's exact Jev policy after reviewing what it sends",
		RunE: func(command *cobra.Command, args []string) error {
			deps = bindJevDeps(command, deps)
			if !jev.ValidFeatureName(args[0]) {
				return errors.New("invalid Jev feature name")
			}
			if !deps.isDaemonSubprocess() {
				return proxyJevCommand(command, args, deps)
			}
			return runJevConsent(command, deps, args[0], confirmed, jsonOutput)
		},
	}
	command.Flags().BoolVar(&confirmed, "yes", false, "Confirm the disclosed policy")
	command.Flags().BoolVar(&jsonOutput, flagJSON, false, "Output structured JSON")
	return command
}

func jevFeatureSpec(deps jevCommandDeps, name string) (jev.FeatureSpec, error) {
	for _, spec := range deps.features() {
		if spec.Name == name {
			return spec, nil
		}
	}
	return jev.FeatureSpec{}, fmt.Errorf("unknown Jev feature %q", name)
}

func runJevConsent(command *cobra.Command, deps jevCommandDeps, name string, confirmed, jsonOutput bool) error {
	cfg := deps.config()
	if cfg == nil {
		return errors.New("configuration is unavailable")
	}
	spec, err := jevFeatureSpec(deps, name)
	if err != nil {
		return err
	}
	policy, err := spec.Policy(cfg.Jev)
	if err != nil {
		return err
	}
	feature, _ := cfg.Jev.FeatureConfigFor(spec.Name)
	if !confirmed {
		printJevDisclosure(command.OutOrStdout(), policy, feature)
		return errors.New("jev consent requires --yes after reviewing the disclosure")
	}
	st, cleanup, err := deps.openStore()
	if err != nil {
		return err
	}
	defer cleanup()
	if _, _, err := st.GrantJevFeatureConsent(command.Context(), spec.Name, policy.Fingerprint, jevConsentActor); err != nil {
		return err
	}
	status, err := st.GetJevFeatureConsentStatus(command.Context(), spec.Name, policy.Fingerprint)
	if err != nil {
		return err
	}
	if jsonOutput {
		return json.MarshalEncode(jsontext.NewEncoder(command.OutOrStdout()), struct {
			Policy  jev.Policy                     `json:"policy"`
			Consent *store.JevFeatureConsentStatus `json:"consent"`
		}{Policy: policy, Consent: status}, json.Deterministic(true))
	}
	printJevDisclosure(command.OutOrStdout(), policy, feature)
	_, _ = fmt.Fprintf(command.OutOrStdout(), "Consent: active (%s)\n", policy.Fingerprint)
	return nil
}

// printJevDisclosure lists exactly what leaves the machine for a feature.
func printJevDisclosure(w io.Writer, policy jev.Policy, feature jev.FeatureConfig) {
	_, _ = fmt.Fprintf(w, "Jev feature disclosure: %s (%s)\n", policy.Title, policy.Feature)
	_, _ = fmt.Fprintf(w, "Purpose: %s\n", policy.Purpose)
	_, _ = fmt.Fprintf(w, "Fingerprint: %s\n", policy.Fingerprint)
	_, _ = fmt.Fprintf(w, "Destination: %s\n", policy.Endpoint)
	_, _ = fmt.Fprintf(w, "Model: %s\n", policy.Model)
	_, _ = fmt.Fprintf(w, "Feature switch: enabled=%s automatic=%s\n", onOff(feature.Enabled), onOff(feature.Automatic))
	_, _ = fmt.Fprintln(w, "Fields that leave the machine:")
	for _, field := range policy.StateFields {
		_, _ = fmt.Fprintf(w, "- %s\n", field)
	}
	_, _ = fmt.Fprintln(w, "Questions asked, exactly as sent:")
	for _, question := range policy.Questions {
		_, _ = fmt.Fprintf(w, "- %s (%s): %s\n", question.ID, question.Type, jev.QuestionText(question.Instructions))
		if question.Criteria != nil {
			_, _ = fmt.Fprintf(w, "  criteria: %s\n", jev.QuestionText(question.Criteria))
		}
	}
	_, _ = fmt.Fprintln(w, "Nothing else is sent: no message bodies, no addresses beyond the listed fields, no identifiers.")
}

func newJevRevokeCommand(deps jevCommandDeps) *cobra.Command {
	var all bool
	var jsonOutput bool
	command := &cobra.Command{
		Use: "revoke [feature]", Args: cobra.MaximumNArgs(1),
		Short: "Revoke consent for one feature or every feature",
		RunE: func(command *cobra.Command, args []string) error {
			deps = bindJevDeps(command, deps)
			if (len(args) == 1) == all {
				return errors.New("revoke takes exactly one feature name or --all")
			}
			if len(args) == 1 && !jev.ValidFeatureName(args[0]) {
				return errors.New("invalid Jev feature name")
			}
			if !deps.isDaemonSubprocess() {
				return proxyJevCommand(command, args, deps)
			}
			return runJevRevoke(command, deps, args, all, jsonOutput)
		},
	}
	command.Flags().BoolVar(&all, "all", false, "Revoke consent for every feature")
	command.Flags().BoolVar(&jsonOutput, flagJSON, false, "Output structured JSON")
	return command
}

func runJevRevoke(command *cobra.Command, deps jevCommandDeps, args []string, all, jsonOutput bool) error {
	st, cleanup, err := deps.openStore()
	if err != nil {
		return err
	}
	defer cleanup()
	var revoked int64
	scope := "all features"
	if all {
		revoked, err = st.RevokeAllJevFeatureConsents(command.Context(), jevConsentActor)
	} else {
		scope = args[0]
		revoked, err = st.RevokeJevFeatureConsent(command.Context(), args[0], jevConsentActor)
	}
	if err != nil {
		return err
	}
	if jsonOutput {
		return json.MarshalEncode(jsontext.NewEncoder(command.OutOrStdout()), struct {
			Scope   string `json:"scope"`
			Revoked int64  `json:"revoked"`
		}{Scope: scope, Revoked: revoked}, json.Deterministic(true))
	}
	_, _ = fmt.Fprintf(command.OutOrStdout(), "Consent revoked for %s: %d grant(s)\n", strings.TrimSpace(scope), revoked)
	return nil
}

func init() {
	rootCmd.AddCommand(newJevCommand(defaultJevCommandDeps()))
}
