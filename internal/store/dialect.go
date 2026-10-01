package store

import (
	"context"
	"database/sql"
	"time"
)

// FTSDoc is the set of fields the dialect needs to upsert a message into
// the full-text search index.
type FTSDoc struct {
	MessageID int64
	Subject   string
	Body      string
	FromAddr  string
	ToAddrs   string
	CcAddrs   string
}

// ColumnMigration is a single ALTER TABLE ADD COLUMN statement used by
// SQLiteDialect.LegacyColumnMigrations to evolve older SQLite databases.
type ColumnMigration struct {
	SQL  string // full ALTER TABLE ... ADD COLUMN statement
	Desc string // short label for error messages
}

// WatermarkBounds contains the database clock and the content-change feed's
// commit bound. Now is published as server_time. CommitBound is the page's
// exclusive upper bound: writes can receive content_changed_at stamps before
// their transactions commit, so paging up to Now could skip invisible rows.
//
// SQLite establishes the bound by probing its single writer slot. CommitBound
// is the last instant that slot was observed free and never exceeds Now. Its
// lag measures the age of that observation, not the age of an open transaction;
// periods without polling can make the lag much larger than the writer's age.
// See docs/api-server.md for the feed's guarantees and exceptions.
//
// A zero CommitBound means no successful probe has established a bound yet.
// The page is empty until one succeeds; see SQLiteDialect.ReadWatermarkBounds.
type WatermarkBounds struct {
	Now         time.Time
	CommitBound time.Time
}

// Dialect groups SQLite SQL generation, archive migrations, and search behavior.
type Dialect interface {
	// DriverName returns the database/sql driver name.
	DriverName() string

	// UnicodeLowerExpression returns a Unicode-aware lowercasing SQL expression
	// using the deterministic scalar registered on every connection.
	UnicodeLowerExpression(expr string) string

	// BlobPrefixSQL returns an expression that reads at most the bytes bound by
	// its one placeholder from a BLOB column. Repair paths use it before scanning
	// raw archive payloads so database/sql does not materialize whole MIME bodies.
	BlobPrefixSQL(column string) string

	// Now returns the SQL expression for the current timestamp.
	Now() string

	// ContentChangedNow returns the SQL expression that stamps content_changed_at
	// with millisecond resolution. Equal timestamps still need the id tiebreaker
	// in change-feed cursors. The textual format must be consistent for lexical
	// comparisons.
	ContentChangedNow() string

	// TimestampParam converts a Go time to the parameter form that compares
	// correctly against a stored timestamp. SQLite stores text and the driver
	// serializes time.Time with a "+00:00" suffix, so unformatted binds would sort
	// below equal stored values and omit rows sharing the cursor instant.
	TimestampParam(t time.Time) any

	// ReadWatermarkBounds reads the database clock and content-change commit
	// bound described by WatermarkBounds. SQLite serializes writers: acquiring
	// the writer slot proves no write is in flight. Called once per page on the
	// pooled handle, outside any transaction held by the caller.
	ReadWatermarkBounds(ctx context.Context, db *sql.DB) (WatermarkBounds, error)

	// Full-text search

	// FTSUpsert inserts or updates one message in the FTS5 index, keeping the
	// SQL and duplicated rowid argument together.
	FTSUpsert(q querier, doc FTSDoc) error

	// FTSSearchClause returns the join, predicate, and ordering fragments for
	// full-text search with ? placeholders. orderArgCount is zero for FTS5
	// because rank is an implicit column. Callers compose the fragments into
	// the final query.
	FTSSearchClause() (join, where, orderBy string, orderArgCount int)

	// FTSDeleteSQL returns the SQL to remove FTS entries for messages belonging to
	// a given source. Takes one parameter: source_id.
	FTSDeleteSQL() string

	// InvalidateFTSForMessage removes or marks stale one message's search
	// document before its canonical body changes. This prevents a failed
	// best-effort reindex from leaving an old body searchable as an exact hit.
	InvalidateFTSForMessage(q querier, messageID int64) error

	// FTSBackfillBatchSQL returns the SQL to populate the search index for a range of message IDs.
	// Uses two ? placeholders for the ID range: WHERE m.id >= ? AND m.id < ?
	FTSBackfillBatchSQL() string

	// FTSAvailable reports whether the FTS5 virtual table is available. Probe
	// failures report unavailable with a nil error, including builds without
	// FTS5. Cancellation instead returns the context error: it must not leave
	// the caller believing an interrupted probe proved search unavailable.
	FTSAvailable(ctx context.Context, db *sql.DB) (bool, error)

	// FTSNeedsBackfill reports whether the FTS index needs to be populated.
	FTSNeedsBackfill(ctx context.Context, db *sql.DB) bool

	// FTSNeedsBackfillQuick is a cheap approximation of FTSNeedsBackfill for
	// hot paths: it must never take longer than a few index lookups. True
	// means the index is visibly behind (a backfill is certainly needed);
	// false is not authoritative — on SQLite the MAX(rowid)-vs-MAX(id)
	// comparison misses interior holes that only the full anti-join finds.
	FTSNeedsBackfillQuick(ctx context.Context, db *sql.DB) bool

	// FTSClearSQL returns the SQL to clear all FTS data before a full backfill.
	FTSClearSQL() string

	// SchemaFTS returns the embedded filename containing FTS DDL used during
	// schema initialization.
	SchemaFTS() string

	// FTSRebuildSchema drops and recreates the FTS5 infrastructure. The caller
	// then backfills the index. This repairs malformed shadow-table state that
	// an in-place rebuild cannot clear. The querier binds the operation to the
	// maintenance transaction and its cancellation context.
	FTSRebuildSchema(ctx context.Context, q contextQuerier) error

	// EnsureFTSIndex is a no-op for SQLite: SchemaFTS creates messages_fts.
	// InitSchema calls this after legacy column migrations with a querier
	// bound to its cancellation context.
	EnsureFTSIndex(q querier) error

	// ValidateMessageWatermarks checks inexpensive watermark invariants that
	// must hold on every open even when the versioned trigger migration is
	// already applied.
	ValidateMessageWatermarks(q querier) error

	// EnsureTriggers recreates the database-maintained last_modified and
	// content_changed_at triggers after legacy column migrations. Recreating
	// them applies tracked-column changes to existing archives. The UPDATE OF
	// scope for trg_messages_last_modified uses the live column list; the
	// message_bodies trigger pair lives in schema.sql. InitSchema supplies a
	// querier bound to its maintenance transaction and cancellation context.
	EnsureTriggers(q querier) error

	// EnsureActivityProjectionTriggers repairs the durable activity queue
	// triggers independently of the message watermark trigger migration.
	EnsureActivityProjectionTriggers(q querier) error

	// LegacyColumnMigrations returns ALTER TABLE ADD COLUMN statements for
	// older archives. IsDuplicateColumnError makes repeated application safe;
	// fresh archives already have the columns from schema.sql.
	LegacyColumnMigrations() []ColumnMigration

	// DatabaseSize returns allocated database bytes: page_count times page_size,
	// excluding WAL/SHM files. In-memory SQLite databases report zero.
	DatabaseSize(ctx context.Context, db *sql.DB, dbPath string) (int64, error)

	// Connection lifecycle

	// SchemaFiles returns the filenames of embedded schema files to execute during InitSchema.
	SchemaFiles() []string

	// CheckpointWAL checkpoints the SQLite WAL.
	CheckpointWAL(db *sql.DB) error
	// CheckpointWALContext checkpoints the WAL with cancellation for busy waits.
	CheckpointWALContext(ctx context.Context, db *sql.DB) error
	// CheckpointWALPassive checkpoints without waiting for readers or writers.
	// The context bounds pool acquisition and the PRAGMA.
	CheckpointWALPassive(ctx context.Context, db *sql.DB) error

	// Schema migration

	// SchemaStaleCheck returns the SQL to check whether migrations are needed.
	SchemaStaleCheck() string

	// IsDuplicateColumnError returns true if the error indicates an ALTER TABLE
	// ADD COLUMN failed because the column already exists.
	IsDuplicateColumnError(err error) bool

	// Error handling

	// IsConflictError returns true if the error indicates a unique constraint violation.
	IsConflictError(err error) bool

	// IsNoSuchTableError returns true if the error indicates a missing table.
	IsNoSuchTableError(err error) bool

	// IsNoSuchModuleError reports a missing module, such as a build without FTS5.
	IsNoSuchModuleError(err error) bool

	// IsReturningError reports an unsupported RETURNING clause (SQLite < 3.35).
	IsReturningError(err error) bool

	// IsBusyError returns true if the error indicates the database is held
	// by another connection, either busy (SQLITE_BUSY) or locked
	// (SQLITE_LOCKED). Used to surface actionable errors from maintenance
	// commands that need exclusive access.
	IsBusyError(err error) bool

	// BoolTrueExpr returns a predicate for SQLite boolean values stored as
	// 0/1 integers.
	BoolTrueExpr(col string) string

	// RFC822CanonicalIDExpr groups stored Message-IDs after removing one valid
	// pair of angle brackets. It operates on BLOB bytes because SQLite TEXT
	// length and substring functions stop at embedded NUL.
	RFC822CanonicalIDExpr(col string) string

	// RFC822CanonicalIDIndexDefinition returns the ON messages portion of the
	// canonical Message-ID/source index. source_id lets scope filtering use the
	// index, while Message-ID remains first for ordered GROUP BY. The expression
	// must match RFC822CanonicalIDExpr("rfc822_message_id") byte for byte.
	RFC822CanonicalIDIndexDefinition() string

	// BuildFTSArg formats prefix-matching terms as space-joined `"term"*`
	// expressions; FTS5 treats spaces as AND. It drops unusable terms and matches
	// the query package's search syntax. When the result is empty, callers must
	// substitute a FALSE predicate instead of sending an invalid empty MATCH.
	BuildFTSArg(terms []string) string

	// JSONIsDistinctExpr returns a null-safe comparison between stored JSON
	// text and one bound JSON value.
	JSONIsDistinctExpr(col string) string

	// BeginExclusive starts BEGIN EXCLUSIVE to block concurrent writers,
	// including sync_runs inserts. WAL mode lets readers proceed.
	BeginExclusive(ctx context.Context, conn *sql.Conn) error

	// BeginWriteSQL returns BEGIN IMMEDIATE to reserve the writer slot before
	// a read-modify-write transaction takes its first snapshot.
	BeginWriteSQL() string

	// RowWriterLockSQL returns a self-assign UPDATE keyed by one ? parameter
	// that obtains the writer slot before a deferred transaction's first read.
	// Reading first and locking later can lose SQLITE_BUSY_SNAPSHOT to another
	// writer without consulting the busy handler. No stored value changes.
	RowWriterLockSQL(table, column string) string
}
