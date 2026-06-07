package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/workitem"
)

// TestPlanFirstLifecycle exercises the complete plan-first pipeline:
// create → add-slice-yaml → render → critique → finalize → verify work items.
func TestPlanFirstLifecycle(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"plans", "features", "tracks"} {
		os.MkdirAll(filepath.Join(dir, sub), 0o755)
	}

	// Step 1: Create plan from topic.
	planID, err := createPlanFromTopic(dir, "Auth Middleware Rewrite", "Rewrite auth for compliance")
	if err != nil {
		t.Fatalf("createPlanFromTopic: %v", err)
	}

	// Verify YAML scaffold was created.
	yamlPath := filepath.Join(dir, "plans", planID+".yaml")
	if _, err := os.Stat(yamlPath); err != nil {
		t.Fatalf("YAML scaffold not found: %v", err)
	}

	// Step 2: Add slices via YAML workflow.
	for _, title := range []string{"Error handling layer", "Token validation", "Migration script"} {
		if err := runPlanAddSliceYAML(dir, planID, title,
			"Implement "+title, "", "", "", "", "S", "Low", ""); err != nil {
			t.Fatalf("addSliceYAML(%s): %v", title, err)
		}
	}

	// Step 2b: Render HTML from YAML.
	if err := renderPlanToFile(dir, planID); err != nil {
		t.Fatalf("renderPlanToFile: %v", err)
	}

	// Verify rendered HTML has CRISPI elements.
	planPath := filepath.Join(dir, "plans", planID+".html")
	data, _ := os.ReadFile(planPath)
	if !strings.Contains(string(data), "btn-finalize") {
		t.Error("rendered plan should use CRISPI template with btn-finalize")
	}

	// Step 3: Critique (reads from YAML).
	critique, err := extractCritiqueData(dir, planID)
	if err != nil {
		t.Fatalf("extractCritiqueData: %v", err)
	}
	if !critique.CritiqueWarranted {
		t.Error("3 slices should warrant critique")
	}
	if critique.Complexity != "medium" {
		t.Errorf("complexity = %q, want medium", critique.Complexity)
	}
	if len(critique.Slices) != 3 {
		t.Errorf("critique slices = %d, want 3", len(critique.Slices))
	}

	// Step 4: Validate.
	validation, err := validatePlan(dir, planID)
	if err != nil {
		t.Fatalf("validatePlan: %v", err)
	}
	if !validation.Valid {
		t.Errorf("plan should be valid, got errors: %v", validation.Errors)
	}

	// Step 5: Finalize — creates track + features (reads slices from YAML).
	p, err := workitem.Open(dir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	defer p.Close()

	result, err := executePlanFinalize(p, dir, planID)
	if err != nil {
		t.Fatalf("executePlanFinalize: %v", err)
	}

	// Verify track created.
	if result.TrackID == "" {
		t.Fatal("no track created")
	}
	if !strings.HasPrefix(result.TrackID, "trk-") {
		t.Errorf("track ID %q doesn't have trk- prefix", result.TrackID)
	}

	// Verify 3 features created.
	if len(result.FeatureIDs) != 3 {
		t.Errorf("features = %d, want 3", len(result.FeatureIDs))
	}

	// Verify track file exists.
	trackPath := filepath.Join(dir, "tracks", result.TrackID+".html")
	if _, err := os.Stat(trackPath); err != nil {
		t.Errorf("track file missing: %s", trackPath)
	}

	// Step 6: Idempotent re-finalize.
	result2, err := executePlanFinalize(p, dir, planID)
	if err != nil {
		t.Fatalf("re-finalize: %v", err)
	}
	if !result2.AlreadyFinalized {
		t.Error("re-finalize should report AlreadyFinalized=true")
	}
	if result2.TrackID != result.TrackID {
		t.Errorf("re-finalize track %q != original %q", result2.TrackID, result.TrackID)
	}
}

// TestPlanLifecycle_NoSlices verifies finalize with no slices
// creates a track but zero features.
func TestPlanLifecycle_NoSlices(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"plans", "features", "tracks"} {
		os.MkdirAll(filepath.Join(dir, sub), 0o755)
	}

	planID, err := createPlanFromTopic(dir, "Empty Plan", "nothing here")
	if err != nil {
		t.Fatal(err)
	}

	p, err := workitem.Open(dir, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	result, err := executePlanFinalize(p, dir, planID)
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
