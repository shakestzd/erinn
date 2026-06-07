package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/htmlparse"
)

func TestLinkErrorMessageInvalidPrefix(t *testing.T) {
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

	// Create a valid feature to use as target
	if err := testCreate("feature", "Target Feature", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}

	// Find the feature ID
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	toID := featNode.ID

	// Try to add link from invalid ID (bad prefix like "bad-" with hex suffix)
	// resolveCollection checks prefix but won't recognize "bad-", triggering the error
	// We use a full ID format "bad-12345678" so resolveID passes but resolveCollection fails
	err := runLinkAdd("bad-12345678", toID, "relates_to")
	if err == nil {
		t.Fatal("expected error for invalid prefix, got nil")
	}

	errMsg := err.Error()

	// Check that error message lists valid prefixes
	validPrefixes := []string{"feat-", "bug-", "spk-", "trk-", "plan-", "spec-"}
	for _, prefix := range validPrefixes {
		if !stringContains(errMsg, prefix) {
			t.Errorf("error message should list %q prefix: %q", prefix, errMsg)
		}
	}
}

func TestLinkErrorMessageNoEdge(t *testing.T) {
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

	// Create two features
	if err := testCreate("feature", "Feature 1", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature 1: %v", err)
	}
	if err := testCreate("feature", "Feature 2", trackID, "medium", false, false); err != nil {
		t.Fatalf("create feature 2: %v", err)
	}

	// Find the feature IDs
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	feat1Node, _ := htmlparse.ParseFile(featFiles[0])
	feat2Node, _ := htmlparse.ParseFile(featFiles[1])
	fromID := feat1Node.ID
	toID := feat2Node.ID

	// Try to remove a link that doesn't exist
	err := runLinkRemove(fromID, toID, "blocks")
	if err == nil {
		t.Fatal("expected error for non-existent edge, got nil")
	}

	errMsg := err.Error()

	// Check that error message suggests 'link list'
	if !stringContains(errMsg, "link list") {
		t.Errorf("error message should suggest 'link list': %q", errMsg)
	}
	// Also check that it mentions the fromID in the suggestion
	if !stringContains(errMsg, fromID) {
		t.Errorf("error message should mention the fromID (%s): %q", fromID, errMsg)
	}
}

func TestWorkitemErrorMessageUnknownType(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	// Try to create a work item with invalid type
	opts := &wiCreateOpts{
		trackID:     "",
		priority:    "medium",
		description: "test",
		start:       false,
		noLink:      false,
	}
	err := runWiCreate("invalid_type", "Test Title", opts)

	if err == nil {
		t.Fatal("expected error for invalid type, got nil")
	}

	errMsg := err.Error()

	// Check that error message lists all valid types
	validTypes := []string{"feature", "bug", "spike", "track", "plan", "spec"}
	for _, typ := range validTypes {
		if !stringContains(errMsg, typ) {
			t.Errorf("error message should list valid type %q: %q", typ, errMsg)
		}
	}
}
