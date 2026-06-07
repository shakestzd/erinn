package main

import (
	"database/sql"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/ingest"
	"github.com/shakestzd/wipnote/core/models"
)

func setupIngestTitleDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func seedSessionWithTitle(t *testing.T, database *sql.DB, sessionID, title string) {
	t.Helper()
	_, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, title)
		 VALUES (?, 'claude-code', datetime('now'), 'completed', NULLIF(?, ''))`,
		sessionID, title,
	)
	if err != nil {
		t.Fatalf("seed session %s: %v", sessionID, err)
	}
}

func getSessionTitle(t *testing.T, database *sql.DB, sessionID string) string {
	t.Helper()
	var title sql.NullString
	err := database.QueryRow(`SELECT title FROM sessions WHERE session_id = ?`, sessionID).Scan(&title)
	if err != nil {
		t.Fatalf("get title for %s: %v", sessionID, err)
	}
	return title.String
}

func makeResultWithTitle(title string) *ingest.ParseResult {
	return &ingest.ParseResult{
		Title: title,
		Messages: []models.Message{
			{Ordinal: 0, Role: "user", Timestamp: time.Now().UTC()},
		},
	}
}

// TestStoreParseResult_UpdatesTitle_NewSession verifies that storeParseResult
// updates sessions.title when result.Title is non-empty on a new session.
func TestStoreParseResult_UpdatesTitle_NewSession(t *testing.T) {
	database := setupIngestTitleDB(t)
	sessionID := "sess-title-new-001"
	seedSessionWithTitle(t, database, sessionID, "") // NULL title

	result := makeResultWithTitle("My AI Title")
	storeParseResult(database, sessionID, "", result)

	got := getSessionTitle(t, database, sessionID)
	if got != "My AI Title" {
		t.Errorf("title after first ingest: got %q, want %q", got, "My AI Title")
	}
}

// TestStoreParseResult_UpdatesTitle_ExistingSession verifies that re-ingesting
// with a new title updates the existing session row (regression guard for the
// plan critique: UPDATE must NOT be inside ensureSession).
func TestStoreParseResult_UpdatesTitle_ExistingSession(t *testing.T) {
	database := setupIngestTitleDB(t)
	sessionID := "sess-title-existing-001"
	seedSessionWithTitle(t, database, sessionID, "Old Title")

	result := makeResultWithTitle("New Title")
	storeParseResult(database, sessionID, "", result)

	got := getSessionTitle(t, database, sessionID)
	if got != "New Title" {
		t.Errorf("title after re-ingest with new value: got %q, want %q", got, "New Title")
	}
}

// TestStoreParseResult_UpdatesTitle_SameValue verifies that re-ingesting with
// the same title does not produce an error (idempotent).
func TestStoreParseResult_UpdatesTitle_SameValue(t *testing.T) {
	database := setupIngestTitleDB(t)
	sessionID := "sess-title-same-001"
	seedSessionWithTitle(t, database, sessionID, "")

	result := makeResultWithTitle("Stable Title")
	storeParseResult(database, sessionID, "", result)
	storeParseResult(database, sessionID, "", result) // re-ingest same value

	got := getSessionTitle(t, database, sessionID)
	if got != "Stable Title" {
		t.Errorf("title after idempotent re-ingest: got %q, want %q", got, "Stable Title")
	}
}

// TestStoreParseResult_NoTitleUpdate_WhenEmpty verifies that storeParseResult
// does NOT clear an existing title when result.Title is empty.
func TestStoreParseResult_NoTitleUpdate_WhenEmpty(t *testing.T) {
	database := setupIngestTitleDB(t)
	sessionID := "sess-title-empty-001"
	seedSessionWithTitle(t, database, sessionID, "Existing Title")

	result := makeResultWithTitle("") // no title in parse result
	storeParseResult(database, sessionID, "", result)

	got := getSessionTitle(t, database, sessionID)
	if got != "Existing Title" {
		t.Errorf("title should be preserved when result.Title is empty: got %q, want %q", got, "Existing Title")
	}
}
