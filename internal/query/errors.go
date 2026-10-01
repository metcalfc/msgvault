package query

import (
	"database/sql"
	"errors"
)

// ErrNotImplemented marks an unsupported optional engine capability.
var ErrNotImplemented = errors.New("query: method not implemented for this engine")

// NewEngine constructs the archive query engine. DuckDB analytics are opened
// separately against the Parquet cache.
func NewEngine(db *sql.DB) Engine { return NewSQLiteEngine(db) }
