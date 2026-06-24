package hooks

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSessionStartReapsStaleSessionsOnly proves the SessionStart wire-in (slice-4):
// the hook self-heals by reaping stale-active SESSIONS left by a prior abnormal
// exit, but does so SESSIONS-ONLY (includeCollectors=false). A stale OTHER session
// with both a dead .session-pid AND an orphan .collector-pid is transitioned to
// completed, while the collector seam (ReapCollectorFn) is NEVER invoked and the
// orphan .collector-pid is left untouched — collectors are the daemon's job.
func TestSessionStartReapsStaleSessionsOnly(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	database, err := openWipnoteTestDB(t, projectDir)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	// Unset env vars that would override the resolved session ID or flag this as a
	// subagent (which would make SessionStart write under the real session ID).
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("WIPNOTE_PARENT_SESSION", "")
	t.Setenv("WIPNOTE_NESTING_DEPTH", "")
	t.Setenv("CLAUDE_ENV_FILE", "")

	// Seed a stale-active OTHER session: no claim ⇒ heartbeat stale; .session-pid
	// with a certainly-dead pid ⇒ owner process dead ⇒ reap-eligible. Plus an
	// orphan .collector-pid so the collector phase WOULD fire if it were enabled.
	const other = "sess-stale-other-001"
	seedReaperSession(t, database, other)
	otherDir := filepath.Join(projectDir, ".wipnote", "sessions", other)
	writeDeadPID(t, otherDir)
	collectorPID := filepath.Join(otherDir, ".collector-pid")
	if err := os.WriteFile(collectorPID, []byte("2147483647\n1"), 0o644); err != nil {
		t.Fatalf("write .collector-pid: %v", err)
	}

	// Fake the collector seam so we can prove it is NEVER called on the hook path.
	prev := ReapCollectorFn
	t.Cleanup(func() { ReapCollectorFn = prev })
	collectorCalled := false
	ReapCollectorFn = func(string, time.Duration) (int, bool) {
		collectorCalled = true
		return 0, true
	}

	// Invoke SessionStart for a NEW session (CWD == projectDir keeps it hermetic:
	// no worktree gitdir repair path is taken).
	const current = "sess-start-new-001"
	event := &CloudEvent{SessionID: current, CWD: projectDir}
	if _, err := SessionStart(event, database, projectDir); err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	// The OTHER stale session must be reaped (active → completed).
	if got := sessionStatus(t, database, other); got != "completed" {
		t.Fatalf("stale OTHER session status=%q, want completed (must be reaped on SessionStart)", got)
	}

	// The NEW session must remain active (self-exclusion).
	if got := sessionStatus(t, database, current); got != "active" {
		t.Fatalf("new session status=%q, want active (must NOT reap itself)", got)
	}

	// The collector seam must NEVER fire — SessionStart passes includeCollectors=false.
	if collectorCalled {
		t.Fatal("ReapCollectorFn was invoked: SessionStart must reap SESSIONS ONLY (includeCollectors=false)")
	}

	// The orphan .collector-pid must still exist — collectors are reaped by the
	// long-lived daemon, never by the short-lived hook.
	if _, err := os.Stat(collectorPID); err != nil {
		t.Fatalf("orphan .collector-pid must be left untouched by the hook path: %v", err)
	}
}
