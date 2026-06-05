// feat-075c110d increment 2 tests: the serve-side writer consolidation.
//
// These fast unit tests (no test binary) assert the increment's load-bearing
// invariants directly against the in-process helpers:
//
//   - serve_child reuses an existing writer (lease held) and never double-spawns
//     (LeaseOwnerAlive probe + ensureWriterDaemon no-op path).
//   - the background maintenance that MOVED into the daemon (the indexer) really
//     runs against the daemon's single writable handle/queue and lands rows.
//
// The "serve_child opens read-only" and "serve-managed writer is reaped on
// serve shutdown" invariants are covered by:
//   - the boundary test (TestWritableDBOpenBoundary): runServeChild is no longer
//     a writable opener; runWriterOnly is the sole daemon writer.
//   - the integration test TestServeManagedWriterReapedOnShutdown
//     (serve_global_proxy_test.go, //go:build integration), which drives a real
//     `wipnote serve` against a throwaway project.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/internal/daemon"
	"github.com/shakestzd/wipnote/internal/db"
	"github.com/shakestzd/wipnote/internal/db/writequeue"
	otelreceiver "github.com/shakestzd/wipnote/internal/otel/receiver"
)

// TestLeaseOwnerAlive_ReflectsLiveOwner verifies the exported probe serve_child
// uses to decide whether a writer is already running.
func TestLeaseOwnerAlive_ReflectsLiveOwner(t *testing.T) {
	dir := t.TempDir()

	if daemon.LeaseOwnerAlive(dir) {
		t.Fatal("LeaseOwnerAlive=true with no lease file")
	}

	lease, err := daemon.AcquireLease(dir)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	if !daemon.LeaseOwnerAlive(dir) {
		t.Fatal("LeaseOwnerAlive=false while THIS process holds the lease")
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if daemon.LeaseOwnerAlive(dir) {
		t.Fatal("LeaseOwnerAlive=true after release")
	}
}

// TestEnsureWriterDaemon_NoDoubleSpawnWhenLeaseHeld asserts that serve_child
// does NOT spawn a second writer when a live writer lease already exists — the
// single-owner guarantee no matter who started the writer (CLI/hook vs serve).
// ensureWriterDaemon must return a no-op stop func and start no process.
func TestEnsureWriterDaemon_NoDoubleSpawnWhenLeaseHeld(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	// Simulate a live writer by holding the lease in THIS test process.
	lease, err := daemon.AcquireLease(dir)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	defer lease.Release()

	// No writer.sock exists, but the lease is held by a live PID — so
	// ensureWriterDaemon must treat the writer as present and NOT spawn.
	stop := ensureWriterDaemon(dir)
	defer stop()

	// If ensureWriterDaemon had spawned a managed child, a brand-new writer.pid
	// would have been created by that child (overwriting/racing our lease). The
	// lease file must still contain OUR pid, proving no second writer started.
	data, err := os.ReadFile(daemon.LeasePath(dir))
	if err != nil {
		t.Fatalf("read lease: %v", err)
	}
	gotPID, _ := strconv.Atoi(string(data[:len(data)-1]))
	if gotPID != os.Getpid() {
		t.Fatalf("lease pid = %d, want our pid %d (ensureWriterDaemon spawned a second writer)", gotPID, os.Getpid())
	}
}

// TestStartWriterMaintenance_IndexerRunsInDaemon proves the maintenance that
// MOVED into the daemon (the NDJSON->SQLite indexer) actually runs against the
// daemon's single writable handle + writequeue: writing an events.ndjson file
// and starting maintenance must land rows in otel_signals via the queue.
func TestStartWriterMaintenance_IndexerRunsInDaemon(t *testing.T) {
	projectRoot := t.TempDir()
	wipnoteDir := filepath.Join(projectRoot, ".wipnote")
	dbPath := filepath.Join(wipnoteDir, "wipnote.db")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	// Bootstrap schema, then open the daemon's single writable handle.
	boot, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	boot.Close()

	writeDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open writable: %v", err)
	}
	defer writeDB.Close()

	writer, err := otelreceiver.NewWriter(dbPath)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer writer.Close()

	q := writequeue.New(writequeue.Config{Capacity: 128})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := q.Start(ctx); err != nil {
		t.Fatalf("queue start: %v", err)
	}
	defer q.Stop(2 * time.Second)

	// Seed an NDJSON session file the indexer will discover and ingest.
	sessionID := "increment2-sess"
	sessDir := filepath.Join(wipnoteDir, "sessions", sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	ndjson := filepath.Join(sessDir, "events.ndjson")
	f, err := os.Create(ndjson)
	if err != nil {
		t.Fatalf("create ndjson: %v", err)
	}
	const lines = 8
	for i := 0; i < lines; i++ {
		ts := time.Now().Add(time.Duration(i) * time.Millisecond).UTC().Format(time.RFC3339Nano)
		fmt.Fprintf(f, `{"kind":"span","harness":"claude_code","ts":"%s","signal_id":"inc2-sig-%d","session_id":"%s","canonical":"api_request","native":"claude_code.api_request"}`+"\n",
			ts, i, sessionID)
	}
	f.Close()

	// Start the daemon-side maintenance (the SAME entrypoint runWriterOnly uses).
	startWriterMaintenance(ctx, writeDB, wipnoteDir, q, writer)

	// The indexer routes batches through the queue; wait for the rows to land.
	deadline := time.Now().Add(8 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		_ = writeDB.QueryRow(`SELECT COUNT(*) FROM otel_signals WHERE session_id = ?`, sessionID).Scan(&n)
		if n >= lines {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n < lines {
		t.Fatalf("daemon-side indexer landed %d rows, want %d — maintenance is not running against the daemon handle", n, lines)
	}
	if stats := q.Stats(); stats.Errors != 0 {
		t.Fatalf("writequeue errors = %d, want 0 (daemon-side maintenance hit contention)", stats.Errors)
	}
}

// ensure the read-only handle helper used by serve_child exists and refuses
// writes — a regression guard that runServeChild's mux handle cannot mutate.
func TestServeChildReadOnlyHandleRejectsWrites(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wipnote.db")
	boot, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	boot.Close()

	ro, err := db.OpenReadOnlyMigrated(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnlyMigrated: %v", err)
	}
	defer ro.Close()

	if _, err := ro.Exec(`CREATE TABLE inc2_should_fail (x INTEGER)`); err == nil {
		t.Fatal("read-only serve_child handle accepted a write — query_only guard missing")
	}
}
