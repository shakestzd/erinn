package main

import (
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

// isMainWorktree reports whether dir is the primary (main) git worktree,
// not a linked worktree created by `git worktree add`. It delegates to the
// real git binary via the default execGitDir seam.
//
// Detection method: in the primary worktree `git rev-parse --git-dir` and
// `git rev-parse --git-common-dir` both resolve to the same path (the repo's
// .git directory). In a linked worktree, --git-dir points to a per-worktree
// admin stub under .git/worktrees/<name>/ while --git-common-dir still points
// to the shared .git directory — they differ.
func isMainWorktree(dir string) bool {
	return isMainWorktreeWith(dir, execGitDir)
}

// execGitDir runs `git -C dir rev-parse <flag>` and returns the trimmed output.
// It is the default seam for isMainWorktreeWith.
func execGitDir(dir, flag string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", flag).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// isMainWorktreeWith is the injectable variant of isMainWorktree used in tests.
// lookup(dir, flag) must return the output of `git rev-parse <flag>` for the
// two flags "--git-dir" and "--git-common-dir".
func isMainWorktreeWith(dir string, lookup func(string, string) (string, error)) bool {
	gitDir, err := lookup(dir, "--git-dir")
	if err != nil || gitDir == "" {
		return false
	}
	commonDir, err := lookup(dir, "--git-common-dir")
	if err != nil || commonDir == "" {
		return false
	}

	// Resolve to absolute paths so symlinks don't produce false mismatches.
	absGitDir, err := filepath.Abs(filepath.Join(dir, gitDir))
	if err != nil {
		absGitDir = gitDir
	}
	absCommonDir, err := filepath.Abs(filepath.Join(dir, commonDir))
	if err != nil {
		absCommonDir = commonDir
	}

	return absGitDir == absCommonDir
}

// reportMainWorktreeIsolation writes the multi-agent Git isolation section to b.
// It is included in runDoctorReport so operators running `wipnote launcher doctor`
// see isolation guidance alongside the other health checks.
func reportMainWorktreeIsolation(b *strings.Builder, repoRoot string) {
	reportMainWorktreeIsolationTo(b, isMainWorktree(repoRoot), repoRoot)
}

// reportMainWorktreeIsolationTo is the injectable variant that accepts the
// pre-computed isMain flag. Used directly in tests.
func reportMainWorktreeIsolationTo(w io.Writer, isMain bool, repoRoot string) {
	fmt.Fprintln(w, "--- multi-agent Git isolation ---")
	if isMain {
		// A primary worktree is EITHER the shared main checkout OR a dedicated
		// isolated clone — both are recommended operating models and are
		// indistinguishable at the git level. So this is informational, not a
		// hard warning: it only matters if THIS checkout is shared by multiple
		// agents/CLIs concurrently (roborev #3647 — don't flag compliant clones).
		fmt.Fprintln(w, "  NOTE: running in a primary worktree (not a linked worktree)")
		fmt.Fprintln(w, "  If this checkout is a dedicated isolated clone for one agent, no action")
		fmt.Fprintln(w, "  is needed — that is a recommended model. Action is only needed if multiple")
		fmt.Fprintln(w, "  agents or CLIs SHARE this checkout, which risks Git index contention and")
		fmt.Fprintln(w, "  interleaved commits. Recommended operating model for shared checkouts:")
		fmt.Fprintln(w, "    - Source edits: use per-agent worktrees (`wipnote yolo --feature <id>`)")
		fmt.Fprintln(w, "      or dedicated isolated clones — never a shared main checkout")
		fmt.Fprintln(w, "    - Metadata commits: serialized via runGitMutation advisory lock")
		fmt.Fprintln(w, "      (feat-3f66d83f) — safe to run from any worktree")
		fmt.Fprintln(w, "    - Lock file cleanup: opt-in only via `wipnote launcher git-lock --fix`")
		fmt.Fprintln(w, "      (requires age threshold + no-live-writer check; never removed automatically)")
		if repoRoot != "" {
			fmt.Fprintln(w, "  To create an isolated worktree: wipnote yolo --feature <id>")
		}
	} else {
		fmt.Fprintln(w, "  OK: running in a linked worktree (isolated from main checkout)")
		fmt.Fprintln(w, "  This is the recommended operating model for multi-agent work.")
	}
	fmt.Fprintln(w)
}
