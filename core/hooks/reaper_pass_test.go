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

	"github.com/shakestzd/wipnote/core/sessionledger"
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

	rep := ReapStaleSessionsAndCollectors(database, projectDir, current, false, false, 0)

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
	rep2 := ReapStaleSessionsAndCollectors(database, projectDir, current, false, false, 0)
	if len(rep2.ReapedSessions) != 0 {
		t.Fatalf("second pass must be a no-op, got ReapedSessions=%v", rep2.ReapedSessions)
	}
}

// claimStatus reads a claim row's status directly so tests can assert that the
// reaper released (abandoned) a reaped session's claims.
func claimStatus(t *testing.T, database *sql.DB, claimID string) string {
	t.Helper()
	var status string
	if err := database.QueryRow(`SELECT status FROM claims WHERE claim_id=?`, claimID).Scan(&status); err != nil {
		t.Fatalf("query claim status %s: %v", claimID, err)
	}
	return status
}

// TestReapReleasesClaims proves FIX B: a REAL reap of a stale session releases
// (abandons) its still-active claims — mirroring normal SessionEnd — so a crashed
// session's claims no longer block claim paths. The reportOnly/dry-run branch
// must NOT release claims.
func TestReapReleasesClaims(t *testing.T) {
	mkSessDir := func(projectDir, sid string) string {
		return filepath.Join(projectDir, ".wipnote", "sessions", sid)
	}

	t.Run("real reap releases the claim", func(t *testing.T) {
		td := setupTestDB(t)
		database := td.DB
		projectDir := t.TempDir()
		td.addFeature("feat-reapclaim01", "feature", "reaper claim work", "in-progress")

		const current = "sess-current"
		seedReaperSession(t, database, current)

		// Stale-dead session that HOLDS a claim. seedFreshClaim gives it an
		// in-progress claim with a recent heartbeat, so to make it heartbeat-stale
		// we age the claim's heartbeat below.
		seedReaperSession(t, database, "sess-stale-claim")
		seedFreshClaim(t, database, "sess-stale-claim", "feat-reapclaim01")
		writeDeadPID(t, mkSessDir(projectDir, "sess-stale-claim"))
		// Age the claim heartbeat far past any TTL so SessionLivenessByHeartbeat
		// reports the session heartbeat-stale.
		if _, err := database.Exec(
			`UPDATE claims SET last_heartbeat_at=? WHERE claim_id=?`,
			time.Now().Add(-72*time.Hour).UTC().Format(time.RFC3339), "claim-sess-stale-claim",
		); err != nil {
			t.Fatalf("age claim heartbeat: %v", err)
		}

		rep := ReapStaleSessionsAndCollectors(database, projectDir, current, false, false, 0)
		if !contains(rep.ReapedSessions, "sess-stale-claim") {
			t.Fatalf("sess-stale-claim must be reaped; ReapedSessions=%v", rep.ReapedSessions)
		}
		if got := claimStatus(t, database, "claim-sess-stale-claim"); got != string(models.ClaimAbandoned) {
			t.Fatalf("reaped session's claim status=%q, want abandoned (claims must be released on reap)", got)
		}
	})

	t.Run("report-only does NOT release the claim", func(t *testing.T) {
		td := setupTestDB(t)
		database := td.DB
		projectDir := t.TempDir()
		td.addFeature("feat-reapclaim02", "feature", "reaper claim work", "in-progress")

		const current = "sess-current"
		seedReaperSession(t, database, current)

		seedReaperSession(t, database, "sess-stale-claim2")
		seedFreshClaim(t, database, "sess-stale-claim2", "feat-reapclaim02")
		writeDeadPID(t, mkSessDir(projectDir, "sess-stale-claim2"))
		if _, err := database.Exec(
			`UPDATE claims SET last_heartbeat_at=? WHERE claim_id=?`,
			time.Now().Add(-72*time.Hour).UTC().Format(time.RFC3339), "claim-sess-stale-claim2",
		); err != nil {
			t.Fatalf("age claim heartbeat: %v", err)
		}

		rep := ReapStaleSessionsAndCollectors(database, projectDir, current, false, true /*reportOnly*/, 0)
		if !contains(rep.ReapedSessions, "sess-stale-claim2") {
			t.Fatalf("report-only must list the would-reap session; got %v", rep.ReapedSessions)
		}
		if got := claimStatus(t, database, "claim-sess-stale-claim2"); got == string(models.ClaimAbandoned) {
			t.Fatalf("report-only must NOT release claims: status=%q, want still-active", got)
		}
	})
}

// TestReapStaleSessionsCap proves FIX C: with 2+ reap-eligible sessions and
// maxSessions=1, exactly one session is reaped per call (the remainder is left
// for the daemon).
func TestReapStaleSessionsCap(t *testing.T) {
	td := setupTestDB(t)
	database := td.DB
	projectDir := t.TempDir()

	mkSessDir := func(sid string) string {
		return filepath.Join(projectDir, ".wipnote", "sessions", sid)
	}

	const current = "sess-current"
	seedReaperSession(t, database, current)

	// Three reap-eligible sessions: no claim ⇒ heartbeat stale; dead pid.
	for _, sid := range []string{"sess-cap-a", "sess-cap-b", "sess-cap-c"} {
		seedReaperSession(t, database, sid)
		writeDeadPID(t, mkSessDir(sid))
	}

	rep := ReapStaleSessionsAndCollectors(database, projectDir, current, false, false, 1 /*maxSessions*/)
	if len(rep.ReapedSessions) != 1 {
		t.Fatalf("maxSessions=1 must reap exactly one session, got %d: %v", len(rep.ReapedSessions), rep.ReapedSessions)
	}

	// A second capped pass reaps the next one (the first is now completed).
	rep2 := ReapStaleSessionsAndCollectors(database, projectDir, current, false, false, 1)
	if len(rep2.ReapedSessions) != 1 {
		t.Fatalf("second capped pass must reap exactly one more session, got %d: %v", len(rep2.ReapedSessions), rep2.ReapedSessions)
	}
	if rep2.ReapedSessions[0] == rep.ReapedSessions[0] {
		t.Fatalf("second pass reaped the same session %q (cap/idempotency broken)", rep2.ReapedSessions[0])
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

	rep := ReapStaleSessionsAndCollectors(database, projectDir, current, true /*includeCollectors*/, true /*reportOnly*/, 0)

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

	rep := ReapStaleSessionsAndCollectors(database, projectDir, current, true /*includeCollectors*/, false, 0)

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
	repNil := ReapStaleSessionsAndCollectors(database, projectDir, current, true, false, 0)
	if len(repNil.ReapedCollectors) != 0 {
		t.Fatalf("nil ReapCollectorFn must reap no collectors, got %v", repNil.ReapedCollectors)
	}
}

// TestReapStaleSession_DoesNotRepeatAcrossProcesses is the regression guard for
// the infinite reap loop.
//
// TestReapStaleSessions above proves idempotency WITHIN one process, and it
// proves it via the projection row flipping to 'completed'. That is exactly the
// evidence that does not survive: the compatibility projection is process-local
// and in-memory, so the next wipnote process starts from a projection rehydrated
// out of the CANONICAL session ledger, where status is derived from
// Record.IsOpen(). If the reap never wrote the ledger, the next process sees the
// session as open, finds it just as stale, and reaps it again — forever.
//
// The second pass here therefore RESETS the projection row to 'active' to
// simulate that rehydration. Without the ledger close the reaper reaps again;
// with it, the ledger says the session already ended and the pass is a no-op.
func TestReapStaleSession_DoesNotRepeatAcrossProcesses(t *testing.T) {
	td := setupTestDB(t)
	database := td.DB
	projectDir := t.TempDir()

	const current = "sess-11111111-1111-4111-8111-111111111111"
	const dead = "sess-22222222-2222-4222-8222-222222222222"
	seedReaperSession(t, database, current)
	seedReaperSession(t, database, dead)
	writeDeadPID(t, filepath.Join(projectDir, ".wipnote", "sessions", dead))

	// The canonical ledger row SessionStart would have written.
	store := sessionledger.NewStore(filepath.Join(projectDir, ".wipnote"))
	if _, err := store.Open(sessionledger.Record{
		SessionID: dead,
		Harness:   "claude",
		StartedAt: time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("seed ledger row: %v", err)
	}

	rep := ReapStaleSessionsAndCollectors(database, projectDir, current, false, false, 0)
	if !contains(rep.ReapedSessions, dead) {
		t.Fatalf("first pass must reap %s; got %v", dead, rep.ReapedSessions)
	}

	// The reap must be durable, not merely projected.
	rec, found, err := store.Get(dead)
	if err != nil || !found {
		t.Fatalf("ledger row missing after reap (err=%v found=%v)", err, found)
	}
	if rec.IsOpen() {
		t.Fatal("reap left the canonical ledger row OPEN — the next process will reap it again")
	}

	// Simulate a fresh process: the projection is rebuilt from the ledger, and a
	// row the ledger still called open would come back as 'active'.
	if _, err := database.Exec(
		`UPDATE sessions SET status='active', completed_at=NULL WHERE session_id=?`, dead,
	); err != nil {
		t.Fatalf("simulate rehydration: %v", err)
	}

	rep2 := ReapStaleSessionsAndCollectors(database, projectDir, current, false, false, 0)
	if contains(rep2.ReapedSessions, dead) {
		t.Fatalf("%s was reaped a second time — this is the infinite reap loop", dead)
	}
}
