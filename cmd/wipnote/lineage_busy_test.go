package main

// bug-7dbaf552: SQLITE_BUSY in general CLI read paths.
//
// bug-74a7bda7 hardened only the completion + hook writers and the dashboard
// read pool. The general CLI read surface (`wipnote lineage`, which walks
// graph_edges via bfsWalk) had no RetryOnBusy after open, so a transient
// SQLITE_BUSY from a concurrent writer surfaced as a hard
// "query neighbors of <id>: database is locked" failure to the user.
//
// This regression test proves the user-visible path is fixed: a bfsWalk-style
// read against a write-contended DB returns NO SQLITE_BUSY to the caller (it
// succeeds after the RetryOnBusy backoff) under BOTH WAL and DELETE journal
// modes. The journal mode is injected directly via newJournalModeDB (shared
// with busy_filesystem_agnostic_test.go) — isUnsafeForMmap / pragmas.go are
// correct and out of scope.
//
// CRITICAL retry-boundary coverage: the bfsWalk retry unit is the per-hop
// db.Query ONLY (never the BFS iteration), and any *sql.Rows from a failed
// attempt is closed before the next attempt so no read lock leaks. This test
// drives a contended workload long enough that, under DELETE journal mode, at
// least one neighbour query hits the SHARED->RESERVED race that busy_timeout
// alone does not fully absorb — exactly the failure the wrapper must hide.

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
)

// seedLineageGraph inserts a small graph_edges chain rooted at root so a
// forward bfsWalk has real neighbours to traverse across multiple hops.
// Uses a writable handle that is closed before returning so it does not
// itself contend with the test's reader/writer goroutines.
func seedLineageGraph(t *testing.T, dbPath, root string) {
	t.Helper()
	w, err := db.OpenWritable(dbPath)
	if err != nil {
		t.Fatalf("OpenWritable seed: %v", err)
	}
	defer w.Close()
	// root -> child-1 -> child-2 (two hops; exercises the BFS queue, not just
	// a single query).
	edges := []struct{ from, to string }{
		{root, "feat-busy-child-1"},
		{"feat-busy-child-1", "feat-busy-child-2"},
	}
	for i, e := range edges {
		if _, err := w.Exec(
			`INSERT INTO graph_edges
				(edge_id, from_node_id, from_node_type, to_node_id, to_node_type, relationship_type)
			 VALUES (?, ?, 'feature', ?, 'feature', ?)`,
			fmt.Sprintf("edge-busy-%d", i), e.from, e.to, "relates_to",
		); err != nil {
			t.Fatalf("seed graph_edges: %v", err)
		}
	}
}

// holdWriteLock spins a goroutine that repeatedly takes the writer lock
// (BEGIN → tiny write → COMMIT) until ctx is cancelled. Under DELETE journal
// mode this is the contention source that drives a concurrent reader's
// SHARED-lock acquisition into the transient SQLITE_BUSY that busy_timeout
// alone does not always absorb — i.e. the exact condition bfsWalk's
// RetryOnBusy wrapper must hide from the caller.
//
// roborev followup (LOW): the writer no longer fails silently. A failed
// OpenWritable / Begin / Commit is reported to the test via t.Errorf (guarded
// by mu so it is goroutine-safe), and every COMMITTED cycle increments
// *cycles so the caller can assert the contention workload actually ran (a
// zero-cycle writer would make the no-BUSY assertion vacuous).
func holdWriteLock(
	ctx context.Context,
	t *testing.T,
	dbPath string,
	wg *sync.WaitGroup,
	mu *sync.Mutex,
	cycles *atomic.Int64,
) {
	t.Helper()
	reportf := func(format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		t.Errorf(format, a...)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		w, err := db.OpenWritable(dbPath)
		if err != nil {
			reportf("holdWriteLock: OpenWritable setup failed (contention "+
				"workload would not run): %v", err)
			return
		}
		defer w.Close()
		n := 0
		for ctx.Err() == nil {
			tx, err := w.Begin()
			if err != nil {
				// A transient BUSY on Begin is expected under DELETE journal
				// contention and is retried by the loop; only a non-BUSY
				// Begin failure is a real setup defect worth surfacing.
				if !db.IsBusyError(err) {
					reportf("holdWriteLock: Begin failed (non-BUSY): %v", err)
				}
				continue
			}
			if _, err := tx.Exec(
				`INSERT OR REPLACE INTO metadata (key, value) VALUES ('busy-probe', ?)`,
				fmt.Sprintf("%d", n)); err != nil {
				_ = tx.Rollback()
				if !db.IsBusyError(err) {
					reportf("holdWriteLock: tx.Exec failed (non-BUSY): %v", err)
				}
				continue
			}
			n++
			// Hold the RESERVED lock briefly so overlapping readers must wait.
			time.Sleep(2 * time.Millisecond)
			if err := tx.Commit(); err != nil {
				if !db.IsBusyError(err) {
					reportf("holdWriteLock: Commit failed (non-BUSY): %v", err)
				}
				continue
			}
			cycles.Add(1)
		}
	}()
}

// TestLineageBfsWalk_NoBusyUnderContention is the bug-7dbaf552 regression
// gate. For BOTH journal modes it hammers the DB with a writer-lock holder
// while repeatedly running forwardWalk (→ bfsWalk → db.Query per hop). It
// asserts every walk succeeds (no "database is locked" surfaced to the
// caller), every walk returns the seeded 2-hop chain, and the first-party
// BUSY counter stays 0 (transient busy absorbed by RetryOnBusy must never
// leak to the launch gate).
func TestLineageBfsWalk_NoBusyUnderContention(t *testing.T) {
	for _, mode := range []string{"wal", "delete"} {
		t.Run(mode, func(t *testing.T) {
			db.ResetBusyCounters()
			dbPath := newJournalModeDB(t, mode)
			const root = "feat-busy-root"
			seedLineageGraph(t, dbPath, root)

			// Read-only handle: exactly how `wipnote lineage` opens the DB
			// (openReadOnlyDB → dbpkg.OpenReadOnly). This is the user-visible
			// path the bug report names.
			ro, err := db.OpenReadOnly(dbPath)
			if err != nil {
				t.Fatalf("OpenReadOnly: %v", err)
			}
			defer ro.Close()

			ctx, cancel := context.WithCancel(context.Background())
			var writers sync.WaitGroup
			var reportMu sync.Mutex
			var writeCycles atomic.Int64
			holdWriteLock(ctx, t, dbPath, &writers, &reportMu, &writeCycles)
			holdWriteLock(ctx, t, dbPath, &writers, &reportMu, &writeCycles)

			// Run many bfsWalk passes against the contended DB. Each pass is
			// the production code path (forwardWalk → bfsWalk). The retry
			// boundary under test is the per-hop db.Query inside bfsWalk.
			deadline := time.Now().Add(3 * time.Second)
			passes := 0
			for time.Now().Before(deadline) {
				nodes, walkErr := forwardWalk(ro, root, allLineageRels, 5)
				if walkErr != nil {
					cancel()
					writers.Wait()
					t.Fatalf("[%s] forwardWalk surfaced an error to the "+
						"caller (RetryOnBusy must absorb transient BUSY): %v",
						mode, walkErr)
				}
				if len(nodes) != 2 {
					cancel()
					writers.Wait()
					t.Fatalf("[%s] forwardWalk returned %d nodes, want 2 "+
						"(seeded 2-hop chain) — a swallowed BUSY would "+
						"truncate the walk", mode, len(nodes))
				}
				passes++
			}
			cancel()
			writers.Wait()

			if passes == 0 {
				t.Fatalf("[%s] no bfsWalk passes ran — workload didn't "+
					"exercise the path", mode)
			}
			// roborev followup (LOW): assert the writer goroutines actually
			// completed at least one BEGIN→write→COMMIT cycle. Without this a
			// silently-broken writer (e.g. setup failure) would make the
			// no-BUSY assertion vacuously green — the test would "pass"
			// without ever exercising lock contention.
			if c := writeCycles.Load(); c < 1 {
				t.Fatalf("[%s] writer goroutines completed %d write-lock "+
					"cycles, want >=1 — contention workload did not run, so "+
					"the no-BUSY assertion would be vacuous", mode, c)
			}
			if total := db.FirstPartyBusyTotal(); total != 0 {
				t.Fatalf("[%s] FirstPartyBusyTotal = %d, want 0 "+
					"(transient BUSY must be absorbed by bfsWalk's "+
					"RetryOnBusy, not leaked to the launch gate)", mode, total)
			}
			t.Logf("[%s] %d contended bfsWalk passes, %d writer cycles, "+
				"zero BUSY surfaced", mode, passes, writeCycles.Load())
		})
	}
}

// TestLineageBfsWalk_RetryBoundaryClosesRows is a focused assertion on the
// retry-boundary lock hygiene that is the riskiest part of the fix: a
// *sql.Rows from a BUSY attempt MUST be closed before the next attempt or a
// read lock leaks and worsens the very contention. We can't easily force a
// BUSY mid-walk deterministically, so instead we assert the structural
// invariant: after a fully successful contended walk, a subsequent writer
// can still immediately take the RESERVED lock (proving bfsWalk left no
// dangling read transaction open).
func TestLineageBfsWalk_RetryBoundaryClosesRows(t *testing.T) {
	dbPath := newJournalModeDB(t, "delete")
	const root = "feat-busy-root"
	seedLineageGraph(t, dbPath, root)

	ro, err := db.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()

	if _, err := forwardWalk(ro, root, allLineageRels, 5); err != nil {
		t.Fatalf("forwardWalk: %v", err)
	}

	// If bfsWalk leaked an open *sql.Rows (read lock), a writer taking the
	// RESERVED lock would block until busy_timeout (5s) and likely fail.
	// With correct close-before-return discipline this is immediate.
	w, err := db.OpenWritable(dbPath)
	if err != nil {
		t.Fatalf("OpenWritable: %v", err)
	}
	defer w.Close()
	start := time.Now()
	if _, err := w.Exec(
		`INSERT OR REPLACE INTO metadata (key, value) VALUES ('post-walk', '1')`,
	); err != nil {
		t.Fatalf("writer blocked after bfsWalk (leaked read lock?): %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("writer took %v after bfsWalk — bfsWalk left a read "+
			"lock open (retry-boundary rows.Close discipline broken)",
			elapsed)
	}
}
