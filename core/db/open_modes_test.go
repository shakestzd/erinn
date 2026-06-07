package db_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
)

// migrationCallRecorder wraps db.MigrationHook and records every call.
type migrationCallRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *migrationCallRecorder) Record(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name)
}

func (r *migrationCallRecorder) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

// TestOpenReadOnly_NoDDL verifies that OpenReadOnly returns a *sql.DB that:
//   - Opens successfully when the database file already exists.
//   - Rejects DDL statements (CREATE TABLE, ALTER TABLE, DROP TABLE) — SQLite's
//     mode=ro enforces this at the engine level.
//   - Allows read queries (SELECT).
func TestOpenReadOnly_NoDDL(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Seed: create a valid wipnote DB first so read-only open has something to open.
	seed, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	seed.Close()

	// Now open read-only.
	rodb, err := db.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer rodb.Close()

	// READ must succeed.
	rows, err := rodb.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		t.Fatalf("SELECT on read-only db: %v", err)
	}
	rows.Close()

	// DDL must be rejected.
	// Note: DROP TABLE IF EXISTS on a non-existent table is a no-op in SQLite's
	// read-only mode, so we test DROP on an existing table (sessions) instead.
	ddlCases := []string{
		`CREATE TABLE IF NOT EXISTS ddl_test_probe (id TEXT PRIMARY KEY)`,
		`ALTER TABLE sessions ADD COLUMN zzz_probe TEXT`,
		`DROP TABLE sessions`,
	}
	for _, stmt := range ddlCases {
		_, execErr := rodb.Exec(stmt)
		if execErr == nil {
			t.Errorf("expected DDL to fail on read-only DB; stmt: %.60s", stmt)
		} else {
			// Verify the error is a read-only rejection (not an unrelated error).
			msg := strings.ToLower(execErr.Error())
			if !strings.Contains(msg, "readonly") && !strings.Contains(msg, "read-only") &&
				!strings.Contains(msg, "read only") && !strings.Contains(msg, "attempt to write") &&
				!strings.Contains(msg, "sqlite_readonly") {
				t.Errorf("DDL error unexpected type for stmt %.60s: %v", stmt, execErr)
			}
		}
	}
}

// TestOpenReadOnly_NonExistentFile verifies that OpenReadOnly returns an error
// when the database file does not exist (mode=ro must not create the file).
func TestOpenReadOnly_NonExistentFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nonexistent.db")

	_, err := db.OpenReadOnly(dbPath)
	if err == nil {
		t.Fatal("expected OpenReadOnly to fail on non-existent file; got nil error")
	}

	// Confirm the file was NOT created.
	if _, statErr := os.Stat(dbPath); statErr == nil {
		t.Error("OpenReadOnly must not create the database file in read-only mode")
	}
}

// TestOpenWritable_NoMigrations verifies that OpenWritable applies connection
// pragmas but does NOT invoke any migration hooks (no schema creation or alter
// table migrations).
func TestOpenWritable_NoMigrations(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Seed: create a valid wipnote DB first.
	seed, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	seed.Close()

	// Track what migration hooks fire.
	recorder := &migrationCallRecorder{}
	db.SetMigrationObserver(recorder.Record)
	defer db.SetMigrationObserver(nil)

	writable, err := db.OpenWritable(dbPath)
	if err != nil {
		t.Fatalf("OpenWritable: %v", err)
	}
	defer writable.Close()

	// No migration calls should have been made.
	calls := recorder.Calls()
	if len(calls) != 0 {
		t.Errorf("OpenWritable called migration hooks: %v", calls)
	}

	// Normal read/write must work.
	_, err = writable.Exec(`INSERT OR IGNORE INTO metadata (key, value) VALUES ('test_key', 'test_val')`)
	if err != nil {
		t.Errorf("INSERT on OpenWritable db: %v", err)
	}

	var val string
	row := writable.QueryRow(`SELECT value FROM metadata WHERE key = 'test_key'`)
	if err := row.Scan(&val); err != nil {
		t.Errorf("SELECT after INSERT on OpenWritable db: %v", err)
	}
	if val != "test_val" {
		t.Errorf("SELECT value = %q, want %q", val, "test_val")
	}
}

// TestOpenMigrated_RunsMigrations verifies that Open (the migrated writable mode)
// does invoke migration hooks.
func TestOpenMigrated_RunsMigrations(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	recorder := &migrationCallRecorder{}
	db.SetMigrationObserver(recorder.Record)
	defer db.SetMigrationObserver(nil)

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open (migrated): %v", err)
	}
	defer database.Close()

	// Migration hooks should have fired.
	calls := recorder.Calls()
	if len(calls) == 0 {
		t.Error("Open (migrated) expected to invoke migration hooks, got none")
	}
}

func TestOpenModes_BusyTimeoutPerPooledConnection(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "busy-timeout-pool.db")

	seed, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	seed.Close()

	t.Run("Open", func(t *testing.T) {
		database, err := db.Open(dbPath)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer database.Close()
		assertBusyTimeoutOnSecondPooledConn(t, database)
	})

	t.Run("OpenWritable", func(t *testing.T) {
		database, err := db.OpenWritable(dbPath)
		if err != nil {
			t.Fatalf("OpenWritable: %v", err)
		}
		defer database.Close()
		assertBusyTimeoutOnSecondPooledConn(t, database)
	})

	t.Run("OpenReadOnly", func(t *testing.T) {
		database, err := db.OpenReadOnly(dbPath)
		if err != nil {
			t.Fatalf("OpenReadOnly: %v", err)
		}
		defer database.Close()
		assertBusyTimeoutOnSecondPooledConn(t, database)
	})
}

func assertBusyTimeoutOnSecondPooledConn(t *testing.T, database *sql.DB) {
	t.Helper()
	database.SetMaxOpenConns(2)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn1, err := database.Conn(ctx)
	if err != nil {
		t.Fatalf("conn1: %v", err)
	}
	defer conn1.Close()

	conn2, err := database.Conn(ctx)
	if err != nil {
		t.Fatalf("conn2: %v", err)
	}
	defer conn2.Close()

	var got int
	if err := conn2.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&got); err != nil {
		t.Fatalf("PRAGMA busy_timeout on conn2: %v", err)
	}
	if got != 5000 {
		t.Fatalf("conn2 busy_timeout = %d, want 5000", got)
	}
}

func TestOpenWritable_BeginUsesImmediateTransaction(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "begin-immediate.db")

	seed, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	seed.Close()

	writable, err := db.OpenWritable(dbPath)
	if err != nil {
		t.Fatalf("OpenWritable: %v", err)
	}
	defer writable.Close()

	tx, err := writable.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	competitor, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(1)")
	if err != nil {
		t.Fatalf("competitor open: %v", err)
	}
	defer competitor.Close()

	_, err = competitor.Exec("BEGIN IMMEDIATE")
	if err == nil {
		_, _ = competitor.Exec("ROLLBACK")
		t.Fatal("competing BEGIN IMMEDIATE succeeded; OpenWritable Begin did not acquire the writer lock up front")
	}
	if !db.IsBusyError(err) {
		t.Fatalf("competing BEGIN IMMEDIATE error = %v, want SQLITE_BUSY", err)
	}
}

// TestOpenReadOnly_NoWALSidecars verifies that OpenReadOnly succeeds on a WAL
// database whose -wal and -shm sidecar files are absent (the normal state after
// a clean checkpoint or on a fresh install). mode=ro fails in this scenario
// because SQLite cannot create the -shm coordination file; mode=rw +
// query_only=ON is the correct approach.
func TestOpenReadOnly_NoWALSidecars(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Seed: create a fully-committed WAL database (Open sets journal_mode=WAL
	// when the filesystem is safe for mmap, but we force WAL explicitly and
	// checkpoint+close so the -wal/-shm files are gone at the start of the test).
	seed, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	// Checkpoint to consolidate the WAL into the main db file.
	if _, execErr := seed.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); execErr != nil {
		t.Logf("wal_checkpoint: %v (non-fatal)", execErr)
	}
	seed.Close()

	// Remove WAL sidecar files to simulate the post-checkpoint state.
	for _, suffix := range []string{"-wal", "-shm"} {
		path := dbPath + suffix
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			t.Fatalf("remove %s: %v", suffix, removeErr)
		}
	}

	// OpenReadOnly must succeed even without sidecars.
	rodb, err := db.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly on WAL db without sidecars: %v", err)
	}
	defer rodb.Close()

	// Read must succeed.
	var n int
	if err := rodb.QueryRow("SELECT COUNT(*) FROM sqlite_master").Scan(&n); err != nil {
		t.Fatalf("SELECT after OpenReadOnly: %v", err)
	}

	// Write must be rejected.
	_, writeErr := rodb.Exec(`INSERT OR IGNORE INTO metadata (key, value) VALUES ('probe', '1')`)
	if writeErr == nil {
		t.Error("expected write to fail on query_only connection; got nil error")
	}
}

// TestOpenWritable_MissingFileFails verifies that OpenWritable returns an error
// when the database file does not exist, rather than auto-creating an empty
// SQLite file that would pass opens but fail on real queries.
func TestOpenWritable_MissingFileFails(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nonexistent.db")

	_, err := db.OpenWritable(dbPath)
	if err == nil {
		t.Fatal("expected OpenWritable to fail on non-existent file; got nil error")
	}

	// Confirm the file was NOT created.
	if _, statErr := os.Stat(dbPath); statErr == nil {
		t.Error("OpenWritable must not create a new database file when the path does not exist")
	}
}

// TestOpenReadOnly_QueryOnlyPerPooledConnection verifies that query_only is
// enforced on EVERY physical connection in the pool, not just the first one.
//
// Strategy: open the DB with MaxOpenConns > 1, hold one connection busy inside
// a transaction (forcing the pool to open a second physical connection for the
// next request), then attempt a write on that second connection and assert it
// fails with a read-only error. This proves that the DSN _pragma=query_only(1)
// is applied per-connection by the driver, not just once on the pool.
func TestOpenReadOnly_QueryOnlyPerPooledConnection(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "pooltest.db")

	// Seed: create a valid wipnote DB.
	seed, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	seed.Close()

	rodb, err := db.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer rodb.Close()

	// Allow the pool to open multiple physical connections.
	rodb.SetMaxOpenConns(4)

	// Hold connection 1 busy inside a transaction.
	tx, err := rodb.Begin()
	if err != nil {
		t.Fatalf("Begin tx (conn 1): %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Issue a read on the held transaction so it has an open read lock.
	row := tx.QueryRow("SELECT COUNT(*) FROM sqlite_master")
	var cnt int
	if err := row.Scan(&cnt); err != nil {
		t.Fatalf("Scan on held tx: %v", err)
	}

	// Acquire a SECOND connection from the pool by running a query outside the tx.
	// The pool must open a new physical connection since conn 1 is occupied.
	rows2, err := rodb.Query("SELECT name FROM sqlite_master WHERE type='table' LIMIT 1")
	if err != nil {
		t.Fatalf("query on conn 2: %v", err)
	}
	rows2.Close()

	// Attempt a write — must fail on every connection.
	_, writeErr := rodb.Exec(`INSERT OR IGNORE INTO metadata (key, value) VALUES ('pool_probe', '1')`)
	if writeErr == nil {
		t.Fatal("expected write to fail on pooled read-only connection; got nil")
	}
	msg := strings.ToLower(writeErr.Error())
	if !strings.Contains(msg, "readonly") && !strings.Contains(msg, "read-only") &&
		!strings.Contains(msg, "read only") && !strings.Contains(msg, "attempt to write") {
		t.Errorf("write error unexpected type: %v", writeErr)
	}
}

// TestOpenReadOnly_PromptClose verifies that read-only paths close rows
// promptly and do not hold long-lived read transactions that block writers.
func TestOpenReadOnly_PromptClose(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	seed, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	seed.Close()

	rodb, err := db.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer rodb.Close()

	// Open rows but close them promptly — writer must not be blocked.
	rows, err := rodb.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	// Consume and close rows promptly.
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("Scan: %v", err)
		}
	}
	rows.Close()

	// Now a writer should be able to open and write without blocking.
	writer, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open (writer) after read: %v", err)
	}
	defer writer.Close()

	_, err = writer.Exec(`INSERT OR IGNORE INTO metadata (key, value) VALUES ('prompt_close_test', '1')`)
	if err != nil {
		t.Errorf("writer INSERT after reader closed rows: %v", err)
	}
}
