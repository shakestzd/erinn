package db_test

import (
	"database/sql"
	"testing"

	"github.com/shakestzd/wipnote/core/db"
)

// setupPlanDB returns an in-memory database with a test plan feature row.
func setupPlanDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	database := setupTestDB(t)
	planID := "plan-test-001"
	_, err := database.Exec(
		`INSERT INTO features (id, type, title, status) VALUES (?, 'plan', 'Test Plan', 'in-progress')`,
		planID,
	)
	if err != nil {
		t.Fatalf("insert test plan: %v", err)
	}
	return database, planID
}

func TestStorePlanFeedback(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	if err := db.StorePlanFeedback(database, planID, "design", "approve", "true", ""); err != nil {
		t.Fatalf("StorePlanFeedback: %v", err)
	}

	entries, err := db.GetPlanFeedback(database, planID)
	if err != nil {
		t.Fatalf("GetPlanFeedback: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	got := entries[0]
	if got.PlanID != planID {
		t.Errorf("PlanID: got %q, want %q", got.PlanID, planID)
	}
	if got.Section != "design" {
		t.Errorf("Section: got %q, want %q", got.Section, "design")
	}
	if got.Action != "approve" {
		t.Errorf("Action: got %q, want %q", got.Action, "approve")
	}
	if got.Value != "true" {
		t.Errorf("Value: got %q, want %q", got.Value, "true")
	}
}

func TestStorePlanAnnotation_TwoAxisState(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	ann := db.PlanAnnotation{
		Section:          "slice-3-block-data-model-1",
		Anchor:           "slice-3-block-data-model-1",
		Value:            "this column should be nullable",
		QuestionID:       "note-1",
		Consumed:         true,
		Resolved:         false,
		ResolutionTarget: "agent",
	}
	if err := db.StorePlanAnnotation(database, planID, ann); err != nil {
		t.Fatalf("StorePlanAnnotation: %v", err)
	}

	entries, err := db.GetPlanFeedback(database, planID)
	if err != nil {
		t.Fatalf("GetPlanFeedback: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	got := entries[0]
	if got.Action != "annotation" {
		t.Errorf("Action: got %q, want %q", got.Action, "annotation")
	}
	if got.Anchor != ann.Anchor {
		t.Errorf("Anchor: got %q, want %q", got.Anchor, ann.Anchor)
	}
	if !got.Consumed {
		t.Errorf("Consumed: got false, want true")
	}
	if got.Resolved {
		t.Errorf("Resolved: got true, want false")
	}
	if got.ResolutionTarget != "agent" {
		t.Errorf("ResolutionTarget: got %q, want %q", got.ResolutionTarget, "agent")
	}
	if got.Value != ann.Value {
		t.Errorf("Value: got %q, want %q", got.Value, ann.Value)
	}

	// Upsert: resolve the note and re-route to human. The two axes move
	// independently — consumed stays true, resolved flips to true.
	ann.Resolved = true
	ann.ResolutionTarget = "human"
	if err := db.StorePlanAnnotation(database, planID, ann); err != nil {
		t.Fatalf("StorePlanAnnotation upsert: %v", err)
	}
	entries, err = db.GetPlanFeedback(database, planID)
	if err != nil {
		t.Fatalf("GetPlanFeedback after upsert: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("upsert created a duplicate row: got %d entries, want 1", len(entries))
	}
	got = entries[0]
	if !got.Consumed || !got.Resolved {
		t.Errorf("after upsert: consumed=%v resolved=%v, want both true", got.Consumed, got.Resolved)
	}
	if got.ResolutionTarget != "human" {
		t.Errorf("after upsert ResolutionTarget: got %q, want %q", got.ResolutionTarget, "human")
	}
}

// TestStorePlanFeedback_LeavesAnnotationColumnsZero verifies that the existing
// approve/comment/answer writers do not disturb the new annotation columns.
func TestStorePlanFeedback_LeavesAnnotationColumnsZero(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	if err := db.StorePlanFeedback(database, planID, "slice-1", "approve", "true", ""); err != nil {
		t.Fatalf("StorePlanFeedback: %v", err)
	}
	entries, err := db.GetPlanFeedback(database, planID)
	if err != nil {
		t.Fatalf("GetPlanFeedback: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	got := entries[0]
	if got.Anchor != "" || got.Consumed || got.Resolved || got.ResolutionTarget != "" {
		t.Errorf("non-annotation row carried annotation state: %+v", got)
	}
}

func TestStorePlanFeedbackUpsert(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	// Store initial answer.
	if err := db.StorePlanFeedback(database, planID, "design", "answer", "option-a", "delivery-mode"); err != nil {
		t.Fatalf("first StorePlanFeedback: %v", err)
	}

	// Re-submit with a different value — should update, not duplicate.
	if err := db.StorePlanFeedback(database, planID, "design", "answer", "option-b", "delivery-mode"); err != nil {
		t.Fatalf("second StorePlanFeedback: %v", err)
	}

	entries, err := db.GetPlanFeedback(database, planID)
	if err != nil {
		t.Fatalf("GetPlanFeedback: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after upsert, got %d", len(entries))
	}
	if entries[0].Value != "option-b" {
		t.Errorf("upsert value: got %q, want %q", entries[0].Value, "option-b")
	}
}

func TestStorePlanFeedbackComment(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	if err := db.StorePlanFeedback(database, planID, "outline", "comment", "please expand section 2", ""); err != nil {
		t.Fatalf("StorePlanFeedback comment: %v", err)
	}

	entries, err := db.GetPlanFeedback(database, planID)
	if err != nil {
		t.Fatalf("GetPlanFeedback: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Action != "comment" {
		t.Errorf("Action: got %q, want comment", entries[0].Action)
	}
}

func TestGetPlanFeedbackBySection(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	if err := db.StorePlanFeedback(database, planID, "design", "approve", "true", ""); err != nil {
		t.Fatalf("StorePlanFeedback design: %v", err)
	}
	if err := db.StorePlanFeedback(database, planID, "outline", "approve", "true", ""); err != nil {
		t.Fatalf("StorePlanFeedback outline: %v", err)
	}
	if err := db.StorePlanFeedback(database, planID, "design", "comment", "looks good", ""); err != nil {
		t.Fatalf("StorePlanFeedback design comment: %v", err)
	}

	designEntries, err := db.GetPlanFeedbackBySection(database, planID, "design")
	if err != nil {
		t.Fatalf("GetPlanFeedbackBySection: %v", err)
	}
	if len(designEntries) != 2 {
		t.Errorf("design section: got %d entries, want 2", len(designEntries))
	}
	for _, e := range designEntries {
		if e.Section != "design" {
			t.Errorf("expected section 'design', got %q", e.Section)
		}
	}

	outlineEntries, err := db.GetPlanFeedbackBySection(database, planID, "outline")
	if err != nil {
		t.Fatalf("GetPlanFeedbackBySection outline: %v", err)
	}
	if len(outlineEntries) != 1 {
		t.Errorf("outline section: got %d entries, want 1", len(outlineEntries))
	}
}

func TestIsPlanFullyApproved_NotYet(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	// No feedback at all — not approved.
	approved, err := db.IsPlanFullyApproved(database, planID, nil)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved (no feedback): %v", err)
	}
	if approved {
		t.Error("expected false with no feedback")
	}

	// One section approved, another has a disapproval — not fully approved.
	if err := db.StorePlanFeedback(database, planID, "design", "approve", "true", ""); err != nil {
		t.Fatalf("StorePlanFeedback: %v", err)
	}
	if err := db.StorePlanFeedback(database, planID, "outline", "approve", "false", ""); err != nil {
		t.Fatalf("StorePlanFeedback: %v", err)
	}

	approved, err = db.IsPlanFullyApproved(database, planID, nil)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved (partial): %v", err)
	}
	if approved {
		t.Error("expected false when one section is disapproved")
	}
}

func TestIsPlanFullyApproved_True(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	sections := []string{"design", "outline", "slice-1", "slice-2"}
	for _, s := range sections {
		if err := db.StorePlanFeedback(database, planID, s, "approve", "true", ""); err != nil {
			t.Fatalf("StorePlanFeedback %s: %v", s, err)
		}
	}

	approved, err := db.IsPlanFullyApproved(database, planID, nil)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved: %v", err)
	}
	if !approved {
		t.Error("expected true when all sections approved")
	}
}

func TestIsPlanFullyApproved_LegacyApprovedValue(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	for _, s := range []string{"design", "outline", "slice-1"} {
		if err := db.StorePlanFeedback(database, planID, s, "approve", "approved", ""); err != nil {
			t.Fatalf("StorePlanFeedback %s: %v", s, err)
		}
	}

	approved, err := db.IsPlanFullyApproved(database, planID, nil)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved: %v", err)
	}
	if !approved {
		t.Error("expected legacy approve value 'approved' to count as approved")
	}
}

func TestIsPlanFullyApproved_LegacyNegativeValuesBlock(t *testing.T) {
	for _, value := range []string{"false", "rejected", "changes_requested"} {
		t.Run(value, func(t *testing.T) {
			database, planID := setupPlanDB(t)
			defer database.Close()

			if err := db.StorePlanFeedback(database, planID, "design", "approve", "approved", ""); err != nil {
				t.Fatalf("StorePlanFeedback design: %v", err)
			}
			if err := db.StorePlanFeedback(database, planID, "slice-1", "approve", value, ""); err != nil {
				t.Fatalf("StorePlanFeedback slice-1: %v", err)
			}

			approved, err := db.IsPlanFullyApproved(database, planID, nil)
			if err != nil {
				t.Fatalf("IsPlanFullyApproved: %v", err)
			}
			if approved {
				t.Errorf("expected legacy negative value %q to block approval", value)
			}
		})
	}
}

func TestIsPlanFullyApproved_WithNonApproveActions(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	// Approve one section and add comments — comments should not block approval.
	if err := db.StorePlanFeedback(database, planID, "design", "approve", "true", ""); err != nil {
		t.Fatalf("StorePlanFeedback approve: %v", err)
	}
	if err := db.StorePlanFeedback(database, planID, "design", "comment", "minor note", ""); err != nil {
		t.Fatalf("StorePlanFeedback comment: %v", err)
	}

	approved, err := db.IsPlanFullyApproved(database, planID, nil)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved: %v", err)
	}
	if !approved {
		t.Error("expected true: comments should not block approval")
	}
}

// TestIsPlanFullyApproved_V2SlicesOnly_IgnoresDesign verifies the slice-gated
// behavior: for a plan with slice approvals, finalize is allowed once all slices
// are approved, regardless of design/outline approval state — matching the
// dashboard review rail. This is the fix for "all slices approved but can't
// finalize because design/outline weren't approved".
func TestIsPlanFullyApproved_V2SlicesOnly_IgnoresDesign(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	// All slices approved; design left untouched AND outline explicitly disapproved.
	for _, s := range []string{"slice-1", "slice-2"} {
		if err := db.StorePlanFeedback(database, planID, s, "approve", "true", ""); err != nil {
			t.Fatalf("StorePlanFeedback %s: %v", s, err)
		}
	}
	if err := db.StorePlanFeedback(database, planID, "outline", "approve", "false", ""); err != nil {
		t.Fatalf("StorePlanFeedback outline: %v", err)
	}

	approved, err := db.IsPlanFullyApproved(database, planID, nil)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved: %v", err)
	}
	if !approved {
		t.Error("expected true: all slices approved should finalize regardless of design/outline")
	}

	// A rejected slice still blocks.
	if err := db.StorePlanFeedback(database, planID, "slice-2", "approve", "false", ""); err != nil {
		t.Fatalf("StorePlanFeedback slice-2 reject: %v", err)
	}
	approved, err = db.IsPlanFullyApproved(database, planID, nil)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved (after reject): %v", err)
	}
	if approved {
		t.Error("expected false: a rejected slice must block finalize")
	}
}

func TestFinalizePlan(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	if err := db.FinalizePlan(database, planID); err != nil {
		t.Fatalf("FinalizePlan: %v", err)
	}

	// Verify status updated in features table.
	var status string
	if err := database.QueryRow(`SELECT status FROM features WHERE id = ?`, planID).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "done" {
		t.Errorf("status: got %q, want done", status)
	}
}

func TestFinalizePlanNotFound(t *testing.T) {
	database, _ := setupPlanDB(t)
	defer database.Close()

	// Finalizing a non-existent plan succeeds gracefully (best-effort).
	// HTML is canonical — plans can exist as files without being indexed.
	err := db.FinalizePlan(database, "plan-does-not-exist")
	if err != nil {
		t.Errorf("expected graceful success for non-existent plan, got: %v", err)
	}
}

func TestFinalizePlanWrongType(t *testing.T) {
	database, _ := setupPlanDB(t)
	defer database.Close()

	// Insert a feature (not a plan) and try to finalize it.
	// Should succeed gracefully — the UPDATE won't match type='plan'.
	_, err := database.Exec(
		`INSERT INTO features (id, type, title, status) VALUES ('feat-not-plan', 'feature', 'Not a Plan', 'in-progress')`,
	)
	if err != nil {
		t.Fatalf("insert feature: %v", err)
	}

	if err := db.FinalizePlan(database, "feat-not-plan"); err != nil {
		t.Errorf("expected graceful success, got: %v", err)
	}

	// Verify the feature status was NOT changed (only type='plan' updates).
	var status string
	database.QueryRow(`SELECT status FROM features WHERE id = 'feat-not-plan'`).Scan(&status)
	if status != "in-progress" {
		t.Errorf("feature status changed unexpectedly: got %q, want in-progress", status)
	}
}

// ---- IsPlanFullyApproved regression tests (slice-4) ----------------------------

// TestIsPlanFullyApproved_LegacyOnly_StillPasses verifies that writing feedback rows
// ONLY for legacy sections (design, questions) and calling IsPlanFullyApproved
// continues to return true when all legacy sections are approved.
func TestIsPlanFullyApproved_LegacyOnly_StillPasses(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	// Write legacy section approvals only.
	legacySections := []string{"design", "outline"}
	for _, s := range legacySections {
		if err := db.StorePlanFeedback(database, planID, s, "approve", "true", ""); err != nil {
			t.Fatalf("StorePlanFeedback %s: %v", s, err)
		}
	}

	approved, err := db.IsPlanFullyApproved(database, planID, nil)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved: %v", err)
	}
	if !approved {
		t.Error("expected true when all legacy sections approved — legacy behavior must be preserved")
	}
}

// TestIsPlanFullyApproved_MixedLegacyAndV2_StableBehavior writes a V2 slice-local
// section row (section='slice-1') alongside legacy sections and asserts that
// IsPlanFullyApproved treats it uniformly — if all sections (legacy + V2) have
// approve=true, the function returns true.
func TestIsPlanFullyApproved_MixedLegacyAndV2_StableBehavior(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	// Write legacy + V2 sections, all approved.
	sections := []string{"design", "outline", "slice-1"}
	for _, s := range sections {
		if err := db.StorePlanFeedback(database, planID, s, "approve", "true", ""); err != nil {
			t.Fatalf("StorePlanFeedback %s: %v", s, err)
		}
	}

	approved, err := db.IsPlanFullyApproved(database, planID, nil)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved: %v", err)
	}
	if !approved {
		t.Error("expected true when all legacy + V2 sections approved")
	}

	// Now add a V2 section with approve=false — should return false.
	if err := db.StorePlanFeedback(database, planID, "slice-2", "approve", "false", ""); err != nil {
		t.Fatalf("StorePlanFeedback slice-2: %v", err)
	}

	approved, err = db.IsPlanFullyApproved(database, planID, nil)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved after slice-2 disapprove: %v", err)
	}
	if approved {
		t.Error("expected false when a V2 section (slice-2) is disapproved")
	}
}

// ---- NormalizePlanFeedbackValues tests (bug-4b399212) ---------------------------

// TestNormalizePlanFeedbackValues verifies that existing rows written by the buggy
// slice-card UI (value='approved'/'rejected'/'changes_requested') are mapped to the
// canonical boolean strings ('true'/'false'), and that the migration is idempotent.
func TestNormalizePlanFeedbackValues(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	// Insert raw rows bypassing StorePlanFeedback to simulate the buggy UI writer.
	_, err := database.Exec(`
		INSERT INTO plan_feedback (plan_id, section, action, value, question_id, created_at, updated_at)
		VALUES (?, 'slice-1', 'approve', 'approved', '', datetime('now'), datetime('now'))`,
		planID)
	if err != nil {
		t.Fatalf("insert approved row: %v", err)
	}
	_, err = database.Exec(`
		INSERT INTO plan_feedback (plan_id, section, action, value, question_id, created_at, updated_at)
		VALUES (?, 'slice-2', 'approve', 'rejected', '', datetime('now'), datetime('now'))`,
		planID)
	if err != nil {
		t.Fatalf("insert rejected row: %v", err)
	}
	_, err = database.Exec(`
		INSERT INTO plan_feedback (plan_id, section, action, value, question_id, created_at, updated_at)
		VALUES (?, 'slice-3', 'approve', 'changes_requested', '', datetime('now'), datetime('now'))`,
		planID)
	if err != nil {
		t.Fatalf("insert changes_requested row: %v", err)
	}

	// Run migration.
	if err := db.NormalizePlanFeedbackValues(database); err != nil {
		t.Fatalf("NormalizePlanFeedbackValues: %v", err)
	}

	// Verify values are now canonical.
	checkValue := func(section, want string) {
		t.Helper()
		var got string
		err := database.QueryRow(
			`SELECT value FROM plan_feedback WHERE plan_id=? AND section=? AND action='approve'`,
			planID, section,
		).Scan(&got)
		if err != nil {
			t.Fatalf("query %s: %v", section, err)
		}
		if got != want {
			t.Errorf("section %s: got %q, want %q", section, got, want)
		}
	}
	checkValue("slice-1", "true")
	checkValue("slice-2", "false")
	checkValue("slice-3", "false")

	// Run migration again — idempotency check, should not error and values unchanged.
	if err := db.NormalizePlanFeedbackValues(database); err != nil {
		t.Fatalf("NormalizePlanFeedbackValues (second run): %v", err)
	}
	checkValue("slice-1", "true")
	checkValue("slice-2", "false")
	checkValue("slice-3", "false")
}

// ---- GetSliceApprovals tests (slice-4) -----------------------------------------

// TestGetSliceApprovals_ReturnsPerSliceStatus verifies that GetSliceApprovals
// returns only slice-N keyed rows and maps them to approval status correctly.
func TestGetSliceApprovals_ReturnsPerSliceStatus(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	// Store mixed sections: design (legacy) and two slices.
	if err := db.StorePlanFeedback(database, planID, "design", "approve", "true", ""); err != nil {
		t.Fatalf("store design: %v", err)
	}
	if err := db.StorePlanFeedback(database, planID, "slice-1", "approve", "true", ""); err != nil {
		t.Fatalf("store slice-1: %v", err)
	}
	if err := db.StorePlanFeedback(database, planID, "slice-2", "approve", "false", ""); err != nil {
		t.Fatalf("store slice-2: %v", err)
	}

	approvals, err := db.GetSliceApprovals(database, planID)
	if err != nil {
		t.Fatalf("GetSliceApprovals: %v", err)
	}

	// Should only contain slice sections, not "design".
	if _, hasDesign := approvals["design"]; hasDesign {
		t.Error("GetSliceApprovals should not include legacy 'design' section")
	}
	if v, ok := approvals["slice-1"]; !ok || v != "approved" {
		t.Errorf("slice-1: got %q ok=%v, want approved=true", v, ok)
	}
	if v, ok := approvals["slice-2"]; !ok || v != "rejected" {
		t.Errorf("slice-2: got %q ok=%v, want rejected", v, ok)
	}
}

// ---- IsPlanFullyApproved regression tests (bug-9e45dd31) ---------------------------

// TestIsPlanFullyApproved_IgnoresNonUIExposedSections verifies that question sections
// (slice-N-question-*) and other non-UI-exposed sections are ignored by IsPlanFullyApproved.
// A plan with all UI sections approved (design + slice-1 approved) plus a question
// section disapproved should still return true.
func TestIsPlanFullyApproved_IgnoresNonUIExposedSections(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	// Store UI-exposed sections, all approved.
	if err := db.StorePlanFeedback(database, planID, "design", "approve", "true", ""); err != nil {
		t.Fatalf("store design: %v", err)
	}
	if err := db.StorePlanFeedback(database, planID, "slice-1", "approve", "true", ""); err != nil {
		t.Fatalf("store slice-1: %v", err)
	}

	// Store a non-UI-exposed question section, disapproved.
	// This should NOT block the approval, as questions are invisible to the gate.
	if err := db.StorePlanFeedback(database, planID, "slice-1-question-xyz", "approve", "false", ""); err != nil {
		t.Fatalf("store slice-1-question-xyz: %v", err)
	}

	approved, err := db.IsPlanFullyApproved(database, planID, nil)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved: %v", err)
	}
	if !approved {
		t.Error("expected true: question sections (slice-N-question-*) must be ignored")
	}
}

// TestIsPlanFullyApproved_BlocksOnUnapprovedUISection verifies that an unapproved
// UI-exposed section (e.g., slice-2 with value='false') blocks approval.
func TestIsPlanFullyApproved_BlocksOnUnapprovedUISection(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	// Store some approved sections.
	if err := db.StorePlanFeedback(database, planID, "design", "approve", "true", ""); err != nil {
		t.Fatalf("store design: %v", err)
	}
	if err := db.StorePlanFeedback(database, planID, "slice-1", "approve", "true", ""); err != nil {
		t.Fatalf("store slice-1: %v", err)
	}

	// Store an unapproved UI-exposed section.
	if err := db.StorePlanFeedback(database, planID, "slice-2", "approve", "false", ""); err != nil {
		t.Fatalf("store slice-2: %v", err)
	}

	approved, err := db.IsPlanFullyApproved(database, planID, nil)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved: %v", err)
	}
	if approved {
		t.Error("expected false: unapproved UI-exposed section (slice-2) must block approval")
	}
}

// TestIsPlanFullyApproved_LegacyCritiqueSectionIgnored verifies that a legacy
// "critique" section (a non-UI-exposed section from older workflow) is ignored
// by IsPlanFullyApproved. A plan with all UI sections approved plus a disapproved
// critique section should still return true.
func TestIsPlanFullyApproved_LegacyCritiqueSectionIgnored(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	// Store UI-exposed sections, all approved.
	if err := db.StorePlanFeedback(database, planID, "design", "approve", "true", ""); err != nil {
		t.Fatalf("store design: %v", err)
	}
	if err := db.StorePlanFeedback(database, planID, "slice-1", "approve", "true", ""); err != nil {
		t.Fatalf("store slice-1: %v", err)
	}

	// Store a legacy critique section, disapproved.
	// This should NOT block approval, as critique is not UI-exposed.
	if err := db.StorePlanFeedback(database, planID, "critique", "approve", "false", ""); err != nil {
		t.Fatalf("store critique: %v", err)
	}

	approved, err := db.IsPlanFullyApproved(database, planID, nil)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved: %v", err)
	}
	if !approved {
		t.Error("expected true: legacy critique section must be ignored")
	}
}

// ---- Canonical-first approval tests (bug-eca8141d) ---------------------------

// TestIsPlanFullyApproved_CanonicalFallback_FreshDB is the exact user-facing
// scenario: fresh DB (no plan_feedback rows) + YAML slices all marked approved.
// IsPlanFullyApproved must return true using canonical YAML state, not false.
func TestIsPlanFullyApproved_CanonicalFallback_FreshDB(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	// No plan_feedback rows — simulates a cache rebuild / first-run.
	yamlSlices := []db.PlanSliceApproval{
		{Num: 1, ApprovalStatus: "approved"},
		{Num: 2, ApprovalStatus: "approved"},
		{Num: 3, Approved: true}, // legacy bool field written by finalize-yaml
	}

	approved, err := db.IsPlanFullyApproved(database, planID, yamlSlices)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved: %v", err)
	}
	if !approved {
		t.Error("expected true: fresh DB + all YAML slices approved should use canonical fallback")
	}
}

// TestIsPlanFullyApproved_CanonicalFallback_PendingSliceBlocks verifies that a
// slice with no ApprovalStatus (pending) blocks finalize even in canonical mode.
func TestIsPlanFullyApproved_CanonicalFallback_PendingSliceBlocks(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	// No plan_feedback rows.
	yamlSlices := []db.PlanSliceApproval{
		{Num: 1, ApprovalStatus: "approved"},
		{Num: 2, ApprovalStatus: ""}, // pending / unset
		{Num: 3, ApprovalStatus: "rejected"},
	}

	approved, err := db.IsPlanFullyApproved(database, planID, yamlSlices)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved: %v", err)
	}
	if approved {
		t.Error("expected false: pending and rejected slices in YAML must block finalize")
	}
}

// TestIsPlanFullyApproved_DBRowsWinOverYAML verifies that when plan_feedback
// rows exist, they take precedence over YAML fields (existing behavior preserved).
func TestIsPlanFullyApproved_DBRowsWinOverYAML(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	// Write a DB row that says slice-1 is NOT approved.
	if err := db.StorePlanFeedback(database, planID, "slice-1", "approve", "false", ""); err != nil {
		t.Fatalf("store slice-1 false: %v", err)
	}
	if err := db.StorePlanFeedback(database, planID, "slice-2", "approve", "true", ""); err != nil {
		t.Fatalf("store slice-2 true: %v", err)
	}

	// YAML says slice-1 is approved — DB row must win.
	yamlSlices := []db.PlanSliceApproval{
		{Num: 1, ApprovalStatus: "approved"},
		{Num: 2, ApprovalStatus: "approved"},
	}

	approved, err := db.IsPlanFullyApproved(database, planID, yamlSlices)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved: %v", err)
	}
	if approved {
		t.Error("expected false: DB row (slice-1 rejected) must override YAML approval state")
	}
}

// TestIsPlanFullyApproved_MergeSliceWithoutRowBlocks is the bug-fddf5820
// finding-5 regression: when one slice has an approved DB row but another YAML
// slice has NO DB row and is unapproved, the plan must NOT report fully
// approved. The old code iterated only the DB rows and never saw the
// unapproved slice, returning true incorrectly.
func TestIsPlanFullyApproved_MergeSliceWithoutRowBlocks(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	// Only slice-1 has a DB row, and it is approved.
	if err := db.StorePlanFeedback(database, planID, "slice-1", "approve", "true", ""); err != nil {
		t.Fatalf("store slice-1 true: %v", err)
	}

	// YAML carries both slices: slice-2 has no DB row and is pending.
	yamlSlices := []db.PlanSliceApproval{
		{Num: 1, ApprovalStatus: "approved"},
		{Num: 2, ApprovalStatus: ""}, // pending — no DB row, must block
	}

	approved, err := db.IsPlanFullyApproved(database, planID, yamlSlices)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved: %v", err)
	}
	if approved {
		t.Error("expected false: an unapproved YAML slice with no DB row must block finalize (per-slice merge)")
	}

	// Now approve slice-2 in YAML (no DB row) — merge should accept it.
	yamlSlices[1].ApprovalStatus = "approved"
	approved, err = db.IsPlanFullyApproved(database, planID, yamlSlices)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved (after YAML approve): %v", err)
	}
	if !approved {
		t.Error("expected true: slice-1 DB-approved + slice-2 YAML-approved should finalize")
	}
}

// TestIsPlanFullyApproved_NilYAMLPreservesSlicesOnly verifies that with nil
// yamlSlices the DB-only slices-only semantics are preserved (bug-9f753d25):
// every slice WITH a row must be approved, and slices without a row are not
// consulted.
func TestIsPlanFullyApproved_NilYAMLPreservesSlicesOnly(t *testing.T) {
	database, planID := setupPlanDB(t)
	defer database.Close()

	if err := db.StorePlanFeedback(database, planID, "slice-1", "approve", "true", ""); err != nil {
		t.Fatalf("store slice-1 true: %v", err)
	}

	approved, err := db.IsPlanFullyApproved(database, planID, nil)
	if err != nil {
		t.Fatalf("IsPlanFullyApproved: %v", err)
	}
	if !approved {
		t.Error("expected true: with nil YAML, the only DB slice row is approved")
	}
}
