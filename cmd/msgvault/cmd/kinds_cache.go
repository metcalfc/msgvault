package cmd

import (
	"context"
	"log/slog"

	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/kindclassify"
	"go.kenn.io/msgvault/internal/store"
)

// cacheBuildKindLimit bounds how many identities one cache build classifies,
// so a large backlog is worked off over several builds instead of delaying
// one. `msgvault kinds build` has no such cap.
const cacheBuildKindLimit = 200

// classifyKindsForCacheBuild classifies new identities before a cache build
// exports correspondent kinds, only when [jev.correspondent_kind] enables
// automatic use. Rules always run then; Jev additionally needs [jev], consent,
// and a key, and the service refuses anything else. It never fails the cache
// build and logs only counts and the Jev skip category, never state.
func classifyKindsForCacheBuild(ctx context.Context, cfg *config.Config, dbPath string, logger *slog.Logger) {
	if cfg == nil || !cfg.Jev.CorrespondentKind.Enabled || !cfg.Jev.CorrespondentKind.Automatic {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	st, err := store.Open(dbPath)
	if err != nil {
		logger.Warn("correspondent kinds: skipped before cache build", "reason", "open_store")
		return
	}
	defer func() { _ = st.Close() }()
	judge, err := newJevKindJudge(cfg, st)
	if err != nil {
		logger.Warn("correspondent kinds: jev unavailable before cache build", "reason", "jev_setup")
		judge = nil
	}
	report, err := kindclassify.Run(ctx, st, kindclassify.Options{
		MinMessages: kindclassify.DefaultMinMessages, Limit: cacheBuildKindLimit,
		Judge: judge, Automatic: true, Logger: logger,
	})
	if err != nil {
		logger.Warn("correspondent kinds: classification failed before cache build", "reason", "store_error")
		return
	}
	rules := 0
	for _, count := range report.Rule {
		rules += count
	}
	logger.Info("correspondent kinds classified before cache build",
		"candidates", report.Candidates, "rule", rules, "jev_judged", report.Jev.Judged,
		"jev_requests", report.Jev.Requests, "jev_skipped", report.Jev.Skipped, "undecided", report.Undecided)
}
