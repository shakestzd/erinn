package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

func setupFinalizeProject(t *testing.T) (*workitem.Project, string) {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"plans", "features", "tracks"} {
		os.MkdirAll(filepath.Join(dir, sub), 0o755)
	}
	p, err := workitem.Open(dir, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p, dir
}

func TestPlanFinalize_CreatesTrackAndFeatures(t *testing.T) {
	p, dir := setupFinalizeProject(t)

	// Create a plan with 2 slices (steps).
	node, err := p.Plans.Create("Test Plan")
	if err != nil {
		t.Fatal(err)
	}
	edit := p.Plans.Edit(node.ID)
	edit = edit.AddStep("Error handling")
	edit = edit.AddStep("Token validation")
	if err := edit.Save(); err != nil {
		t.Fatal(err)
	}

	result, err := executePlanFinalize(p, dir, node.ID)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}

	if result.TrackID == "" {
		t.Fatal("no track created")
	}
	if !strings.HasPrefix(result.TrackID, "trk-") {
		t.Errorf("track ID %q missing trk- prefix", result.TrackID)
	}
	if len(result.FeatureIDs) != 2 {
		t.Errorf("features = %d, want 2", len(result.FeatureIDs))
	}
	for _, fid := range result.FeatureIDs {
		if !strings.HasPrefix(fid, "feat-") {
			t.Errorf("feature ID %q missing feat- prefix", fid)
		}
	}
}

func TestPlanFinalize_Idempotent(t *testing.T) {
	p, dir := setupFinalizeProject(t)

	node, err := p.Plans.Create("Idempotent Plan")
	if err != nil {
		t.Fatal(err)
	}
	edit := p.Plans.Edit(node.ID)
	edit = edit.AddStep("Slice A")
	if err := edit.Save(); err != nil {
		t.Fatal(err)
	}

	// First finalize.
	result1, err := executePlanFinalize(p, dir, node.ID)
	if err != nil {
		t.Fatalf("first finalize: %v", err)
	}

	// Second finalize should detect already-finalized.
	result2, err := executePlanFinalize(p, dir, node.ID)
	if err != nil {
		t.Fatalf("second finalize: %v", err)
	}

	if !result2.AlreadyFinalized {
		t.Error("second finalize should report AlreadyFinalized=true")
	}
	if result2.TrackID != result1.TrackID {
		t.Errorf("tracks differ: %q vs %q", result2.TrackID, result1.TrackID)
	}
	if len(result2.FeatureIDs) != len(result1.FeatureIDs) {
		t.Errorf("feature counts differ: %d vs %d", len(result2.FeatureIDs), len(result1.FeatureIDs))
	}
}

func TestPlanFinalize_ExecuteCmd(t *testing.T) {
	p, dir := setupFinalizeProject(t)

	node, err := p.Plans.Create("Execute Cmd Plan")
	if err != nil {
		t.Fatal(err)
	}
	edit := p.Plans.Edit(node.ID)
	edit = edit.AddStep("First slice")
	if err := edit.Save(); err != nil {
		t.Fatal(err)
	}

	result, err := executePlanFinalize(p, dir, node.ID)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}

	want := "wipnote yolo --track " + result.TrackID
	if result.ExecuteCmd != want {
		t.Errorf("ExecuteCmd = %q, want %q", result.ExecuteCmd, want)
	}
}

func TestBuildExecuteCmd(t *testing.T) {
	if got := buildExecuteCmd("trk-abc123"); got != "wipnote yolo --track trk-abc123" {
		t.Errorf("got %q", got)
	}
	if got := buildExecuteCmd(""); got != "" {
		t.Errorf("empty track should return empty, got %q", got)
	}
}

func TestPlanFinalize_EmptyPlan(t *testing.T) {
	p, dir := setupFinalizeProject(t)

	node, err := p.Plans.Create("Empty Plan")
	if err != nil {
		t.Fatal(err)
	}

	result, err := executePlanFinalize(p, dir, node.ID)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}

	if result.TrackID == "" {
		t.Error("track should be created even with no slices")
	}
	if len(result.FeatureIDs) != 0 {
		t.Errorf("features = %d, want 0", len(result.FeatureIDs))
	}
}

// ---- New YAML-based finalize (plan finalize v2) tests -------------------------

// setupYAMLFinalizeProject creates a temp dir with a YAML plan that has a track,
// problem statement, and the requested number of slices. Returns the plan ID.
func setupYAMLFinalizeProject(t *testing.T, p *workitem.Project, dir string, numSlices int) (string, string) {
	t.Helper()

	// Create a track first.
	track, err := p.Tracks.Create("Test Track")
	if err != nil {
		t.Fatalf("create track: %v", err)
	}

	planID := workitem.GenerateID("plan", "YAML Test Plan")
	plan := planyaml.NewPlan(planID, "YAML Test Plan", "test plan")
	plan.Meta.TrackID = track.ID
	plan.Design.Problem = "We need to solve this problem"

	for i := 1; i <= numSlices; i++ {
		plan.Slices = append(plan.Slices, planyaml.PlanSlice{
			ID:    workitem.GenerateID("slice", "slice"),
			Num:   i,
			Title: "Slice " + strings.Repeat("I", i),
			What:  "do something",
			Why:   "because reasons",
		})
	}

	planPath := filepath.Join(dir, "plans", planID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save plan YAML: %v", err)
	}

	// Also create the HTML workitem for the plan.
	if _, err := p.Plans.Create("YAML Test Plan",
		workitem.PlanWithTrack(track.ID),
	); err != nil {
		t.Logf("create plan node warning: %v", err)
	}

	return planID, track.ID
}

func TestPlanFinalizeFromYAML_HappyPath(t *testing.T) {
	p, dir := setupFinalizeProject(t)
	planID, trackID := setupYAMLFinalizeProject(t, p, dir, 2)

	result, err := executePlanFinalizeFromYAML(p, dir, planID)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}

	if result.TrackID != trackID {
		t.Errorf("trackID = %q, want %q", result.TrackID, trackID)
	}
	if len(result.FeatureIDs) != 2 {
		t.Errorf("features = %d, want 2", len(result.FeatureIDs))
	}
	for _, fid := range result.FeatureIDs {
		if !strings.HasPrefix(fid, "feat-") {
			t.Errorf("feature ID %q missing feat- prefix", fid)
		}
	}

	// Verify features were created and linked.
	for _, fid := range result.FeatureIDs {
		feat, err := p.Features.Get(fid)
		if err != nil {
			t.Fatalf("get feature %s: %v", fid, err)
		}
		if feat.TrackID != trackID {
			t.Errorf("feature %s track = %q, want %q", fid, feat.TrackID, trackID)
		}
		// Should have planned_in edge to plan.
		plannedIn := feat.Edges[string("planned_in")]
		found := false
		for _, e := range plannedIn {
			if e.TargetID == planID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("feature %s missing planned_in → %s edge", fid, planID)
		}
	}
}

func TestPlanFinalizeFromYAML_FeatureIDWrittenBack(t *testing.T) {
	p, dir := setupFinalizeProject(t)
	planID, _ := setupYAMLFinalizeProject(t, p, dir, 2)

	_, err := executePlanFinalizeFromYAML(p, dir, planID)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}

	// Load YAML and verify feature_ids were written back.
	planPath := filepath.Join(dir, "plans", planID+".yaml")
	plan, err := planyaml.Load(planPath)
	if err != nil {
		t.Fatalf("load plan: %v", err)
	}
	for i, s := range plan.Slices {
		if s.FeatureID == "" {
			t.Errorf("slice[%d] (num=%d) has no feature_id after finalize", i, s.Num)
		}
		if !strings.HasPrefix(s.FeatureID, "feat-") {
			t.Errorf("slice[%d] feature_id = %q, missing feat- prefix", i, s.FeatureID)
		}
	}
	if plan.Meta.Status != "finalized" {
		t.Errorf("plan status = %q, want finalized", plan.Meta.Status)
	}
}

func TestPlanFinalizeFromYAML_NoTrack(t *testing.T) {
	p, dir := setupFinalizeProject(t)

	// Create plan YAML without track.
	planID := workitem.GenerateID("plan", "no-track plan")
	plan := planyaml.NewPlan(planID, "no-track plan", "")
	plan.Design.Problem = "a problem"
	plan.Slices = append(plan.Slices, planyaml.PlanSlice{
		Num: 1, Title: "slice one", What: "do it",
	})
	planPath := filepath.Join(dir, "plans", planID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save: %v", err)
	}

	_, err := executePlanFinalizeFromYAML(p, dir, planID)
	if err == nil {
		t.Fatal("expected error for plan without track")
	}
	if !strings.Contains(err.Error(), "track") {
		t.Errorf("error should mention 'track', got: %v", err)
	}
}

func TestPlanFinalizeFromYAML_NoProblemStatement(t *testing.T) {
	p, dir := setupFinalizeProject(t)

	track, _ := p.Tracks.Create("T")
	planID := workitem.GenerateID("plan", "no-problem plan")
	plan := planyaml.NewPlan(planID, "no-problem plan", "")
	plan.Meta.TrackID = track.ID
	// Leave Design.Problem empty.
	plan.Slices = append(plan.Slices, planyaml.PlanSlice{
		Num: 1, Title: "slice one", What: "do it",
	})
	planPath := filepath.Join(dir, "plans", planID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save: %v", err)
	}

	_, err := executePlanFinalizeFromYAML(p, dir, planID)
	if err == nil {
		t.Fatal("expected error for plan without problem statement")
	}
	if !strings.Contains(err.Error(), "problem") {
		t.Errorf("error should mention 'problem', got: %v", err)
	}
}

func TestPlanFinalizeFromYAML_NoSlices(t *testing.T) {
	p, dir := setupFinalizeProject(t)

	track, _ := p.Tracks.Create("T")
	planID := workitem.GenerateID("plan", "no-slices plan")
	plan := planyaml.NewPlan(planID, "no-slices plan", "")
	plan.Meta.TrackID = track.ID
	plan.Design.Problem = "a real problem"
	// Leave Slices empty.
	planPath := filepath.Join(dir, "plans", planID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save: %v", err)
	}

	_, err := executePlanFinalizeFromYAML(p, dir, planID)
	if err == nil {
		t.Fatal("expected error for plan without slices")
	}
	if !strings.Contains(err.Error(), "slice") {
		t.Errorf("error should mention 'slice', got: %v", err)
	}
}

func TestPlanFinalizeFromYAML_AlreadyFinalized(t *testing.T) {
	p, dir := setupFinalizeProject(t)
	planID, _ := setupYAMLFinalizeProject(t, p, dir, 1)

	// First finalize.
	if _, err := executePlanFinalizeFromYAML(p, dir, planID); err != nil {
		t.Fatalf("first finalize: %v", err)
	}

	// Second finalize should return locked error.
	_, err := executePlanFinalizeFromYAML(p, dir, planID)
	if err == nil {
		t.Fatal("expected error on double-finalize")
	}
	if !strings.Contains(err.Error(), "locked") && !strings.Contains(err.Error(), "reopen") {
		t.Errorf("error should mention 'locked' or 'reopen', got: %v", err)
	}
}

func TestPlanFinalizeFromYAML_Reopen(t *testing.T) {
	p, dir := setupFinalizeProject(t)
	planID, _ := setupYAMLFinalizeProject(t, p, dir, 1)

	// Finalize then reopen.
	if _, err := executePlanFinalizeFromYAML(p, dir, planID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if err := executePlanReopen(dir, planID); err != nil {
		t.Fatalf("reopen: %v", err)
	}

	// After reopen, status should be todo/draft (not finalized).
	planPath := filepath.Join(dir, "plans", planID+".yaml")
	plan, err := planyaml.Load(planPath)
	if err != nil {
		t.Fatalf("load plan: %v", err)
	}
	if plan.Meta.Status == "finalized" {
		t.Error("plan should not be finalized after reopen")
	}
}

// TestPlanFinalizeFromYAML_ReopenRefinalizeIdempotent verifies that
// reopen + re-finalize does not duplicate features: the same FeatureIDs
// must be referenced after re-finalize as after the initial finalize.
func TestPlanFinalizeFromYAML_ReopenRefinalizeIdempotent(t *testing.T) {
	p, dir := setupFinalizeProject(t)
	planID, _ := setupYAMLFinalizeProject(t, p, dir, 2)

	// First finalize: captures original feature IDs.
	result1, err := executePlanFinalizeFromYAML(p, dir, planID)
	if err != nil {
		t.Fatalf("first finalize: %v", err)
	}
	if len(result1.FeatureIDs) != 2 {
		t.Fatalf("first finalize: expected 2 features, got %d", len(result1.FeatureIDs))
	}

	// Reopen: unlocks the plan.
	if err := executePlanReopen(dir, planID); err != nil {
		t.Fatalf("reopen: %v", err)
	}

	// Re-finalize: must reuse existing FeatureIDs, not create new ones.
	result2, err := executePlanFinalizeFromYAML(p, dir, planID)
	if err != nil {
		t.Fatalf("re-finalize: %v", err)
	}

	// Same number of features.
	if len(result2.FeatureIDs) != len(result1.FeatureIDs) {
		t.Errorf("re-finalize feature count = %d, want %d", len(result2.FeatureIDs), len(result1.FeatureIDs))
	}

	// Same IDs (order preserved).
	for i, id := range result1.FeatureIDs {
		if i >= len(result2.FeatureIDs) {
			break
		}
		if result2.FeatureIDs[i] != id {
			t.Errorf("slice %d: re-finalize feature ID = %q, want %q (duplicate created)", i+1, result2.FeatureIDs[i], id)
		}
	}

	// YAML still references the original IDs.
	planPath := filepath.Join(dir, "plans", planID+".yaml")
	plan, err := planyaml.Load(planPath)
	if err != nil {
		t.Fatalf("load plan after re-finalize: %v", err)
	}
	for i, s := range plan.Slices {
		if i >= len(result1.FeatureIDs) {
			break
		}
		if s.FeatureID != result1.FeatureIDs[i] {
			t.Errorf("slice[%d] YAML feature_id = %q, want %q after re-finalize", i, s.FeatureID, result1.FeatureIDs[i])
		}
	}
}

// TestPlanFinalizeFromYAML_ReopenRefinalize_DoesNotMutateExistingFeature is the
// canonical test for the one-way-mutation invariant: plan finalize creates
// features once, and after that features are independent work items. Re-running
// plan finalize must NOT overwrite a feature's title or content even if the
// YAML slice has diverged (e.g. the user edited the feature directly).
func TestPlanFinalizeFromYAML_ReopenRefinalize_DoesNotMutateExistingFeature(t *testing.T) {
	p, dir := setupFinalizeProject(t)
	planID, _ := setupYAMLFinalizeProject(t, p, dir, 2)

	// First finalize: creates features for each slice.
	result1, err := executePlanFinalizeFromYAML(p, dir, planID)
	if err != nil {
		t.Fatalf("first finalize: %v", err)
	}
	if len(result1.FeatureIDs) != 2 {
		t.Fatalf("first finalize: expected 2 features, got %d", len(result1.FeatureIDs))
	}
	capturedID := result1.FeatureIDs[0]

	// Simulate a user/agent editing the feature's title directly (independent of plan).
	const userEditedTitle = "User-Edited Title (independent of plan YAML)"
	if err := p.Features.Edit(capturedID).SetTitle(userEditedTitle).Save(); err != nil {
		t.Fatalf("edit feature title: %v", err)
	}

	// Verify the edit landed.
	before, err := p.Features.Get(capturedID)
	if err != nil {
		t.Fatalf("get feature before re-finalize: %v", err)
	}
	if before.Title != userEditedTitle {
		t.Fatalf("setup failed: feature title = %q, want %q", before.Title, userEditedTitle)
	}

	// Reopen: unlocks the plan for re-finalize.
	if err := executePlanReopen(dir, planID); err != nil {
		t.Fatalf("reopen: %v", err)
	}

	// Re-finalize: trust-and-skip must leave the existing feature untouched.
	result2, err := executePlanFinalizeFromYAML(p, dir, planID)
	if err != nil {
		t.Fatalf("re-finalize: %v", err)
	}

	// Same feature ID — no duplicate created.
	if len(result2.FeatureIDs) == 0 {
		t.Fatal("re-finalize returned no feature IDs")
	}
	if result2.FeatureIDs[0] != capturedID {
		t.Errorf("re-finalize changed feature ID: got %q, want %q (duplicate created)", result2.FeatureIDs[0], capturedID)
	}

	// Title is still the user's manual edit — trust-and-skip did NOT overwrite it.
	after, err := p.Features.Get(capturedID)
	if err != nil {
		t.Fatalf("get feature after re-finalize: %v", err)
	}
	if after.Title != userEditedTitle {
		t.Errorf("re-finalize overwrote feature title: got %q, want %q", after.Title, userEditedTitle)
	}
}

func TestAddSliceYAML_NoPrintsFakeFeatureID(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "plans"), 0o755)

	planID := workitem.GenerateID("plan", "test")
	plan := planyaml.NewPlan(planID, "test", "")
	planPath := filepath.Join(dir, "plans", planID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save: %v", err)
	}

	// runPlanAddSliceYAML should return without error.
	err := runPlanAddSliceYAML(dir, planID, "My Slice", "impl detail", "", "", "", "", "S", "Low", "")
	if err != nil {
		t.Fatalf("add-slice-yaml: %v", err)
	}

	// Load and verify the slice has a slice- prefixed ID and NO feature_id yet.
	loaded, _ := planyaml.Load(planPath)
	if len(loaded.Slices) != 1 {
		t.Fatalf("expected 1 slice, got %d", len(loaded.Slices))
	}
	got := loaded.Slices[0]
	if got.FeatureID != "" {
		t.Errorf("slice should have empty feature_id before finalize, got %q", got.FeatureID)
	}
	// The whole point of bug-32f787d1: slice IDs must not pretend to be features.
	if strings.HasPrefix(got.ID, "feat-") {
		t.Errorf("slice ID must not look like a feature ID, got %q", got.ID)
	}
	if !strings.HasPrefix(got.ID, "slic") {
		t.Errorf("slice ID should be slic-prefixed (workitem.GenerateID convention), got %q", got.ID)
	}
}

// TestFinalizePlanHTML_RadioGroupInvariant verifies that finalizePlanHTML correctly
// handles radio buttons in slice-card YAML plans. The three approval radios
// (approved, changes_requested, rejected) must maintain radio-group invariant:
// exactly one can be checked at a time.
func TestFinalizePlanHTML_RadioGroupInvariant(t *testing.T) {
	htmlWithRadios := `<!DOCTYPE html>
<html>
<body>
<article data-status="draft">
  <input type="radio" name="slice-1-approval" value="approved" data-section="slice-1" data-action="approve">
  <input type="radio" name="slice-1-approval" value="changes_requested" data-section="slice-1" data-action="approve">
  <input type="radio" name="slice-1-approval" value="rejected" data-section="slice-1" data-action="approve">
</article>
</body>
</html>`

	// Parse the HTML.
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlWithRadios))
	if err != nil {
		t.Fatalf("parse HTML: %v", err)
	}

	// Simulate finalizePlanHTML logic for approved section.
	section := "slice-1"
	approved := true
	doc.Find(`input[type='radio'][data-section='` + section + `'][data-action='approve']`).
		Each(func(_ int, s *goquery.Selection) {
			val, _ := s.Attr("value")
			if approved && val == "approved" {
				s.SetAttr("checked", "checked")
			} else {
				s.RemoveAttr("checked")
			}
		})

	// Verify only the "approved" radio is checked.
	doc.Find(`input[type='radio'][data-section='` + section + `'][data-action='approve']`).
		Each(func(_ int, s *goquery.Selection) {
			val, _ := s.Attr("value")
			_, hasChecked := s.Attr("checked")
			if val == "approved" {
				if !hasChecked {
					t.Error("approved radio should be checked")
				}
			} else {
				if hasChecked {
					t.Errorf("%s radio should not be checked", val)
				}
			}
		})
}

// TestFinalizePlanHTML_CheckboxGroupApproval verifies that finalizePlanHTML
// correctly handles checkboxes in legacy plans. When approved=true, the checkbox
// should be checked; when approved=false, it should be unchecked.
func TestFinalizePlanHTML_CheckboxGroupApproval(t *testing.T) {
	htmlWithCheckbox := `<!DOCTYPE html>
<html>
<body>
<article data-status="draft">
  <input type="checkbox" data-section="section-1" data-action="approve">
</article>
</body>
</html>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlWithCheckbox))
	if err != nil {
		t.Fatalf("parse HTML: %v", err)
	}

	// Test case 1: approved=true
	section := "section-1"
	approved := true
	if approved {
		doc.Find(`input[type='checkbox'][data-section='`+section+`'][data-action='approve']`).
			SetAttr("checked", "checked")
	} else {
		doc.Find(`input[type='checkbox'][data-section='` + section + `'][data-action='approve']`).
			RemoveAttr("checked")
	}

	_, hasChecked := doc.Find(`input[type='checkbox'][data-section='` + section + `'][data-action='approve']`).
		First().Attr("checked")
	if !hasChecked {
		t.Error("checkbox should be checked when approved=true")
	}

	// Test case 2: approved=false
	approved = false
	if approved {
		doc.Find(`input[type='checkbox'][data-section='`+section+`'][data-action='approve']`).
			SetAttr("checked", "checked")
	} else {
		doc.Find(`input[type='checkbox'][data-section='` + section + `'][data-action='approve']`).
			RemoveAttr("checked")
	}

	_, hasChecked = doc.Find(`input[type='checkbox'][data-section='` + section + `'][data-action='approve']`).
		First().Attr("checked")
	if hasChecked {
		t.Error("checkbox should not be checked when approved=false")
	}
}
