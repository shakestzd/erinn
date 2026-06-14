package main

import (
	"os"
	"path/filepath"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

// TestReindexPlanApprovals_ReplaysYAMLApprovals verifies that reindexPlanApprovals
// reads ApprovalStatus from YAML slices and inserts plan_feedback rows so that
// IsPlanFullyApproved returns true on a fresh DB (bug-eca8141d scenario).
func TestReindexPlanApprovals_ReplaysYAMLApprovals(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}

	dbDir := filepath.Join(dir, ".db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("mkdir .db: %v", err)
	}
	dbPath := filepath.Join(dbDir, "wipnote.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	// Write a plan YAML with all slices approved (simulates post-approve-slice state).
	const planID = "plan-reindex-test01"
	plan := &planyaml.PlanYAML{
		Meta: planyaml.PlanMeta{
			ID:     planID,
			Title:  "Test Plan",
			Status: "review",
		},
		Slices: []planyaml.PlanSlice{
			{Num: 1, Title: "Slice One", ApprovalStatus: "approved"},
			{Num: 2, Title: "Slice Two", ApprovalStatus: "approved"},
			{Num: 3, Title: "Slice Three", ApprovalStatus: "approved"},
		},
	}
	yamlPath := filepath.Join(plansDir, planID+".yaml")
	if err := planyaml.Save(yamlPath, plan); err != nil {
		t.Fatalf("save plan YAML: %v", err)
	}

	// Confirm fresh DB has no plan_feedback rows.
	approvals, err := dbpkg.GetSliceApprovals(database, planID)
	if err != nil {
		t.Fatalf("GetSliceApprovals pre-reindex: %v", err)
	}
	if len(approvals) != 0 {
		t.Fatalf("expected 0 slice approvals before reindex, got %d", len(approvals))
	}

	// Run reindex.
	planFiles, rowsReplayed, errs := reindexPlanApprovals(database, dir)
	if errs != 0 {
		t.Fatalf("reindexPlanApprovals: %d errors", errs)
	}
	if planFiles != 1 {
		t.Errorf("expected 1 plan file, got %d", planFiles)
	}
	if rowsReplayed != 3 {
		t.Errorf("expected 3 rows replayed, got %d", rowsReplayed)
	}

	// After reindex: IsPlanFullyApproved must return true.
	approved, err := dbpkg.IsPlanFullyApproved(database, planID, nil)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved after reindex: %v", err)
	}
	if !approved {
		t.Error("expected true: reindex should have replayed approval rows so IsPlanFullyApproved returns true")
	}
}

// TestReindexPlanApprovals_DoesNotOverwriteLiveRows verifies that INSERT OR IGNORE
// semantics preserve live interactive rows written after the last reindex.
func TestReindexPlanApprovals_DoesNotOverwriteLiveRows(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}

	dbDir := filepath.Join(dir, ".db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("mkdir .db: %v", err)
	}
	dbPath := filepath.Join(dbDir, "wipnote.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	const planID = "plan-overwrite-test01"
	plan := &planyaml.PlanYAML{
		Meta: planyaml.PlanMeta{ID: planID, Title: "Overwrite Test"},
		Slices: []planyaml.PlanSlice{
			{Num: 1, Title: "Slice One", ApprovalStatus: "approved"},
			{Num: 2, Title: "Slice Two", ApprovalStatus: "approved"},
		},
	}
	yamlPath := filepath.Join(plansDir, planID+".yaml")
	if err := planyaml.Save(yamlPath, plan); err != nil {
		t.Fatalf("save plan YAML: %v", err)
	}

	// Pre-write a live row that says slice-1 is REJECTED (disagrees with YAML).
	if err := dbpkg.StorePlanFeedback(database, planID, "slice-1", "approve", "false", ""); err != nil {
		t.Fatalf("pre-write slice-1 rejected: %v", err)
	}

	// Reindex.
	_, rowsReplayed, errs := reindexPlanApprovals(database, dir)
	if errs != 0 {
		t.Fatalf("reindexPlanApprovals: %d errors", errs)
	}
	// Only slice-2 should be inserted (slice-1 row already exists and is preserved).
	if rowsReplayed != 1 {
		t.Errorf("expected 1 new row (slice-2 only), got %d", rowsReplayed)
	}

	// slice-1 must still be "rejected" (live row preserved).
	approvals, err := dbpkg.GetSliceApprovals(database, planID)
	if err != nil {
		t.Fatalf("GetSliceApprovals: %v", err)
	}
	if approvals["slice-1"] != "rejected" {
		t.Errorf("slice-1: expected rejected (live row preserved), got %q", approvals["slice-1"])
	}
	if approvals["slice-2"] != "approved" {
		t.Errorf("slice-2: expected approved (replayed from YAML), got %q", approvals["slice-2"])
	}
}

// TestReindexPlanApprovals_PendingSliceNotReplayed verifies that slices with no
// ApprovalStatus (pending) do not get a spurious "false" row inserted.
func TestReindexPlanApprovals_PendingSliceNotReplayed(t *testing.T) {
	if testing.Short() {
		t.Skip("drives reindex integration flow")
	}

	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}

	dbDir := filepath.Join(dir, ".db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("mkdir .db: %v", err)
	}
	dbPath := filepath.Join(dbDir, "wipnote.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	const planID = "plan-pending-test01"
	plan := &planyaml.PlanYAML{
		Meta: planyaml.PlanMeta{ID: planID, Title: "Pending Test"},
		Slices: []planyaml.PlanSlice{
			{Num: 1, Title: "Slice One", ApprovalStatus: "approved"},
			{Num: 2, Title: "Slice Two", ApprovalStatus: ""}, // pending
			{Num: 3, Title: "Slice Three", ApprovalStatus: "rejected"},
		},
	}
	yamlPath := filepath.Join(plansDir, planID+".yaml")
	if err := planyaml.Save(yamlPath, plan); err != nil {
		t.Fatalf("save plan YAML: %v", err)
	}

	_, rowsReplayed, errs := reindexPlanApprovals(database, dir)
	if errs != 0 {
		t.Fatalf("reindexPlanApprovals: %d errors", errs)
	}
	// Pending slice (num=2) must NOT generate a row. Only approved+rejected get rows.
	if rowsReplayed != 2 {
		t.Errorf("expected 2 rows (approved + rejected, not pending), got %d", rowsReplayed)
	}

	// Confirm slice-2 has no row.
	approvals, err := dbpkg.GetSliceApprovals(database, planID)
	if err != nil {
		t.Fatalf("GetSliceApprovals: %v", err)
	}
	if _, has := approvals["slice-2"]; has {
		t.Error("slice-2 (pending) must not have a plan_feedback row after reindex")
	}

	// Confirm IsPlanFullyApproved is false (pending slice missing).
	yamlSlicesForCheck := []dbpkg.PlanSliceApproval{
		{Num: 1, ApprovalStatus: "approved"},
		{Num: 2, ApprovalStatus: ""},
		{Num: 3, ApprovalStatus: "rejected"},
	}
	approved, err := dbpkg.IsPlanFullyApproved(database, planID, yamlSlicesForCheck)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved: %v", err)
	}
	if approved {
		t.Error("expected false: pending and rejected slices must block finalize")
	}
}
