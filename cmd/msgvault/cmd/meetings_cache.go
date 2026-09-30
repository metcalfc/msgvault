package cmd

import (
	"context"
	"log/slog"

	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/store"
)

// cacheBuildMeetingLimit bounds how many calendar series one cache build
// judges, so a backlog is worked off over several builds. `msgvault
// meetings judge` has no such cap.
const cacheBuildMeetingLimit = 200

// judgeMeetingsForCacheBuild runs the meeting judgments before a cache build
// exports meeting weights, only when a meeting feature enables automatic
// use. The service refuses anything [jev], consent, or the key do not allow.
// It never fails the cache build and logs only counts and the Jev skip
// category, never state.
func judgeMeetingsForCacheBuild(ctx context.Context, cfg *config.Config, dbPath string, logger *slog.Logger) {
	if cfg == nil || !cfg.Jev.MeetingEventKind.Enabled || !cfg.Jev.MeetingEventKind.Automatic {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	st, err := store.Open(dbPath)
	if err != nil {
		logger.Warn("meeting judgments: skipped before cache build", "reason", "open_store")
		return
	}
	defer func() { _ = st.Close() }()
	report, err := runMeetingsJudge(ctx, cfg, st, cacheBuildMeetingLimit, true, logger)
	if err != nil {
		logger.Warn("meeting judgments: failed before cache build", "reason", "store_error")
		return
	}
	logger.Info("meeting judgments before cache build",
		"series", report.EventKinds.Candidates, "not_meetings", report.EventKinds.NotMeetings,
		"jev_judged", report.EventKinds.Judged, "jev_requests", report.EventKinds.Requests,
		"jev_skipped", report.EventKinds.Skipped)
}
