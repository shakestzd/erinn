package agent_test

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/agent"
	"github.com/shakestzd/wipnote/core/db"
	_ "modernc.org/sqlite"
)

// openMemDB opens an in-memory SQLite database with the full wipnote schema.
func openMemDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("openMemDB: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// writeActiveSessionFile writes a minimal .active-session JSON file so
// ResolveSessionID can find a session ID from the file path.
func writeActiveSessionFile(t *testing.T, dir, sessionID string) {
	t.Helper()
	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	data := map[string]interface{}{
		"session_id": sessionID,
		"timestamp":  1.0,
	}
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal active session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wipnoteDir, ".active-session"), b, 0o644); err != nil {
		t.Fatalf("write .active-session: %v", err)
	}
}

// insertSession inserts a session row directly so the hot path has something to find.
func insertSession(t *testing.T, database *sql.DB, sessionID string) {
	t.Helper()
	_, err := database.Exec(`
		INSERT INTO sessions (session_id, agent_assigned, created_at, status)
		VALUES (?, ?, datetime('now'), 'active')`,
		sessionID, "test-agent",
	)
	if err != nil {
		t.Fatalf("insertSession: %v", err)
	}
}

// TestEnsureSession_HotPath verifies that when the session already exists in DB,
// EnsureSession returns the ID without performing an INSERT.
func TestEnsureSession_HotPath(t *testing.T) {
	const sessionID = "hot-path-session-001"

	database := openMemDB(t)
	insertSession(t, database, sessionID)

	dir := t.TempDir()
	writeActiveSessionFile(t, dir, sessionID)
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("CLAUDE_SESSION_ID", "")

	// Count rows before.
	var beforeCount int
	database.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&beforeCount) //nolint:errcheck

	got, err := agent.EnsureSession(database, dir)
	if err != nil {
		t.Fatalf("EnsureSession hot path: %v", err)
	}
	if got != sessionID {
		t.Errorf("got session ID %q, want %q", got, sessionID)
	}

	// Count rows after — must not have increased (hot path = no INSERT).
	var afterCount int
	database.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&afterCount) //nolint:errcheck
	if afterCount != beforeCount {
		t.Errorf("hot path inserted a row: before=%d after=%d", beforeCount, afterCount)
	}
}

// TestEnsureSession_ColdPath verifies that when the session does not exist in DB,
// EnsureSession inserts a minimal session row and returns the ID.
func TestEnsureSession_ColdPath(t *testing.T) {
	const sessionID = "cold-path-session-002"

	database := openMemDB(t)

	dir := t.TempDir()
	writeActiveSessionFile(t, dir, sessionID)
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("WIPNOTE_AGENT_ID", "test-agent")
	t.Setenv("CLAUDE_CODE", "")
	t.Setenv("CLAUDE_MODEL", "test-model")

	got, err := agent.EnsureSession(database, dir)
	if err != nil {
		t.Fatalf("EnsureSession cold path: %v", err)
	}
	if got != sessionID {
		t.Errorf("got session ID %q, want %q", got, sessionID)
	}

	// Verify the row was inserted.
	var count int
	database.QueryRow(`SELECT COUNT(*) FROM sessions WHERE session_id = ?`, sessionID).Scan(&count) //nolint:errcheck
	if count != 1 {
		t.Errorf("expected 1 session row, got %d", count)
	}
}

// TestEnsureSession_Transient verifies that "cli-*" session IDs skip the DB entirely.
func TestEnsureSession_Transient(t *testing.T) {
	database := openMemDB(t)

	dir := t.TempDir()
	// Clear env so ResolveSessionID falls through to the generated "cli-<pid>-<ts>" form.
	t.Setenv("WIPNOTE_SESSION_ID", "")
	t.Setenv("CLAUDE_SESSION_ID", "")
	// No .active-session file, so a transient ID is generated.

	got, err := agent.EnsureSession(database, dir)
	if err != nil {
		t.Fatalf("EnsureSession transient: %v", err)
	}
	if !strings.HasPrefix(got, "cli-") {
		t.Errorf("expected transient ID with 'cli-' prefix, got %q", got)
	}

	// No rows should have been inserted.
	var count int
	database.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&count) //nolint:errcheck
	if count != 0 {
		t.Errorf("transient path should not insert DB rows, got %d", count)
	}
}

// TestEnsureSession_SetsEnv verifies that os.Setenv("WIPNOTE_SESSION_ID") is
// called after a successful resolve, so downstream EnvSessionID() works.
func TestEnsureSession_SetsEnv(t *testing.T) {
	const sessionID = "env-set-session-003"

	database := openMemDB(t)
	insertSession(t, database, sessionID)

	dir := t.TempDir()
	writeActiveSessionFile(t, dir, sessionID)
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("CLAUDE_SESSION_ID", "")

	// Clear the env var so we can verify it's re-set by EnsureSession.
	os.Unsetenv("WIPNOTE_SESSION_ID")

	_, err := agent.EnsureSession(database, dir)
	if err != nil {
		t.Fatalf("EnsureSession env set: %v", err)
	}

	got := os.Getenv("WIPNOTE_SESSION_ID")
	if got != sessionID {
		t.Errorf("WIPNOTE_SESSION_ID not set: got %q, want %q", got, sessionID)
	}
}

// TestEnsureSession_ColdPath_WritesActiveSession verifies that on cold path,
// the .active-session file is written (or updated) with the new session ID.
func TestEnsureSession_ColdPath_WritesActiveSession(t *testing.T) {
	const sessionID = "cold-active-session-004"

	database := openMemDB(t)

	dir := t.TempDir()
	// Create the .wipnote directory but not .active-session.
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("WIPNOTE_AGENT_ID", "test-agent")
	t.Setenv("CLAUDE_CODE", "")
	t.Setenv("CLAUDE_MODEL", "")

	_, err := agent.EnsureSession(database, dir)
	if err != nil {
		t.Fatalf("EnsureSession cold path active session write: %v", err)
	}

	// Verify .active-session was written and contains the session ID.
	activePath := filepath.Join(dir, ".wipnote", ".active-session")
	b, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatalf("reading .active-session: %v", err)
	}
	var data map[string]interface{}
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatalf("parsing .active-session JSON: %v", err)
	}
	if data["session_id"] != sessionID {
		t.Errorf(".active-session session_id=%q, want %q", data["session_id"], sessionID)
	}
}
