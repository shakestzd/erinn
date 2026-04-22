package worktree_test

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/htmlgraph/internal/worktree"
)

// setupGitRepo creates a temp git repo with an initial commit and returns its path.
func setupGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %s", args, out)
		}
	}

	f, err := os.Create(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("create README: %v", err)
	}
	f.WriteString("# Test")
	f.Close()

	exec.Command("git", "-C", dir, "add", ".").Run() //nolint:errcheck
	cmd := exec.Command("git", "-C", dir, "commit", "-m", "initial")
	cmd.Dir = dir
	cmd.Run() //nolint:errcheck

	return dir
}

// writeFeatureHTML writes a minimal feature HTML file. If trackID is empty, data-track-id is omitted.
func writeFeatureHTML(t *testing.T, dir, featureID, trackID string) {
	t.Helper()
	featureDir := filepath.Join(dir, ".htmlgraph", "features")
	if err := os.MkdirAll(featureDir, 0755); err != nil {
		t.Fatalf("mkdir features: %v", err)
	}
	trackAttr := ""
	if trackID != "" {
		trackAttr = ` data-track-id="` + trackID + `"`
	}
	html := `<article id="` + featureID + `"` + trackAttr + ` data-status="todo">` +
		`<header><h1>Test Feature</h1></header>` +
		`<section data-content><p>Description</p></section>` +
		`</article>`
	path := filepath.Join(featureDir, featureID+".html")
	if err := os.WriteFile(path, []byte(html), 0644); err != nil {
		t.Fatalf("write feature HTML: %v", err)
	}
}

// TestEnsureForFeatureIdempotent verifies that the first call creates the worktree
// and the second call returns the same path without re-creating.
func TestEnsureForFeatureIdempotent(t *testing.T) {
	dir := setupGitRepo(t)
	writeFeatureHTML(t, dir, "feat-aaa", "")

	path1, err := worktree.EnsureForFeature("feat-aaa", dir, io.Discard)
	if err != nil {
		t.Fatalf("first EnsureForFeature: %v", err)
	}

	expected := filepath.Join(dir, ".claude", "worktrees", "feat-aaa")
	if path1 != expected {
		t.Errorf("path: got %q, want %q", path1, expected)
	}
	if _, err := os.Stat(path1); err != nil {
		t.Errorf("worktree dir does not exist: %v", err)
	}

	// Second call is idempotent.
	path2, err := worktree.EnsureForFeature("feat-aaa", dir, io.Discard)
	if err != nil {
		t.Fatalf("second EnsureForFeature: %v", err)
	}
	if path1 != path2 {
		t.Errorf("paths differ on second call: %q vs %q", path1, path2)
	}
}

// TestEnsureForFeatureResolvesParentTrack verifies that when a feature has a parent track,
// EnsureForFeature returns the track worktree path, not the feature path.
func TestEnsureForFeatureResolvesParentTrack(t *testing.T) {
	dir := setupGitRepo(t)
	writeFeatureHTML(t, dir, "feat-ccc", "trk-parent111")

	path, err := worktree.EnsureForFeature("feat-ccc", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForFeature: %v", err)
	}

	expectedTrackPath := filepath.Join(dir, ".claude", "worktrees", "trk-parent111")
	if path != expectedTrackPath {
		t.Errorf("path: got %q, want track path %q", path, expectedTrackPath)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("track worktree dir does not exist: %v", err)
	}
	// Feature worktree should NOT exist.
	featurePath := filepath.Join(dir, ".claude", "worktrees", "feat-ccc")
	if _, err := os.Stat(featurePath); err == nil {
		t.Error("feature worktree should NOT exist when feature has a parent track")
	}
}

// TestEnsureForTrackIdempotent verifies that repeated calls to EnsureForTrack return the same path.
func TestEnsureForTrackIdempotent(t *testing.T) {
	dir := setupGitRepo(t)

	path1, err := worktree.EnsureForTrack("trk-ttt111", dir, io.Discard)
	if err != nil {
		t.Fatalf("first EnsureForTrack: %v", err)
	}

	path2, err := worktree.EnsureForTrack("trk-ttt111", dir, io.Discard)
	if err != nil {
		t.Fatalf("second EnsureForTrack: %v", err)
	}

	if path1 != path2 {
		t.Errorf("paths differ: %q vs %q", path1, path2)
	}

	expected := filepath.Join(dir, ".claude", "worktrees", "trk-ttt111")
	if path1 != expected {
		t.Errorf("path: got %q, want %q", path1, expected)
	}
}

// TestEnsureForAgentSignature verifies EnsureForAgent signature, naming convention,
// and that it creates the expected worktree path.
func TestEnsureForAgentSignature(t *testing.T) {
	dir := setupGitRepo(t)

	// Create the track branch that the agent will branch from.
	exec.Command("git", "-C", dir, "branch", "trk-agent111").Run() //nolint:errcheck

	path, err := worktree.EnsureForAgent("trk-agent111", "slice-3", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForAgent: %v", err)
	}

	expected := filepath.Join(dir, ".claude", "worktrees", "trk-agent111", "agent-slice-3")
	if path != expected {
		t.Errorf("path: got %q, want %q", path, expected)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("agent worktree dir does not exist: %v", err)
	}
}

// TestProgressWriterDiscardQuiet verifies that passing io.Discard causes no stdout leakage.
func TestProgressWriterDiscardQuiet(t *testing.T) {
	dir := setupGitRepo(t)
	writeFeatureHTML(t, dir, "feat-quiet", "")

	// Capture stdout via pipe.
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	_, errF := worktree.EnsureForFeature("feat-quiet", dir, io.Discard)
	_, errT := worktree.EnsureForTrack("trk-quiet", dir, io.Discard)

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	buf.ReadFrom(r)

	if errF != nil {
		t.Fatalf("EnsureForFeature: %v", errF)
	}
	if errT != nil {
		t.Fatalf("EnsureForTrack: %v", errT)
	}

	if buf.Len() > 0 {
		t.Errorf("expected no stdout output when using io.Discard; got: %q", buf.String())
	}
}

// TestEnsureForFeatureWriterReceivesProgress verifies that progress is written to the writer.
func TestEnsureForFeatureWriterReceivesProgress(t *testing.T) {
	dir := setupGitRepo(t)
	writeFeatureHTML(t, dir, "feat-progress", "")

	var buf bytes.Buffer
	_, err := worktree.EnsureForFeature("feat-progress", dir, &buf)
	if err != nil {
		t.Fatalf("EnsureForFeature: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "feat-progress") {
		t.Errorf("expected writer to receive progress containing feat-progress; got: %q", output)
	}
}

// TestRepairGitdirStalePath verifies that RepairGitdir rewrites a stale cross-machine
// gitdir path to the correct absolute path based on the current repo root.
func TestRepairGitdirStalePath(t *testing.T) {
	dir := setupGitRepo(t)

	// Create a real worktree so the gitdir target exists.
	worktreePath, err := worktree.EnsureForTrack("trk-repair111", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForTrack: %v", err)
	}

	// Determine the correct gitdir before corrupting the .git file.
	gitFile := filepath.Join(worktreePath, ".git")
	correctContent, err := os.ReadFile(gitFile)
	if err != nil {
		t.Fatalf("read .git file: %v", err)
	}
	correctGitdir := strings.TrimPrefix(strings.TrimSpace(string(correctContent)), "gitdir: ")

	// Overwrite .git with a bogus cross-machine path.
	bogusContent := "gitdir: /nonexistent/cross-machine/path/.git/worktrees/trk-repair111\n"
	if err := os.WriteFile(gitFile, []byte(bogusContent), 0644); err != nil {
		t.Fatalf("write bogus .git: %v", err)
	}

	// Call RepairGitdir — it should detect the stale path and rewrite it.
	repaired, err := worktree.RepairGitdir(worktreePath, dir)
	if err != nil {
		t.Fatalf("RepairGitdir: %v", err)
	}
	if !repaired {
		t.Error("RepairGitdir: expected repaired=true for bogus path, got false")
	}

	// Verify the .git file now contains the correct gitdir.
	got, err := os.ReadFile(gitFile)
	if err != nil {
		t.Fatalf("read .git after repair: %v", err)
	}
	gotGitdir := strings.TrimPrefix(strings.TrimSpace(string(got)), "gitdir: ")
	if gotGitdir != correctGitdir {
		t.Errorf("gitdir after repair: got %q, want %q", gotGitdir, correctGitdir)
	}
}

// TestRepairGitdirValidPath verifies that RepairGitdir returns repaired=false
// when the existing gitdir is already valid (no-op case).
func TestRepairGitdirValidPath(t *testing.T) {
	dir := setupGitRepo(t)

	worktreePath, err := worktree.EnsureForTrack("trk-valid111", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForTrack: %v", err)
	}

	// The gitdir written by git worktree add should already be valid.
	repaired, err := worktree.RepairGitdir(worktreePath, dir)
	if err != nil {
		t.Fatalf("RepairGitdir: %v", err)
	}
	if repaired {
		t.Error("RepairGitdir: expected repaired=false for valid path, got true")
	}
}

// TestRepairGitdirNotAWorktree verifies that RepairGitdir handles a plain directory
// (no .git file) gracefully — no error, no repair.
func TestRepairGitdirNotAWorktree(t *testing.T) {
	dir := t.TempDir() // plain directory, no .git file

	repaired, err := worktree.RepairGitdir(dir, dir)
	if err != nil {
		t.Fatalf("RepairGitdir on plain dir: %v", err)
	}
	if repaired {
		t.Error("RepairGitdir: expected repaired=false for non-worktree dir, got true")
	}
}
