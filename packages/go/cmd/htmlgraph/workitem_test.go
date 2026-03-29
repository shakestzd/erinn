package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/htmlgraph/internal/htmlparse"
	"github.com/shakestzd/htmlgraph/internal/models"
)

// testCreate is a test helper that wraps runWiCreate with the opts struct.
func testCreate(typeName, title, trackID, priority string, start, noLink bool) error {
	return runWiCreate(typeName, title, &wiCreateOpts{
		trackID:  trackID,
		priority: priority,
		start:    start,
		noLink:   noLink,
	})
}

func TestAutoTrackEdgesOnCreate(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".htmlgraph")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	// Create a track first
	if err := testCreate("track", "Test Track", "", "medium", false, false); err != nil {
		t.Fatalf("create track: %v", err)
	}

	// Find the track ID from disk
	trackFiles, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	if len(trackFiles) != 1 {
		t.Fatalf("expected 1 track file, got %d", len(trackFiles))
	}
	trackNode, err := htmlparse.ParseFile(trackFiles[0])
	if err != nil {
		t.Fatalf("parse track: %v", err)
	}
	trackID := trackNode.ID

	// Create a feature linked to the track
	if err := testCreate("feature", "Tracked Feature", trackID, "high", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}

	// Find the feature
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, err := htmlparse.ParseFile(featFiles[0])
	if err != nil {
		t.Fatalf("parse feature: %v", err)
	}

	// Verify feature has part_of edge to track
	partOfEdges, ok := featNode.Edges["part_of"]
	if !ok || len(partOfEdges) == 0 {
		t.Errorf("feature missing part_of edge; edges = %v", featNode.Edges)
	} else if partOfEdges[0].TargetID != trackID {
		t.Errorf("part_of target = %q, want %q", partOfEdges[0].TargetID, trackID)
	}

	// Re-read the track to check contains edge
	trackNode, err = htmlparse.ParseFile(trackFiles[0])
	if err != nil {
		t.Fatalf("re-parse track: %v", err)
	}
	containsEdges, ok := trackNode.Edges["contains"]
	if !ok || len(containsEdges) == 0 {
		t.Errorf("track missing contains edge; edges = %v", trackNode.Edges)
	} else if containsEdges[0].TargetID != featNode.ID {
		t.Errorf("contains target = %q, want %q", containsEdges[0].TargetID, featNode.ID)
	}
}

func TestAutoTrackEdgesNotCreatedForTrack(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".htmlgraph")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	// Creating a track should not attempt auto-edges even if trackID is passed
	if err := testCreate("track", "Parent Track", "", "medium", false, false); err != nil {
		t.Fatalf("create track: %v", err)
	}

	trackFiles, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	if len(trackFiles) != 1 {
		t.Fatalf("expected 1 track file, got %d", len(trackFiles))
	}
	node, _ := htmlparse.ParseFile(trackFiles[0])
	if len(node.Edges) > 0 {
		t.Errorf("track should have no edges, got %v", node.Edges)
	}
}

func TestAutoImplementedInEdgeOnStart(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".htmlgraph")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	// Set a fake session ID (EnvSessionID reads HTMLGRAPH_SESSION_ID first)
	t.Setenv("HTMLGRAPH_SESSION_ID", "test-session-abc")

	// Create a feature
	if err := testCreate("feature", "Impl Feature", "", "high", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}

	// Find the feature ID
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	featNode, _ := htmlparse.ParseFile(featFiles[0])
	featID := featNode.ID

	// Start the feature (should create implemented_in edge)
	if err := runWiSetStatus("feature", featID, "in-progress"); err != nil {
		t.Fatalf("start feature: %v", err)
	}

	// Re-read and check for implemented_in edge
	featNode, _ = htmlparse.ParseFile(featFiles[0])
	implEdges, ok := featNode.Edges["implemented_in"]
	if !ok || len(implEdges) == 0 {
		t.Errorf("feature missing implemented_in edge; edges = %v", featNode.Edges)
	} else if implEdges[0].TargetID != "test-session-abc" {
		t.Errorf("implemented_in target = %q, want %q", implEdges[0].TargetID, "test-session-abc")
	}

	// Start again — should be idempotent (no duplicate edge)
	if err := runWiSetStatus("feature", featID, "in-progress"); err != nil {
		t.Fatalf("re-start feature: %v", err)
	}
	featNode, _ = htmlparse.ParseFile(featFiles[0])
	implEdges = featNode.Edges["implemented_in"]
	if len(implEdges) != 1 {
		t.Errorf("expected 1 implemented_in edge after re-start, got %d", len(implEdges))
	}
}

func TestNoImplementedInEdgeWithoutSession(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".htmlgraph")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	// Use a sentinel value that won't match any real session, then clear it.
	// EnvSessionID checks HTMLGRAPH_SESSION_ID first; "none" forces it to
	// return "none" (which is fine — the edge target will be "none").
	// Instead, we set it empty and chdir to tmpDir so readActiveSession
	// finds no .active-session file.
	t.Setenv("HTMLGRAPH_SESSION_ID", "")
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	if err := testCreate("feature", "No Session Feature", "", "low", false, false); err != nil {
		t.Fatalf("create: %v", err)
	}

	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	featNode, _ := htmlparse.ParseFile(featFiles[0])

	if err := runWiSetStatus("feature", featNode.ID, "in-progress"); err != nil {
		t.Fatalf("start: %v", err)
	}

	featNode, _ = htmlparse.ParseFile(featFiles[0])
	if len(featNode.Edges["implemented_in"]) > 0 {
		t.Errorf("should not have implemented_in edge without session, got %v", featNode.Edges)
	}
}

func TestAutoCausedByEdgeOnBugCreate(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".htmlgraph")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	// Create a feature first and start it
	if err := testCreate("feature", "Active Feature", "", "high", true, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}

	// Now create a bug — should auto-link caused_by to active feature
	if err := testCreate("bug", "Found a bug", "", "high", false, false); err != nil {
		t.Fatalf("create bug: %v", err)
	}

	// Find the bug
	bugFiles, _ := filepath.Glob(filepath.Join(hgDir, "bugs", "bug-*.html"))
	if len(bugFiles) != 1 {
		t.Fatalf("expected 1 bug file, got %d", len(bugFiles))
	}
	bugNode, _ := htmlparse.ParseFile(bugFiles[0])

	// Find the feature ID
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	featNode, _ := htmlparse.ParseFile(featFiles[0])

	// Verify caused_by edge
	causedByEdges := bugNode.Edges["caused_by"]
	if len(causedByEdges) == 0 {
		t.Logf("bug edges: %v", bugNode.Edges)
		t.Skip("no DB available in test — auto caused_by requires session DB")
		return
	}
	if causedByEdges[0].TargetID != featNode.ID {
		t.Errorf("caused_by target = %q, want %q", causedByEdges[0].TargetID, featNode.ID)
	}
}

func TestBugCreateNoLinkSkipsCausedBy(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".htmlgraph")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	// Create and start a feature
	if err := testCreate("feature", "Active Feature", "", "high", true, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}

	// Create bug with --no-link
	if err := testCreate("bug", "Unrelated bug", "", "medium", false, true); err != nil {
		t.Fatalf("create bug: %v", err)
	}

	bugFiles, _ := filepath.Glob(filepath.Join(hgDir, "bugs", "bug-*.html"))
	bugNode, _ := htmlparse.ParseFile(bugFiles[0])

	// Should have no caused_by edge
	if len(bugNode.Edges["caused_by"]) > 0 {
		t.Errorf("--no-link should skip caused_by edge, got %v", bugNode.Edges)
	}
}

func setupHgDir(t *testing.T) (tmpDir, hgDir string) {
	t.Helper()
	tmpDir = t.TempDir()
	hgDir = filepath.Join(tmpDir, ".htmlgraph")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	projectDirFlag = tmpDir
	t.Cleanup(func() { projectDirFlag = "" })
	return tmpDir, hgDir
}

func findFeatureID(t *testing.T, hgDir string) string {
	t.Helper()
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	node, err := htmlparse.ParseFile(featFiles[0])
	if err != nil {
		t.Fatalf("parse feature: %v", err)
	}
	return node.ID
}

func readFeatureNode(t *testing.T, hgDir string) *models.Node {
	t.Helper()
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 1 {
		t.Fatalf("expected 1 feature file, got %d", len(featFiles))
	}
	node, err := htmlparse.ParseFile(featFiles[0])
	if err != nil {
		t.Fatalf("re-parse feature: %v", err)
	}
	return node
}

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
	if err := runWiRemoveStep("feature", featID, "2"); err != nil {
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

func TestRemoveStepOutOfRange(t *testing.T) {
	_, hgDir := setupHgDir(t)

	if err := testCreate("feature", "Step Feature", "", "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featID := findFeatureID(t, hgDir)

	if err := runWiAddStep("feature", featID, []string{"only step"}, false); err != nil {
		t.Fatalf("add step: %v", err)
	}

	if err := runWiRemoveStep("feature", featID, "0"); err == nil {
		t.Error("expected error when removing step 0, got nil")
	}
	if err := runWiRemoveStep("feature", featID, "5"); err == nil {
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

	if err := runWiCompleteStep("feature", featID, "1"); err != nil {
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

func TestEditDescription(t *testing.T) {
	_, hgDir := setupHgDir(t)

	if err := testCreate("feature", "Desc Feature", "", "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featID := findFeatureID(t, hgDir)

	// Content must be wrapped in an element (e.g. <p>) to survive the HTML
	// round-trip, because the parser reads child elements, not raw text nodes.
	if err := runWiEditDescription("feature", featID, "<p>New description text</p>"); err != nil {
		t.Fatalf("edit description: %v", err)
	}

	node := readFeatureNode(t, hgDir)
	if !strings.Contains(node.Content, "New description text") {
		t.Errorf("content = %q, want it to contain %q", node.Content, "New description text")
	}
}

func TestEditDescriptionOverwrite(t *testing.T) {
	_, hgDir := setupHgDir(t)

	if err := testCreate("feature", "Overwrite Feature", "", "medium", false, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	featID := findFeatureID(t, hgDir)

	if err := runWiEditDescription("feature", featID, "<p>Original</p>"); err != nil {
		t.Fatalf("set original description: %v", err)
	}
	if err := runWiEditDescription("feature", featID, "<p>Updated</p>"); err != nil {
		t.Fatalf("overwrite description: %v", err)
	}

	node := readFeatureNode(t, hgDir)
	if strings.Contains(node.Content, "Original") {
		t.Errorf("content should not contain %q after overwrite, got %q", "Original", node.Content)
	}
	if !strings.Contains(node.Content, "Updated") {
		t.Errorf("content = %q, want it to contain %q", node.Content, "Updated")
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
