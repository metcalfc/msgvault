package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"go.kenn.io/msgvault/internal/cleanupsuggest"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/kindclassify"
	"go.kenn.io/msgvault/internal/meetingjudge"
	"go.kenn.io/msgvault/internal/orgresolution"
	"go.kenn.io/msgvault/internal/peoplesweep"
	"go.kenn.io/msgvault/internal/persondedup"
	"go.kenn.io/msgvault/internal/personenrichment"
	"go.kenn.io/msgvault/internal/personfacts"
	"go.kenn.io/msgvault/internal/profilejudge"
	"go.kenn.io/msgvault/internal/providercredentials"
	"go.kenn.io/msgvault/internal/queryunderstand"
	"go.kenn.io/msgvault/internal/sweepjudge"
)

// jevRuntimeStore is what a live Jev service needs from the archive: consent
// checks and the persisted daily counters. *store.Store implements it.
type jevRuntimeStore interface {
	jev.ConsentChecker
	jev.Ledger
}

// jevCredentialSource resolves the stored jev/api_key credential bound to the
// endpoint origin, falling back to the configured environment variable. The
// value never reaches output.
func jevCredentialSource(cfg *config.Config) jev.CredentialSource {
	return func(endpoint, apiKeyEnv string) (string, bool, error) {
		if cfg == nil {
			return "", false, errors.New("configuration is unavailable")
		}
		snapshot, err := providercredentials.Read(cfg.TokensDir())
		if err != nil {
			return "", false, err
		}
		key, state, err := snapshot.Resolve(providercredentials.JevID, endpoint, apiKeyEnv, os.LookupEnv)
		if err != nil {
			return "", false, err
		}
		return key, state.Configured, nil
	}
}

// newJevService builds the shared Jev door for one process, or returns nil
// when [jev] is off so callers wire nothing.
//
// The service runs under the [jev] section this process started with: like
// every jev.* setting the settings API marks restart-required, a change to
// [jev] takes effect after the daemon restarts. Consent and the credential
// are outside that snapshot and are rechecked on every request.
func newJevService(cfg *config.Config, st jevRuntimeStore) (*jev.Service, error) {
	if cfg == nil || st == nil || !cfg.Jev.Enabled {
		return nil, nil //nolint:nilnil // nil means "no service"; callers treat it as disabled.
	}
	return jev.NewService(jev.ServiceOptions{
		Config:             func() (jev.Config, error) { return cfg.Jev, nil },
		Consents:           st,
		Ledger:             st,
		Credential:         jevCredentialSource(cfg),
		CredentialRevision: jevCredentialRevision(cfg),
	})
}

// jevCredentialRevision is the cheap probe the service uses to decide whether
// the stored key must be reread: the credential store file's size and
// modification time. A missing store is a stable revision of its own; the
// environment variable cannot change within the process.
func jevCredentialRevision(cfg *config.Config) jev.CredentialRevision {
	return func() (string, error) {
		info, err := os.Stat(filepath.Join(cfg.TokensDir(), providercredentials.Filename))
		if errors.Is(err, os.ErrNotExist) {
			return "absent", nil
		}
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10), nil
	}
}

// newJevIdentityJudge wires the enrichment identity check, or returns nil
// when Jev or the feature is off in the startup configuration so the exact
// rule alone applies until the daemon restarts with them on. automatic marks
// unattended callers such as the daemon's scheduled runs.
func newJevIdentityJudge(cfg *config.Config, st jevRuntimeStore, automatic bool) (personenrichment.IdentityJudge, error) {
	if cfg == nil || !cfg.Jev.Enabled || !cfg.Jev.IdentityVerification.Enabled {
		return nil, nil //nolint:nilnil // nil means "no judge"; the worker keeps the exact rule.
	}
	service, err := newJevService(cfg, st)
	if err != nil || service == nil {
		return nil, err
	}
	return personenrichment.NewJevIdentityJudge(service, automatic), nil
}

// jevOrganizationStore is what organization resolution needs from the
// archive besides the Jev service's own consent and counters.
type jevOrganizationStore interface {
	jevRuntimeStore
	orgresolution.Store
}

// newOrganizationPreparer wires organization resolution. A reference whose
// domain shares exactly one shortlisted organization's registrable domain is
// resolved in code whatever the configuration. Jev is wired only when it and
// the feature are on in the startup configuration; otherwise every reference
// that would need a judgment keeps the exact lookup alone until the daemon
// restarts with them on. automatic marks unattended callers such as
// scheduled runs.
func newOrganizationPreparer(
	cfg *config.Config, st jevOrganizationStore, automatic bool,
) (personfacts.OrganizationPreparer, error) {
	var judge orgresolution.Judge
	if cfg != nil && cfg.Jev.Enabled && cfg.Jev.OrganizationResolution.Enabled {
		service, err := newJevService(cfg, st)
		if err != nil {
			return nil, err
		}
		if service != nil {
			judge = service
		}
	}
	return orgresolution.NewPreparer(judge, st, automatic, nil), nil
}

// newJevEventKindJudge wires the meeting event kind judgment's Jev door, or
// returns nil when Jev or the feature is off so only the plain rules apply.
// Consent, the credential, and the automatic switch are rechecked by the
// service on every request.
func newJevEventKindJudge(cfg *config.Config, st jevRuntimeStore) (meetingjudge.Judge, error) {
	if cfg == nil || !cfg.Jev.Enabled || !cfg.Jev.MeetingEventKind.Enabled {
		return nil, nil //nolint:nilnil // nil means "no Jev".
	}
	service, err := newJevService(cfg, st)
	if err != nil || service == nil {
		return nil, err
	}
	return service, nil
}

// newJevAssigneeJudge wires the meeting action assignee judgment's Jev door,
// or returns nil when Jev or the feature is off so no assignee is inferred.
func newJevAssigneeJudge(cfg *config.Config, st jevRuntimeStore) (meetingjudge.Judge, error) {
	if cfg == nil || !cfg.Jev.Enabled || !cfg.Jev.MeetingActionAssignee.Enabled {
		return nil, nil //nolint:nilnil // nil means "no Jev".
	}
	service, err := newJevService(cfg, st)
	if err != nil || service == nil {
		return nil, err
	}
	return service, nil
}

// newJevKindJudge wires the correspondent kind classifier's Jev door, or
// returns nil when Jev or the feature is off so `kinds build` applies the
// deterministic rules alone. Consent, the credential, and the automatic
// switch are rechecked by the service on every request.
func newJevKindJudge(cfg *config.Config, st jevRuntimeStore) (kindclassify.Judge, error) {
	if cfg == nil || !cfg.Jev.Enabled || !cfg.Jev.CorrespondentKind.Enabled {
		return nil, nil //nolint:nilnil // nil means "rules only".
	}
	service, err := newJevService(cfg, st)
	if err != nil || service == nil {
		return nil, err
	}
	return service, nil
}

// newJevCleanupJudge wires the cleanup suggestion judgment, or returns nil
// when Jev or the feature is off so `suggest-cleanup` only reports the pool.
// Consent and the credential are rechecked by the service on every request.
func newJevCleanupJudge(cfg *config.Config, st jevRuntimeStore) (cleanupsuggest.Judge, error) {
	if cfg == nil || !cfg.Jev.Enabled || !cfg.Jev.CleanupSuggestions.Enabled {
		return nil, nil //nolint:nilnil // nil means "no judgment".
	}
	service, err := newJevService(cfg, st)
	if err != nil || service == nil {
		return nil, err
	}
	return service, nil
}

// newJevQueryUnderstandingJudge wires Explore query suggestions, or returns
// nil when Jev or the feature is off in the startup configuration so the
// endpoint reports the feature disabled and reads nothing. Consent and the
// credential are rechecked by the service on every request.
func newJevQueryUnderstandingJudge(cfg *config.Config, st jevRuntimeStore) (queryunderstand.Judge, error) {
	if cfg == nil || !cfg.Jev.Enabled || !cfg.Jev.QueryUnderstanding.Enabled {
		return nil, nil //nolint:nilnil // nil means "no suggestions".
	}
	service, err := newJevService(cfg, st)
	if err != nil || service == nil {
		return nil, err
	}
	return service, nil
}

// newJevSweepGrounder wires the people sweep's claim grounding, or returns
// nil when Jev or the feature is off so claims keep the chat model's
// reported confidence. automatic marks the daemon's scheduled sweeps.
func newJevSweepGrounder(
	cfg *config.Config, st jevRuntimeStore, automatic bool,
) (peoplesweep.ClaimGrounder, error) {
	if cfg == nil || !cfg.Jev.Enabled || !cfg.Jev.SweepClaimGrounding.Enabled {
		return nil, nil //nolint:nilnil // nil means "no grounding".
	}
	service, err := newJevService(cfg, st)
	if err != nil || service == nil {
		return nil, err
	}
	return sweepjudge.NewGrounder(service, automatic, nil), nil
}

// newJevDuplicatePeopleJudge wires the duplicate people judgment, or returns
// nil when Jev or the feature is off so no duplicate is proposed. Consent,
// the credential, and the automatic switch are rechecked by the service on
// every request.
func newJevDuplicatePeopleJudge(cfg *config.Config, st jevRuntimeStore) (persondedup.Judge, error) {
	if cfg == nil || !cfg.Jev.Enabled || !cfg.Jev.PersonDuplicates.Enabled {
		return nil, nil //nolint:nilnil // nil means "no Jev".
	}
	service, err := newJevService(cfg, st)
	if err != nil || service == nil {
		return nil, err
	}
	return service, nil
}

// newJevProfileChoicesJudge wires the person profile choices judgment, or
// returns nil when Jev or the feature is off so profiles keep the plain
// rules. Consent, the credential, and the automatic switch are rechecked by
// the service on every request.
func newJevProfileChoicesJudge(cfg *config.Config, st jevRuntimeStore) (profilejudge.Judge, error) {
	if cfg == nil || !cfg.Jev.Enabled || !cfg.Jev.PersonProfileChoices.Enabled {
		return nil, nil //nolint:nilnil // nil means "no Jev".
	}
	service, err := newJevService(cfg, st)
	if err != nil || service == nil {
		return nil, err
	}
	return service, nil
}
