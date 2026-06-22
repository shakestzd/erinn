package hooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/daemon"
	"github.com/shakestzd/wipnote/core/daemon/apply"
	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/db/writequeue"
	"github.com/shakestzd/wipnote/core/models"
)

// roborev-473 findings 3 & 4 regression tests.
//
// Finding 3: pending_subagent_starts must be APPLIED-ack (visible) before
// SubagentStart returns, because the OTLP receiver reads that row the instant the
// first subagent span arrives — an enqueue-only write opens a miss window.
//
// Finding 4: the SubagentStart lineage INSERT and the SubagentStop close UPDATE
// must apply in FIFO order on the daemon's single writer (start before stop), so
// a quick stop never updates 0 rows and leaves an orphaned `active` lineage row
// that the later-landing insert created.
//
// Both are exercised against a REAL in-process writer daemon so the routed
// enqueue/applied ops actually reach the single writer (not the no-daemon direct
// fallback that setupLifecycleDB models).

// startInProcessDaemonForHooks binds a real daemon.Listener to the project's
// writer socket with a single-writer applier over a pinned handle and serves it.
// Returns a stop func. The hot hooks dial this listener via apply.RouteSQL /
// apply.RouteSQLAsync (daemon.NewWriterClient(projectRoot)), so no _serve-child
// subprocess is forked under `go test`.
func startInProcessDaemonForHooks(t *testing.T, projectRoot, dbPath string) func() {
	t.Helper()

	writerDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("daemon: open writer DB: %v", err)
	}
	writerDB.SetMaxOpenConns(1)
	writerDB.SetMaxIdleConns(1)

	lease, err := daemon.AcquireLease(projectRoot)
	if err != nil {
		writerDB.Close()
		t.Fatalf("daemon: acquire lease: %v", err)
	}

	q := writequeue.New(writequeue.Config{Capacity: writequeue.DefaultCapacity})
	qctx, qcancel := context.WithCancel(context.Background())
	if err := q.Start(qctx); err != nil {
		qcancel()
		_ = lease.Release()
		writerDB.Close()
		t.Fatalf("daemon: queue start: %v", err)
	}

	ln, err := daemon.NewListener(daemon.ListenerConfig{
		SocketPath: daemon.SocketPath(projectRoot),
		Queue:      q,
		Applier:    apply.NewApplier(writerDB),
		OwnerPID:   os.Getpid(),
	})
	if err != nil {
		q.Stop(time.Second)
		qcancel()
		_ = lease.Release()
		writerDB.Close()
		t.Fatalf("daemon: new listener: %v", err)
	}
	serveCtx, serveCancel := context.WithCancel(context.Background())
	go func() { _ = ln.Serve(serveCtx) }()

	// Wait for the socket inode so the first hook dial doesn't race the bind.
	sock := daemon.SocketPath(projectRoot)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(sock); statErr == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	return func() {
		serveCancel()
		_ = ln.Close()
		q.Stop(2 * time.Second)
		qcancel()
		_ = lease.Release()
		writerDB.Close()
	}
}

// TestSubagentStart_PendingRowAppliedAckBeforeReturn is the roborev-473 finding 3
// regression test: with a reachable writer daemon, the pending_subagent_starts
// row must be COMMITTED (visible) by the time SubagentStart returns — proving the
// write took the APPLIED-ack route (apply.RouteSQL), not the enqueue-only route
// (which could return before the row applied, opening the OTLP-receiver miss
// window).
func TestSubagentStart_PendingRowAppliedAckBeforeReturn(t *testing.T) {
	clearRoborev473NestedEnv(t)

	root, err := os.MkdirTemp("", "wn-rv473-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.MkdirAll(filepath.Join(root, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	dbPath := filepath.Join(root, ".wipnote", "wipnote.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)
	t.Setenv("WIPNOTE_PROJECT_DIR", root)

	// Bootstrap schema, then close — the daemon opens the only writer handle.
	boot, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("bootstrap db.Open: %v", err)
	}
	parentSessionID := "rv473-parent-pending"
	if err := db.InsertSession(boot, &models.Session{
		SessionID: parentSessionID, AgentAssigned: "claude-code", Status: "active",
	}); err != nil {
		t.Fatalf("InsertSession parent: %v", err)
	}
	boot.Close()

	stop := startInProcessDaemonForHooks(t, root, dbPath)
	defer stop()

	// Production hot-hook dispatch is READ-ONLY for the handle SubagentStart reads
	// from; the pending write routes APPLIED-ack through the daemon.
	roDB, err := db.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer roDB.Close()

	subagentID := "rv473-subagent-pending"
	event := &CloudEvent{
		SessionID: parentSessionID,
		CWD:       root,
		AgentID:   subagentID,
		AgentType: "wipnote:patch-coder",
	}
	if _, err := SubagentStart(event, roDB); err != nil {
		t.Fatalf("SubagentStart: %v", err)
	}

	// CRITICAL: read on a SEPARATE read-only connection IMMEDIATELY after return,
	// with NO settle/sleep. If the pending write were enqueue-only the row might
	// not be applied yet; the applied-ack route guarantees it is committed.
	verify, err := db.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("verify OpenReadOnly: %v", err)
	}
	defer verify.Close()
	pending, err := db.GetPendingSubagentStart(verify, subagentID)
	if err != nil {
		t.Fatalf("GetPendingSubagentStart: %v", err)
	}
	if pending == nil {
		t.Fatal("pending_subagent_starts row NOT visible immediately after SubagentStart returned — " +
			"finding 3 requires APPLIED-ack (apply.RouteSQL) so the OTLP receiver never misses it")
	}
	if pending.AgentID != subagentID || pending.SessionID != parentSessionID {
		t.Errorf("pending row mismatch: agent_id=%q session_id=%q", pending.AgentID, pending.SessionID)
	}
}

// TestSubagentLineage_FIFOStopCloseAfterStartInsert is the roborev-473 finding 4
// regression test: with a reachable writer daemon, SubagentStart's enqueue-only
// lineage INSERT and SubagentStop's enqueue-only close UPDATE apply in FIFO order
// on the single writer. Because start fires before stop, the insert lands first
// and the close UPDATE matches it — the row ends `completed`, never an orphaned
// `active`.
func TestSubagentLineage_FIFOStopCloseAfterStartInsert(t *testing.T) {
	clearRoborev473NestedEnv(t)

	root, err := os.MkdirTemp("", "wn-rv473-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.MkdirAll(filepath.Join(root, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	dbPath := filepath.Join(root, ".wipnote", "wipnote.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)
	t.Setenv("WIPNOTE_PROJECT_DIR", root)

	boot, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("bootstrap db.Open: %v", err)
	}
	parentSessionID := "rv473-parent-fifo"
	if err := db.InsertSession(boot, &models.Session{
		SessionID: parentSessionID, AgentAssigned: "claude-code", Status: "active",
	}); err != nil {
		t.Fatalf("InsertSession parent: %v", err)
	}
	boot.Close()

	stop := startInProcessDaemonForHooks(t, root, dbPath)
	defer stop()

	roDB, err := db.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer roDB.Close()

	subagentID := "rv473-subagent-fifo"

	// Start: enqueues the synthetic-sessions INSERT + lineage INSERT (active).
	startEvent := &CloudEvent{
		SessionID: parentSessionID,
		CWD:       root,
		AgentID:   subagentID,
		AgentType: "general-purpose",
	}
	if _, err := SubagentStart(startEvent, roDB); err != nil {
		t.Fatalf("SubagentStart: %v", err)
	}

	// Stop FIRES IMMEDIATELY (quick stop) — its close UPDATE enqueues right behind
	// the start insert. FIFO single-writer ordering guarantees the insert applies
	// first, so the close matches the row.
	stopEvent := &CloudEvent{
		SessionID:            parentSessionID,
		CWD:                  root,
		AgentID:              subagentID,
		LastAssistantMessage: "done",
	}
	if _, err := SubagentStop(stopEvent, roDB); err != nil {
		t.Fatalf("SubagentStop: %v", err)
	}

	// Allow the FIFO worker to drain both enqueued ops.
	var status string
	deadline := time.Now().Add(2 * time.Second)
	for {
		row := roDB.QueryRow(`SELECT status FROM agent_lineage_trace WHERE trace_id = ?`, subagentID)
		if err := row.Scan(&status); err == nil && status != "" {
			if status == "completed" {
				break
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	if status != "completed" {
		t.Fatalf("lineage row status = %q, want %q — finding 4: the FIFO insert-before-close "+
			"ordering must close the lineage row, not leave an orphaned `active` row", status, "completed")
	}
}

// clearRoborev473NestedEnv unsets nested-session env that would hijack the
// project-dir / session resolution and dial a socket this test never bound.
func clearRoborev473NestedEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"CLAUDE_CODE_SESSION_ID", "CLAUDE_SESSION_ID", "WIPNOTE_SESSION_ID",
		"WIPNOTE_PARENT_SESSION", "WIPNOTE_NESTING_DEPTH",
		"CLAUDE_CODE_ENTRYPOINT", "WIPNOTE_AGENT_ID",
		"WIPNOTE_NO_AUTO_WRITER",
	} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}
