package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
)

func TestFeatureReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full work-item lifecycle")
	}

	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	// Create and complete a feature.
	if err := testCreate("feature", "Reopen Me", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	node, _ := htmlparse.ParseFile(featFiles[0])
	featID := node.ID

	// Start then complete the feature so it's done.
	if err := runWiSetStatus("feature", featID, "in-progress"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := runWiSetStatus("feature", featID, "done"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Verify it's done before reopen.
	node, _ = htmlparse.ParseFile(featFiles[0])
	if node.Status != models.StatusDone {
		t.Fatalf("expected status done before reopen, got %q", node.Status)
	}

	// Reopen it.
	if err := executeFeatureReopen(featID); err != nil {
		t.Fatalf("reopen: %v", err)
	}

	// Verify status is now in-progress.
	node, _ = htmlparse.ParseFile(featFiles[0])
	if node.Status != models.StatusInProgress {
		t.Errorf("expected status in-progress after reopen, got %q", node.Status)
	}
}

func TestFeatureReopenNonDoneErrors(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	// Create a feature (status: todo by default).
	if err := testCreate("feature", "Not Done Feature", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	node, _ := htmlparse.ParseFile(featFiles[0])
	featID := node.ID

	// Reopen should fail since it's not done.
	err := executeFeatureReopen(featID)
	if err == nil {
		t.Fatal("expected error when reopening non-done feature, got nil")
	}
}
