package db_test

import (
	"testing"

	"github.com/shakestzd/wipnote/core/db"
)

func TestSessionExecContextMigration_AddsColumnsNoDataLoss(t *testing.T) {
	path := fileDBPath(t, "pre_exec_context.db")

	raw := openRaw(t, path)
	if _, err := raw.Exec(`CREATE TABLE sessions (
		session_id TEXT PRIMARY KEY,
		agent_assigned TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		status TEXT NOT NULL DEFAULT 'active',
		project_dir TEXT
	)`); err != nil {
		t.Fatalf("seed legacy sessions table: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status, project_dir)
		 VALUES (?, ?, ?, ?)`,
		"legacy-session-001", "claude-code", "active", ".",
	); err != nil {
		t.Fatalf("seed legacy session row: %v", err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 13"); err != nil {
		t.Fatalf("seed user_version=13: %v", err)
	}
	raw.Close()

	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open legacy DB: %v", err)
	}
	defer database.Close()

	var execPath, branch, harness, projectDir interface{}
	err = database.QueryRow(
		`SELECT exec_worktree_path, branch, harness, project_dir
		   FROM sessions WHERE session_id = ?`, "legacy-session-001",
	).Scan(&execPath, &branch, &harness, &projectDir)
	if err != nil {
		t.Fatalf("select new columns after migration: %v", err)
	}
	if execPath != nil {
		t.Errorf("exec_worktree_path: got %v, want NULL", execPath)
	}
	if branch != nil {
		t.Errorf("branch: got %v, want NULL", branch)
	}
	if harness != nil {
		t.Errorf("harness: got %v, want NULL", harness)
	}
	if pd, ok := projectDir.(string); !ok || pd != "." {
		t.Errorf("project_dir after migration: got %v, want .", projectDir)
	}
	if v := queryUserVersion(t, database); v != db.CurrentSchemaVersion() {
		t.Errorf("user_version after migration = %d, want %d", v, db.CurrentSchemaVersion())
	}
}

func TestSessionExecContextMigration_Idempotent(t *testing.T) {
	path := fileDBPath(t, "exec_context_idempotent.db")

	db1, err := db.Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	db1.Close()

	db2, err := db.Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db2.Close()
	if got := queryUserVersion(t, db2); got != db.CurrentSchemaVersion() {
		t.Errorf("user_version = %d, want %d", got, db.CurrentSchemaVersion())
	}
}
