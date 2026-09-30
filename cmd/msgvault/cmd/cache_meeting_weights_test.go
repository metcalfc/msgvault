package cmd

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/identityindex"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

// cachedMeetingWeight reads one event's exported meeting weight; ok is false
// when the event has no row (it weighs 1).
func cachedMeetingWeight(t *testing.T, analyticsDir string, messageID int64) (float64, bool) {
	t.Helper()
	duck, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = duck.Close() }()
	var weight float64
	err = duck.QueryRow(`SELECT weight FROM read_parquet(?) WHERE message_id = ?`,
		filepath.Join(analyticsDir, identityindex.DatasetMeetingWeights, "*.parquet"), messageID).Scan(&weight)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false
	}
	require.NoError(t, err)
	return weight, true
}

func TestMeetingKindDriftRefreshesCachedWeights(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "test.db")
	analyticsDir := filepath.Join(tmp, "analytics")
	st, err := store.Open(dbPath)
	requirements.NoError(err)
	requirements.NoError(st.InitSchema())
	source, err := st.GetOrCreateSource("gcal", "owner@example.com")
	requirements.NoError(err)
	conversation, err := st.EnsureConversationWithType(source.ID, "event:webinar", "calendar", "Vendor webinar")
	requirements.NoError(err)
	eventID, err := st.UpsertMessage(&store.Message{
		ConversationID: conversation, SourceID: source.ID, SourceMessageID: "webinar",
		MessageType: "calendar_event", SentAt: sql.NullTime{Time: time.Date(2026, 7, 20, 15, 0, 0, 0, time.UTC), Valid: true},
		Subject: sql.NullString{String: "Vendor webinar", Valid: true},
	})
	requirements.NoError(err)
	requirements.NoError(st.SetMessageMetadata(eventID, sql.NullString{String: `{"status":"confirmed"}`, Valid: true}))
	requirements.NoError(st.Close())
	_, err = buildCache(dbPath, analyticsDir, true)
	requirements.NoError(err)
	_, listed := cachedMeetingWeight(t, analyticsDir, eventID)
	assertions.False(listed, "an unjudged small event weighs 1")
	before, err := query.ReadCacheSyncState(analyticsDir)
	requirements.NoError(err)

	st, err = store.Open(dbPath)
	requirements.NoError(err)
	_, err = st.WriteCalendarEventKindsContext(t.Context(), []store.CalendarEventKind{{
		ConversationID: conversation, Kind: "external_webinar_or_marketing", Source: store.CalendarEventKindSourceJev,
		Confidence: 0.9, Model: "jev-test",
	}})
	requirements.NoError(err)
	requirements.NoError(st.Close())

	stale := cacheNeedsBuild(dbPath, analyticsDir)
	requirements.True(stale.NeedsBuild)
	assertions.True(stale.HasMeetingWeightDrift)
	assertions.False(stale.FullRebuild)
	assertions.True(derivedDriftOnly(stale))
	result, err := buildCacheDerivedOnly(dbPath, analyticsDir)
	requirements.NoError(err)
	assertions.True(result.IdentityOnly)
	after, err := query.ReadCacheSyncState(analyticsDir)
	requirements.NoError(err)
	assertions.Equal(before.MeetingWeightRevision+1, after.MeetingWeightRevision)
	assertions.NotEqual(before.Revision(), after.Revision())
	weight, listed := cachedMeetingWeight(t, analyticsDir, eventID)
	assertions.True(listed)
	assertions.InDelta(0.0, weight, 1e-9, "a confident webinar is not a meeting")
	assertions.False(cacheNeedsBuild(dbPath, analyticsDir).NeedsBuild)
}
