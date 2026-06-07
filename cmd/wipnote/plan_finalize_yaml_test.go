package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/plan/planyaml"
	"github.com/shakestzd/wipnote/core/workitem"
)

func TestApplyAmendments(t *testing.T) {
	plan := &planyaml.PlanYAML{
		Slices: []planyaml.PlanSlice{
			{Num: 1, Title: "Original", DoneWhen: []string{"existing"}, Files: []string{"a.go"}, Effort: "S", Risk: "Low"},
			{Num: 2, Title: "Slice 2", DoneWhen: []string{"criterion"}, Effort: "M"},
		},
	}

	amendments := []planAmendment{
		{SliceNum: 1, Field: "done_when", Operation: "add", Content: "new criterion"},
		{SliceNum: 1, Field: "title", Operation: "set", Content: "Updated Title"},
		{SliceNum: 1, Field: "files", Operation: "add", Content: "b.go"},
		{SliceNum: 2, Field: "effort", Operation: "set", Content: "L"},
		{SliceNum: 2, Field: "done_when", Operation: "remove", Content: "criterion"},
		{SliceNum: 99, Field: "title", Operation: "set", Content: "Missing"}, // should be skipped
	}

	applyAmendments(plan, amendments)

	// Verify slice 1
	if plan.Slices[0].Title != "Updated Title" {
		t.Errorf("slice 1 title: got %q, want %q", plan.Slices[0].Title, "Updated Title")
	}
	if len(plan.Slices[0].DoneWhen) != 2 {
		t.Errorf("slice 1 done_when len: got %d, want 2", len(plan.Slices[0].DoneWhen))
	} else if plan.Slices[0].DoneWhen[1] != "new criterion" {
		t.Errorf("slice 1 done_when[1]: got %q, want %q", plan.Slices[0].DoneWhen[1], "new criterion")
	}
	if len(plan.Slices[0].Files) != 2 {
		t.Errorf("slice 1 files len: got %d, want 2", len(plan.Slices[0].Files))
	} else if plan.Slices[0].Files[1] != "b.go" {
		t.Errorf("slice 1 files[1]: got %q, want %q", plan.Slices[0].Files[1], "b.go")
	}

	// Verify slice 2
	if plan.Slices[1].Effort != "L" {
		t.Errorf("slice 2 effort: got %q, want %q", plan.Slices[1].Effort, "L")
	}
	if len(plan.Slices[1].DoneWhen) != 0 {
		t.Errorf("slice 2 done_when len: got %d, want 0 (criterion removed)", len(plan.Slices[1].DoneWhen))
	}
}

func TestApplyAmendments_SetScalars(t *testing.T) {
	plan := &planyaml.PlanYAML{
		Slices: []planyaml.PlanSlice{
			{Num: 3, Title: "Old", What: "old what", Why: "old why", Risk: "Low"},
		},
	}

	amendments := []planAmendment{
		{SliceNum: 3, Field: "what", Operation: "set", Content: "new what"},
		{SliceNum: 3, Field: "why", Operation: "set", Content: "new why"},
		{SliceNum: 3, Field: "risk", Operation: "set", Content: "High"},
	}

	applyAmendments(plan, amendments)

	if plan.Slices[0].What != "new what" {
		t.Errorf("what: got %q, want %q", plan.Slices[0].What, "new what")
	}
	if plan.Slices[0].Why != "new why" {
		t.Errorf("why: got %q, want %q", plan.Slices[0].Why, "new why")
	}
	if plan.Slices[0].Risk != "High" {
		t.Errorf("risk: got %q, want %q", plan.Slices[0].Risk, "High")
	}
}

func TestRemoveStr(t *testing.T) {
	result := removeStr([]string{"a", "b", "c"}, "b")
	if len(result) != 2 || result[0] != "a" || result[1] != "c" {
		t.Errorf("removeStr: got %v, want [a c]", result)
	}

	// Remove non-existent element — slice unchanged
	result2 := removeStr([]string{"x"}, "y")
	if len(result2) != 1 || result2[0] != "x" {
		t.Errorf("removeStr non-existent: got %v, want [x]", result2)
	}

	// Remove from empty slice
	result3 := removeStr([]string{}, "z")
	if len(result3) != 0 {
		t.Errorf("removeStr empty: got %v, want []", result3)
	}

	// Remove duplicate occurrences
	result4 := removeStr([]string{"a", "b", "a"}, "a")
	if len(result4) != 1 || result4[0] != "b" {
		t.Errorf("removeStr duplicates: got %v, want [b]", result4)
	}
}

func TestBuildFeatureContent_NoQuestions(t *testing.T) {
	content := buildFeatureContent("base what", nil, nil)
	if content != "base what" {
		t.Errorf("no questions: got %q, want %q", content, "base what")
	}
}

func TestBuildFeatureContent_WithAnswers(t *testing.T) {
	questions := []planyaml.PlanQuestion{
		{
			ID:          "q1",
			Text:        "Caching strategy",
			Recommended: "lazy",
			Options: []planyaml.QuestionOption{
				{Key: "lazy", Label: "Lazy loading"},
				{Key: "eager", Label: "Eager loading"},
			},
		},
		{
			ID:          "q2",
			Text:        "Error handling",
			Recommended: "structured-log",
			Options: []planyaml.QuestionOption{
				{Key: "structured-log", Label: "Structured log"},
				{Key: "metric-counter", Label: "Metric counter"},
			},
		},
	}
	// Human answered q2 with metric-counter; q1 falls back to recommended.
	answers := map[string]string{"q2": "metric-counter"}

	content := buildFeatureContent("do the thing", questions, answers)

	if !strings.Contains(content, "## Accepted Design Decisions") {
		t.Error("expected Accepted Design Decisions section")
	}
	if !strings.Contains(content, "Lazy loading") {
		t.Errorf("expected q1 fallback label 'Lazy loading' in %q", content)
	}
	if !strings.Contains(content, "Metric counter") {
		t.Errorf("expected q2 answer label 'Metric counter' in %q", content)
	}
	if !strings.Contains(content, "do the thing") {
		t.Error("expected base 'what' text to be preserved")
	}
}

func TestBuildFeatureContent_FallbackToRecommended(t *testing.T) {
	questions := []planyaml.PlanQuestion{
		{
			ID:          "q1",
			Text:        "Storage backend",
			Recommended: "sqlite",
			Options: []planyaml.QuestionOption{
				{Key: "sqlite", Label: "SQLite"},
				{Key: "postgres", Label: "PostgreSQL"},
			},
		},
	}
	// No answers provided — should fall back to recommended.
	content := buildFeatureContent("store data", questions, map[string]string{})

	if !strings.Contains(content, "SQLite") {
		t.Errorf("expected fallback label 'SQLite' in %q", content)
	}
	if !strings.Contains(content, "unanswered") {
		t.Errorf("expected 'unanswered' marker for fallback in %q", content)
	}
}

func TestBuildFeatureContent_SkipsQuestionsWithNoAnswer(t *testing.T) {
	// Question with no recommended and no answer should be skipped.
	questions := []planyaml.PlanQuestion{
		{ID: "q1", Text: "Empty question", Options: []planyaml.QuestionOption{
			{Key: "a", Label: "Option A"},
		}},
	}
	content := buildFeatureContent("base", questions, map[string]string{})
	if strings.Contains(content, "Accepted Design Decisions") {
		t.Error("should not emit decisions section when no question has an answer or recommended")
	}
}

// ---------------------------------------------------------------------------
// Regression tests for bug-3e524d0f: finalize-yaml must read plan_feedback
// approvals and reconcile them into the YAML before creating track+features.
// ---------------------------------------------------------------------------

// TestFinalizeYAML_ReconcilesFeedbackApprovals is the primary regression test for
// bug-3e524d0f. It verifies that finalizeYAMLWithDB reads plan_feedback, flips
// per-slice approved flags, creates the track+features, and is idempotent.
func TestFinalizeYAML_ReconcilesFeedbackApprovals(t *testing.T) {
	const numSlices = 3

	dir := t.TempDir()
	for _, sub := range []string{"plans", "features", "tracks"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	pID := workitem.GenerateID("plan", "finalize-yaml regression test")
	plan := planyaml.NewPlan(pID, "Finalize YAML Regression", "test description")
	plan.Meta.Status = "active"
	for i := 1; i <= numSlices; i++ {
		plan.Slices = append(plan.Slices, planyaml.PlanSlice{
			ID:       workitem.GenerateID("slice", fmt.Sprintf("slice-%d", i)),
			Num:      i,
			Title:    fmt.Sprintf("Slice %d Title", i),
			What:     fmt.Sprintf("What for slice %d", i),
			Approved: false, // explicitly false in YAML — must be flipped
		})
	}
	planPath := filepath.Join(dir, "plans", pID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save plan yaml: %v", err)
	}
	htmlStub := []byte("<html><body></body></html>")
	if err := os.WriteFile(filepath.Join(dir, "plans", pID+".html"), htmlStub, 0o644); err != nil {
		t.Fatalf("write html stub: %v", err)
	}

	// Seed approve rows in plan_feedback for all slices.
	sqlDB, err := openPlanDB(dir)
	if err != nil {
		t.Fatalf("openPlanDB: %v", err)
	}
	defer sqlDB.Close()
	for i := 1; i <= numSlices; i++ {
		section := fmt.Sprintf("slice-%d", i)
		if err := dbpkg.StorePlanFeedback(sqlDB, pID, section, "approve", "true", ""); err != nil {
			t.Fatalf("seed approval slice-%d: %v", i, err)
		}
	}
	// Also seed an amendment to verify amendment integration (set slice-1 title via amendment).
	amendJSON := `{"slice_num":1,"field":"what","operation":"set","content":"Amended what for slice 1"}`
	if err := dbpkg.StorePlanFeedback(sqlDB, pID, "amendment", "accepted", amendJSON, ""); err != nil {
		t.Fatalf("seed amendment: %v", err)
	}

	// --- First run ---
	createdIDs, failures, err := finalizeYAMLWithDB(sqlDB, dir, pID)
	if err != nil {
		t.Fatalf("finalizeYAMLWithDB first run: %v", err)
	}
	if len(failures) > 0 {
		t.Errorf("unexpected failures: %v", failures)
	}

	// (a) Correct number of features created.
	if len(createdIDs) != numSlices {
		t.Errorf("created features = %d, want %d", len(createdIDs), numSlices)
	}
	for _, id := range createdIDs {
		if !strings.HasPrefix(id, "feat-") {
			t.Errorf("feature id %q has wrong prefix", id)
		}
	}

	// (b) Reconciled YAML has approved:true for all slices.
	reloaded, err := planyaml.Load(planPath)
	if err != nil {
		t.Fatalf("reload plan yaml: %v", err)
	}
	if reloaded.Meta.Status != "finalized" {
		t.Errorf("plan status = %q, want finalized", reloaded.Meta.Status)
	}
	for _, s := range reloaded.Slices {
		if !s.Approved {
			t.Errorf("slice %d: approved = false in YAML after finalize, want true", s.Num)
		}
	}

	// (c) Amendment was applied: slice 1 what should be the amended value.
	if reloaded.Slices[0].What != "Amended what for slice 1" {
		t.Errorf("slice 1 what = %q, want %q", reloaded.Slices[0].What, "Amended what for slice 1")
	}

	// (d) Track was created and linked.
	if reloaded.Meta.TrackID == "" {
		t.Error("plan meta.track_id is empty after finalize")
	}

	// (e) Features exist in project.
	p, err := workitem.Open(dir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	defer p.Close()
	for _, fid := range createdIDs {
		if _, err := p.Features.Get(fid); err != nil {
			t.Errorf("feature %s not found: %v", fid, err)
		}
	}

	// --- Second run (idempotency) ---
	createdIDs2, failures2, err := finalizeYAMLWithDB(sqlDB, dir, pID)
	if err != nil {
		t.Fatalf("finalizeYAMLWithDB second run: %v", err)
	}
	if len(failures2) > 0 {
		t.Errorf("second run unexpected failures: %v", failures2)
	}
	if len(createdIDs2) != numSlices {
		t.Errorf("second run returned %d feature IDs, want %d (idempotent)", len(createdIDs2), numSlices)
	}
	// Same IDs — no new features created.
	for i, id := range createdIDs {
		if i < len(createdIDs2) && createdIDs2[i] != id {
			t.Errorf("second run feature[%d] = %q, want %q (dup created)", i, createdIDs2[i], id)
		}
	}
}

// TestFinalizeYAML_PreMarkedFinalized reproduces the exact live failure from
// bug-3e524d0f: when a plan is ALREADY marked status:finalized in the YAML
// (e.g. by updatePlanStatus before feature creation succeeded) but no features
// were created, finalize-yaml must fall through and create them anyway.
func TestFinalizeYAML_PreMarkedFinalized(t *testing.T) {
	const numSlices = 2

	dir := t.TempDir()
	for _, sub := range []string{"plans", "features", "tracks"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	pID := workitem.GenerateID("plan", "premarked finalized test")
	plan := planyaml.NewPlan(pID, "Pre-Marked Finalized Plan", "")
	// Simulate the buggy state: status=finalized, approved:false, no features.
	plan.Meta.Status = "finalized"
	for i := 1; i <= numSlices; i++ {
		plan.Slices = append(plan.Slices, planyaml.PlanSlice{
			ID:       workitem.GenerateID("slice", fmt.Sprintf("s%d", i)),
			Num:      i,
			Title:    fmt.Sprintf("Pre-Finalized Slice %d", i),
			What:     fmt.Sprintf("do thing %d", i),
			Approved: false, // bug state: approved not flipped
		})
	}
	planPath := filepath.Join(dir, "plans", pID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save plan yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plans", pID+".html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}

	sqlDB, err := openPlanDB(dir)
	if err != nil {
		t.Fatalf("openPlanDB: %v", err)
	}
	defer sqlDB.Close()

	// Seed approvals in plan_feedback — this is what the dashboard writes.
	for i := 1; i <= numSlices; i++ {
		if err := dbpkg.StorePlanFeedback(sqlDB, pID, fmt.Sprintf("slice-%d", i), "approve", "true", ""); err != nil {
			t.Fatalf("seed approval: %v", err)
		}
	}

	// Must NOT short-circuit — must create features even though YAML says "finalized".
	createdIDs, failures, err := finalizeYAMLWithDB(sqlDB, dir, pID)
	if err != nil {
		t.Fatalf("finalizeYAMLWithDB on pre-finalized plan: %v", err)
	}
	if len(failures) > 0 {
		t.Errorf("unexpected failures: %v", failures)
	}
	if len(createdIDs) != numSlices {
		t.Errorf("created %d features, want %d — finalize-yaml short-circuited incorrectly", len(createdIDs), numSlices)
	}

	// YAML must now reflect approved:true for all slices.
	reloaded, err := planyaml.Load(planPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, s := range reloaded.Slices {
		if !s.Approved {
			t.Errorf("slice %d: approved still false after recovery finalize", s.Num)
		}
	}

	// Idempotent: second run returns same IDs, no duplicates.
	createdIDs2, _, err := finalizeYAMLWithDB(sqlDB, dir, pID)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(createdIDs2) != numSlices {
		t.Errorf("second run returned %d, want %d", len(createdIDs2), numSlices)
	}
}

// TestFinalizeYAMLWithDB_SetsSliceFeatureID verifies that finalizeYAMLWithDB
// writes feature_id and execution_status back to the plan YAML for approved
// slices. This regression test for bug-2dc77f9f ensures promote-slice's
// idempotency guard (slice.FeatureID != "") fires correctly.
func TestFinalizeYAMLWithDB_SetsSliceFeatureID(t *testing.T) {
	const numSlices = 2

	dir := t.TempDir()
	for _, sub := range []string{"plans", "features", "tracks"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	pID := workitem.GenerateID("plan", "feature-id regression test")
	plan := planyaml.NewPlan(pID, "Feature ID Test Plan", "")
	plan.Meta.Status = "active"
	for i := 1; i <= numSlices; i++ {
		plan.Slices = append(plan.Slices, planyaml.PlanSlice{
			ID:       workitem.GenerateID("slice", fmt.Sprintf("slice-%d", i)),
			Num:      i,
			Title:    fmt.Sprintf("Slice %d", i),
			What:     fmt.Sprintf("do thing %d", i),
			Approved: false,
		})
	}
	planPath := filepath.Join(dir, "plans", pID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save plan yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plans", pID+".html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write html stub: %v", err)
	}

	// Seed approvals in plan_feedback.
	sqlDB, err := openPlanDB(dir)
	if err != nil {
		t.Fatalf("openPlanDB: %v", err)
	}
	defer sqlDB.Close()
	for i := 1; i <= numSlices; i++ {
		section := fmt.Sprintf("slice-%d", i)
		if err := dbpkg.StorePlanFeedback(sqlDB, pID, section, "approve", "true", ""); err != nil {
			t.Fatalf("seed approval slice-%d: %v", i, err)
		}
	}

	// Run finalize.
	createdIDs, failures, err := finalizeYAMLWithDB(sqlDB, dir, pID)
	if err != nil {
		t.Fatalf("finalizeYAMLWithDB: %v", err)
	}
	if len(failures) > 0 {
		t.Errorf("unexpected failures: %v", failures)
	}
	if len(createdIDs) != numSlices {
		t.Errorf("created %d features, want %d", len(createdIDs), numSlices)
	}

	// Reload and verify: each slice must have FeatureID and ExecutionStatus set.
	reloaded, err := planyaml.Load(planPath)
	if err != nil {
		t.Fatalf("reload plan yaml: %v", err)
	}

	featureIDSet := make(map[string]bool)
	for _, fid := range createdIDs {
		featureIDSet[fid] = true
	}

	for _, s := range reloaded.Slices {
		if s.FeatureID == "" {
			t.Errorf("slice %d: FeatureID is empty after finalize", s.Num)
		} else if !featureIDSet[s.FeatureID] {
			t.Errorf("slice %d: FeatureID %q not in createdIDs", s.Num, s.FeatureID)
		}
		if s.ExecutionStatus != "promoted" {
			t.Errorf("slice %d: ExecutionStatus = %q, want %q", s.Num, s.ExecutionStatus, "promoted")
		}
	}
}
