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

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
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

// ---------- collectSessionHTMLIDs tests ----------

func TestCollectSessionHTMLIDs_Empty(t *testing.T) {
	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, ".htmlgraph", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	ids, err := collectSessionHTMLIDs(filepath.Join(dir, ".htmlgraph"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected empty set, got %d entries", len(ids))
	}
}

func TestCollectSessionHTMLIDs_MissingDir(t *testing.T) {
	dir := t.TempDir()
	// .htmlgraph/sessions/ does not exist — should return empty set, not error.
	ids, err := collectSessionHTMLIDs(filepath.Join(dir, ".htmlgraph"))
	if err != nil {
		t.Fatalf("unexpected error for missing dir: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected empty set, got %d entries", len(ids))
	}
}

func TestCollectSessionHTMLIDs_MixedFiles(t *testing.T) {
	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, ".htmlgraph", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Write HTML files.
	htmlFiles := []string{
		"sess-aabbccdd.html",
		"sess-11223344.html",
	}
	for _, f := range htmlFiles {
		if err := os.WriteFile(filepath.Join(sessionsDir, f), []byte("<html/>"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	// Write non-HTML files — should be ignored.
	nonHTMLFiles := []string{"notes.yaml", "README.txt", "data.json"}
	for _, f := range nonHTMLFiles {
		if err := os.WriteFile(filepath.Join(sessionsDir, f), []byte("ignored"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}

	ids, err := collectSessionHTMLIDs(filepath.Join(dir, ".htmlgraph"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(ids) != 2 {
		t.Fatalf("expected 2 IDs, got %d: %v", len(ids), ids)
	}
	if _, ok := ids["sess-aabbccdd"]; !ok {
		t.Errorf("expected sess-aabbccdd in result")
	}
	if _, ok := ids["sess-11223344"]; !ok {
		t.Errorf("expected sess-11223344 in result")
	}
	// Non-HTML filenames should NOT appear.
	for _, f := range nonHTMLFiles {
		if _, ok := ids[f]; ok {
			t.Errorf("non-HTML file %q should not be in result", f)
		}
	}
}

// ---------- findContentFreeSessionIDs tests ----------

func TestFindContentFreeSessionIDs_NoMessages(t *testing.T) {
	database := setupCleanupTestDB(t)

	ghostID := "ghost-no-msg-00000001"
	withMsgID := "live-with-msg-0000001"
	insertSessionRow(t, database, ghostID)
	insertSessionRow(t, database, withMsgID)

	// Insert a message for the live session.
	_, err := database.Exec(
		`INSERT INTO messages (session_id, ordinal, role, content) VALUES (?, 1, 'user', 'hello')`,
		withMsgID,
	)
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}

	ids, err := findContentFreeSessionIDs(database)
	if err != nil {
		t.Fatalf("findContentFreeSessionIDs: %v", err)
	}

	if !containsID(ids, ghostID) {
		t.Errorf("ghost session %s should be in candidates", ghostID)
	}
	if containsID(ids, withMsgID) {
		t.Errorf("session with message %s should NOT be in candidates", withMsgID)
	}
}

func TestFindContentFreeSessionIDs_NoToolCalls(t *testing.T) {
	database := setupCleanupTestDB(t)

	ghostID := "ghost-no-tc-000000001"
	withTCID := "live-with-tc-00000001"
	insertSessionRow(t, database, ghostID)
	insertSessionRow(t, database, withTCID)

	// Insert a tool_call for the live session.
	_, err := database.Exec(
		`INSERT INTO tool_calls (session_id, tool_name, category) VALUES (?, 'Bash', 'Execution')`,
		withTCID,
	)
	if err != nil {
		t.Fatalf("insert tool_call: %v", err)
	}

	ids, err := findContentFreeSessionIDs(database)
	if err != nil {
		t.Fatalf("findContentFreeSessionIDs: %v", err)
	}

	if !containsID(ids, ghostID) {
		t.Errorf("ghost session %s should be in candidates", ghostID)
	}
	if containsID(ids, withTCID) {
		t.Errorf("session with tool_call %s should NOT be in candidates", withTCID)
	}
}

func TestFindContentFreeSessionIDs_NoAgentEvents(t *testing.T) {
	database := setupCleanupTestDB(t)

	ghostID := "ghost-no-ae-000000001"
	withAEID := "live-with-ae-00000001"
	insertSessionRow(t, database, ghostID)
	insertSessionRow(t, database, withAEID)

	// Insert an agent_event for the live session.
	_, err := database.Exec(
		`INSERT INTO agent_events (event_id, agent_id, event_type, timestamp, session_id)
		 VALUES ('evt-test-ae-001', 'claude-code', 'tool_call', CURRENT_TIMESTAMP, ?)`,
		withAEID,
	)
	if err != nil {
		t.Fatalf("insert agent_event: %v", err)
	}

	ids, err := findContentFreeSessionIDs(database)
	if err != nil {
		t.Fatalf("findContentFreeSessionIDs: %v", err)
	}

	if !containsID(ids, ghostID) {
		t.Errorf("ghost session %s should be in candidates", ghostID)
	}
	if containsID(ids, withAEID) {
		t.Errorf("session with agent_event %s should NOT be in candidates", withAEID)
	}
}

// ---------- runCleanupGhostSessions integration tests ----------

// setupHTMLGraphDir creates a .htmlgraph/ directory tree with an SQLite DB
// at the given root, and returns the htmlgraphDir path.
func setupHTMLGraphDir(t *testing.T) (string, *sql.DB) {
	t.Helper()
	root := t.TempDir()
	hgDir := filepath.Join(root, ".htmlgraph")
	sessionsDir := filepath.Join(hgDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(hgDir, ".db"), 0o755); err != nil {
		t.Fatalf("mkdir .db: %v", err)
	}
	dbPath := filepath.Join(hgDir, ".db", "htmlgraph.db")
	t.Setenv("HTMLGRAPH_DB_PATH", dbPath)
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return hgDir, database
}

// captureStdout captures os.Stdout during fn() and returns what was printed.
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

// TestRunCleanupGhostSessions_NeverDeletesHTMLBacked is the critical regression
// test: a session row with zero events but an HTML file on disk must NOT be deleted.
func TestRunCleanupGhostSessions_NeverDeletesHTMLBacked(t *testing.T) {
	hgDir, database := setupHTMLGraphDir(t)

	sessionID := "html-backed-sess-0001"
	insertSessionRow(t, database, sessionID)

	// Write an HTML file — this is the canonical record.
	writeMinimalSessionHTML(t, filepath.Join(hgDir, "sessions"), sessionID)

	// Close the DB so runCleanupGhostSessions can open it fresh.
	database.Close()

	// Point project-dir flag to the temp project root.
	origFlag := projectDirFlag
	projectDirFlag = filepath.Dir(hgDir)
	defer func() { projectDirFlag = origFlag }()

	err := runCleanupGhostSessions(false)
	if err != nil {
		t.Fatalf("runCleanupGhostSessions: %v", err)
	}

	// Verify the row still exists.
	db2, err := dbpkg.Open(filepath.Join(hgDir, ".db", "htmlgraph.db"))
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer db2.Close()

	if !sessionExists(t, db2, sessionID) {
		t.Errorf("HTML-backed session %s was wrongly deleted", sessionID)
	}
}

// TestRunCleanupGhostSessions_DeletesTrueGhosts verifies that a session row
// with zero events AND no HTML file is deleted.
func TestRunCleanupGhostSessions_DeletesTrueGhosts(t *testing.T) {
	hgDir, database := setupHTMLGraphDir(t)

	ghostID := "true-ghost-sess-00001"
	insertSessionRow(t, database, ghostID)
	// No HTML file written — this is a true ghost.
	database.Close()

	origFlag := projectDirFlag
	projectDirFlag = filepath.Dir(hgDir)
	defer func() { projectDirFlag = origFlag }()

	err := runCleanupGhostSessions(false)
	if err != nil {
		t.Fatalf("runCleanupGhostSessions: %v", err)
	}

	db2, err := dbpkg.Open(filepath.Join(hgDir, ".db", "htmlgraph.db"))
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer db2.Close()

	if sessionExists(t, db2, ghostID) {
		t.Errorf("true ghost session %s should have been deleted", ghostID)
	}
}

// TestRunCleanupGhostSessions_DryRunNoDelete verifies that --dry-run lists
// ghosts but does not delete them.
func TestRunCleanupGhostSessions_DryRunNoDelete(t *testing.T) {
	hgDir, database := setupHTMLGraphDir(t)

	ghostID := "dry-run-ghost-000001"
	insertSessionRow(t, database, ghostID)
	// No HTML file — true ghost.
	database.Close()

	origFlag := projectDirFlag
	projectDirFlag = filepath.Dir(hgDir)
	defer func() { projectDirFlag = origFlag }()

	var output string
	output = captureStdout(t, func() {
		err := runCleanupGhostSessions(true /* dryRun */)
		if err != nil {
			t.Errorf("runCleanupGhostSessions dry-run: %v", err)
		}
	})

	// Ghost should appear in output.
	if !strings.Contains(output, ghostID[:16]) && !strings.Contains(output, "ghost") {
		t.Errorf("dry-run output should mention ghost session, got: %q", output)
	}
	if !strings.Contains(output, "Dry run") {
		t.Errorf("dry-run output should contain 'Dry run', got: %q", output)
	}

	// Row must still exist.
	db2, err := dbpkg.Open(filepath.Join(hgDir, ".db", "htmlgraph.db"))
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer db2.Close()

	if !sessionExists(t, db2, ghostID) {
		t.Errorf("dry-run should not delete ghost session %s", ghostID)
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
