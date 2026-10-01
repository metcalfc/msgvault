package testutil

import (
	"testing"

	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil/sqlitetest"
)

// NewTestStore creates a temporary SQLite archive and cleans it up after the test.
func NewTestStore(t *testing.T) *store.Store { t.Helper(); return NewSQLiteTestStore(t) }

// NewSQLiteTestStore clones the production-initialized SQLite template.
func NewSQLiteTestStore(t *testing.T) *store.Store { t.Helper(); return sqlitetest.New(t) }
