package db_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	_ "modernc.org/sqlite"
)

// fileDBPath returns a per-test on-disk SQLite path. In-memory databases reset
// user_version on each connection, which defeats the purpose of these tests.
func fileDBPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

// openRaw opens a SQLite file directly (no Open wrapper, no pragmas, no
// migrations) — used to seed fixtures at specific user_version states.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open raw: %v", err)
	}
	return database
}

// queryUserVersion reads PRAGMA user_version from a *sql.DB.
func queryUserVersion(t *testing.T, database *sql.DB) int {
	t.Helper()
	var v int
	if err := database.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("PRAGMA user_version: %v", err)
	}
	return v
}

// TestCurrentSchemaVersion_PositiveAfterOpen confirms that opening a brand-new
// DB sets PRAGMA user_version to the package's currentSchemaVersion (> 0).
func TestCurrentSchemaVersion_PositiveAfterOpen(t *testing.T) {
	path := fileDBPath(t, "fresh.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open fresh: %v", err)
	}
	defer database.Close()

	v := queryUserVersion(t, database)
	if v <= 0 {
		t.Fatalf("PRAGMA user_version after fresh Open = %d, want > 0", v)
	}
	if v != db.CurrentSchemaVersion() {
		t.Fatalf("PRAGMA user_version after fresh Open = %d, want %d",
			v, db.CurrentSchemaVersion())
	}
}

// TestOpenWarmDB_SkipsDDL verifies that a second Open of an already-migrated
// database executes ZERO migration steps (no CREATE, ALTER, DROP, trigger, or
// normalization UPDATE statements).
//
// The migration observer hook fires on every migration step, so a zero-call
// recording proves the fast warm path was taken.
func TestOpenWarmDB_SkipsDDL(t *testing.T) {
	path := fileDBPath(t, "warm.db")

	// Cold open: applies all migrations and lands at currentSchemaVersion.
	cold, err := db.Open(path)
	if err != nil {
		t.Fatalf("cold Open: %v", err)
	}
	cold.Close()

	// Warm open: must invoke ZERO migration hooks.
	recorder := &migrationCallRecorder{}
	db.SetMigrationObserver(recorder.Record)
	defer db.SetMigrationObserver(nil)

	warm, err := db.Open(path)
	if err != nil {
		t.Fatalf("warm Open: %v", err)
	}
	defer warm.Close()

	calls := recorder.Calls()
	if len(calls) != 0 {
		t.Fatalf("warm Open invoked migration hooks (want 0): %v", calls)
	}

	// user_version must remain at currentSchemaVersion.
	if v := queryUserVersion(t, warm); v != db.CurrentSchemaVersion() {
		t.Fatalf("user_version after warm Open = %d, want %d",
			v, db.CurrentSchemaVersion())
	}
}

// TestMigrateFromUserVersion0_EmptyDB applies migrations to an empty DB at
// user_version=0 (the legacy/fresh case) and verifies that ALL migrations run
// in order and user_version ends at currentSchemaVersion.
func TestMigrateFromUserVersion0_EmptyDB(t *testing.T) {
	path := fileDBPath(t, "v0_empty.db")

	// Seed: open raw, force user_version=0 (already the default), close.
	raw := openRaw(t, path)
	if _, err := raw.Exec("PRAGMA user_version = 0"); err != nil {
		t.Fatalf("seed user_version=0: %v", err)
	}
	if v := queryUserVersion(t, raw); v != 0 {
		t.Fatalf("seeded user_version = %d, want 0", v)
	}
	raw.Close()

	// Track which migrations apply.
	recorder := &migrationCallRecorder{}
	db.SetMigrationObserver(recorder.Record)
	defer db.SetMigrationObserver(nil)

	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open v0 empty: %v", err)
	}
	defer database.Close()

	// All declared step names should have fired in order.
	calls := recorder.Calls()
	wantNames := db.MigrationStepNames()
	if len(calls) != len(wantNames) {
		t.Fatalf("step count mismatch: got %d (%v), want %d (%v)",
			len(calls), calls, len(wantNames), wantNames)
	}
	for i, want := range wantNames {
		if calls[i] != want {
			t.Errorf("step[%d] = %q, want %q", i, calls[i], want)
		}
	}

	// user_version landed at the current schema version.
	if v := queryUserVersion(t, database); v != db.CurrentSchemaVersion() {
		t.Fatalf("user_version after migrate = %d, want %d",
			v, db.CurrentSchemaVersion())
	}
}

// TestMigrateFromUserVersionNMinus1 applies only the final migration step to a
// DB pre-staged at user_version = currentSchemaVersion - 1. Verifies that only
// the last step runs and that user_version advances by exactly one.
func TestMigrateFromUserVersionNMinus1(t *testing.T) {
	current := db.CurrentSchemaVersion()
	if current < 2 {
		t.Skipf("currentSchemaVersion=%d < 2; cannot test N-1 migration", current)
	}

	path := fileDBPath(t, "v_nminus1.db")

	// Cold-migrate, then forcibly rewind to currentSchemaVersion-1 to simulate
	// a DB that already has the current full schema but is "one step behind".
	cold, err := db.Open(path)
	if err != nil {
		t.Fatalf("cold seed Open: %v", err)
	}
	// SQLite does not support parameter binding for PRAGMA values, so the
	// literal target version is rendered into the statement.
	if _, err := cold.Exec(fmt.Sprintf("PRAGMA user_version = %d", current-1)); err != nil {
		t.Fatalf("rewind user_version: %v", err)
	}
	cold.Close()

	recorder := &migrationCallRecorder{}
	db.SetMigrationObserver(recorder.Record)
	defer db.SetMigrationObserver(nil)

	warm, err := db.Open(path)
	if err != nil {
		t.Fatalf("warm Open at N-1: %v", err)
	}
	defer warm.Close()

	calls := recorder.Calls()
	want := db.MigrationStepNames()
	wantLast := want[len(want)-1:]
	if len(calls) != 1 || calls[0] != wantLast[0] {
		t.Fatalf("warm Open at N-1 ran %v; want exactly [%s]", calls, wantLast[0])
	}

	if v := queryUserVersion(t, warm); v != current {
		t.Fatalf("user_version after N-1 migrate = %d, want %d", v, current)
	}
}

// TestMigrateFromPreCopySwap simulates a legacy DB whose agent_events table
// was created WITHOUT the CHECK constraint and WITH the self-referential
// parent_event_id foreign key. After Open runs migrations, the table must have
// the CHECK constraint and must not have the parent_event_id FK, and the
// migration must run AT MOST ONCE.
func TestMigrateFromPreCopySwap(t *testing.T) {
	path := fileDBPath(t, "pre_copy_swap.db")

	// Seed: create a legacy schema. We construct the agent_events table without
	// the CHECK constraint and WITH the parent_event_id FK; everything else is
	// a minimal viable schema needed by the FK targets.
	raw := openRaw(t, path)
	_, err := raw.Exec(`CREATE TABLE sessions (
		session_id TEXT PRIMARY KEY,
		agent_assigned TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		total_events INTEGER DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'active'
	)`)
	if err != nil {
		t.Fatalf("seed sessions: %v", err)
	}
	_, err = raw.Exec(`CREATE TABLE features (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		title TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'todo',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		t.Fatalf("seed features: %v", err)
	}
	// Legacy agent_events: no CHECK, has self-FK on parent_event_id. The
	// column list mirrors the original wipnote schema before the CHECK
	// constraint was added — in particular feature_id was always present (it
	// participates in the FK to features), but the parent_event_id
	// self-referential FK existed (this is what the swap is meant to drop).
	_, err = raw.Exec(`CREATE TABLE agent_events (
		event_id TEXT PRIMARY KEY,
		agent_id TEXT NOT NULL,
		event_type TEXT NOT NULL,
		timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		tool_name TEXT,
		session_id TEXT NOT NULL,
		feature_id TEXT,
		parent_event_id TEXT,
		FOREIGN KEY (session_id) REFERENCES sessions(session_id),
		FOREIGN KEY (feature_id) REFERENCES features(id),
		FOREIGN KEY (parent_event_id) REFERENCES agent_events(event_id)
	)`)
	if err != nil {
		t.Fatalf("seed legacy agent_events: %v", err)
	}
	// Force user_version=0 so the migration runner treats this as a legacy DB.
	if _, err := raw.Exec("PRAGMA user_version = 0"); err != nil {
		t.Fatalf("seed user_version=0: %v", err)
	}
	raw.Close()

	// First Open: migrations run end-to-end.
	recorder1 := &migrationCallRecorder{}
	db.SetMigrationObserver(recorder1.Record)

	database, err := db.Open(path)
	if err != nil {
		db.SetMigrationObserver(nil)
		t.Fatalf("first Open of legacy fixture: %v", err)
	}
	db.SetMigrationObserver(nil)

	// agent_events must now have the CHECK constraint.
	var ddl string
	if err := database.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='agent_events'`,
	).Scan(&ddl); err != nil {
		database.Close()
		t.Fatalf("read agent_events DDL: %v", err)
	}
	if !strings.Contains(ddl, "tool_name != 'UserQuery'") {
		database.Close()
		t.Fatalf("agent_events DDL missing CHECK constraint after migrate; got:\n%s", ddl)
	}
	if strings.Contains(ddl, "REFERENCES agent_events(event_id)") {
		database.Close()
		t.Fatalf("agent_events DDL still has self-referential FK; got:\n%s", ddl)
	}

	// One of the recorded step names must be the copy-swap step.
	swapStep := db.CopySwapStepName()
	if swapStep == "" {
		database.Close()
		t.Fatal("CopySwapStepName returned empty — runner did not expose copy-swap step")
	}
	firstCalls := recorder1.Calls()
	if !contains(firstCalls, swapStep) {
		database.Close()
		t.Fatalf("first Open did not invoke %q; got %v", swapStep, firstCalls)
	}

	// user_version landed at current.
	if v := queryUserVersion(t, database); v != db.CurrentSchemaVersion() {
		database.Close()
		t.Fatalf("user_version after first migrate = %d, want %d",
			v, db.CurrentSchemaVersion())
	}
	database.Close()

	// Second Open: copy-swap step must NOT fire again.
	recorder2 := &migrationCallRecorder{}
	db.SetMigrationObserver(recorder2.Record)
	defer db.SetMigrationObserver(nil)

	database2, err := db.Open(path)
	if err != nil {
		t.Fatalf("second Open after migrate: %v", err)
	}
	defer database2.Close()

	secondCalls := recorder2.Calls()
	if contains(secondCalls, swapStep) {
		t.Fatalf("second Open re-ran %q; got %v", swapStep, secondCalls)
	}
	if len(secondCalls) != 0 {
		t.Fatalf("second Open ran migrations: %v (want none)", secondCalls)
	}
}

// TestMigrateFromPreCopySwap_IndexesRestored verifies that agent_events
// indexes are reinstalled after the copy-and-swap migration drops the table.
// Without this restore step, lookups by agent_id, session_id, etc. would do
// full table scans after migration.
func TestMigrateFromPreCopySwap_IndexesRestored(t *testing.T) {
	path := fileDBPath(t, "pre_copy_swap_indexes.db")

	// Reuse the legacy seed from TestMigrateFromPreCopySwap.
	raw := openRaw(t, path)
	_, err := raw.Exec(`CREATE TABLE sessions (
		session_id TEXT PRIMARY KEY,
		agent_assigned TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		total_events INTEGER DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'active'
	)`)
	if err != nil {
		t.Fatalf("seed sessions: %v", err)
	}
	_, err = raw.Exec(`CREATE TABLE features (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		title TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'todo',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		t.Fatalf("seed features: %v", err)
	}
	_, err = raw.Exec(`CREATE TABLE agent_events (
		event_id TEXT PRIMARY KEY,
		agent_id TEXT NOT NULL,
		event_type TEXT NOT NULL,
		timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		tool_name TEXT,
		session_id TEXT NOT NULL,
		feature_id TEXT,
		parent_event_id TEXT,
		FOREIGN KEY (session_id) REFERENCES sessions(session_id),
		FOREIGN KEY (feature_id) REFERENCES features(id),
		FOREIGN KEY (parent_event_id) REFERENCES agent_events(event_id)
	)`)
	if err != nil {
		t.Fatalf("seed legacy agent_events: %v", err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 0"); err != nil {
		t.Fatalf("seed user_version=0: %v", err)
	}
	raw.Close()

	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open legacy fixture: %v", err)
	}
	defer database.Close()

	// Sample required indexes — these must exist after migration.
	requiredIndexes := []string{
		"idx_agent_events_session_ts_desc",
		"idx_agent_events_agent_ts_desc",
		"idx_agent_events_agent",
		"idx_agent_events_type",
		"idx_agent_events_timestamp",
	}
	for _, name := range requiredIndexes {
		var got string
		err := database.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, name,
		).Scan(&got)
		if err != nil {
			t.Errorf("index %q missing after migrate: %v", name, err)
			continue
		}
		if got != name {
			t.Errorf("index lookup for %q returned %q", name, got)
		}
	}
}

// TestOpenWarmDB_NoWriteLockOnContention verifies the warm-open fast path
// does not acquire the write lock. We hold a long-lived writer (an open BEGIN
// IMMEDIATE transaction) on the DB, then perform a warm Open from a second
// handle. If Open attempts any DDL it will hit SQLITE_BUSY (the busy_timeout
// is 5s and the contended write lock is held by the parent). A successful
// warm Open with no SQLITE_BUSY proves zero writes.
func TestOpenWarmDB_NoWriteLockOnContention(t *testing.T) {
	path := fileDBPath(t, "warm_contention.db")

	// First Open: run migrations to completion.
	primary, err := db.Open(path)
	if err != nil {
		t.Fatalf("primary Open: %v", err)
	}
	defer primary.Close()

	// Hold a write lock on the primary handle. BEGIN IMMEDIATE acquires the
	// RESERVED lock right away, so any other writer attempting to commit must
	// wait. Use a tx so we can defer the rollback.
	tx, err := primary.Begin()
	if err != nil {
		t.Fatalf("primary Begin: %v", err)
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO metadata (key, value) VALUES ('lock_holder', '1')`); err != nil {
		tx.Rollback()
		t.Fatalf("primary INSERT to hold lock: %v", err)
	}

	// Warm Open from a second handle. Must NOT attempt any write — and must
	// therefore succeed in well under the 5s busy_timeout. If it tries to run
	// DDL it will block on the write lock and either fail with SQLITE_BUSY or
	// take a long time.
	done := make(chan error, 1)
	go func() {
		secondary, err := db.Open(path)
		if err != nil {
			done <- err
			return
		}
		secondary.Close()
		done <- nil
	}()

	// Warm Open must complete quickly because the fast path issues only reads.
	// A 2s budget is generous (busy_timeout for a contended write is 5s).
	select {
	case err := <-done:
		if err != nil {
			tx.Rollback()
			t.Fatalf("warm Open under writer-held lock: %v", err)
		}
	case <-time.After(2 * time.Second):
		tx.Rollback()
		t.Fatal("warm Open did not complete within 2s under writer-held lock — fast path likely attempted a write")
	}

	// Cleanup.
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

// TestMigrationsAreOrdered confirms the migration registry presents step
// versions in strictly increasing order, with the last version equal to
// CurrentSchemaVersion. Catches an accidental gap or duplicate.
func TestMigrationsAreOrdered(t *testing.T) {
	versions := db.MigrationStepVersions()
	if len(versions) == 0 {
		t.Fatal("no migrations registered")
	}
	for i := 1; i < len(versions); i++ {
		if versions[i] <= versions[i-1] {
			t.Fatalf("migration versions not strictly increasing at index %d: %v",
				i, versions)
		}
	}
	if last := versions[len(versions)-1]; last != db.CurrentSchemaVersion() {
		t.Fatalf("last migration version = %d, want CurrentSchemaVersion = %d",
			last, db.CurrentSchemaVersion())
	}
}

// contains reports whether haystack contains needle.
func contains(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}

// seedLegacyDB creates a pre-copy-swap schema (no CHECK constraint, has
// parent_event_id self-FK) at user_version=0. Shared by several tests below.
func seedLegacyDB(t *testing.T, path string) {
	t.Helper()
	raw := openRaw(t, path)
	defer raw.Close()
	for _, stmt := range []string{
		`CREATE TABLE sessions (
			session_id TEXT PRIMARY KEY,
			agent_assigned TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			total_events INTEGER DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'active'
		)`,
		`CREATE TABLE features (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			title TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'todo',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE agent_events (
			event_id TEXT PRIMARY KEY,
			agent_id TEXT NOT NULL,
			event_type TEXT NOT NULL,
			timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			tool_name TEXT,
			session_id TEXT NOT NULL,
			feature_id TEXT,
			parent_event_id TEXT,
			FOREIGN KEY (session_id) REFERENCES sessions(session_id),
			FOREIGN KEY (feature_id) REFERENCES features(id),
			FOREIGN KEY (parent_event_id) REFERENCES agent_events(event_id)
		)`,
		`PRAGMA user_version = 0`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("seedLegacyDB %q: %v", stmt, err)
		}
	}
}

// TestCopySwap_TriggerRecreated verifies that trg_increment_total_events exists
// after migrating a legacy DB through the copy-swap step, and that inserting a
// new agent_event correctly increments sessions.total_events.
func TestCopySwap_TriggerRecreated(t *testing.T) {
	path := fileDBPath(t, "trigger_recreated.db")
	seedLegacyDB(t, path)

	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open legacy fixture: %v", err)
	}
	defer database.Close()

	// The trigger must exist in sqlite_master.
	var trigName string
	err = database.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='trigger' AND name='trg_increment_total_events'`,
	).Scan(&trigName)
	if err != nil {
		t.Fatalf("trg_increment_total_events missing after copy-swap migration: %v", err)
	}
	if trigName != "trg_increment_total_events" {
		t.Fatalf("unexpected trigger name: %q", trigName)
	}

	// Seed a session so we can test the trigger fires.
	if _, err := database.Exec(`INSERT INTO sessions (session_id, agent_assigned, total_events, status)
		VALUES ('sess-1', 'test-agent', 0, 'active')`); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	// Insert an agent_event — the trigger should increment total_events.
	if _, err := database.Exec(`INSERT INTO agent_events
		(event_id, agent_id, event_type, session_id)
		VALUES ('ev-1', 'test-agent', 'start', 'sess-1')`); err != nil {
		t.Fatalf("insert agent_event: %v", err)
	}

	var totalEvents int
	if err := database.QueryRow(`SELECT total_events FROM sessions WHERE session_id='sess-1'`).
		Scan(&totalEvents); err != nil {
		t.Fatalf("read total_events: %v", err)
	}
	if totalEvents != 1 {
		t.Fatalf("total_events after insert = %d, want 1 (trigger not firing)", totalEvents)
	}
}

// TestCopySwap_TriggerRecreated_MultipleEvents verifies the trigger keeps
// counting correctly across multiple agent_event inserts.
func TestCopySwap_TriggerRecreated_MultipleEvents(t *testing.T) {
	path := fileDBPath(t, "trigger_multi.db")
	seedLegacyDB(t, path)

	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open legacy fixture: %v", err)
	}
	defer database.Close()

	if _, err := database.Exec(`INSERT INTO sessions (session_id, agent_assigned, total_events, status)
		VALUES ('sess-2', 'agent', 0, 'active')`); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := database.Exec(`INSERT INTO agent_events
			(event_id, agent_id, event_type, session_id)
			VALUES (?, 'agent', 'start', 'sess-2')`,
			fmt.Sprintf("ev-%d", i)); err != nil {
			t.Fatalf("insert event %d: %v", i, err)
		}
	}
	var total int
	if err := database.QueryRow(`SELECT total_events FROM sessions WHERE session_id='sess-2'`).
		Scan(&total); err != nil {
		t.Fatalf("read total_events: %v", err)
	}
	if total != 3 {
		t.Fatalf("total_events = %d, want 3", total)
	}
}

// TestAlterFailure_DoesNotAdvanceVersion verifies that a genuine (non-duplicate)
// ALTER TABLE failure inside a migration causes the step to return an error,
// which prevents user_version from advancing. On the next open the step retries.
//
// We simulate this by opening a DB at user_version=2 (past the initial tables
// step but before step 3 which does the ALTERs), then truncating the sessions
// table entirely so that the ALTER TABLE ADD COLUMN on a non-existent table
// produces a real error — but we instead test the isDuplicateColumnError helper
// and the contract: non-duplicate errors from ALTER are now returned, not swallowed.
func TestAlterFailure_DoesNotAdvanceVersion(t *testing.T) {
	path := fileDBPath(t, "alter_failure.db")

	// Cold open to create a fully migrated DB.
	cold, err := db.Open(path)
	if err != nil {
		t.Fatalf("cold Open: %v", err)
	}
	// Rewind to user_version=2 to force step 3 to re-run on next open.
	if _, err := cold.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatalf("rewind user_version: %v", err)
	}
	// Drop sessions table so the ALTER TABLE ADD COLUMN in step 3 fails with a
	// real error ("no such table: sessions"), not "duplicate column".
	if _, err := cold.Exec("DROP TABLE IF EXISTS sessions"); err != nil {
		t.Fatalf("drop sessions: %v", err)
	}
	cold.Close()

	// Opening now should fail because step 3 hits a real ALTER error on the
	// missing sessions table. user_version must NOT advance past 2.
	failDB, err := db.Open(path)
	if err == nil {
		failDB.Close()
		t.Fatal("Open should have returned an error when ALTER fails on missing table")
	}

	// Verify user_version did NOT advance.
	raw := openRaw(t, path)
	defer raw.Close()
	v := queryUserVersion(t, raw)
	if v != 2 {
		t.Fatalf("user_version after failed ALTER = %d, want 2 (step must not advance on failure)", v)
	}
}

// TestRepairTrigger_AlreadyMigratedDB_TriggerDropped verifies that a database
// already at the pre-fix current schema version (user_version 9) that has lost
// trg_increment_total_events gets the trigger restored by the new step-10
// migration — and that sessions.total_events increments correctly thereafter.
//
// This is the exact upgrade population that bug-045124a6's fix missed: step 4
// only ran for DBs that hadn't yet completed step 4.
func TestRepairTrigger_AlreadyMigratedDB_TriggerDropped(t *testing.T) {
	path := fileDBPath(t, "repair_trigger.db")

	// Cold open: migrate to full current schema (now version 11).
	cold, err := db.Open(path)
	if err != nil {
		t.Fatalf("cold Open: %v", err)
	}

	// Rewind to version 9 (the pre-fix current version) to simulate a DB that
	// ran all migrations up to and including step 9 but not step 10.
	if _, err := cold.Exec("PRAGMA user_version = 9"); err != nil {
		cold.Close()
		t.Fatalf("rewind user_version to 9: %v", err)
	}

	// Drop the trigger to simulate the broken state the old migration left behind.
	if _, err := cold.Exec("DROP TRIGGER IF EXISTS trg_increment_total_events"); err != nil {
		cold.Close()
		t.Fatalf("drop trigger: %v", err)
	}

	// Verify trigger is gone before migration.
	var name string
	err = cold.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='trigger' AND name='trg_increment_total_events'`,
	).Scan(&name)
	if err == nil {
		cold.Close()
		t.Fatal("trigger still present after manual DROP — test setup error")
	}
	cold.Close()

	// Warm open: must run steps 10 and 11 (both needed for repair + backfill).
	recorder := &migrationCallRecorder{}
	db.SetMigrationObserver(recorder.Record)
	defer db.SetMigrationObserver(nil)

	warm, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open after trigger drop: %v", err)
	}
	defer warm.Close()

	// Steps 10, 11 and 12 should have run (trigger repair + total_events
	// backfill + gate_records profile columns).
	calls := recorder.Calls()
	want := []string{"010_repair_trigger_increment_total_events", "011_backfill_total_events", "012_gate_records_profile_signature", "013_arch_cards"}
	if !slices.Equal(calls, want) {
		t.Fatalf("expected steps %v, got %v", want, calls)
	}

	// user_version must be at currentSchemaVersion.
	if v := queryUserVersion(t, warm); v != db.CurrentSchemaVersion() {
		t.Fatalf("user_version after repair = %d, want %d", v, db.CurrentSchemaVersion())
	}

	// Trigger must now exist in sqlite_master.
	var trigName string
	if err := warm.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='trigger' AND name='trg_increment_total_events'`,
	).Scan(&trigName); err != nil {
		t.Fatalf("trigger missing after repair migration: %v", err)
	}

	// Seed a session and insert an agent_event to confirm the trigger fires.
	if _, err := warm.Exec(`INSERT INTO sessions (session_id, agent_assigned, total_events, status)
		VALUES ('sess-repair', 'agent', 0, 'active')`); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := warm.Exec(`INSERT INTO agent_events
		(event_id, agent_id, event_type, session_id)
		VALUES ('ev-repair', 'agent', 'start', 'sess-repair')`); err != nil {
		t.Fatalf("insert agent_event: %v", err)
	}
	var totalEvents int
	if err := warm.QueryRow(`SELECT total_events FROM sessions WHERE session_id='sess-repair'`).
		Scan(&totalEvents); err != nil {
		t.Fatalf("read total_events: %v", err)
	}
	if totalEvents != 1 {
		t.Fatalf("total_events after insert = %d, want 1 (trigger not firing after repair)", totalEvents)
	}
}

// TestRepairTrigger_FreshDB_TriggerIntact verifies that a fresh database
// (migrated end-to-end through all steps) has trg_increment_total_events
// present and functioning after migrations complete. Step 10 is idempotent
// (CREATE TRIGGER IF NOT EXISTS) so it must not break a DB that already has
// the trigger.
func TestRepairTrigger_FreshDB_TriggerIntact(t *testing.T) {
	path := fileDBPath(t, "fresh_trigger.db")

	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open fresh: %v", err)
	}
	defer database.Close()

	// Trigger must exist.
	var trigName string
	if err := database.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='trigger' AND name='trg_increment_total_events'`,
	).Scan(&trigName); err != nil {
		t.Fatalf("trigger missing on fresh DB: %v", err)
	}

	// Confirm it fires correctly.
	if _, err := database.Exec(`INSERT INTO sessions (session_id, agent_assigned, total_events, status)
		VALUES ('sess-fresh', 'agent', 0, 'active')`); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO agent_events
		(event_id, agent_id, event_type, session_id)
		VALUES ('ev-fresh', 'agent', 'start', 'sess-fresh')`); err != nil {
		t.Fatalf("insert agent_event: %v", err)
	}
	var total int
	if err := database.QueryRow(`SELECT total_events FROM sessions WHERE session_id='sess-fresh'`).
		Scan(&total); err != nil {
		t.Fatalf("read total_events: %v", err)
	}
	if total != 1 {
		t.Fatalf("total_events = %d, want 1", total)
	}
}

// TestBackfillTotalEvents_StaleCountsRepaired verifies that step 11
// (011_backfill_total_events) recomputes sessions.total_events from actual
// agent_events rows for sessions whose counts are stale because agent_events
// were inserted while trg_increment_total_events was absent.
//
// Setup: a DB at user_version 10 (step 10 ran, trigger is present) with a
// session that had events recorded BEFORE the trigger existed (so total_events
// is wrong/zero). Step 11 must correct it.
func TestBackfillTotalEvents_StaleCountsRepaired(t *testing.T) {
	path := fileDBPath(t, "backfill_stale.db")

	// Cold open: migrate to version 11.
	cold, err := db.Open(path)
	if err != nil {
		t.Fatalf("cold Open: %v", err)
	}

	// Insert a session and agent_events WITHOUT the trigger firing.
	// Simulate this by dropping the trigger first, inserting, then rewinding
	// user_version to 10 so step 11 runs on next open.
	if _, err := cold.Exec("DROP TRIGGER IF EXISTS trg_increment_total_events"); err != nil {
		cold.Close()
		t.Fatalf("drop trigger: %v", err)
	}
	if _, err := cold.Exec(`INSERT INTO sessions (session_id, agent_assigned, total_events, status)
		VALUES ('sess-stale', 'agent', 0, 'active')`); err != nil {
		cold.Close()
		t.Fatalf("insert session: %v", err)
	}
	for i := 1; i <= 5; i++ {
		if _, err := cold.Exec(`INSERT INTO agent_events
			(event_id, agent_id, event_type, session_id)
			VALUES (?, 'agent', 'start', 'sess-stale')`,
			fmt.Sprintf("ev-stale-%d", i)); err != nil {
			cold.Close()
			t.Fatalf("insert event %d: %v", i, err)
		}
	}
	// Confirm total_events is still 0 (trigger was absent during inserts).
	var preCount int
	if err := cold.QueryRow(`SELECT total_events FROM sessions WHERE session_id='sess-stale'`).
		Scan(&preCount); err != nil {
		cold.Close()
		t.Fatalf("read pre-backfill total_events: %v", err)
	}
	if preCount != 0 {
		cold.Close()
		t.Fatalf("pre-backfill total_events = %d, want 0 (trigger was absent)", preCount)
	}
	// Rewind to version 10 so step 11 runs on next open.
	if _, err := cold.Exec("PRAGMA user_version = 10"); err != nil {
		cold.Close()
		t.Fatalf("rewind user_version to 10: %v", err)
	}
	cold.Close()

	// Warm open: only step 11 should run.
	recorder := &migrationCallRecorder{}
	db.SetMigrationObserver(recorder.Record)
	defer db.SetMigrationObserver(nil)

	warm, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open for backfill: %v", err)
	}
	defer warm.Close()

	// Steps 11, 12 and 13 should have run (backfill + gate_records profile columns + arch_cards).
	calls := recorder.Calls()
	want := []string{"011_backfill_total_events", "012_gate_records_profile_signature", "013_arch_cards"}
	if !slices.Equal(calls, want) {
		t.Fatalf("expected steps %v, got %v", want, calls)
	}

	// total_events must now equal the true event count (5).
	var postCount int
	if err := warm.QueryRow(`SELECT total_events FROM sessions WHERE session_id='sess-stale'`).
		Scan(&postCount); err != nil {
		t.Fatalf("read post-backfill total_events: %v", err)
	}
	if postCount != 5 {
		t.Fatalf("total_events after backfill = %d, want 5", postCount)
	}
}

// TestBackfillTotalEvents_Idempotent verifies that step 11 is idempotent:
// a session whose total_events is already correct is unaffected.
func TestBackfillTotalEvents_Idempotent(t *testing.T) {
	path := fileDBPath(t, "backfill_idem.db")

	// Cold open: migrate to version 11. Trigger is active, so inserting events
	// increments total_events correctly.
	cold, err := db.Open(path)
	if err != nil {
		t.Fatalf("cold Open: %v", err)
	}
	if _, err := cold.Exec(`INSERT INTO sessions (session_id, agent_assigned, total_events, status)
		VALUES ('sess-idem', 'agent', 0, 'active')`); err != nil {
		cold.Close()
		t.Fatalf("insert session: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := cold.Exec(`INSERT INTO agent_events
			(event_id, agent_id, event_type, session_id)
			VALUES (?, 'agent', 'start', 'sess-idem')`,
			fmt.Sprintf("ev-idem-%d", i)); err != nil {
			cold.Close()
			t.Fatalf("insert event %d: %v", i, err)
		}
	}
	// Confirm total_events is correct (3) before re-running backfill.
	var preCount int
	if err := cold.QueryRow(`SELECT total_events FROM sessions WHERE session_id='sess-idem'`).
		Scan(&preCount); err != nil {
		cold.Close()
		t.Fatalf("read pre-idempotent total_events: %v", err)
	}
	if preCount != 3 {
		cold.Close()
		t.Fatalf("pre-idempotent total_events = %d, want 3", preCount)
	}
	// Rewind to version 10 to force step 11 to re-run.
	if _, err := cold.Exec("PRAGMA user_version = 10"); err != nil {
		cold.Close()
		t.Fatalf("rewind user_version: %v", err)
	}
	cold.Close()

	warm, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open for idempotent backfill: %v", err)
	}
	defer warm.Close()

	// total_events must still be 3 after re-run.
	var postCount int
	if err := warm.QueryRow(`SELECT total_events FROM sessions WHERE session_id='sess-idem'`).
		Scan(&postCount); err != nil {
		t.Fatalf("read post-idempotent total_events: %v", err)
	}
	if postCount != 3 {
		t.Fatalf("total_events after idempotent backfill = %d, want 3", postCount)
	}
}

// TestBackfillTotalEvents_FreshDB_Unaffected verifies that a fresh database
// with no sessions is not affected by step 11 (zero rows updated, no error).
func TestBackfillTotalEvents_FreshDB_Unaffected(t *testing.T) {
	path := fileDBPath(t, "backfill_fresh.db")
	cold, err := db.Open(path)
	if err != nil {
		t.Fatalf("cold Open: %v", err)
	}
	// Rewind to force step 11.
	if _, err := cold.Exec("PRAGMA user_version = 10"); err != nil {
		cold.Close()
		t.Fatalf("rewind user_version: %v", err)
	}
	cold.Close()

	warm, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open on empty DB: %v", err)
	}
	defer warm.Close()

	// No sessions — just confirm Open succeeded and version is correct.
	if v := queryUserVersion(t, warm); v != db.CurrentSchemaVersion() {
		t.Fatalf("user_version = %d, want %d", v, db.CurrentSchemaVersion())
	}
}

// TestDuplicateColumnTreatedAsApplied verifies that "duplicate column name"
// errors from ALTER TABLE ADD COLUMN are silently treated as already-applied
// (idempotent), not surfaced as migration failures.
func TestDuplicateColumnTreatedAsApplied(t *testing.T) {
	path := fileDBPath(t, "dup_col.db")

	// A fully-migrated DB: all columns already exist, so re-running step 3
	// will hit "duplicate column name" on every ALTER. Rewind to user_version=2
	// to force step 3 to re-execute.
	cold, err := db.Open(path)
	if err != nil {
		t.Fatalf("cold Open: %v", err)
	}
	if _, err := cold.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatalf("rewind user_version: %v", err)
	}
	cold.Close()

	// Step 3 will attempt to ADD COLUMN on tables that already have those
	// columns. All errors should be "duplicate column name" and treated as
	// idempotent — Open must succeed and land at currentSchemaVersion.
	warm, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open after rewind: %v", err)
	}
	defer warm.Close()

	if v := queryUserVersion(t, warm); v != db.CurrentSchemaVersion() {
		t.Fatalf("user_version after dup-column rerun = %d, want %d",
			v, db.CurrentSchemaVersion())
	}
}

