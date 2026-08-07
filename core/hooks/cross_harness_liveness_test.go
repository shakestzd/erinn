package hooks

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// TestGeminiSessionEndRelease verifies the cross-harness session-end contract
// for part (c): Gemini's Stop event is mapped (in packages/plugin-core/
// manifest.json) to geminiEventName "SessionEnd" with geminiHandler
// "session-end", so a Gemini session exit reaches THIS exact SessionEnd
// handler. The handler must release all active claims for the session — the
// same path Codex reaches via its SessionEnd -> session-end manifest wiring
// (repointed from the phantom TaskComplete event in bug-e39d408f).
func TestGeminiSessionEndRelease(t *testing.T) {
	td := setupTestDB(t)
	database := td.DB
	projectDir := t.TempDir()

	td.addFeature("feat-gem01a2", "feature", "gemini work", "in-progress")

	c := &models.Claim{
		ClaimID:        "claim-gemini",
		WorkItemID:     "feat-gem01a2",
		OwnerSessionID: "test-sess",
		OwnerAgent:     "gemini-cli",
		Status:         models.ClaimInProgress,
	}
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	// Precondition: the claim is active.
	if got, _ := db.GetActiveClaim(database, "feat-gem01a2"); got == nil {
		t.Fatal("precondition: expected an active claim before SessionEnd")
	}

	// Gemini reaches the session-end handler via the manifest geminiHandler
	// mapping; invoking it directly is the function-level equivalent.
	endEvent := &CloudEvent{SessionID: "test-sess", CWD: projectDir}
	res, err := SessionEnd(endEvent, database, projectDir)
	if err != nil {
		t.Fatalf("SessionEnd: %v", err)
	}
	if !res.Continue {
		t.Error("expected Continue=true from SessionEnd")
	}

	// All active claims for the session must be released (abandoned).
	if got, _ := db.GetActiveClaim(database, "feat-gem01a2"); got != nil {
		t.Fatalf("expected no active claim after SessionEnd, got status=%s", got.Status)
	}
	var status string
	database.QueryRow(
		`SELECT status FROM claims WHERE claim_id = 'claim-gemini'`,
	).Scan(&status)
	if status != "abandoned" {
		t.Fatalf("claim status after SessionEnd = %q, want abandoned", status)
	}
}

// TestPostToolUse_ClaimlessWritesSessionFiles is the integration assertion for
// part (b): a Write with NO active feature must no longer be dropped — it must
// land in the session_files claimless ledger keyed on (session_id,file_path).
//
// The started event is seeded directly (the production-equivalent row a
// successful PreToolUse writes) so the test isolates PostToolUse's claimless
// else-branch from the unrelated work-item PreToolUse guard.
func TestPostToolUse_ClaimlessWritesSessionFiles(t *testing.T) {
	td := setupTestDB(t)
	database := td.DB
	projectDir := t.TempDir()
	featureIDCache = featureIDCacheEntry{}

	seedStarted := func(eventID string) {
		ev := &models.AgentEvent{
			EventID:   eventID,
			AgentID:   "claude-code",
			EventType: models.EventToolCall,
			Timestamp: time.Now().UTC(),
			ToolName:  "Write",
			SessionID: "test-sess",
			Status:    "started",
		}
		if err := db.InsertEvent(database, ev); err != nil {
			t.Fatalf("seed started event: %v", err)
		}
	}

	seedStarted("evt-claimless-1")
	postEvent := &CloudEvent{
		SessionID:  "test-sess",
		CWD:        projectDir,
		ToolName:   "Write",
		ToolUseID:  "tool-claimless-1",
		ToolInput:  map[string]any{"file_path": "/repo/claimless.go"},
		ToolResult: map[string]any{"output": "written", "is_error": false},
	}
	res, err := PostToolUse(postEvent, database)
	if err != nil {
		t.Fatalf("PostToolUse: %v", err)
	}
	if !res.Continue {
		t.Error("expected Continue=true")
	}

	got, err := db.ListClaimlessFilesBySession(database, "test-sess")
	if err != nil {
		t.Fatalf("ListClaimlessFilesBySession: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("claimless write was dropped: expected 1 session_files row, got %d", len(got))
	}
	if got[0].FilePath != "/repo/claimless.go" {
		t.Errorf("file_path = %q, want /repo/claimless.go", got[0].FilePath)
	}
	if got[0].Operation != "write" {
		t.Errorf("operation = %q, want write", got[0].Operation)
	}

	// Idempotent on repeat (same session_id,file_path) — still one row.
	seedStarted("evt-claimless-2")
	postEvent.ToolUseID = "tool-claimless-2"
	if _, err := PostToolUse(postEvent, database); err != nil {
		t.Fatalf("PostToolUse repeat: %v", err)
	}
	got2, _ := db.ListClaimlessFilesBySession(database, "test-sess")
	if len(got2) != 1 {
		t.Fatalf("repeat claimless write must dedupe: got %d rows, want 1", len(got2))
	}
}
