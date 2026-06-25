package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

// TestOwningPlanID verifies that owningPlanID extracts a plan-* ID from the
// feature node's planned_in or part_of edges, and returns "" when none exists.
func TestOwningPlanID(t *testing.T) {
	cases := []struct {
		name  string
		edges map[string][]models.Edge
		want  string
	}{
		{
			name: "planned_in edge present",
			edges: map[string][]models.Edge{
				"planned_in": {{TargetID: "plan-abc12345"}},
			},
			want: "plan-abc12345",
		},
		{
			name: "part_of edge with plan target",
			edges: map[string][]models.Edge{
				"part_of": {{TargetID: "plan-xyz98765"}},
			},
			want: "plan-xyz98765",
		},
		{
			name: "part_of edge with track target (not a plan)",
			edges: map[string][]models.Edge{
				"part_of": {{TargetID: "trk-notatplan"}},
			},
			want: "",
		},
		{
			name:  "no edges",
			edges: nil,
			want:  "",
		},
		{
			name: "planned_in edge with non-plan target",
			edges: map[string][]models.Edge{
				"planned_in": {{TargetID: "feat-notaplan"}},
			},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := &models.Node{Edges: tc.edges}
			got := owningPlanID(node)
			if got != tc.want {
				t.Errorf("owningPlanID() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPlanFeatureIDs verifies that planFeatureIDs reads feature IDs from a
// plan YAML's slices (only slices with a FeatureID set).
func TestPlanFeatureIDs(t *testing.T) {
	wipDir := t.TempDir()
	plansDir := filepath.Join(wipDir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}

	planID := "plan-testfeatids"
	plan := &planyaml.PlanYAML{}
	plan.Meta.ID = planID
	plan.Meta.Status = "finalized"
	plan.Design.Problem = "test problem"
	plan.Slices = []planyaml.PlanSlice{
		{Num: 1, Title: "Slice one", FeatureID: "feat-aaaaaaaa"},
		{Num: 2, Title: "Slice two", FeatureID: "feat-bbbbbbbb"},
		{Num: 3, Title: "Slice three, no feature yet", FeatureID: ""},
	}

	yamlPath := filepath.Join(plansDir, planID+".yaml")
	if err := planyaml.Save(yamlPath, plan); err != nil {
		t.Fatalf("save plan YAML: %v", err)
	}

	ids, err := planFeatureIDs(wipDir, planID)
	if err != nil {
		t.Fatalf("planFeatureIDs: %v", err)
	}

	// Only slices with a non-empty FeatureID should be returned.
	if len(ids) != 2 {
		t.Fatalf("planFeatureIDs returned %d IDs, want 2: %v", len(ids), ids)
	}
	if ids[0] != "feat-aaaaaaaa" || ids[1] != "feat-bbbbbbbb" {
		t.Errorf("planFeatureIDs returned %v, want [feat-aaaaaaaa feat-bbbbbbbb]", ids)
	}
}

// TestPlanFeatureIDs_MissingYAML verifies that planFeatureIDs returns an error
// (not a panic) when the plan YAML does not exist.
func TestPlanFeatureIDs_MissingYAML(t *testing.T) {
	wipDir := t.TempDir()
	_, err := planFeatureIDs(wipDir, "plan-doesnotexist")
	if err == nil {
		t.Error("expected error for missing YAML, got nil")
	}
}

// TestAllFeaturesDone verifies the done-check across feature statuses.
func TestAllFeaturesDone(t *testing.T) {
	wipDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wipDir, "features"), 0o755); err != nil {
		t.Fatal(err)
	}

	p, err := workitem.Open(wipDir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	defer p.Close()

	// Create two features.
	feat1, err := p.Features.Create("Feature One")
	if err != nil {
		t.Fatalf("create feat1: %v", err)
	}
	feat2, err := p.Features.Create("Feature Two")
	if err != nil {
		t.Fatalf("create feat2: %v", err)
	}

	// Both pending: not all done.
	if allFeaturesDone(p, []string{feat1.ID, feat2.ID}) {
		t.Error("allFeaturesDone should be false when both features are pending")
	}

	// Complete only feat1: still not all done.
	if _, err := p.Features.Complete(feat1.ID); err != nil {
		t.Fatalf("complete feat1: %v", err)
	}
	if allFeaturesDone(p, []string{feat1.ID, feat2.ID}) {
		t.Error("allFeaturesDone should be false when only one of two is done")
	}

	// Complete feat2: now all done.
	if _, err := p.Features.Complete(feat2.ID); err != nil {
		t.Fatalf("complete feat2: %v", err)
	}
	if !allFeaturesDone(p, []string{feat1.ID, feat2.ID}) {
		t.Error("allFeaturesDone should be true when all features are done")
	}
}

// TestAllFeaturesDone_NonExistentFeature verifies that a missing feature is
// treated as not done (safe default).
func TestAllFeaturesDone_NonExistentFeature(t *testing.T) {
	wipDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wipDir, "features"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := workitem.Open(wipDir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	defer p.Close()

	if allFeaturesDone(p, []string{"feat-nonexistent"}) {
		t.Error("allFeaturesDone should be false for a non-existent feature")
	}
}

// TestMaybeAutoGeneratePlanRollupRecap_LastFeatureTriggers verifies that
// completing the last feature of a plan auto-generates the plan-rollup recap.
// This test requires a real git repo because RunPlanRollupRecap resolves the
// plan YAML's first commit.
func TestMaybeAutoGeneratePlanRollupRecap_LastFeatureTriggers(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full plan-rollup recap lifecycle with git")
	}

	// Set up a git repo with .wipnote structure.
	repo := initFixtureRepo(t)
	wipDir := filepath.Join(repo, ".wipnote")
	for _, sub := range []string{"features", "plans", "recaps"} {
		if err := os.MkdirAll(filepath.Join(wipDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p, err := workitem.Open(wipDir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	defer p.Close()

	// Create one feature.
	feat, err := p.Features.Create("Last Feature")
	if err != nil {
		t.Fatalf("create feature: %v", err)
	}

	planID := "plan-autorecaptest"

	// Write and commit a plan YAML that references the feature.
	plan := &planyaml.PlanYAML{}
	plan.Meta.ID = planID
	plan.Meta.Status = "finalized"
	plan.Design.Problem = "test problem"
	plan.Slices = []planyaml.PlanSlice{
		{Num: 1, Title: "Slice one", FeatureID: feat.ID},
	}

	plansDir := filepath.Join(wipDir, "plans")
	yamlPath := filepath.Join(plansDir, planID+".yaml")
	if err := planyaml.Save(yamlPath, plan); err != nil {
		t.Fatalf("save plan YAML: %v", err)
	}

	// Commit the plan YAML so RunPlanRollupRecap can find its first commit.
	runInRepo := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cmd %v: %v\n%s", args, err, out)
		}
	}
	runInRepo("git", "add", yamlPath)
	runInRepo("git", "commit", "--no-gpg-sign", "-m", "add plan YAML for auto-recap test")

	// Build the feature node with a planned_in edge to the plan.
	featNode, err := p.Features.Get(feat.ID)
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if featNode.Edges == nil {
		featNode.Edges = make(map[string][]models.Edge)
	}
	featNode.Edges[string(models.RelPlannedIn)] = []models.Edge{
		{TargetID: planID},
	}

	// Mark the feature done (required for allFeaturesDone to return true).
	if _, err := p.Features.Complete(feat.ID); err != nil {
		t.Fatalf("complete feature: %v", err)
	}

	// Call the function under test.
	maybeAutoGeneratePlanRollupRecap(wipDir, featNode, p)

	// Assert the plan-rollup recap artifact was written.
	recapPath := filepath.Join(wipDir, "recaps", "recap-pln-"+planID+".html")
	if _, statErr := os.Stat(recapPath); statErr != nil {
		t.Fatalf("plan-rollup recap not generated at %s: %v", recapPath, statErr)
	}
}

// TestMaybeAutoGeneratePlanRollupRecap_NonLastFeatureNoTrigger verifies that
// completing a feature when other plan features are still in progress does NOT
// trigger recap generation.
func TestMaybeAutoGeneratePlanRollupRecap_NonLastFeatureNoTrigger(t *testing.T) {
	wipDir := t.TempDir()
	for _, sub := range []string{"features", "plans", "recaps"} {
		if err := os.MkdirAll(filepath.Join(wipDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p, err := workitem.Open(wipDir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	defer p.Close()

	// Create two features.
	feat1, err := p.Features.Create("Feature One")
	if err != nil {
		t.Fatalf("create feat1: %v", err)
	}
	feat2, err := p.Features.Create("Feature Two")
	if err != nil {
		t.Fatalf("create feat2: %v", err)
	}

	planID := "plan-nontriggertest"

	// Write a plan YAML referencing both features.
	plan := &planyaml.PlanYAML{}
	plan.Meta.ID = planID
	plan.Meta.Status = "finalized"
	plan.Design.Problem = "test problem"
	plan.Slices = []planyaml.PlanSlice{
		{Num: 1, Title: "Slice one", FeatureID: feat1.ID},
		{Num: 2, Title: "Slice two", FeatureID: feat2.ID},
	}
	yamlPath := filepath.Join(wipDir, "plans", planID+".yaml")
	if err := planyaml.Save(yamlPath, plan); err != nil {
		t.Fatalf("save plan YAML: %v", err)
	}

	// Build feat1 node with a planned_in edge.
	feat1Node, err := p.Features.Get(feat1.ID)
	if err != nil {
		t.Fatalf("get feat1: %v", err)
	}
	if feat1Node.Edges == nil {
		feat1Node.Edges = make(map[string][]models.Edge)
	}
	feat1Node.Edges[string(models.RelPlannedIn)] = []models.Edge{
		{TargetID: planID},
	}

	// Complete feat1 only (feat2 remains pending).
	if _, err := p.Features.Complete(feat1.ID); err != nil {
		t.Fatalf("complete feat1: %v", err)
	}

	// Call function under test with feat1 completed (feat2 still pending).
	maybeAutoGeneratePlanRollupRecap(wipDir, feat1Node, p)

	// Assert no plan-rollup recap was generated.
	recapPath := filepath.Join(wipDir, "recaps", "recap-pln-"+planID+".html")
	if _, statErr := os.Stat(recapPath); statErr == nil {
		t.Error("plan-rollup recap should NOT have been generated when a feature is still pending")
	}
}

// TestMaybeAutoGeneratePlanRollupRecap_Idempotent_ConcurrentGeneration verifies
// that an uncommitted recap file (written by a concurrent agent that is still
// running) is treated as "generation in progress" and is NOT overwritten.
// This guards the concurrent-duplicate case: the file exists on disk but has no
// git history yet, meaning another process is actively writing it.
func TestMaybeAutoGeneratePlanRollupRecap_Idempotent_ConcurrentGeneration(t *testing.T) {
	wipDir := t.TempDir()
	for _, sub := range []string{"features", "plans", "recaps"} {
		if err := os.MkdirAll(filepath.Join(wipDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p, err := workitem.Open(wipDir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	defer p.Close()

	feat, err := p.Features.Create("Solo Feature")
	if err != nil {
		t.Fatalf("create feature: %v", err)
	}

	planID := "plan-idempotenttest"

	// Write plan YAML.
	plan := &planyaml.PlanYAML{}
	plan.Meta.ID = planID
	plan.Meta.Status = "finalized"
	plan.Design.Problem = "test problem"
	plan.Slices = []planyaml.PlanSlice{
		{Num: 1, Title: "Slice one", FeatureID: feat.ID},
	}
	yamlPath := filepath.Join(wipDir, "plans", planID+".yaml")
	if err := planyaml.Save(yamlPath, plan); err != nil {
		t.Fatalf("save plan YAML: %v", err)
	}

	// Pre-create the recap artifact WITHOUT committing it to git, simulating a
	// concurrent agent that is currently writing the recap. The file exists on
	// disk but has no git history (recapLastCommitSHA returns "").
	recapPath := filepath.Join(wipDir, "recaps", "recap-pln-"+planID+".html")
	if err := os.WriteFile(recapPath, []byte("<html>concurrent recap in progress</html>"), 0o644); err != nil {
		t.Fatalf("write existing recap: %v", err)
	}
	existingContent, _ := os.ReadFile(recapPath)

	// Build feat node with planned_in edge.
	featNode, err := p.Features.Get(feat.ID)
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if featNode.Edges == nil {
		featNode.Edges = make(map[string][]models.Edge)
	}
	featNode.Edges[string(models.RelPlannedIn)] = []models.Edge{
		{TargetID: planID},
	}

	// Complete the feature.
	if _, err := p.Features.Complete(feat.ID); err != nil {
		t.Fatalf("complete feature: %v", err)
	}

	// Call function under test. Should treat the uncommitted file as a
	// concurrent-generation in-progress sentinel and skip.
	maybeAutoGeneratePlanRollupRecap(wipDir, featNode, p)

	// Assert the concurrent recap was NOT overwritten.
	afterContent, _ := os.ReadFile(recapPath)
	if string(afterContent) != string(existingContent) {
		t.Error("existing uncommitted plan recap was overwritten — concurrent-generation guard violated")
	}
}

// TestMaybeAutoGeneratePlanRollupRecap_RefreshesFinalizePhaseRecap verifies
// that a pre-implementation recap written by plan finalize is REFRESHED (not
// skipped) when the last feature completes. This is the roborev #544 fix 1:
// the old code skipped whenever the file existed; the new code detects that the
// existing recap was committed before the current HEAD and regenerates it.
func TestMaybeAutoGeneratePlanRollupRecap_RefreshesFinalizePhaseRecap(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full plan-rollup recap lifecycle with git")
	}

	// Set up a real git repo.
	repo := initFixtureRepo(t)
	wipDir := filepath.Join(repo, ".wipnote")
	for _, sub := range []string{"features", "plans", "recaps"} {
		if err := os.MkdirAll(filepath.Join(wipDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p, err := workitem.Open(wipDir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	defer p.Close()

	feat, err := p.Features.Create("Feature Under Plan")
	if err != nil {
		t.Fatalf("create feature: %v", err)
	}

	planID := "plan-refreshtest"

	// Write and commit the plan YAML.
	plan := &planyaml.PlanYAML{}
	plan.Meta.ID = planID
	plan.Meta.Status = "finalized"
	plan.Design.Problem = "test refresh"
	plan.Slices = []planyaml.PlanSlice{
		{Num: 1, Title: "Slice one", FeatureID: feat.ID},
	}
	plansDir := filepath.Join(wipDir, "plans")
	yamlPath := filepath.Join(plansDir, planID+".yaml")
	if err := planyaml.Save(yamlPath, plan); err != nil {
		t.Fatalf("save plan YAML: %v", err)
	}

	runInRepo := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cmd %v: %v\n%s", args, err, out)
		}
	}

	// Commit the plan YAML (simulates plan finalize commit).
	runInRepo("git", "add", yamlPath)
	runInRepo("git", "commit", "--no-gpg-sign", "-m", "plan("+planID+"): finalize")

	// Simulate plan finalize generating a pre-implementation recap: write
	// the recap file and commit it. This represents the stale state left by
	// plan finalize before any features are built.
	recapPath := filepath.Join(wipDir, "recaps", "recap-pln-"+planID+".html")
	staleContent := []byte("<html>pre-implementation recap from plan finalize</html>")
	if err := os.WriteFile(recapPath, staleContent, 0o644); err != nil {
		t.Fatalf("write stale recap: %v", err)
	}
	runInRepo("git", "add", recapPath)
	runInRepo("git", "commit", "--no-gpg-sign", "-m", "wipnote: recap-pln-"+planID+" (finalize phase)")

	// Add a commit AFTER the finalize-phase recap to simulate feature implementation.
	dummyFile := filepath.Join(repo, "impl.go")
	if err := os.WriteFile(dummyFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write impl file: %v", err)
	}
	runInRepo("git", "add", dummyFile)
	runInRepo("git", "commit", "--no-gpg-sign", "-m", "feat: implement feature")

	// Build the feature node with a planned_in edge.
	featNode, err := p.Features.Get(feat.ID)
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if featNode.Edges == nil {
		featNode.Edges = make(map[string][]models.Edge)
	}
	featNode.Edges[string(models.RelPlannedIn)] = []models.Edge{
		{TargetID: planID},
	}

	// Complete the feature — now all features are done.
	if _, err := p.Features.Complete(feat.ID); err != nil {
		t.Fatalf("complete feature: %v", err)
	}

	// Call the function under test. The stale recap is committed behind HEAD,
	// so it should be REFRESHED (the file must change).
	maybeAutoGeneratePlanRollupRecap(wipDir, featNode, p)

	// The recap must have been regenerated — content must differ from the stale
	// pre-implementation placeholder.
	afterContent, err := os.ReadFile(recapPath)
	if err != nil {
		t.Fatalf("read recap after: %v", err)
	}
	if string(afterContent) == string(staleContent) {
		t.Error("plan recap was NOT refreshed — stale pre-implementation recap left in place (roborev #544 fix 1 regression)")
	}
}

// TestMaybeAutoGeneratePlanRollupRecap_Noplan verifies that a feature with no
// plan edge is a no-op (no recap generated, no panic).
func TestMaybeAutoGeneratePlanRollupRecap_NoPlan(t *testing.T) {
	wipDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wipDir, "features"), 0o755); err != nil {
		t.Fatal(err)
	}

	p, err := workitem.Open(wipDir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	defer p.Close()

	// Feature with no plan edge.
	featNode := &models.Node{
		ID:     "feat-noplan",
		Status: models.StatusDone,
		Edges:  map[string][]models.Edge{},
	}

	// Should be a silent no-op.
	maybeAutoGeneratePlanRollupRecap(wipDir, featNode, p)

	// Confirm no recap was created.
	entries, _ := os.ReadDir(filepath.Join(wipDir, "recaps"))
	if len(entries) > 0 {
		t.Errorf("expected no recap files, got %d", len(entries))
	}
}
