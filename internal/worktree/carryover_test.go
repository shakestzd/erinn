package worktree_test

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/internal/worktree"
)

// readFile is a small helper returning a file's contents as a string.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestCarryUncommittedChanges_TrackedEditCarriedMainUnchanged verifies the core
// behavior (bug-bcf8a311): a tracked edit on main is applied into a freshly-created
// worktree AND remains present on main (main is never mutated).
func TestCarryUncommittedChanges_TrackedEditCarriedMainUnchanged(t *testing.T) {
	dir := setupGitRepo(t)

	// Dirty main: edit the tracked README (vs HEAD).
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("# Test\nuncommitted edit\n"), 0644); err != nil {
		t.Fatalf("edit README: %v", err)
	}

	// Create a worktree at the same HEAD.
	path, created, err := worktree.EnsureForTrackStatus("trk-carry01", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForTrackStatus: %v", err)
	}
	if !created {
		t.Fatalf("expected worktree to be newly created")
	}

	res, err := worktree.CarryUncommittedChanges(dir, path, io.Discard)
	if err != nil {
		t.Fatalf("CarryUncommittedChanges: %v", err)
	}
	if !res.Carried {
		t.Fatalf("expected Carried=true, got %+v", res)
	}
	if res.ChangedFiles != 1 {
		t.Errorf("ChangedFiles: got %d, want 1", res.ChangedFiles)
	}
	if res.ApplyError != nil {
		t.Errorf("unexpected ApplyError: %v", res.ApplyError)
	}

	// The edit is present in the worktree.
	if got := readFile(t, filepath.Join(path, "README.md")); !strings.Contains(got, "uncommitted edit") {
		t.Errorf("worktree README missing carried edit; got %q", got)
	}
	// The edit is STILL present on main (main unchanged).
	if got := readFile(t, readme); !strings.Contains(got, "uncommitted edit") {
		t.Errorf("main README should still contain the edit; got %q", got)
	}
}

// TestCarryUncommittedChanges_CleanMainNoOp verifies that a clean tracked tree
// is a no-op with no error.
func TestCarryUncommittedChanges_CleanMainNoOp(t *testing.T) {
	dir := setupGitRepo(t)

	path, _, err := worktree.EnsureForTrackStatus("trk-clean01", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForTrackStatus: %v", err)
	}

	res, err := worktree.CarryUncommittedChanges(dir, path, io.Discard)
	if err != nil {
		t.Fatalf("CarryUncommittedChanges: %v", err)
	}
	if res.Carried {
		t.Errorf("expected Carried=false on clean main, got %+v", res)
	}
	if res.ChangedFiles != 0 {
		t.Errorf("ChangedFiles: got %d, want 0", res.ChangedFiles)
	}
	if res.ApplyError != nil {
		t.Errorf("unexpected ApplyError: %v", res.ApplyError)
	}
}

// TestCarryUncommittedChanges_UntrackedReportedNotApplied verifies untracked
// files are reported but never copied into the worktree.
func TestCarryUncommittedChanges_UntrackedReportedNotApplied(t *testing.T) {
	dir := setupGitRepo(t)

	// Untracked file on main.
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("do not carry\n"), 0644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}

	path, _, err := worktree.EnsureForTrackStatus("trk-untracked01", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForTrackStatus: %v", err)
	}

	res, err := worktree.CarryUncommittedChanges(dir, path, io.Discard)
	if err != nil {
		t.Fatalf("CarryUncommittedChanges: %v", err)
	}
	if res.Carried {
		t.Errorf("untracked-only main should not carry a tracked diff; got %+v", res)
	}
	found := false
	for _, f := range res.UntrackedFiles {
		if f == "secret.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected secret.txt reported as untracked; got %v", res.UntrackedFiles)
	}
	// Untracked file must NOT exist in the worktree.
	if _, statErr := os.Stat(filepath.Join(path, "secret.txt")); statErr == nil {
		t.Errorf("untracked file should NOT have been carried into the worktree")
	}
}

// TestCarryUncommittedChanges_ApplyFailureFailSafe verifies that when `git apply`
// fails, the function does not abort (returns nil error), records ApplyError, and
// nothing is lost. We force a failure by committing a divergent change in the
// worktree so the captured-from-main diff no longer applies cleanly.
func TestCarryUncommittedChanges_ApplyFailureFailSafe(t *testing.T) {
	dir := setupGitRepo(t)

	// Dirty main: edit README.
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("# Test\nmain edit\n"), 0644); err != nil {
		t.Fatalf("edit README: %v", err)
	}

	path, _, err := worktree.EnsureForTrackStatus("trk-applyfail01", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForTrackStatus: %v", err)
	}

	// Diverge the worktree's README and commit it so the main diff context
	// (which expects "# Test") no longer matches and git apply fails.
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("totally different content\n"), 0644); err != nil {
		t.Fatalf("diverge worktree README: %v", err)
	}
	if out, err := exec.Command("git", "-C", path, "commit", "-am", "diverge").CombinedOutput(); err != nil {
		t.Fatalf("commit in worktree: %v: %s", err, out)
	}

	var buf bytes.Buffer
	res, err := worktree.CarryUncommittedChanges(dir, path, &buf)
	if err != nil {
		t.Fatalf("CarryUncommittedChanges must not return a fatal error (fail-safe): %v", err)
	}
	if res.ApplyError == nil {
		t.Fatalf("expected ApplyError to be set on a non-applying diff")
	}
	if res.Carried {
		t.Errorf("Carried should be false when apply fails")
	}
	if !strings.Contains(buf.String(), "could not auto-carry") {
		t.Errorf("expected fail-safe warning in output; got %q", buf.String())
	}
	// Main work is untouched.
	if got := readFile(t, readme); !strings.Contains(got, "main edit") {
		t.Errorf("main README should be untouched after apply failure; got %q", got)
	}
}

// TestCarryUncommittedChanges_StagedAndUnstagedBothCarried verifies that staged
// (index) edits as well as unstaged edits are carried — `git diff HEAD` covers both.
func TestCarryUncommittedChanges_StagedAndUnstagedBothCarried(t *testing.T) {
	dir := setupGitRepo(t)

	// Staged new tracked file.
	staged := filepath.Join(dir, "staged.txt")
	if err := os.WriteFile(staged, []byte("staged content\n"), 0644); err != nil {
		t.Fatalf("write staged: %v", err)
	}
	if out, err := exec.Command("git", "-C", dir, "add", "staged.txt").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	// Unstaged edit to an existing tracked file.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\nunstaged\n"), 0644); err != nil {
		t.Fatalf("edit README: %v", err)
	}

	path, _, err := worktree.EnsureForTrackStatus("trk-mixed01", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForTrackStatus: %v", err)
	}

	res, err := worktree.CarryUncommittedChanges(dir, path, io.Discard)
	if err != nil {
		t.Fatalf("CarryUncommittedChanges: %v", err)
	}
	if !res.Carried {
		t.Fatalf("expected Carried=true; got %+v", res)
	}
	if got := readFile(t, filepath.Join(path, "staged.txt")); !strings.Contains(got, "staged content") {
		t.Errorf("staged file not carried; got %q", got)
	}
	if got := readFile(t, filepath.Join(path, "README.md")); !strings.Contains(got, "unstaged") {
		t.Errorf("unstaged edit not carried; got %q", got)
	}
}
