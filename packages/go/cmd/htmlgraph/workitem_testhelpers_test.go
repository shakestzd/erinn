package main

import (
	"os"
	"path/filepath"
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
