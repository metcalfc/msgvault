package dbtest

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeT implements testing.TB and captures fatal/error calls instead of aborting.
// It records the last Errorf or Fatalf message and panics on FailNow so callers
// can detect that a fatal path was reached. This supports both stdlib t.Fatalf
// usage and testify require.* helpers (which call Errorf + FailNow).
type fakeT struct {
	testing.TB

	fatalMsg string
}

func (f *fakeT) Errorf(format string, args ...any) {
	f.fatalMsg = fmt.Sprintf(format, args...)
}

func (f *fakeT) Fatalf(format string, args ...any) {
	f.fatalMsg = fmt.Sprintf(format, args...)
	panic("fatalf") // stop execution in the caller
}

func (f *fakeT) FailNow() {
	panic("failnow")
}

func (f *fakeT) Helper() {}

func TestAddMessage_SourceIDMatchesConversation(t *testing.T) {
	tdb := NewTestDB(t)
	tdb.SeedStandardDataSet()

	// Happy path: SourceID 1 matches conversation 1's source_id.
	id := tdb.AddMessage(MessageOpts{
		SourceID:       1,
		ConversationID: 1,
		Subject:        "match",
		SentAt:         "2024-06-01 10:00:00",
	})
	require.NotZero(t, id, "expected non-zero message ID")
}

func TestAddMessage_MismatchedSourceID(t *testing.T) {
	tdb := NewTestDB(t)
	tdb.SeedStandardDataSet()

	src2 := tdb.AddSource(SourceOpts{Identifier: "other@gmail.com"})

	ft := &fakeT{TB: t}
	fakeTDB := &TestDB{
		DB:            tdb.DB,
		T:             ft,
		nextMessageID: tdb.nextMessageID,
	}

	var caught bool
	func() {
		defer func() {
			if r := recover(); r != nil {
				caught = true
			}
		}()
		fakeTDB.AddMessage(MessageOpts{
			SourceID:       src2,
			ConversationID: 1,
			Subject:        "mismatch",
			SentAt:         "2024-06-01 11:00:00",
		})
	}()

	require.True(t, caught, "expected fatal for mismatched SourceID")
	require.NotEmpty(t, ft.fatalMsg, "expected fatal for mismatched SourceID")
	t.Logf("got expected fatal: %s", ft.fatalMsg)
}

func TestAddMessage_DBErrorFailsTest(t *testing.T) {
	tdb := NewTestDB(t)
	tdb.SeedStandardDataSet()

	ft := &fakeT{TB: t}
	fakeTDB := &TestDB{
		DB:            tdb.DB,
		T:             ft,
		nextMessageID: tdb.nextMessageID,
	}

	// Close the DB to force a non-ErrNoRows error on the source_id lookup.
	_ = tdb.DB.Close()

	func() {
		defer func() { _ = recover() }()
		fakeTDB.AddMessage(MessageOpts{
			ConversationID: 1,
			Subject:        "db error",
			SentAt:         "2024-06-01 12:00:00",
		})
	}()

	require.NotEmpty(t, ft.fatalMsg, "expected fatal for DB error on source_id lookup")
	t.Logf("got expected fatal: %s", ft.fatalMsg)
}

func TestAddMessage_MissingConversation(t *testing.T) {
	tdb := NewTestDB(t)
	tdb.SeedStandardDataSet()

	ft := &fakeT{TB: t}
	fakeTDB := &TestDB{
		DB:            tdb.DB,
		T:             ft,
		nextMessageID: tdb.nextMessageID,
	}

	var caught bool
	func() {
		defer func() {
			if r := recover(); r != nil {
				caught = true
			}
		}()
		fakeTDB.AddMessage(MessageOpts{
			SourceID:       1,
			ConversationID: 999,
			Subject:        "missing conv",
			SentAt:         "2024-06-01 12:00:00",
		})
	}()

	require.True(t, caught, "expected fatal for missing conversation")
	require.NotEmpty(t, ft.fatalMsg, "expected fatal for missing conversation")
	t.Logf("got expected fatal: %s", ft.fatalMsg)
}

func TestNewTestDBProductionInvariants(t *testing.T) {
	fixture := NewTestDB(t)
	var foreignKeys int
	require.NoError(t, fixture.DB.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys))
	require.Equal(t, 1, foreignKeys)
	var archiveID string
	require.NoError(t, fixture.DB.QueryRow("SELECT value FROM archive_metadata WHERE key = 'archive_uid'").Scan(&archiveID))
	require.NotEmpty(t, archiveID)
	var ftsRows int
	require.NoError(t, fixture.DB.QueryRow("SELECT COUNT(*) FROM messages_fts").Scan(&ftsRows))
	_, err := fixture.DB.Exec("INSERT INTO message_labels (message_id, label_id) VALUES (999, 999)")
	require.Error(t, err, "production foreign keys reject nonexistent message and label")
}

func TestNewTestDBWithoutFTSRetainsProductionInvariants(t *testing.T) {
	fixture := NewTestDBWithoutFTS(t)
	_, err := fixture.DB.Exec("SELECT rowid FROM messages_fts")
	require.Error(t, err)
	var foreignKeys int
	require.NoError(t, fixture.DB.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys))
	require.Equal(t, 1, foreignKeys)
}
