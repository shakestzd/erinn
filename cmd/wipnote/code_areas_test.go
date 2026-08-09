package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/internal/blame"
)

// gitInitWithFiles makes root a git repo with the given files committed, so
// blame.WalkAreas (which drives the inventory off `git ls-files`) sees them.
func gitInitWithFiles(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "add", "--"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "seed"},
	} {
		if args[len(args)-1] == "--" {
			args = append(args, files...)
		}
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// setupAreasTestDB creates a temp .wipnote dir with a populated SQLite DB.
// Returns the wipnote dir path and a cleanup function.
func setupAreasTestDB(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	if err := os.MkdirAll(hgDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	return hgDir
}

// areasSetup populates a full test environment: temp dir tree + populated DB.
// Returns root dir (source files) and hgDir (.wipnote).
func areasSetup(t *testing.T) (root, hgDir string) {
	t.Helper()
	hgDir = setupAreasTestDB(t)
	root = filepath.Dir(hgDir)

	// Write some source files and commit them so git ls-files sees them.
	files := []string{"alpha.go", "beta.go", "gamma.go"}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package main"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	gitInitWithFiles(t, root, files...)

	d, err := db.Open(filepath.Join(hgDir, "wipnote.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	now := time.Now().UTC()
	insertT := func(id, title string) {
		if err := db.UpsertTrack(d, &db.Track{
			ID: id, Type: "track", Title: title,
			Status: "active", Priority: "medium",
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("upsert track %s: %v", id, err)
		}
	}
	insertF := func(id, title, trackID string) {
		if err := db.UpsertFeature(d, &db.Feature{
			ID: id, Type: "feature", Title: title,
			Status: "active", Priority: "medium", TrackID: trackID,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("upsert feature %s: %v", id, err)
		}
	}
	insertFF := func(id, featureID, path string) {
		if err := db.UpsertFeatureFile(d, &models.FeatureFile{
			ID: id, FeatureID: featureID, FilePath: path, Operation: "edit",
		}); err != nil {
			t.Fatalf("upsert feature_file %s: %v", id, err)
		}
	}

	insertT("trk-ca-1", "Track One")
	insertT("trk-ca-2", "Track Two")
	insertF("feat-ca-1", "Feature One", "trk-ca-1")
	insertF("feat-ca-2", "Feature Two", "trk-ca-2")
	insertFF("ff-ca-1", "feat-ca-1", "alpha.go")
	insertFF("ff-ca-2", "feat-ca-2", "beta.go")
	// gamma.go: untracked

	return root, hgDir
}

// captureCodeAreas runs formatAreasText / formatAreasMarkdown directly via WalkAreas.
// This avoids the DB-open path in runCodeAreas (which relies on findWipnoteDir).
func captureCodeAreas(t *testing.T, hgDir, rootDir, format string, byFile, includeUntracked bool) string {
	t.Helper()
	d, err := db.Open(filepath.Join(hgDir, "wipnote.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	inc := includeUntracked
	res, err := blame.WalkAreas(context.Background(), d, rootDir, blame.WalkOptions{
		ByFile:           byFile,
		IncludeUntracked: &inc,
	})
	if err != nil {
		t.Fatalf("WalkAreas: %v", err)
	}

	switch format {
	case "json":
		data, _ := json.MarshalIndent(res, "", "  ")
		return string(data)
	case "markdown":
		return formatAreasMarkdown(res, byFile)
	default:
		return formatAreasText(res, byFile)
	}
}

func TestCodeAreas_ByTrack_Grouping(t *testing.T) {
	root, hgDir := areasSetup(t)
	out := captureCodeAreas(t, hgDir, root, "text", false, true)

	if !strings.Contains(out, "Track One") {
		t.Errorf("expected 'Track One' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Track Two") {
		t.Errorf("expected 'Track Two' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "alpha.go") {
		t.Errorf("expected 'alpha.go' in output, got:\n%s", out)
	}
}

func TestCodeAreas_ByFile_Inverse(t *testing.T) {
	root, hgDir := areasSetup(t)
	out := captureCodeAreas(t, hgDir, root, "text", true, false)

	if !strings.Contains(out, "alpha.go") {
		t.Errorf("expected 'alpha.go' in by-file output, got:\n%s", out)
	}
	if !strings.Contains(out, "beta.go") {
		t.Errorf("expected 'beta.go' in by-file output, got:\n%s", out)
	}
}

func TestCodeAreas_UntrackedDetection(t *testing.T) {
	root, hgDir := areasSetup(t)
	out := captureCodeAreas(t, hgDir, root, "text", false, true)

	if !strings.Contains(out, "Untracked") {
		t.Errorf("expected 'Untracked' in output, got:\n%s", out)
	}
}

func TestCodeAreas_FormatJSON(t *testing.T) {
	root, hgDir := areasSetup(t)
	out := captureCodeAreas(t, hgDir, root, "json", false, true)

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("JSON output not parseable: %v\nOutput:\n%s", err, out)
	}
	if _, ok := parsed["by_track"]; !ok {
		t.Error("JSON missing 'by_track' key")
	}
}

func TestCodeAreas_FormatMarkdown(t *testing.T) {
	root, hgDir := areasSetup(t)
	out := captureCodeAreas(t, hgDir, root, "markdown", false, true)

	if !strings.Contains(out, "# Code Areas") {
		t.Errorf("markdown missing '# Code Areas', got:\n%s", out)
	}
	if !strings.Contains(out, "## Track:") {
		t.Errorf("markdown missing '## Track:' section, got:\n%s", out)
	}
	if !strings.Contains(out, "## Untracked") {
		t.Errorf("markdown missing '## Untracked' section, got:\n%s", out)
	}
}

func TestCodeAreas_FormatMarkdown_ByFile(t *testing.T) {
	root, hgDir := areasSetup(t)
	out := captureCodeAreas(t, hgDir, root, "markdown", true, false)

	if !strings.Contains(out, "# Code Areas") {
		t.Errorf("markdown missing '# Code Areas', got:\n%s", out)
	}
	if !strings.Contains(out, "## Files") {
		t.Errorf("markdown missing '## Files' section, got:\n%s", out)
	}
}

func TestCodeAreas_UntrackedExcluded(t *testing.T) {
	root, hgDir := areasSetup(t)
	out := captureCodeAreas(t, hgDir, root, "text", false, false)

	// gamma.go is untracked; should not appear when includeUntracked=false.
	if strings.Contains(out, "gamma.go") {
		t.Errorf("gamma.go should not appear when include-untracked=false, got:\n%s", out)
	}
}

// TestCodeAreas_CmdRegistered verifies the code-areas command is in the cobra root.
func TestCodeAreas_CmdRegistered(t *testing.T) {
	root := buildRoot()
	found := false
	for _, sub := range root.Commands() {
		if sub.Name() == "code-areas" {
			found = true
			break
		}
	}
	if !found {
		t.Error("code-areas command not registered in buildRoot()")
	}
}
