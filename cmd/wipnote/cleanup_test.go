package main

import (
	"bytes"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/sessionledger"
)

// setupCleanupTestDB creates an in-memory SQLite DB for cleanup tests.
func setupCleanupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// insertSessionRow inserts a minimal session row for test setup.
func insertSessionRow(t *testing.T, db *sql.DB, sessionID string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status, created_at)
		 VALUES (?, 'claude-code', 'active', ?)`,
		sessionID, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert session %s: %v", sessionID, err)
	}
}

// sessionExists returns true if the session_id is present in the sessions table.
func sessionExists(t *testing.T, db *sql.DB, sessionID string) bool {
	t.Helper()
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE session_id = ?`, sessionID).Scan(&count)
	if err != nil {
		t.Fatalf("check session existence %s: %v", sessionID, err)
	}
	return count > 0
}

// writeMinimalSessionHTML writes a minimal (header-only) HTML file for a session.
func writeMinimalSessionHTML(t *testing.T, dir, sessionID string) {
	t.Helper()
	content := `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Session</title></head>
<body>
  <article id="` + sessionID + `" data-type="session" data-status="active"
           data-agent="claude-code" data-started-at="2026-04-09T10:00:00.000000"
           data-event-count="0">
    <header><h1>Session</h1></header>
  </article>
</body>
</html>`
	path := filepath.Join(dir, sessionID+".html")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write session HTML %s: %v", path, err)
	}
}

// ---------- shared helpers ----------

// setupHTMLGraphDir creates a .wipnote/ directory tree with an SQLite DB
// at the given root, and returns the wipnoteDir path.
func setupHTMLGraphDir(t *testing.T) (string, *sql.DB) {
	t.Helper()
	root := t.TempDir()
	hgDir := filepath.Join(root, ".wipnote")
	sessionsDir := filepath.Join(hgDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(hgDir, ".db"), 0o755); err != nil {
		t.Fatalf("mkdir .db: %v", err)
	}
	dbPath := filepath.Join(hgDir, ".db", "wipnote.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return hgDir, database
}

// captureStdout captures os.Stdout during fn() and returns what was printed.
// Used across the package, not just here.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy: %v", err)
	}
	return buf.String()
}

// ---------- ghost-sessions removal ----------

// TestCleanupGhostSessions_Removed pins the removal of `cleanup ghost-sessions`
// (feat-fc3cc9e0), which this file used to cover with three unit tests and
// three integration tests.
//
// The command deleted rows from `sessions` that had no HTML file and no
// messages/tool_calls/agent_events. All four tables are now a per-process
// projection hydrated from canonical artifacts on every openDB, so the DELETE
// committed to a throwaway database and reindexSessionLedger re-inserted every
// "deleted" row on the next command — run twice, it reported the same
// deletions both times.
//
// There is no canonical redirect. A session's canonical record IS its ledger
// entry, so a row with no HTML but a live ledger entry is not a ghost; it is a
// session whose HTML was never rendered, and deleting the ledger entry would
// destroy provenance rather than tidy up after it.
//
// `cleanup orphan-sessions` is unaffected and still covered below: it removes
// NDJSON directories from disk, which is durable, and uses the projection only
// to ask which session dirs have no session — a question the ledger answers.
func TestCleanupGhostSessions_Removed(t *testing.T) {
	data, err := os.ReadFile("cleanup.go")
	if err != nil {
		t.Fatalf("read cleanup.go: %v", err)
	}
	src := string(data)
	// The doc comment in cleanup.go deliberately names the removed command and
	// explains why it went, so the scan targets the implementation, not the
	// prose.
	for _, forbidden := range []string{
		"runCleanupGhostSessions",
		"DELETE FROM sessions",
		"cleanupGhostSessionsCmd",
	} {
		if strings.Contains(src, forbidden) {
			t.Errorf("cleanup.go still references %q — a delete against the projection cannot outlive the process", forbidden)
		}
	}

	sub := cleanupCmd().Commands()
	for _, c := range sub {
		if c.Name() == "ghost-sessions" {
			t.Error("cleanup still registers the ghost-sessions subcommand")
		}
	}
	if len(sub) == 0 {
		t.Error("cleanup lost all subcommands; orphan-sessions must remain")
	}
}

// ---------- helpers ----------

func containsID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

// ---------- orphan-sessions tests ----------

// makeOrphanSessionDir creates a session directory under wipnoteDir/sessions/<id>/
// with an events.ndjson file and optionally back-dates it.
func makeOrphanSessionDir(t *testing.T, wipnoteDir, sessionID string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(wipnoteDir, "sessions", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
	ndjson := filepath.Join(dir, "events.ndjson")
	if err := os.WriteFile(ndjson, []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile events.ndjson: %v", err)
	}
	if age > 0 {
		oldTime := time.Now().Add(-age)
		_ = os.Chtimes(dir, oldTime, oldTime)
		_ = os.Chtimes(ndjson, oldTime, oldTime)
	}
	return dir
}

// TestRunCleanupOrphanSessions_ListDoesNotDelete verifies that --list (no
// --delete) prints candidates but does not remove any directories.
func TestRunCleanupOrphanSessions_ListDoesNotDelete(t *testing.T) {
	hgDir, database := setupHTMLGraphDir(t)
	// Create an orphan session directory (no DB row).
	orphanDir := makeOrphanSessionDir(t, hgDir, "orphan-list-cli-01", 20*24*time.Hour)
	database.Close()

	origFlag := projectDirFlag
	projectDirFlag = filepath.Dir(hgDir)
	defer func() { projectDirFlag = origFlag }()

	output := captureStdout(t, func() {
		err := runCleanupOrphanSessions(false /* delete */, false /* yes */)
		if err != nil {
			t.Errorf("runCleanupOrphanSessions list: %v", err)
		}
	})

	// Directory must still exist (dry-run / list).
	if _, err := os.Stat(orphanDir); err != nil {
		t.Errorf("orphan dir should still exist after list: %v", err)
	}

	// Output should mention the orphan or a summary line.
	if len(output) == 0 {
		t.Error("expected non-empty output from orphan-sessions list")
	}
}

// TestRunCleanupOrphanSessions_DeleteRequiresYes verifies --delete without
// --yes returns an error.
func TestRunCleanupOrphanSessions_DeleteRequiresYes(t *testing.T) {
	hgDir, database := setupHTMLGraphDir(t)
	makeOrphanSessionDir(t, hgDir, "orphan-noyes-01", 20*24*time.Hour)
	database.Close()

	origFlag := projectDirFlag
	projectDirFlag = filepath.Dir(hgDir)
	defer func() { projectDirFlag = origFlag }()

	err := runCleanupOrphanSessions(true /* delete */, false /* yes */)
	if err == nil {
		t.Error("expected error when --delete is used without --yes")
	}
}

// TestRunCleanupOrphanSessions_DeleteWithYesRemovesEligible verifies that
// --delete --yes removes orphan directories older than OrphanRetentionDays.
func TestRunCleanupOrphanSessions_DeleteWithYesRemovesEligible(t *testing.T) {
	hgDir, database := setupHTMLGraphDir(t)

	// Old orphan (20 days > 14-day retention, no recent writes after chtimes).
	oldOrphanDir := makeOrphanSessionDir(t, hgDir, "old-orphan-del-01", 20*24*time.Hour)
	// Young orphan (5 days < 14-day retention) — must NOT be deleted.
	youngOrphanDir := makeOrphanSessionDir(t, hgDir, "young-orphan-del-01", 5*24*time.Hour)

	database.Close()

	origFlag := projectDirFlag
	projectDirFlag = filepath.Dir(hgDir)
	defer func() { projectDirFlag = origFlag }()

	err := runCleanupOrphanSessions(true /* delete */, true /* yes */)
	if err != nil {
		t.Fatalf("runCleanupOrphanSessions delete: %v", err)
	}

	// Old orphan must be gone.
	if _, err := os.Stat(oldOrphanDir); !os.IsNotExist(err) {
		t.Errorf("old orphan dir should be deleted, got stat err: %v", err)
	}

	// Young orphan must remain.
	if _, err := os.Stat(youngOrphanDir); err != nil {
		t.Errorf("young orphan dir should still exist: %v", err)
	}
}

// TestRunCleanupOrphanSessions_NoOrphans verifies clean output when there
// are no orphan directories.
func TestRunCleanupOrphanSessions_NoOrphans(t *testing.T) {
	hgDir, database := setupHTMLGraphDir(t)
	database.Close()

	// A session is "known" when the CANONICAL session ledger says so. This
	// used to INSERT straight into a file-backed sessions table; the command
	// now reads a projection hydrated from the ledger, so seeding the table
	// directly seeded a database nothing opens and the known session read as
	// an orphan (feat-fc3cc9e0).
	seedSessionLedgerEntry(t, hgDir, "3f6b1c2e-8a44-4d9b-9f21-77c0de5a1b34")
	makeOrphanSessionDir(t, hgDir, "3f6b1c2e-8a44-4d9b-9f21-77c0de5a1b34", 0)

	origFlag := projectDirFlag
	projectDirFlag = filepath.Dir(hgDir)
	defer func() { projectDirFlag = origFlag }()

	output := captureStdout(t, func() {
		err := runCleanupOrphanSessions(false /* delete */, false /* yes */)
		if err != nil {
			t.Errorf("runCleanupOrphanSessions no-orphans: %v", err)
		}
	})

	if !strings.Contains(output, "No orphan") {
		t.Errorf("expected 'No orphan' message, got: %q", output)
	}
}

// seedSessionLedgerEntry writes one open session into the canonical session
// ledger, which is what hydrates the projection's `sessions` table.
func seedSessionLedgerEntry(t *testing.T, wipnoteDir, sessionID string) {
	t.Helper()
	if _, err := sessionledger.NewStore(wipnoteDir).Open(sessionledger.Record{
		SessionID: sessionID,
		Harness:   "claude-code",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed session ledger %s: %v", sessionID, err)
	}
}
