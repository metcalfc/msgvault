// Package store provides database access for msgvault.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-sqlite3"
	"go.kenn.io/kit/atomicfile"
	"go.kenn.io/msgvault/internal/sqliteutil"
)

//go:embed schema.sql schema_sqlite.sql
var schemaFS embed.FS

// Store provides database operations for msgvault.
//
// The db field wraps a *sql.DB with a thin logging adapter that
// emits slog records for every Query / Exec / QueryRow call.
// Because loggedDB embeds *sql.DB and overrides the instrumented
// methods, existing store code that does s.db.Query(...) compiles
// unchanged and automatically routes through the logger.
type Store struct {
	db                   *loggedDB
	dbPath               string
	sqliteFilesystemPath string
	dialect              Dialect
	readOnly             bool // Opened via OpenReadOnly; skips WAL checkpoint on close
	fts5Available        bool // Whether FTS5 is available for full-text search
	closeCleanup         func()
	// directoryProjectionReady becomes true only after InitSchema has created
	// the projection tables and dirty-marking triggers. Every writable Store
	// transaction then refreshes its affected Directory rows before commit.
	directoryProjectionReady bool

	// syncGeneration is immutable metadata on a per-run Store view.
	// Mutating transactions on that view fence the exact running source
	// generation before touching archive rows. The shared Store remains
	// unscoped so unrelated maintenance and concurrent source syncs do not
	// share mutable run state.
	syncGeneration *syncGeneration
	syncBase       *Store
	// syncExecutionLocks is shared with sync-scoped views. Each held lock is
	// owned by the worker process, not by a durable sync_runs row.
	syncExecutionLocks *syncExecutionLockState

	cardDAVPersonOperationsMu sync.Mutex
	cardDAVPersonOperations   map[int64]*cardDAVPersonOperation

	sqliteOptimizeMu sync.Mutex
	// syncOptimizeMu guards lastSyncOptimize, which throttles the planner
	// maintenance a successful sync triggers (see optimizeAfterSync).
	syncOptimizeMu   sync.Mutex
	lastSyncOptimize time.Time
	// syncOptimizeNow and checkpointRetryBackoff are test seams, nil in
	// production.
	syncOptimizeNow           func() time.Time
	syncOptimizeHook          func()
	checkpointRetryBackoff    []time.Duration
	documentVectorOperationMu sync.Mutex
	// Test-only seams into migration, backfill, and transaction paths, nil in
	// production and settable only from test files. They belong to the
	// Store rather than the package because more than one Store can be
	// active at once inside a single test binary — test fixtures build their
	// schemas concurrently — and a hook installed by one test must never fire
	// on another Store's work. As package-level variables
	// they were also a data race between a test that installs one and any
	// concurrent migration that reads it.
	initSchemaWindowHook                  func()
	beforeLargeIndexBuildHook             func()
	attributeSeedReadHook                 func(slug string)
	contentChangedBackfillBatchHook       func(fromID, toID int64) error
	attachmentRoleRepairPreparedHook      func()
	listIDRepairBeforeApplyHook           func()
	listIDRepairAfterScanHook             func(context.Context, *loggedTx, []listIDRepairUpdate) error
	listIDRepairAfterFingerprintLockHook  func()
	imapLabelRepairPerMessageHook         func(messageID int64)
	cardDAVConflictResolveSnapshotHook    func()
	cardDAVTombstonePrepareSnapshotHook   func()
	cardDAVReviewPersonLockHook           func()
	cardDAVCollisionIdentityLockHook      func()
	cardDAVPublicationStateReadHook       func()
	identityMatchAcceptBeforeDecisionHook func()
	// personDuplicateRevalidateHook receives the participants a
	// duplicate-person revalidation loaded; tests only.
	personDuplicateRevalidateHook          func(participantIDs []int64)
	contactMatchBindAfterPromoteHook       func()
	personEnrichmentReviewBeforeUpdateHook func(*loggedTx)
	senderRepairMessageLockHook            func()
	personOperationBeforeIdentityLockHook  func()
	personMergeAfterSnapshotHook           func()
	personEnrichmentClock                  func() time.Time
	personEnrichmentBudgetBarrier          func()
	personEnrichmentRunBarrier             func(phase string)
	personEnrichmentTxBarrier              func(phase string)
	personEnrichmentOwnershipBarrier       func(phase string, tx *loggedTx)
	personNetworkSourceReadHook            func(limit, count int)
	operationHistoryAfterAdapterReadHook   func(kind string)
	operationHistoryStatusAfterActiveHook  func(kind string)

	// Zero means "use the production batch size"; see
	// contentChangedBackfillBatch and rfc822IDBackfillBatch. Per-Store for
	// the same reason.
	contentChangedBackfillBatchSizeOverride int64
	rfc822IDBackfillBatchSizeOverride       int
	labelEmojiBatchSizeOverride             int
	// labelEmojiBatchHook, when set, runs after each committed recipient
	// batch; a non-nil return stops the cleanup there. Tests only.
	labelEmojiBatchHook func(lastID int64) error
}

// synchronous=FULL + fullfsync=true protects WAL writes against OS/power crashes
// (NORMAL only protects against process crashes). msgvault is commonly run as a
// laptop daemon (`msgvault serve`) where sleep/wake, forced reboots, and OOM kills
// give many opportunities to leave a torn page on disk; the write volume is tiny
// so the durability cost is negligible. fullfsync is macOS-only (F_FULLFSYNC
// fcntl) and a no-op on other platforms.
const defaultSQLiteParams = "?_journal_mode=WAL&_busy_timeout=30000&_synchronous=FULL&_fullfsync=true&_foreign_keys=ON"

// isSQLiteError checks if err is a sqlite3.Error with a message containing substr.
// This is more robust than strings.Contains on err.Error() because it first
// type-asserts to the specific driver error type using errors.As.
// Handles both value (sqlite3.Error) and pointer (*sqlite3.Error) forms.
//
// SQLiteDialect's error predicates are thin wrappers around this helper; it also
// services subset.go (which has not been migrated to Dialect).
func isSQLiteError(err error, substr string) bool {
	if sqliteErr, ok := errors.AsType[sqlite3.Error](err); ok {
		return strings.Contains(sqliteErr.Error(), substr)
	}
	var sqliteErrPtr *sqlite3.Error
	if errors.As(err, &sqliteErrPtr) && sqliteErrPtr != nil {
		return strings.Contains(sqliteErrPtr.Error(), substr)
	}
	return false
}

// testSQLiteParams configures SQLite for ephemeral test databases: WAL mode
// for concurrency parity with production, but synchronous=OFF (no fsync per
// commit). Test DBs live in t.TempDir() and are discarded at test exit, so
// durability against OS crashes is irrelevant — and on slow-fsync platforms
// such as CI runners, the production FULL setting can push bulk-import
// tests past their timing tripwires.
const testSQLiteParams = "?_journal_mode=WAL&_busy_timeout=30000&_synchronous=OFF&_foreign_keys=ON"

// Open opens or creates a SQLite database at a file path or file: URI.
// Server database connection strings are rejected before filesystem access.
func Open(dbPath string) (*Store, error) {
	return OpenContext(context.Background(), dbPath)
}

// OpenContext opens or creates a database at dbPath and cancels connection
// setup and capability probes when ctx is canceled.
func OpenContext(ctx context.Context, dbPath string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return openSQLiteContext(ctx, dbPath, defaultSQLiteParams)
}

// OpenForTest opens or creates a database tuned for test use: ephemeral,
// fast, with durability disabled.
//
// Not for production use — a process crash mid-test can leave a corrupt
// database, which is fine because tests recreate it from scratch.
func OpenForTest(dbPath string) (*Store, error) {
	return openSQLite(dbPath, testSQLiteParams)
}

// openSQLite opens a SQLite database at the given file path with the
// supplied DSN parameters appended.
func openSQLite(dbPath, params string) (*Store, error) {
	return openSQLiteContext(context.Background(), dbPath, params)
}

func openSQLiteContext(ctx context.Context, dbPath, params string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	normalizedDSN, filesystemPath, err := sqliteutil.ResolveDSN(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolve SQLite database path: %w", err)
	}
	// Ensure directory exists (skip for in-memory databases)
	if dbPath != ":memory:" && !strings.Contains(dbPath, ":memory:") {
		dir := filepath.Dir(filesystemPath)
		// #nosec G703 -- dbPath is the caller-selected database location; creating its parent is intentional.
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}

	dsn := appendSQLiteParams(normalizedDSN, params)
	db, err := sql.Open(sqliteutil.DriverName(), dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	// SQLite with WAL supports one writer + multiple readers.
	// Allow enough connections for concurrent reads (TUI async
	// queries, FTS backfill) while SQLite handles write serialization.
	// Exception: :memory: databases are per-connection, so multiple
	// connections would create separate databases.
	if dbPath == ":memory:" || strings.Contains(dbPath, ":memory:") {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(4)
	}

	dialect := &SQLiteDialect{}
	if err := ctx.Err(); err != nil {
		_ = db.Close()
		return nil, err
	}

	s := &Store{
		db:                   newLoggedDB(db),
		dbPath:               dbPath,
		sqliteFilesystemPath: filesystemPath,
		dialect:              dialect,
		syncExecutionLocks:   newSyncExecutionLockState(),
	}

	// Probe like the read-only opens do: a Store must know whether full-text
	// search is available in the database it just opened. InitSchema re-probes
	// after creating the FTS objects, so a caller that initializes a fresh
	// database still gets the right answer; a caller that opens an already
	// initialized one no longer has to run InitSchema to learn it.
	//
	available, err := dialect.FTSAvailable(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("probe FTS availability: %w", err)
	}
	s.fts5Available = available
	if err := s.detectDirectoryProjectionReadiness(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	return s, nil
}

func appendSQLiteParams(dsn, params string) string {
	if params == "" {
		return dsn
	}
	if strings.Contains(dsn, "?") {
		return dsn + "&" + strings.TrimPrefix(params, "?")
	}
	return dsn + params
}

// detectDirectoryProjectionReadiness distinguishes an old database without
// the optional Directory projection from one whose dirty queue must be
// respected by a read-only Store. It does not create or migrate anything.
func (s *Store) detectDirectoryProjectionReadiness(ctx context.Context) error {
	var installed bool
	query := `SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'directory_projection_dirty')`

	if err := s.db.QueryRowContext(ctx, query).Scan(&installed); err != nil {
		return fmt.Errorf("detect directory projection: %w", err)
	}
	s.directoryProjectionReady = installed
	return nil
}

// OpenReadOnly opens an existing database in read-only mode. Suitable for
// query-only workloads (MCP server) where multiple processes access the
// same database concurrently. Does not create the database, run migrations,
// or checkpoint WAL on close.
func OpenReadOnly(dbPath string) (*Store, error) {
	return OpenReadOnlyContext(context.Background(), dbPath)
}

// OpenReadOnlyContext opens an existing database for queries and cancels
// connection setup and capability probes when ctx is canceled.
func OpenReadOnlyContext(ctx context.Context, dbPath string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	normalizedDSN, filesystemPath, err := sqliteutil.ResolveDSN(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolve SQLite database path: %w", err)
	}
	if _, err := os.Stat(filesystemPath); err != nil {
		return nil, fmt.Errorf("database not found: %s "+
			"(run 'msgvault init-db' first)", dbPath)
	}

	// Use _query_only instead of mode=ro. WAL-mode databases may need
	// to create or update -wal/-shm sidecar files on open, which fails
	// under SQLITE_OPEN_READONLY. _query_only opens normally (so SQLite
	// can manage sidecars) but rejects all write SQL at the query layer.
	dsn := appendSQLiteParams(normalizedDSN, "?_query_only=true&_busy_timeout=5000")
	db, err := sql.Open(sqliteutil.DriverName(), dsn)
	if err != nil {
		return nil, fmt.Errorf("open database (read-only): %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	db.SetMaxOpenConns(4)

	dialect := &SQLiteDialect{}

	s := &Store{
		db:                   newLoggedDB(db),
		dbPath:               dbPath,
		sqliteFilesystemPath: filesystemPath,
		dialect:              dialect,
		readOnly:             true,
		syncExecutionLocks:   newSyncExecutionLockState(),
	}

	available, err := dialect.FTSAvailable(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("probe FTS availability: %w", err)
	}
	s.fts5Available = available
	if err := s.detectDirectoryProjectionReadiness(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	return s, nil
}

const sqliteOptimizeTimeout = time.Second

// Close checkpoints the WAL (unless read-only) and closes the database.
func (s *Store) Close() error {
	if !s.readOnly {
		{
			// Persist statistics for short-lived commands without draining a pool
			// that may still have a checked-out connection during shutdown.
			ctx, cancel := context.WithTimeout(context.Background(), sqliteOptimizeTimeout)
			// Log maintenance errors here so expected deadlines do not also emit SQL warnings.
			// Include 0x10 to bound ANALYZE even on a fresh connection without an analysis_limit.
			_, err := s.db.DB.ExecContext(ctx, "PRAGMA optimize=0x10012")
			logSQLiteOptimizeError("store close", err)
			cancel()
		}

		// Checkpoint WAL before closing to fold it back into the main
		// database. This prevents WAL accumulation across sessions and
		// reduces the risk of corruption from stale WAL entries.
		_ = s.CheckpointWAL()
	}
	lockErr := s.releaseAllSyncExecutionLocks()
	err := s.db.Close()
	if s.closeCleanup != nil {
		s.closeCleanup()
		s.closeCleanup = nil
	}
	return errors.Join(lockErr, err)
}

// CheckpointWAL forces a WAL checkpoint, folding the WAL back into the main
// database file. Uses TRUNCATE mode which also resets the WAL file to zero
// bytes. Returns nil on success; callers may log but should not fail on error.
func (s *Store) CheckpointWAL() error {
	return s.dialect.CheckpointWAL(s.db.DB)
}

// CheckpointWALContext forces a WAL checkpoint using ctx. SQLite checkpoints
// can wait for readers, so scheduled maintenance should pass its job context.
func (s *Store) CheckpointWALContext(ctx context.Context) error {
	return s.dialect.CheckpointWALContext(ctx, s.db.DB)
}

// optimizeSQLite refreshes persistent query-planner statistics when SQLite
// decides they are missing or stale. The 0x10000 bit makes SQLite consider all
// tables instead of relying on query history from whichever pooled connection
// database/sql selects.
func (s *Store) optimizeSQLite(ctx context.Context) error {
	return s.optimizeSQLiteWithin(ctx, sqliteOptimizeTimeout)
}

// optimizeSQLiteWithin is optimizeSQLite with a caller-chosen budget covering
// both the pool reservation and the statistics refresh.
func (s *Store) optimizeSQLiteWithin(ctx context.Context, timeout time.Duration) error {
	if s.readOnly {
		return nil
	}
	// Maintenance is best-effort, and an active call is already performing the
	// same update. Skip duplicates instead of making cancellation wait on it.
	if !s.sqliteOptimizeMu.TryLock() {
		return nil
	}
	defer s.sqliteOptimizeMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Reserve every pool slot before refreshing statistics. ANALYZE loads its
	// results only into the SQLite connection that runs it; holding every slot
	// lets us reload each existing connection and prevents database/sql from
	// opening an unrefreshed one concurrently. Connections opened after this
	// point load the persistent sqlite_stat tables when they read the schema.
	poolSize := max(1, s.db.Stats().MaxOpenConnections)
	connections := make([]*sql.Conn, 0, poolSize)
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()
	for range poolSize {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("reserve SQLite connection pool for planner statistics: %w", err)
		}
		connections = append(connections, conn)
	}

	// Bound each ANALYZE to a sample so a large archive refreshes its
	// statistics in milliseconds rather than scanning every index.
	for _, conn := range connections {
		if _, err := conn.ExecContext(ctx, "PRAGMA analysis_limit=1000"); err != nil {
			return fmt.Errorf("limit SQLite planner statistics analysis: %w", err)
		}
	}
	if _, err := connections[0].ExecContext(ctx, "PRAGMA optimize=0x10002"); err != nil {
		return fmt.Errorf("optimize SQLite planner statistics: %w", err)
	}
	for _, conn := range connections {
		if _, err := conn.ExecContext(ctx, "ANALYZE sqlite_schema"); err != nil {
			return fmt.Errorf("reload SQLite planner statistics: %w", err)
		}
	}
	return nil
}

func (s *Store) optimizeSQLiteBestEffort(ctx context.Context, trigger string) {
	logSQLiteOptimizeError(trigger, s.optimizeSQLite(ctx))
}

// syncOptimizeInterval throttles the planner maintenance successful syncs
// trigger; the daemon's daily SQLite maintenance job covers the rest.
const syncOptimizeInterval = 6 * time.Hour

// optimizeAfterSync runs planner maintenance after a successful sync at most
// once per syncOptimizeInterval, so busy archives do not pay a full pool
// reservation after every sync.
func (s *Store) optimizeAfterSync(ctx context.Context) {
	now := time.Now()
	if s.syncOptimizeNow != nil {
		now = s.syncOptimizeNow()
	}
	s.syncOptimizeMu.Lock()
	due := s.lastSyncOptimize.IsZero() || now.Sub(s.lastSyncOptimize) >= syncOptimizeInterval
	if due {
		s.lastSyncOptimize = now
	}
	s.syncOptimizeMu.Unlock()
	if due {
		if s.syncOptimizeHook != nil {
			s.syncOptimizeHook()
		}
		s.optimizeSQLiteBestEffort(ctx, "successful sync")
	}
}

// isSQLiteContention reports errors that mean the database was busy rather
// than broken: expected on a loaded archive and retried later.
func isSQLiteContention(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if sqliteErr, ok := errors.AsType[sqlite3.Error](err); ok {
		switch sqliteErr.Code {
		case sqlite3.ErrBusy, sqlite3.ErrLocked, sqlite3.ErrInterrupt:
			return true
		}
	}
	return false
}

func logSQLiteOptimizeError(trigger string, err error) {
	if err == nil {
		return
	}
	if isSQLiteContention(err) {
		slog.Debug("SQLite planner statistics maintenance interrupted",
			"trigger", trigger,
			"error", err.Error(),
		)
		return
	}
	slog.Warn("SQLite planner statistics maintenance failed",
		"trigger", trigger,
		"error", err.Error(),
	)
}

// DB returns the underlying *sql.DB for consumers that need to
// pass the raw handle elsewhere (e.g. the DuckDB engine's
// sqlite_scan wrapper). The wrapper's structured-logging
// behaviour is bypassed for those consumers — they're operating
// at a different abstraction layer.
func (s *Store) DB() *sql.DB {
	return s.db.DB
}

// BackupDatabase writes a point-in-time consistent copy of the SQLite database
// to dst using VACUUM INTO. Publishing requires hard links or an atomic
// no-replace rename; it fails rather than risk overwriting an existing target
// when neither is available.
func (s *Store) BackupDatabase(dst string) error {
	return s.BackupDatabaseContext(context.Background(), dst)
}

// BackupDatabaseContext is the request-aware form of BackupDatabase.
func (s *Store) BackupDatabaseContext(ctx context.Context, dst string) (returnErr error) {
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("backup target already exists: %s", dst)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect backup target %s: %w", dst, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	tempDir, err := os.MkdirTemp(
		filepath.Dir(dst),
		"."+filepath.Base(dst)+".tmp-*",
	)
	if err != nil {
		return fmt.Errorf("create temporary backup directory for %s: %w", dst, err)
	}
	defer func() {
		if err := os.RemoveAll(tempDir); err != nil {
			cleanupErr := fmt.Errorf("remove temporary backup directory %s: %w", tempDir, err)
			returnErr = errors.Join(returnErr, cleanupErr)
		}
	}()

	tempPath := filepath.Join(tempDir, "backup.db")
	if _, err := s.DB().ExecContext(ctx, "VACUUM INTO ?", tempPath); err != nil {
		return fmt.Errorf("vacuum into temporary backup for %s: %w", dst, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Publish without replacing: a file created at dst while VACUUM INTO ran
	// must survive. Deferred staging directory cleanup removes any staged name.
	publish := atomicfile.PublishNoReplace

	if err := publish(tempPath, dst); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("backup target already exists: %s", dst)
		}
		return fmt.Errorf("publish backup %s: %w", dst, err)
	}
	return nil
}

// WithExclusiveLock executes fn while holding an exclusive write lock on the
// database. In WAL mode this blocks concurrent writers (e.g. StartSync) while
// allowing reads (e.g. IsAttachmentPathReferenced) to proceed. Use this to
// serialize destructive file operations against concurrent sync attachment
// ingestion. The context controls both lock acquisition and the lifetime of
// the underlying connection; cancelling it aborts a pending BEGIN EXCLUSIVE
// and rolls back any held transaction.
//
// fn must NOT write through the store. The EXCLUSIVE lock is held on a
// dedicated connection (conn below), while every store write goes to the
// pool — a different connection. A write from fn would wait for the writer
// slot this call already holds. fn is for reads
// (ACCESS SHARE, which EXCLUSIVE permits) plus filesystem work only.
func (s *Store) WithExclusiveLock(ctx context.Context, fn func() error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := s.dialect.BeginExclusive(ctx, conn); err != nil {
		return fmt.Errorf("begin exclusive: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	if err := fn(); err != nil {
		return err
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit exclusive: %w", err)
	}
	committed = true
	return nil
}

// withTx executes fn within a database transaction. If fn returns an error,
// the transaction is rolled back; otherwise it is committed. The callback
// receives *loggedTx so every statement inside the transaction is logged.
func (s *Store) withTx(fn func(tx *loggedTx) error) error {
	return s.withTxContext(context.Background(), fn)
}

// withTxContext is the request-aware form of withTx. Cancelling ctx aborts
// connection acquisition and every context-aware statement in the
// transaction.
func (s *Store) withTxContext(ctx context.Context, fn func(tx *loggedTx) error) error {
	return s.withTxOptionsContext(ctx, nil, fn)
}

// withReadSnapshotContext gives a multi-statement aggregate read one stable,
// read-only SQLite transaction snapshot.
func (s *Store) withReadSnapshotContext(
	ctx context.Context, fn func(tx *loggedTx) error,
) error {
	return s.withTxOptionsContext(ctx, &sql.TxOptions{
		Isolation: sql.LevelRepeatableRead,
		ReadOnly:  true,
	}, fn)
}

func (s *Store) withTxOptionsContext(
	ctx context.Context, opts *sql.TxOptions, fn func(tx *loggedTx) error,
) error {
	start := time.Now()
	slog.Debug("sql tx begin")
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		slog.Warn("sql tx begin failed", "error", err.Error())
		return fmt.Errorf("begin tx: %w", err)
	}
	if s.syncGeneration != nil && (opts == nil || !opts.ReadOnly) {
		if err := s.fenceSyncGenerationTx(ctx, tx); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			slog.Warn("sql tx rollback failed",
				"error", rbErr.Error(),
				"fn_error", err.Error(),
				"duration_ms", time.Since(start).Milliseconds())
		} else {
			slog.Info("sql tx rollback",
				"reason", err.Error(),
				"duration_ms", time.Since(start).Milliseconds())
		}
		return err
	}
	if s.directoryProjectionReady && !s.readOnly && (opts == nil || !opts.ReadOnly) {
		if err := s.refreshDirectoryProjectionsBeforeCommitTx(ctx, tx); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		slog.Warn("sql tx commit failed",
			"error", err.Error(),
			"duration_ms", time.Since(start).Milliseconds())
		if errors.Is(err, sql.ErrTxDone) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
		}
		return err
	}
	// A tx crossing the slow threshold is a diagnostic, not a problem —
	// bulk syncs routinely commit 100ms+ batches — so it logs at Info and
	// only escalates to Warn at 10x the threshold, where something is
	// genuinely wrong (lock contention, an unindexed cascade).
	ms := time.Since(start).Milliseconds()
	switch slowMs := sqlLogSlowMs.Load(); {
	case slowMs > 0 && ms >= 10*slowMs:
		slog.Warn("sql tx slow", "duration_ms", ms)
	case slowMs > 0 && ms >= slowMs:
		slog.Info("sql tx slow", "duration_ms", ms)
	default:
		slog.Debug("sql tx commit", "duration_ms", ms)
	}
	return nil
}

// runMaintenance runs archive-scale maintenance inside one transaction.
// The context bounds transaction setup and the statements issued by fn.
// The logged transaction preserves the Store SQL and tracing conventions.
func (s *Store) runMaintenance(ctx context.Context, fn func(ctx context.Context, tx *loggedTx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin maintenance tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		if errors.Is(err, sql.ErrTxDone) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
		}
		return fmt.Errorf("commit maintenance tx: %w", err)
	}
	committed = true
	return nil
}

// queryInChunks executes a parameterized IN-query in chunks to stay within
// SQLite's parameter limit. queryTemplate must contain a single %s placeholder
// for the comma-separated "?" list. The prefix args are prepended before each
// chunk's args (e.g., a source_id filter).
// chunkQuerier abstracts the subset of *loggedDB that queryInChunks
// and execInChunks actually use. The Query path returns *loggedRows
// so streaming-query timing reflects scan-close, not just prepare.
type chunkQuerier interface {
	Query(query string, args ...any) (*loggedRows, error)
	QueryContext(ctx context.Context, query string, args ...any) (*loggedRows, error)
	Exec(query string, args ...any) (sql.Result, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func queryInChunks[T any](db chunkQuerier, ids []T, prefixArgs []any, queryTemplate string, fn func(*loggedRows) error) error {
	return queryInChunksContext(context.Background(), db, ids, prefixArgs, queryTemplate, fn)
}

func queryInChunksContext[T any](ctx context.Context, db chunkQuerier, ids []T, prefixArgs []any, queryTemplate string, fn func(*loggedRows) error) error {
	const chunkSize = 500
	for i := 0; i < len(ids); i += chunkSize {
		if err := ctx.Err(); err != nil {
			return err
		}

		end := min(i+chunkSize, len(ids))
		chunk := ids[i:end]

		placeholders := make([]string, len(chunk))
		args := slices.Clone(prefixArgs)
		for j, id := range chunk {
			placeholders[j] = "?"
			args = append(args, id)
		}

		query := fmt.Sprintf(queryTemplate, strings.Join(placeholders, ","))
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}

		for rows.Next() {
			if err := fn(rows); err != nil {
				_ = rows.Close()
				return err
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}

// chunkInsert describes a multi-row INSERT for insertInChunks.
// Prefix is everything up to "VALUES ", suffix is anything after the values
// (such as a conflict-handling clause). ValuesPerRow counts the
// parameters in one row's tuple (used to stay under the driver's parameter
// limit).
type chunkInsert struct {
	totalRows    int
	valuesPerRow int
	prefix       string
	suffix       string
}

// insertInChunks executes a multi-value INSERT in chunks to stay within SQLite's
// parameter limit (999). valueBuilder generates the VALUES placeholders and
// args for each chunk of row indices.
func insertInChunks(tx interface {
	Exec(query string, args ...any) (sql.Result, error)
}, c chunkInsert, valueBuilder func(start, end int) ([]string, []any)) error {
	// SQLite default SQLITE_MAX_VARIABLE_NUMBER is 999
	// Leave some margin for safety
	const maxParams = 900
	chunkSize := max(maxParams/c.valuesPerRow, 1)

	for i := 0; i < c.totalRows; i += chunkSize {
		end := min(i+chunkSize, c.totalRows)

		values, args := valueBuilder(i, end)
		query := c.prefix + strings.Join(values, ",") + c.suffix
		if _, err := tx.Exec(query, args...); err != nil {
			return err
		}
	}
	return nil
}

// execInChunks executes a parameterized DELETE/UPDATE with an IN-clause in chunks
// to stay within SQLite's parameter limit. queryTemplate must contain a single %s
// placeholder for the comma-separated "?" list. The prefix args are prepended before
// each chunk's args (e.g., a message_id filter).
func execInChunks[T any](db chunkQuerier, ids []T, prefixArgs []any, queryTemplate string) error {
	return execInChunksContext(context.Background(), db, ids, prefixArgs, queryTemplate)
}

// execInChunksContext is the context-aware form of execInChunks: every chunk's
// statement carries ctx, and the walk stops at the next chunk boundary once it
// is cancelled. Callers whose write scales with archive size — the legacy
// calendar-attribution backfill in particular — must use this form, because a
// chunked whole-table walk on a background context is unreachable by SIGINT and
// SIGTERM for as long as any one chunk waits on a lock.
func execInChunksContext[T any](
	ctx context.Context, db chunkQuerier, ids []T, prefixArgs []any, queryTemplate string,
) error {
	const chunkSize = 500
	for i := 0; i < len(ids); i += chunkSize {
		if err := ctx.Err(); err != nil {
			return err
		}

		end := min(i+chunkSize, len(ids))
		chunk := ids[i:end]

		placeholders := make([]string, len(chunk))
		args := slices.Clone(prefixArgs)
		for j, id := range chunk {
			placeholders[j] = "?"
			args = append(args, id)
		}

		query := fmt.Sprintf(queryTemplate, strings.Join(placeholders, ","))
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
	return nil
}

// Rebind returns the query with SQLite ? placeholders unchanged.
func (s *Store) Rebind(query string) string {
	return query
}

// FTS5Available returns whether FTS5 full-text search is available.
func (s *Store) FTS5Available() bool {
	return s.fts5Available
}

// IsBusyError reports whether err indicates another process holds the
// database (SQLITE_BUSY or SQLITE_LOCKED). Callers running maintenance
// operations that need exclusive access can use this to produce a
// user-actionable "stop other processes and retry" message.
func (s *Store) IsBusyError(err error) bool {
	return s.dialect.IsBusyError(err)
}

// SchemaStale checks whether the database schema is missing columns
// added by recent migrations. Returns (stale, column, err). Only
// reports stale when the query succeeds and the column is absent;
// query errors are returned separately so callers don't misdiagnose
// corruption or permission problems as outdated schema.
func (s *Store) SchemaStale() (bool, string, error) {
	var count int
	err := s.db.QueryRow(s.dialect.SchemaStaleCheck()).Scan(&count)
	if err != nil {
		return false, "", fmt.Errorf("check schema version: %w", err)
	}
	if count == 0 {
		return true, "messages.embed_gen", nil
	}
	return false, "", nil
}

// initSchemaWindowHook is a test-only seam. It fires at the point in InitSchema
// where a message inserted by another connection is at its most exposed: the
// one-time content_changed_at backfill has already run AND recorded itself in
// the migration ledger, so it will never look for NULL watermarks again, while
// the remaining whole-table index builds still have minutes of work to do on a
// large archive. A row that lands here has to be stamped by the INSERT trigger
// or it never appears in the change feed at all, which is why the watermark
// triggers are created before the backfill rather than after the indexes. Nil
// in production, and per-Store: see the field's declaration.

// InitSchema initializes the database schema, uninterruptibly.
//
// Kept for callers that have no context to offer — short-lived tooling and test
// fixtures. Anything with a cancellable context, and in particular anything that
// opens an archive on behalf of a signal-handling process, must use
// InitSchemaContext: on an existing archive this runs a one-time full-table
// backfill that can take hours, and a background context makes SIGINT and
// SIGTERM unable to reach it.
func (s *Store) InitSchema() error {
	return s.InitSchemaContext(context.Background())
}

// InitSchemaContext initializes the database schema, honouring ctx.
// This creates all tables if they don't exist.
//
// EVERY statement this method issues carries ctx — with no exceptions, which is
// the property to preserve rather than a summary of the current call list.
// Directly or through the transaction it runs in, that covers the schema
// scripts, the legacy ADD COLUMN migrations, every migration-ledger read and
// write (runOnceMigration owns them), the maintenance transactions and the
// dialect DDL they carry, the legacy phone-unique migration including the
// participant merge and its link-graph rewrite, the attribution backfill
// including its chunked calendar UPDATE, the FTS schema, the FTS index build,
// the FTS availability probe, the archive-UID transaction, and the default
// collection. Every operation must remain reachable by cancellation while
// waiting for archive locks or processing a large migration.
//
// The closure is mechanically checkable and was established that way rather
// than by reading: walk every call this method makes, transitively, and look
// for one that reaches loggedDB/loggedTx Exec, Query, QueryRow or Begin, each
// of which substitutes context.Background(). Anything a helper hands to a
// dialect must be a boundQuerier, not the raw transaction. Three earlier rounds
// each bound part of this method and each left this comment claiming the whole
// of it; if you add a step, run that walk again before you touch this
// paragraph.
//
// Cancelling ctx stops the one-time content_changed_at backfill at the batch
// boundary it reaches. Every batch already committed is kept, the migration
// ledger is NOT marked, and the next open resumes where this one stopped — so an
// interrupted upgrade costs the batch in flight and nothing else. The same holds
// for the other ledger-gated migrations: a cancelled one is not marked applied,
// so the next open runs it again.
func (s *Store) InitSchemaContext(ctx context.Context) error {
	// Load and execute schema files provided by the dialect.
	for _, filename := range s.dialect.SchemaFiles() {
		schema, err := schemaFS.ReadFile(filename)
		if err != nil {
			return fmt.Errorf("read %s: %w", filename, err)
		}
		// Keep schema scripts cancellable while waiting for the SQLite writer slot.
		if _, err := s.db.ExecContext(ctx, string(schema)); err != nil {
			return fmt.Errorf("execute %s: %w", filename, err)
		}
	}
	{
		if err := ensureRecipientCacheJournalTriggers(boundQuerier{ctx: ctx, q: s.db}); err != nil {
			return err
		}
	}
	if err := s.ensureMigrationLedgerVersionColumn(ctx); err != nil {
		return fmt.Errorf("ensure migration ledger version column: %w", err)
	}
	if err := s.runOnceMigration(
		ctx, migrationPersonInferenceProviderV2, 1, false,
		s.migratePersonInferenceProviderV2,
	); err != nil {
		return fmt.Errorf("migrate people inference provider profiles: %w", err)
	}
	if err := s.runOnceMigration(
		ctx, migrationPersonSweepCallsV2, 1, false,
		s.migratePersonSweepCallsV2,
	); err != nil {
		return fmt.Errorf("migrate person sweep call journal: %w", err)
	}
	if err := s.runOnceMigration(
		ctx, migrationPersonSweepBatchPurposeV2, 1, false,
		s.migratePersonSweepBatchPurposeV2,
	); err != nil {
		return fmt.Errorf("migrate person sweep call journal purposes: %w", err)
	}
	if err := s.runOnceMigration(
		ctx, migrationPersonFactClaimOriginBrief, 1, false,
		s.migratePersonFactClaimOriginBrief,
	); err != nil {
		return fmt.Errorf("migrate person fact claim origins: %w", err)
	}
	if err := s.runOnceMigration(
		ctx, migrationPersonSweepAttemptBriefFailure, 1, false,
		s.migratePersonSweepAttemptBriefFailure,
	); err != nil {
		return fmt.Errorf("migrate person sweep attempt brief failure class: %w", err)
	}
	if err := s.runOnceMigration(
		ctx, migrationPersonEnrichmentIdentityUncertain, 1, false,
		s.migratePersonEnrichmentIdentityUncertain,
	); err != nil {
		return fmt.Errorf("migrate person enrichment attempt states: %w", err)
	}
	if err := s.ensureDirectoryProjectionInfrastructure(ctx); err != nil {
		return err
	}
	// The Directory projection is derived from the person tables, so an
	// archive that predates it gets every person marked dirty and refreshed
	// once. Later opens find the ledger entry and skip the backfill; triggers
	// keep the projection current from then on.
	if err := s.runOnceMigration(
		ctx, migrationDirectoryProjectionV1, 1, false,
		func(ctx context.Context) error {
			return s.backfillDirectoryProjectionContext(ctx)
		},
	); err != nil {
		return err
	}
	// Legacy databases may hold duplicate (message_id, content_hash)
	// attachment rows from the old SELECT-then-INSERT UpsertAttachment.
	// Dedupe before creating the partial unique index that enforces
	// idempotency going forward. Gated on the applied_migrations ledger:
	// the dedupe's GROUP BY over the full attachments table is not free on
	// a large archive, and it never finds work after the first run.
	//
	// Deduplication and index creation share a maintenance transaction so
	// the index is built against the rows just deduplicated.
	if err := s.runOnceMigration(
		ctx, migrationAttachmentsContentHashUnique, 1, false,
		func(ctx context.Context) error {
			return s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
				if err := s.dedupeAttachmentsBeforeUniqueIndex(ctx, tx); err != nil {
					return fmt.Errorf("dedupe attachments: %w", err)
				}
				if _, err := tx.ExecContext(ctx, `
					CREATE UNIQUE INDEX IF NOT EXISTS idx_attachments_msg_content_hash
					    ON attachments(message_id, content_hash)
					    WHERE content_hash IS NOT NULL AND content_hash != ''
				`); err != nil {
					return fmt.Errorf("create idx_attachments_msg_content_hash: %w", err)
				}
				return nil
			})
		},
	); err != nil {
		return err
	}

	// The source-support tables were added after generated identity candidates
	// and evidence. Preserve old rows before source-removal cleanup starts using
	// support absence as proof that a row is stale.
	if err := s.ensureIdentityMatchCandidateSourceSupportColumns(ctx); err != nil {
		return fmt.Errorf("prepare identity match source support provenance: %w", err)
	}
	if err := s.runOnceMigration(
		ctx, migrationIdentityMatchSourceSupport, 1, false,
		func(ctx context.Context) error {
			return s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
				return s.backfillLegacyIdentityMatchSourceSupport(ctx, tx)
			})
		},
	); err != nil {
		return err
	}

	// Legacy databases may have idx_participants_phone as a non-unique
	// partial index (it was created that way before the schema flipped
	// to UNIQUE). `CREATE UNIQUE INDEX IF NOT EXISTS` in schema.sql
	// silently leaves the non-unique index in place, so
	// EnsureParticipantByPhone's ON CONFLICT (phone_number) finds no
	// matching unique constraint on upgraded DBs. Run a one-shot
	// migration that dedupes phone rows, drops the index, and
	// recreates it as UNIQUE.
	if err := s.ensureParticipantsPhoneUniqueIndex(ctx); err != nil {
		return fmt.Errorf("ensure idx_participants_phone unique: %w", err)
	}

	// Seed the open communication-service catalog. The catalog is
	// presentation and normalization metadata, not an enum: unknown bridges
	// are registered as rows at runtime. The migration ledger prevents
	// startup from fighting later user edits or deletions.
	if err := s.seedCommunicationServices(ctx); err != nil {
		return fmt.Errorf("seed communication services: %w", err)
	}

	// Add legacy columns before their dependent migrations. A successful
	// last_modified ALTER forces its backfill even if a ledger sentinel exists,
	// because newly added values are NULL. Existing columns report duplicate
	// column errors. Context cancellation bounds schema-lock waits.
	lastModifiedColumnAdded := false
	for _, m := range s.dialect.LegacyColumnMigrations() {
		if _, err := s.db.ExecContext(ctx, m.SQL); err != nil {
			if !s.dialect.IsDuplicateColumnError(err) {
				return fmt.Errorf("migrate schema (%s): %w", m.Desc, err)
			}
		} else if m.Desc == "last_modified" {
			lastModifiedColumnAdded = true
		}
	}
	if err := s.ensureCacheSourceAttribution(ctx); err != nil {
		return err
	}
	// Older runs predate typed checkpoints. Restore types only when the source
	// or pinned Gmail handoff cursor identifies them unambiguously, then tag
	// unfinished Gmail recovery runs for the strict resume matcher.
	if err := s.runOnceMigration(
		ctx, migrationSyncRunResumeMetadata, 1, false,
		func(ctx context.Context) error {
			return s.withTxContext(ctx, func(tx *loggedTx) error {
				return s.backfillSyncRunResumeMetadata(ctx, tx)
			})
		},
	); err != nil {
		return fmt.Errorf("backfill sync run resume metadata: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_sync_runs_operation
		ON sync_runs(operation_id, id)
		WHERE operation_id IS NOT NULL
	`); err != nil {
		return fmt.Errorf("create sync operation index: %w", err)
	}
	if err := s.ensureCardDAVConflictPendingInvariant(ctx); err != nil {
		return fmt.Errorf("migrate CardDAV conflict pending state: %w", err)
	}
	if err := s.ensureVCardSourceResourceIdentityIndexes(ctx); err != nil {
		return fmt.Errorf("scope vCard identities to source resources: %w", err)
	}
	if err := s.runOnceMigration(ctx, migrationPersonDisplayNameChangedAt, 1, false,
		func(ctx context.Context) error {
			if _, err := s.db.ExecContext(ctx, `UPDATE persons
				SET display_name_changed_at = created_at
				WHERE display_name_changed_at IS NULL`); err != nil {
				return fmt.Errorf("backfill person display-name change times: %w", err)
			}
			return nil
		}); err != nil {
		return err
	}
	if err := s.runOnceMigration(ctx, migrationMeetingWeightTwoPersonSocial, 1, false,
		func(ctx context.Context) error {
			return s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
				return s.bumpMeetingWeightRevisionTx(ctx, tx)
			})
		}); err != nil {
		return err
	}
	if err := s.runOnceMigration(ctx, migrationStripLabelEmoji, 1, false,
		s.stripStoredLabelEmoji); err != nil {
		return err
	}
	// Organization domains written before IDNA normalization may still contain
	// Unicode. Canonicalize them before fact resolution compares incoming ASCII
	// references with persisted roots and identifiers.
	if err := s.runOnceMigration(
		ctx, migrationOrganizationDomainIDNA, 1, false,
		func(ctx context.Context) error {
			return s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
				return s.canonicalizeLegacyOrganizationDomains(ctx, tx)
			})
		},
	); err != nil {
		return fmt.Errorf("canonicalize organization domains: %w", err)
	}
	// Recovery reads only acceptances whose link transaction may not have
	// committed. Create this after the legacy-column loop: schema.sql is
	// executed before that loop, so an upgraded table does not have the column
	// yet when its CREATE TABLE IF NOT EXISTS is skipped.
	if _, err := s.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_identity_match_candidates_application_pending
		ON identity_match_candidates(application_pending, id)
		WHERE state = 'accepted'
	`); err != nil {
		return fmt.Errorf("index pending identity match applications: %w", err)
	}
	if err := s.ensureParticipantIdentifierServiceScopeIndex(ctx); err != nil {
		return fmt.Errorf("create participant identifier service-scope index: %w", err)
	}

	// This one-shot backfill must run after LegacyColumnMigrations so upgraded
	// databases have the nullable service/scope columns before they are read.
	if err := s.ensureParticipantIdentifierServiceScope(ctx); err != nil {
		return fmt.Errorf("classify participant identifier service scope: %w", err)
	}

	// Stable source-part identity supersedes content hash for attachment rows
	// that have it. The legacy hash index remains only for rows whose source
	// cannot provide an occurrence key. This runs after the column migrations
	// because upgraded archives do not have source_part_key before this point.
	if err := s.ensureAttachmentOccurrenceUniqueIndexes(ctx); err != nil {
		return err
	}

	// Relax message_recipients uniqueness to include the normalized envelope
	// address before installing triggers that read the table. SQLite rebuilds a
	// legacy table-level UNIQUE away; installing the triggers first lets SQLite
	// rewrite their definitions during that swap and leaves them pointing at a
	// temporary table after it is dropped. The migration itself restores any
	// pre-existing sweep definitions transactionally, while this ordering keeps
	// fresh and normally upgraded archives on the simple repair-before-install
	// path.
	if err := s.ensureRecipientEnvelopeUniqueIndex(ctx); err != nil {
		return fmt.Errorf("ensure idx_message_recipients_envelope unique: %w", err)
	}

	// Create the message watermark, contextual embedding journal, and attachment
	// change journal triggers. This must run after the migration loop above,
	// which adds the legacy columns referenced by those triggers.
	//
	// It runs HERE, immediately after the columns exist, rather than at the end
	// of the upgrade: everything below is index builds and whole-table backfills
	// that take minutes on a large archive, and until the INSERT trigger exists
	// a message written by another connection lands with content_changed_at
	// NULL. That is terminal — the feed's range predicate excludes NULL and the
	// backfill's ledger sentinel means it never runs again — so the row would
	// never appear in the change feed. Ordering the triggers first makes the
	// INSERT trigger the writer for those rows and leaves the backfill's
	// `WHERE content_changed_at IS NULL` correctly finding nothing to do for
	// them.
	//
	// Safe with respect to the backfills below. Neither is re-stamped by the
	// content_changed_at triggers: those are scoped to INSERT or to
	// `UPDATE OF <content columns>`, and content_changed_at is not one of those
	// columns, so a statement naming only the watermark never matches. The
	// last_modified backfill writes a value that differs from the old NULL,
	// which is what the last_modified triggers yield to. The reverse interaction
	// is avoided because UPDATE OF excludes content_changed_at: stamping only
	// that watermark leaves last_modified unchanged.
	//
	// On SQLite this covers the content_changed_at triggers plus the messages
	// last_modified trigger, which needs an UPDATE OF scope built from the live
	// column list (see lastModifiedUpdateOfColumns) and so cannot be static SQL;
	// only the message_bodies last_modified pair still rides schema.sql.
	// The versioned migration drops and recreates each definition once.
	// Bump its name when the definition or tracked column list changes.
	//
	// Bind the transaction to ctx so trigger DDL honors cancellation.
	if err := s.dialect.ValidateMessageWatermarks(boundQuerier{ctx: ctx, q: s.db}); err != nil {
		return fmt.Errorf("validate message watermarks: %w", err)
	}
	watermarkTriggersAlreadyApplied, err := s.IsMigrationAppliedContext(
		ctx, migrationMessageWatermarkTriggers, 1)
	if err != nil {
		return fmt.Errorf("check message watermark trigger migration: %w", err)
	}
	if err := s.runOnceMigration(
		ctx, migrationMessageWatermarkTriggers, 1, false,
		func(ctx context.Context) error {
			return s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
				return s.dialect.EnsureTriggers(boundQuerier{ctx: ctx, q: tx})
			})
		},
	); err != nil {
		return fmt.Errorf("ensure message watermark triggers: %w", err)
	}
	if err := s.runOnceMigration(
		ctx, migrationEmbeddingChangeJournalTriggers, 1, false,
		func(ctx context.Context) error {
			// A fresh archive (or a pre-watermark archive) just ran the current
			// EnsureTriggers above, which already includes the journal definitions.
			// Only an archive that entered this InitSchema with watermark v1 applied
			// needs a second pass to upgrade its existing trigger set.
			if !watermarkTriggersAlreadyApplied {
				return nil
			}
			return s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
				return s.dialect.EnsureTriggers(boundQuerier{ctx: ctx, q: tx})
			})
		},
	); err != nil {
		return fmt.Errorf("ensure embedding change journal triggers: %w", err)
	}
	if err := s.runOnceMigration(
		ctx, migrationPersonSweepChangeTriggers, 1, false,
		func(ctx context.Context) error {
			// Fresh archives installed the current definitions with the watermark
			// triggers above. Existing archives and explicit repair runs need a
			// second pass because EnsureTriggers owns the shared mutation tables.
			if !watermarkTriggersAlreadyApplied {
				return nil
			}
			return s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
				return s.dialect.EnsureTriggers(boundQuerier{ctx: ctx, q: tx})
			})
		},
	); err != nil {
		return fmt.Errorf("ensure person sweep change triggers: %w", err)
	}
	if err := s.runOnceMigration(
		ctx, migrationActivityProjectionTriggers, 1, false,
		func(ctx context.Context) error {
			return s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
				return s.dialect.EnsureActivityProjectionTriggers(
					boundQuerier{ctx: ctx, q: tx},
				)
			})
		},
	); err != nil {
		return fmt.Errorf("ensure activity projection triggers: %w", err)
	}

	// Gmail exposes archived chat transcripts as MIME messages carrying its
	// system CHAT label. Older syncs stored every Gmail payload as email, which
	// made the archive present these transcripts as separate email items. Run
	// after the message and activity triggers exist so reclassified rows
	// invalidate analytical and relationship projections like a normal update.
	if err := s.runOnceMigration(
		ctx, migrationGmailChatClassification, 1, false,
		func(ctx context.Context) error {
			return s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
				return s.classifyLegacyGmailChats(ctx, tx)
			})
		},
	); err != nil {
		return err
	}

	// Initialize explicit attribution provenance for every legacy message once
	// under the maintenance timeout escape hatch. Granola and Circleback
	// historically derived is_from_me from confirmed organizer identities.
	// Google Calendar combined that signal with Organizer.Self, so its archived
	// event payload separates source-native ownership from identity-derived
	// ownership. Other providers' existing values are source-native. Runtime
	// identity mutations can then update only rows whose derived attribution
	// actually changes instead of rewriting an entire source to initialize NULL
	// provenance.
	if err := s.runOnceMigration(
		ctx, migrationMessageAttributionProvenance, 1, false,
		func(ctx context.Context) error {
			return s.runMaintenance(
				ctx,
				func(ctx context.Context, tx *loggedTx) error {
					if err := backfillLegacyMessageAttributionProvenance(ctx, tx); err != nil {
						return err
					}

					// Published message shards used to trust the effective
					// is_from_me value. The provenance migration changes cache
					// inputs even when no identity is added or removed, so advance
					// the account-identity revision in the same transaction as the
					// repaired rows. Empty archives have no stale shards to
					// invalidate.
					var hasMessages bool
					if err := tx.QueryRowContext(
						ctx,
						`SELECT EXISTS (SELECT 1 FROM messages)`,
					).Scan(&hasMessages); err != nil {
						return fmt.Errorf("check attribution migration cache impact: %w", err)
					}
					if !hasMessages {
						return nil
					}
					return s.bumpAccountIdentityRevisionContext(ctx, tx)
				},
			)
		},
	); err != nil {
		return err
	}

	// Runs after the legacy-column loop (upgraded archives need the
	// address_key column before it is read, backfilled, and uniquely
	// indexed) and after the attribution-provenance migration above: a
	// duplicate collapse refreshes message attribution, and that refresh
	// folds identity matches into is_from_me. On a pre-provenance archive
	// the backfill would then read those identity-derived values as
	// source-native, permanently mislabeling ownership provenance.
	if err := s.ensureAccountIdentityAddressKeys(ctx); err != nil {
		return fmt.Errorf("ensure account identity address keys: %w", err)
	}

	// Identity discovery scans one source in message-ID order. The source index
	// already orders ties by rowid; no additional composite index is needed.
	// Fresh schemas build the canonical RFC822 index while messages is empty,
	// and existing archives create missing indexes below.
	if s.beforeLargeIndexBuildHook != nil {
		s.beforeLargeIndexBuildHook()
	}

	{
		if err := s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
			if _, err := tx.ExecContext(ctx, `
				CREATE INDEX IF NOT EXISTS idx_messages_live_sent_at
				    ON messages(COALESCE(sent_at, received_at, internal_date) DESC, id DESC)
				    WHERE deleted_at IS NULL AND deleted_from_source_at IS NULL
			`); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				CREATE INDEX IF NOT EXISTS idx_messages_sent_at_julianday
				    ON messages(julianday(sent_at))
			`); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `
				CREATE INDEX IF NOT EXISTS idx_messages_live_timestamp_julianday
				    ON messages(julianday(COALESCE(sent_at, received_at, internal_date)))
				    WHERE deleted_at IS NULL AND deleted_from_source_at IS NULL
			`)
			return err
		}); err != nil {
			return fmt.Errorf("create live message timestamp indexes: %w", err)
		}

		// Partial indexes over the deletion timestamps. The analytics cache
		// staleness check (cacheNeedsBuild) counts messages source-deleted or
		// dedup-hidden since the last build on every daemon start, before the
		// API server binds. Neither predicate is served by an existing index
		// (idx_messages_deleted leads with source_id), so each COUNT was a
		// full scan of the messages table — measured at ~4.5s on a cold page
		// cache over a 2.5M-row archive, which was the entire cold-start
		// latency of `msgvault search`. The partial form keeps the indexes
		// proportional to the deleted rows only, so live-message insert and
		// update paths pay no maintenance for them.
		if err := s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
			if _, err := tx.ExecContext(ctx, `
				CREATE INDEX IF NOT EXISTS idx_messages_deleted_from_source_at
				    ON messages(deleted_from_source_at)
				    WHERE deleted_from_source_at IS NOT NULL
			`); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `
				CREATE INDEX IF NOT EXISTS idx_messages_deleted_at
				    ON messages(deleted_at)
				    WHERE deleted_at IS NOT NULL
			`)
			return err
		}); err != nil {
			return fmt.Errorf("create deletion timestamp indexes: %w", err)
		}
	}

	// Indexes over rfc822_message_id serve dedup. The plain index serves the
	// per-group message lookup (GetDuplicateGroupMessages /
	// GetDuplicateGroupMessagesBatch). Without it, each lookup was a full
	// scan of the messages table — measured at ~190ms/lookup, with one
	// lookup per duplicate group, so a scan with 22k groups burned the
	// entire 30-minute CLI plan-request timeout before content-hash
	// comparison even started (kenn-io/msgvault#510). Plain (non-partial)
	// index: a partial WHERE rfc822_message_id IS NOT NULL AND != '' form is
	// not usable by the planner for this table's bound `= ?` / `IN (...)`
	// lookups — SQLite can't prove col = ? implies col != '' since ? could
	// bind to '' — so it would silently fall back to SCAN (verified via
	// EXPLAIN QUERY PLAN before writing this).
	//
	// The composite expression/source index serves duplicate discovery
	// (FindDuplicatesByRFC822ID), whose GROUP BY runs the canonical
	// (bracket-unwrapped) Message-ID expression — a computed CASE/SUBSTR the
	// plain index cannot serve. Canonical ID leads the index so grouping can
	// stream in index order; source_id lets Engine.Scan's mandatory source scope
	// be evaluated from the same index. The indexed expression is generated by
	// the same dialect method as the query, keeping them byte-identical so the
	// planner matches them; dedup_index_test.go pins scoped query plans.
	// IF NOT EXISTS keeps creation safe to repeat.
	if err := s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
		if _, err := tx.ExecContext(ctx, `
			CREATE INDEX IF NOT EXISTS idx_messages_rfc822_message_id
			    ON messages(rfc822_message_id)
		`); err != nil {
			return err
		}

		_, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS `+
			rfc822CanonicalIndexName+` `+s.dialect.RFC822CanonicalIDIndexDefinition())
		return err
	}); err != nil {
		return fmt.Errorf("create rfc822 message id index: %w", err)
	}

	// Backfill last_modified for rows that predate the column. SQLite cannot
	// ADD COLUMN with a non-constant default, so the legacy ADD COLUMN above
	// leaves existing rows NULL; this one-shot UPDATE sets them to
	// CURRENT_TIMESTAMP so the embed worker's CAS token is a comparable value
	// (a NULL token would never satisfy `last_modified = ?` and the row would
	// loop "needs embedding" forever). Idempotent and portable: on a fresh
	// DB no rows are NULL, so this is a no-op. Gated
	// on the applied_migrations ledger: last_modified has no index, so the
	// UPDATE's WHERE clause is a full scan of the messages table — the
	// dominant cost of daemon startup on a large archive — and it never
	// finds work after the first run. The update uses a context-bound
	// maintenance transaction.
	if err := s.runOnceMigration(
		ctx, migrationMessagesLastModifiedBackfill, 1, lastModifiedColumnAdded,
		func(ctx context.Context) error {
			if err := s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
				_, err := tx.ExecContext(ctx,
					`UPDATE messages SET last_modified = `+s.dialect.Now()+
						` WHERE last_modified IS NULL`)
				return err
			}); err != nil {
				return fmt.Errorf("backfill last_modified: %w", err)
			}
			return nil
		},
	); err != nil {
		return err
	}

	// Backfill content_changed_at for rows that predate the column, in
	// committed batches (backfillContentChangedAt). Gated on the ledger
	// because the scan never finds work after the first completed run; no
	// forced-rerun path is needed (unlike last_modified's) because the column
	// and its sentinel ship in the same release. The ledger is marked only
	// after every batch has committed, so an interrupted upgrade re-enters the
	// loop on the next open rather than declaring itself done.
	if err := s.runOnceMigration(
		ctx, migrationMessagesContentChangedAtBackfill, 1, false,
		func(ctx context.Context) error {
			if err := s.backfillContentChangedAt(ctx); err != nil {
				return fmt.Errorf("backfill content_changed_at: %w", err)
			}
			return nil
		},
	); err != nil {
		return err
	}

	if s.initSchemaWindowHook != nil {
		s.initSchemaWindowHook()
	}

	// The feed cursor uses (content_changed_at, id), so its keyset index
	// has the same order. Create it after the legacy ADD COLUMN migration.
	if err := s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
		_, err := tx.ExecContext(ctx,
			`CREATE INDEX IF NOT EXISTS idx_messages_content_changed_at
			     ON messages(content_changed_at, id)`)
		return err
	}); err != nil {
		return fmt.Errorf("create content_changed_at index: %w", err)
	}

	// Drop the obsolete partial index over messages needing embedding. It was
	// redundant with the per-generation embed watermark (the work-finder scan
	// rides the messages PRIMARY KEY B-tree via `id > :watermark ORDER BY id`)
	// and useless during a rebuild (old-gen leftovers carry a non-NULL embed_gen
	// that an `embed_gen IS NULL` index never covers), while costing index
	// maintenance on the two hottest write paths (message insert + embed_gen
	// stamp). DROP IF EXISTS is idempotent; it
	// cleans up any dev DB that already created the index. Run under
	// runMaintenance to match the original CREATE's transaction context.
	if err := s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
		_, err := tx.ExecContext(ctx,
			`DROP INDEX IF EXISTS idx_messages_embed_gen`)
		return err
	}); err != nil {
		return fmt.Errorf("drop idx_messages_embed_gen: %w", err)
	}

	// Load the optional FTS schema, if the dialect keeps one separate.
	if ftsFile := s.dialect.SchemaFTS(); ftsFile != "" {
		ftsSchema, err := schemaFS.ReadFile(ftsFile)
		if err != nil {
			return fmt.Errorf("read %s: %w", ftsFile, err)
		}
		// Bound to ctx like the rest of the DDL above. A cancellation here is
		// not a missing module: IsNoSuchModuleError does not match it, so it is
		// reported rather than swallowed by the fall-through below.
		if _, err := s.db.ExecContext(ctx, string(ftsSchema)); err != nil {
			if !s.dialect.IsNoSuchModuleError(err) {
				return fmt.Errorf("init FTS schema: %w", err)
			}
			// Module not compiled in; availability stays false. Fall
			// through so the rest of schema init still runs.
		}
	}
	if err := s.ensureArchiveUIDContext(ctx); err != nil {
		return err
	}
	if err := s.runOnceMigration(ctx, migrationMeetingProjectionV1, meetingProjectionVersion, false, s.backfillMeetingProjectionsContext); err != nil {
		return fmt.Errorf("initialize meeting projections: %w", err)
	}

	// Probe FTS availability with ctx. The probe returns a bool, so a cancellation that
	// was not reported as an error would be recorded as "search is unavailable"
	// on a store the daemon is about to hand to callers.
	available, err := s.dialect.FTSAvailable(ctx, s.db.DB)
	if err != nil {
		return fmt.Errorf("probe FTS availability: %w", err)
	}
	s.fts5Available = available

	if err := s.EnsureSeededAttributeDefinitionsContext(ctx); err != nil {
		return fmt.Errorf("ensure seeded attribute definitions: %w", err)
	}

	// The CardDAV inference export backfill projects every person's vCard
	// attributes, so it must run after LegacyColumnMigrations has added the
	// attribute_definitions columns it selects and after seeding has installed
	// the vCard mappings that decide which inferred facts are exportable.
	if err := s.runOnceMigration(
		ctx, migrationCardDAVInferenceExportState, 1, false,
		s.backfillCardDAVInferenceExportState,
	); err != nil {
		return fmt.Errorf("backfill CardDAV inference export state: %w", err)
	}

	// Reconcile the system relationship type catalog on every open: insert
	// missing seeds, repair structural drift, and leave user-owned labels,
	// vCard mappings, colours, icons, and descriptions alone. See
	// EnsureSeededRelationshipTypes for the two column classes.
	if err := s.EnsureSeededRelationshipTypesContext(ctx); err != nil {
		return err
	}

	// Ensure the default "All" collection exists and contains every source.
	if err := s.EnsureDefaultCollectionContext(ctx); err != nil {
		return fmt.Errorf("ensure default collection: %w", err)
	}

	// Schema initialization can add indexes to an existing archive. Refresh
	// statistics after all schema work so SQLite can cost those indexes against
	// the archive's actual data distribution.
	s.optimizeSQLiteBestEffort(ctx, "schema initialization")

	return nil
}

// runOnceMigration runs fn when the applied_migrations ledger has no entry for
// name or records a version below the requested minimum (or when force
// overrides that), and writes the entry only after fn returns successfully, so
// a migration that failed or was cancelled runs again on the next open.
//
// It is the single owner of the ledger statements for every one-time step in
// InitSchemaContext, and both of them carry ctx. That is not incidental. The
// read is a gate: answered on a cancelled context it lets the whole migration
// below it run to completion after the operator asked the daemon to stop. The
// write is worse: reached on a cancelled context it stamps "done" on a
// migration that was cut off, and every later open skips it. Keeping both in
// one place is what stops the next migration added to InitSchemaContext from
// reintroducing a contextless pair.
func (s *Store) runOnceMigration(
	ctx context.Context, name string, version int, force bool, fn func(ctx context.Context) error,
) error {
	applied, err := s.IsMigrationAppliedContext(ctx, name, version)
	if err != nil {
		return err
	}
	if applied && !force {
		return nil
	}
	if err := fn(ctx); err != nil {
		return err
	}
	return s.markMigrationAppliedContext(ctx, s.db, name, version)
}

// contentChangedBackfillBatchSize is how many ROWS one backfill batch stamps —
// rows needing the stamp, not ids walked past. Matching the FTS batch size
// amortizes transaction setup while keeping each writer-lock hold bounded.
// Tests override this per Store rather than mutating the package constant.
const contentChangedBackfillBatchSize int64 = 5000

// contentChangedBackfillBatch is the batch size this Store's backfill uses:
// the production default unless a test has overridden it for this Store.
func (s *Store) contentChangedBackfillBatch() int64 {
	if s.contentChangedBackfillBatchSizeOverride > 0 {
		return s.contentChangedBackfillBatchSizeOverride
	}
	return contentChangedBackfillBatchSize
}

// contentChangedBackfillBatchHook is a test-only seam: when non-nil it is
// consulted before each batch with the batch's first and last id (inclusive),
// and a non-nil return aborts the backfill at that batch boundary. It lets a
// test interrupt an upgrade exactly between two committed batches — the case
// that decides whether the backfill is resumable — without racing a real crash,
// and it lets a test count the transactions an archive's shape costs. Nil (and
// thus a no-op) in production; only export_test.go ever sets it, and it is
// per-Store: see the field's declaration.

// backfillContentChangedAt seeds content_changed_at on rows that predate the
// column, walking the rows that still need it in committed batches.
//
// Seeded from last_modified rather than "now": an existing archive's
// last_modified is a reasonable estimate of when the row last moved, so each
// backfilled row reports a change time close to its real one. It does NOT
// shorten the first walk — an empty cursor selects every non-NULL watermark, so
// a first-run consumer still receives the whole archive, oldest change first,
// which is what an empty cursor means. What "now" would cost instead is the
// meaning: every pre-existing row would report the upgrade instant as its
// change time, one undifferentiated block, and a consumer resuming mid-walk
// would be paging by id through a single instant rather than through history.
// SQLite runs the seed through strftime so a legacy last_modified in any other
// textual form lands in the one format the triggers write -- its cursor
// comparison is lexical, and a single stray "2024-03-04T05:06:07Z" would sort
// into the wrong place and be skipped or repeated forever.
//
// Batched rather than one whole-table UPDATE for two
// reasons that matter on a large archive at daemon startup:
//
//   - Resumability. Each batch is its own committed transaction, so a restart
//     mid-upgrade keeps every row already stamped and the next run does the
//     remainder. A single transaction over the whole table would roll all of it
//     back, so an archive big enough to take longer than the operator's
//     patience could never finish.
//   - Bounded cost per transaction. A whole-table UPDATE holds the SQLite
//     writer lock for its full duration, blocking every other writer
//     in the process, and grows the WAL by the size of the change before the
//     commit lets a checkpoint truncate it.
//
// The walk is a KEYSET walk over the rows that still need work — the next batch
// is "the first N rows with `content_changed_at IS NULL` above the last id
// stamped", ordered by id — rather than a march through the numeric span from
// MIN(id) to MAX(id). It carries the last id seen, so it never adds anything to
// an id and cannot overflow int64 at the top of the id space (where a computed
// batch end wraps negative, matches nothing, and wraps the cursor instead of
// terminating), it makes no assumption about which ids are legal (0 and negative
// ones both are: SQLite's INTEGER PRIMARY KEY is the rowid and the schema
// constrains it no further), and it costs one transaction per batch of ROWS
// rather than one per batch of ids — so a sparse archive, which is what a
// long-lived one looks like after deletions and subset copies, does not pay
// millions of empty transactions at daemon startup before the port is bound.
//
// Re-entry is correct because the predicate is `content_changed_at IS NULL`:
// an already-stamped row is selected by no batch, so a resumed run rewrites
// nothing it already did, and a row stamped by the INSERT trigger or column
// DEFAULT while the backfill is running is left alone. It is not free: the walk
// keeps its cursor in memory only, so a resumed run reads once past the stamped
// prefix before it reaches the remaining work (see the batch loop).
//
// SQLite does not bump: its last_modified trigger is scoped
// `UPDATE OF <every column except content_changed_at>`, which it must be so the
// content_changed_at stamp cannot clobber an explicit last_modified write (see
// lastModifiedUpdateOfColumns). This statement names only the watermark, so it
// no longer matches.
func (s *Store) backfillContentChangedAt(ctx context.Context) error {
	// The batch is chosen by reading ids first, then stamping exactly the range
	// they span. Selecting the ids is what makes the walk a keyset walk; the
	// UPDATE can then use a plain BETWEEN because the selected ids are, by
	// construction, every unstamped row in [first,last] -- they are a
	// contiguous run of the unstamped set in id order -- so a bounded range
	// beats an N-element IN list and matches nothing extra.
	//
	// The first batch has no lower bound at all rather than starting from some
	// smallest-imaginable id: an id of 0, or a negative one, is legal, and
	// there is no sentinel below math.MinInt64 to start a `>` comparison from.
	firstBatchSQL := `SELECT id FROM messages WHERE content_changed_at IS NULL ORDER BY id LIMIT ?`
	nextBatchSQL := `SELECT id FROM messages WHERE content_changed_at IS NULL AND id > ? ORDER BY id LIMIT ?`

	// The outer COALESCE is load-bearing, not belt-and-braces. strftime
	// returns NULL -- not an error -- for any input its parser rejects: a unix
	// epoch integer, an empty string, free text. last_modified is an untyped
	// SQLite DATETIME column, so such a value is storable, and the resulting
	// NULL watermark is terminal: the feed's range predicate excludes NULL and
	// the caller's ledger gate means this scan never runs again. Falling back
	// to "now" keeps the row in the feed at the cost of one over-fresh
	// watermark.
	stampSQL := `UPDATE messages
	             SET content_changed_at = COALESCE(
	                     strftime('%Y-%m-%d %H:%M:%f', last_modified),
	                     strftime('%Y-%m-%d %H:%M:%f', 'now'))
	             WHERE content_changed_at IS NULL AND id >= ? AND id <= ?`

	batchSize := s.contentChangedBackfillBatch()
	started := time.Now()
	lastLog := started
	var stamped int64
	var cursor int64
	for batch := 0; ; batch++ {
		// Select and stamp each batch in the same transaction. A resumed run
		// scans past already stamped rows because the cursor is in memory only.
		// The content_changed_at index is installed after this backfill.
		var firstID, lastID, count, affected int64
		if err := s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
			var err error
			firstID, lastID, count, err = nextContentChangedBackfillBatch(
				ctx, tx, firstBatchSQL, nextBatchSQL, batch == 0, cursor, batchSize)
			if err != nil {
				return err
			}
			if count == 0 {
				return nil
			}
			if s.contentChangedBackfillBatchHook != nil {
				// Before the stamp, so a test that aborts here aborts a batch
				// that has written nothing -- the transaction rolls back and
				// the committed batches before it are what an interrupted
				// upgrade leaves behind.
				if err := s.contentChangedBackfillBatchHook(firstID, lastID); err != nil {
					return err
				}
			}
			result, err := tx.ExecContext(ctx, stampSQL, firstID, lastID)
			if err != nil {
				return err
			}
			affected, err = result.RowsAffected()
			return err
		}); err != nil {
			return fmt.Errorf("batch above id %d: %w", cursor, err)
		}
		if count == 0 {
			break
		}
		stamped += affected
		cursor = lastID

		// Progress on the terms an operator watching a stalled startup needs:
		// how much has been stamped, and where the walk has reached. Time-
		// throttled rather than per-batch so a small archive logs nothing.
		if time.Since(lastLog) >= 30*time.Second {
			slog.Info("backfilling content_changed_at",
				slog.Int64("stamped", stamped),
				slog.Int64("stamped_to_id", lastID),
				slog.Duration("elapsed", time.Since(started)))
			lastLog = time.Now()
		}

		// A short batch is the last one: the SELECT found fewer unstamped rows
		// above the cursor than it asked for, so there are none beyond this
		// batch. Stopping here rather than on an empty batch saves the archive
		// one query per upgrade; rows inserted after this point are stamped by
		// the INSERT trigger, not by this walk.
		if count < batchSize {
			break
		}
	}
	if stamped > 0 {
		slog.Info("backfilled content_changed_at",
			slog.Int64("stamped", stamped),
			slog.Duration("elapsed", time.Since(started)))
	}
	return nil
}

// nextContentChangedBackfillBatch reads the next batch of ids needing the
// content_changed_at stamp: up to limit rows with a NULL watermark, in id
// order, above afterID (or from the start of the table when first is set).
// It returns the batch's first and last id and how many rows it spans; a count
// of zero means there is no work left above the cursor.
func nextContentChangedBackfillBatch(
	ctx context.Context,
	tx *loggedTx,
	firstBatchSQL, nextBatchSQL string,
	first bool,
	afterID, limit int64,
) (firstID, lastID, count int64, err error) {
	var rows *loggedRows
	if first {
		rows, err = tx.QueryContext(ctx, firstBatchSQL, limit)
	} else {
		rows, err = tx.QueryContext(ctx, nextBatchSQL, afterID, limit)
	}
	if err != nil {
		return 0, 0, 0, fmt.Errorf("select rows needing content_changed_at above id %d: %w", afterID, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, 0, 0, fmt.Errorf("scan message id needing content_changed_at: %w", err)
		}
		if count == 0 {
			firstID = id
		}
		lastID = id
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, 0, 0, fmt.Errorf("read rows needing content_changed_at above id %d: %w", afterID, err)
	}
	return firstID, lastID, count, nil
}

// dedupeAttachmentsBeforeUniqueIndex removes duplicate
// (message_id, content_hash) rows from attachments so the partial
// unique index idx_attachments_msg_content_hash can be created. Pre-fix
// UpsertAttachment used a SELECT-then-INSERT pattern that could create
// duplicates under concurrency; this cleans them up once. Idempotent.
func (s *Store) dedupeAttachmentsBeforeUniqueIndex(ctx context.Context, tx *loggedTx) error {
	_, err := tx.ExecContext(ctx, `
		DELETE FROM attachments
		WHERE content_hash IS NOT NULL AND content_hash != ''
		  AND id NOT IN (
			SELECT MIN(id) FROM attachments
			WHERE content_hash IS NOT NULL AND content_hash != ''
			GROUP BY message_id, content_hash
		  )
	`)
	return err
}

func (s *Store) ensureAttachmentOccurrenceUniqueIndexes(ctx context.Context) error {
	return s.runOnceMigration(
		ctx,
		migrationAttachmentOccurrenceUnique,
		1, false,
		func(ctx context.Context) error {
			return s.runMaintenance(ctx, func(ctx context.Context, tx *loggedTx) error {
				if _, err := tx.ExecContext(ctx,
					`DROP INDEX IF EXISTS idx_attachments_msg_content_hash`); err != nil {
					return fmt.Errorf("drop legacy attachment hash identity index: %w", err)
				}
				if _, err := tx.ExecContext(ctx, `
					CREATE UNIQUE INDEX IF NOT EXISTS idx_attachments_msg_source_part
					    ON attachments(message_id, source_part_key)
					    WHERE source_part_key IS NOT NULL
				`); err != nil {
					return fmt.Errorf("create attachment source-part identity index: %w", err)
				}
				if _, err := tx.ExecContext(ctx, `
					CREATE UNIQUE INDEX IF NOT EXISTS idx_attachments_msg_content_hash
					    ON attachments(message_id, content_hash)
					    WHERE source_part_key IS NULL
					      AND content_hash IS NOT NULL
					      AND content_hash != ''
				`); err != nil {
					return fmt.Errorf("create legacy attachment hash identity index: %w", err)
				}
				return nil
			})
		},
	)
}

// NeedsFTSBackfill reports whether the FTS index needs to be populated.
//
// This runs an anti-join that scans every message when the index is already
// complete (the healthy steady state), so it is expensive on a large archive.
// Callers on hot request paths must not invoke it per request — see the
// server-level memoization in handleCLISearch.
func (s *Store) NeedsFTSBackfill() bool {
	return s.NeedsFTSBackfillContext(context.Background())
}

// NeedsFTSBackfillContext bounds the full completeness probe by ctx.
func (s *Store) NeedsFTSBackfillContext(ctx context.Context) bool {
	if !s.fts5Available {
		return false
	}
	return s.dialect.FTSNeedsBackfill(ctx, s.db.DB)
}

// NeedsFTSBackfillQuick is the cheap, hot-path-safe form of NeedsFTSBackfill:
// true means a backfill is certainly needed; false may miss interior index
// holes (SQLite) that only the full probe finds.
func (s *Store) NeedsFTSBackfillQuick() bool {
	return s.NeedsFTSBackfillQuickContext(context.Background())
}

// NeedsFTSBackfillQuickContext is the request-aware form of
// NeedsFTSBackfillQuick.
func (s *Store) NeedsFTSBackfillQuickContext(ctx context.Context) bool {
	if !s.fts5Available {
		return false
	}
	return s.dialect.FTSNeedsBackfillQuick(ctx, s.db.DB)
}

// Stats holds database statistics.
//
// MessageCount is the count of active messages: those still present in the
// source account (deleted_at IS NULL AND deleted_from_source_at IS NULL).
// SourceDeletedCount is the count of archived messages that were deleted from
// the source account but are retained in the archive (deleted_at IS NULL AND
// deleted_from_source_at IS NOT NULL). The archive is the system of record,
// so the canonical total is MessageCount + SourceDeletedCount; callers that
// display a total must label the two populations rather than pick one
// silently. Dedup-hidden rows (deleted_at IS NOT NULL) are excluded from
// both counts.
type Stats struct {
	MessageCount       int64
	SourceDeletedCount int64
	ThreadCount        int64
	AttachmentCount    int64
	LabelCount         int64
	SourceCount        int64
	DatabaseSize       int64
}

// GetStats returns statistics about the database.
// Delegates to GetStatsForScope with no scope filter (global counts).
func (s *Store) GetStats() (*Stats, error) {
	return s.GetStatsForScope(nil)
}

// GetStatsContext is the context-aware form of GetStats. Request paths pass
// the request context so the count queries carry the request_id for SQL
// logging and are cancelled with the request.
func (s *Store) GetStatsContext(ctx context.Context) (*Stats, error) {
	return s.GetStatsForScopeContext(ctx, nil)
}

// GetStatsForScope returns statistics scoped to the given source IDs.
// When sourceIDs is nil or empty, returns global counts.
// All message-derived counts (threads, attachments, labels) exclude
// dedup-hidden and source-deleted messages via LiveMessagesWhere.
// DatabaseSize is always the global file size — it cannot be decomposed per source.
func (s *Store) GetStatsForScope(sourceIDs []int64) (*Stats, error) {
	return s.GetStatsForScopeContext(context.Background(), sourceIDs)
}

// GetStatsForScopeContext is the context-aware form of GetStatsForScope.
func (s *Store) GetStatsForScopeContext(ctx context.Context, sourceIDs []int64) (*Stats, error) {
	stats := &Stats{}

	var queries []struct {
		query string
		args  []any
		dest  *int64
	}

	if len(sourceIDs) == 0 {
		// Unscoped: global catalog counts, matching pre-slice-3 semantics.
		// All message-linked counts apply LiveMessagesWhere so dedup-hidden
		// and source-deleted rows aren't reported as live rows.
		queries = []struct {
			query string
			args  []any
			dest  *int64
		}{
			{
				"SELECT COUNT(*) FROM messages WHERE " + LiveMessagesWhere("", true),
				nil,
				&stats.MessageCount,
			},
			{
				"SELECT COUNT(*) FROM messages WHERE " + SourceDeletedMessagesWhere(""),
				nil,
				&stats.SourceDeletedCount,
			},
			{"SELECT COUNT(*) FROM conversations WHERE EXISTS (" +
				"SELECT 1 FROM messages m WHERE m.conversation_id = conversations.id AND " +
				LiveMessagesWhere("m", true) +
				")", nil,
				&stats.ThreadCount,
			},
			{"SELECT COUNT(*) FROM attachments a WHERE EXISTS (" +
				"SELECT 1 FROM messages m WHERE m.id = a.message_id AND " +
				LiveMessagesWhere("m", true) +
				")", nil,
				&stats.AttachmentCount,
			},
			{"SELECT COUNT(*) FROM labels l WHERE EXISTS (" +
				"SELECT 1 FROM message_labels ml JOIN messages m ON m.id = ml.message_id WHERE ml.label_id = l.id AND " +
				LiveMessagesWhere("m", true) +
				")", nil,
				&stats.LabelCount,
			},
			{
				"SELECT COUNT(*) FROM sources",
				nil,
				&stats.SourceCount,
			},
		}
	} else {
		// Build the IN (?, ?, ...) placeholder list. TrimSuffix is panic-safe
		// for any len(sourceIDs); the outer guard already routes empty slices
		// to the unscoped branch, but this avoids a negative slice index if
		// the guard is ever refactored.
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(sourceIDs)), ",")

		inClause := "source_id IN (" + placeholders + ")"
		args := make([]any, len(sourceIDs))
		for i, id := range sourceIDs {
			args[i] = id
		}
		cloneArgs := func() []any {
			out := make([]any, len(args))
			copy(out, args)
			return out
		}

		queries = []struct {
			query string
			args  []any
			dest  *int64
		}{
			{
				"SELECT COUNT(*) FROM messages WHERE " + LiveMessagesWhere("", true) + " AND " + inClause,
				cloneArgs(),
				&stats.MessageCount,
			},
			{
				"SELECT COUNT(*) FROM messages WHERE " + SourceDeletedMessagesWhere("") + " AND " + inClause,
				cloneArgs(),
				&stats.SourceDeletedCount,
			},
			{
				"SELECT COUNT(DISTINCT conversation_id) FROM messages WHERE " + LiveMessagesWhere("", true) + " AND " + inClause,
				cloneArgs(),
				&stats.ThreadCount,
			},
			{"SELECT COUNT(*) FROM attachments a WHERE EXISTS (" +
				"SELECT 1 FROM messages m WHERE m.id = a.message_id AND " +
				LiveMessagesWhere("m", true) +
				" AND m." + inClause + ")", cloneArgs(),
				&stats.AttachmentCount,
			},
			{"SELECT COUNT(DISTINCT ml.label_id) FROM message_labels ml " +
				"JOIN messages m ON m.id = ml.message_id WHERE " +
				LiveMessagesWhere("m", true) +
				" AND m." + inClause, cloneArgs(),
				&stats.LabelCount,
			},
		}
		// SourceCount reflects the scope: how many distinct accounts are
		// represented. Dedupe defensively in case a caller passes a
		// slice with repeats.
		seen := make(map[int64]struct{}, len(sourceIDs))
		for _, id := range sourceIDs {
			seen[id] = struct{}{}
		}
		stats.SourceCount = int64(len(seen))
	}

	for _, q := range queries {
		var row *sql.Row
		if len(q.args) > 0 {
			row = s.db.QueryRowContext(ctx, q.query, q.args...)
		} else {
			row = s.db.QueryRowContext(ctx, q.query)
		}
		if err := row.Scan(q.dest); err != nil {
			if s.dialect.IsNoSuchTableError(err) {
				continue
			}
			return nil, fmt.Errorf("get stats %q: %w", q.query, err)
		}
	}

	// Report logical main-database page allocation, excluding WAL/SHM files.
	if size, err := s.dialect.DatabaseSize(ctx, s.db.DB, s.dbPath); err == nil {
		stats.DatabaseSize = size
	} else if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}

	return stats, nil
}
