package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupExecutePreviewDir creates a temp .htmlgraph dir with:
//   - 1 track
//   - 2 linked bugs
//   - 1 plan linked to the track
//
// Returns (tmpDir, hgDir, trackID).
func setupExecutePreviewDir(t *testing.T) (string, string, string) {
	t.Helper()

	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".htmlgraph")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	projectDirFlag = tmpDir
	t.Cleanup(func() { projectDirFlag = "" })

	// Create a track.
	if err := testCreate("track", "Execute Preview Track", "", "high", false, false); err != nil {
		t.Fatalf("create track: %v", err)
	}
	trackFiles, _ := filepath.Glob(filepath.Join(hgDir, "tracks", "trk-*.html"))
	if len(trackFiles) == 0 {
		t.Fatal("no track file created")
	}
	trackID := parseHTMLForIDMust(t, trackFiles[0])

	// Create bug 1 linked to the track.
	if err := runWiCreate("bug", "Execute Preview Bug One", &wiCreateOpts{
		trackID:     trackID,
		priority:    "high",
		description: "first bug for execute-preview test",
		start:       false,
		noLink:      false,
	}); err != nil {
		t.Fatalf("create bug 1: %v", err)
	}

	// Create bug 2 linked to the track.
	if err := runWiCreate("bug", "Execute Preview Bug Two", &wiCreateOpts{
		trackID:     trackID,
		priority:    "medium",
		description: "second bug for execute-preview test",
		start:       false,
		noLink:      false,
	}); err != nil {
		t.Fatalf("create bug 2: %v", err)
	}

	// Create a plan linked to the track.
	if err := runWiCreate("plan", "Execute Preview Plan", &wiCreateOpts{
		trackID:     trackID,
		priority:    "medium",
		description: "plan for execute-preview test",
		start:       false,
		noLink:      false,
	}); err != nil {
		t.Fatalf("create plan: %v", err)
	}

	return tmpDir, hgDir, trackID
}

// TestExecutePreview_FormatJSON verifies that execute-preview <trk-id> --format json
// produces valid JSON with the required top-level keys.
func TestExecutePreview_FormatJSON(t *testing.T) {
	_, _, trackID := setupExecutePreviewDir(t)

	out, err := captureShowOutput(t, func() error {
		return runExecutePreview(trackID, "json")
	})
	if err != nil {
		t.Fatalf("runExecutePreview(%q, json): %v", trackID, err)
	}

	// Must parse as JSON.
	var env map[string]any
	if jsonErr := json.Unmarshal([]byte(out), &env); jsonErr != nil {
		t.Fatalf("output is not valid JSON: %v\noutput:\n%s", jsonErr, out)
	}

	// Must contain all required top-level keys.
	for _, key := range []string{"track", "features", "bugs", "plans", "git"} {
		if _, ok := env[key]; !ok {
			t.Errorf("JSON missing required key %q; keys present: %v", key, jsonKeys(env))
		}
	}
}

// TestExecutePreview_TrackFields verifies that the track section contains expected fields.
func TestExecutePreview_TrackFields(t *testing.T) {
	_, _, trackID := setupExecutePreviewDir(t)

	out, err := captureShowOutput(t, func() error {
		return runExecutePreview(trackID, "json")
	})
	if err != nil {
		t.Fatalf("runExecutePreview: %v", err)
	}

	var env map[string]any
	_ = json.Unmarshal([]byte(out), &env)

	track, ok := env["track"].(map[string]any)
	if !ok {
		t.Fatalf("track field is not an object")
	}

	for _, f := range []string{"id", "title", "status"} {
		if _, ok := track[f]; !ok {
			t.Errorf("track missing field %q", f)
		}
	}

	if track["id"] != trackID {
		t.Errorf("track.id = %v, want %q", track["id"], trackID)
	}
}

// TestExecutePreview_BugsPresent verifies at least one bug appears in the bugs array.
func TestExecutePreview_BugsPresent(t *testing.T) {
	_, _, trackID := setupExecutePreviewDir(t)

	out, err := captureShowOutput(t, func() error {
		return runExecutePreview(trackID, "json")
	})
	if err != nil {
		t.Fatalf("runExecutePreview: %v", err)
	}

	var env map[string]any
	_ = json.Unmarshal([]byte(out), &env)

	bugsRaw, ok := env["bugs"]
	if !ok {
		t.Fatal("bugs key missing from output")
	}
	bugs, ok := bugsRaw.([]any)
	if !ok {
		t.Fatalf("bugs is not an array, got %T", bugsRaw)
	}
	if len(bugs) < 1 {
		t.Errorf("expected at least 1 bug, got %d", len(bugs))
	}

	// Each bug should have id, title, status.
	for i, bugRaw := range bugs {
		bug, ok := bugRaw.(map[string]any)
		if !ok {
			t.Errorf("bugs[%d] is not an object", i)
			continue
		}
		for _, f := range []string{"id", "title", "status"} {
			if _, ok := bug[f]; !ok {
				t.Errorf("bugs[%d] missing field %q", i, f)
			}
		}
	}
}

// TestExecutePreview_GitKeys verifies the git section contains expected fields.
func TestExecutePreview_GitKeys(t *testing.T) {
	_, _, trackID := setupExecutePreviewDir(t)

	out, err := captureShowOutput(t, func() error {
		return runExecutePreview(trackID, "json")
	})
	if err != nil {
		t.Fatalf("runExecutePreview: %v", err)
	}

	var env map[string]any
	_ = json.Unmarshal([]byte(out), &env)

	git, ok := env["git"].(map[string]any)
	if !ok {
		t.Fatalf("git field is not an object")
	}

	for _, f := range []string{"branch", "commits_ahead_main", "commits_behind_main", "head_sha"} {
		if _, ok := git[f]; !ok {
			t.Errorf("git missing field %q", f)
		}
	}
}

// TestExecutePreview_FormatText verifies that --format text produces non-JSON output.
func TestExecutePreview_FormatText(t *testing.T) {
	_, _, trackID := setupExecutePreviewDir(t)

	out, err := captureShowOutput(t, func() error {
		return runExecutePreview(trackID, "text")
	})
	if err != nil {
		t.Fatalf("runExecutePreview(text): %v", err)
	}

	var m map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &m) == nil {
		t.Error("expected human text output, but got valid JSON object")
	}

	if !strings.Contains(out, trackID) {
		t.Errorf("text output does not contain track ID %q:\n%s", trackID, out)
	}
}

// TestExecutePreview_TwoBugs verifies that both bugs appear when two are linked.
func TestExecutePreview_TwoBugs(t *testing.T) {
	_, _, trackID := setupExecutePreviewDir(t)

	out, err := captureShowOutput(t, func() error {
		return runExecutePreview(trackID, "json")
	})
	if err != nil {
		t.Fatalf("runExecutePreview: %v", err)
	}

	var env map[string]any
	_ = json.Unmarshal([]byte(out), &env)

	bugs, _ := env["bugs"].([]any)
	if len(bugs) < 2 {
		t.Errorf("expected 2 bugs, got %d", len(bugs))
	}
}
