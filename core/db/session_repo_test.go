package db_test

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// TestGetToolUseContext_ClaimLookupByAgentID asserts the primary lookup path:
// a claim created with claimed_by_agent_id = "agent-A" is found when
// GetToolUseContext is called with that agent_id.
func TestGetToolUseContext_ClaimLookupByAgentID(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	insertTestFeatures(t, database, "feat-primary")
	c := &models.Claim{
		ClaimID:          "claim-primary",
		WorkItemID:       "feat-primary",
		OwnerSessionID:   "sess-test",
		OwnerAgent:       "claude-code",
		ClaimedByAgentID: "agent-A",
		Status:           models.ClaimInProgress,
	}
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	row, err := db.GetToolUseContext(database, "sess-test", "agent-A")
	if err != nil {
		t.Fatalf("GetToolUseContext: %v", err)
	}
	if row == nil {
		t.Fatal("expected row, got nil")
	}
	if row.ClaimedItem != "feat-primary" {
		t.Errorf("ClaimedItem: got %q, want %q", row.ClaimedItem, "feat-primary")
	}
}

// TestGetToolUseContext_ClaimLookupBySessionFallback is the bug-cb4918d8
// regression test: a claim keyed on owner_session_id must resolve even when
// the caller's agent_id does not match any claim row. This is exactly the
// subagent case — parent orchestrator owns the claim with agent_id="", and
// a subagent tool call arrives with agent_id="abc123" under the same
// session_id.
func TestGetToolUseContext_ClaimLookupBySessionFallback(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	insertTestFeatures(t, database, "feat-parent")
	// Parent claim with empty ClaimedByAgentID (orchestrator).
	c := &models.Claim{
		ClaimID:          "claim-parent",
		WorkItemID:       "feat-parent",
		OwnerSessionID:   "sess-test",
		OwnerAgent:       "claude-code",
		ClaimedByAgentID: "",
		Status:           models.ClaimInProgress,
	}
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	// Subagent tool call: same session_id, different agent_id.
	row, err := db.GetToolUseContext(database, "sess-test", "subagent-different-id")
	if err != nil {
		t.Fatalf("GetToolUseContext: %v", err)
	}
	if row == nil {
		t.Fatal("expected row, got nil")
	}
	if row.ClaimedItem != "feat-parent" {
		t.Errorf("ClaimedItem: got %q, want %q (session-id fallback should have resolved parent claim)",
			row.ClaimedItem, "feat-parent")
	}
}

func TestGetToolUseContext_DirectClaimScopedToSession(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	if err := db.InsertSession(database, &models.Session{
		SessionID:     "sess-other",
		AgentAssigned: "codex",
		CreatedAt:     now,
		Status:        "active",
	}); err != nil {
		t.Fatalf("InsertSession other: %v", err)
	}
	insertTestFeatures(t, database, "feat-other")
	c := &models.Claim{
		ClaimID:          "claim-other-session",
		WorkItemID:       "feat-other",
		OwnerSessionID:   "sess-other",
		OwnerAgent:       "codex",
		ClaimedByAgentID: "codex",
		Status:           models.ClaimInProgress,
	}
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	row, err := db.GetToolUseContext(database, "sess-test", "codex")
	if err != nil {
		t.Fatalf("GetToolUseContext: %v", err)
	}
	if row == nil {
		t.Fatal("expected row, got nil")
	}
	if row.ClaimedItem != "" {
		t.Fatalf("ClaimedItem = %q, want empty; direct claims must be scoped to session", row.ClaimedItem)
	}
}

func TestGetToolUseContext_DirectClaimUsesLatestLease(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	insertTestFeatures(t, database, "feat-old", "feat-new")
	oldClaim := &models.Claim{
		ClaimID:          "claim-old",
		WorkItemID:       "feat-old",
		OwnerSessionID:   "sess-test",
		OwnerAgent:       "codex",
		ClaimedByAgentID: "codex",
		Status:           models.ClaimInProgress,
	}
	if err := db.ClaimItem(database, oldClaim, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem old: %v", err)
	}
	if _, err := database.Exec(
		`UPDATE claims SET leased_at = ? WHERE claim_id = ?`,
		now.Add(-time.Minute).Format(time.RFC3339), oldClaim.ClaimID,
	); err != nil {
		t.Fatalf("set old lease: %v", err)
	}
	newClaim := &models.Claim{
		ClaimID:          "claim-new",
		WorkItemID:       "feat-new",
		OwnerSessionID:   "sess-test",
		OwnerAgent:       "codex",
		ClaimedByAgentID: "codex",
		Status:           models.ClaimInProgress,
	}
	if err := db.ClaimItem(database, newClaim, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem new: %v", err)
	}
	if _, err := database.Exec(
		`UPDATE claims SET leased_at = ? WHERE claim_id = ?`,
		now.Format(time.RFC3339), newClaim.ClaimID,
	); err != nil {
		t.Fatalf("set new lease: %v", err)
	}

	row, err := db.GetToolUseContext(database, "sess-test", "codex")
	if err != nil {
		t.Fatalf("GetToolUseContext: %v", err)
	}
	if row == nil {
		t.Fatal("expected row, got nil")
	}
	if row.ClaimedItem != "feat-new" {
		t.Fatalf("ClaimedItem = %q, want feat-new", row.ClaimedItem)
	}
}
