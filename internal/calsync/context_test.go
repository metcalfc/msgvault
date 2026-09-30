package calsync

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/gcal"
)

func TestPersistCalendarEventHonorsCancellation(t *testing.T) {
	for _, cancelledEvent := range []bool{false, true} {
		name := "active"
		if cancelledEvent {
			name = "cancelled"
		}
		t.Run(name, func(t *testing.T) {
			requirements := require.New(t)
			assertions := assert.New(t)
			syncer, st := newSyncer(t, gcal.NewMockAPI(), Options{})
			source, err := st.GetOrCreateSource(gcal.SourceType, "calendar@example.com/primary")
			requirements.NoError(err)
			event := gcal.Event{ID: "event-1", Summary: "Synthetic meeting"}
			if cancelledEvent {
				event.Status = gcal.StatusCancelled
			}
			st.DB().SetMaxOpenConns(1)
			conn, err := st.DB().Conn(t.Context())
			requirements.NoError(err)
			t.Cleanup(func() { _ = conn.Close(); st.DB().SetMaxOpenConns(0) })
			before := st.DB().Stats().WaitCount
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := &Result{}
			done := make(chan error, 1)
			go func() {
				_, _, err := syncer.persistOne(ctx, source.ID, gcal.Calendar{ID: "primary"}, event, result)
				done <- err
			}()
			requirements.Eventually(func() bool { return st.DB().Stats().WaitCount > before }, 5*time.Second, time.Millisecond)
			cancel()
			select {
			case err := <-done:
				requirements.ErrorIs(err, context.Canceled)
			case <-time.After(5 * time.Second):
				requirements.FailNow("calendar persistence ignored cancellation")
			}
			requirements.NoError(conn.Close())
			assertions.Empty(result.InsertedIDs)
			assertions.Zero(result.EventsAdded)
			assertions.Zero(result.EventsCancelled)
			existing, err := st.MessageExistsBatch(source.ID, []string{"event-1"})
			requirements.NoError(err)
			assertions.Empty(existing)
		})
	}
}

type cancelOnEventPageAPI struct {
	*gcal.MockAPI

	cancel context.CancelFunc
}

func (a *cancelOnEventPageAPI) ListEvents(ctx context.Context, calendarID string, params gcal.EventsListParams) (*gcal.EventsPage, error) {
	a.cancel()
	return &gcal.EventsPage{Items: []gcal.Event{{ID: "one"}, {ID: "two"}}, NextSyncToken: "next"}, nil
}
func TestIncrementalCancellationDoesNotRecordUnattemptedFailures(t *testing.T) {
	req := require.New(t)
	check := assert.New(t)
	api := gcal.NewMockAPI()
	s, st := newSyncer(t, api, Options{})
	src, err := st.GetOrCreateSource(gcal.SourceType, "calendar@example.com/primary")
	req.NoError(err)
	src.SyncCursor = sql.NullString{String: "prior", Valid: true}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.client = &cancelOnEventPageAPI{MockAPI: api, cancel: cancel}
	err = s.incrementalCalendar(ctx, src, gcal.Calendar{ID: "primary"}, &Result{})
	req.ErrorIs(err, context.Canceled)
	var count int
	req.NoError(st.DB().QueryRow("SELECT COUNT(*) FROM sync_run_items").Scan(&count))
	check.Zero(count, "cancellation must not be recorded as failures for unattempted events")
}
