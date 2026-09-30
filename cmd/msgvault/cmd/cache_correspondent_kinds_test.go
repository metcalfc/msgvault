package cmd

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/correspondentkind"
	"go.kenn.io/msgvault/internal/identityindex"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

// cachedPeopleKind reads the kind columns relationship_people carries for
// one cluster.
func cachedPeopleKind(t *testing.T, analyticsDir string, canonicalID int64) (sql.NullString, sql.NullString) {
	t.Helper()
	duck, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = duck.Close() }()
	var kind, source sql.NullString
	require.NoError(t, duck.QueryRow(`SELECT correspondent_kind, correspondent_kind_source
		FROM read_parquet(?) WHERE canonical_id = ?`,
		filepath.Join(analyticsDir, identityindex.DatasetPeople, "*.parquet"), canonicalID).Scan(&kind, &source))
	return kind, source
}

func TestCorrespondentKindDriftRefreshesCachedKinds(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "test.db")
	analyticsDir := filepath.Join(tmp, "analytics")
	st, err := store.Open(dbPath)
	requirements.NoError(err)
	requirements.NoError(st.InitSchema())
	alerts, err := st.EnsureParticipant("alerts@example.com", "Example Alerts", "example.com")
	requirements.NoError(err)
	source, err := st.GetOrCreateSource("gmail", "user@example.com")
	requirements.NoError(err)
	conv := insertExportMessagesConversation(t, st, source.ID, "conversation", "Test")
	insertExportMessagesMessage(t, st, source.ID, conv, "message", time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC), "body")
	_, err = st.DB().Exec(st.Rebind(`UPDATE messages SET sender_id = ? WHERE source_id = ?`), alerts, source.ID)
	requirements.NoError(err)
	requirements.NoError(st.Close())
	_, err = buildCache(dbPath, analyticsDir, true)
	requirements.NoError(err)
	kind, _ := cachedPeopleKind(t, analyticsDir, alerts)
	assertions.False(kind.Valid, "an unclassified cluster has no kind")
	before, err := query.ReadCacheSyncState(analyticsDir)
	requirements.NoError(err)

	st, err = store.Open(dbPath)
	requirements.NoError(err)
	_, err = st.WriteDerivedCorrespondentKindsContext(t.Context(), []store.DerivedCorrespondentKind{{
		ParticipantID: alerts, Source: correspondentkind.SourceRule, Kind: correspondentkind.Automated,
	}})
	requirements.NoError(err)
	requirements.NoError(st.Close())

	stale := cacheNeedsBuild(dbPath, analyticsDir)
	requirements.True(stale.NeedsBuild)
	assertions.True(stale.HasCorrespondentKindDrift)
	assertions.False(stale.FullRebuild)
	assertions.True(derivedDriftOnly(stale))
	result, err := buildCacheDerivedOnly(dbPath, analyticsDir)
	requirements.NoError(err)
	assertions.True(result.IdentityOnly)
	after, err := query.ReadCacheSyncState(analyticsDir)
	requirements.NoError(err)
	assertions.Equal(before.CorrespondentKindRevision+1, after.CorrespondentKindRevision)
	assertions.NotEqual(before.Revision(), after.Revision())
	kind, kindSource := cachedPeopleKind(t, analyticsDir, alerts)
	assertions.Equal(sql.NullString{String: "automated", Valid: true}, kind)
	assertions.Equal(sql.NullString{String: "rule", Valid: true}, kindSource)
	assertions.False(cacheNeedsBuild(dbPath, analyticsDir).NeedsBuild)
}
