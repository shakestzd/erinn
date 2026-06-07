package db_test

// Tests for parallel attribution, collision detection, and out-of-order
// subagent attribution (slice-5, feat-6d8110b1, plan-1670cacd).

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// TestClaimCollisionPolicy — two root sessions claiming same work item:
// - First ClaimItemOrRenew succeeds.
// - Second ClaimItemOrRenew also succeeds (warn-and-allow, no block).
// - ListClaimsForWorkItem returns both active claims.
// - DetectCollaboration reports HasCollision=true with both sessions listed.
func TestClaimCollisionPolicy(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	insertExtraFeature(t, database, "feat-collab")
	insertExtraSession(t, database, "sess-root-A", "claude-code")
	insertExtraSession(t, database, "sess-root-B", "codex-cli")

	c1 := &models.Claim{
		ClaimID:          "clm-col-A",
		WorkItemID:       "feat-collab",
		OwnerSessionID:   "sess-root-A",
		OwnerAgent:       "claude-code",
		ClaimedByAgentID: "agent-A",
		Status:           models.ClaimInProgress,
	}
	if err := db.ClaimItemOrRenew(database, c1, 30*time.Minute); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	// Second root session — warn-and-allow: must NOT return an error.
	c2 := &models.Claim{
		ClaimID:          "clm-col-B",
		WorkItemID:       "feat-collab",
		OwnerSessionID:   "sess-root-B",
		OwnerAgent:       "codex-cli",
		ClaimedByAgentID: "agent-B",
		Status:           models.ClaimInProgress,
	}
	if err := db.ClaimItemOrRenew(database, c2, 30*time.Minute); err != nil {
		t.Fatalf("second claim (warn-and-allow must succeed): %v", err)
	}

	claimsForItem, err := db.ListClaimsForWorkItem(database, "feat-collab")
	if err != nil {
		t.Fatalf("ListClaimsForWorkItem: %v", err)
	}
	if len(claimsForItem) < 2 {
		t.Errorf("expected >=2 active claims, got %d", len(claimsForItem))
	}

	coll, err := db.DetectCollaboration(database, "feat-collab")
	if err != nil {
		t.Fatalf("DetectCollaboration: %v", err)
	}
	if !coll.HasCollision {
		t.Error("expected HasCollision=true")
	}
	if len(coll.Claimants) < 2 {
		t.Errorf("expected >=2 claimants, got %d", len(coll.Claimants))
	}
}

// TestSameHarnessTwoRootSessionsCollision is the headline slice-5 case:
// two ROOT sessions from the SAME harness claim the same work item. Root
// sessions share an (empty) claimed_by_agent_id, so before the renewal
// identity included owner_session_id the second ClaimItemOrRenew silently
// renewed the first session's row instead of inserting a second claimant —
// and DetectCollaboration never saw the collision. This test fails with the
// old (work_item_id, claimed_by_agent_id)-only renewal and passes once
// owner_session_id is part of the renewal identity.
func TestSameHarnessTwoRootSessionsCollision(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	insertExtraFeature(t, database, "feat-tworoot")
	insertExtraSession(t, database, "sess-root-1", "claude-code")
	insertExtraSession(t, database, "sess-root-2", "claude-code")

	// First root session — same harness, empty claimed_by_agent_id (root).
	c1 := &models.Claim{
		ClaimID:          "clm-root-1",
		WorkItemID:       "feat-tworoot",
		OwnerSessionID:   "sess-root-1",
		OwnerAgent:       "claude-code",
		ClaimedByAgentID: "", // root / orchestrator: no subagent discriminator
		Status:           models.ClaimInProgress,
	}
	if err := db.ClaimItemOrRenew(database, c1, 30*time.Minute); err != nil {
		t.Fatalf("first root claim: %v", err)
	}

	// Second root session, SAME harness, SAME empty claimed_by_agent_id.
	c2 := &models.Claim{
		ClaimID:          "clm-root-2",
		WorkItemID:       "feat-tworoot",
		OwnerSessionID:   "sess-root-2",
		OwnerAgent:       "claude-code",
		ClaimedByAgentID: "",
		Status:           models.ClaimInProgress,
	}
	if err := db.ClaimItemOrRenew(database, c2, 30*time.Minute); err != nil {
		t.Fatalf("second root claim (warn-and-allow must succeed): %v", err)
	}

	// Both root sessions must hold distinct active claim rows.
	claimsForItem, err := db.ListClaimsForWorkItem(database, "feat-tworoot")
	if err != nil {
		t.Fatalf("ListClaimsForWorkItem: %v", err)
	}
	if len(claimsForItem) != 2 {
		t.Fatalf("expected exactly 2 active claims (one per root session), got %d", len(claimsForItem))
	}

	// DetectCollaboration must report the collision between the two roots.
	coll, err := db.DetectCollaboration(database, "feat-tworoot")
	if err != nil {
		t.Fatalf("DetectCollaboration: %v", err)
	}
	if !coll.HasCollision {
		t.Fatal("expected HasCollision=true for two root sessions on same work item")
	}
	if len(coll.Claimants) != 2 {
		t.Fatalf("expected 2 distinct claimants, got %d", len(coll.Claimants))
	}

	// Idempotency guard: a SAME-session re-claim renews in place (no 3rd row).
	if err := db.ClaimItemOrRenew(database, c1, 30*time.Minute); err != nil {
		t.Fatalf("re-claim from sess-root-1: %v", err)
	}
	again, err := db.ListClaimsForWorkItem(database, "feat-tworoot")
	if err != nil {
		t.Fatalf("ListClaimsForWorkItem after re-claim: %v", err)
	}
	if len(again) != 2 {
		t.Fatalf("re-claim from same session must renew in place, expected 2 rows, got %d", len(again))
	}
}

// TestParallelHarnessClaims — three harnesses (Claude, Codex, Gemini) claim the
// same work item concurrently. All three must coexist; collaboration state shows
// all three with correct attribution (session, agent/harness).
func TestParallelHarnessClaims(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	insertExtraFeature(t, database, "feat-parallel")
	for _, s := range []struct{ id, harness string }{
		{"sess-claude-ph", "claude-code"},
		{"sess-codex-ph", "codex-cli"},
		{"sess-gemini-ph", "gemini-cli"},
	} {
		insertExtraSession(t, database, s.id, s.harness)
	}

	harnesses := []struct {
		claimID   string
		sessionID string
		agent     string
		agentID   string
	}{
		{"clm-ph-claude", "sess-claude-ph", "claude-code", "agent-ph-claude"},
		{"clm-ph-codex", "sess-codex-ph", "codex-cli", "agent-ph-codex"},
		{"clm-ph-gemini", "sess-gemini-ph", "gemini-cli", "agent-ph-gemini"},
	}

	for _, h := range harnesses {
		c := &models.Claim{
			ClaimID:          h.claimID,
			WorkItemID:       "feat-parallel",
			OwnerSessionID:   h.sessionID,
			OwnerAgent:       h.agent,
			ClaimedByAgentID: h.agentID,
			Status:           models.ClaimInProgress,
		}
		if err := db.ClaimItemOrRenew(database, c, 30*time.Minute); err != nil {
			t.Fatalf("claim %s: %v", h.claimID, err)
		}
	}

	coll, err := db.DetectCollaboration(database, "feat-parallel")
	if err != nil {
		t.Fatalf("DetectCollaboration: %v", err)
	}
	if !coll.HasCollision {
		t.Errorf("expected collision with 3 parallel harness claimants")
	}
	if len(coll.Claimants) != 3 {
		t.Errorf("expected 3 claimants, got %d", len(coll.Claimants))
	}

	for _, h := range harnesses {
		found := false
		for _, cl := range coll.Claimants {
			if cl.OwnerSessionID == h.sessionID && cl.OwnerAgent == h.agent {
				found = true
			}
		}
		if !found {
			t.Errorf("harness %s/%s not in collaboration claimants", h.agent, h.sessionID)
		}
	}
}

// TestSubagentAttribution_OutOfOrderParent verifies that subagent session rows
// written BEFORE the parent session record arrives are retained and correctly
// linked after BackfillParentSession is called.
func TestSubagentAttribution_OutOfOrderParent(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	insertExtraFeature(t, database, "feat-ooo")

	// Insert child session WITHOUT parent yet (out-of-order arrival).
	_, err := database.Exec(
		`INSERT OR IGNORE INTO sessions (session_id, agent_assigned, created_at, status, is_subagent)
		 VALUES ('agent-child-ooo', 'feature-coder', ?, 'active', 1)`,
		now.Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert child session: %v", err)
	}

	// Insert child event with work-item attribution before parent exists.
	ev := &models.AgentEvent{
		EventID:      "evt-ooo-1",
		AgentID:      "agent-child-ooo",
		EventType:    models.EventToolCall,
		Timestamp:    now,
		ToolName:     "Write",
		InputSummary: "child editing",
		SessionID:    "agent-child-ooo",
		FeatureID:    "feat-ooo",
		Status:       "started",
		Source:       "hook",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.InsertEvent(database, ev); err != nil {
		t.Fatalf("insert child event: %v", err)
	}

	// Parent arrives late.
	parent := &models.Session{
		SessionID:     "sess-parent-ooo",
		AgentAssigned: "claude-code",
		CreatedAt:     now,
		Status:        "active",
	}
	if err := db.InsertSession(database, parent); err != nil {
		t.Fatalf("insert parent: %v", err)
	}

	// Backfill the parent link on the child session.
	if err := db.BackfillParentSession(database, "agent-child-ooo", "sess-parent-ooo"); err != nil {
		t.Fatalf("BackfillParentSession: %v", err)
	}

	// Child event must still exist with feature attribution.
	retrieved, err := db.GetEvent(database, "evt-ooo-1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if retrieved == nil {
		t.Fatal("child event not found after parent backfill")
	}
	if retrieved.FeatureID != "feat-ooo" {
		t.Errorf("child event lost feature: got %q, want %q", retrieved.FeatureID, "feat-ooo")
	}

	// Parent row now has a linked child.
	var childSID string
	err = database.QueryRow(
		`SELECT session_id FROM sessions WHERE parent_session_id = ?`, "sess-parent-ooo",
	).Scan(&childSID)
	if err == sql.ErrNoRows {
		t.Error("child session not linked to parent after BackfillParentSession")
	} else if err != nil {
		t.Fatalf("query child: %v", err)
	}
}

// TestLiveCollision_LiveRefusal verifies that LiveCollision reports
// HasLiveCollision=true when a foreign session holds a fresh heartbeat.
func TestLiveCollision_LiveRefusal(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	insertExtraFeature(t, database, "feat-lc-live")
	insertExtraSession(t, database, "sess-holder-live", "claude-code")
	insertExtraSession(t, database, "sess-caller-live", "claude-code")

	// Holder writes a claim with a very recent heartbeat.
	holder := &models.Claim{
		ClaimID:          "clm-lc-holder",
		WorkItemID:       "feat-lc-live",
		OwnerSessionID:   "sess-holder-live",
		OwnerAgent:       "claude-code",
		ClaimedByAgentID: "agent-holder",
		Status:           models.ClaimInProgress,
	}
	if err := db.ClaimItemOrRenew(database, holder, 30*time.Minute); err != nil {
		t.Fatalf("holder claim: %v", err)
	}

	// Simulate a recent heartbeat for the holder session (now).
	_, err := database.Exec(
		`UPDATE claims SET last_heartbeat_at = ? WHERE claim_id = ?`,
		time.Now().UTC().Format(time.RFC3339), "clm-lc-holder",
	)
	if err != nil {
		t.Fatalf("update heartbeat: %v", err)
	}

	// Caller checks with a 2-minute liveness window — holder IS live.
	lc, err := db.LiveCollision(database, "feat-lc-live", "sess-caller-live", 2*time.Minute)
	if err != nil {
		t.Fatalf("LiveCollision: %v", err)
	}
	if !lc.HasLiveCollision {
		t.Error("expected HasLiveCollision=true, got false")
	}
	if len(lc.LiveClaimants) != 1 {
		t.Errorf("expected 1 live claimant, got %d", len(lc.LiveClaimants))
	}

	// LiveCollisionMessage must produce a non-empty refusal string.
	msg := db.LiveCollisionMessage(lc)
	if msg == "" {
		t.Error("LiveCollisionMessage returned empty string for live collision")
	}
	if !strings.Contains(msg, "--force") {
		t.Errorf("LiveCollisionMessage missing '--force' hint: %q", msg)
	}
}

// TestLiveCollision_StaleAllows verifies that a stale (expired-heartbeat)
// claimant does NOT produce a live collision — the item is reclaimable.
func TestLiveCollision_StaleAllows(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	insertExtraFeature(t, database, "feat-lc-stale")
	insertExtraSession(t, database, "sess-stale-holder", "claude-code")
	insertExtraSession(t, database, "sess-stale-caller", "claude-code")

	stale := &models.Claim{
		ClaimID:          "clm-lc-stale",
		WorkItemID:       "feat-lc-stale",
		OwnerSessionID:   "sess-stale-holder",
		OwnerAgent:       "claude-code",
		ClaimedByAgentID: "agent-stale",
		Status:           models.ClaimInProgress,
	}
	if err := db.ClaimItemOrRenew(database, stale, 30*time.Minute); err != nil {
		t.Fatalf("stale claim: %v", err)
	}

	// Back-date the heartbeat so it is well outside any liveness window.
	ancient := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	_, err := database.Exec(
		`UPDATE claims SET last_heartbeat_at = ? WHERE claim_id = ?`,
		ancient, "clm-lc-stale",
	)
	if err != nil {
		t.Fatalf("update heartbeat: %v", err)
	}

	// 2-minute window — stale holder is NOT live.
	lc, err := db.LiveCollision(database, "feat-lc-stale", "sess-stale-caller", 2*time.Minute)
	if err != nil {
		t.Fatalf("LiveCollision: %v", err)
	}
	if lc.HasLiveCollision {
		t.Error("expected HasLiveCollision=false for stale claimant, got true")
	}
	if len(lc.LiveClaimants) != 0 {
		t.Errorf("expected 0 live claimants, got %d", len(lc.LiveClaimants))
	}
}

// TestLiveCollision_SelfReclaim verifies that a session reclaiming its OWN
// item (same callerSessionID) is NOT reported as a live collision.
func TestLiveCollision_SelfReclaim(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	insertExtraFeature(t, database, "feat-lc-self")
	insertExtraSession(t, database, "sess-self-reclaim", "claude-code")

	self := &models.Claim{
		ClaimID:          "clm-lc-self",
		WorkItemID:       "feat-lc-self",
		OwnerSessionID:   "sess-self-reclaim",
		OwnerAgent:       "claude-code",
		ClaimedByAgentID: "agent-self",
		Status:           models.ClaimInProgress,
	}
	if err := db.ClaimItemOrRenew(database, self, 30*time.Minute); err != nil {
		t.Fatalf("self claim: %v", err)
	}

	// Fresh heartbeat — but caller IS the holder.
	_, err := database.Exec(
		`UPDATE claims SET last_heartbeat_at = ? WHERE claim_id = ?`,
		time.Now().UTC().Format(time.RFC3339), "clm-lc-self",
	)
	if err != nil {
		t.Fatalf("update heartbeat: %v", err)
	}

	lc, err := db.LiveCollision(database, "feat-lc-self", "sess-self-reclaim", 2*time.Minute)
	if err != nil {
		t.Fatalf("LiveCollision: %v", err)
	}
	if lc.HasLiveCollision {
		t.Error("expected HasLiveCollision=false for self-reclaim, got true")
	}
}

// TestLiveCollision_EndedSessionReclaimable verifies that a claim owned by a
// session whose status is terminal (completed/failed/paused) is NOT a live
// collision — even when the heartbeat is still within the staleness window.
// This is the fix for Ctrl+C-killed sessions that leave orphaned claims:
// SessionEnd hook is skipped by the kill, so the claim is never released,
// but the session row IS updated to a terminal status by the SessionStop hook.
func TestLiveCollision_EndedSessionReclaimable(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	insertExtraFeature(t, database, "feat-lc-ended")
	insertExtraSession(t, database, "sess-ended-holder", "claude-code")
	insertExtraSession(t, database, "sess-new-caller", "claude-code")

	ended := &models.Claim{
		ClaimID:          "clm-lc-ended",
		WorkItemID:       "feat-lc-ended",
		OwnerSessionID:   "sess-ended-holder",
		OwnerAgent:       "claude-code",
		ClaimedByAgentID: "agent-ended",
		Status:           models.ClaimInProgress,
	}
	if err := db.ClaimItemOrRenew(database, ended, 30*time.Minute); err != nil {
		t.Fatalf("ended claim: %v", err)
	}

	// Heartbeat is fresh (within window) — simulates the Ctrl+C scenario where
	// the session was killed before the heartbeat expired.
	_, err := database.Exec(
		`UPDATE claims SET last_heartbeat_at = ? WHERE claim_id = ?`,
		time.Now().UTC().Format(time.RFC3339), "clm-lc-ended",
	)
	if err != nil {
		t.Fatalf("update heartbeat: %v", err)
	}

	// Mark the session as completed (terminal status — set by SessionStop hook).
	_, err = database.Exec(
		`UPDATE sessions SET status = 'completed', completed_at = ? WHERE session_id = ?`,
		time.Now().UTC().Format(time.RFC3339), "sess-ended-holder",
	)
	if err != nil {
		t.Fatalf("mark session completed: %v", err)
	}

	// Despite fresh heartbeat, the ended session must NOT block reclaim.
	lc, err := db.LiveCollision(database, "feat-lc-ended", "sess-new-caller", 2*time.Minute)
	if err != nil {
		t.Fatalf("LiveCollision: %v", err)
	}
	if lc.HasLiveCollision {
		t.Error("ended session (status=completed) with fresh heartbeat must NOT be a live collision")
	}
	if len(lc.LiveClaimants) != 0 {
		t.Errorf("expected 0 live claimants for ended session, got %d", len(lc.LiveClaimants))
	}
}

// TestLiveCollision_ActiveNonEndedBlocks confirms that an active, non-ended
// session with a fresh heartbeat still produces a live collision (the positive
// case for the gate to ensure we haven't broken the original guard).
func TestLiveCollision_ActiveNonEndedBlocks(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	insertExtraFeature(t, database, "feat-lc-active")
	insertExtraSession(t, database, "sess-active-holder", "claude-code")
	insertExtraSession(t, database, "sess-active-caller", "claude-code")

	active := &models.Claim{
		ClaimID:          "clm-lc-active",
		WorkItemID:       "feat-lc-active",
		OwnerSessionID:   "sess-active-holder",
		OwnerAgent:       "claude-code",
		ClaimedByAgentID: "agent-active",
		Status:           models.ClaimInProgress,
	}
	if err := db.ClaimItemOrRenew(database, active, 30*time.Minute); err != nil {
		t.Fatalf("active claim: %v", err)
	}

	// Heartbeat is fresh; session stays 'active' (default from insertExtraSession).
	_, err := database.Exec(
		`UPDATE claims SET last_heartbeat_at = ? WHERE claim_id = ?`,
		time.Now().UTC().Format(time.RFC3339), "clm-lc-active",
	)
	if err != nil {
		t.Fatalf("update heartbeat: %v", err)
	}

	lc, err := db.LiveCollision(database, "feat-lc-active", "sess-active-caller", 2*time.Minute)
	if err != nil {
		t.Fatalf("LiveCollision: %v", err)
	}
	if !lc.HasLiveCollision {
		t.Error("active non-ended session with fresh heartbeat must be a live collision")
	}
	if len(lc.LiveClaimants) != 1 {
		t.Errorf("expected 1 live claimant, got %d", len(lc.LiveClaimants))
	}
}

// insertExtraFeature inserts a feature row for FK satisfaction (does not fail if exists).
func insertExtraFeature(t *testing.T, database *sql.DB, id string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := database.Exec(
		`INSERT OR IGNORE INTO features (id, type, title, status, priority, created_at, updated_at)
		 VALUES (?, 'feature', ?, 'in-progress', 'medium', ?, ?)`,
		id, id, now.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insertExtraFeature %s: %v", id, err)
	}
}

// insertExtraSession inserts a minimal session row for FK satisfaction.
func insertExtraSession(t *testing.T, database *sql.DB, sessionID, agent string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := database.Exec(
		`INSERT OR IGNORE INTO sessions (session_id, agent_assigned, created_at, status)
		 VALUES (?, ?, ?, 'active')`,
		sessionID, agent, now.Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insertExtraSession %s: %v", sessionID, err)
	}
}
