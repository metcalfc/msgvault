package cmd

import (
	"context"
	"fmt"
	"log/slog"

	"go.kenn.io/msgvault/internal/scheduler"
	"go.kenn.io/msgvault/internal/store"
)

// The daily contact-match job refreshes identity candidates for contact-only
// profiles and applies the exact matches the rule decides. CardDAV syncs refresh them too, but mail and chat syncs also
// create participants that can match an existing contact, so a small daily
// pass catches those without tying the refresh to every import.
const (
	contactMatchJob  = "contact-matches"
	contactMatchCron = "41 4 * * *"
)

func registerContactMatchJob(sched *scheduler.Scheduler, s *store.Store) error {
	return sched.AddJob(scheduler.Job{
		Name:     contactMatchJob,
		Schedule: contactMatchCron,
		Run: func(ctx context.Context) error {
			result, err := s.BuildContactMatchCandidatesContext(ctx)
			if err != nil {
				return err
			}
			if result.Created > 0 || result.AutoMerged > 0 || result.AutoBound > 0 ||
				result.LinkedClosed > 0 {
				slog.InfoContext(ctx, "contact matches refreshed",
					"created", result.Created, "matches", result.Matches,
					"auto_merged", result.AutoMerged, "auto_bound", result.AutoBound,
					"linked_closed", result.LinkedClosed, "left_for_review", result.LeftForReview)
			}
			if result.AutoMerged > 0 || result.AutoBound > 0 {
				// Automatic merges change people, so identity analytics are
				// refreshed as after any other identity change.
				if err := s.RefreshIdentityDatasetsAfterChange(ctx); err != nil {
					return fmt.Errorf("refresh identity analytics after contact matches: %w", err)
				}
			}
			return nil
		},
	})
}
