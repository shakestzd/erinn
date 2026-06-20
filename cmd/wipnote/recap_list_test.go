package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecapListShowDelete exercises the headless list/show/delete subcommands
// end to end via the built binary: machine-readable JSON output and exit codes.
func TestRecapListShowDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary-spawning test in -short mode")
	}

	bin := buildRecapBinary(t)
	repo := initFixtureRepo(t)
	home := t.TempDir()
	featureID := "feat-listshowdel"

	run := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"WIPNOTE_PROJECT_DIR="+repo,
			"HOME="+home,
		)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// Generate a recap so there is something to list.
	if out, err := run("recap", featureID); err != nil {
		t.Logf("recap generate (err=%v): %s", err, out)
	}
	recapID := "recap-" + featureID

	// list --format json must succeed and contain the recap id.
	listOut, err := run("recap", "list", "--format", "json")
	if err != nil {
		t.Fatalf("recap list failed (err=%v): %s", err, listOut)
	}
	var listed []map[string]any
	jsonStart := strings.Index(listOut, "[")
	if jsonStart < 0 {
		t.Fatalf("no JSON array in list output: %s", listOut)
	}
	if err := json.Unmarshal([]byte(listOut[jsonStart:]), &listed); err != nil {
		t.Fatalf("unmarshal list json: %v\noutput: %s", err, listOut)
	}
	found := false
	for _, r := range listed {
		if r["id"] == recapID {
			found = true
		}
	}
	if !found {
		t.Fatalf("recap %q not in list output: %s", recapID, listOut)
	}

	// show <id> --format json must succeed for a known id.
	showOut, err := run("recap", "show", recapID, "--format", "json")
	if err != nil {
		t.Fatalf("recap show failed (err=%v): %s", err, showOut)
	}
	if !strings.Contains(showOut, recapID) {
		t.Fatalf("show output missing id: %s", showOut)
	}

	// show on an unknown id must exit non-zero.
	if out, err := run("recap", "show", "recap-feat-nonexistent"); err == nil {
		t.Fatalf("expected non-zero exit for unknown recap show, got: %s", out)
	}

	// delete <id> must succeed and remove the artifact.
	if out, err := run("recap", "delete", recapID); err != nil {
		t.Fatalf("recap delete failed (err=%v): %s", err, out)
	}
	artifactPath := filepath.Join(repo, ".wipnote", "recaps", recapID+".html")
	if _, statErr := os.Stat(artifactPath); !os.IsNotExist(statErr) {
		t.Fatalf("artifact still present after delete: %v", statErr)
	}

	// delete on a now-unknown id must exit non-zero.
	if out, err := run("recap", "delete", recapID); err == nil {
		t.Fatalf("expected non-zero exit for second delete, got: %s", out)
	}

	// traversal/separator ids must be rejected before any filesystem delete.
	if out, err := run("recap", "delete", "../features/"+featureID); err == nil {
		t.Fatalf("expected non-zero exit for traversal recap delete, got: %s", out)
	}
}
