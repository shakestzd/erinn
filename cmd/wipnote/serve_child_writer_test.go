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

// TestDashboardTelemetryIndexer_MaterialisesIntoDashboardProjection replaces
// TestStartWriterMaintenance_IndexerRunsInDaemon.
//
// The old test asserted that the NDJSON->SQLite indexer ran inside the writer
// daemon and landed rows in the daemon's otel_signals. That assertion became
// the wrong one to make: the daemon and the HTTP serve_child are separate
// processes with separate in-memory projections (feat-fc3cc9e0), so rows the
// daemon indexed were unreachable from the handlers that query them and the
// dashboard's OTel surface would have been blank however green the test ran.
//
// The indexer moved to the process that serves the queries, so this drives it
// there: seed an events.ndjson, start the indexer against a dashboard-shaped
// projection, and require the signals to become queryable in THAT handle.
func TestDashboardTelemetryIndexer_MaterialisesIntoDashboardProjection(t *testing.T) {
	if testing.Short() {
		t.Skip("drives the dashboard telemetry indexer flow")
	}

	projectRoot := t.TempDir()
	wipnoteDir := filepath.Join(projectRoot, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	isolateProjectDir(t, projectRoot)

	// Exactly the handle runServeChild builds and hands to the mux.
	database, err := db.OpenEphemeralProjection()
	if err != nil {
		t.Fatalf("OpenEphemeralProjection: %v", err)
	}
	defer database.Close()

	// Seed an NDJSON session file the indexer will discover and ingest. The
	// indexer skips session directories with no row in the sessions table
	// (the orphan filter), so the session must exist there first.
	sessionID := "dash-indexer-sess"
	if err := db.UpsertSession(database, &models.Session{
		SessionID: sessionID,
		Status:    "active",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session row: %v", err)
	}
	sessDir := filepath.Join(wipnoteDir, "sessions", sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	f, err := os.Create(filepath.Join(sessDir, "events.ndjson"))
	if err != nil {
		t.Fatalf("create ndjson: %v", err)
	}
	const lines = 8
	for i := 0; i < lines; i++ {
		ts := time.Now().Add(time.Duration(i) * time.Millisecond).UTC().Format(time.RFC3339Nano)
		fmt.Fprintf(f, `{"kind":"span","harness":"claude_code","ts":"%s","signal_id":"dash-sig-%d","session_id":"%s","canonical":"api_request","native":"claude_code.api_request"}`+"\n",
			ts, i, sessionID)
	}
	f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startDashboardTelemetryIndexer(ctx, database, wipnoteDir)

	deadline := time.Now().Add(8 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		_ = database.QueryRow(`SELECT COUNT(*) FROM otel_signals WHERE session_id = ?`, sessionID).Scan(&n)
		if n >= lines {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n < lines {
		t.Fatalf("dashboard projection holds %d signal rows, want %d — the OTel surface would render empty", n, lines)
	}
}

// TestStartWriterMaintenance_NoTelemetryIndexerInDaemon is the companion guard:
// the daemon must NOT be where telemetry is materialised. If someone re-adds an
// indexer to startWriterMaintenance it will populate a projection no reader can
// reach, which is precisely the failure the move above corrects — and it would
// look like working code.
func TestStartWriterMaintenance_NoTelemetryIndexerInDaemon(t *testing.T) {
	projectRoot := t.TempDir()
	wipnoteDir := filepath.Join(projectRoot, ".wipnote")
	sessionID := "daemon-should-not-index"
	sessDir := filepath.Join(wipnoteDir, "sessions", sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	// This test starts the REAL maintenance loops, and two of them act on the
	// filesystem: retention archives and then os.RemoveAll's session
	// directories, and the reconcile drain git-commits work-item artifacts.
	// t.TempDir alone does not contain them — paths.ResolveProjectDir reads
	// WIPNOTE_PROJECT_DIR / CLAUDE_PROJECT_DIR before the working directory, so
	// an unisolated run of this test would sweep the real project. Isolate, and
	// additionally force retention into dry-run so even a resolution mistake
	// cannot delete anything.
	isolateProjectDir(t, projectRoot)
	t.Setenv("WIPNOTE_RETENTION_DRYRUN", "1")
	f, err := os.Create(filepath.Join(sessDir, "events.ndjson"))
	if err != nil {
		t.Fatalf("create ndjson: %v", err)
	}
	fmt.Fprintf(f, `{"kind":"span","harness":"claude_code","ts":"%s","signal_id":"daemon-sig-0","session_id":"%s","canonical":"api_request","native":"claude_code.api_request"}`+"\n",
		time.Now().UTC().Format(time.RFC3339Nano), sessionID)
	f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startWriterMaintenance(ctx, wipnoteDir)

	// Give any (wrongly) started indexer several poll intervals to act.
	time.Sleep(1500 * time.Millisecond)

	// The daemon builds a fresh projection per pass and drops it, so there is no
	// daemon-side handle to inspect. What we can assert is the observable
	// consequence of a daemon-side indexer: it would have written the on-disk
	// checkpoint next to the NDJSON.
	if _, err := os.Stat(filepath.Join(sessDir, ".index-offset")); err == nil {
		t.Error("startWriterMaintenance wrote an NDJSON checkpoint — a telemetry indexer is running in the daemon, where nothing can read what it produces")
	}
}

// TestDaemonSingleWriter_NoBusyUnderConcurrentWritePaths is the core
// feat-075c110d regression: the daemon's THREE concurrent write paths — the
// OTel sink (otel_signals), the socket-op applier (agent_events), and a
// direct maintenance write — must all serialize on ONE handle with ZERO
// SQLITE_BUSY and no dropped rows.
//
// Before the fix the daemon opened TWO writable pools to the same FILE
// (dbpkg.Open + the receiver Writer's own pool), so concurrent BEGIN IMMEDIATE
// produced "database is locked (5)". The file is gone (feat-fc3cc9e0) and with
// it cross-process lock contention, but the single-handle serialization this
// guards is unchanged and still load-bearing: OpenEphemeralProjection caps the
// pool at one connection for exactly the same reason, and concurrent producers
// on one connection is still what the daemon does.
func TestDaemonSingleWriter_NoBusyUnderConcurrentWritePaths(t *testing.T) {
	// THE single handle — exactly runWriterOnly's topology.
	writeDB, err := db.OpenEphemeralProjection()
	if err != nil {
		t.Fatalf("OpenEphemeralProjection: %v", err)
	}
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

// TestServeChildReadOnlyHandleRejectsWrites is gone. It asserted that the
// handle serve_child hands its mux refuses writes — an invariant that existed
// because the dashboard read a shared on-disk database the writer daemon owned,
// so a stray dashboard write meant cross-process contention. serve_child now
// builds its own in-memory projection (feat-fc3cc9e0), owns it outright, and
// passes it as both the read and write handle; there is no shared file left to
// protect and the property is no longer true by design. The remaining guard
// worth having — that serve_child opens no file-backed database at all — is
// covered by the write-boundary tests, not by re-asserting query_only here.
