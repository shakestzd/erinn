package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/plan/planyaml"
	"github.com/shakestzd/wipnote/core/workitem"
)

func TestCritique_ComplexityGateLow(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "plans"), 0o755)

	planID, err := createPlanFromTopic(dir, "Small Plan", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := runPlanAddSliceYAML(dir, planID, "Single slice",
		"Do one thing", "", "", "", "", "S", "Low", ""); err != nil {
		t.Fatal(err)
	}

	out, err := extractCritiqueData(dir, planID)
	if err != nil {
		t.Fatalf("extractCritiqueData: %v", err)
	}
	if out.CritiqueWarranted {
		t.Error("expected critique_warranted=false for 1 slice")
	}
	if out.Complexity != "low" {
		t.Errorf("complexity = %q, want low", out.Complexity)
	}
}

func TestCritique_ComplexityGateMedium(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "plans"), 0o755)

	planID, err := createPlanFromTopic(dir, "Medium Plan", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"S1", "S2", "S3", "S4"} {
		if err := runPlanAddSliceYAML(dir, planID, s,
			"Do "+s, "", "", "", "", "S", "Low", ""); err != nil {
			t.Fatal(err)
		}
	}

	out, err := extractCritiqueData(dir, planID)
	if err != nil {
		t.Fatal(err)
	}
	if !out.CritiqueWarranted {
		t.Error("expected critique_warranted=true for 4 slices")
	}
	if out.Complexity != "medium" {
		t.Errorf("complexity = %q, want medium", out.Complexity)
	}
}

func TestCritique_TitleExtraction(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "plans"), 0o755)

	planID, err := createPlanFromTopic(dir, "Auth Rewrite", "compliance driven")
	if err != nil {
		t.Fatal(err)
	}

	out, err := extractCritiqueData(dir, planID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Title != "Auth Rewrite" {
		t.Errorf("title = %q, want Auth Rewrite", out.Title)
	}
	if out.Description != "compliance driven" {
		t.Errorf("description = %q, want 'compliance driven'", out.Description)
	}
}

func TestClassifyComplexity(t *testing.T) {
	tests := []struct {
		count      int
		complexity string
		warranted  bool
	}{
		{0, "low", false},
		{1, "low", false},
		{2, "low", false},
		{3, "medium", true},
		{5, "medium", true},
		{6, "high", true},
		{10, "high", true},
	}

	for _, tc := range tests {
		c, w := classifyComplexity(tc.count)
		if c != tc.complexity || w != tc.warranted {
			t.Errorf("classifyComplexity(%d) = (%q, %v), want (%q, %v)",
				tc.count, c, w, tc.complexity, tc.warranted)
		}
	}
}

// TestPlanCritique_YAMLDoesNotOpenDB verifies that extractCritiqueData for a
// v2 YAML plan never calls workitem.Open (and therefore never touches SQLite).
// The test installs an open-factory spy that fails the test if invoked.
func TestPlanCritique_YAMLDoesNotOpenDB(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "plans"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Build a minimal YAML plan directly — no workitem.Open during setup.
	planID := "plan-spytest1"
	yamlPath := filepath.Join(dir, "plans", planID+".yaml")
	plan := planyaml.NewPlan(planID, "Spy Test Plan", "testing DB isolation")
	plan.Design.Problem = "test problem"
	plan.Design.Goals = []string{"goal1"}
	plan.Design.Constraints = []string{"constraint1"}
	plan.Slices = []planyaml.PlanSlice{
		{Num: 1, ID: "s1", Title: "Slice One", What: "do it", Why: "because",
			Files: []string{"x.go"}, DoneWhen: []string{"done"}, Tests: "unit",
			Effort: "S", Risk: "Low"},
		{Num: 2, ID: "s2", Title: "Slice Two", What: "do more", Why: "because",
			Files: []string{"y.go"}, DoneWhen: []string{"done"}, Tests: "unit",
			Effort: "S", Risk: "Low"},
		{Num: 3, ID: "s3", Title: "Slice Three", What: "finish", Why: "complete",
			Files: []string{"z.go"}, DoneWhen: []string{"done"}, Tests: "unit",
			Effort: "S", Risk: "Low"},
	}
	if err := planyaml.Save(yamlPath, plan); err != nil {
		t.Fatalf("save YAML plan: %v", err)
	}

	// Install spy: fail the test if workitem.Open is ever called.
	orig := critiqueProjectOpener
	t.Cleanup(func() { critiqueProjectOpener = orig })
	critiqueProjectOpener = func(projectDir, agent string) (*workitem.Project, error) {
		t.Errorf("workitem.Open called for YAML plan (projectDir=%s) — DB path leaked", projectDir)
		return nil, errors.New("spy: DB must not be opened for YAML plans")
	}

	out, err := extractCritiqueData(dir, planID)
	if err != nil {
		t.Fatalf("extractCritiqueData: %v", err)
	}
	if out.Title != "Spy Test Plan" {
		t.Errorf("title = %q, want Spy Test Plan", out.Title)
	}
	if out.SliceCount != 3 {
		t.Errorf("slice_count = %d, want 3", out.SliceCount)
	}
	if out.Description != "testing DB isolation" {
		t.Errorf("description = %q, want testing DB isolation", out.Description)
	}
}
