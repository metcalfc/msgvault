package cmd

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

func TestCacheRevalidatesIdentityDimensionsAfterBuildDecision(t *testing.T) {
	testCacheSnapshotIdentityDrift(t, false)
}

func TestCacheRevalidatesIdentityDimensionsAfterRevisionRead(t *testing.T) {
	testCacheSnapshotIdentityDrift(t, true)
}

func TestCacheRevalidatesIdentityDimensionsSQLiteScanner(t *testing.T) {
	must := require.New(t)
	probe, err := sql.Open("duckdb", "")
	must.NoError(err)
	t.Cleanup(func() { _ = probe.Close() })
	requireSQLiteScanner(t, probe)
	oldGOOS := cacheSnapshotGOOS
	cacheSnapshotGOOS = "linux"
	t.Cleanup(func() { cacheSnapshotGOOS = oldGOOS })
	t.Setenv("MSGVAULT_FORCE_CSV_SNAPSHOT", "")
	testCacheSnapshotIdentityDrift(t, false)
}

func testCacheSnapshotIdentityDrift(t *testing.T, afterRevision bool) {
	t.Helper()
	for _, mutation := range []string{"link", "unlink", "conversation type"} {
		t.Run(mutation, func(t *testing.T) {
			must, check := require.New(t), assert.New(t)
			root := t.TempDir()
			dbPath, cache := filepath.Join(root, "test.db"), filepath.Join(root, "analytics")
			st, err := store.Open(dbPath)
			must.NoError(err)
			t.Cleanup(func() { _ = st.Close() })
			must.NoError(st.InitSchema())
			src, err := st.GetOrCreateSource("gmail", "archive@example.com")
			must.NoError(err)
			conv, err := st.EnsureConversationWithType(src.ID, "thread", "email_thread", "Synthetic conversation")
			must.NoError(err)
			a, err := st.EnsureParticipant("a@example.com", "A", "example.com")
			must.NoError(err)
			b, err := st.EnsureParticipant("b@example.com", "B", "example.com")
			must.NoError(err)
			addMessage := func(key string) int64 {
				t.Helper()
				id, err := st.UpsertMessage(&store.Message{SourceID: src.ID, ConversationID: conv, SourceMessageID: key, MessageType: "email", SentAt: sql.NullTime{Time: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC), Valid: true}, SenderID: sql.NullInt64{Int64: a, Valid: true}})
				must.NoError(err)
				must.NoError(st.ReplaceMessageRecipients(id, "from", []int64{a}, nil))
				must.NoError(st.ReplaceMessageRecipients(id, "to", []int64{b}, nil))
				return id
			}
			first := addMessage("first")
			if mutation == "unlink" {
				_, err = st.LinkParticipants(a, b)
				must.NoError(err)
			}
			_, err = buildCache(dbPath, cache, true)
			must.NoError(err)
			addMessage("second")
			before := cacheNeedsBuild(dbPath, cache)
			must.True(before.HasNew)
			must.False(before.FullRebuild)
			mutate := func() {
				switch mutation {
				case "link":
					_, err = st.LinkParticipants(a, b)
				case "unlink":
					_, err = st.UnlinkParticipants(a, b)
				default:
					_, err = st.DB().Exec(`UPDATE conversations SET conversation_type='group_chat' WHERE id=?`, conv)
				}
				must.NoError(err)
			}
			if afterRevision {
				buildCacheBeforeParticipantClustersHook = mutate
			} else {
				buildCacheBeforeIdentitySnapshotHook = mutate
			}
			t.Cleanup(func() {
				buildCacheBeforeIdentitySnapshotHook = nil
				buildCacheBeforeParticipantClustersHook = nil
			})
			_, err = buildCacheAuto(dbPath, cache)
			must.NoError(err)
			buildCacheBeforeIdentitySnapshotHook = nil
			buildCacheBeforeParticipantClustersHook = nil
			db, err := sql.Open("duckdb", "")
			must.NoError(err)
			defer func() { _ = db.Close() }()
			activity := filepath.Join(cache, "relationship_activity", "**", "*.parquet")
			var count int
			if mutation == "conversation type" {
				must.NoError(db.QueryRow(`SELECT COUNT(*) FROM read_parquet(?) WHERE message_id=? AND conversation_type!='group_chat'`, activity, first).Scan(&count))
				check.Zero(count, "old activity must use the new conversation type")
			} else {
				must.NoError(db.QueryRow(`SELECT COUNT(*) FROM read_parquet(?) WHERE message_id=? AND canonical_id=?`, activity, first, b).Scan(&count))
				if mutation == "link" {
					check.Zero(count, "old activity must use the merged canonical identity")
				} else {
					check.Positive(count, "old activity must restore the split identity")
				}
			}
			// The marker intentionally lags a mutation after its revision read;
			// a later automatic pass must converge without losing the mutation.
			if afterRevision {
				_, err = buildCacheAuto(dbPath, cache)
				must.NoError(err)
			}
			check.False(cacheNeedsBuild(dbPath, cache).NeedsBuild, "publication must be current after repairing snapshot drift")
		})
	}
}
