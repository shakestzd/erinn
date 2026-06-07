package db

// bug-dd5db2d1 white-box regression (package db, NOT db_test): proves the
// open-time `PRAGMA journal_mode` SET inside ApplyPragmas is driven through
// RetryOnBusy and correctly drains+closes the pragma's result row on EVERY
// attempt — including a retried attempt after a genuine, non-waitable
// SQLITE_BUSY.
//
// Why white-box: a single-process test cannot reliably reproduce the
// MULTI-PROCESS lock saturation that exhausts busy_timeout in production
// (in-process, SQLite's busy handler waits out the churn within the 5s
// budget). bug-56b686aa's busy_timeout-first ordering is exactly why a timing
// race is no longer the surface. The DURABLE regression signal is therefore
// the CODE PATH: the journal_mode SET must (a) go through RetryOnBusy so a
// non-waitable BUSY is retried rather than surfaced, and (b) re-open and drain
// the result-returning pragma's rows on each attempt without leaking the prior
// attempt's handle. This test asserts both by injecting an immediate BUSY via
// a real driver lock topology and counting attempts through the busySleep seam
// (the same internal seam busy_retry_test.go uses).

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestApplyPragmas_JournalModeSET_RetriesNonWaitableBusy forces the genuine
// non-waitable BUSY that busy_timeout does NOT absorb (the SHARED→RESERVED
// deadlock-avoidance path: one connection parks a SHARED read lock while
// another holds RESERVED, so the journal_mode lock-upgrade returns SQLITE_BUSY
// IMMEDIATELY without consulting the busy handler). It proves ApplyPragmas
// retries through it and the result row is handled correctly each attempt.
func TestApplyPragmas_JournalModeSET_RetriesNonWaitableBusy(t *testing.T) {
	ResetBusyCounters()

	// Real temp-file DB. Bootstrap the FILE into WAL so that, on a host whose
	// existing selection logic chooses DELETE (overlayfs/FUSE devcontainer —
	// isUnsafeForMmap true), the current on-disk mode (wal) DIFFERS from the
	// selected target. That difference is what makes ApplyPragmas actually
	// EXECUTE the in-scope `PRAGMA journal_mode = <selected>` write instead of
	// taking bug-56b686aa's query-before-set fast path.
	//
	// SCOPE: mode SELECTION is NOT changed. The test drives ApplyPragmas with
	// the UNMODIFIED real BuildPragmas(dbPath) output, so whatever
	// isUnsafeForMmap/BuildPragmas chooses for this path is exactly what gets
	// written — the fix only makes that WRITE resilient, never picks a mode.
	// If the host filesystem cannot hold WAL, or the selected mode already
	// equals the file mode, the SET would be skipped and the contention path
	// unexercised — detect that and Skip rather than assert a false negative.
	dbPath := filepath.Join(t.TempDir(), "whitebox.db")
	bootstrap, err := Open(dbPath)
	if err != nil {
		t.Fatalf("bootstrap Open: %v", err)
	}
	if _, err := bootstrap.Exec("PRAGMA journal_mode = wal"); err != nil {
		bootstrap.Close()
		t.Fatalf("bootstrap journal_mode set: %v", err)
	}
	fileMode := QueryJournalMode(bootstrap)
	bootstrap.Close()

	// The mode ApplyPragmas WILL write is whatever the existing, unchanged
	// selection logic chooses — read from the real BuildPragmas, not overridden.
	contendPragmas := BuildPragmas(dbPath)
	selected := contendPragmas["journal_mode"]
	if strings.EqualFold(fileMode, selected) {
		t.Skipf("file journal_mode %q already equals selected %q on this host "+
			"— ApplyPragmas would take the query-before-set fast path and the "+
			"journal_mode WRITE (the bug-dd5db2d1 surface) would not execute",
			fileMode, selected)
	}

	// Connection A: open a streaming read cursor and hold it undrained ->
	// live SHARED lock.
	connA, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("connA open: %v", err)
	}
	connA.SetMaxOpenConns(1)
	if _, err := connA.Exec(`CREATE TABLE IF NOT EXISTS _wb (id INTEGER)`); err != nil {
		t.Fatalf("connA create: %v", err)
	}
	for i := 0; i < 50; i++ {
		if _, err := connA.Exec(`INSERT INTO _wb (id) VALUES (?)`, i); err != nil {
			t.Fatalf("connA seed: %v", err)
		}
	}
	pinned, err := connA.Conn(context.Background())
	if err != nil {
		t.Fatalf("connA pinned conn: %v", err)
	}
	rows, err := pinned.QueryContext(context.Background(), `SELECT id FROM _wb`)
	if err != nil {
		t.Fatalf("connA streaming query: %v", err)
	}
	rows.Next() // materialize cursor -> hold SHARED

	// Connection B: hold RESERVED continuously (BEGIN IMMEDIATE). With A's
	// SHARED parked and B's RESERVED held, a journal_mode SET on a third
	// connection gets immediate, non-waitable SQLITE_BUSY.
	connB, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("connB open: %v", err)
	}
	connB.SetMaxOpenConns(1)
	if _, err := connB.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("connB BEGIN IMMEDIATE: %v", err)
	}

	// Single sync.Once-guarded release: fully drop BOTH contending handles
	// exactly once. The seam invokes it mid-flight (so the retried attempt
	// succeeds); a t.Cleanup fallback invokes it if the seam never fired
	// (e.g. no BUSY observed) so handles are never leaked AND never
	// double-closed (the Once makes it idempotent — no harmless double-close).
	// Closing the whole *sql.DB (not just the pinned conn) guarantees the
	// OS-level SQLite file locks are released before the retried attempt runs.
	var releaseOnce sync.Once
	releaseContention := func() {
		releaseOnce.Do(func() {
			rows.Close()
			pinned.Close()
			connA.Close()
			_, _ = connB.Exec("ROLLBACK")
			connB.Close()
		})
	}
	t.Cleanup(releaseContention)

	// Instrument the shared backoff seam so we can both count retry attempts
	// AND release the contention mid-flight (on the first backoff sleep) so
	// the retried attempt deterministically succeeds.
	origSleep := busySleep
	var sleeps int
	busySleep = func(d time.Duration) {
		sleeps++
		releaseContention() // first backoff drops the locks (idempotent)
		// Do not actually sleep the full backoff in the test.
	}
	t.Cleanup(func() { busySleep = origSleep })

	// Drive ApplyPragmas exactly as db.Open does: a fresh handle, then the
	// pragma application. The journal_mode SET must hit the immediate BUSY,
	// invoke the busySleep seam (>=1 retry), release the locks, and succeed.
	target, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("target open: %v", err)
	}
	defer target.Close()

	applyErr := ApplyPragmas(target, contendPragmas)

	if applyErr != nil {
		if strings.Contains(applyErr.Error(),
			"applying PRAGMA journal_mode: database is locked") {
			t.Fatalf("ApplyPragmas surfaced the forbidden journal_mode lock "+
				"error (bug-dd5db2d1 regression — SET not retried): %v", applyErr)
		}
		t.Fatalf("ApplyPragmas failed under non-waitable BUSY: %v", applyErr)
	}
	if sleeps == 0 {
		t.Fatal("RetryOnBusy backoff seam never fired — the journal_mode SET " +
			"did not actually retry; the test did not exercise the wrap " +
			"(non-waitable BUSY was not reproduced)")
	}

	// Result-row handling: after a successful (retried) journal_mode SET the
	// pinned pragma connection must be clean — a follow-up query must work,
	// proving the prior attempt's result rows were drained+closed and not
	// leaked across the retry.
	jm := QueryJournalMode(target)
	if !strings.EqualFold(jm, selected) {
		t.Fatalf("journal_mode = %q after retried SET, want %q "+
			"(the SELECTED mode must apply unchanged; result row mishandled?)",
			jm, selected)
	}

	// FirstPartyBusyTotal must be 0: ApplyPragmas's wrap absorbs transient
	// BUSY transparently and never calls Record, so the launch-gate counter
	// stays consistent.
	if total := FirstPartyBusyTotal(); total != 0 {
		t.Fatalf("FirstPartyBusyTotal = %d, want 0 "+
			"(open-time pragma retry must not leak into the first-party "+
			"launch-gate counter)", total)
	}
}

// DESIGN NOTE — why there is no separate "concurrent" regression test.
//
// roborev job 3237 correctly flagged the prior concurrent-churn test as
// VACUOUS: on a DELETE-selecting host (this overlayfs devcontainer) the seed
// DB was already DELETE and the explicit target was DELETE, so bug-56b686aa's
// query-before-set fast path SKIPPED the `PRAGMA journal_mode = DELETE` write
// entirely — RetryOnBusy was never exercised yet the test "passed".
//
// Two rewrites were attempted to make a concurrent test non-vacuous, and both
// were rejected on evidence gathered THIS session:
//
//   - Continuous-churn writer + WAL-seed (current!=target) + seam assertion:
//     FAILED its own non-vacuity guard. SQLite's busy_timeout=5000ms on the
//     pinned pragma connection internally waits the churn out, so the seam
//     never fires. A churn test asserting the seam fired is inherently
//     UNSATISFIABLE in-process.
//   - Non-waitable SHARED+RESERVED topology + N concurrent ApplyPragmas +
//     seam assertion: passed in isolation but FLAKED under full-suite load
//     (the first goroutine to hit BUSY releases the locks via the seam, so
//     remaining goroutines find no contention; under scheduler load a run can
//     have zero goroutines hit the non-waitable window before busy_timeout
//     absorbs it, tripping the non-vacuity guard nondeterministically).
//
// Conclusion: a test that is BOTH concurrent AND deterministically
// non-vacuous is not achievable in-process for this bug. The single-call
// TestApplyPragmas_JournalModeSET_RetriesNonWaitableBusy above IS the
// deterministic, non-vacuous regression test — it seeds current!=target
// (WAL->DELETE so the SET executes, skipping cleanly if the host rejects
// WAL), reproduces the genuine non-waitable SQLITE_BUSY, and ASSERTS the
// busySleep seam fired (sleeps==0 -> t.Fatal), so it can never pass without
// genuinely exercising the RetryOnBusy wrap. It is verified stable across
// repeated and full-suite runs. Adding a flaky concurrent sibling would be a
// net regression in suite reliability, not added coverage.
