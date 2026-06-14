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
	"sync"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/daemon"
	"github.com/shakestzd/wipnote/core/daemon/apply"
	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/db/writequeue"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/observe/otel"
	otelreceiver "github.com/shakestzd/wipnote/observe/otel/receiver"
	sqls "github.com/shakestzd/wipnote/observe/otel/sink/sqlite"
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
	if testing.Short() {
		t.Skip("drives daemon writer maintenance integration flow")
	}

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

	// Consolidated single-writer topology (feat-075c110d): ONE writable
	// handle capped at MaxOpenConns=1, shared by the maintenance loops AND the
	// OTel sink (NewWriterFromDB borrows it). This mirrors runWriterOnly so the
	// test exercises the real wiring — otel_signals must populate through this
	// single handle with zero contention.
	writeDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open writable: %v", err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)
	defer writeDB.Close()

	writer, err := otelreceiver.NewWriterFromDB(writeDB)
	if err != nil {
		t.Fatalf("NewWriterFromDB: %v", err)
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

// TestDaemonSingleWriter_NoBusyUnderConcurrentWritePaths is the core
// feat-075c110d regression: the daemon's THREE concurrent write paths — the
// OTel sink (otel_signals), the socket-op applier (agent_events), and a
// direct maintenance write — must all serialize on ONE writable handle
// (MaxOpenConns=1) with ZERO SQLITE_BUSY.
//
// Before the fix the daemon opened TWO writable pools (dbpkg.Open + the
// receiver Writer's own pool), so concurrent BEGIN IMMEDIATE produced
// "database is locked (5)". This test drives all three paths hard against the
// consolidated single handle and asserts both the writer_service busy counter
// AND the queue error counter stay zero, and that every row lands.
func TestDaemonSingleWriter_NoBusyUnderConcurrentWritePaths(t *testing.T) {
	projectRoot := t.TempDir()
	wipnoteDir := filepath.Join(projectRoot, ".wipnote")
	dbPath := filepath.Join(wipnoteDir, "wipnote.db")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	boot, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	boot.Close()

	// THE single writable handle — exactly runWriterOnly's topology.
	writeDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open writable: %v", err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)
	defer writeDB.Close()

	writer, err := otelreceiver.NewWriterFromDB(writeDB)
	if err != nil {
		t.Fatalf("NewWriterFromDB: %v", err)
	}
	defer writer.Close()

	// Both the otel sink AND the applier route through ONE queue worker — the
	// same single-writer serialization the daemon uses.
	q := writequeue.New(writequeue.Config{Capacity: 512})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := q.Start(ctx); err != nil {
		t.Fatalf("queue start: %v", err)
	}
	defer q.Stop(2 * time.Second)

	snk := sqls.NewQueued(q, writer)
	applier := apply.NewApplier(writer.DB())

	db.ResetBusyCounters()

	const (
		sessions  = 4
		otelBatch = 6
		evtsPer   = 6
		filesPer  = 6
	)
	res := map[string]any{"service.name": "claude-code"}

	var wg sync.WaitGroup
	for sIdx := 0; sIdx < sessions; sIdx++ {
		sessionID := fmt.Sprintf("sw-sess-%d", sIdx)

		// Path 1: OTel sink → otel_signals (also creates the session row).
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			batch := make([]otel.UnifiedSignal, otelBatch)
			for i := range batch {
				batch[i] = otel.UnifiedSignal{
					SignalID:      fmt.Sprintf("%s-osig-%d", sid, i),
					Harness:       otel.HarnessClaude,
					SessionID:     sid,
					Kind:          otel.KindSpan,
					CanonicalName: "api_request",
					NativeName:    "claude_code.api_request",
					Timestamp:     time.Now(),
				}
			}
			if err := snk.WriteBatchSync(context.Background(), otel.HarnessClaude, res, batch); err != nil {
				t.Errorf("otel WriteBatchSync(%s): %v", sid, err)
			}
		}(sessionID)

		// Path 2: socket-op applier → agent_events upsert (concurrent with the
		// otel writes on the same single handle).
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			for i := 0; i < evtsPer; i++ {
				ev := &models.AgentEvent{
					EventID:      fmt.Sprintf("%s-evt-%d", sid, i),
					AgentID:      "__root__",
					EventType:    models.EventCheckPoint,
					Timestamp:    time.Now().UTC(),
					ToolName:     "T",
					InputSummary: "concurrent-write",
					SessionID:    sid,
					Status:       "recorded",
					Source:       "test",
					CreatedAt:    time.Now().UTC(),
					UpdatedAt:    time.Now().UTC(),
				}
				payload, err := apply.Encode(apply.DerivedOp{Type: apply.OpTypeAgentEventUpsert, Event: ev})
				if err != nil {
					t.Errorf("encode op: %v", err)
					return
				}
				op, err := applier(daemon.Envelope{
					OpID:    apply.OpID(sid, int64(i)),
					OpType:  apply.OpTypeAgentEventUpsert,
					Payload: payload,
				})
				if err != nil {
					t.Errorf("applier(%s): %v", sid, err)
					return
				}
				if err := q.SubmitSync(context.Background(), op); err != nil {
					t.Errorf("submit applier op(%s): %v", sid, err)
				}
			}
		}(sessionID)

		// Path 3: direct maintenance write through writeDB — models the
		// claimless-file indexer / retention job that writes directly to the
		// daemon's handle (not via the applier queue), racing the two queued
		// paths. db.UpsertSessionFile is the exact call the indexer makes.
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			for i := 0; i < filesPer; i++ {
				filePath := fmt.Sprintf("/project/file-%d.go", i)
				if err := db.UpsertSessionFile(writeDB, sid, filePath, "write"); err != nil {
					t.Errorf("UpsertSessionFile(%s, %s): %v", sid, filePath, err)
				}
			}
		}(sessionID)
	}

	wg.Wait()

	// Zero contention on the consolidated handle.
	if got := db.BusyCount(db.SubsystemWriterService); got != 0 {
		t.Fatalf("writer_service SQLITE_BUSY count = %d, want 0 (single-writer consolidation broken)", got)
	}
	if stats := q.Stats(); stats.Errors != 0 {
		t.Fatalf("writequeue errors = %d, want 0 (concurrent paths contended)", stats.Errors)
	}

	// Every row from all three paths landed.
	var otelRows, evtRows, fileRows int
	if err := writeDB.QueryRow(`SELECT COUNT(*) FROM otel_signals`).Scan(&otelRows); err != nil {
		t.Fatalf("count otel_signals: %v", err)
	}
	if err := writeDB.QueryRow(`SELECT COUNT(*) FROM agent_events`).Scan(&evtRows); err != nil {
		t.Fatalf("count agent_events: %v", err)
	}
	if err := writeDB.QueryRow(`SELECT COUNT(*) FROM session_files`).Scan(&fileRows); err != nil {
		t.Fatalf("count session_files: %v", err)
	}
	if wantOtel := sessions * otelBatch; otelRows != wantOtel {
		t.Errorf("otel_signals rows = %d, want %d", otelRows, wantOtel)
	}
	if wantEvt := sessions * evtsPer; evtRows != wantEvt {
		t.Errorf("agent_events rows = %d, want %d", evtRows, wantEvt)
	}
	// Each session writes filesPer unique paths; since UpsertSessionFile is
	// idempotent on (session_id, file_path), distinct sessions produce distinct
	// rows (different session_id), so total = sessions * filesPer.
	if wantFiles := sessions * filesPer; fileRows != wantFiles {
		t.Errorf("session_files rows = %d, want %d", fileRows, wantFiles)
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
