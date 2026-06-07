package db_test

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// TestMigration_SessionFilesTable asserts the v8 migration registers the
// session_files step, bumps the schema version, and that a freshly-Open'd DB
// has the table with the (session_id,file_path) unique key reachable via the
// idempotent upsert.
func TestMigration_SessionFilesTable(t *testing.T) {
	if db.CurrentSchemaVersion() < 8 {
		t.Fatalf("currentSchemaVersion = %d, want >= 8", db.CurrentSchemaVersion())
	}
	names := db.MigrationStepNames()
	found := false
	for _, n := range names {
		if n == "008_session_files" {
			found = true
		}
	}
	if !found {
		t.Fatalf("migration step 008_session_files not registered; got %v", names)
	}

	database := setupTestDB(t)
	defer database.Close()

	var name string
	err := database.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='session_files'`,
	).Scan(&name)
	if err != nil || name != "session_files" {
		t.Fatalf("session_files table missing: name=%q err=%v", name, err)
	}

	if err := db.UpsertSessionFile(database, "sess-test", "/p/a.go", "write"); err != nil {
		t.Fatalf("UpsertSessionFile 1: %v", err)
	}
	if err := db.UpsertSessionFile(database, "sess-test", "/p/a.go", "edit"); err != nil {
		t.Fatalf("UpsertSessionFile 2: %v", err)
	}
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM session_files WHERE session_id='sess-test'`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 deduped session_files row, got %d", n)
	}
	var op string
	database.QueryRow(`SELECT operation FROM session_files WHERE session_id='sess-test'`).Scan(&op)
	if op != "edit" {
		t.Fatalf("operation not updated on conflict: got %q want edit", op)
	}
}

// TestClaimlessSessionFilesLedger asserts the claimless ledger round-trips:
// a touch with no feature is recorded in session_files and surfaced by
// ListClaimlessFilesBySession (newest first), independent of feature_files.
func TestClaimlessSessionFilesLedger(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	if err := db.UpsertSessionFile(database, "sess-test", "/p/old.go", "write"); err != nil {
		t.Fatalf("upsert old: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := db.UpsertSessionFile(database, "sess-test", "/p/new.go", "edit"); err != nil {
		t.Fatalf("upsert new: %v", err)
	}

	got, err := db.ListClaimlessFilesBySession(database, "sess-test")
	if err != nil {
		t.Fatalf("ListClaimlessFilesBySession: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 claimless files, got %d (%v)", len(got), got)
	}
	if got[0].FilePath != "/p/new.go" {
		t.Errorf("newest-first ordering broken: got[0]=%q want /p/new.go", got[0].FilePath)
	}

	if err := db.UpsertSessionFile(database, "", "/p/x.go", "write"); err != nil {
		t.Errorf("empty session no-op should not error: %v", err)
	}
	if err := db.UpsertSessionFile(database, "sess-test", "", "write"); err != nil {
		t.Errorf("empty path no-op should not error: %v", err)
	}

	ff, _ := db.ListFilesBySession(database, "sess-test")
	if len(ff) != 0 {
		t.Errorf("claimless touches must not appear in feature_files reader, got %d", len(ff))
	}
}

// TestLivenessByHeartbeat is the core honest-liveness assertion: liveness is
// derived from claim-heartbeat recency, NOT sessions.status.
func TestLivenessByHeartbeat(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	insertTestFeatures(t, database, "feat-live")

	threshold := 120 * time.Second

	if db.SessionLivenessByHeartbeat(database, "sess-test", threshold) {
		t.Fatal("session with no claims must not be live")
	}

	c := &models.Claim{
		ClaimID:        "claim-live",
		WorkItemID:     "feat-live",
		OwnerSessionID: "sess-test",
		OwnerAgent:     "claude-code",
		Status:         models.ClaimInProgress,
	}
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	if !db.SessionLivenessByHeartbeat(database, "sess-test", threshold) {
		t.Fatal("session with fresh claim heartbeat must be live")
	}

	old := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	if _, err := database.Exec(
		`UPDATE claims SET last_heartbeat_at = ? WHERE claim_id = 'claim-live'`, old,
	); err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}
	var status string
	database.QueryRow(`SELECT status FROM sessions WHERE session_id='sess-test'`).Scan(&status)
	if status != "active" {
		t.Fatalf("precondition: session should still be status=active, got %q", status)
	}
	if db.SessionLivenessByHeartbeat(database, "sess-test", threshold) {
		t.Fatal("stale heartbeat must report not-live regardless of sessions.status")
	}
}

// TestStaleActiveSessionsNotLive_Bug6c3e8252 reproduces bug-6c3e8252: legacy
// status='active' ghost sessions whose claim heartbeats are ancient must be
// reported not-live once liveness is heartbeat-derived.
func TestStaleActiveSessionsNotLive_Bug6c3e8252(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	ancient := time.Now().UTC().AddDate(0, -2, 0).Format(time.RFC3339)

	// Distinct work items per ghost: one-active-claim-per-work-item is an
	// unrelated constraint; bug-6c3e8252 is about distinct stale sessions.
	ghosts := []string{"sess-191b813d", "sess-4d136dad", "sess-73f58d23"}
	for i, sid := range ghosts {
		feat := "feat-ghost" + sid[5:11]
		insertTestFeatures(t, database, feat)
		if _, err := database.Exec(
			`INSERT INTO sessions (session_id, agent_assigned, status, created_at)
			 VALUES (?, 'claude-code', 'active', ?)`, sid, ancient,
		); err != nil {
			t.Fatalf("insert ghost session %s: %v", sid, err)
		}
		c := &models.Claim{
			ClaimID:        "claim-ghost-" + sid,
			WorkItemID:     feat,
			OwnerSessionID: sid,
			OwnerAgent:     "claude-code",
			Status:         models.ClaimInProgress,
		}
		if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
			t.Fatalf("ClaimItem ghost %d: %v", i, err)
		}
		if _, err := database.Exec(
			`UPDATE claims SET last_heartbeat_at = ? WHERE claim_id = ?`,
			ancient, "claim-ghost-"+sid,
		); err != nil {
			t.Fatalf("backdate ghost heartbeat: %v", err)
		}
	}

	threshold := 120 * time.Second
	staleLive := 0
	for _, sid := range ghosts {
		var st string
		database.QueryRow(`SELECT status FROM sessions WHERE session_id=?`, sid).Scan(&st)
		if st != "active" {
			t.Fatalf("precondition: %s should still be status=active, got %q", sid, st)
		}
		if db.SessionLivenessByHeartbeat(database, sid, threshold) {
			staleLive++
		}
	}
	if staleLive != 0 {
		t.Fatalf("bug-6c3e8252 not fixed: %d stale status=active ghost sessions still report live (want 0)", staleLive)
	}

	insertTestFeatures(t, database, "feat-freshok")
	c := &models.Claim{
		ClaimID:        "claim-fresh",
		WorkItemID:     "feat-freshok",
		OwnerSessionID: "sess-test",
		OwnerAgent:     "claude-code",
		Status:         models.ClaimInProgress,
	}
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem fresh: %v", err)
	}
	database.Exec(`UPDATE claims SET last_heartbeat_at = ? WHERE claim_id = 'claim-fresh'`, now)
	if !db.SessionLivenessByHeartbeat(database, "sess-test", threshold) {
		t.Fatal("a session with a current heartbeat must still be live")
	}
}

// TestLivenessStalenessThreshold_Default asserts the default threshold is the
// 2x heartbeat-interval (120s) and that a missing config falls back to it.
func TestLivenessStalenessThreshold_Default(t *testing.T) {
	if got := db.LivenessStalenessThreshold(""); got != 120*time.Second {
		t.Fatalf("default threshold (no project dir) = %v, want 120s", got)
	}
	if got := db.LivenessStalenessThreshold(t.TempDir()); got != 120*time.Second {
		t.Fatalf("default threshold (no config.json) = %v, want 120s", got)
	}
}
