// Package worktree provides helpers to create and reuse git worktrees for
// HtmlGraph work items (features, tracks, and agent tasks).
//
// All three public functions are idempotent: calling them on an already-existing
// worktree returns the existing path without error. Progress messages are written
// to the io.Writer passed by the caller; pass io.Discard to suppress all output.
package worktree

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakestzd/htmlgraph/internal/htmlparse"
)

// RepairGitdirIfStale checks whether the current directory is a linked git
// worktree whose .git file points at a nonexistent gitdir path, and rewrites
// it to the correct location under mainRepoRoot when so. This recovers
// worktrees created on one host (e.g. macOS at /Users/.../project/.git/…)
// that are now being used on another host (e.g. a Linux Codespace at
// /workspaces/project/.git/…).
//
// Returns (true, nil) when a repair was performed, (false, nil) when the
// gitdir is already valid or CWD is not a linked worktree, and (false, err)
// on unexpected I/O errors.
//
// Use this at CLI entry when HTMLGRAPH_PROJECT_DIR is known — the helper is
// intentionally conservative: it only rewrites when it can locate both the
// stale gitdir reference AND the expected correct path under the provided
// mainRepoRoot. Anything ambiguous is left alone.
func RepairGitdirIfStale(worktreeDir, mainRepoRoot string) (bool, error) {
	gitFile := filepath.Join(worktreeDir, ".git")
	info, err := os.Stat(gitFile)
	if os.IsNotExist(err) || (err == nil && info.IsDir()) {
		// Not a linked worktree (either no .git at all, or the main repo's .git directory).
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", gitFile, err)
	}

	raw, err := os.ReadFile(gitFile)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", gitFile, err)
	}
	line := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(line, "gitdir: ") {
		return false, nil
	}
	gitdir := strings.TrimPrefix(line, "gitdir: ")

	if _, err := os.Stat(gitdir); err == nil {
		return false, nil // already valid
	}

	worktreeName := filepath.Base(worktreeDir)
	correctGitdir := filepath.Join(mainRepoRoot, ".git", "worktrees", worktreeName)
	if _, err := os.Stat(correctGitdir); err != nil {
		return false, fmt.Errorf("expected gitdir %q not present under mainRepoRoot: %w", correctGitdir, err)
	}

	// Also rewrite the main repo's gitdir pointer back at the worktree path,
	// which git uses for reverse lookups.
	mainGitdirFile := filepath.Join(correctGitdir, "gitdir")
	if _, err := os.Stat(mainGitdirFile); err == nil {
		_ = os.WriteFile(mainGitdirFile, []byte(filepath.Join(worktreeDir, ".git")+"\n"), 0644)
	}

	if err := os.WriteFile(gitFile, []byte("gitdir: "+correctGitdir+"\n"), 0644); err != nil {
		return false, fmt.Errorf("rewrite %s: %w", gitFile, err)
	}
	return true, nil
}

// EnsureForFeature ensures a git worktree exists for the given feature and returns its path.
// When the feature belongs to a parent track, the track worktree is created/reused instead.
// Progress is written to w; pass io.Discard to suppress output.
func EnsureForFeature(featureID, repoRoot string, w io.Writer) (string, error) {
	// If the feature has a parent track, delegate to the track worktree.
	trackID := resolveTrackForFeature(featureID, repoRoot)
	if trackID != "" {
		return EnsureForTrack(trackID, repoRoot, w)
	}

	worktreePath := filepath.Join(repoRoot, ".claude", "worktrees", featureID)
	branchName := "yolo-" + featureID

	// Reuse existing worktree.
	if _, err := os.Stat(worktreePath); err == nil {
		fmt.Fprintf(w, "  Worktree: %s (reusing existing)\n", worktreePath)
		return worktreePath, nil
	}

	if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
		return "", fmt.Errorf("could not create worktrees directory: %w", err)
	}

	cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", worktreePath, "-b", branchName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add failed: %w\n%s", err, out)
	}

	fmt.Fprintf(w, "  Worktree: %s (branch: %s)\n", worktreePath, branchName)
	excludeHtmlgraphFromWorktree(worktreePath, w)
	reindexWorktree(worktreePath, w)

	return worktreePath, nil
}

// EnsureForTrack ensures a git worktree exists for the given track and returns its path.
// Progress is written to w; pass io.Discard to suppress output.
func EnsureForTrack(trackID, repoRoot string, w io.Writer) (string, error) {
	worktreePath := filepath.Join(repoRoot, ".claude", "worktrees", trackID)
	branchName := trackID // Track worktrees use the track ID as the branch name.

	// Reuse existing worktree.
	if _, err := os.Stat(worktreePath); err == nil {
		fmt.Fprintf(w, "  Worktree: %s (reusing existing)\n", worktreePath)
		return worktreePath, nil
	}

	if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
		return "", fmt.Errorf("could not create worktrees directory: %w", err)
	}

	cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", worktreePath, "-b", branchName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add failed: %w\n%s", err, out)
	}

	fmt.Fprintf(w, "  Worktree: %s (branch: %s)\n", worktreePath, branchName)
	excludeHtmlgraphFromWorktree(worktreePath, w)
	reindexWorktree(worktreePath, w)

	return worktreePath, nil
}

// EnsureForAgent ensures a git worktree exists for the given agent task and returns its path.
// The worktree branches from the track branch and is placed at
// .claude/worktrees/<trackID>/agent-<taskName>.
// Progress is written to w; pass io.Discard to suppress output.
func EnsureForAgent(trackID, taskName, repoRoot string, w io.Writer) (string, error) {
	agentBranch := "agent-" + trackID + "-" + taskName
	worktreePath := filepath.Join(repoRoot, ".claude", "worktrees", trackID, "agent-"+taskName)

	// Reuse existing worktree.
	if _, err := os.Stat(worktreePath); err == nil {
		fmt.Fprintf(w, "  Agent worktree: %s (reusing existing)\n", worktreePath)
		return worktreePath, nil
	}

	// Track branch must exist before creating an agent branch from it.
	if err := exec.Command("git", "-C", repoRoot, "rev-parse", "--verify", trackID).Run(); err != nil {
		return "", fmt.Errorf("track branch %s not found: create track worktree first with htmlgraph yolo --track %s", trackID, trackID)
	}

	if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
		return "", fmt.Errorf("could not create agent worktrees directory: %w", err)
	}

	cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", worktreePath, "-b", agentBranch, trackID)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add failed: %w\n%s", err, out)
	}

	fmt.Fprintf(w, "  Agent worktree: %s (branch: %s, from: %s)\n", worktreePath, agentBranch, trackID)
	return worktreePath, nil
}

// resolveTrackForFeature reads a feature HTML file and returns its data-track-id attribute.
// If the feature file doesn't exist or has no track ID, returns empty string.
func resolveTrackForFeature(featureID, projectRoot string) string {
	featureFile := filepath.Join(projectRoot, ".htmlgraph", "features", featureID+".html")
	node, err := htmlparse.ParseFile(featureFile)
	if err != nil {
		// File not found or parse error — gracefully return empty.
		return ""
	}
	return node.TrackID
}

// excludeHtmlgraphFromWorktree adds .htmlgraph/ to the worktree's local git exclude file.
// Best-effort: errors are written to w but do not abort.
func excludeHtmlgraphFromWorktree(worktreePath string, w io.Writer) {
	gitFile := filepath.Join(worktreePath, ".git")
	content, err := os.ReadFile(gitFile)
	if err != nil {
		fmt.Fprintf(w, "  Warning: could not read .git file for exclude setup: %v\n", err)
		return
	}

	gitdirLine := strings.TrimSpace(string(content))
	gitdir := strings.TrimPrefix(gitdirLine, "gitdir: ")
	if gitdir == gitdirLine {
		return // Not a worktree — no gitdir prefix found.
	}

	excludePath := filepath.Join(gitdir, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(excludePath), 0755); err != nil {
		fmt.Fprintf(w, "  Warning: could not create exclude directory: %v\n", err)
		return
	}

	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(w, "  Warning: could not open exclude file: %v\n", err)
		return
	}
	defer f.Close()

	if _, err := f.WriteString("\n.htmlgraph/\n"); err != nil {
		fmt.Fprintf(w, "  Warning: could not write to exclude file: %v\n", err)
	}
}

// reindexWorktreeFn is the function called by reindexWorktree. Swap to a
// no-op in tests to skip the subprocess fork. Defaults to the real impl.
var reindexWorktreeFn = runReindexSubprocess

// reindexWorktree runs `htmlgraph reindex` in the given worktree directory so
// the worktree's SQLite cache is current before Claude launches. Best-effort:
// failures are written to w but do not abort.
func reindexWorktree(worktreeDir string, w io.Writer) {
	reindexWorktreeFn(worktreeDir, w)
}

// runReindexSubprocess is the real implementation of reindexWorktree.
func runReindexSubprocess(worktreeDir string, w io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(w, "  Warning: could not determine executable path for reindex: %v\n", err)
		return
	}
	reindexCmd := exec.CommandContext(ctx, exe, "reindex")
	reindexCmd.Dir = worktreeDir
	if err := reindexCmd.Run(); err != nil {
		fmt.Fprintf(w, "  Warning: reindex in worktree failed: %v\n", err)
	}
}
