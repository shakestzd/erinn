package main

import (
	"os"
	"path/filepath"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

// TestReindexPlanFeedback_ReplaysYAMLApprovals verifies that reindexPlanFeedback
// reads ApprovalStatus from YAML slices and inserts plan_feedback rows so that
// IsPlanFullyApproved returns true on a fresh DB (bug-eca8141d scenario).
func TestReindexPlanFeedback_ReplaysYAMLApprovals(t *testing.T) {
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
	planFiles, rowsReplayed, errs := reindexPlanFeedback(database, dir)
	if errs != 0 {
		t.Fatalf("reindexPlanFeedback: %d errors", errs)
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

// TestReindexPlanFeedback_DoesNotOverwriteLiveRows verifies that INSERT OR IGNORE
// semantics preserve live interactive rows written after the last reindex.
func TestReindexPlanFeedback_DoesNotOverwriteLiveRows(t *testing.T) {
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
	_, rowsReplayed, errs := reindexPlanFeedback(database, dir)
	if errs != 0 {
		t.Fatalf("reindexPlanFeedback: %d errors", errs)
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

// TestReindexPlanFeedback_PendingSliceNotReplayed verifies that slices with no
// ApprovalStatus (pending) do not get a spurious "false" row inserted.
func TestReindexPlanFeedback_PendingSliceNotReplayed(t *testing.T) {
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

	_, rowsReplayed, errs := reindexPlanFeedback(database, dir)
	if errs != 0 {
		t.Fatalf("reindexPlanFeedback: %d errors", errs)
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

// TestReindexPlanFeedback_ReplaysNonApproveActions is the regression for the
// allowlist defect (feat-fc3cc9e0): the old reindexPlanApprovals only ever
// replayed action="approve" rows synthesized from slice fields, so
// set_execution_status, amendment, and any other action recorded in
// plan.Feedback.Entries were invisible to a fresh ephemeral projection no
// matter what canonical YAML held — a read-side filter discarding writes
// that reached canonical storage correctly. This seeds THREE different
// actions (set_execution_status, amendment, and comment — deliberately more
// than the two that were caught by name, to prove the fix is a general
// "replay everything" rather than three more hardcoded cases) directly in
// plan.Feedback.Entries and confirms every one of them lands in plan_feedback
// after reindex, not just the approve rows the old allowlist admitted.
func TestReindexPlanFeedback_ReplaysNonApproveActions(t *testing.T) {
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

	const planID = "plan-nonapprove-test01"
	plan := &planyaml.PlanYAML{
		Meta: planyaml.PlanMeta{ID: planID, Title: "Non-Approve Actions Test"},
		Slices: []planyaml.PlanSlice{
			{Num: 1, Title: "Slice One"},
		},
		Feedback: &planyaml.PlanFeedback{
			Entries: []planyaml.PlanFeedbackEntry{
				{Section: "slice-1", Action: "set_execution_status", Value: "done"},
				{Section: "slice-1", Action: "amendment", Value: `{"slice_num":1,"field":"what","operation":"set","content":"revised"}`},
				{Section: "slice-1", Action: "comment", Value: "looks good"},
			},
		},
	}
	yamlPath := filepath.Join(plansDir, planID+".yaml")
	if err := planyaml.Save(yamlPath, plan); err != nil {
		t.Fatalf("save plan YAML: %v", err)
	}

	_, rowsReplayed, errs := reindexPlanFeedback(database, dir)
	if errs != 0 {
		t.Fatalf("reindexPlanFeedback: %d errors", errs)
	}
	if rowsReplayed != 3 {
		t.Errorf("expected 3 rows replayed (set_execution_status, amendment, comment), got %d", rowsReplayed)
	}

	rows, err := dbpkg.GetPlanFeedback(database, planID)
	if err != nil {
		t.Fatalf("GetPlanFeedback: %v", err)
	}
	byAction := make(map[string]string, len(rows))
	for _, r := range rows {
		byAction[r.Action] = r.Value
	}
	if byAction["set_execution_status"] != "done" {
		t.Errorf("set_execution_status = %q, want %q", byAction["set_execution_status"], "done")
	}
	if byAction["amendment"] == "" {
		t.Error("amendment row missing after reindex — hydration is still discarding it")
	}
	if byAction["comment"] != "looks good" {
		t.Errorf("comment = %q, want %q", byAction["comment"], "looks good")
	}
}
