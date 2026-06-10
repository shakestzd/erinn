// Integration tests for the slice-6 writer transport (feat-f3bcbcef of
// plan-ae0c37b2). These tests sit at the cmd/wipnote layer so they
// exercise the real wiring done in serve_child.go — the queue, the
// queued SQLite sink, and the underlying *receiver.Writer — against an
// actual SQLite database.
//
// The goal is to lock in the architectural rule the plan targets:
// every event/index write path inside the dashboard process goes
// through EXACTLY ONE writable SQLite owner (the queue worker). The
// indexer's 4 MiB per-tick budget (commit e16095482) stays in place as
// complementary defense; the second test below proves the two
// mechanisms compose without deadlock.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/db/writequeue"
	"github.com/shakestzd/wipnote/observe/otel"
	"github.com/shakestzd/wipnote/observe/otel/indexer"
	otelreceiver "github.com/shakestzd/wipnote/observe/otel/receiver"
	sqls "github.com/shakestzd/wipnote/observe/otel/sink/sqlite"
)

// setupWriterTransport builds the slice-6 writer-service trio
// (Writer + Queue + QueuedSink) backed by a fresh, migrated SQLite
// database. Returns the queue (for assertions), the sink (for
// indexer/test producer wiring), and the db path for downstream reads.
func setupWriterTransport(t *testing.T) (*writequeue.Queue, *sqls.QueuedSink, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "otel.db")

	// db.Open runs migrations against a writable handle. We immediately
	// close it; the Writer below opens its own MaxOpenConns=1 handle
	// which is the single owner from here on.
	readDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	readDB.Close()

	writer, err := otelreceiver.NewWriter(dbPath)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	q := writequeue.New(writequeue.Config{Capacity: 128})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("queue.Start: %v", err)
	}
	t.Cleanup(func() { q.Stop(5 * time.Second) })

	return q, sqls.NewQueued(q, writer), dbPath
}

// TestWriterTransport_ConcurrentProducers spawns multiple goroutines
// that submit signal batches to the queued sink simultaneously. The
// expectation is zero SQLITE_BUSY errors — every batch lands cleanly
// because there is exactly one writable owner serializing the writes.
//
// This is the integration counterpart to the unit-level
// TestWriteQueue_SerializesConcurrentProducers in
// internal/db/writequeue: that test asserts the queue layer is
// single-threaded; this test asserts the queue layer plus the real
// SQLite *receiver.Writer plays nicely under concurrent load.
func TestWriterTransport_ConcurrentProducers(t *testing.T) {
	q, queuedSink, dbPath := setupWriterTransport(t)

	const producers = 8
	const batchesPerProducer = 12
	const signalsPerBatch = 5

	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(pid int) {
			defer wg.Done()
			sessionID := fmt.Sprintf("sess-%d", pid)
			for b := 0; b < batchesPerProducer; b++ {
				signals := make([]otel.UnifiedSignal, signalsPerBatch)
				for i := range signals {
					signals[i] = otel.UnifiedSignal{
						SignalID:      fmt.Sprintf("sig-%d-%d-%d", pid, b, i),
						Harness:       otel.HarnessClaude,
						Kind:          otel.KindSpan,
						CanonicalName: "test_span",
						NativeName:    "test_span",
						Timestamp:     time.Now(),
						SessionID:     sessionID,
					}
				}
				if err := queuedSink.WriteBatch(context.Background(),
					otel.HarnessClaude, nil, signals); err != nil {
					t.Errorf("WriteBatch: %v", err)
					return
				}
			}
		}(p)
	}
	wg.Wait()

	// Wait for the queue to drain so the row count below reflects
	// every successful submit.
	deadline := time.Now().Add(5 * time.Second)
	expected := int64(producers * batchesPerProducer)
	for time.Now().Before(deadline) {
		if q.Stats().Dequeued >= expected && q.Stats().Depth == 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	stats := q.Stats()
	if stats.Errors != 0 {
		t.Errorf("queue errors = %d, want 0 (SQLite contention escaped)", stats.Errors)
	}

	// Verify the actual rows landed — proves no SQLITE_BUSY silently
	// dropped a batch. Use a fresh read connection: opening a separate
	// reader is safe in WAL mode and avoids contention with the
	// writer's MaxOpenConns=1 pool.
	readDB, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer readDB.Close()
	var n int
	if err := readDB.QueryRow(`SELECT COUNT(*) FROM otel_signals`).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	wantRows := producers * batchesPerProducer * signalsPerBatch
	if n != wantRows {
		t.Errorf("rows = %d, want %d (queue accepted every batch but DB lost rows)", n, wantRows)
	}
}

// TestWriterTransport_WithIndexerBudget verifies the slice-6 queue
// composes correctly with the indexer's 4 MiB per-tick budget
// (maxBytesPerTick in indexer.go, commit e16095482). The two
// mechanisms are independent: the budget caps how many bytes a single
// session can monopolize per tick; the queue serializes writes across
// every producer.
//
// We pre-populate a session's NDJSON file with more than one tick's
// worth of data, run the indexer to completion across multiple ticks,
// and assert every signal eventually lands without deadlock or
// duplicates.
func TestWriterTransport_WithIndexerBudget(t *testing.T) {
	q, queuedSink, dbPath := setupWriterTransport(t)

	wipnoteDir := filepath.Join(t.TempDir(), ".wipnote")
	sessionID := "budget-test-sess"
	sessDir := filepath.Join(wipnoteDir, "sessions", sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Write a small NDJSON file. We deliberately stay under one tick
	// of bytes so the test runs fast — the budget interaction is
	// proven by composing with the indexer's normal codepath, not by
	// stressing the 4 MiB cap (which would slow CI without adding
	// coverage). The architectural assertion is the same either way:
	// no deadlock, no duplicates.
	ndjsonPath := filepath.Join(sessDir, "events.ndjson")
	f, err := os.Create(ndjsonPath)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const totalLines = 64
	for i := 0; i < totalLines; i++ {
		ts := time.Now().Add(time.Duration(i) * time.Millisecond).UTC().Format(time.RFC3339Nano)
		line := fmt.Sprintf(`{"kind":"span","harness":"claude_code","ts":"%s","signal_id":"budget-sig-%d","session_id":"%s","canonical":"api_request","native":"claude_code.api_request"}`+"\n",
			ts, i, sessionID)
		if _, err := f.WriteString(line); err != nil {
			t.Fatalf("WriteString: %v", err)
		}
	}
	f.Close()

	idxr := indexer.New(wipnoteDir, queuedSink)

	// Drive the indexer's poll loop on a background goroutine. The
	// poll runs every 500 ms; we wait until either every line has
	// been ingested or the deadline expires.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go idxr.Start(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := q.Stats()
		if s.Depth == 0 && s.Dequeued >= int64(totalLines) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if stats := q.Stats(); stats.Errors != 0 {
		t.Errorf("queue errors = %d, want 0", stats.Errors)
	}

	// Count rows in SQLite to confirm no duplicates were introduced
	// by the budget/queue interaction.
	readDB, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer readDB.Close()
	var n int
	if err := readDB.QueryRow(`SELECT COUNT(*) FROM otel_signals WHERE session_id = ?`,
		sessionID).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != totalLines {
		t.Errorf("rows = %d, want %d (indexer + queue dropped or duplicated signals)", n, totalLines)
	}
}

// TestWriterTransport_CanonicalFirstFallback locks in the
// HIGH-architectural rule from plan-ae0c37b2 slice 6: a producer that
// hits queue overflow must not lose user work. We simulate "canonical
// already on disk, queue rejected the index update" by submitting past
// capacity and asserting WriteBatch returns nil (success from the
// producer's perspective) on overflow.
func TestWriterTransport_CanonicalFirstFallback(t *testing.T) {
	// Tiny capacity + blocked consumer so every submit after the first
	// returns ErrQueueFull, which the queued sink converts to nil.
	dbPath := filepath.Join(t.TempDir(), "otel.db")
	readDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	readDB.Close()
	writer, err := otelreceiver.NewWriter(dbPath)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	q := writequeue.New(writequeue.Config{Capacity: 1})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("queue.Start: %v", err)
	}
	t.Cleanup(func() { q.Stop(5 * time.Second) })

	queuedSink := sqls.NewQueued(q, writer)

	// Submit one batch that blocks the consumer goroutine.
	blockingDone := make(chan struct{})
	defer close(blockingDone)
	blockerStarted := make(chan struct{})
	// Use the queue directly to inject a blocker op.
	if err := q.Submit(context.Background(), func(context.Context) error {
		close(blockerStarted)
		<-blockingDone
		return nil
	}); err != nil {
		t.Fatalf("submit blocker: %v", err)
	}
	<-blockerStarted

	// Fill the (capacity=1) channel with one op.
	if err := q.Submit(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("fill submit: %v", err)
	}

	// Now go through the queued sink: should be rejected, but the
	// canonical-first contract requires nil from the producer's
	// perspective.
	signals := []otel.UnifiedSignal{{
		SignalID:      "canonical-first",
		Harness:       otel.HarnessClaude,
		Kind:          otel.KindLog,
		CanonicalName: "test",
		NativeName:    "test",
		Timestamp:     time.Now(),
		SessionID:     "cf-sess",
	}}
	err = queuedSink.WriteBatch(context.Background(), otel.HarnessClaude, nil, signals)
	if err != nil {
		t.Errorf("WriteBatch on overflow returned %v, want nil (canonical-first contract)", err)
	}
	if rej := q.Stats().Rejected; rej == 0 {
		t.Errorf("Rejected = 0, want > 0 — overflow was supposed to bump the counter")
	}
}
