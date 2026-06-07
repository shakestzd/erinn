package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/planyaml"
)

func TestRunPlanCreateFromTopic(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"plans"} {
		os.MkdirAll(filepath.Join(dir, sub), 0o755)
	}

	planID, err := createPlanFromTopic(dir, "Auth Middleware Rewrite", "Rewrite auth for compliance")
	if err != nil {
		t.Fatalf("createPlanFromTopic: %v", err)
	}

	// Verify hex8 format.
	matched, _ := regexp.MatchString(`^plan-[0-9a-f]{8}$`, planID)
	if !matched {
		t.Errorf("plan ID %q does not match hex8 format", planID)
	}

	// Verify HTML file exists (minimal workitem registration).
	planPath := filepath.Join(dir, "plans", planID+".html")
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("plan HTML file not found: %v", err)
	}
	html := string(data)

	// Verify title is present in the minimal HTML.
	if !strings.Contains(html, "Auth Middleware Rewrite") {
		t.Error("plan HTML missing title")
	}

	// Verify YAML scaffold was created.
	yamlPath := filepath.Join(dir, "plans", planID+".yaml")
	yamlData, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("YAML scaffold not found: %v", err)
	}
	yaml := string(yamlData)

	if !strings.Contains(yaml, "Auth Middleware Rewrite") {
		t.Error("YAML missing title")
	}
	if !strings.Contains(yaml, "Rewrite auth for compliance") {
		t.Error("YAML missing description")
	}
	if !strings.Contains(yaml, planID) {
		t.Error("YAML meta.id should match plan ID")
	}
}

func TestRunPlanAddSlice(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "plans"), 0o755)

	planID, err := createPlanFromTopic(dir, "Test Plan", "A test plan")
	if err != nil {
		t.Fatalf("createPlanFromTopic: %v", err)
	}

	// Add slices via YAML workflow.
	if err := runPlanAddSliceYAML(dir, planID, "Implement error handling",
		"Handle errors", "", "", "", "", "S", "Low", ""); err != nil {
		t.Fatalf("addSliceYAML: %v", err)
	}

	// Render to HTML.
	if err := renderPlanToFile(dir, planID); err != nil {
		t.Fatalf("renderPlanToFile: %v", err)
	}

	// Verify slice exists in the rendered HTML.
	planPath := filepath.Join(dir, "plans", planID+".html")
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)

	if !strings.Contains(html, "Implement error handling") {
		t.Error("plan HTML missing slice title")
	}

	// Verify CRISPI-specific elements were rendered.
	if !strings.Contains(html, `data-node="1"`) {
		t.Error("plan HTML missing graph node data-node=1")
	}
	if !strings.Contains(html, `data-slice="1"`) {
		t.Error("plan HTML missing slice card data-slice=1")
	}

	// Add a second slice and re-render.
	if err := runPlanAddSliceYAML(dir, planID, "Add tests",
		"Write unit tests", "", "", "", "", "S", "Low", ""); err != nil {
		t.Fatalf("addSliceYAML second: %v", err)
	}
	if err := renderPlanToFile(dir, planID); err != nil {
		t.Fatalf("renderPlanToFile second: %v", err)
	}

	data, _ = os.ReadFile(planPath)
	html = string(data)
	if !strings.Contains(html, "Add tests") {
		t.Error("plan HTML missing second slice")
	}
	if !strings.Contains(html, `data-node="2"`) {
		t.Error("plan HTML missing graph node data-node=2")
	}
	if !strings.Contains(html, `data-slice="2"`) {
		t.Error("plan HTML missing slice card data-slice=2")
	}
}

func TestUpdatePlanStatus(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}

	// Write minimal plan fixtures. updatePlanStatus now reads/writes YAML;
	// both HTML and YAML files must exist because findPlanFile looks for HTML.
	planID := "plan-test123"
	htmlPath := filepath.Join(plansDir, planID+".html")
	htmlFixture := `<html><body><article id="plan-test123" data-type="plan" data-status="draft">test plan</article></body></html>`
	if err := os.WriteFile(htmlPath, []byte(htmlFixture), 0o644); err != nil {
		t.Fatalf("write html fixture: %v", err)
	}
	yamlFixture := "meta:\n  id: plan-test123\n  title: Test Plan\n  status: draft\n  version: 1\n"
	yamlPath := filepath.Join(plansDir, planID+".yaml")
	if err := os.WriteFile(yamlPath, []byte(yamlFixture), 0o644); err != nil {
		t.Fatalf("write yaml fixture: %v", err)
	}

	if err := updatePlanStatus(dir, planID, "done"); err != nil {
		t.Fatalf("updatePlanStatus: %v", err)
	}

	// Assert YAML meta.status was updated (YAML is now the source of truth).
	plan, err := planyaml.Load(yamlPath)
	if err != nil {
		t.Fatalf("load plan YAML after update: %v", err)
	}
	if plan.Meta.Status != "done" {
		t.Errorf("expected YAML meta.status=\"done\", got %q", plan.Meta.Status)
	}

	// Assert HTML was NOT modified by updatePlanStatus (caller owns re-render).
	data, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read plan HTML after update: %v", err)
	}
	if strings.Contains(string(data), `data-status="done"`) {
		t.Errorf("updatePlanStatus must not modify HTML; HTML re-render is caller's responsibility")
	}
}
