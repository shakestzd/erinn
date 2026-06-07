package worktree_test

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/worktree"
)

// setupGitRepo creates a temp git repo with an initial commit and returns its path.
// (The post-creation reindex subprocess is auto-skipped under go test via
// isGoTestBinary in worktree.go — no explicit env-var setup needed here.)
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
	featureDir := filepath.Join(dir, ".wipnote", "features")
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

// writeTrackHTML writes a minimal track HTML file with the given title.
func writeTrackHTML(t *testing.T, dir, trackID, title string) {
	t.Helper()
	trackDir := filepath.Join(dir, ".wipnote", "tracks")
	if err := os.MkdirAll(trackDir, 0755); err != nil {
		t.Fatalf("mkdir tracks: %v", err)
	}
	html := `<article id="` + trackID + `" data-status="todo">` +
		`<header><h1>` + title + `</h1></header>` +
		`<section data-content><p>Description</p></section>` +
		`</article>`
	path := filepath.Join(trackDir, trackID+".html")
	if err := os.WriteFile(path, []byte(html), 0644); err != nil {
		t.Fatalf("write track HTML: %v", err)
	}
}

// TestEnsureForTrackTitled_TitleRenameStability verifies that renaming a track's title
// does not cause EnsureForTrackTitled to compute a new worktree path (which would fail
// with "branch already checked out"). The original path is reused regardless of title changes.
func TestEnsureForTrackTitled_TitleRenameStability(t *testing.T) {
	dir := setupGitRepo(t)

	// First call: create worktree with title-1.
	path1, err := worktree.EnsureForTrackTitled("My Feature Track", "trk-stable01", dir, io.Discard)
	if err != nil {
		t.Fatalf("first EnsureForTrackTitled: %v", err)
	}
	if _, err := os.Stat(path1); err != nil {
		t.Fatalf("worktree should exist at %s: %v", path1, err)
	}

	// Simulate a title rename: call again with a different title.
	// This must return the original path, not a new one derived from "Updated Title".
	path2, err := worktree.EnsureForTrackTitled("Updated Title", "trk-stable01", dir, io.Discard)
	if err != nil {
		t.Fatalf("second EnsureForTrackTitled after rename: %v", err)
	}

	if path1 != path2 {
		t.Errorf("title rename caused path change: first=%q second=%q", path1, path2)
	}

	// The new titled path (with updated title slug) must NOT have been created.
	newPath := filepath.Join(dir, ".claude", "worktrees", "updated-title-trk-stable01")
	if _, err := os.Stat(newPath); err == nil {
		t.Errorf("new titled path should NOT be created after title rename: %s", newPath)
	}
}

// TestEnsureForTrackTitled_TitleRenameIdempotent verifies that renaming a track
// title after an EnsureForTrackTitled call does not change the returned path.
// Path stability matters more than label freshness — once created, the directory
// name should not change just because the title was edited.
func TestEnsureForTrackTitled_TitleRenameIdempotent(t *testing.T) {
	dir := setupGitRepo(t)

	// Create worktree with title-1.
	path1, err := worktree.EnsureForTrackTitled("My Original Title", "trk-rename01", dir, io.Discard)
	if err != nil {
		t.Fatalf("first EnsureForTrackTitled: %v", err)
	}
	if _, err := os.Stat(path1); err != nil {
		t.Fatalf("worktree should exist at %s: %v", path1, err)
	}

	// Simulate a title rename by calling again with a different title.
	path2, err := worktree.EnsureForTrackTitled("Updated Title After Rename", "trk-rename01", dir, io.Discard)
	if err != nil {
		t.Fatalf("second EnsureForTrackTitled after rename: %v", err)
	}

	if path1 != path2 {
		t.Errorf("title rename must not change worktree path: before=%q after=%q", path1, path2)
	}
}

// TestEnsureForTrackTitled_BranchExistsWithoutWorktree is the regression test for
// bug-92690d5b: a prior `wipnote yolo --track <id>` left the branch behind but
// the worktree directory was removed (or the path was deleted manually). A second
// invocation must succeed by attaching to the existing branch instead of failing
// with `fatal: a branch named '<id>' already exists`.
func TestEnsureForTrackTitled_BranchExistsWithoutWorktree(t *testing.T) {
	dir := setupGitRepo(t)

	// First call creates branch + titled worktree.
	path1, err := worktree.EnsureForTrackTitled("Re-run Track", "trk-rerun01", dir, io.Discard)
	if err != nil {
		t.Fatalf("first EnsureForTrackTitled: %v", err)
	}

	// Properly remove the worktree (branch persists, worktree registration is
	// deleted along with the directory). This is the steady-state shape of the
	// bug — a prior session torn down its worktree but the branch sticks around.
	if out, err := exec.Command("git", "-C", dir, "worktree", "remove", "--force", path1).CombinedOutput(); err != nil {
		t.Fatalf("git worktree remove: %v: %s", err, out)
	}

	// Sanity: the directory is gone but the branch survives.
	if _, err := os.Stat(path1); !os.IsNotExist(err) {
		t.Fatalf("worktree dir should be gone, got err=%v", err)
	}
	if err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "refs/heads/trk-rerun01").Run(); err != nil {
		t.Fatalf("branch should still exist after worktree remove: %v", err)
	}

	// Second call must succeed by attaching to the existing branch.
	path2, err := worktree.EnsureForTrackTitled("Re-run Track", "trk-rerun01", dir, io.Discard)
	if err != nil {
		t.Fatalf("second EnsureForTrackTitled (regression for bug-92690d5b): %v", err)
	}
	if _, err := os.Stat(path2); err != nil {
		t.Errorf("second-run worktree should exist at %s: %v", path2, err)
	}

	// Confirm the worktree is now checked out on the original branch.
	out, err := exec.Command("git", "-C", path2, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD in re-attached worktree: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "trk-rerun01" {
		t.Errorf("re-attached worktree HEAD: got %q, want %q", got, "trk-rerun01")
	}
}

// TestEnsureForTrackTitled_BranchCheckedOutInMainRepo guards against silently
// reusing a non-isolated checkout (review finding from job 162). When the track
// branch happens to be checked out in the main repo (or any path outside
// .claude/worktrees/), EnsureForTrackTitled must refuse to attach rather than
// running yolo against the user's primary working tree.
func TestEnsureForTrackTitled_BranchCheckedOutInMainRepo(t *testing.T) {
	dir := setupGitRepo(t)

	// Create the track branch and check it out in the MAIN repo — simulating
	// `git checkout trk-X` outside any managed worktree.
	if out, err := exec.Command("git", "-C", dir, "checkout", "-b", "trk-mainrepo01").CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b: %v: %s", err, out)
	}

	// EnsureForTrackTitled must NOT reuse the main repo path.
	_, err := worktree.EnsureForTrackTitled("Main Repo Track", "trk-mainrepo01", dir, io.Discard)
	if err == nil {
		t.Fatalf("expected error when branch is checked out in main repo, got nil")
	}
	if !strings.Contains(err.Error(), "already checked out") {
		t.Errorf("expected 'already checked out' in error, got: %v", err)
	}

	// And the managed worktree directory must NOT have been created.
	managedPath := filepath.Join(dir, ".claude", "worktrees", "main-repo-track-trk-mainrepo01")
	if _, statErr := os.Stat(managedPath); statErr == nil {
		t.Errorf("managed worktree should not have been created at %s", managedPath)
	}
}

// TestEnsureForTrackTitled_StaleRegistrationAfterManualRemoval covers the case
// where the worktree directory was rm-rf'd manually (no `git worktree remove`),
// so git's worktree registration is stale. EnsureForTrackTitled must prune and
// recreate without erroring.
func TestEnsureForTrackTitled_StaleRegistrationAfterManualRemoval(t *testing.T) {
	dir := setupGitRepo(t)

	path1, err := worktree.EnsureForTrackTitled("Stale Reg Track", "trk-stale01", dir, io.Discard)
	if err != nil {
		t.Fatalf("first EnsureForTrackTitled: %v", err)
	}

	// Manually remove the worktree directory — leaves a stale registration.
	if err := os.RemoveAll(path1); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	path2, err := worktree.EnsureForTrackTitled("Stale Reg Track", "trk-stale01", dir, io.Discard)
	if err != nil {
		t.Fatalf("second EnsureForTrackTitled after manual rm (bug-92690d5b): %v", err)
	}
	if _, err := os.Stat(path2); err != nil {
		t.Errorf("re-created worktree should exist at %s: %v", path2, err)
	}
}
