package cmd

import (
	"errors"
	"os"

	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/jev"
	"go.kenn.io/msgvault/internal/personenrichment"
	"go.kenn.io/msgvault/internal/providercredentials"
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
// when [jev] is off so callers wire nothing. Enablement, consent, and the
// credential are still rechecked on every request.
func newJevService(cfg *config.Config, st jevRuntimeStore) (*jev.Service, error) {
	if cfg == nil || st == nil || !cfg.Jev.Enabled {
		return nil, nil //nolint:nilnil // nil means "no service"; callers treat it as disabled.
	}
	return jev.NewService(jev.ServiceOptions{
		Config:     func() (jev.Config, error) { return cfg.Jev, nil },
		Consents:   st,
		Ledger:     st,
		Credential: jevCredentialSource(cfg),
	})
}

// newJevIdentityJudge wires the enrichment identity check, or returns nil
// when Jev or the feature is off so the exact rule alone applies. automatic
// marks unattended callers such as the daemon's scheduled runs.
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
