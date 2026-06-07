package db

import (
	"path/filepath"
	"testing"
)

// TestSessionFamilyMigration_IdempotentAddColumn verifies the migration step
// that adds session_family_id to the sessions table is idempotent.
func TestSessionFamilyMigration_IdempotentAddColumn(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wipnote.db")

	// First open runs all migrations including session_family_id.
	db1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}

	// Verify column was added.
	var val interface{}
	err = db1.QueryRow(`SELECT session_family_id FROM sessions LIMIT 1`).Scan(&val)
	// ErrNoRows is fine; we just want no "no such column" error.
	if err != nil && err.Error() == "sql: no rows in result set" {
		// expected — column exists, table just empty
	} else if err != nil && err.Error() != "sql: no rows in result set" {
		// Check if it's a no-column error vs no-rows
		if isNoSuchColumnError(err) {
			t.Fatalf("session_family_id column missing after migration: %v", err)
		}
	}
	db1.Close()

	// Second open should be idempotent (no dup column errors).
	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open (idempotency check): %v", err)
	}
	defer db2.Close()

	// Verify we're at the current schema version.
	v, err := readUserVersion(db2)
	if err != nil {
		t.Fatalf("readUserVersion: %v", err)
	}
	if v != currentSchemaVersion {
		t.Errorf("user_version = %d, want %d", v, currentSchemaVersion)
	}
}

// isNoSuchColumnError returns true if the error is a SQLite "no such column" error.
func isNoSuchColumnError(err error) bool {
	if err == nil {
		return false
	}
	return containsStr(err.Error(), "no such column")
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStrHelper(s, sub))
}

func containsStrHelper(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
