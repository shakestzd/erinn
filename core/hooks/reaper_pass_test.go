package hooks

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// --- shared reaper-test seeding helpers ---

// seedReaperSession inserts an active session row. setupTestDB already seeds
// "test-sess"; these are the additional candidates the reaper scans.
func seedReaperSession(t *testing.T, database *sql.DB, sid string) {
	t.Helper()
	_, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status)
		 VALUES (?,?,?,?)`,
		sid, "claude-code", time.Now().UTC().Format(time.RFC3339), "active",
	)
	if err != nil {
		t.Fatalf("seed session %s: %v", sid, err)
	}
}

// seedFreshClaim gives a session an in-progress claim with a recent
// last_heartbeat_at, so SessionLivenessByHeartbeat reports it live (heartbeat
// NOT stale). A session with no claim at all is heartbeat-stale.
func seedFreshClaim(t *testing.T, database *sql.DB, sid, workItemID string) {
	t.Helper()
	c := &models.Claim{
		ClaimID:        "claim-" + sid,
		WorkItemID:     workItemID,
		OwnerSessionID: sid,
		OwnerAgent:     "claude-code",
		Status:         models.ClaimInProgress,
	}
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem(%s): %v", sid, err)
	}
}

// writeAlivePID writes a .session-pid anchor for a process that is actually
// alive — this test process (os.Getpid()) plus its real /proc start time — so
// IsSessionProcessAlive returns true (owner LIVE → never reaped).
func writeAlivePID(t *testing.T, sessDir string) {
	t.Helper()
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sessDir, err)
	}
	pid := os.Getpid()
	content := strconv.Itoa(pid)
	if st, ok := readProcStartTime(pid); ok {
		content += "\n" + strconv.FormatUint(st, 10)
	}
	if err := os.WriteFile(filepath.Join(sessDir, ".session-pid"), []byte(content), 0o644); err != nil {
		t.Fatalf("write .session-pid: %v", err)
	}
}

// writeDeadPID writes a .session-pid anchor for a pid that is certainly not
// running (max int32), so IsSessionProcessAlive returns false (owner DEAD).
func writeDeadPID(t *testing.T, sessDir string) {
	t.Helper()
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sessDir, err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, ".session-pid"), []byte("2147483647\n1"), 0o644); err != nil {
		t.Fatalf("write .session-pid: %v", err)
	}
}

func sessionStatus(t *testing.T, database *sql.DB, sid string) string {
	t.Helper()
	var status string
	if err := database.QueryRow(`SELECT status FROM sessions WHERE session_id=?`, sid).Scan(&status); err != nil {
		t.Fatalf("query status %s: %v", sid, err)
	}
	return status
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestReapStaleSessions exercises the full {heartbeat fresh/stale} ×
// {.session-pid process-alive / process-dead / missing} matrix. Only the cell
// (stale heartbeat AND .session-pid present AND process-dead AND != current) is
// eligible. Everything else safe-degrades to "leave active".
func TestReapStaleSessions(t *testing.T) {
	td := setupTestDB(t)
	database := td.DB
	projectDir := t.TempDir()
	td.addFeature("feat-reapr01", "feature", "reaper work", "in-progress")

	mkSessDir := func(sid string) string {
		return filepath.Join(projectDir, ".wipnote", "sessions", sid)
	}

	// current: the running session — must NEVER be reaped even if it looked dead.
	const current = "sess-current"
	seedReaperSession(t, database, current)
	writeDeadPID(t, mkSessDir(current)) // no claim → stale; dead pid; but it's current

	// stale-dead: the ONLY reap-eligible session.
	seedReaperSession(t, database, "sess-stale-dead")
	writeDeadPID(t, mkSessDir("sess-stale-dead")) // no claim → heartbeat stale; pid dead

	// stale-alive: long-idle but the owning process is alive → LIVE → not reaped.
	seedReaperSession(t, database, "sess-stale-alive")
	writeAlivePID(t, mkSessDir("sess-stale-alive")) // no claim → stale; but process alive

	// stale-legacy: no .session-pid anchor → safe-degrade to LIVE → not reaped.
	seedReaperSession(t, database, "sess-stale-legacy")
	_ = os.MkdirAll(mkSessDir("sess-stale-legacy"), 0o755) // dir exists, no .session-pid

	// fresh-dead: heartbeat fresh (recent claim) → not stale → not reaped even
	// though the recorded pid is dead.
	seedReaperSession(t, database, "sess-fresh-dead")
	seedFreshClaim(t, database, "sess-fresh-dead", "feat-reapr01")
	writeDeadPID(t, mkSessDir("sess-fresh-dead"))

	rep := ReapStaleSessionsAndCollectors(database, projectDir, current, false, false)

	if !contains(rep.ReapedSessions, "sess-stale-dead") {
		t.Fatalf("sess-stale-dead must be reaped; ReapedSessions=%v", rep.ReapedSessions)
	}
	if len(rep.ReapedSessions) != 1 {
		t.Fatalf("exactly one session must be reaped, got %v", rep.ReapedSessions)
	}
	if got := sessionStatus(t, database, "sess-stale-dead"); got != "completed" {
		t.Fatalf("sess-stale-dead status=%q, want completed", got)
	}

	// Everything else stays active.
	for _, sid := range []string{current, "sess-stale-alive", "sess-stale-legacy", "sess-fresh-dead"} {
		if got := sessionStatus(t, database, sid); got != "active" {
			t.Fatalf("%s status=%q, want active (must not be reaped)", sid, got)
		}
	}

	// Idempotency: a second pass finds nothing — the already-completed row is no
	// longer active, so RowsAffected is 0.
	rep2 := ReapStaleSessionsAndCollectors(database, projectDir, current, false, false)
	if len(rep2.ReapedSessions) != 0 {
		t.Fatalf("second pass must be a no-op, got ReapedSessions=%v", rep2.ReapedSessions)
	}
}

// TestReapReportOnly proves the dry-run mode: the would-reap session is listed
// in ReapedSessions but its row is NOT mutated (still active), and the injected
// ReapCollectorFn is never invoked.
func TestReapReportOnly(t *testing.T) {
	td := setupTestDB(t)
	database := td.DB
	projectDir := t.TempDir()

	const current = "sess-current"
	seedReaperSession(t, database, current)

	seedReaperSession(t, database, "sess-stale-dead")
	sessDir := filepath.Join(projectDir, ".wipnote", "sessions", "sess-stale-dead")
	writeDeadPID(t, sessDir)
	// Give the dead session a .collector-pid so the collector phase would fire.
	if err := os.WriteFile(filepath.Join(sessDir, ".collector-pid"), []byte("2147483647\n1"), 0o644); err != nil {
		t.Fatalf("write .collector-pid: %v", err)
	}

	prev := ReapCollectorFn
	t.Cleanup(func() { ReapCollectorFn = prev })
	called := false
	ReapCollectorFn = func(string, time.Duration) (int, bool) {
		called = true
		return 0, false
	}

	rep := ReapStaleSessionsAndCollectors(database, projectDir, current, true /*includeCollectors*/, true /*reportOnly*/)

	if !contains(rep.ReapedSessions, "sess-stale-dead") {
		t.Fatalf("report-only must still list would-reap session; got %v", rep.ReapedSessions)
	}
	if got := sessionStatus(t, database, "sess-stale-dead"); got != "active" {
		t.Fatalf("report-only must NOT mutate the row: status=%q, want active", got)
	}
	if !contains(rep.ReapedCollectors, "sess-stale-dead") {
		t.Fatalf("report-only must list would-reap collector session; got %v", rep.ReapedCollectors)
	}
	if called {
		t.Fatal("report-only must NOT invoke ReapCollectorFn")
	}
}

// TestReapStaleSessionsAndCollectors_Seam proves the collector phase goes
// through the injected ReapCollectorFn seam, excludes the current session, and
// degrades to a no-op when the seam is nil.
func TestReapStaleSessionsAndCollectors_Seam(t *testing.T) {
	td := setupTestDB(t)
	database := td.DB
	projectDir := t.TempDir()

	const current = "sess-current"
	seedReaperSession(t, database, current)

	mkOrphanCollector := func(sid string) string {
		seedReaperSession(t, database, sid)
		sessDir := filepath.Join(projectDir, ".wipnote", "sessions", sid)
		writeDeadPID(t, sessDir) // owner process dead, no claim → heartbeat stale
		if err := os.WriteFile(filepath.Join(sessDir, ".collector-pid"), []byte("2147483647\n1"), 0o644); err != nil {
			t.Fatalf("write .collector-pid: %v", err)
		}
		return sessDir
	}

	// Two orphan collectors: one owned by the current session, one not.
	currentSessDir := filepath.Join(projectDir, ".wipnote", "sessions", current)
	_ = os.MkdirAll(currentSessDir, 0o755)
	if err := os.WriteFile(filepath.Join(currentSessDir, ".collector-pid"), []byte("2147483647\n1"), 0o644); err != nil {
		t.Fatalf("write current .collector-pid: %v", err)
	}
	orphanDir := mkOrphanCollector("sess-orphan")

	prev := ReapCollectorFn
	t.Cleanup(func() { ReapCollectorFn = prev })

	var calledDirs []string
	ReapCollectorFn = func(sessDir string, _ time.Duration) (int, bool) {
		calledDirs = append(calledDirs, sessDir)
		return 4242, true
	}

	rep := ReapStaleSessionsAndCollectors(database, projectDir, current, true /*includeCollectors*/, false)

	// The fake must have been invoked for the NON-current orphan only.
	if !contains(calledDirs, orphanDir) {
		t.Fatalf("ReapCollectorFn not invoked for orphan %q; calledDirs=%v", orphanDir, calledDirs)
	}
	if contains(calledDirs, currentSessDir) {
		t.Fatalf("ReapCollectorFn must NOT be invoked for the current session; calledDirs=%v", calledDirs)
	}
	if !contains(rep.ReapedCollectors, "sess-orphan:4242") {
		t.Fatalf("ReapedCollectors must record the killed orphan; got %v", rep.ReapedCollectors)
	}

	// nil seam ⇒ no panic, no collector action.
	ReapCollectorFn = nil
	repNil := ReapStaleSessionsAndCollectors(database, projectDir, current, true, false)
	if len(repNil.ReapedCollectors) != 0 {
		t.Fatalf("nil ReapCollectorFn must reap no collectors, got %v", repNil.ReapedCollectors)
	}
}
