package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// setupBackfillDB opens a temp-file SQLite DB for testing.
// A file-based DB is required here because the worker pool uses goroutines,
// and SQLite :memory: creates a separate (empty) database per connection.
func setupBackfillDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// seedBackfillSession inserts a session row with the given title and transcript_path.
func seedBackfillSession(t *testing.T, database *sql.DB, sessionID, title, transcriptPath string) {
	t.Helper()
	_, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, title, transcript_path)
		 VALUES (?, 'claude-code', datetime('now'), 'completed', NULLIF(?, ''), NULLIF(?, ''))`,
		sessionID, title, transcriptPath,
	)
	if err != nil {
		t.Fatalf("seed session %s: %v", sessionID, err)
	}
}

// writeJSONLWithAITitle writes a minimal JSONL file containing an ai-title event.
func writeJSONLWithAITitle(t *testing.T, path, sessionID, aiTitle string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for jsonl: %v", err)
	}
	content := fmt.Sprintf(`{"type":"ai-title","sessionId":%q,"aiTitle":%q}`+"\n", sessionID, aiTitle)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write jsonl %s: %v", path, err)
	}
}

// writeJSONLNoAITitle writes a minimal JSONL file with a user message but no ai-title.
func writeJSONLNoAITitle(t *testing.T, path, sessionID string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for jsonl: %v", err)
	}
	content := fmt.Sprintf(`{"type":"user","sessionId":%q,"message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`+"\n", sessionID)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write jsonl %s: %v", path, err)
	}
}

// getBackfillTitle retrieves the title for a session from the DB.
func getBackfillTitle(t *testing.T, database *sql.DB, sessionID string) string {
	t.Helper()
	var title sql.NullString
	err := database.QueryRow(`SELECT title FROM sessions WHERE session_id = ?`, sessionID).Scan(&title)
	if err != nil {
		t.Fatalf("get title for %s: %v", sessionID, err)
	}
	return title.String
}

// TestBackfillUpdatesStaleTitle verifies that sessions with legacy/empty titles
// get their titles updated from the JSONL ai-title event.
func TestBackfillUpdatesStaleTitle(t *testing.T) {
	database := setupBackfillDB(t)
	tmpDir := t.TempDir()
	wipnoteDir := filepath.Join(tmpDir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir wipnoteDir: %v", err)
	}

	type sessionCase struct {
		id    string
		title string
	}

	legacySessions := []sessionCase{
		{id: "sess-bf-001", title: "[wipnote-titler] Some old title"},
		{id: "sess-bf-002", title: "[wipnote-titler] Another old"},
		{id: "sess-bf-003", title: "[wipnote-titler] Third one"},
	}
	nullSession := sessionCase{id: "sess-bf-004", title: ""}
	dashSession := sessionCase{id: "sess-bf-005", title: "--"}

	// Write JSONLs with ai-title for all 4 that should be updated.
	for i, sc := range legacySessions {
		jsonlPath := filepath.Join(tmpDir, sc.id+".jsonl")
		writeJSONLWithAITitle(t, jsonlPath, sc.id, fmt.Sprintf("Real title %d", i+1))
		seedBackfillSession(t, database, sc.id, sc.title, jsonlPath)
	}

	// null session gets an ai-title
	jsonlPath4 := filepath.Join(tmpDir, nullSession.id+".jsonl")
	writeJSONLWithAITitle(t, jsonlPath4, nullSession.id, "Real title 4")
	seedBackfillSession(t, database, nullSession.id, nullSession.title, jsonlPath4)

	// dash session has NO ai-title in its JSONL — title should remain "--"
	jsonlPath5 := filepath.Join(tmpDir, dashSession.id+".jsonl")
	writeJSONLNoAITitle(t, jsonlPath5, dashSession.id)
	seedBackfillSession(t, database, dashSession.id, dashSession.title, jsonlPath5)

	if err := runAITitleBackfill(database, wipnoteDir, true); err != nil {
		t.Fatalf("runAITitleBackfill: %v", err)
	}

	// The 4 sessions with ai-title in JSONL should be updated.
	for i, sc := range legacySessions {
		want := fmt.Sprintf("Real title %d", i+1)
		got := getBackfillTitle(t, database, sc.id)
		if got != want {
			t.Errorf("session %s: got title %q, want %q", sc.id, got, want)
		}
	}
	got4 := getBackfillTitle(t, database, nullSession.id)
	if got4 != "Real title 4" {
		t.Errorf("null session: got %q, want %q", got4, "Real title 4")
	}

	// The session with no ai-title should retain "--"
	got5 := getBackfillTitle(t, database, dashSession.id)
	if got5 != "--" {
		t.Errorf("dash session without ai-title: got %q, want %q", got5, "--")
	}
}

// TestBackfillIdempotentViaMarker verifies that the sentinel file prevents
// the backfill from re-running and overwriting titles updated after the first run.
func TestBackfillIdempotentViaMarker(t *testing.T) {
	database := setupBackfillDB(t)
	tmpDir := t.TempDir()
	wipnoteDir := filepath.Join(tmpDir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir wipnoteDir: %v", err)
	}

	sessionID := "sess-idem-001"
	jsonlPath := filepath.Join(tmpDir, sessionID+".jsonl")
	writeJSONLWithAITitle(t, jsonlPath, sessionID, "AI Title")
	seedBackfillSession(t, database, sessionID, "[wipnote-titler] old", jsonlPath)

	// First run: should update title and write marker.
	if err := runAITitleBackfill(database, wipnoteDir, false); err != nil {
		t.Fatalf("first run: %v", err)
	}

	markerPath := filepath.Join(wipnoteDir, "migrations", "ai-title-backfill.done")
	if _, err := os.Stat(markerPath); os.IsNotExist(err) {
		t.Fatal("marker file was not created after first run")
	}

	// Manually change the title to something else.
	database.Exec(`UPDATE sessions SET title = 'Manually changed' WHERE session_id = ?`, sessionID)

	// Second run with forceRun=false — marker exists, should be a no-op.
	if err := runAITitleBackfill(database, wipnoteDir, false); err != nil {
		t.Fatalf("second run: %v", err)
	}

	got := getBackfillTitle(t, database, sessionID)
	if got != "Manually changed" {
		t.Errorf("second run should not overwrite title: got %q, want %q", got, "Manually changed")
	}
}

// TestBackfillSkipsMissingJSONL verifies that sessions with missing JSONL files
// are skipped without touching the title or crashing.
func TestBackfillSkipsMissingJSONL(t *testing.T) {
	database := setupBackfillDB(t)
	tmpDir := t.TempDir()
	wipnoteDir := filepath.Join(tmpDir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir wipnoteDir: %v", err)
	}

	sessionID := "sess-missing-001"
	missingPath := filepath.Join(tmpDir, "nonexistent.jsonl")
	// Deliberately do not create the file.
	seedBackfillSession(t, database, sessionID, "[wipnote-titler] stale", missingPath)

	// Should not crash; should log+skip.
	if err := runAITitleBackfill(database, wipnoteDir, true); err != nil {
		t.Fatalf("runAITitleBackfill: %v", err)
	}

	// Title should remain unchanged.
	got := getBackfillTitle(t, database, sessionID)
	if got != "[wipnote-titler] stale" {
		t.Errorf("title should be untouched for missing JSONL: got %q", got)
	}
}

// TestBackfillResumable verifies that re-running with forceRun=true after a
// partial run picks up unfinished sessions and converges.
func TestBackfillResumable(t *testing.T) {
	database := setupBackfillDB(t)
	tmpDir := t.TempDir()
	wipnoteDir := filepath.Join(tmpDir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir wipnoteDir: %v", err)
	}

	// Seed two sessions.
	for i := 1; i <= 2; i++ {
		sid := fmt.Sprintf("sess-resume-%03d", i)
		jsonlPath := filepath.Join(tmpDir, sid+".jsonl")
		writeJSONLWithAITitle(t, jsonlPath, sid, fmt.Sprintf("Resumed title %d", i))
		seedBackfillSession(t, database, sid, "[wipnote-titler] old", jsonlPath)
	}

	// First run: fully process everything.
	if err := runAITitleBackfill(database, wipnoteDir, false); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Delete the marker to simulate an interrupted previous run.
	markerPath := filepath.Join(wipnoteDir, "migrations", "ai-title-backfill.done")
	if err := os.Remove(markerPath); err != nil {
		t.Fatalf("remove marker: %v", err)
	}

	// Manually revert one session title to simulate partial progress.
	database.Exec(`UPDATE sessions SET title = '[wipnote-titler] reverted' WHERE session_id = 'sess-resume-001'`)

	// Second run with forceRun=false (no marker) — should process again.
	if err := runAITitleBackfill(database, wipnoteDir, false); err != nil {
		t.Fatalf("second run: %v", err)
	}

	// Both sessions should now have correct titles.
	for i := 1; i <= 2; i++ {
		sid := fmt.Sprintf("sess-resume-%03d", i)
		want := fmt.Sprintf("Resumed title %d", i)
		got := getBackfillTitle(t, database, sid)
		if got != want {
			t.Errorf("session %s: got %q, want %q", sid, got, want)
		}
	}

	// Marker should exist after successful completion.
	if _, err := os.Stat(markerPath); os.IsNotExist(err) {
		t.Fatal("marker file should exist after completed second run")
	}
}
