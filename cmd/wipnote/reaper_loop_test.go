package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/hooks"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/observe/otel/collector"
)

// TestStartReaperLoopNoOp proves the nil/empty guards: startReaperLoop must
// return without panic when writeDB is nil OR projectRoot is empty, so a
// mis-wired daemon never crashes.
func TestStartReaperLoopNoOp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// nil DB → guarded return, no goroutine, no panic.
	startReaperLoop(ctx, nil, "")

	// non-nil DB but empty projectRoot → guarded return, no panic.
	dbPath := filepath.Join(t.TempDir(), "wipnote.db")
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	startReaperLoop(ctx, database, "")
}

// TestDaemonPassReapsOrphanCollector exercises the daemon-loop BODY directly
// (avoiding goroutine/ticker timing flakiness): a single
// ReapStaleSessionsAndCollectors(writeDB, projectRoot, "", includeCollectors=true,
// reportOnly=false) call — exactly what startReaperLoop ticks. The
// observe/register init() (blank-imported by cmd/wipnote's main) wires the live
// ReapCollectorFn seam, so this proves the daemon path reaps BOTH a stale session
// AND a real orphaned collector subprocess. This complements the core/hooks test
// that proves the HOOK path leaves collectors untouched.
func TestDaemonPassReapsOrphanCollector(t *testing.T) {
	projectRoot := t.TempDir()
	dbPath := filepath.Join(projectRoot, ".wipnote", "wipnote.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	// Seed a stale-active session: no claim ⇒ heartbeat stale; dead .session-pid
	// ⇒ owner process provably dead ⇒ reap-eligible.
	const sid = "sess-daemon-orphan-001"
	if err := dbpkg.InsertSession(database, &models.Session{
		SessionID:     sid,
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	sessDir := filepath.Join(projectRoot, ".wipnote", "sessions", sid)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessDir: %v", err)
	}
	// Dead .session-pid: max int32 pid + a start-time line (never running).
	if err := os.WriteFile(filepath.Join(sessDir, ".session-pid"), []byte("2147483647\n1"), 0o644); err != nil {
		t.Fatalf("write .session-pid: %v", err)
	}

	// Spawn a REAL orphan collector subprocess and register it via
	// WriteCollectorPID (records pid + /proc start time → IsCollectorAlive matches).
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	childPID := cmd.Process.Pid
	t.Cleanup(func() {
		// Defensive: ensure the child is gone even if an assertion fails first.
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	collector.WriteCollectorPID(projectRoot, sid, childPID)

	collectorPID := filepath.Join(sessDir, ".collector-pid")
	if _, err := os.Stat(collectorPID); err != nil {
		t.Fatalf("precondition: .collector-pid must exist before reap: %v", err)
	}

	// Run the daemon-loop body directly: sessions + collectors, remediate.
	rep := hooks.ReapStaleSessionsAndCollectors(database, projectRoot, "", true, false, 0)
	if rep == nil {
		t.Fatal("ReapStaleSessionsAndCollectors returned nil report")
	}

	// (1) The session must be reaped (active → completed).
	var status string
	if err := database.QueryRow(`SELECT status FROM sessions WHERE session_id=?`, sid).Scan(&status); err != nil {
		t.Fatalf("query session status: %v", err)
	}
	if status != "completed" {
		t.Fatalf("session status=%q, want completed (daemon pass must reap stale session)", status)
	}

	// (2) The orphan collector subprocess must be killed. Reap the zombie, then
	// kill(pid,0) must return ESRCH (no such process).
	_, _ = cmd.Process.Wait()
	if err := syscall.Kill(childPID, 0); err == nil {
		t.Fatalf("collector pid %d should be dead after daemon reap", childPID)
	}

	// (3) The .collector-pid record must be cleared.
	if _, err := os.Stat(collectorPID); !os.IsNotExist(err) {
		t.Fatalf(".collector-pid must be removed after reaping the orphan collector (stat err=%v)", err)
	}
}
