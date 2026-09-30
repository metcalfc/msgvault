package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDaemonArchiveSQLUsesRestrictedEngine(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	c, s := openTestDaemonAnalyticsStore(t)
	c.Analytics.AutoBuildCache = false
	_, err := s.DB().Exec(`
		INSERT INTO sources (id, source_type, identifier) VALUES (1, 'gmail', 'user@example.com');
		INSERT INTO conversations (id, source_id, source_conversation_id, conversation_type)
			VALUES (1, 1, 'thread-1', 'email_thread');
		INSERT INTO messages (id, source_id, source_message_id, conversation_id, message_type, sent_at)
			VALUES (1, 1, 'message-1', 1, 'email', '2024-01-01 00:00:00');
	`)
	requirements.NoError(err)
	_, err = buildCache(c.DatabaseDSN(), c.AnalyticsDir(), true)
	requirements.NoError(err)
	ownerEngine, err := openDaemonDuckDBEngine(c, s)
	requirements.NoError(err)
	t.Cleanup(func() { requirements.NoError(ownerEngine.Close()) })
	jobs := newCacheBuildJobs(t.Context(), nil, func(context.Context, buildCacheMode) error { return nil })

	result, accepted, err := runDaemonSQLQueryWithJobs(t.Context(), c, s, ownerEngine,
		"SELECT COUNT(*) FROM messages", daemonSQLQueryOptions{archiveOnly: true}, jobs)
	requirements.NoError(err)
	assertions.Nil(accepted)
	requirements.Len(result.Rows, 1)
	assertions.EqualValues(1, result.Rows[0][0])

	outside := filepath.Join(t.TempDir(), "outside.txt")
	requirements.NoError(os.WriteFile(outside, []byte("synthetic outside content"), 0o600))
	sql := "SELECT content FROM read_text('" + strings.ReplaceAll(filepath.ToSlash(outside), "'", "''") + "')"
	_, _, err = runDaemonSQLQueryWithJobs(t.Context(), c, s, ownerEngine,
		sql, daemonSQLQueryOptions{archiveOnly: true}, jobs)
	requirements.Error(err)
	assertions.Contains(err.Error(), "Permission Error")

	result, _, err = runDaemonSQLQueryWithJobs(t.Context(), c, s, ownerEngine,
		sql, daemonSQLQueryOptions{}, jobs)
	requirements.NoError(err)
	assertions.Equal("synthetic outside content", result.Rows[0][0])
}

func TestDaemonSQLStreamsPublishedArchive(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	c, s := openTestDaemonAnalyticsStore(t)
	c.Analytics.AutoBuildCache = false
	_, err := buildCache(c.DatabaseDSN(), c.AnalyticsDir(), true)
	requirements.NoError(err)
	engine, err := openDaemonDuckDBEngine(c, s)
	requirements.NoError(err)
	t.Cleanup(func() { requirements.NoError(engine.Close()) })
	jobs := newCacheBuildJobs(t.Context(), nil, func(context.Context, buildCacheMode) error { return nil })
	for _, archiveOnly := range []bool{false, true} {
		count := 0
		consume := func(columns []string, row []any) error {
			assertions.Equal([]string{"id"}, columns)
			if row != nil {
				count++
			}
			return nil
		}
		result, accepted, err := runDaemonSQLQueryWithJobs(t.Context(), c, s, engine, "SELECT range AS id FROM range(10001)", daemonSQLQueryOptions{archiveOnly: archiveOnly, consume: consume}, jobs)
		requirements.NoError(err)
		assertions.Nil(accepted)
		assertions.Nil(result.Rows)
		assertions.Equal(10001, count)
		assertions.Equal(count, result.RowCount)
		requirements.NotNil(result.Cache)
	}
}
