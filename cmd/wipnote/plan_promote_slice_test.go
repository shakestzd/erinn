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

func TestPromoteSlice_Approved_CreatesFeature(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"plans", "features", "tracks"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	p, err := workitem.Open(dir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	defer p.Close()

	track, err := p.Tracks.Create("Test Track")
	if err != nil {
		t.Fatalf("create track: %v", err)
	}

	pID := workitem.GenerateID("plan", "promote test")
	plan := planyaml.NewPlan(pID, "Promote Test", "")
	plan.Meta.TrackID = track.ID
	plan.Meta.Status = "active"
	plan.Design.Problem = "test problem"
	plan.Slices = append(plan.Slices, planyaml.PlanSlice{
		ID:    workitem.GenerateID("slice", "slice one"),
		Num:   1,
		Title: "Slice One",
		What:  "Do the thing",
		Why:   "Because",
	})
	planPath := filepath.Join(dir, "plans", pID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save plan yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plans", pID+".html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}

	// Approve slice 1.
	db, err := openPlanDB(dir)
	if err != nil {
		t.Fatalf("openPlanDB: %v", err)
	}
	if err := dbpkg.StorePlanFeedback(db, pID, "slice-1", "approve", "true", ""); err != nil {
		db.Close()
		t.Fatalf("store approval: %v", err)
	}
	db.Close()

	// Promote slice 1.
	featID, err := promoteSliceFromYAML(dir, pID, 1, false, false)
	if err != nil {
		t.Fatalf("promoteSliceFromYAML: %v", err)
	}

	// Assertions.
	if !strings.HasPrefix(featID, "feat-") {
		t.Errorf("featID = %q, want feat- prefix", featID)
	}

	// Feature must exist.
	p2, err := workitem.Open(dir, "test-agent")
	if err != nil {
		t.Fatalf("open p2: %v", err)
	}
	defer p2.Close()

	feat, err := p2.Features.Get(featID)
	if err != nil {
		t.Fatalf("get feature %s: %v", featID, err)
	}

	// Must have part_of edge to track.
	found := false
	for _, edges := range feat.Edges {
		for _, e := range edges {
			if e.TargetID == track.ID {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("feature %s missing edge to track %s", featID, track.ID)
	}

	// Must have planned_in edge to plan.
	plannedInEdges := feat.Edges["planned_in"]
	foundPlan := false
	for _, e := range plannedInEdges {
		if e.TargetID == pID {
			foundPlan = true
		}
	}
	if !foundPlan {
		t.Errorf("feature %s missing planned_in edge to %s", featID, pID)
	}

	// YAML slice must have feature_id written back.
	reloaded, err := planyaml.Load(planPath)
	if err != nil {
		t.Fatalf("reload plan: %v", err)
	}
	if reloaded.Slices[0].FeatureID != featID {
		t.Errorf("slice feature_id = %q, want %q", reloaded.Slices[0].FeatureID, featID)
	}
}

func TestPromoteSlice_NotApproved_Refuses(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"plans", "features", "tracks"} {
		os.MkdirAll(filepath.Join(dir, sub), 0o755)
	}

	p, _ := workitem.Open(dir, "test-agent")
	defer p.Close()
	track, _ := p.Tracks.Create("Track")

	pID := workitem.GenerateID("plan", "refuse test")
	plan := planyaml.NewPlan(pID, "Refuse Test", "")
	plan.Meta.TrackID = track.ID
	plan.Meta.Status = "active"
	plan.Slices = append(plan.Slices, planyaml.PlanSlice{
		ID: workitem.GenerateID("slice", "s"), Num: 1, Title: "S", What: "w",
	})
	planPath := filepath.Join(dir, "plans", pID+".yaml")
	planyaml.Save(planPath, plan)
	os.WriteFile(filepath.Join(dir, "plans", pID+".html"), []byte("<html></html>"), 0o644)

	// No approval row — must refuse.
	_, err := promoteSliceFromYAML(dir, pID, 1, false, false)
	if err == nil {
		t.Fatal("expected error for unapproved slice")
	}
	if !strings.Contains(err.Error(), "approved") && !strings.Contains(err.Error(), "approval") {
		t.Errorf("error should mention approval, got: %v", err)
	}

	// No feature created.
	p2, _ := workitem.Open(dir, "test-agent")
	defer p2.Close()
	reloaded, _ := planyaml.Load(planPath)
	if reloaded.Slices[0].FeatureID != "" {
		t.Errorf("feature_id should be empty when promotion refused, got %q", reloaded.Slices[0].FeatureID)
	}
}

func TestPromoteSlice_BlockedByPendingDeps_Refuses(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"plans", "features", "tracks"} {
		os.MkdirAll(filepath.Join(dir, sub), 0o755)
	}

	p, _ := workitem.Open(dir, "test-agent")
	defer p.Close()
	track, _ := p.Tracks.Create("Track")

	pID := workitem.GenerateID("plan", "deps test")
	plan := planyaml.NewPlan(pID, "Deps Test", "")
	plan.Meta.TrackID = track.ID
	plan.Meta.Status = "active"
	plan.Slices = []planyaml.PlanSlice{
		{ID: workitem.GenerateID("slice", "s1"), Num: 1, Title: "S1", What: "w"},
		{ID: workitem.GenerateID("slice", "s2"), Num: 2, Title: "S2", What: "w", Deps: []int{1}},
	}
	planPath := filepath.Join(dir, "plans", pID+".yaml")
	planyaml.Save(planPath, plan)
	os.WriteFile(filepath.Join(dir, "plans", pID+".html"), []byte("<html></html>"), 0o644)

	// Approve slice 2 but NOT slice 1 (dep is not done).
	db, _ := openPlanDB(dir)
	dbpkg.StorePlanFeedback(db, pID, "slice-2", "approve", "true", "")
	db.Close()

	// Without --waive-deps: must refuse.
	_, err := promoteSliceFromYAML(dir, pID, 2, false, false)
	if err == nil {
		t.Fatal("expected error when dep not done")
	}
	if !strings.Contains(err.Error(), "dep") && !strings.Contains(err.Error(), "blocking") && !strings.Contains(err.Error(), "slice-1") {
		t.Errorf("error should mention blocking dep, got: %v", err)
	}

	// With --waive-deps: must succeed.
	// Also approve slice 2 again (already done) to ensure idempotency of approval store.
	db2, _ := openPlanDB(dir)
	dbpkg.StorePlanFeedback(db2, pID, "slice-2", "approve", "true", "")
	db2.Close()

	featID, err := promoteSliceFromYAML(dir, pID, 2, true, false)
	if err != nil {
		t.Fatalf("waive-deps promote: %v", err)
	}
	if !strings.HasPrefix(featID, "feat-") {
		t.Errorf("featID = %q, want feat- prefix", featID)
	}
}

// TestPromoteSlice_DepDoneViaSetSliceStatus_NoWaiveNeeded is a regression test
// for the action-name mismatch (job-43 HIGH finding): runSetSliceStatus and
// promoteSliceFromYAML must agree on the plan_feedback action key
// ('set_execution_status'), so a dep marked done via the documented CLI path
// unblocks promotion of dependent slices without --waive-deps.
func TestPromoteSlice_DepDoneViaSetSliceStatus_NoWaiveNeeded(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"plans", "features", "tracks"} {
		os.MkdirAll(filepath.Join(dir, sub), 0o755)
	}

	p, _ := workitem.Open(dir, "test-agent")
	defer p.Close()
	track, _ := p.Tracks.Create("Track")

	pID := workitem.GenerateID("plan", "dep done")
	plan := planyaml.NewPlan(pID, "Dep Done", "")
	plan.Meta.TrackID = track.ID
	plan.Meta.Status = "active"
	plan.Slices = []planyaml.PlanSlice{
		{ID: workitem.GenerateID("slice", "s1"), Num: 1, Title: "S1", What: "w"},
		{ID: workitem.GenerateID("slice", "s2"), Num: 2, Title: "S2", What: "w", Deps: []int{1}},
	}
	planPath := filepath.Join(dir, "plans", pID+".yaml")
	planyaml.Save(planPath, plan)
	os.WriteFile(filepath.Join(dir, "plans", pID+".html"), []byte("<html></html>"), 0o644)

	// Approve slice 2 (the one we'll promote) and mark slice 1's execution
	// status as 'done' via the same code path the user docs describe.
	db, _ := openPlanDB(dir)
	dbpkg.StorePlanFeedback(db, pID, "slice-2", "approve", "true", "")
	db.Close()
	if err := runSetSliceStatus(dir, pID, "1", "done"); err != nil {
		t.Fatalf("runSetSliceStatus: %v", err)
	}

	// Without --waive-deps: must succeed because slice 1 is done.
	featID, err := promoteSliceFromYAML(dir, pID, 2, false, false)
	if err != nil {
		t.Fatalf("expected promote to succeed when dep marked done via runSetSliceStatus, got: %v", err)
	}
	if !strings.HasPrefix(featID, "feat-") {
		t.Errorf("featID = %q, want feat- prefix", featID)
	}
}

func TestPromoteSlice_Idempotent(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"plans", "features", "tracks"} {
		os.MkdirAll(filepath.Join(dir, sub), 0o755)
	}

	p, _ := workitem.Open(dir, "test-agent")
	defer p.Close()
	track, _ := p.Tracks.Create("Track")

	pID := workitem.GenerateID("plan", "idempotent test")
	plan := planyaml.NewPlan(pID, "Idempotent Test", "")
	plan.Meta.TrackID = track.ID
	plan.Meta.Status = "active"
	plan.Slices = append(plan.Slices, planyaml.PlanSlice{
		ID: workitem.GenerateID("slice", "s"), Num: 1, Title: "S", What: "w",
	})
	planPath := filepath.Join(dir, "plans", pID+".yaml")
	planyaml.Save(planPath, plan)
	os.WriteFile(filepath.Join(dir, "plans", pID+".html"), []byte("<html></html>"), 0o644)

	db, _ := openPlanDB(dir)
	dbpkg.StorePlanFeedback(db, pID, "slice-1", "approve", "true", "")
	db.Close()

	// First promote.
	featID1, err := promoteSliceFromYAML(dir, pID, 1, false, false)
	if err != nil {
		t.Fatalf("first promote: %v", err)
	}

	// Second promote — must reuse same feature_id, not create duplicate.
	featID2, err := promoteSliceFromYAML(dir, pID, 1, false, false)
	if err != nil {
		t.Fatalf("second promote: %v", err)
	}

	if featID1 != featID2 {
		t.Errorf("idempotent promote changed feature ID: %q → %q", featID1, featID2)
	}

	// Verify only one feature with this ID exists.
	reloaded, _ := planyaml.Load(planPath)
	if reloaded.Slices[0].FeatureID != featID1 {
		t.Errorf("slice feature_id = %q, want %q", reloaded.Slices[0].FeatureID, featID1)
	}
}

func TestPromoteSlice_SetsExecutionStatusPromoted(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"plans", "features", "tracks"} {
		os.MkdirAll(filepath.Join(dir, sub), 0o755)
	}

	p, _ := workitem.Open(dir, "test-agent")
	defer p.Close()
	track, _ := p.Tracks.Create("Track")

	pID := workitem.GenerateID("plan", "exec status test")
	plan := planyaml.NewPlan(pID, "ExecStatus Test", "")
	plan.Meta.TrackID = track.ID
	plan.Meta.Status = "active"
	plan.Slices = append(plan.Slices, planyaml.PlanSlice{
		ID: workitem.GenerateID("slice", "s"), Num: 1, Title: "S", What: "w",
	})
	planPath := filepath.Join(dir, "plans", pID+".yaml")
	planyaml.Save(planPath, plan)
	os.WriteFile(filepath.Join(dir, "plans", pID+".html"), []byte("<html></html>"), 0o644)

	db, _ := openPlanDB(dir)
	dbpkg.StorePlanFeedback(db, pID, "slice-1", "approve", "true", "")
	db.Close()

	if _, err := promoteSliceFromYAML(dir, pID, 1, false, false); err != nil {
		t.Fatalf("promote: %v", err)
	}

	// GetSliceApprovals reflects execution_status='promoted'.
	db2, _ := openPlanDB(dir)
	defer db2.Close()
	statuses, err := getSliceExecutionStatuses(db2, pID)
	if err != nil {
		t.Fatalf("getSliceExecutionStatuses: %v", err)
	}
	if statuses["slice-1"] != "promoted" {
		t.Errorf("execution_status = %q, want 'promoted'", statuses["slice-1"])
	}

	// Also verify YAML execution_status field.
	reloaded, _ := planyaml.Load(planPath)
	if reloaded.Slices[0].ExecutionStatus != "promoted" {
		t.Errorf("YAML execution_status = %q, want 'promoted'", reloaded.Slices[0].ExecutionStatus)
	}
}

func TestPromoteSlice_PlanRemainsActive(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"plans", "features", "tracks"} {
		os.MkdirAll(filepath.Join(dir, sub), 0o755)
	}

	p, _ := workitem.Open(dir, "test-agent")
	defer p.Close()
	track, _ := p.Tracks.Create("Track")

	pID := workitem.GenerateID("plan", "status test")
	plan := planyaml.NewPlan(pID, "Status Test", "")
	plan.Meta.TrackID = track.ID
	plan.Meta.Status = "active"
	plan.Slices = []planyaml.PlanSlice{
		{ID: workitem.GenerateID("slice", "s1"), Num: 1, Title: "S1", What: "w"},
		{ID: workitem.GenerateID("slice", "s2"), Num: 2, Title: "S2", What: "w"},
	}
	planPath := filepath.Join(dir, "plans", pID+".yaml")
	planyaml.Save(planPath, plan)
	os.WriteFile(filepath.Join(dir, "plans", pID+".html"), []byte("<html></html>"), 0o644)

	db, _ := openPlanDB(dir)
	dbpkg.StorePlanFeedback(db, pID, "slice-1", "approve", "true", "")
	db.Close()

	if _, err := promoteSliceFromYAML(dir, pID, 1, false, false); err != nil {
		t.Fatalf("promote: %v", err)
	}

	// Plan meta.status must not change.
	reloaded, _ := planyaml.Load(planPath)
	if reloaded.Meta.Status != "active" {
		t.Errorf("plan status = %q after promote, want 'active' (unchanged)", reloaded.Meta.Status)
	}
}

func TestExecutePreview_IncludesPromotedSliceFeatures(t *testing.T) {
	dir := t.TempDir()
	hgDir := filepath.Join(dir, ".wipnote")
	for _, sub := range []string{"plans", "features", "tracks"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	p, err := workitem.Open(hgDir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	defer p.Close()

	track, err := p.Tracks.Create("Preview Track")
	if err != nil {
		t.Fatalf("create track: %v", err)
	}

	pID := workitem.GenerateID("plan", "preview plan")
	plan := planyaml.NewPlan(pID, "Preview Plan", "")
	plan.Meta.TrackID = track.ID
	plan.Meta.Status = "active"
	plan.Design.Problem = "test problem"
	plan.Slices = append(plan.Slices, planyaml.PlanSlice{
		ID:    workitem.GenerateID("slice", "s"),
		Num:   1,
		Title: "Preview Slice",
		What:  "Do preview thing",
		Why:   "Because",
	})
	planPath := filepath.Join(hgDir, "plans", pID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save plan yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hgDir, "plans", pID+".html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}

	// Approve and promote slice 1.
	db, err := openPlanDB(hgDir)
	if err != nil {
		t.Fatalf("openPlanDB: %v", err)
	}
	if err := dbpkg.StorePlanFeedback(db, pID, "slice-1", "approve", "true", ""); err != nil {
		db.Close()
		t.Fatalf("store approval: %v", err)
	}
	db.Close()

	featID, err := promoteSliceFromYAML(hgDir, pID, 1, false, false)
	if err != nil {
		t.Fatalf("promoteSliceFromYAML: %v", err)
	}

	// execute-preview on the track should include the promoted feature.
	preview, err := buildExecutePreview(hgDir, track.ID)
	if err != nil {
		t.Fatalf("buildExecutePreview: %v", err)
	}

	found := false
	for _, f := range preview.Features {
		if f.ID == featID {
			found = true
			break
		}
	}
	if !found {
		ids := make([]string, len(preview.Features))
		for i, f := range preview.Features {
			ids[i] = f.ID
		}
		t.Errorf("promoted feature %s not found in execute-preview features: %v", featID, ids)
	}
}

// --- Spec-enforcement gate tests (feat-0fd7c8bc) ----------------------

// seedPromoteFixture creates a temp project with a single approved slice and
// returns the wipnote dir + plan ID. The layout mirrors production: a
// project root (t.TempDir()) containing a .wipnote/ subdirectory.
func seedPromoteFixture(t *testing.T, decisionsNotes string) (hgDir, planID string) {
	t.Helper()
	projectRoot := t.TempDir()
	hgDir = filepath.Join(projectRoot, ".wipnote")
	for _, sub := range []string{"plans", "features", "tracks"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	p, err := workitem.Open(hgDir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	track, err := p.Tracks.Create("Gate Track")
	if err != nil {
		p.Close()
		t.Fatalf("create track: %v", err)
	}
	p.Close()

	planID = workitem.GenerateID("plan", "gate test")
	plan := planyaml.NewPlan(planID, "Gate Test", "")
	plan.Meta.TrackID = track.ID
	plan.Meta.Status = "active"
	plan.Design.Problem = "x"
	plan.Slices = append(plan.Slices, planyaml.PlanSlice{
		ID:             workitem.GenerateID("slice", "s"),
		Num:            1,
		Title:          "Slice One",
		What:           "do",
		Why:            "because",
		DecisionsNotes: decisionsNotes,
	})
	planPath := filepath.Join(hgDir, "plans", planID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hgDir, "plans", planID+".html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}

	db, err := openPlanDB(hgDir)
	if err != nil {
		t.Fatalf("openPlanDB: %v", err)
	}
	if err := dbpkg.StorePlanFeedback(db, planID, "slice-1", "approve", "true", ""); err != nil {
		db.Close()
		t.Fatalf("store approval: %v", err)
	}
	db.Close()

	return hgDir, planID
}

// writeSpecEnforcementConfig writes a config.json into the .wipnote dir.
func writeSpecEnforcementConfig(t *testing.T, hgDir string, promoteSlice, featureComplete bool) {
	t.Helper()
	body := fmt.Sprintf(`{"spec_enforcement":{"promote_slice":%t,"feature_complete":%t}}`,
		promoteSlice, featureComplete)
	if err := os.WriteFile(filepath.Join(hgDir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// TestPromoteSliceGate_Disabled — default config, no DecisionsNotes; succeeds.
func TestPromoteSliceGate_Disabled(t *testing.T) {
	dir, pID := seedPromoteFixture(t, "")

	featID, err := promoteSliceFromYAML(dir, pID, 1, false, false)
	if err != nil {
		t.Fatalf("expected success with gate disabled, got error: %v", err)
	}
	if !strings.HasPrefix(featID, "feat-") {
		t.Errorf("featID = %q", featID)
	}
}

// TestPromoteSliceGate_EnabledNoNotes — gate enabled + empty notes refuses.
func TestPromoteSliceGate_EnabledNoNotes(t *testing.T) {
	dir, pID := seedPromoteFixture(t, "")
	writeSpecEnforcementConfig(t, dir, true, false)

	_, err := promoteSliceFromYAML(dir, pID, 1, false, false)
	if err == nil {
		t.Fatal("expected gate refusal, got nil")
	}
	if !strings.Contains(err.Error(), "no decisions") {
		t.Errorf("error should mention missing decisions: %v", err)
	}
	if !strings.Contains(err.Error(), "elicit-decisions") {
		t.Errorf("error should point to remediation command: %v", err)
	}
}

// TestPromoteSliceGate_EnabledWithNotes — gate enabled + notes present succeeds.
func TestPromoteSliceGate_EnabledWithNotes(t *testing.T) {
	dir, pID := seedPromoteFixture(t, "### Decisions\nWe picked X.")
	writeSpecEnforcementConfig(t, dir, true, false)

	featID, err := promoteSliceFromYAML(dir, pID, 1, false, false)
	if err != nil {
		t.Fatalf("expected success with notes present, got: %v", err)
	}
	if !strings.HasPrefix(featID, "feat-") {
		t.Errorf("featID = %q", featID)
	}
}

// TestPromoteSliceGate_AllowSkip — gate enabled, no notes, --allow-spec-skip
// succeeds and writes an audit line into slice.Comment.
func TestPromoteSliceGate_AllowSkip(t *testing.T) {
	dir, pID := seedPromoteFixture(t, "")
	writeSpecEnforcementConfig(t, dir, true, false)

	featID, err := promoteSliceFromYAML(dir, pID, 1, false, true)
	if err != nil {
		t.Fatalf("expected --allow-spec-skip to succeed, got: %v", err)
	}
	if !strings.HasPrefix(featID, "feat-") {
		t.Errorf("featID = %q", featID)
	}

	// Audit comment must appear on the slice.
	plan, err := planyaml.Load(filepath.Join(dir, "plans", pID+".yaml"))
	if err != nil {
		t.Fatalf("reload plan: %v", err)
	}
	if !strings.Contains(plan.Slices[0].Comment, "allow-spec-skip") {
		t.Errorf("expected audit comment on slice, got: %q", plan.Slices[0].Comment)
	}
}
