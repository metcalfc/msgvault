package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

func TestCalendarPersistenceContextCancelsDatabaseAcquisition(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	f := storetest.New(t)
	id := f.CreateMessage("existing-event")
	metadata := sql.NullString{String: `{"status":"confirmed"}`, Valid: true}
	requirements.NoError(f.Store.SetMessageMetadata(id, metadata))
	runID := f.StartSync()
	for _, scoped := range []bool{false, true} {
		st := f.Store
		label := "plain"
		if scoped {
			st = st.ScopedToSync(f.Source.ID, runID)
			label = "scoped"
		}
		operations := []struct {
			name string
			run  func(context.Context) error
		}{
			{"message", func(ctx context.Context) error {
				_, err := st.UpsertMessageContext(ctx, &store.Message{SourceID: f.Source.ID, ConversationID: f.ConvID, SourceMessageID: "new-event", MessageType: "calendar_event"})
				return err
			}},
			{"metadata", func(ctx context.Context) error { return st.SetMessageMetadataContext(ctx, id, sql.NullString{}) }},
			{"read metadata", func(ctx context.Context) error { _, err := st.GetMessageMetadataContext(ctx, id); return err }},
			{"find event", func(ctx context.Context) error {
				_, err := st.MessageExistsBatchContext(ctx, f.Source.ID, []string{"existing-event"})
				return err
			}},
			{"conversation", func(ctx context.Context) error {
				_, err := st.EnsureConversationWithTypeContext(ctx, f.Source.ID, "new-conversation", "calendar", "new title")
				return err
			}},
			{"body", func(ctx context.Context) error {
				return st.UpsertMessageBodyContext(ctx, id, sql.NullString{String: "new body", Valid: true}, sql.NullString{})
			}},
			{"raw", func(ctx context.Context) error {
				return st.UpsertMessageRawWithFormatContext(ctx, id, []byte(`{}`), "gcal")
			}},
			{"recipients", func(ctx context.Context) error { return st.ReplaceMessageRecipientsContext(ctx, id, "to", nil, nil) }},
			{"fts", func(ctx context.Context) error {
				return st.UpsertFTSContext(ctx, id, "new subject", "new body", "", "", "")
			}},
		}
		for _, op := range operations {
			t.Run(label+"/"+op.name, func(t *testing.T) {
				requirements := require.New(t)
				if op.name == "fts" && !st.FTS5Available() {
					t.Skip("FTS unavailable")
				}
				st.DB().SetMaxOpenConns(1)
				conn, err := st.DB().Conn(t.Context())
				requirements.NoError(err)
				t.Cleanup(func() { _ = conn.Close(); st.DB().SetMaxOpenConns(0) })
				before := st.DB().Stats().WaitCount
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- op.run(ctx) }()
				requirements.Eventually(func() bool { return st.DB().Stats().WaitCount > before }, 5*time.Second, time.Millisecond, "write must reach connection acquisition")
				cancel()
				select {
				case err := <-done:
					requirements.ErrorIs(err, context.Canceled)
				case <-time.After(5 * time.Second):
					requirements.FailNow("cancelled database operation did not return")
				}
				requirements.NoError(conn.Close())
			})
		}
	}
	got, err := f.Store.GetMessageMetadata(id)
	requirements.NoError(err)
	assertions.Equal(metadata, got)
	existing, err := f.Store.MessageExistsBatch(f.Source.ID, []string{"new-event"})
	requirements.NoError(err)
	assertions.Empty(existing)
}
