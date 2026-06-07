package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/db"
)

// openIsolatedDB opens a file-based SQLite in a temp dir so tests
// don't interfere with the shared in-memory database used by other suites.
func openIsolatedDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestSetAndGetActiveWorkItem(t *testing.T) {
	database := openIsolatedDB(t)

	// Empty table returns "".
	got := db.GetActiveWorkItem(database, "sess-1", "agent-1")
	if got != "" {
		t.Fatalf("want empty, got %q", got)
	}

	// Set a claim and read it back.
	if err := db.SetActiveWorkItem(database, "sess-1", "agent-1", "feat-abc"); err != nil {
		t.Fatalf("SetActiveWorkItem: %v", err)
	}
	got = db.GetActiveWorkItem(database, "sess-1", "agent-1")
	if got != "feat-abc" {
		t.Fatalf("want feat-abc, got %q", got)
	}
}

func TestSetActiveWorkItem_Overwrite(t *testing.T) {
	database := openIsolatedDB(t)

	if err := db.SetActiveWorkItem(database, "sess-1", "agent-1", "feat-old"); err != nil {
		t.Fatalf("first set: %v", err)
	}
	// Overwrite with a different item — must not error.
	if err := db.SetActiveWorkItem(database, "sess-1", "agent-1", "feat-new"); err != nil {
		t.Fatalf("overwrite set: %v", err)
	}
	got := db.GetActiveWorkItem(database, "sess-1", "agent-1")
	if got != "feat-new" {
		t.Fatalf("want feat-new, got %q", got)
	}
}

func TestSetActiveWorkItem_Idempotent(t *testing.T) {
	database := openIsolatedDB(t)

	// Calling set twice with the same args must not error and must leave one row.
	for i := 0; i < 2; i++ {
		if err := db.SetActiveWorkItem(database, "sess-1", "agent-1", "feat-abc"); err != nil {
			t.Fatalf("SetActiveWorkItem iter %d: %v", i, err)
		}
	}
	// Confirm exactly one row.
	var count int
	database.QueryRow(
		`SELECT COUNT(*) FROM active_work_items WHERE session_id = ? AND agent_id = ?`,
		"sess-1", "agent-1",
	).Scan(&count) //nolint:errcheck
	if count != 1 {
		t.Fatalf("want 1 row, got %d", count)
	}
}

func TestClearActiveWorkItem(t *testing.T) {
	database := openIsolatedDB(t)

	if err := db.SetActiveWorkItem(database, "sess-1", "agent-1", "feat-abc"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := db.ClearActiveWorkItem(database, "sess-1", "agent-1"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got := db.GetActiveWorkItem(database, "sess-1", "agent-1")
	if got != "" {
		t.Fatalf("want empty after clear, got %q", got)
	}
}

func TestClearActiveWorkItem_NonexistentIsNoOp(t *testing.T) {
	database := openIsolatedDB(t)

	// Clearing a non-existent row must not error.
	if err := db.ClearActiveWorkItem(database, "sess-1", "agent-missing"); err != nil {
		t.Fatalf("clear nonexistent: %v", err)
	}
}

func TestActiveWorkItemsForSession_MultipleAgents(t *testing.T) {
	database := openIsolatedDB(t)

	if err := db.SetActiveWorkItem(database, "sess-1", "agent-a", "feat-1"); err != nil {
		t.Fatalf("set a: %v", err)
	}
	if err := db.SetActiveWorkItem(database, "sess-1", "agent-b", "feat-2"); err != nil {
		t.Fatalf("set b: %v", err)
	}
	// Different session — must not appear in sess-1 results.
	if err := db.SetActiveWorkItem(database, "sess-2", "agent-a", "feat-3"); err != nil {
		t.Fatalf("set other session: %v", err)
	}

	items, err := db.ActiveWorkItemsForSession(database, "sess-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d: %v", len(items), items)
	}
	if items["agent-a"] != "feat-1" {
		t.Errorf("agent-a: want feat-1, got %q", items["agent-a"])
	}
	if items["agent-b"] != "feat-2" {
		t.Errorf("agent-b: want feat-2, got %q", items["agent-b"])
	}
}

func TestActiveWorkItemsForSession_EmptySession(t *testing.T) {
	database := openIsolatedDB(t)

	items, err := db.ActiveWorkItemsForSession(database, "sess-nobody")
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("want empty map, got %v", items)
	}
}

func TestNormaliseAgentID(t *testing.T) {
	if got := db.NormaliseAgentID(""); got != db.AgentRootSentinel {
		t.Errorf("empty: want %q, got %q", db.AgentRootSentinel, got)
	}
	if got := db.NormaliseAgentID("a1b2"); got != "a1b2" {
		t.Errorf("non-empty: want a1b2, got %q", got)
	}
}

func TestGetActiveWorkItemWithFallback_PrefersNewTable(t *testing.T) {
	database := openIsolatedDB(t)

	// Insert a legacy active_feature_id on a session.
	database.Exec( //nolint:errcheck
		`INSERT INTO sessions (session_id, agent_assigned, status, created_at)
		 VALUES ('sess-1', 'claude-code', 'active', datetime('now'))`)
	database.Exec( //nolint:errcheck
		`UPDATE sessions SET active_feature_id = 'feat-legacy' WHERE session_id = 'sess-1'`)

	// Without a row in active_work_items, fallback returns legacy value.
	got := db.GetActiveWorkItemWithFallback(database, "sess-1", "agent-a")
	if got != "feat-legacy" {
		t.Fatalf("fallback: want feat-legacy, got %q", got)
	}

	// Set new-table row — must be preferred over legacy.
	if err := db.SetActiveWorkItem(database, "sess-1", "agent-a", "feat-new"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got = db.GetActiveWorkItemWithFallback(database, "sess-1", "agent-a")
	if got != "feat-new" {
		t.Fatalf("prefer new: want feat-new, got %q", got)
	}
}
