package cmd

import (
	"context"
	"log/slog"

	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/store"
)

// cacheBuildPersonJudgeLimit bounds how many items one cache build judges,
// so a backlog is worked off over several builds. `msgvault person judge`
// has no such cap.
const cacheBuildPersonJudgeLimit = 200

// judgePeopleForCacheBuild runs the person judgments before a cache build,
// only when a person feature enables automatic use. The service refuses
// anything [jev], consent, or the key do not allow. It never fails the cache
// build and logs only counts and the Jev skip category, never state.
func judgePeopleForCacheBuild(ctx context.Context, cfg *config.Config, dbPath string, logger *slog.Logger) {
	if cfg == nil || !personJudgeAutomatic(cfg) {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	st, err := store.Open(dbPath)
	if err != nil {
		logger.Warn("person judgments: skipped before cache build", "reason", "open_store")
		return
	}
	defer func() { _ = st.Close() }()
	report, err := runPersonJudge(ctx, cfg, st, cacheBuildPersonJudgeLimit, true, logger)
	if err != nil {
		logger.Warn("person judgments: failed before cache build", "reason", "store_error")
		return
	}
	logger.Info("person judgments before cache build",
		"duplicate_proposals", report.Duplicates.Proposals, "duplicate_requests", report.Duplicates.Requests,
		"duplicate_candidates", report.Duplicates.Candidates, "duplicate_retired", report.Duplicates.Retired,
		"duplicate_skipped", report.Duplicates.Skipped,
		"profile_requests", report.Profiles.Requests, "primary_roles_set", report.Profiles.PrimaryRolesSet,
		"display_names_set", report.Profiles.DisplayNamesSet, "conflicts_settled", report.Profiles.ConflictsSettled,
		"profile_settled_in_code", report.Profiles.SettledInCode,
		"profile_skipped", report.Profiles.Skipped)
}

// personJudgeAutomatic reports whether any person judgment may run
// unattended.
func personJudgeAutomatic(cfg *config.Config) bool {
	duplicates, profiles := cfg.Jev.PersonDuplicates, cfg.Jev.PersonProfileChoices
	return cfg.Jev.Enabled &&
		(duplicates.Enabled && duplicates.Automatic || profiles.Enabled && profiles.Automatic)
}
