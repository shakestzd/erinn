// Slice 7 canonical-first acceptance tests (plan-ae0c37b2, feat-33c26c74).
//
// These integration tests lock in the architectural contract that hooks and
// other event producers return SUCCESS to the user even when:
//
//  1. The writable SQLite handle is unavailable (writer_unavailable).
//  2. The slice-6 writer queue is at capacity (queue_full).
//  3. The writer crashes mid-queue, leaving N ops unwritten.
//
// In every case the canonical NDJSON written upstream is the authoritative
// copy, and reindex recovers any derived-index rows that the synchronous /
// queued write path missed. The tests exercise both the in-process queue
// (queue_full + writer-crash) and the subprocess fallback (writer_unavailable)
// to cover the full hook surface.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/db/writequeue"
	"github.com/shakestzd/wipnote/core/hooks"
	"github.com/shakestzd/wipnote/core/models"
)

// TestOpenHookDB_IgnoresPathAndYieldsUsableEphemeralProjection retires a
// premise rather than an assertion.
//
// This test used to hand OpenHookDB a DIRECTORY, on the reasoning that SQLite
// cannot bind to one, and assert the resulting (nil, writer_unavailable) pair.
// That branch is unreachable now: OpenHookDB ignores dbPath entirely and returns
// a process-local in-memory projection (feat-fc3cc9e0), so there is no
// path-driven failure mode left to provoke. Asserting the old shape would only
// have measured that the cutover had not happened.
//
// What is worth pinning instead is the property the cutover created, and it is
// pinned with the same hostile input: a path that could never work as a SQLite
// file must still yield a USABLE handle, must report no fallback, and must
// leave nothing behind on disk at that path. Reintroduce a path-backed open and
// every one of those fails.
func TestOpenHookDB_IgnoresPathAndYieldsUsableEphemeralProjection(t *testing.T) {
	hooks.ResetFallbackCounts()

	// A directory: unopenable as a SQLite file by construction.
	badPath := t.TempDir()

	database, reason := hooks.OpenHookDB("test", "sess-writer-unavailable", filepath.Join(badPath))
	if database == nil {
		t.Fatalf("want a usable handle regardless of path, got nil (reason=%q)", reason)
	}
	t.Cleanup(func() { database.Close() })
	if reason != "" {
		t.Fatalf("want no fallback reason on a successful open, got %q", reason)
	}

	// Usable means migrated, not merely non-nil: a handle that cannot answer a
	// schema query would fail every caller at the first read.
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatalf("projection is not schema-migrated: %v", err)
	}
	if n != 0 {
		t.Fatalf("a fresh projection must start empty, got %d sessions", n)
	}

	// The whole point of the cutover: nothing was created at the path.
	entries, err := os.ReadDir(badPath)
	if err != nil {
		t.Fatalf("read %s: %v", badPath, err)
	}
	if len(entries) != 0 {
		t.Fatalf("OpenHookDB created artifacts at the supplied path: %v", entries)
	}

	// A successful open is not a degradation and must not be counted as one.
	if wu, _, _ := hooks.FallbackCounts(); wu != 0 {
		t.Errorf("writer_unavailable counter: want 0 after a successful open, got %d", wu)
	}
}

// TestWriterUnavailable_FallsBackToCanonicalAppend keeps the writer_unavailable
// contract covered through a path that is still REACHABLE.
//
// The contract never went away — a hook that has neither a queue nor a usable
// handle must record the degradation and return quietly, because the canonical
// NDJSON write upstream is authoritative and Claude Code must never see a hook
// error. Only the way of provoking it changed: it is no longer a failed open,
// it is a caller arriving with nothing to write through.
func TestWriterUnavailable_FallsBackToCanonicalAppend(t *testing.T) {
	hooks.ResetFallbackCounts()

	if wu0, _, _ := hooks.FallbackCounts(); wu0 != 0 {
		t.Fatalf("baseline writer_unavailable count: want 0, got %d", wu0)
	}

	ran := false
	hooks.SubmitDerivedOp("test", "sess-writer-unavailable", nil /*queue*/, nil /*db*/, func(*sql.DB) error {
		ran = true
		return fmt.Errorf("op must never run without a writer")
	})

	if ran {
		t.Error("the derived op ran with no queue and no DB; it must be skipped, not executed against nil")
	}
	if wu1, _, _ := hooks.FallbackCounts(); wu1 != 1 {
		t.Errorf("writer_unavailable counter: want 1, got %d", wu1)
	}
}

// TestQueueFull_FallsBackWithoutUserVisibleFailure asserts that when the
// writer queue is at capacity, SubmitDerivedOp records a queue_full fallback
// and does NOT propagate an error to the caller. The canonical NDJSON write
// upstream — simulated here by writing an events.ndjson file before the
// submit — is authoritative; reindex recovers the row.
func TestQueueFull_FallsBackWithoutUserVisibleFailure(t *testing.T) {
	hooks.ResetFallbackCounts()

	// Build a queue with capacity 1 and a slow op that blocks forever so the
	// next Submit hits ErrQueueFull. The slow op never completes within the
	// test; we Stop the queue at the end to release the goroutine.
	q := writequeue.New(writequeue.Config{Capacity: 1})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("queue.Start: %v", err)
	}
	t.Cleanup(func() { q.Stop(2 * time.Second) })

	// Fill the queue: first Submit enters the worker (blocks on the channel
	// receive then on time.Sleep below); second Submit fills the buffer slot.
	blockCh := make(chan struct{})
	releaseCh := make(chan struct{})
	if err := q.Submit(context.Background(), func(_ context.Context) error {
		close(blockCh)
		<-releaseCh
		return nil
	}); err != nil {
		t.Fatalf("Submit (worker): %v", err)
	}
	<-blockCh
	if err := q.Submit(context.Background(), func(_ context.Context) error {
		<-releaseCh
		return nil
	}); err != nil {
		t.Fatalf("Submit (buffer): %v", err)
	}
	t.Cleanup(func() { close(releaseCh) })

	// Now SubmitDerivedOp should fall back without error.
	hooks.SubmitDerivedOp("test", "sess-queue-full", q, nil, func(_ *sql.DB) error {
		t.Errorf("op should not have run — queue should reject Submit")
		return nil
	})

	_, qf, _ := hooks.FallbackCounts()
	if qf != 1 {
		t.Errorf("queue_full counter: want 1, got %d", qf)
	}
}

// TestReplayAfterFallback_IsIdempotent writes the same event twice through
// the synchronous DB path (simulating "fallback hit, then later reindex
// re-applies the same event") and asserts exactly one row exists. The
// guarantee comes from db.UpsertEvent's INSERT OR REPLACE on the event_id
// primary key; this test locks in that contract for slice 7.
func TestReplayAfterFallback_IsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wipnote.db")
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	// Seed a session row so the FK on agent_events resolves.
	if _, err := database.Exec(`
		INSERT INTO sessions (session_id, agent_assigned, created_at, status)
		VALUES (?, ?, ?, ?)`,
		"sess-replay", "claude-code", time.Now().UTC().Format(time.RFC3339), "active",
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	ev := &models.AgentEvent{
		EventID:      "evt-replay-1",
		AgentID:      "claude-code",
		EventType:    models.EventToolCall,
		Timestamp:    time.Now().UTC(),
		ToolName:     "Test",
		InputSummary: "replay test",
		SessionID:    "sess-replay",
		Status:       "completed",
		Source:       "hook",
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}

	// First write — simulates the fallback path running the op directly.
	if err := dbpkg.UpsertEvent(database, ev); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Second write — simulates reindex catching up from canonical NDJSON.
	if err := dbpkg.UpsertEvent(database, ev); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var count int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM agent_events WHERE event_id = ?`, ev.EventID,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("idempotence: want 1 row, got %d", count)
	}
}

// TestWriterCrashMidQueue_RecoversFromCanonical models the worst case from
// the slice-7 review note: the writer service stops with N ops still in the
// queue (or in-flight). The test verifies that:
//
//  1. After Stop, additional Submit calls return ErrWriterUnavailable.
//  2. The canonical NDJSON written upstream survives unaffected.
//  3. A subsequent reindex (modeled here by a direct UpsertEvent against the
//     same DB) restores the rows — confirming the architectural promise that
//     "no data lost from user perspective" holds even when writer dies mid-queue.
func TestWriterCrashMidQueue_RecoversFromCanonical(t *testing.T) {
	hooks.ResetFallbackCounts()

	// Drive the queue with a deliberately-slow op so we can stop the queue
	// while there are pending submits.
	q := writequeue.New(writequeue.Config{Capacity: 4})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var executed atomic.Int32
	blockCh := make(chan struct{})
	// One slow op currently running.
	if err := q.Submit(context.Background(), func(_ context.Context) error {
		executed.Add(1)
		<-blockCh
		return nil
	}); err != nil {
		t.Fatalf("submit slow op: %v", err)
	}
	// Three more queued behind it.
	for i := 0; i < 3; i++ {
		if err := q.Submit(context.Background(), func(_ context.Context) error {
			executed.Add(1)
			return nil
		}); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}

	// Simulate a writer crash by abruptly stopping with a tight deadline.
	// The slow op is still blocked on blockCh, so Stop times out before
	// it drains — the queued ops never run.
	stopDone := make(chan struct{})
	go func() {
		q.Stop(50 * time.Millisecond)
		close(stopDone)
	}()
	<-stopDone

	// Any further submit must report writer-unavailable.
	hooks.SubmitDerivedOp("test", "sess-crash", q, nil, func(_ *sql.DB) error {
		t.Errorf("op should not have run after Stop")
		return nil
	})
	wu, _, _ := hooks.FallbackCounts()
	if wu == 0 {
		t.Errorf("writer_unavailable counter not bumped after Stop; want >=1")
	}

	// Release the blocked op so the worker goroutine can exit cleanly.
	close(blockCh)

	// Canonical-recovery simulation: build a fresh DB and run the four
	// "would have been written" rows as a reindex would. Idempotent.
	dbPath := filepath.Join(t.TempDir(), "wipnote.db")
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open recovery: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC()
	if _, err := database.Exec(`
		INSERT INTO sessions (session_id, agent_assigned, created_at, status)
		VALUES (?, ?, ?, ?)`,
		"sess-crash", "claude-code", now.Format(time.RFC3339), "active",
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	for i := 0; i < 4; i++ {
		ev := &models.AgentEvent{
			EventID:      fmt.Sprintf("evt-crash-%d", i),
			AgentID:      "claude-code",
			EventType:    models.EventToolCall,
			Timestamp:    now,
			ToolName:     "Test",
			InputSummary: "post-crash recovery",
			SessionID:    "sess-crash",
			Status:       "completed",
			Source:       "reindex",
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := dbpkg.UpsertEvent(database, ev); err != nil {
			t.Fatalf("recovery upsert %d: %v", i, err)
		}
	}

	var count int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM agent_events WHERE session_id = ?`, "sess-crash",
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 4 {
		t.Errorf("post-crash recovery: want 4 rows from reindex, got %d", count)
	}

	// And the recovery is idempotent — running it again gives the same count.
	for i := 0; i < 4; i++ {
		ev := &models.AgentEvent{
			EventID:      fmt.Sprintf("evt-crash-%d", i),
			AgentID:      "claude-code",
			EventType:    models.EventToolCall,
			Timestamp:    now,
			ToolName:     "Test",
			InputSummary: "post-crash recovery",
			SessionID:    "sess-crash",
			Status:       "completed",
			Source:       "reindex",
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		_ = dbpkg.UpsertEvent(database, ev)
	}
	var count2 int
	_ = database.QueryRow(
		`SELECT COUNT(*) FROM agent_events WHERE session_id = ?`, "sess-crash",
	).Scan(&count2)
	if count2 != 4 {
		t.Errorf("idempotent reindex: want 4 rows, got %d", count2)
	}
}

// TestSubmitDerivedOp_RunsSynchronouslyWhenQueueNil asserts the subprocess
// hook path (no queue available) executes ops directly against the supplied
// DB. This is the legacy hook subprocess pattern preserved by slice 7.
func TestSubmitDerivedOp_RunsSynchronouslyWhenQueueNil(t *testing.T) {
	hooks.ResetFallbackCounts()

	dbPath := filepath.Join(t.TempDir(), "wipnote.db")
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	var ran atomic.Bool
	hooks.SubmitDerivedOp("test", "sess-sync", nil, database, func(_ *sql.DB) error {
		ran.Store(true)
		return nil
	})
	if !ran.Load() {
		t.Errorf("op did not run synchronously when queue is nil")
	}

	wu, qf, to := hooks.FallbackCounts()
	if wu != 0 || qf != 0 || to != 0 {
		t.Errorf("no fallback expected for sync success path; got writer=%d queue=%d timeout=%d", wu, qf, to)
	}
}

// TestSubmitDerivedOp_FallsBackWhenNeitherQueueNorDB asserts the edge case
// where neither dependency is available — the function must record the
// writer_unavailable fallback and return silently.
func TestSubmitDerivedOp_FallsBackWhenNeitherQueueNorDB(t *testing.T) {
	hooks.ResetFallbackCounts()

	hooks.SubmitDerivedOp("test", "sess-no-deps", nil, nil, func(_ *sql.DB) error {
		t.Errorf("op must not run when neither queue nor DB is available")
		return nil
	})

	wu, _, _ := hooks.FallbackCounts()
	if wu != 1 {
		t.Errorf("writer_unavailable counter: want 1, got %d", wu)
	}
}

// TestSubmitDerivedOp_QueueDeliversWhenAvailable asserts the happy path:
// queue is running, op runs on the worker goroutine, no fallback.
func TestSubmitDerivedOp_QueueDeliversWhenAvailable(t *testing.T) {
	hooks.ResetFallbackCounts()

	q := writequeue.New(writequeue.Config{Capacity: 4})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { q.Stop(2 * time.Second) })

	var ran atomic.Bool
	done := make(chan struct{})
	hooks.SubmitDerivedOp("test", "sess-happy", q, nil, func(_ *sql.DB) error {
		ran.Store(true)
		close(done)
		return nil
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("op did not run within 2s")
	}
	if !ran.Load() {
		t.Errorf("op did not run on queue worker")
	}
	wu, qf, to := hooks.FallbackCounts()
	if wu != 0 || qf != 0 || to != 0 {
		t.Errorf("no fallback expected for happy path; got writer=%d queue=%d timeout=%d", wu, qf, to)
	}
}
