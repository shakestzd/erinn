package agent_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestEnsureSession_DaemonRouted verifies EnsureSessionRouted behaviour:
//   - When the session is new (cold path) and the daemon route acks, no direct
//     writable open is performed on the hot or cold path.
//   - When the session already exists (hot path), the daemon route is never called
//     and the read-only handle serves the exists-check.
//
// The test stubs agent.RouteSessionInsertFn with a function var seam that
// records whether it was called and returns the desired ack. A read-only DB
// opened on an in-memory handle serves the SELECT; a second in-memory handle
// represents the writable fallback — it is closed before EnsureSessionRouted
// returns, proving no writable open happened when daemon acks.
func TestEnsureSession_DaemonRouted(t *testing.T) {
	// --- cold path: daemon acks → no fallback to writableDB ---
	t.Run("cold_path_daemon_ack", func(t *testing.T) {
		const sessionID = "daemon-routed-cold-001"

		// Read-only DB: empty (no session row). Represents the shared read index.
		roDB := openMemDB(t)

		dir := t.TempDir()
		writeActiveSessionFile(t, dir, sessionID)
		t.Setenv("WIPNOTE_SESSION_ID", sessionID)
		t.Setenv("CLAUDE_SESSION_ID", "")
		t.Setenv("WIPNOTE_AGENT_ID", "test-agent")
		t.Setenv("CLAUDE_CODE", "")
		t.Setenv("CLAUDE_MODEL", "test-model")

		// Inject a fake seam that acks and records whether it was called.
		called := false
		prev := agent.RouteSessionInsertFn
		agent.RouteSessionInsertFn = func(_, _, _, _, _, _, _ string) bool {
			called = true
			return true // ack: daemon handled the insert
		}
		t.Cleanup(func() { agent.RouteSessionInsertFn = prev })

		// roborev-473 finding 2: the writable handle is now a LAZY thunk.
		// When the daemon acks the cold insert, the thunk must NEVER be invoked
		// (no writable open + migration paid). We assert that directly.
		opened := false
		got, err := agent.EnsureSessionRouted(roDB, func() (*sql.DB, error) {
			opened = true
			return openMemDB(t), nil
		}, dir, dir, 500*time.Millisecond)
		if err != nil {
			t.Fatalf("EnsureSessionRouted cold+daemon: %v", err)
		}
		if got != sessionID {
			t.Errorf("got session ID %q, want %q", got, sessionID)
		}
		if !called {
			t.Error("RouteSessionInsertFn was not called on cold path")
		}
		if opened {
			t.Error("lazy writable opener was invoked on the daemon-acked cold path; expected NO writable open")
		}
	})

	// --- hot path: session exists → daemon route never called ---
	t.Run("hot_path_read_only", func(t *testing.T) {
		const sessionID = "daemon-routed-hot-002"

		roDB := openMemDB(t)
		insertSession(t, roDB, sessionID)

		dir := t.TempDir()
		writeActiveSessionFile(t, dir, sessionID)
		t.Setenv("WIPNOTE_SESSION_ID", sessionID)
		t.Setenv("CLAUDE_SESSION_ID", "")

		called := false
		prev := agent.RouteSessionInsertFn
		agent.RouteSessionInsertFn = func(_, _, _, _, _, _, _ string) bool {
			called = true
			return true
		}
		t.Cleanup(func() { agent.RouteSessionInsertFn = prev })

		// roborev-473 finding 2: on the warm/exists path the lazy writable opener
		// must NEVER be invoked — the read-only exists-check short-circuits first.
		opened := false
		got, err := agent.EnsureSessionRouted(roDB, func() (*sql.DB, error) {
			opened = true
			return openMemDB(t), nil
		}, dir, dir, 500*time.Millisecond)
		if err != nil {
			t.Fatalf("EnsureSessionRouted hot: %v", err)
		}
		if got != sessionID {
			t.Errorf("got session ID %q, want %q", got, sessionID)
		}
		if called {
			t.Error("RouteSessionInsertFn was called on hot path; should not be")
		}
		if opened {
			t.Error("lazy writable opener was invoked on the warm/exists path; expected NO writable open")
		}
	})

	// --- cold path: daemon returns false → fallback to writableDB ---
	t.Run("cold_path_daemon_miss_fallback", func(t *testing.T) {
		const sessionID = "daemon-routed-fallback-003"

		roDB := openMemDB(t)

		dir := t.TempDir()
		writeActiveSessionFile(t, dir, sessionID)
		t.Setenv("WIPNOTE_SESSION_ID", sessionID)
		t.Setenv("CLAUDE_SESSION_ID", "")
		t.Setenv("WIPNOTE_AGENT_ID", "test-agent")
		t.Setenv("CLAUDE_CODE", "")
		t.Setenv("CLAUDE_MODEL", "")

		prev := agent.RouteSessionInsertFn
		agent.RouteSessionInsertFn = func(_, _, _, _, _, _, _ string) bool {
			return false // daemon miss → caller must fall back
		}
		t.Cleanup(func() { agent.RouteSessionInsertFn = prev })

		// roborev-473 finding 2: on a daemon MISS the lazy writable opener IS
		// invoked and EnsureSessionRouted writes the row through (and then closes)
		// the handle it returns. We back the fallback with an on-disk DB so the row
		// survives that close, then re-open it to assert the insert landed.
		fallbackPath := filepath.Join(dir, "fallback.db")
		opened := false
		got, err := agent.EnsureSessionRouted(roDB, func() (*sql.DB, error) {
			opened = true
			return db.Open(fallbackPath)
		}, dir, dir, 0)
		if err != nil {
			t.Fatalf("EnsureSessionRouted fallback: %v", err)
		}
		if got != sessionID {
			t.Errorf("got session ID %q, want %q", got, sessionID)
		}
		if !opened {
			t.Error("lazy writable opener was NOT invoked on the daemon-miss fallback path")
		}

		// Fallback must have written the row through the lazily-opened handle.
		verify, err := db.Open(fallbackPath)
		if err != nil {
			t.Fatalf("re-open fallback DB: %v", err)
		}
		defer verify.Close()
		var count int
		verify.QueryRow(`SELECT COUNT(*) FROM sessions WHERE session_id = ?`, sessionID).Scan(&count) //nolint:errcheck
		if count != 1 {
			t.Errorf("fallback DB has %d row(s); expected 1 after daemon-miss fallback", count)
		}
	})
}

// TestEnsureSessionWithTimeout_FailsFastUnderContention verifies the cold-path
// write does not stall for the full SQLite busy_timeout (5s) when another
// connection holds the write lock. With a short timeout the dedicated-connection
// busy_timeout bounds the wait, so EnsureSessionWithTimeout returns a busy error
// quickly instead of blocking the launcher's interactive path. Regression test
// for bug-504095f2.
func TestEnsureSessionWithTimeout_FailsFastUnderContention(t *testing.T) {
	const sessionID = "contention-session-005"
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wipnote.db")

	// Primary handle (schema-current) used by EnsureSessionWithTimeout.
	d1, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open d1: %v", err)
	}
	defer d1.Close()

	// Second connection holds the RESERVED write lock for the whole test.
	d2, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open d2: %v", err)
	}
	defer d2.Close()
	holder, err := d2.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire holder conn: %v", err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("acquire write lock: %v", err)
	}
	defer holder.ExecContext(context.Background(), "ROLLBACK") //nolint:errcheck

	writeActiveSessionFile(t, dir, sessionID)
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("CLAUDE_SESSION_ID", "")

	start := time.Now()
	_, err = agent.EnsureSessionWithTimeout(d1, dir, 300*time.Millisecond)
	elapsed := time.Since(start)

	// Must fail fast — well under the 5s busy_timeout. Allow generous slack for
	// slow CI, but anything near 5s means the bound was not applied.
	if elapsed > 2*time.Second {
		t.Fatalf("EnsureSessionWithTimeout blocked %v under contention; expected fast-fail near 300ms", elapsed)
	}
	if err == nil {
		t.Fatalf("expected a busy error while the write lock was held, got nil")
	}
}
