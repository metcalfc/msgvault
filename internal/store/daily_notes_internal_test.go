package store

import (
	"context"
	"errors"
	"testing"

	"github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
)

func TestDailyNoteRetryClassifier(t *testing.T) {
	assert := assert.New(t)
	sqliteStore := &Store{dialect: &SQLiteDialect{}}

	assert.True(dailyNoteRetryable(context.Background(), sqliteStore,
		sqlite3.Error{Code: sqlite3.ErrBusy}))
	assert.False(dailyNoteRetryable(context.Background(), sqliteStore,
		sqlite3.Error{Code: sqlite3.ErrConstraint}))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	assert.False(dailyNoteRetryable(cancelled, sqliteStore,
		sqlite3.Error{Code: sqlite3.ErrBusy}))
	assert.False(dailyNoteRetryable(context.Background(), sqliteStore, errors.New("plain")))
}
