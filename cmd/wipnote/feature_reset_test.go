package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
)

func setupResetTest(t *testing.T) (tmpDir, hgDir string) {
	t.Helper()
	tmpDir = t.TempDir()
	hgDir = filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return tmpDir, hgDir
}

func TestFeatureReset_HappyPath(t *testing.T) {
	tmpDir, hgDir := setupResetTest(t)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	if err := testCreate("feature", "Reset Me", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	node, _ := htmlparse.ParseFile(featFiles[0])
	featID := node.ID

	// Start the feature.
	if err := runWiSetStatus("feature", featID, "in-progress"); err != nil {
		t.Fatalf("start: %v", err)
	}

	node, _ = htmlparse.ParseFile(featFiles[0])
	if node.Status != models.StatusInProgress {
		t.Fatalf("expected in-progress before reset, got %q", node.Status)
	}

	// Reset it.
	title, err := executeReset("feature", featID)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if title != "Reset Me" {
		t.Errorf("title: want %q, got %q", "Reset Me", title)
	}

	// Status must be todo, agent cleared.
	node, _ = htmlparse.ParseFile(featFiles[0])
	if node.Status != models.StatusTodo {
		t.Errorf("status: want todo, got %q", node.Status)
	}
	if node.AgentAssigned != "" {
		t.Errorf("AgentAssigned: want empty, got %q", node.AgentAssigned)
	}
}

func TestFeatureReset_PreservesStepsAndDescription(t *testing.T) {
	tmpDir, hgDir := setupResetTest(t)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	if err := testCreate("feature", "Preserve Me", trackID, "high", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	node, _ := htmlparse.ParseFile(featFiles[0])
	featID := node.ID

	// Add description and a step.
	if err := runSetDescription("feature", featID, "important description", "", "", "", false); err != nil {
		t.Fatalf("set description: %v", err)
	}

	// Start then reset.
	if err := runWiSetStatus("feature", featID, "in-progress"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := executeReset("feature", featID); err != nil {
		t.Fatalf("reset: %v", err)
	}

	// Description and priority must be preserved.
	node, _ = htmlparse.ParseFile(featFiles[0])
	if !stringContains(node.Content, "important description") {
		t.Errorf("description not preserved: got %q", node.Content)
	}
	if node.Priority != models.Priority("high") {
		t.Errorf("priority: want high, got %q", node.Priority)
	}
}

func TestFeatureReset_ErrorOnTodo(t *testing.T) {
	tmpDir, hgDir := setupResetTest(t)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	if err := testCreate("feature", "Todo Feature", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	node, _ := htmlparse.ParseFile(featFiles[0])

	_, err := executeReset("feature", node.ID)
	if err == nil {
		t.Fatal("expected error when resetting todo feature, got nil")
	}
	if !stringContains(err.Error(), "not in-progress") {
		t.Errorf("error should mention 'not in-progress': %v", err)
	}
}

func TestFeatureReset_ErrorOnDone(t *testing.T) {
	tmpDir, hgDir := setupResetTest(t)
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	if err := testCreate("feature", "Done Feature", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	node, _ := htmlparse.ParseFile(featFiles[0])
	featID := node.ID

	if err := runWiSetStatus("feature", featID, "in-progress"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := runWiSetStatus("feature", featID, "done"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	_, err := executeReset("feature", featID)
	if err == nil {
		t.Fatal("expected error when resetting done feature, got nil")
	}
	if !stringContains(err.Error(), "not in-progress") {
		t.Errorf("error should mention 'not in-progress': %v", err)
	}
}
