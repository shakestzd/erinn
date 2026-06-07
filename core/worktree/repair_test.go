package worktree_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/worktree"
)

// TestRepairGitdirStalePath is the primary TDD test: create a real worktree,
// overwrite its .git file with a bogus absolute path, call RepairGitdir, and
// assert the file is corrected.
func TestRepairGitdirStalePath(t *testing.T) {
	dir := setupGitRepo(t)

	// Create a real worktree so git registers it under .git/worktrees/.
	worktreePath, err := worktree.EnsureForTrack("trk-repair111", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForTrack: %v", err)
	}

	// Overwrite the .git file with a stale cross-machine path.
	gitFile := filepath.Join(worktreePath, ".git")
	bogusGitdir := "/nonexistent/cross/machine/path/.git/worktrees/trk-repair111"
	if err := os.WriteFile(gitFile, []byte("gitdir: "+bogusGitdir+"\n"), 0644); err != nil {
		t.Fatalf("overwrite .git: %v", err)
	}

	// Call repair.
	mainGitDir := filepath.Join(dir, ".git")
	if err := worktree.RepairGitdir(worktreePath, mainGitDir); err != nil {
		t.Fatalf("RepairGitdir: %v", err)
	}

	// Assert the .git file now contains a valid gitdir.
	content, err := os.ReadFile(gitFile)
	if err != nil {
		t.Fatalf("read .git after repair: %v", err)
	}

	line := strings.TrimSpace(string(content))
	if !strings.HasPrefix(line, "gitdir: ") {
		t.Fatalf("repaired .git has unexpected format: %q", line)
	}

	repairedGitdir := strings.TrimPrefix(line, "gitdir: ")

	// The repaired path must exist on disk.
	if _, err := os.Stat(repairedGitdir); err != nil {
		t.Errorf("repaired gitdir %q does not exist: %v", repairedGitdir, err)
	}

	// The repaired path must be under the main repo's .git/worktrees/.
	expectedPrefix := filepath.Join(mainGitDir, "worktrees")
	if !strings.HasPrefix(repairedGitdir, expectedPrefix) {
		t.Errorf("repaired gitdir %q not under %q", repairedGitdir, expectedPrefix)
	}
}

// TestRepairGitdirNoOpWhenValid verifies that repair is a no-op when the
// gitdir already points to an existing path.
func TestRepairGitdirNoOpWhenValid(t *testing.T) {
	dir := setupGitRepo(t)

	worktreePath, err := worktree.EnsureForTrack("trk-repair222", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForTrack: %v", err)
	}

	// Read the original .git content before repair.
	gitFile := filepath.Join(worktreePath, ".git")
	originalContent, err := os.ReadFile(gitFile)
	if err != nil {
		t.Fatalf("read original .git: %v", err)
	}

	mainGitDir := filepath.Join(dir, ".git")
	if err := worktree.RepairGitdir(worktreePath, mainGitDir); err != nil {
		t.Fatalf("RepairGitdir (no-op): %v", err)
	}

	afterContent, err := os.ReadFile(gitFile)
	if err != nil {
		t.Fatalf("read .git after repair: %v", err)
	}

	if string(originalContent) != string(afterContent) {
		t.Errorf("file changed on no-op repair:\nbefore: %q\nafter:  %q",
			originalContent, afterContent)
	}
}

// TestRepairGitdirMissingFile verifies that repair returns nil (not an error)
// when the .git file does not exist.
func TestRepairGitdirMissingFile(t *testing.T) {
	dir := t.TempDir()
	mainGitDir := filepath.Join(dir, ".git")

	if err := worktree.RepairGitdir("/nonexistent/worktree", mainGitDir); err != nil {
		t.Errorf("expected nil error for missing .git file, got: %v", err)
	}
}

// TestRepairGitdirUnrecognizedFormat verifies that repair is a no-op when
// the .git file content does not start with "gitdir: ".
func TestRepairGitdirUnrecognizedFormat(t *testing.T) {
	dir := t.TempDir()
	worktreePath := dir

	gitFile := filepath.Join(worktreePath, ".git")
	if err := os.WriteFile(gitFile, []byte("not a gitdir line\n"), 0644); err != nil {
		t.Fatalf("write fake .git: %v", err)
	}

	mainGitDir := filepath.Join(dir, "fake.git")
	if err := worktree.RepairGitdir(worktreePath, mainGitDir); err != nil {
		t.Errorf("expected nil error for unrecognized format, got: %v", err)
	}

	// File must be unchanged.
	content, _ := os.ReadFile(gitFile)
	if string(content) != "not a gitdir line\n" {
		t.Errorf("file was modified: %q", content)
	}
}

// TestRepairGitdirFromRepoRoot verifies the convenience wrapper that derives
// mainGitDir from the repo root.
func TestRepairGitdirFromRepoRoot(t *testing.T) {
	dir := setupGitRepo(t)

	worktreePath, err := worktree.EnsureForTrack("trk-repair333", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForTrack: %v", err)
	}

	// Overwrite with a stale path.
	gitFile := filepath.Join(worktreePath, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: /old/machine/path/.git/worktrees/trk-repair333\n"), 0644); err != nil {
		t.Fatalf("overwrite .git: %v", err)
	}

	if err := worktree.RepairGitdirFromRepoRoot(worktreePath, dir); err != nil {
		t.Fatalf("RepairGitdirFromRepoRoot: %v", err)
	}

	content, err := os.ReadFile(gitFile)
	if err != nil {
		t.Fatalf("read .git after repair: %v", err)
	}

	line := strings.TrimSpace(string(content))
	repairedGitdir := strings.TrimPrefix(line, "gitdir: ")
	if _, err := os.Stat(repairedGitdir); err != nil {
		t.Errorf("repaired gitdir %q does not exist: %v", repairedGitdir, err)
	}
}

// TestRepairGitdirBasenameCollision guards against the bug where repair
// derives the admin-dir name from filepath.Base(worktreePath) instead of the
// existing gitdir pointer. Two worktrees can share a basename (git
// disambiguates the admin dir with a numeric suffix, e.g. agent-task vs
// agent-task1); using the worktree path's basename would silently rewrite
// the second worktree's .git to point at the first worktree's admin dir.
//
// Reproduces the review finding on PR #54: both agent-task worktrees live
// in directories named `agent-task` but git named the admin dirs
// `agent-task` and `agent-task1`. Repair must preserve the existing admin
// name (read from the stale gitdir).
func TestRepairGitdirBasenameCollision(t *testing.T) {
	dir := setupGitRepo(t)
	mainGitDir := filepath.Join(dir, ".git")

	// Simulate two worktrees whose paths have the same basename but whose
	// admin dirs are disambiguated by git (agent-task, agent-task1). In a
	// real setup, git writes those two admin dirs itself; we fabricate them
	// here so the test is self-contained.
	wt1 := filepath.Join(dir, "trackA", "agent-task")
	wt2 := filepath.Join(dir, "trackB", "agent-task")
	for _, p := range []string{wt1, wt2} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}
	adminA := filepath.Join(mainGitDir, "worktrees", "agent-task")
	adminB := filepath.Join(mainGitDir, "worktrees", "agent-task1")
	for _, p := range []string{adminA, adminB} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir admin %s: %v", p, err)
		}
	}

	// Both worktrees carry stale cross-machine paths but the suffix of each
	// stale pointer still carries the correct admin-dir name.
	stale1 := "/old/machine/.git/worktrees/agent-task"
	stale2 := "/old/machine/.git/worktrees/agent-task1"
	if err := os.WriteFile(filepath.Join(wt1, ".git"), []byte("gitdir: "+stale1+"\n"), 0o644); err != nil {
		t.Fatalf("write wt1 .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt2, ".git"), []byte("gitdir: "+stale2+"\n"), 0o644); err != nil {
		t.Fatalf("write wt2 .git: %v", err)
	}

	if err := worktree.RepairGitdir(wt1, mainGitDir); err != nil {
		t.Fatalf("repair wt1: %v", err)
	}
	if err := worktree.RepairGitdir(wt2, mainGitDir); err != nil {
		t.Fatalf("repair wt2: %v", err)
	}

	got1, _ := os.ReadFile(filepath.Join(wt1, ".git"))
	got2, _ := os.ReadFile(filepath.Join(wt2, ".git"))
	want1 := "gitdir: " + adminA + "\n"
	want2 := "gitdir: " + adminB + "\n"

	if string(got1) != want1 {
		t.Errorf("wt1 repaired to %q, want %q", got1, want1)
	}
	if string(got2) != want2 {
		t.Errorf("wt2 repaired to %q, want %q — "+
			"basename-collision regression (review comment on PR #54)", got2, want2)
	}
}
