package db_test

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
)

// TestGetCurrentSessionResumable_SurfacesSessionWithoutWorkItem proves the
// current-session lookup bypasses the work_item_id <> '' gate that the grouped
// listing applies: a session with no work-item attribution is still returned.
func TestGetCurrentSessionResumable_SurfacesSessionWithoutWorkItem(t *testing.T) {
	database := openIsolatedDB(t)

	now := time.Now().UTC()
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, harness)
		 VALUES (?, 'claude-code', ?, 'active', 'claude')`,
		"sess-current", now.Add(-time.Minute).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	got, err := db.GetCurrentSessionResumable(database, time.Hour, []string{"sess-current"})
	if err != nil {
		t.Fatalf("GetCurrentSessionResumable: %v", err)
	}
	if got == nil {
		t.Fatal("got nil, want the current session even without a work item")
	}
	if got.LastSessionID != "sess-current" {
		t.Fatalf("LastSessionID = %q, want sess-current", got.LastSessionID)
	}
	if got.WorkItemID != "" {
		t.Fatalf("WorkItemID = %q, want empty", got.WorkItemID)
	}
	if got.Harness != "claude" {
		t.Fatalf("Harness = %q, want claude", got.Harness)
	}
}

// TestGetCurrentSessionResumable_SurfacesCompletedWorkItem proves the lookup
// bypasses the item_status NOT IN ('done','completed') filter: a session whose
// work item is completed is still surfaced as the current session.
func TestGetCurrentSessionResumable_SurfacesCompletedWorkItem(t *testing.T) {
	database := openIsolatedDB(t)

	now := time.Now().UTC()
	if _, err := database.Exec(
		`INSERT INTO features (id, type, title, status) VALUES ('feat-done', 'feature', 'Done', 'done')`,
	); err != nil {
		t.Fatalf("insert feature: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, harness, active_feature_id)
		 VALUES (?, 'claude-code', ?, 'active', 'claude', 'feat-done')`,
		"sess-current", now.Add(-time.Minute).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	got, err := db.GetCurrentSessionResumable(database, time.Hour, []string{"sess-current"})
	if err != nil {
		t.Fatalf("GetCurrentSessionResumable: %v", err)
	}
	if got == nil {
		t.Fatal("got nil, want the current session even with a completed work item")
	}
	if got.WorkItemID != "feat-done" {
		t.Fatalf("WorkItemID = %q, want feat-done", got.WorkItemID)
	}
	if got.Title != "Done" {
		t.Fatalf("Title = %q, want Done", got.Title)
	}
}

// TestGetCurrentSessionResumable_PicksMostRecentAmongIDs verifies that when
// several candidate session IDs (the family members) are passed, the most
// recently active one is chosen.
func TestGetCurrentSessionResumable_PicksMostRecentAmongIDs(t *testing.T) {
	database := openIsolatedDB(t)

	now := time.Now().UTC()
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, harness)
		 VALUES (?, 'claude-code', ?, 'active', 'claude')`,
		"sess-stub", now.Add(-3*time.Hour).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert stub: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, harness)
		 VALUES (?, 'claude-code', ?, 'active', 'claude')`,
		"sess-child", now.Add(-time.Minute).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert child: %v", err)
	}

	got, err := db.GetCurrentSessionResumable(database, 24*time.Hour, []string{"sess-stub", "sess-child"})
	if err != nil {
		t.Fatalf("GetCurrentSessionResumable: %v", err)
	}
	if got == nil {
		t.Fatal("got nil, want the most recent family member")
	}
	if got.LastSessionID != "sess-child" {
		t.Fatalf("LastSessionID = %q, want sess-child (most recent)", got.LastSessionID)
	}
}

// TestGetCurrentSessionResumable_UsesRootAgentWorkItem verifies the awi join is
// scoped to the root agent: a session carrying both a subagent claim and a root
// claim resolves to the ROOT work item (and appears once), not an arbitrary
// subagent's item.
func TestGetCurrentSessionResumable_UsesRootAgentWorkItem(t *testing.T) {
	database := openIsolatedDB(t)

	now := time.Now().UTC()
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, harness)
		 VALUES (?, 'claude-code', ?, 'active', 'claude')`,
		"sess-cur", now.Add(-time.Minute).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	// A subagent claim and a root claim coexist on the same session.
	if err := db.SetActiveWorkItem(database, "sess-cur", "subagent-x", "feat-sub"); err != nil {
		t.Fatalf("set subagent claim: %v", err)
	}
	if err := db.SetActiveWorkItem(database, "sess-cur", db.AgentRootSentinel, "feat-root"); err != nil {
		t.Fatalf("set root claim: %v", err)
	}

	got, err := db.GetCurrentSessionResumable(database, time.Hour, []string{"sess-cur"})
	if err != nil {
		t.Fatalf("GetCurrentSessionResumable: %v", err)
	}
	if got == nil {
		t.Fatal("got nil, want the current session")
	}
	if got.WorkItemID != "feat-root" {
		t.Fatalf("WorkItemID = %q, want feat-root (root-agent scoped)", got.WorkItemID)
	}
}

// TestGetCurrentSessionResumable_NilWhenUnknown verifies clean degradation: no
// matching session row yields (nil, nil), so the chooser simply omits the slot.
func TestGetCurrentSessionResumable_NilWhenUnknown(t *testing.T) {
	database := openIsolatedDB(t)

	got, err := db.GetCurrentSessionResumable(database, time.Hour, []string{"sess-missing"})
	if err != nil {
		t.Fatalf("GetCurrentSessionResumable: %v", err)
	}
	if got != nil {
		t.Fatalf("got %+v, want nil for unknown session", got)
	}

	// Empty ID list is also a clean no-op.
	got, err = db.GetCurrentSessionResumable(database, time.Hour, nil)
	if err != nil {
		t.Fatalf("GetCurrentSessionResumable(nil): %v", err)
	}
	if got != nil {
		t.Fatalf("got %+v, want nil for empty id list", got)
	}
}
