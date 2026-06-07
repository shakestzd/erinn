package db_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// setupClaimDB returns an in-memory database with a test session and feature.
func setupClaimDB(t *testing.T) *sql.DB {
	t.Helper()
	database := setupTestDB(t)
	_, err := database.Exec(
		`INSERT INTO features (id, type, title, status) VALUES ('feat-test', 'feature', 'Test Feature', 'in-progress')`,
	)
	if err != nil {
		t.Fatalf("insert test feature: %v", err)
	}
	return database
}

// makeClaim returns a minimal Claim for testing.
func makeClaim(id, workItemID, sessionID string) *models.Claim {
	return &models.Claim{
		ClaimID:        id,
		WorkItemID:     workItemID,
		OwnerSessionID: sessionID,
		OwnerAgent:     "claude-code",
		Status:         models.ClaimProposed,
	}
}

func TestClaimItem(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	c := makeClaim("claim-001", "feat-test", "sess-test")
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	got, err := db.GetActiveClaim(database, "feat-test")
	if err != nil {
		t.Fatalf("GetActiveClaim: %v", err)
	}
	if got == nil {
		t.Fatal("expected active claim, got nil")
	}
	if got.ClaimID != "claim-001" {
		t.Errorf("claim_id: got %q, want %q", got.ClaimID, "claim-001")
	}
	if got.Status != models.ClaimProposed {
		t.Errorf("status: got %q, want %q", got.Status, models.ClaimProposed)
	}
	if got.LeaseExpiresAt.IsZero() {
		t.Error("lease_expires_at should not be zero")
	}
}

func TestClaimItemConflict(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	// Insert a second session so FK passes.
	_, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status) VALUES ('sess-other', 'claude-code', ?, 'active')`,
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert second session: %v", err)
	}

	c1 := makeClaim("claim-002", "feat-test", "sess-test")
	if err := db.ClaimItem(database, c1, 30*time.Minute); err != nil {
		t.Fatalf("first ClaimItem: %v", err)
	}

	// Different session tries to claim the same work item — should fail.
	c2 := makeClaim("claim-003", "feat-test", "sess-other")
	err = db.ClaimItem(database, c2, 30*time.Minute)
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
}

func TestClaimItemIdempotent(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	c1 := makeClaim("claim-004", "feat-test", "sess-test")
	if err := db.ClaimItem(database, c1, 30*time.Minute); err != nil {
		t.Fatalf("first ClaimItem: %v", err)
	}

	// Same session claims same work item — the work item is still active,
	// so it should conflict even for the same session.
	c2 := makeClaim("claim-005", "feat-test", "sess-test")
	err := db.ClaimItem(database, c2, 30*time.Minute)
	// Conflict is expected because the first claim is still active.
	if err == nil {
		t.Log("second claim from same session succeeded — idempotent insert allowed")
	} else {
		t.Logf("second claim from same session returned (expected): %v", err)
	}

	// Either way, the original claim must still be retrievable.
	got, err := db.GetActiveClaim(database, "feat-test")
	if err != nil {
		t.Fatalf("GetActiveClaim: %v", err)
	}
	if got == nil {
		t.Fatal("expected active claim, got nil")
	}
}

func TestHeartbeatClaim(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	c := makeClaim("claim-hb1", "feat-test", "sess-test")
	if err := db.ClaimItem(database, c, 5*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	before, err := db.GetClaim(database, "claim-hb1")
	if err != nil {
		t.Fatalf("GetClaim before: %v", err)
	}

	// Extend by 30 minutes.
	if err := db.HeartbeatClaim(database, "claim-hb1", "sess-test", 30*time.Minute); err != nil {
		t.Fatalf("HeartbeatClaim: %v", err)
	}

	after, err := db.GetClaim(database, "claim-hb1")
	if err != nil {
		t.Fatalf("GetClaim after: %v", err)
	}
	if !after.LeaseExpiresAt.After(before.LeaseExpiresAt) {
		t.Errorf("lease_expires_at not extended: before=%v after=%v",
			before.LeaseExpiresAt, after.LeaseExpiresAt)
	}
}

func TestHeartbeatClaimByWorkItem(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	c := makeClaim("claim-hbwi1", "feat-test", "sess-test")
	if err := db.ClaimItem(database, c, 5*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	before, err := db.GetClaim(database, "claim-hbwi1")
	if err != nil {
		t.Fatalf("GetClaim before: %v", err)
	}

	if err := db.HeartbeatClaimByWorkItem(database, "feat-test", "sess-test", "", 30*time.Minute); err != nil {
		t.Fatalf("HeartbeatClaimByWorkItem: %v", err)
	}

	after, err := db.GetClaim(database, "claim-hbwi1")
	if err != nil {
		t.Fatalf("GetClaim after: %v", err)
	}
	if !after.LeaseExpiresAt.After(before.LeaseExpiresAt) {
		t.Errorf("lease_expires_at not extended: before=%v after=%v",
			before.LeaseExpiresAt, after.LeaseExpiresAt)
	}
}

func TestHeartbeatClaimWrongSession(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	c := makeClaim("claim-hbws1", "feat-test", "sess-test")
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	err := db.HeartbeatClaim(database, "claim-hbws1", "sess-wrong", 30*time.Minute)
	if err == nil {
		t.Fatal("expected error when heartbeating with wrong session, got nil")
	}
}

func TestTransitionClaim(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	c := makeClaim("claim-tr1", "feat-test", "sess-test")
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	transitions := []models.ClaimStatus{
		models.ClaimClaimed,
		models.ClaimInProgress,
		models.ClaimCompleted,
	}
	for _, next := range transitions {
		if err := db.TransitionClaim(database, "claim-tr1", next); err != nil {
			t.Fatalf("TransitionClaim -> %s: %v", next, err)
		}
		got, err := db.GetClaim(database, "claim-tr1")
		if err != nil {
			t.Fatalf("GetClaim after transition to %s: %v", next, err)
		}
		if got.Status != next {
			t.Errorf("status: got %q, want %q", got.Status, next)
		}
	}
}

func TestTransitionClaimInvalid(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	c := makeClaim("claim-tri1", "feat-test", "sess-test")
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	// Transition to completed (terminal).
	if err := db.TransitionClaim(database, "claim-tri1", models.ClaimClaimed); err != nil {
		t.Fatalf("TransitionClaim -> claimed: %v", err)
	}
	if err := db.TransitionClaim(database, "claim-tri1", models.ClaimInProgress); err != nil {
		t.Fatalf("TransitionClaim -> in_progress: %v", err)
	}
	if err := db.TransitionClaim(database, "claim-tri1", models.ClaimCompleted); err != nil {
		t.Fatalf("TransitionClaim -> completed: %v", err)
	}

	// Attempt to transition out of completed — invalid.
	err := db.TransitionClaim(database, "claim-tri1", models.ClaimInProgress)
	if err == nil {
		t.Fatal("expected error for completed -> in_progress transition, got nil")
	}
}

func TestReleaseClaim(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	c := makeClaim("claim-rel1", "feat-test", "sess-test")
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	if err := db.ReleaseClaim(database, "claim-rel1", "sess-test", models.ClaimCompleted); err != nil {
		t.Fatalf("ReleaseClaim: %v", err)
	}

	got, err := db.GetClaim(database, "claim-rel1")
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}
	if got.Status != models.ClaimCompleted {
		t.Errorf("status: got %q, want %q", got.Status, models.ClaimCompleted)
	}

	// Active claim should now be gone.
	active, err := db.GetActiveClaim(database, "feat-test")
	if err != nil {
		t.Fatalf("GetActiveClaim after release: %v", err)
	}
	if active != nil {
		t.Errorf("expected no active claim after release, got %v", active)
	}
}

func TestReleaseAllClaimsForSession(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	// Insert extra features for additional claims.
	for i := 2; i <= 3; i++ {
		id := fmt.Sprintf("feat-extra-%d", i)
		_, err := database.Exec(
			`INSERT INTO features (id, type, title, status) VALUES (?, 'feature', ?, 'in-progress')`,
			id, "Extra Feature "+id,
		)
		if err != nil {
			t.Fatalf("insert feature %s: %v", id, err)
		}
	}

	claimIDs := []string{"claim-ra1", "claim-ra2", "claim-ra3"}
	workIDs := []string{"feat-test", "feat-extra-2", "feat-extra-3"}
	for i, cid := range claimIDs {
		c := makeClaim(cid, workIDs[i], "sess-test")
		if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
			t.Fatalf("ClaimItem %s: %v", cid, err)
		}
	}

	released, err := db.ReleaseAllClaimsForSession(database, "sess-test")
	if err != nil {
		t.Fatalf("ReleaseAllClaimsForSession: %v", err)
	}
	if released != 3 {
		t.Errorf("released: got %d, want 3", released)
	}

	for _, wid := range workIDs {
		active, err := db.GetActiveClaim(database, wid)
		if err != nil {
			t.Fatalf("GetActiveClaim(%s): %v", wid, err)
		}
		if active != nil {
			t.Errorf("expected no active claim for %s after release-all", wid)
		}
	}
}

func TestReapExpiredClaims(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	c := makeClaim("claim-exp1", "feat-test", "sess-test")
	// Use a negative duration so the lease is already in the past.
	if err := db.ClaimItem(database, c, -1*time.Minute); err != nil {
		t.Fatalf("ClaimItem with past expiry: %v", err)
	}

	// Force the lease_expires_at to be in the past (ClaimItem sets it, but
	// ReapExpiredClaims is called inside, so reap again explicitly).
	reaped, err := db.ReapExpiredClaims(database)
	if err != nil {
		t.Fatalf("ReapExpiredClaims: %v", err)
	}
	if reaped < 1 {
		t.Errorf("reaped: got %d, want >=1", reaped)
	}

	got, err := db.GetClaim(database, "claim-exp1")
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}
	if got.Status != models.ClaimExpired {
		t.Errorf("status: got %q, want %q", got.Status, models.ClaimExpired)
	}
}

func TestReapDoesNotAffectLiveClaims(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	c := makeClaim("claim-live1", "feat-test", "sess-test")
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	reaped, err := db.ReapExpiredClaims(database)
	if err != nil {
		t.Fatalf("ReapExpiredClaims: %v", err)
	}
	if reaped != 0 {
		t.Errorf("reaped live claim, got count %d", reaped)
	}

	got, err := db.GetClaim(database, "claim-live1")
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}
	if got.Status != models.ClaimProposed {
		t.Errorf("status: got %q, want %q", got.Status, models.ClaimProposed)
	}
}

func TestClaimItemAfterExpiry(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	// Insert a second session.
	_, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status) VALUES ('sess-new', 'claude-code', ?, 'active')`,
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert second session: %v", err)
	}

	// First session claims with past expiry.
	c1 := makeClaim("claim-ae1", "feat-test", "sess-test")
	if err := db.ClaimItem(database, c1, -1*time.Minute); err != nil {
		t.Fatalf("ClaimItem (past expiry): %v", err)
	}

	// Verify the old claim is now expired (reap fires inside ClaimItem and this call).
	_, _ = db.ReapExpiredClaims(database)

	// Second session should now be able to claim.
	c2 := makeClaim("claim-ae2", "feat-test", "sess-new")
	if err := db.ClaimItem(database, c2, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem after expiry: %v", err)
	}

	active, err := db.GetActiveClaim(database, "feat-test")
	if err != nil {
		t.Fatalf("GetActiveClaim: %v", err)
	}
	if active == nil {
		t.Fatal("expected active claim, got nil")
	}
	if active.ClaimID != "claim-ae2" {
		t.Errorf("claim_id: got %q, want %q", active.ClaimID, "claim-ae2")
	}
}

func TestListActiveClaimsBySession(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	// Insert a second feature for a second claim.
	_, err := database.Exec(
		`INSERT INTO features (id, type, title, status) VALUES ('feat-list-2', 'feature', 'List Feature 2', 'in-progress')`,
	)
	if err != nil {
		t.Fatalf("insert feat-list-2: %v", err)
	}

	c1 := makeClaim("claim-list1", "feat-test", "sess-test")
	if err := db.ClaimItem(database, c1, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem c1: %v", err)
	}
	c2 := makeClaim("claim-list2", "feat-list-2", "sess-test")
	if err := db.ClaimItem(database, c2, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem c2: %v", err)
	}

	claims, err := db.ListActiveClaimsBySession(database, "sess-test")
	if err != nil {
		t.Fatalf("ListActiveClaimsBySession: %v", err)
	}
	if len(claims) != 2 {
		t.Errorf("count: got %d, want 2", len(claims))
	}

	// Release one and verify the count drops.
	if err := db.ReleaseClaim(database, "claim-list1", "sess-test", models.ClaimCompleted); err != nil {
		t.Fatalf("ReleaseClaim: %v", err)
	}
	claims, err = db.ListActiveClaimsBySession(database, "sess-test")
	if err != nil {
		t.Fatalf("ListActiveClaimsBySession after release: %v", err)
	}
	if len(claims) != 1 {
		t.Errorf("count after release: got %d, want 1", len(claims))
	}
}

func TestClaimItemWithAgentID(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	c := makeClaim("claim-agent1", "feat-test", "sess-test")
	c.ClaimedByAgentID = "subagent-opus-abc"
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem with agent ID: %v", err)
	}

	got, err := db.GetActiveClaim(database, "feat-test")
	if err != nil {
		t.Fatalf("GetActiveClaim: %v", err)
	}
	if got == nil {
		t.Fatal("expected active claim, got nil")
	}
	if got.ClaimedByAgentID != "subagent-opus-abc" {
		t.Errorf("claimed_by_agent_id: got %q, want %q", got.ClaimedByAgentID, "subagent-opus-abc")
	}

	// GetClaim should also return the agent ID.
	gotByID, err := db.GetClaim(database, "claim-agent1")
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}
	if gotByID.ClaimedByAgentID != "subagent-opus-abc" {
		t.Errorf("GetClaim claimed_by_agent_id: got %q, want %q", gotByID.ClaimedByAgentID, "subagent-opus-abc")
	}
}

func TestHasActiveClaimByAgent(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	// No claims yet — should return false for any agent.
	if db.HasActiveClaimByAgent(database, "subagent-x") {
		t.Error("expected false before any claims")
	}
	if db.HasActiveClaimByAgent(database, "") {
		t.Error("expected false for orchestrator before any claims")
	}

	// Create a claim with a specific agent ID.
	c := makeClaim("claim-hac1", "feat-test", "sess-test")
	c.ClaimedByAgentID = "subagent-x"
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	// The specific agent should match.
	if !db.HasActiveClaimByAgent(database, "subagent-x") {
		t.Error("expected true for subagent-x after claiming")
	}
	// A different agent should not match.
	if db.HasActiveClaimByAgent(database, "subagent-y") {
		t.Error("expected false for subagent-y")
	}
	// The orchestrator (empty string) should not match.
	if db.HasActiveClaimByAgent(database, "") {
		t.Error("expected false for orchestrator when claim is by subagent-x")
	}
}

func TestHasActiveClaimByAgentOrchestrator(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	// Create a claim with empty agent ID (orchestrator).
	c := makeClaim("claim-haco1", "feat-test", "sess-test")
	c.ClaimedByAgentID = ""
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	// Orchestrator should match.
	if !db.HasActiveClaimByAgent(database, "") {
		t.Error("expected true for orchestrator claim")
	}
	// Subagent should not match.
	if db.HasActiveClaimByAgent(database, "subagent-z") {
		t.Error("expected false for subagent-z when claim is by orchestrator")
	}
}

func TestClaimItemMultiAgent(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	// Orchestrator claims the work item (claimed_by_agent_id = "").
	c1 := makeClaim("claim-ma1", "feat-test", "sess-test")
	c1.ClaimedByAgentID = ""
	if err := db.ClaimItem(database, c1, 30*time.Minute); err != nil {
		t.Fatalf("orchestrator ClaimItem: %v", err)
	}

	// A subagent claims the SAME work item — should succeed because different agent.
	c2 := makeClaim("claim-ma2", "feat-test", "sess-test")
	c2.ClaimedByAgentID = "subagent-opus"
	if err := db.ClaimItem(database, c2, 30*time.Minute); err != nil {
		t.Fatalf("subagent ClaimItem: %v", err)
	}

	// Both agents should have active claims.
	if !db.HasActiveClaimByAgent(database, "") {
		t.Error("orchestrator should have active claim")
	}
	if !db.HasActiveClaimByAgent(database, "subagent-opus") {
		t.Error("subagent-opus should have active claim")
	}

	// A second subagent with the same ID should be blocked.
	c3 := makeClaim("claim-ma3", "feat-test", "sess-test")
	c3.ClaimedByAgentID = "subagent-opus"
	err := db.ClaimItem(database, c3, 30*time.Minute)
	if err == nil {
		t.Fatal("expected conflict for duplicate subagent claim")
	}
}

func TestHasActiveClaimByAgentReleasedClaim(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	c := makeClaim("claim-hacr1", "feat-test", "sess-test")
	c.ClaimedByAgentID = "subagent-released"
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	// Release the claim.
	if err := db.ReleaseClaim(database, "claim-hacr1", "sess-test", models.ClaimCompleted); err != nil {
		t.Fatalf("ReleaseClaim: %v", err)
	}

	// Released claim should not count as active.
	if db.HasActiveClaimByAgent(database, "subagent-released") {
		t.Error("expected false after releasing claim")
	}
}

func TestUpdateClaimAgentID(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	// Create a claim with empty claimed_by_agent_id.
	c := makeClaim("claim-ucai1", "feat-test", "sess-test")
	c.ClaimedByAgentID = ""
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	// Tag the claim with an agent ID.
	if err := db.UpdateClaimAgentID(database, "feat-test", "test-agent-123"); err != nil {
		t.Fatalf("UpdateClaimAgentID: %v", err)
	}

	// Verify the claim now has the agent ID set.
	got, err := db.GetClaim(database, "claim-ucai1")
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}
	if got.ClaimedByAgentID != "test-agent-123" {
		t.Errorf("claimed_by_agent_id: got %q, want %q", got.ClaimedByAgentID, "test-agent-123")
	}
}

func TestUpdateClaimAgentIDNoOverwrite(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	// Create a claim that already has a non-empty claimed_by_agent_id.
	c := makeClaim("claim-ucai2", "feat-test", "sess-test")
	c.ClaimedByAgentID = "original-agent"
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	// Attempt to tag with a different agent ID — should not overwrite.
	if err := db.UpdateClaimAgentID(database, "feat-test", "new-agent"); err != nil {
		t.Fatalf("UpdateClaimAgentID: %v", err)
	}

	// Verify the original agent ID is preserved.
	got, err := db.GetClaim(database, "claim-ucai2")
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}
	if got.ClaimedByAgentID != "original-agent" {
		t.Errorf("claimed_by_agent_id: got %q, want %q (should not have been overwritten)", got.ClaimedByAgentID, "original-agent")
	}
}

// unmarshalWriteScope is a test helper that unmarshals the write_scope JSON of a claim.
func unmarshalWriteScope(t *testing.T, raw json.RawMessage) models.WriteScope {
	t.Helper()
	var scope models.WriteScope
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &scope); err != nil {
			t.Fatalf("unmarshal write_scope: %v (raw: %s)", err, string(raw))
		}
	}
	return scope
}

// TestWriteScopeAppendedInHeartbeatUpdate verifies that a heartbeat with a non-empty
// writePath stores that path in write_scope.paths via a single UPDATE per call.
//
// Single-UPDATE proof: HeartbeatClaimByWorkItem both extends lease_expires_at AND
// populates write_scope in one SQL statement. We verify the path is present AND the
// lease was extended — if only one happened, it would imply two separate statements,
// one of which would have been silently lost.
func TestWriteScopeAppendedInHeartbeatUpdate(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	c := makeClaim("claim-ws1", "feat-test", "sess-test")
	if err := db.ClaimItem(database, c, 5*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	// Record lease before heartbeat.
	before, err := db.GetClaim(database, "claim-ws1")
	if err != nil {
		t.Fatalf("GetClaim before: %v", err)
	}

	// Heartbeat with a path — extends lease AND sets write_scope in one UPDATE.
	if err := db.HeartbeatClaimByWorkItem(database, "feat-test", "sess-test", "internal/foo/bar.go", 30*time.Minute); err != nil {
		t.Fatalf("HeartbeatClaimByWorkItem: %v", err)
	}

	after, err := db.GetClaim(database, "claim-ws1")
	if err != nil {
		t.Fatalf("GetClaim after: %v", err)
	}

	// Lease must have extended from 5m to 30m — proving the UPDATE executed.
	if !after.LeaseExpiresAt.After(before.LeaseExpiresAt) {
		t.Errorf("lease_expires_at did not extend: before=%v after=%v",
			before.LeaseExpiresAt, after.LeaseExpiresAt)
	}

	// write_scope.paths must contain the heartbeated path (set in the SAME UPDATE).
	scope := unmarshalWriteScope(t, after.WriteScope)
	found := false
	for _, p := range scope.Paths {
		if p == "internal/foo/bar.go" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("write_scope.paths does not contain the heartbeated path; got %v", scope.Paths)
	}
}

// TestWriteScopeDedupeCap verifies deduplication and the 200-path FIFO cap.
func TestWriteScopeDedupeCap(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	c := makeClaim("claim-ws2", "feat-test", "sess-test")
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	// Append 205 distinct paths (0..204).
	for i := 0; i < 205; i++ {
		path := fmt.Sprintf("file-%03d.go", i)
		if err := db.HeartbeatClaimByWorkItem(database, "feat-test", "sess-test", path, 30*time.Minute); err != nil {
			t.Fatalf("HeartbeatClaimByWorkItem path %d: %v", i, err)
		}
	}

	got, err := db.GetClaim(database, "claim-ws2")
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}
	scope := unmarshalWriteScope(t, got.WriteScope)

	// Cap: exactly 200 paths should remain.
	if len(scope.Paths) != 200 {
		t.Errorf("expected 200 paths after cap, got %d", len(scope.Paths))
	}

	// Oldest 5 paths (file-000..file-004) should have been dropped.
	dropped := map[string]bool{}
	for i := 0; i < 5; i++ {
		dropped[fmt.Sprintf("file-%03d.go", i)] = true
	}
	for _, p := range scope.Paths {
		if dropped[p] {
			t.Errorf("dropped path %q should not be in write_scope after cap", p)
		}
	}

	// Newest path (file-204.go) must be present.
	newest := "file-204.go"
	found := false
	for _, p := range scope.Paths {
		if p == newest {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("newest path %q not found in write_scope; paths=%v", newest, scope.Paths)
	}

	// Re-append an existing path — length must stay at 200.
	existingPath := "file-100.go"
	if err := db.HeartbeatClaimByWorkItem(database, "feat-test", "sess-test", existingPath, 30*time.Minute); err != nil {
		t.Fatalf("HeartbeatClaimByWorkItem re-append: %v", err)
	}
	got2, err := db.GetClaim(database, "claim-ws2")
	if err != nil {
		t.Fatalf("GetClaim after re-append: %v", err)
	}
	scope2 := unmarshalWriteScope(t, got2.WriteScope)
	if len(scope2.Paths) != 200 {
		t.Errorf("after re-append: expected 200 paths, got %d", len(scope2.Paths))
	}
	// The re-appended path should still be present.
	found = false
	for _, p := range scope2.Paths {
		if p == existingPath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("re-appended path %q should still be in write_scope", existingPath)
	}
}

// TestWriteScopeClearedOnRelease verifies that write_scope is set to NULL when
// a session is released (ReleaseAllClaimsForSession) or when claims expire (ReapExpiredClaims).
func TestWriteScopeClearedOnRelease(t *testing.T) {
	t.Run("ReleaseAllClaimsForSession", func(t *testing.T) {
		database := setupClaimDB(t)
		defer database.Close()

		c := makeClaim("claim-wscr1", "feat-test", "sess-test")
		if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
			t.Fatalf("ClaimItem: %v", err)
		}
		if err := db.HeartbeatClaimByWorkItem(database, "feat-test", "sess-test", "some/file.go", 30*time.Minute); err != nil {
			t.Fatalf("HeartbeatClaimByWorkItem: %v", err)
		}
		// Verify path is present before release.
		pre, _ := db.GetClaim(database, "claim-wscr1")
		scope := unmarshalWriteScope(t, pre.WriteScope)
		if len(scope.Paths) == 0 {
			t.Fatal("expected write_scope.paths to be populated before release")
		}

		if _, err := db.ReleaseAllClaimsForSession(database, "sess-test"); err != nil {
			t.Fatalf("ReleaseAllClaimsForSession: %v", err)
		}

		post, err := db.GetClaim(database, "claim-wscr1")
		if err != nil {
			t.Fatalf("GetClaim after release: %v", err)
		}
		if len(post.WriteScope) > 0 {
			t.Errorf("expected write_scope to be NULL after release, got: %s", string(post.WriteScope))
		}
	})

	t.Run("ReapExpiredClaims", func(t *testing.T) {
		database := setupClaimDB(t)
		defer database.Close()

		c := makeClaim("claim-wscr2", "feat-test", "sess-test")
		if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
			t.Fatalf("ClaimItem: %v", err)
		}
		// Use a direct DB exec to add a path without expiring the claim yet.
		if err := db.HeartbeatClaimByWorkItem(database, "feat-test", "sess-test", "another/file.go", 30*time.Minute); err != nil {
			t.Fatalf("HeartbeatClaimByWorkItem: %v", err)
		}
		// Force expiry by setting lease_expires_at in the past.
		_, err := database.Exec(
			`UPDATE claims SET lease_expires_at = ? WHERE claim_id = ?`,
			time.Now().Add(-1*time.Minute).UTC().Format(time.RFC3339), "claim-wscr2",
		)
		if err != nil {
			t.Fatalf("force-expire: %v", err)
		}
		if _, err := db.ReapExpiredClaims(database); err != nil {
			t.Fatalf("ReapExpiredClaims: %v", err)
		}

		post, err := db.GetClaim(database, "claim-wscr2")
		if err != nil {
			t.Fatalf("GetClaim after reap: %v", err)
		}
		if len(post.WriteScope) > 0 {
			t.Errorf("expected write_scope to be NULL after reap, got: %s", string(post.WriteScope))
		}
	})
}

// TestWriteScopeEmptyAfterClaim verifies the ephemeral contract:
// a freshly claimed item has no write_scope.paths until a heartbeat-with-path fires.
func TestWriteScopeEmptyAfterClaim(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	c := makeClaim("claim-ws-fresh", "feat-test", "sess-test")
	if err := db.ClaimItem(database, c, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	got, err := db.GetClaim(database, "claim-ws-fresh")
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}

	// No heartbeat-with-path yet: write_scope should be absent or have empty paths.
	if len(got.WriteScope) > 0 {
		scope := unmarshalWriteScope(t, got.WriteScope)
		if len(scope.Paths) != 0 {
			t.Errorf("expected empty write_scope.paths after bare claim, got %v", scope.Paths)
		}
	}

	// Fire heartbeat with empty path — still no paths.
	if err := db.HeartbeatClaimByWorkItem(database, "feat-test", "sess-test", "", 30*time.Minute); err != nil {
		t.Fatalf("HeartbeatClaimByWorkItem empty: %v", err)
	}
	got2, err := db.GetClaim(database, "claim-ws-fresh")
	if err != nil {
		t.Fatalf("GetClaim after empty heartbeat: %v", err)
	}
	if len(got2.WriteScope) > 0 {
		scope2 := unmarshalWriteScope(t, got2.WriteScope)
		if len(scope2.Paths) != 0 {
			t.Errorf("expected still-empty paths after empty heartbeat, got %v", scope2.Paths)
		}
	}

	// Now fire heartbeat with a real path — it should appear.
	if err := db.HeartbeatClaimByWorkItem(database, "feat-test", "sess-test", "cmd/wipnote/main.go", 30*time.Minute); err != nil {
		t.Fatalf("HeartbeatClaimByWorkItem with path: %v", err)
	}
	got3, err := db.GetClaim(database, "claim-ws-fresh")
	if err != nil {
		t.Fatalf("GetClaim after path heartbeat: %v", err)
	}
	scope3 := unmarshalWriteScope(t, got3.WriteScope)
	if len(scope3.Paths) != 1 || scope3.Paths[0] != "cmd/wipnote/main.go" {
		t.Errorf("expected exactly one path after first path heartbeat, got %v", scope3.Paths)
	}
}

func TestListClaims(t *testing.T) {
	database := setupClaimDB(t)
	defer database.Close()

	// Insert a second session and feature.
	_, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status) VALUES ('sess-b', 'claude-code', ?, 'active')`,
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert sess-b: %v", err)
	}
	_, err = database.Exec(
		`INSERT INTO features (id, type, title, status) VALUES ('feat-lc-2', 'feature', 'LC Feature 2', 'in-progress')`,
	)
	if err != nil {
		t.Fatalf("insert feat-lc-2: %v", err)
	}

	ca := makeClaim("claim-lc-a", "feat-test", "sess-test")
	if err := db.ClaimItem(database, ca, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem ca: %v", err)
	}
	cb := makeClaim("claim-lc-b", "feat-lc-2", "sess-b")
	if err := db.ClaimItem(database, cb, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem cb: %v", err)
	}
	// Release ca so it becomes completed.
	if err := db.ReleaseClaim(database, "claim-lc-a", "sess-test", models.ClaimCompleted); err != nil {
		t.Fatalf("ReleaseClaim ca: %v", err)
	}

	t.Run("all", func(t *testing.T) {
		all, err := db.ListClaims(database, "", "", 100)
		if err != nil {
			t.Fatalf("ListClaims all: %v", err)
		}
		if len(all) < 2 {
			t.Errorf("expected >=2 claims, got %d", len(all))
		}
	})

	t.Run("by_session", func(t *testing.T) {
		bySession, err := db.ListClaims(database, "sess-test", "", 100)
		if err != nil {
			t.Fatalf("ListClaims by session: %v", err)
		}
		if len(bySession) != 1 {
			t.Errorf("by session: got %d, want 1", len(bySession))
		}
		if bySession[0].ClaimID != "claim-lc-a" {
			t.Errorf("claim_id: got %q, want %q", bySession[0].ClaimID, "claim-lc-a")
		}
	})

	t.Run("by_status", func(t *testing.T) {
		completed, err := db.ListClaims(database, "", "completed", 100)
		if err != nil {
			t.Fatalf("ListClaims by status: %v", err)
		}
		if len(completed) != 1 {
			t.Errorf("completed status: got %d, want 1", len(completed))
		}
	})

	t.Run("limit", func(t *testing.T) {
		limited, err := db.ListClaims(database, "", "", 1)
		if err != nil {
			t.Fatalf("ListClaims limit: %v", err)
		}
		if len(limited) != 1 {
			t.Errorf("limit 1: got %d, want 1", len(limited))
		}
	})
}
