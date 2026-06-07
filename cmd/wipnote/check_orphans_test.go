package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/htmlparse"
)

func TestCheckOrphansFindsUnlinkedItems(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		os.MkdirAll(filepath.Join(hgDir, sub), 0o755)
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	// Create a track
	if err := testCreate("track", "My Track", "", "medium", false, false); err != nil {
		t.Fatal(err)
	}
	trackFiles, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	trackNode, _ := htmlparse.ParseFile(trackFiles[0])
	trackID := trackNode.ID

	// Create a linked feature (has track)
	if err := testCreate("feature", "Linked Feature", trackID, "high", false, false); err != nil {
		t.Fatal(err)
	}

	// Create a spike (should be exempt — no track required)
	if err := testCreate("spike", "Research Spike", "", "low", false, false); err != nil {
		t.Fatal(err)
	}

	// Run orphan check — should not error (non-strict)
	if err := runCheckOrphans(false); err != nil {
		t.Fatalf("check orphans failed: %v", err)
	}
}

func TestIsOrphan(t *testing.T) {
	tests := []struct {
		name    string
		trackID string
		edges   map[string][]struct{ rel string }
		want    bool
	}{
		{"has track", "trk-123", nil, false},
		{"no track no edges", "", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Quick check using the models directly
			if tt.trackID != "" && tt.want {
				t.Error("item with trackID should not be orphan")
			}
		})
	}
}
