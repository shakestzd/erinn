package db_test

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
)

func TestPropagateFamilyAttribution_CopiesToChildrenLackingAttribution(t *testing.T) {
	database := openIsolatedDB(t)

	now := time.Now().UTC()
	// Parent stub: older, carries active_feature_id.
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, session_family_id, active_feature_id)
		 VALUES (?, 'claude-code', ?, 'active', 'fam-1', 'feat-x')`,
		"sess-parent", now.Add(-2*time.Hour).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert parent: %v", err)
	}
	// Child: newer, no attribution.
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, session_family_id)
		 VALUES (?, 'claude-code', ?, 'active', 'fam-1')`,
		"sess-child", now.Add(-time.Hour).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert child: %v", err)
	}
	// Unrelated family member must stay untouched.
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, session_family_id)
		 VALUES (?, 'claude-code', ?, 'active', 'fam-2')`,
		"sess-other", now.Add(-30*time.Minute).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert other: %v", err)
	}

	n, err := db.PropagateFamilyAttribution(database, "fam-1")
	if err != nil {
		t.Fatalf("PropagateFamilyAttribution: %v", err)
	}
	if n != 1 {
		t.Fatalf("updated count = %d, want 1", n)
	}
	if got := db.GetActiveFeatureIDForSession(database, "sess-child"); got != "feat-x" {
		t.Fatalf("child active_feature_id = %q, want feat-x", got)
	}
	// Unrelated family untouched.
	if got := db.GetActiveFeatureIDForSession(database, "sess-other"); got != "" {
		t.Fatalf("unrelated active_feature_id = %q, want empty", got)
	}
}

func TestPropagateFamilyAttribution_DoesNotOverwriteExisting(t *testing.T) {
	database := openIsolatedDB(t)

	now := time.Now().UTC()
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, session_family_id, active_feature_id)
		 VALUES (?, 'claude-code', ?, 'active', 'fam-1', 'feat-x')`,
		"sess-parent", now.Add(-2*time.Hour).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert parent: %v", err)
	}
	// Child already attributed to a DIFFERENT item — must be preserved.
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, session_family_id, active_feature_id)
		 VALUES (?, 'claude-code', ?, 'active', 'fam-1', 'feat-y')`,
		"sess-child", now.Add(-time.Hour).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert child: %v", err)
	}

	n, err := db.PropagateFamilyAttribution(database, "fam-1")
	if err != nil {
		t.Fatalf("PropagateFamilyAttribution: %v", err)
	}
	if n != 0 {
		t.Fatalf("updated count = %d, want 0 (nothing lacking attribution)", n)
	}
	if got := db.GetActiveFeatureIDForSession(database, "sess-child"); got != "feat-y" {
		t.Fatalf("child active_feature_id = %q, want feat-y preserved", got)
	}
}

func TestPropagateFamilyAttribution_NoDonorIsNoOp(t *testing.T) {
	database := openIsolatedDB(t)

	now := time.Now().UTC()
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, session_family_id)
		 VALUES (?, 'claude-code', ?, 'active', 'fam-1')`,
		"sess-a", now.Add(-2*time.Hour).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert a: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, session_family_id)
		 VALUES (?, 'claude-code', ?, 'active', 'fam-1')`,
		"sess-b", now.Add(-time.Hour).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert b: %v", err)
	}

	n, err := db.PropagateFamilyAttribution(database, "fam-1")
	if err != nil {
		t.Fatalf("PropagateFamilyAttribution: %v", err)
	}
	if n != 0 {
		t.Fatalf("updated count = %d, want 0 (no donor)", n)
	}
}

func TestPropagateFamilyAttribution_PropagatesViaActiveWorkItem(t *testing.T) {
	database := openIsolatedDB(t)

	now := time.Now().UTC()
	// Donor carries attribution only via the active_work_items table, not the
	// legacy active_feature_id column.
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, session_family_id)
		 VALUES (?, 'claude-code', ?, 'active', 'fam-1')`,
		"sess-parent", now.Add(-2*time.Hour).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert parent: %v", err)
	}
	if err := db.SetActiveWorkItem(database, "sess-parent", db.AgentRootSentinel, "feat-z"); err != nil {
		t.Fatalf("SetActiveWorkItem: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, session_family_id)
		 VALUES (?, 'claude-code', ?, 'active', 'fam-1')`,
		"sess-child", now.Add(-time.Hour).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert child: %v", err)
	}

	n, err := db.PropagateFamilyAttribution(database, "fam-1")
	if err != nil {
		t.Fatalf("PropagateFamilyAttribution: %v", err)
	}
	if n != 1 {
		t.Fatalf("updated count = %d, want 1", n)
	}
	if got := db.GetActiveWorkItemWithFallback(database, "sess-child", db.AgentRootSentinel); got != "feat-z" {
		t.Fatalf("child resolved work item = %q, want feat-z", got)
	}
}

// TestPropagateFamilyAttribution_CountReflectsRowsAffected verifies the updated
// count is driven by rows actually written (conditional UPDATE), and that a
// second pass is a no-op once every sibling is attributed — the idempotency that
// the TOCTOU-safe WHERE guard provides.
func TestPropagateFamilyAttribution_CountReflectsRowsAffected(t *testing.T) {
	database := openIsolatedDB(t)

	now := time.Now().UTC()
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, session_family_id, active_feature_id)
		 VALUES (?, 'claude-code', ?, 'active', 'fam-1', 'feat-x')`,
		"sess-parent", now.Add(-3*time.Hour).Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert parent: %v", err)
	}
	for i, sid := range []string{"sess-child-a", "sess-child-b"} {
		if _, err := database.Exec(
			`INSERT INTO sessions (session_id, agent_assigned, created_at, status, session_family_id)
			 VALUES (?, 'claude-code', ?, 'active', 'fam-1')`,
			sid, now.Add(-time.Duration(i+1)*time.Hour).Format(time.RFC3339),
		); err != nil {
			t.Fatalf("insert %s: %v", sid, err)
		}
	}

	n, err := db.PropagateFamilyAttribution(database, "fam-1")
	if err != nil {
		t.Fatalf("PropagateFamilyAttribution: %v", err)
	}
	if n != 2 {
		t.Fatalf("first pass updated = %d, want 2", n)
	}
	// Second pass: every sibling now attributed → conditional UPDATE writes nothing.
	n2, err := db.PropagateFamilyAttribution(database, "fam-1")
	if err != nil {
		t.Fatalf("second PropagateFamilyAttribution: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("second pass updated = %d, want 0 (idempotent)", n2)
	}
}

func TestPropagateFamilyAttribution_EmptyFamilyIsNoOp(t *testing.T) {
	database := openIsolatedDB(t)
	n, err := db.PropagateFamilyAttribution(database, "")
	if err != nil {
		t.Fatalf("PropagateFamilyAttribution empty: %v", err)
	}
	if n != 0 {
		t.Fatalf("updated count = %d, want 0", n)
	}
}
