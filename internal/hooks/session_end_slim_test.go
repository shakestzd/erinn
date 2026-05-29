package hooks

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/internal/db"
	"github.com/shakestzd/wipnote/internal/models"
)

// TestSessionEnd_SetsStatusCompleted asserts that SessionEnd writes
// status=completed on the sessions row — the first critical write that must
// survive even if Claude Code cancels the handler mid-flight.
func TestSessionEnd_SetsStatusCompleted(t *testing.T) {
	td := setupTestDB(t)
	database := td.DB
	projectDir := t.TempDir()

	sessionID := "slim-sess-001"
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_AGENT_ID", "claude-code")
	t.Setenv("WIPNOTE_AGENT_TYPE", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	if err := db.InsertSession(database, &models.Session{
		SessionID:     sessionID,
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
		ProjectDir:    projectDir,
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	evt := &CloudEvent{SessionID: sessionID, CWD: projectDir}
	res, err := SessionEnd(evt, database, projectDir)
	if err != nil {
		t.Fatalf("SessionEnd: %v", err)
	}
	if !res.Continue {
		t.Error("expected Continue=true")
	}

	sess, err := db.GetSession(database, sessionID)
	if err != nil || sess == nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Status != "completed" {
		t.Errorf("status = %q, want completed", sess.Status)
	}
	if sess.CompletedAt == nil {
		t.Error("completed_at must be set after SessionEnd")
	}
}

// TestSessionEnd_ReleasesClaimsBeforeOtherWork asserts that SessionEnd
// releases all active claims for the session — a critical write that must
// happen early so that claim release is not lost if the handler is cancelled.
func TestSessionEnd_ReleasesClaimsBeforeOtherWork(t *testing.T) {
	td := setupTestDB(t)
	database := td.DB
	projectDir := t.TempDir()

	sessionID := "slim-sess-002"
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_AGENT_ID", "claude-code")
	t.Setenv("WIPNOTE_AGENT_TYPE", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	td.addFeature("feat-slim-0001", "feature", "slim test item", "in-progress")

	c := &models.Claim{
		ClaimID:        "claim-slim-001",
		WorkItemID:     "feat-slim-0001",
		OwnerSessionID: sessionID,
		OwnerAgent:     "claude-code",
		Status:         models.ClaimInProgress,
	}
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	// Precondition: claim is active.
	if got, _ := db.GetActiveClaim(database, "feat-slim-0001"); got == nil {
		t.Fatal("precondition: expected active claim before SessionEnd")
	}

	evt := &CloudEvent{SessionID: sessionID, CWD: projectDir}
	if _, err := SessionEnd(evt, database, projectDir); err != nil {
		t.Fatalf("SessionEnd: %v", err)
	}

	// Claim must be released (abandoned).
	if got, _ := db.GetActiveClaim(database, "feat-slim-0001"); got != nil {
		t.Fatalf("claim still active after SessionEnd, status=%s", got.Status)
	}
	var status string
	database.QueryRow(
		`SELECT status FROM claims WHERE claim_id = 'claim-slim-001'`,
	).Scan(&status)
	if status != "abandoned" {
		t.Errorf("claim status = %q, want abandoned", status)
	}
}

// TestSessionEnd_DoesNotFinalizeHTML asserts that SessionEnd no longer calls
// FinalizeSessionHTML (that step is now owned exclusively by Stop). We verify by
// creating a sentinel session HTML and confirming SessionEnd leaves it unchanged.
// Stop is responsible for updating the event-count badge; SessionEnd must not.
func TestSessionEnd_DoesNotFinalizeHTML(t *testing.T) {
	td := setupTestDB(t)
	database := td.DB
	projectDir := t.TempDir()

	sessionID := "slim-no-html-003"
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_AGENT_ID", "claude-code")
	t.Setenv("WIPNOTE_AGENT_TYPE", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	if err := db.InsertSession(database, &models.Session{
		SessionID:     sessionID,
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
		ProjectDir:    projectDir,
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	// Place a sentinel session HTML in the expected location.
	// FinalizeSessionHTML writes to .wipnote/sessions/<id>/session.html;
	// if SessionEnd calls it, this file would be overwritten.
	sessDir := filepath.Join(projectDir, ".wipnote", "sessions", sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	const sentinel = `<html><body>SENTINEL-DO-NOT-OVERWRITE</body></html>`
	htmlPath := filepath.Join(sessDir, "session.html")
	if err := os.WriteFile(htmlPath, []byte(sentinel), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	evt := &CloudEvent{SessionID: sessionID, CWD: projectDir}
	if _, err := SessionEnd(evt, database, projectDir); err != nil {
		t.Fatalf("SessionEnd: %v", err)
	}

	// Verify the sentinel HTML is unchanged — SessionEnd must NOT finalize it.
	got, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != sentinel {
		t.Errorf("SessionEnd modified the session HTML (FinalizeSessionHTML must NOT be called here);\ngot:  %q\nwant: %q", string(got), sentinel)
	}
}
