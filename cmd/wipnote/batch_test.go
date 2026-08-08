package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/workitem"
)

func TestParseBatchSpec(t *testing.T) {
	yaml := `
track:
  title: "Test Track"
  priority: high
  steps:
    - "ARCH: first requirement"
    - "ARCH: second requirement"

features:
  - title: "Feature Alpha"
    priority: high
    steps:
      - "Implement the widget"
      - "Add tests"
  - title: "Feature Beta"
    priority: medium
    blocked_by: ["Feature Alpha"]
    steps:
      - "Wire the API"

links:
  - from: "Feature Beta"
    to: "Feature Alpha"
    rel: relates_to
`
	spec, err := parseBatchSpec([]byte(yaml))
	if err != nil {
		t.Fatalf("parseBatchSpec: %v", err)
	}
	if spec.Track.Title != "Test Track" {
		t.Errorf("track title = %q, want %q", spec.Track.Title, "Test Track")
	}
	if spec.Track.Priority != "high" {
		t.Errorf("track priority = %q, want %q", spec.Track.Priority, "high")
	}
	if len(spec.Track.Steps) != 2 {
		t.Errorf("track steps = %d, want 2", len(spec.Track.Steps))
	}
	if len(spec.Features) != 2 {
		t.Errorf("features = %d, want 2", len(spec.Features))
	}
	if spec.Features[0].Title != "Feature Alpha" {
		t.Errorf("feature[0].title = %q, want %q", spec.Features[0].Title, "Feature Alpha")
	}
	if len(spec.Features[1].BlockedBy) != 1 {
		t.Errorf("feature[1].blocked_by = %d, want 1", len(spec.Features[1].BlockedBy))
	}
	if len(spec.Links) != 1 {
		t.Errorf("links = %d, want 1", len(spec.Links))
	}
	if spec.Links[0].Rel != "relates_to" {
		t.Errorf("link rel = %q, want %q", spec.Links[0].Rel, "relates_to")
	}
}

func TestRunBatchApply(t *testing.T) {
	// Set up a temp .wipnote directory
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	yaml := `
track:
  title: "Integration Track"
  priority: high
  steps:
    - "ARCH: items form hierarchy"
    - "ARCH: dual-write edges"

features:
  - title: "First Feature"
    priority: high
    steps:
      - "Do the thing"
      - "Test it"
  - title: "Second Feature"
    priority: medium
    blocked_by: ["First Feature"]
  - title: "Third Feature"
    priority: low

links:
  - from: "Third Feature"
    to: "First Feature"
    rel: relates_to
`
	// Set project-dir so findWipnoteDir works
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	result, err := executeBatchApply([]byte(yaml), false)
	if err != nil {
		t.Fatalf("executeBatchApply: %v", err)
	}

	// Verify track created
	if result.TrackID == "" {
		t.Fatal("track not created")
	}
	if !strings.HasPrefix(result.TrackID, "trk-") {
		t.Errorf("track ID = %q, want trk- prefix", result.TrackID)
	}

	// Verify 3 features created
	if len(result.FeatureIDs) != 3 {
		t.Fatalf("features created = %d, want 3", len(result.FeatureIDs))
	}
	for _, fid := range result.FeatureIDs {
		if !strings.HasPrefix(fid, "feat-") {
			t.Errorf("feature ID = %q, want feat- prefix", fid)
		}
	}

	// Verify files exist on disk
	trackFiles, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	if len(trackFiles) != 1 {
		t.Errorf("track files = %d, want 1", len(trackFiles))
	}
	featFiles, _ := filepath.Glob(filepath.Join(hgDir, "features", "feat-*.html"))
	if len(featFiles) != 3 {
		t.Errorf("feature files = %d, want 3", len(featFiles))
	}

	// Verify links count
	if result.LinksCreated != 2 { // 1 blocked_by + 1 explicit link
		t.Errorf("links created = %d, want 2", result.LinksCreated)
	}
}

// TestRunBatchApply_BlockedByCarriesBatchApplyOrigin is the bug-f55532ba
// regression check for batch.go: its blocked_by edges must stamp
// graph.EdgeOriginBatchApply into metadata so they are distinguishable from
// a NULL-metadata human `link add` assertion. Unlike plan-slice edges, batch
// blocked_by edges represent a genuine intended dependency (the spec author
// wrote it deliberately) so they must NOT match graph.EdgeOriginPlanSlice —
// FindBottlenecks must keep counting them.
func TestRunBatchApply_BlockedByCarriesBatchApplyOrigin(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	yaml := `
track:
  title: "Origin Track"
  priority: high

features:
  - title: "First Feature"
    priority: high
  - title: "Second Feature"
    priority: medium
    blocked_by: ["First Feature"]
`
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	result, err := executeBatchApply([]byte(yaml), false)
	if err != nil {
		t.Fatalf("executeBatchApply: %v", err)
	}
	if len(result.FeatureIDs) != 2 {
		t.Fatalf("features created = %d, want 2", len(result.FeatureIDs))
	}
	firstID, secondID := result.FeatureIDs[0], result.FeatureIDs[1]

	p, err := workitem.Open(hgDir, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	meta := edgeMetadata(t, p.DB, secondID, firstID, "blocked_by")
	if meta == nil {
		t.Fatalf("expected metadata on batch-apply blocked_by edge, got none")
	}
	if meta["origin"] != graph.EdgeOriginBatchApply {
		t.Errorf("origin = %q, want %q", meta["origin"], graph.EdgeOriginBatchApply)
	}
	if meta["origin"] == graph.EdgeOriginPlanSlice {
		t.Errorf("batch-apply edge must not carry the plan-slice origin tag")
	}
}

func TestRunBatchApplyDryRun(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}

	yaml := `
track:
  title: "Dry Run Track"
  priority: medium

features:
  - title: "Dry Run Feature"
    priority: low
`
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	result, err := executeBatchApply([]byte(yaml), true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}

	// Dry run should report what would be created
	if result.TrackID != "[dry-run]" {
		t.Errorf("dry run track = %q, want %q", result.TrackID, "[dry-run]")
	}
	if len(result.FeatureIDs) != 1 {
		t.Errorf("dry run features = %d, want 1", len(result.FeatureIDs))
	}

	// No files should exist
	trackFiles, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	if len(trackFiles) != 0 {
		t.Errorf("dry run created %d track files, want 0", len(trackFiles))
	}
}

func TestParseBatchSpecNoTrack(t *testing.T) {
	yaml := `
features:
  - title: "Standalone Feature"
    priority: high
    steps:
      - "Just do it"
`
	spec, err := parseBatchSpec([]byte(yaml))
	if err != nil {
		t.Fatalf("parseBatchSpec: %v", err)
	}
	if spec.Track.Title != "" {
		t.Errorf("track title = %q, want empty", spec.Track.Title)
	}
	if len(spec.Features) != 1 {
		t.Errorf("features = %d, want 1", len(spec.Features))
	}
}

func TestParseBatchSpecInvalid(t *testing.T) {
	_, err := parseBatchSpec([]byte("not: [valid: yaml: {{"))
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestBatchApplyRejectsOrphanFeatures(t *testing.T) {
	yamlData := `
features:
  - title: "Orphan Feature"
    priority: high
`
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	_, err := executeBatchApply([]byte(yamlData), false)
	if err == nil {
		t.Fatal("expected error for features without track")
	}
	if !strings.Contains(err.Error(), "track") {
		t.Errorf("error should mention track, got: %v", err)
	}
}

func TestBatchApplyRejectsOrphanFeaturesDryRun(t *testing.T) {
	yamlData := `
features:
  - title: "Orphan Feature"
    priority: high
`
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}
	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	_, err := executeBatchApply([]byte(yamlData), true)
	if err == nil {
		t.Fatal("expected error for features without track (dry-run)")
	}
	if !strings.Contains(err.Error(), "track") {
		t.Errorf("error should mention track, got: %v", err)
	}
}
