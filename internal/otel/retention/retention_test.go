package retention_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/internal/otel/retention"
	_ "modernc.org/sqlite"
)

// openTestDB creates an in-memory SQLite DB with the minimal sessions schema.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE sessions (
		session_id   TEXT PRIMARY KEY,
		status       TEXT NOT NULL DEFAULT 'active',
		completed_at TEXT
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// insertSession inserts a minimal session row.
func insertSession(t *testing.T, db *sql.DB, sessionID, status string, completedAt *time.Time) {
	t.Helper()
	var completedStr *string
	if completedAt != nil {
		s := completedAt.UTC().Format(time.RFC3339)
		completedStr = &s
	}
	_, err := db.Exec(`INSERT INTO sessions (session_id, status, completed_at) VALUES (?, ?, ?)`,
		sessionID, status, completedStr)
	if err != nil {
		t.Fatalf("insert session %s: %v", sessionID, err)
	}
}

// makeSessionDir creates a synthetic session dir with an events.ndjson file.
func makeSessionDir(t *testing.T, wipnoteDir, sessionID, content string) {
	t.Helper()
	dir := filepath.Join(wipnoteDir, "sessions", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.ndjson"), []byte(content), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}
	offsetStr := fmt.Sprintf("%d", len(content))
	if err := os.WriteFile(filepath.Join(dir, ".index-offset"), []byte(offsetStr), 0o644); err != nil {
		t.Fatalf("write index-offset: %v", err)
	}
}

func TestRun_ArchivesOldCompletedSession(t *testing.T) {
	dir := t.TempDir()
	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}

	db := openTestDB(t)

	// Session completed 40 days ago — should be archived.
	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	insertSession(t, db, "sess-old", "completed", &old)
	makeSessionDir(t, wipnoteDir, "sess-old", `{"event":"test"}`+"\n")

	t.Setenv("WIPNOTE_SESSION_RETAIN_DAYS", "30")
	if err := retention.Run(db, wipnoteDir, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Live session dir should be gone.
	if _, err := os.Stat(filepath.Join(wipnoteDir, "sessions", "sess-old")); !os.IsNotExist(err) {
		t.Error("expected live session dir to be removed after archiving")
	}

	// Archive should exist somewhere under .wipnote/archive/.
	archiveRoot := filepath.Join(wipnoteDir, "archive")
	found := false
	_ = filepath.Walk(archiveRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.Name() == "sess-old.tar.gz" {
			found = true
		}
		return nil
	})
	if !found {
		t.Error("expected archive file to exist under .wipnote/archive/")
	}
}

func TestRun_SkipsActiveSession(t *testing.T) {
	dir := t.TempDir()
	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}

	db := openTestDB(t)

	// Active session — must not be archived regardless of age.
	old := time.Now().UTC().Add(-60 * 24 * time.Hour)
	insertSession(t, db, "sess-active", "active", &old)
	makeSessionDir(t, wipnoteDir, "sess-active", `{"event":"live"}`+"\n")

	t.Setenv("WIPNOTE_SESSION_RETAIN_DAYS", "30")
	if err := retention.Run(db, wipnoteDir, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Live dir must still exist.
	if _, err := os.Stat(filepath.Join(wipnoteDir, "sessions", "sess-active")); err != nil {
		t.Errorf("expected active session dir to remain: %v", err)
	}
}

func TestRun_SkipsRecentCompletedSession(t *testing.T) {
	dir := t.TempDir()
	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}

	db := openTestDB(t)

	// Completed 5 days ago — within retention window.
	recent := time.Now().UTC().Add(-5 * 24 * time.Hour)
	insertSession(t, db, "sess-recent", "completed", &recent)
	makeSessionDir(t, wipnoteDir, "sess-recent", `{"event":"recent"}`+"\n")

	t.Setenv("WIPNOTE_SESSION_RETAIN_DAYS", "30")
	if err := retention.Run(db, wipnoteDir, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Live dir must still exist.
	if _, err := os.Stat(filepath.Join(wipnoteDir, "sessions", "sess-recent")); err != nil {
		t.Errorf("expected recent session dir to remain: %v", err)
	}
}

func TestRun_DryRunDoesNotMoveFiles(t *testing.T) {
	dir := t.TempDir()
	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}

	db := openTestDB(t)

	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	insertSession(t, db, "sess-dry", "completed", &old)
	makeSessionDir(t, wipnoteDir, "sess-dry", `{"event":"dry"}`+"\n")

	t.Setenv("WIPNOTE_SESSION_RETAIN_DAYS", "30")
	if err := retention.Run(db, wipnoteDir, true /* dryRun */); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Dry-run: live dir must still exist.
	if _, err := os.Stat(filepath.Join(wipnoteDir, "sessions", "sess-dry")); err != nil {
		t.Errorf("dry-run must not remove session dir: %v", err)
	}

	// Dry-run: archive must not exist.
	archiveRoot := filepath.Join(wipnoteDir, "archive")
	if _, err := os.Stat(archiveRoot); err == nil {
		t.Error("dry-run must not create archive dir")
	}
}

func TestExtractArchive_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}

	db := openTestDB(t)

	content := `{"trace_id":"abc","span_id":"123"}` + "\n"
	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	insertSession(t, db, "sess-rt", "completed", &old)
	makeSessionDir(t, wipnoteDir, "sess-rt", content)

	// Archive it.
	t.Setenv("WIPNOTE_SESSION_RETAIN_DAYS", "30")
	if err := retention.Run(db, wipnoteDir, false); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify live dir was removed.
	if _, err := os.Stat(filepath.Join(wipnoteDir, "sessions", "sess-rt")); !os.IsNotExist(err) {
		t.Fatal("expected session dir to be removed before restore")
	}

	// Restore.
	if err := retention.ExtractArchive(wipnoteDir, "sess-rt"); err != nil {
		t.Fatalf("ExtractArchive: %v", err)
	}

	// Restored events.ndjson should match original content.
	got, err := os.ReadFile(filepath.Join(wipnoteDir, "sessions", "sess-rt", "events.ndjson"))
	if err != nil {
		t.Fatalf("read restored events: %v", err)
	}
	if string(got) != content {
		t.Errorf("restored content mismatch:\n got:  %q\n want: %q", got, content)
	}
}

// --- Fix 2: path traversal via session ID ---

// TestValidateSessionID_RejectsTraversal verifies that path-traversal IDs are
// rejected and no filesystem operation is attempted outside the sessions dir.
func TestValidateSessionID_RejectsTraversal(t *testing.T) {
	cases := []struct {
		id   string
		desc string
	}{
		{"../escape", "parent traversal"},
		{"/abs/path", "absolute path"},
		{"..", "dotdot alone"},
		{"subdir/id", "slash in id"},
		{"sub\\id", "backslash in id"},
		{".", "dot alone"},
	}
	for _, tc := range cases {
		err := retention.ValidateSessionID(tc.id)
		if err == nil {
			t.Errorf("ValidateSessionID(%q) [%s]: expected error, got nil", tc.id, tc.desc)
		}
	}
}

// TestValidateSessionID_AcceptsValidIDs verifies that normal session IDs pass.
func TestValidateSessionID_AcceptsValidIDs(t *testing.T) {
	valid := []string{
		"sess-abc123",
		"d846b50d-9ce4-45c1-8ad2-0f84da537efd",
		"session_20260524",
	}
	for _, id := range valid {
		if err := retention.ValidateSessionID(id); err != nil {
			t.Errorf("ValidateSessionID(%q): unexpected error: %v", id, err)
		}
	}
}

// TestRun_SkipsTraversalSessionIDs verifies that when the DB contains rows with
// path-traversal session IDs, Run skips them safely without touching any
// directory outside .wipnote/sessions/. It creates sentinel directories at the
// actual resolved escape targets and verifies they are preserved.
func TestRun_SkipsTraversalSessionIDs(t *testing.T) {
	dir := t.TempDir()
	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}

	db := openTestDB(t)

	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	traversalIDs := []string{"../escape", "..", "/abs/path", "sub/dir"}
	for _, id := range traversalIDs {
		insertSession(t, db, id, "completed", &old)
	}

	// Create sentinel directories at the ACTUAL resolved escape targets:
	// - "../escape" from .wipnote/sessions/ resolves to .wipnote/escape
	// - ".." from .wipnote/sessions/ resolves to .wipnote/
	// We verify these are not removed by archiveSession if ValidateSessionID
	// were to regress.
	escapeViaParent := filepath.Join(wipnoteDir, "escape")
	if err := os.MkdirAll(escapeViaParent, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a marker file to detect if .wipnote/ itself is removed.
	markerFile := filepath.Join(wipnoteDir, "retention-test-marker")
	if err := os.WriteFile(markerFile, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WIPNOTE_SESSION_RETAIN_DAYS", "30")
	// Run should complete without error (bad rows are skipped, not fatal).
	if err := retention.Run(db, wipnoteDir, false); err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}

	// Verify that .wipnote/escape is still present — ../escape should not have
	// caused it to be removed.
	if _, err := os.Stat(escapeViaParent); os.IsNotExist(err) {
		t.Error("Run: ../escape traversal caused .wipnote/escape to be removed")
	}

	// Verify that .wipnote/ itself was not removed (marker file still present).
	if _, err := os.Stat(markerFile); os.IsNotExist(err) {
		t.Error("Run: .. traversal caused .wipnote/ directory to be removed")
	}
}

// TestArchiveSession_RejectsTraversal verifies that ArchiveSession returns an
// error for path-traversal IDs and does not touch the filesystem outside the
// sessions directory.
func TestArchiveSession_RejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a real directory at the escape target to detect if it gets removed.
	escapeTarget := filepath.Join(dir, "escape")
	if err := os.MkdirAll(escapeTarget, 0o755); err != nil {
		t.Fatal(err)
	}

	traversalIDs := []string{"../escape", "/abs/path", ".."}
	for _, id := range traversalIDs {
		err := retention.ArchiveSession(wipnoteDir, id, false)
		if err == nil {
			t.Errorf("ArchiveSession(%q): expected error for traversal ID, got nil", id)
		}
	}

	// The escape target directory must still exist — nothing was removed.
	if _, err := os.Stat(escapeTarget); os.IsNotExist(err) {
		t.Error("ArchiveSession traversal: escape target directory was unexpectedly removed")
	}
}
