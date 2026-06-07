package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RepairGitdir checks whether the worktree at worktreePath has a valid .git
// gitdir pointer and rewrites it if the path no longer exists (cross-machine
// path drift, e.g. macOS path opened inside a Linux devcontainer).
//
// The .git file in a linked worktree is a one-line text file of the form:
//
//	gitdir: /absolute/path/to/.git/worktrees/<name>
//
// When a worktree is created on macOS and then opened inside a Linux
// devcontainer, the absolute path becomes stale. RepairGitdir detects this
// and rewrites the path using the current repo root derived from mainGitDir.
//
// mainGitDir is the absolute path to the main repo's .git directory (e.g.
// /workspaces/myrepo/.git). Pass the result of locating the main .git from
// repoRoot.
//
// Returns nil when no repair was needed or repair succeeded. Returns an error
// only when the file is present but cannot be read or written.
func RepairGitdir(worktreePath, mainGitDir string) error {
	gitFile := filepath.Join(worktreePath, ".git")

	content, err := os.ReadFile(gitFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Not a worktree or already a directory — nothing to do.
		}
		return fmt.Errorf("read %s: %w", gitFile, err)
	}

	line := strings.TrimSpace(string(content))
	if !strings.HasPrefix(line, "gitdir: ") {
		return nil // Unexpected format — leave it alone.
	}

	currentGitdir := strings.TrimPrefix(line, "gitdir: ")

	// Fast path: gitdir already points to an existing path.
	if _, err := os.Stat(currentGitdir); err == nil {
		return nil
	}

	// Compute the correct gitdir: <mainGitDir>/worktrees/<name>.
	//
	// Important — preserve git's own disambiguated name. When two worktrees
	// share a basename (e.g. two `agent-task` worktrees on different tracks),
	// git names the later admin dir `agent-task1`, `agent-task2`, etc. The
	// stale .git pointer on the current machine still carries that correct
	// name — we just need a new *prefix*. Taking `filepath.Base(worktreePath)`
	// instead would rewrite the second worktree to point at the first one's
	// admin dir, silently corrupting branch metadata for both.
	//
	// So: derive the admin-dir basename from the existing (stale) gitdir,
	// and only swap out the leading path.
	worktreeName := filepath.Base(currentGitdir)
	correctGitdir := filepath.Join(mainGitDir, "worktrees", worktreeName)

	// Verify the computed path actually exists before writing.
	if _, err := os.Stat(correctGitdir); err != nil {
		return fmt.Errorf("stale gitdir %q and computed replacement %q does not exist: %w",
			currentGitdir, correctGitdir, err)
	}

	newContent := "gitdir: " + correctGitdir + "\n"
	if err := os.WriteFile(gitFile, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("rewrite %s: %w", gitFile, err)
	}

	return nil
}

// RepairGitdirFromRepoRoot is a convenience wrapper around RepairGitdir that
// derives mainGitDir from repoRoot (the directory containing the top-level
// .git directory or file).
//
// Returns nil when no repair was needed or repair succeeded. Returns an error
// when the main .git directory cannot be located or the repair fails.
func RepairGitdirFromRepoRoot(worktreePath, repoRoot string) error {
	mainGitDir := filepath.Join(repoRoot, ".git")
	info, err := os.Stat(mainGitDir)
	if err != nil {
		return fmt.Errorf("locate main .git at %s: %w", mainGitDir, err)
	}
	if !info.IsDir() {
		// repoRoot is itself a worktree — resolve common dir via git.
		return fmt.Errorf("expected %s to be a directory (main repo .git), not a file", mainGitDir)
	}
	return RepairGitdir(worktreePath, mainGitDir)
}
