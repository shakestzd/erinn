package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// migrationObserver is an optional hook called by Open each time a migration
// step fires. It is used exclusively by tests to assert that OpenWritable does
// NOT trigger migrations. Production code should leave it nil.
var migrationObserver func(name string)

// SetMigrationObserver installs a migration observer for testing. Pass nil to
// remove a previously installed observer. This function is not concurrency-safe
// — install observers only at test setup before any Open call.
func SetMigrationObserver(fn func(name string)) {
	migrationObserver = fn
}

// notifyMigration calls the migration observer if one is installed.
func notifyMigration(name string) {
	if migrationObserver != nil {
		migrationObserver(name)
	}
}

// OpenReadOnly opens an existing wipnote SQLite database in read-only mode.
// It applies connection-level pragmas (busy_timeout, cache_size, etc.) but
// does NOT run any DDL, migrations, or normalisation writes.
//
// Read-only enforcement is achieved via PRAGMA query_only=ON rather than the
// SQLite URI mode=ro parameter. mode=ro requires the -shm sidecar to exist for
// WAL databases; when it is absent (routine state after a clean checkpoint or
// on a fresh install) mode=ro returns SQLITE_CANTOPEN before the connection is
// usable. mode=rw allows SQLite to create the -shm coordination file when
// needed while query_only=ON blocks all DML/DDL at the engine level, giving us
// the same read-only safety without the WAL sidecar requirement.
//
// IMPORTANT — prompt close contract: read-only paths MUST close all sql.Rows,
// sql.Stmt, and sql.Tx values promptly after use. In DELETE journal mode a
// reader that holds an open shared-lock blocks the single writer from acquiring
// the reserved lock. Failure to close promptly can cause SQLITE_BUSY on the
// writer side. In WAL mode readers and writers do not block each other, but the
// prompt-close discipline must still be observed for portability.
//
// Returns an error if the database file does not exist.
func OpenReadOnly(dbPath string) (*sql.DB, error) {
	// Fail fast if the file doesn't exist — we never create a new file here.
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("OpenReadOnly: database file not found: %w", err)
	}

	// Build a read-only DSN using mode=rw so WAL -shm sidecars can be created
	// when absent. query_only=ON (applied below) blocks all writes at the engine
	// level, providing the same read-only safety without the sidecar requirement.
	dsn := buildReadOnlyDSN(dbPath)
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("OpenReadOnly: sql.Open: %w", err)
	}

	// Apply read-compatible pragmas. query_only=ON is the critical one — it
	// prevents all DML/DDL on this connection while still allowing WAL sidecar
	// creation (which is an OS-level file operation, not SQL).
	pragmas := buildReadOnlyPragmas()
	if err := applyReadOnlyPragmas(database, pragmas); err != nil {
		database.Close()
		return nil, fmt.Errorf("OpenReadOnly: apply pragmas: %w", err)
	}

	// Verify read access with a lightweight query.
	if _, err := database.Exec("SELECT 1"); err != nil {
		database.Close()
		return nil, fmt.Errorf("OpenReadOnly: smoke check failed: %w", err)
	}

	return database, nil
}

// OpenWritable opens an existing wipnote SQLite database for reading and
// writing. It applies all connection-level pragmas needed for normal operation
// but does NOT run schema creation (CreateAllTables / CreateAllIndexes) or any
// migration hooks.
//
// Use this mode when the schema is already known to be current (e.g. the
// database was previously initialised by Open) and you only need to read/write
// data rows. Callers that need schema creation or migrations must use Open
// instead.
//
// IMPORTANT — prompt close contract: same as OpenReadOnly. In DELETE journal
// mode, open transactions can block the writer. Close rows and transactions
// promptly.
//
// APPROVED CALLERS — every first-party Go callsite that opens a writable
// SQLite handle is enumerated in cmd/wipnote/sqlite_write_boundary_test.go
// (variable approvedWriteSites). Adding a new caller without updating that
// inventory fails the boundary test. Hook / indexer / OTLP-receiver paths
// MUST route writes through the slice-6 writer service (feat-f3bcbcef);
// do not add new direct OpenWritable callers in those locations.
func OpenWritable(dbPath string) (*sql.DB, error) {
	// Fail fast if the database file does not exist. OpenWritable is intended
	// for an already-initialised, schema-current database. Auto-creating an
	// empty SQLite file here would silently produce a zero-schema DB that looks
	// open but fails on the first real query. Callers that need to create a new
	// database must use Open instead (which runs CreateAllTables + migrations).
	// In-memory databases (":memory:") are exempt — they are always "new."
	isInMemory := strings.Contains(dbPath, ":memory:")
	if !isInMemory {
		if _, err := os.Stat(dbPath); err != nil {
			return nil, fmt.Errorf("OpenWritable: database file not found (use Open to create a new database): %w", err)
		}
	}

	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("OpenWritable: creating db directory: %w", err)
	}

	// Use the same busy_timeout DSN embedding as Open to prevent SQLITE_BUSY
	// on the very first connection before pragmas have been applied. _txlock
	// makes database/sql Begin issue BEGIN IMMEDIATE, avoiding deferred
	// SHARED→RESERVED upgrade BUSY on DELETE-journal filesystems.
	dsn := dbPath
	if !isInMemory {
		dsn = dsn + "?_pragma=busy_timeout(5000)&_txlock=immediate"
	}

	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("OpenWritable: sql.Open: %w", err)
	}

	// Apply all connection pragmas (same as Open) but do NOT call CreateAllTables,
	// CreateAllIndexes, or any migration helpers.
	if err := ApplyPragmas(database, BuildPragmas(dbPath)); err != nil {
		database.Close()
		return nil, fmt.Errorf("OpenWritable: applying pragmas: %w", err)
	}

	return database, nil
}

// OpenReadOnlyMigrated guarantees the database at dbPath exists and is at the
// current schema version, then returns a read-only handle for the actual
// query work. It mirrors the serve_child.go topology (writable Open FIRST so
// schema/migrations are applied — mode=ro never creates a file and never
// migrates — THEN a separate read-only handle for the long read path).
//
// bug-7dbaf552 / roborev followup: read-only CLI surfaces (`wipnote query`,
// `wipnote lineage`) were switched to OpenReadOnly for contention safety, but
// that dropped the migrate-on-open guarantee that the prior writable open
// provided — a fresh or schema-behind workspace would fail before the read
// even ran. This helper restores BOTH guarantees: Open here is the Fix-1
// RetryOnBusy-wrapped migration path, so the brief bootstrap open is itself
// resilient to a transient SQLITE_BUSY; the bootstrap handle is closed
// immediately so it never holds the writer lock during the (potentially long)
// read path that follows on the returned read-only handle.
func OpenReadOnlyMigrated(dbPath string) (*sql.DB, error) {
	boot, err := Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("bootstrap (schema/migrations): %w", err)
	}
	if cerr := boot.Close(); cerr != nil {
		return nil, fmt.Errorf("close bootstrap handle: %w", cerr)
	}
	return OpenReadOnly(dbPath)
}

// buildReadOnlyDSN builds a URI DSN for the read-only connection.
// We use mode=rw (not mode=ro) so that SQLite can create the WAL -shm
// coordination file when it is absent — a routine state after a clean
// checkpoint. The connection is then locked read-only by query_only=ON
// embedded in the DSN as a _pragma parameter.
//
// POOL SAFETY: _pragma parameters in the modernc.org/sqlite DSN are applied
// by the driver's applyQueryParams hook on EVERY new physical connection
// (not once on the pool). This guarantees that every connection the pool
// opens — including connections created lazily after the initial Exec — has
// query_only=1 set before it is returned to a caller, so writes cannot
// succeed on any pooled connection.
//
// The single db.Exec("PRAGMA query_only=1") call in applyReadOnlyPragmas is
// retained as belt-and-suspenders (it fires on the first connection only) but
// the DSN parameter is the authoritative per-connection guard.
func buildReadOnlyDSN(dbPath string) string {
	if strings.Contains(dbPath, ":memory:") {
		// In-memory databases cannot use file URI mode; keep as-is.
		return dbPath
	}
	// mode=rw: allow WAL sidecar creation.
	// _pragma=busy_timeout(5000): protect first lock acquisition.
	// _pragma=query_only(1): applied on EVERY physical connection by the driver;
	//   prevents all DML/DDL at the engine level on every pooled connection.
	return "file:" + dbPath + "?mode=rw&_pragma=busy_timeout(5000)&_pragma=query_only(1)"
}

// buildReadOnlyPragmas returns the pragma set appropriate for a query-only
// connection. query_only=ON is the critical setting: it blocks all DML/DDL
// while allowing the connection to open in mode=rw so WAL sidecars can be
// created when absent.
func buildReadOnlyPragmas() map[string]string {
	return map[string]string{
		"query_only": "1",
		"cache_size": "-64000",
		"temp_store": "MEMORY",
	}
}

// applyReadOnlyPragmas sets the query_only and performance pragmas on the
// connection. query_only is applied first and is required — if it fails the
// open is aborted to prevent accidental writes. The remaining pragmas are
// best-effort.
func applyReadOnlyPragmas(database *sql.DB, pragmas map[string]string) error {
	// query_only MUST succeed — it is the write-guard for this connection.
	if _, err := database.Exec("PRAGMA query_only = 1"); err != nil {
		return fmt.Errorf("set query_only: %w", err)
	}
	for pragma, value := range pragmas {
		if pragma == "query_only" {
			continue // already applied above
		}
		if _, err := database.Exec(fmt.Sprintf("PRAGMA %s = %s", pragma, value)); err != nil {
			// Best-effort: skip non-critical pragmas.
			_ = err
		}
	}
	return nil
}
