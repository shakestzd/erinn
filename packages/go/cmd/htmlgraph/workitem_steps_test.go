package main

import (
	"strings"
	"testing"
)

func TestRemoveStep(t *testing.T) {
	_, hgDir := setupHgDir(t)

	if err := testCreate("feature", "Step Feature", "", "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featID := findFeatureID(t, hgDir)

	// Add 3 steps
	for _, desc := range []string{"step one", "step two", "step three"} {
		if err := runWiAddStep("feature", featID, []string{desc}, false); err != nil {
			t.Fatalf("add step %q: %v", desc, err)
		}
	}

	// Remove step 2 (middle)
	if err := runWiRemoveStep("feature", featID, []string{"2"}); err != nil {
		t.Fatalf("remove step 2: %v", err)
	}

	node := readFeatureNode(t, hgDir)
	if len(node.Steps) != 2 {
		t.Fatalf("expected 2 steps after removal, got %d", len(node.Steps))
	}
	if node.Steps[0].Description != "step one" {
		t.Errorf("step[0] description = %q, want %q", node.Steps[0].Description, "step one")
	}
	if node.Steps[1].Description != "step three" {
		t.Errorf("step[1] description = %q, want %q", node.Steps[1].Description, "step three")
	}
}

func TestRemoveStepMultiple(t *testing.T) {
	_, hgDir := setupHgDir(t)

	if err := testCreate("feature", "Batch Remove Feature", "", "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featID := findFeatureID(t, hgDir)

	for _, desc := range []string{"step one", "step two", "step three", "step four"} {
		if err := runWiAddStep("feature", featID, []string{desc}, false); err != nil {
			t.Fatalf("add step %q: %v", desc, err)
		}
	}

	// Remove steps 2 and 4 (descending order handled internally)
	if err := runWiRemoveStep("feature", featID, []string{"2", "4"}); err != nil {
		t.Fatalf("remove steps 2 and 4: %v", err)
	}

	node := readFeatureNode(t, hgDir)
	if len(node.Steps) != 2 {
		t.Fatalf("expected 2 steps after removal, got %d", len(node.Steps))
	}
	if node.Steps[0].Description != "step one" {
		t.Errorf("step[0] description = %q, want %q", node.Steps[0].Description, "step one")
	}
	if node.Steps[1].Description != "step three" {
		t.Errorf("step[1] description = %q, want %q", node.Steps[1].Description, "step three")
	}
}

func TestRemoveStepOutOfRange(t *testing.T) {
	_, hgDir := setupHgDir(t)

	if err := testCreate("feature", "Step Feature", "", "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featID := findFeatureID(t, hgDir)

	if err := runWiAddStep("feature", featID, []string{"only step"}, false); err != nil {
		t.Fatalf("add step: %v", err)
	}

	if err := runWiRemoveStep("feature", featID, []string{"0"}); err == nil {
		t.Error("expected error when removing step 0, got nil")
	}
	if err := runWiRemoveStep("feature", featID, []string{"5"}); err == nil {
		t.Error("expected error when removing step 5 (out of range), got nil")
	}
}

func TestCompleteStep(t *testing.T) {
	_, hgDir := setupHgDir(t)

	if err := testCreate("feature", "Complete Step Feature", "", "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featID := findFeatureID(t, hgDir)

	for _, desc := range []string{"first step", "second step"} {
		if err := runWiAddStep("feature", featID, []string{desc}, false); err != nil {
			t.Fatalf("add step %q: %v", desc, err)
		}
	}

	if err := runWiCompleteStep("feature", featID, []string{"1"}); err != nil {
		t.Fatalf("complete step 1: %v", err)
	}

	node := readFeatureNode(t, hgDir)
	if len(node.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(node.Steps))
	}
	if !node.Steps[0].Completed {
		t.Errorf("step[0] should be completed")
	}
	if node.Steps[1].Completed {
		t.Errorf("step[1] should not be completed")
	}
}

func TestCompleteStepMultiple(t *testing.T) {
	_, hgDir := setupHgDir(t)

	if err := testCreate("feature", "Batch Complete Feature", "", "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featID := findFeatureID(t, hgDir)

	for _, desc := range []string{"first step", "second step", "third step"} {
		if err := runWiAddStep("feature", featID, []string{desc}, false); err != nil {
			t.Fatalf("add step %q: %v", desc, err)
		}
	}

	if err := runWiCompleteStep("feature", featID, []string{"1", "3"}); err != nil {
		t.Fatalf("complete steps 1 and 3: %v", err)
	}

	node := readFeatureNode(t, hgDir)
	if len(node.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(node.Steps))
	}
	if !node.Steps[0].Completed {
		t.Errorf("step[0] should be completed")
	}
	if node.Steps[1].Completed {
		t.Errorf("step[1] should not be completed")
	}
	if !node.Steps[2].Completed {
		t.Errorf("step[2] should be completed")
	}
}

func TestUpdateStep(t *testing.T) {
	_, hgDir := setupHgDir(t)

	if err := testCreate("feature", "Update Step Feature", "", "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featID := findFeatureID(t, hgDir)

	for _, desc := range []string{"original step one", "original step two"} {
		if err := runWiAddStep("feature", featID, []string{desc}, false); err != nil {
			t.Fatalf("add step %q: %v", desc, err)
		}
	}

	if err := runWiUpdateStep("feature", featID, "1", "updated step one"); err != nil {
		t.Fatalf("update step 1: %v", err)
	}

	node := readFeatureNode(t, hgDir)
	if len(node.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(node.Steps))
	}
	if node.Steps[0].Description != "updated step one" {
		t.Errorf("step[0] description = %q, want %q", node.Steps[0].Description, "updated step one")
	}
	if node.Steps[1].Description != "original step two" {
		t.Errorf("step[1] description = %q, want %q", node.Steps[1].Description, "original step two")
	}
}

func TestBatchAddStepMultiple(t *testing.T) {
	_, hgDir := setupHgDir(t)

	if err := testCreate("feature", "Batch Step Feature", "", "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featID := findFeatureID(t, hgDir)

	steps := []string{"Step A", "Step B", "Step C"}
	if err := runWiAddStep("feature", featID, steps, false); err != nil {
		t.Fatalf("batch add-step: %v", err)
	}

	node := readFeatureNode(t, hgDir)
	if len(node.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(node.Steps))
	}
	for i, want := range steps {
		if node.Steps[i].Description != want {
			t.Errorf("step[%d] description = %q, want %q", i, node.Steps[i].Description, want)
		}
	}
}

func TestBatchAddStepSingle(t *testing.T) {
	_, hgDir := setupHgDir(t)

	if err := testCreate("feature", "Single Step Feature", "", "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featID := findFeatureID(t, hgDir)

	if err := runWiAddStep("feature", featID, []string{"Only step"}, false); err != nil {
		t.Fatalf("add single step: %v", err)
	}

	node := readFeatureNode(t, hgDir)
	if len(node.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(node.Steps))
	}
	if node.Steps[0].Description != "Only step" {
		t.Errorf("step[0] description = %q, want %q", node.Steps[0].Description, "Only step")
	}
}

func TestBatchAddStepEmpty(t *testing.T) {
	_, hgDir := setupHgDir(t)

	if err := testCreate("feature", "Empty Step Feature", "", "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featID := findFeatureID(t, hgDir)

	err := runWiAddStep("feature", featID, []string{}, false)
	if err == nil {
		t.Fatal("expected error when no step descriptions provided, got nil")
	}
	if !strings.Contains(err.Error(), "no step descriptions") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "no step descriptions")
	}
}
