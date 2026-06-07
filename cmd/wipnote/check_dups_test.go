package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
)

// TestCheckDupsSurfacesCluster verifies `wipnote check dups` detects an item
// carrying a relates_to edge tagged needs-triage-dup, reading canonical HTML
// (no SQLite index required).
func TestCheckDupsSurfacesCluster(t *testing.T) {
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
	if err := testCreate("bug", "Original defect", trackID, "medium", false, false); err != nil {
		t.Fatalf("create original: %v", err)
	}
	if err := testCreate("bug", "Duplicate defect", trackID, "medium", false, false); err != nil {
		t.Fatalf("create dup: %v", err)
	}

	bugFiles, _ := filepath.Glob(filepath.Join(hgDir, "bugs", "bug-*.html"))
	if len(bugFiles) < 2 {
		t.Fatalf("expected 2 bug files, got %d", len(bugFiles))
	}
	origNode, _ := htmlparse.ParseFile(bugFiles[0])
	dupNode, _ := htmlparse.ParseFile(bugFiles[1])

	// Manually attach the needs-triage-dup relates_to edge (simulates a
	// strong similarity match at create time).
	p, err := workitem.Open(hgDir, "wipnote-cli")
	if err != nil {
		t.Fatalf("open project: %v", err)
	}
	_, err = p.Bugs.AddEdge(dupNode.ID, models.Edge{
		TargetID:     origNode.ID,
		Relationship: models.RelRelatesTo,
		Title:        needsTriageDupTag + ": " + origNode.ID,
		Properties:   map[string]string{"tag": needsTriageDupTag},
	})
	p.Close()
	if err != nil {
		t.Fatalf("attach dup edge: %v", err)
	}

	if err := runCheckDups(false); err != nil {
		t.Fatalf("runCheckDups returned error: %v", err)
	}

	// Re-parse to confirm the marker round-tripped through canonical HTML.
	reDup, _ := htmlparse.ParseFile(bugFiles[1])
	found := false
	for _, e := range reDup.Edges[string(models.RelRelatesTo)] {
		if e.TargetID == origNode.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("needs-triage-dup relates_to edge did not round-trip to HTML")
	}
}
