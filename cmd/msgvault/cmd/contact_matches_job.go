package cmd

import (
	"context"
	"log/slog"

	"go.kenn.io/msgvault/internal/scheduler"
	"go.kenn.io/msgvault/internal/store"
)

// The daily contact-match job refreshes identity candidates for contact-only
// profiles. CardDAV syncs refresh them too, but mail and chat syncs also
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
			if result.Created > 0 {
				slog.InfoContext(ctx, "contact matches proposed",
					"created", result.Created, "matches", result.Matches)
			}
			return nil
		},
	})
}
