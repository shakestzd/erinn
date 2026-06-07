// Package worktree provides helpers to create and reuse git worktrees for
// wipnote work items (features, tracks, and agent tasks).
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

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/internal/slug"
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
// Use this at CLI entry when WIPNOTE_PROJECT_DIR is known — the helper is
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
	path, _, err := EnsureForFeatureStatus(featureID, repoRoot, w)
	return path, err
}

// EnsureForFeatureStatus is EnsureForFeature plus a "created vs reused" signal.
// created is true only when this call created a NEW worktree on disk; it is
// false when an existing worktree was reused. Callers that mutate the new
// worktree (e.g. carrying over uncommitted changes) must gate on created to
// avoid double-applying into a reused worktree (bug-bcf8a311 guardrail).
func EnsureForFeatureStatus(featureID, repoRoot string, w io.Writer) (string, bool, error) {
	// If the feature has a parent track, delegate to the track worktree.
	trackID := resolveTrackForFeature(featureID, repoRoot)
	if trackID != "" {
		return EnsureForTrackStatus(trackID, repoRoot, w)
	}

	worktreePath := filepath.Join(repoRoot, ".claude", "worktrees", featureID)
	branchName := "yolo-" + featureID

	// Reuse existing worktree.
	if _, err := os.Stat(worktreePath); err == nil {
		fmt.Fprintf(w, "  Worktree: %s (reusing existing)\n", worktreePath)
		return worktreePath, false, nil
	}

	resolved, created, err := addOrAttachWorktree(repoRoot, worktreePath, branchName)
	if err != nil {
		return "", false, err
	}
	if !created {
		fmt.Fprintf(w, "  Worktree: %s (reusing existing)\n", resolved)
		return resolved, false, nil
	}

	fmt.Fprintf(w, "  Worktree: %s (branch: %s)\n", resolved, branchName)
	excludeWipnoteFromWorktree(resolved, w)
	reindexWorktree(resolved, w)

	return resolved, true, nil
}

// EnsureForTrack ensures a git worktree exists for the given track and returns its path.
// Progress is written to w; pass io.Discard to suppress output.
func EnsureForTrack(trackID, repoRoot string, w io.Writer) (string, error) {
	path, _, err := EnsureForTrackStatus(trackID, repoRoot, w)
	return path, err
}

// EnsureForTrackStatus is EnsureForTrack plus a "created vs reused" signal.
// See EnsureForFeatureStatus for the contract on the created return value.
func EnsureForTrackStatus(trackID, repoRoot string, w io.Writer) (string, bool, error) {
	worktreePath := filepath.Join(repoRoot, ".claude", "worktrees", trackID)
	branchName := trackID // Track worktrees use the track ID as the branch name.

	// Reuse existing worktree.
	if _, err := os.Stat(worktreePath); err == nil {
		fmt.Fprintf(w, "  Worktree: %s (reusing existing)\n", worktreePath)
		return worktreePath, false, nil
	}

	resolved, created, err := addOrAttachWorktree(repoRoot, worktreePath, branchName)
	if err != nil {
		return "", false, err
	}
	if !created {
		fmt.Fprintf(w, "  Worktree: %s (reusing existing)\n", resolved)
		return resolved, false, nil
	}

	fmt.Fprintf(w, "  Worktree: %s (branch: %s)\n", resolved, branchName)
	excludeWipnoteFromWorktree(resolved, w)
	reindexWorktree(resolved, w)

	return resolved, true, nil
}

// TrackWorktreeDirName returns the directory name to use for a track worktree.
// When trackTitle is non-empty, returns "<title-slug>-<trackID>" (slug capped at 30 chars).
// Falls back to bare trackID when the title produces an empty slug.
func TrackWorktreeDirName(trackTitle, trackID string) string {
	if trackTitle != "" {
		s := slug.Make(trackTitle, 30)
		if s != "" {
			return s + "-" + trackID
		}
	}
	return trackID
}

// EnsureForTrackTitled ensures a git worktree exists for the given track, using a
// human-readable directory name "<title-slug>-<trackID>" when trackTitle is provided.
// Only new worktrees use the titled path; if an existing worktree for the track branch
// is found (at the legacy bare-ID path or any titled path) it is reused unchanged to
// avoid orphaning running sessions and to prevent git errors when the branch is already
// checked out.
// Progress is written to w; pass io.Discard to suppress output.
func EnsureForTrackTitled(trackTitle, trackID, repoRoot string, w io.Writer) (string, error) {
	path, _, err := EnsureForTrackTitledStatus(trackTitle, trackID, repoRoot, w)
	return path, err
}

// EnsureForTrackTitledStatus is EnsureForTrackTitled plus a "created vs reused"
// signal. See EnsureForFeatureStatus for the contract on the created return value.
func EnsureForTrackTitledStatus(trackTitle, trackID, repoRoot string, w io.Writer) (string, bool, error) {
	// Check the legacy bare-ID path first — reuse without rename to avoid
	// orphaning any running yolo session that has the old path as its CWD.
	legacyPath := filepath.Join(repoRoot, ".claude", "worktrees", trackID)
	if _, err := os.Stat(legacyPath); err == nil {
		fmt.Fprintf(w, "  Worktree: %s (reusing existing)\n", legacyPath)
		return legacyPath, false, nil
	}

	// Scan for any existing worktree already checked out on this track's branch.
	// This handles the title-rename case: if the track was previously created with
	// title-1 (giving path "title-1-slug-<trackID>"), a rename to title-2 would
	// compute a new path and fail because the branch is already checked out.
	// We scan the worktrees directory for any entry ending in "-<trackID>" or
	// equal to any titled variant that git already knows about.
	if existing := findExistingWorktreeForBranch(repoRoot, trackID, w); existing != "" {
		fmt.Fprintf(w, "  Worktree: %s (reusing existing)\n", existing)
		return existing, false, nil
	}

	// New worktree: use titled path.
	dirName := TrackWorktreeDirName(trackTitle, trackID)
	worktreePath := filepath.Join(repoRoot, ".claude", "worktrees", dirName)
	branchName := trackID // Branch name remains the bare track ID.

	// Check if the titled path already exists (idempotent on second call with same title).
	if _, err := os.Stat(worktreePath); err == nil {
		fmt.Fprintf(w, "  Worktree: %s (reusing existing)\n", worktreePath)
		return worktreePath, false, nil
	}

	resolved, created, err := addOrAttachWorktree(repoRoot, worktreePath, branchName)
	if err != nil {
		return "", false, err
	}
	if !created {
		fmt.Fprintf(w, "  Worktree: %s (reusing existing)\n", resolved)
		return resolved, false, nil
	}

	fmt.Fprintf(w, "  Worktree: %s (branch: %s)\n", resolved, branchName)
	excludeWipnoteFromWorktree(resolved, w)
	reindexWorktree(resolved, w)

	return resolved, true, nil
}

// addOrAttachWorktree creates a git worktree at worktreePath for branchName, or
// returns the path of an existing worktree when one is already checked out on the
// branch. This makes track/feature worktree creation idempotent against the
// "branch already exists" failure that occurs when the worktree directory was
// removed but the branch reference persists from a prior run (bug-92690d5b).
//
// Returns (resolvedPath, created, err):
//   - resolvedPath equals worktreePath when a new worktree was created,
//     otherwise it's the path of the pre-existing worktree on branchName.
//   - created is true only when this call created a new worktree on disk.
func addOrAttachWorktree(repoRoot, worktreePath, branchName string) (string, bool, error) {
	// Prune stale worktree registrations (e.g. from manually-deleted directories)
	// so the porcelain listing reflects current on-disk state. Best-effort.
	_ = exec.Command("git", "-C", repoRoot, "worktree", "prune").Run()

	managedRoot := filepath.Join(repoRoot, ".claude", "worktrees")
	if existing := worktreeOnBranch(repoRoot, branchName); existing != "" {
		// Only reuse worktrees we manage. The branch may be checked out at the
		// main repo path or some external worktree — silently running yolo
		// against either of those would bypass isolation.
		if isUnderDir(existing, managedRoot) {
			return existing, false, nil
		}
		return "", false, fmt.Errorf(
			"branch %s is already checked out at %s (outside %s); "+
				"switch or remove that checkout before re-running",
			branchName, existing, managedRoot)
	}

	if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
		return "", false, fmt.Errorf("could not create worktrees directory: %w", err)
	}

	branchExists := exec.Command("git", "-C", repoRoot, "rev-parse", "--verify", "refs/heads/"+branchName).Run() == nil

	var cmd *exec.Cmd
	if branchExists {
		// Attach to existing branch — omit -b so git does not try to create it.
		cmd = exec.Command("git", "-C", repoRoot, "worktree", "add", worktreePath, branchName)
	} else {
		cmd = exec.Command("git", "-C", repoRoot, "worktree", "add", worktreePath, "-b", branchName)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", false, fmt.Errorf("git worktree add failed: %w\n%s", err, out)
	}
	return worktreePath, true, nil
}

// isUnderDir reports whether path is the same as, or nested under, dir.
// Both paths are resolved to absolute form before comparison so callers can
// pass repo-relative inputs from `git worktree list --porcelain` without
// worrying about format differences.
func isUnderDir(path, dir string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// worktreeOnBranch returns the path of the worktree currently checked out on
// branchName per `git worktree list --porcelain`, or "" when no worktree is on
// that branch. The porcelain format is more authoritative than scanning a
// directory because it reflects worktrees registered anywhere in the repo and
// stays accurate after `git worktree prune`. Callers must filter the result
// against an expected parent directory before reusing — the helper itself
// does not constrain location.
func worktreeOnBranch(repoRoot, branchName string) string {
	out, err := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return ""
	}
	var path string
	fullRef := "refs/heads/" + branchName
	for line := range strings.SplitSeq(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			if strings.TrimPrefix(line, "branch ") == fullRef {
				return path
			}
		case line == "":
			path = ""
		}
	}
	return ""
}

// findExistingWorktreeForBranch scans the worktrees directory under repoRoot for any
// directory that is a git worktree checked out on branchName. Returns the path if
// found, empty string otherwise. This prevents "branch already checked out" errors
// when a track title is renamed after the worktree was first created.
func findExistingWorktreeForBranch(repoRoot, branchName string, _ io.Writer) string {
	worktreesDir := filepath.Join(repoRoot, ".claude", "worktrees")
	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		return "" // directory doesn't exist yet — no existing worktrees
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(worktreesDir, entry.Name())
		// Check if this directory's git HEAD points at branchName.
		out, err := exec.Command("git", "-C", candidate, "rev-parse", "--abbrev-ref", "HEAD").Output()
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(out)) == branchName {
			return candidate
		}
	}
	return ""
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
		return "", fmt.Errorf("track branch %s not found: create track worktree first with wipnote yolo --track %s", trackID, trackID)
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

// CreateForClaudeHook creates or reuses the git worktree requested by Claude
// Code's WorktreeCreate replacement hook. The returned path is the path Claude
// must receive on stdout.
func CreateForClaudeHook(repoRoot, worktreePath, branchName string, w io.Writer) (string, error) {
	resolved, created, err := addOrAttachWorktree(repoRoot, worktreePath, branchName)
	if err != nil {
		return "", err
	}
	if created {
		excludeWipnoteFromWorktree(resolved, w)
		reindexWorktree(resolved, w)
	}
	return resolved, nil
}

// resolveTrackForFeature reads a feature HTML file and returns its data-track-id attribute.
// If the feature file doesn't exist or has no track ID, returns empty string.
func resolveTrackForFeature(featureID, projectRoot string) string {
	featureFile := filepath.Join(projectRoot, ".wipnote", "features", featureID+".html")
	node, err := htmlparse.ParseFile(featureFile)
	if err != nil {
		// File not found or parse error — gracefully return empty.
		return ""
	}
	return node.TrackID
}

// excludeWipnoteFromWorktree adds .wipnote/ to the worktree's local git exclude file.
// Best-effort: errors are written to w but do not abort.
//
// Note on the design intent: the exclusion prevents .wipnote/ mutations that
// occur inside the worktree from appearing in `git status` noise within that
// worktree. It does NOT prevent the main-repo artifact from being committed.
// When a work item is completed via `wipnote feature/bug/spike complete`,
// commitWipnoteArtifact (cmd/wipnote/workitem_commit.go) uses `git -C <repoRoot>`
// with an explicit absolute path to stage and commit the HTML directly in the
// main repository, bypassing the per-worktree exclude entirely.
func excludeWipnoteFromWorktree(worktreePath string, w io.Writer) {
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

	if _, err := f.WriteString("\n.wipnote/\n"); err != nil {
		fmt.Fprintf(w, "  Warning: could not write to exclude file: %v\n", err)
	}
}

// reindexWorktreeFn is the function called by reindexWorktree. Swap to a
// no-op in tests to skip the subprocess fork. Defaults to the real impl.
var reindexWorktreeFn = runReindexSubprocess

// SetReindexFnForTest replaces reindexWorktreeFn for the duration of a test.
// The previous function is returned so callers can restore it via defer.
// Test-only helper; do not call from production code.
func SetReindexFnForTest(fn func(worktreeDir string, w io.Writer)) func(string, io.Writer) {
	prev := reindexWorktreeFn
	reindexWorktreeFn = fn
	return prev
}

// reindexWorktree runs `wipnote reindex` in the given worktree directory so
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
