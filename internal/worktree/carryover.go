package worktree

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// CarryResult summarizes what CarryUncommittedChanges did, so callers can emit
// accurate user-facing messaging (e.g. the yolo dirty-main advisory).
type CarryResult struct {
	// Carried is true when a non-empty tracked diff was successfully applied
	// into the worktree.
	Carried bool
	// ChangedFiles is the count of tracked files present in the carried diff.
	// Zero when the diff was empty or could not be parsed.
	ChangedFiles int
	// UntrackedFiles lists untracked, non-ignored files in the canonical repo
	// that were NOT carried (git diff HEAD excludes untracked files). Reported
	// for the user; never auto-copied (avoids pulling in build artifacts/secrets).
	UntrackedFiles []string
	// ApplyError is non-nil when capturing or applying the diff failed. The launch
	// is never aborted on this; the user's work remains on main and the caller
	// surfaces a warning. Carried is false when ApplyError is set.
	ApplyError error
}

// CarryUncommittedChanges copies the canonical main repo's uncommitted TRACKED
// changes (staged + unstaged, vs HEAD) into a freshly-created worktree WITHOUT
// modifying main. The worktree is assumed to sit at the same HEAD as canonicalRoot
// (true immediately after `git worktree add`), so the diff applies cleanly.
//
// Behavior contract (bug-bcf8a311, "copy into worktree, keep main"):
//   - Capture: `git -C <canonicalRoot> diff HEAD`. If empty, no-op (Carried=false).
//   - Apply: pipe the diff to `git -C <worktreePath> apply`.
//   - main's working tree is NEVER mutated (no stash, reset, checkout).
//   - Untracked files are reported (ls-files --others --exclude-standard) but
//     never carried.
//   - FAIL-SAFE: any capture/apply error is recorded in ApplyError and surfaced
//     as a warning by the caller; the function returns nil error so the launch
//     proceeds and nothing is lost or moved.
//
// Progress/warnings are written to w; pass io.Discard to suppress output.
// CarryUncommittedChanges only returns a non-nil error for programmer misuse
// (empty paths); operational failures are reported via CarryResult.ApplyError.
func CarryUncommittedChanges(canonicalRoot, worktreePath string, w io.Writer) (CarryResult, error) {
	var res CarryResult
	if canonicalRoot == "" || worktreePath == "" {
		return res, fmt.Errorf("CarryUncommittedChanges: canonicalRoot and worktreePath are required")
	}

	// Always report untracked files regardless of whether there is a tracked diff.
	res.UntrackedFiles = listUntracked(canonicalRoot)

	// Capture tracked changes vs HEAD (staged + unstaged).
	diff, err := exec.Command("git", "-C", canonicalRoot, "diff", "HEAD").Output()
	if err != nil {
		res.ApplyError = fmt.Errorf("capture diff: %w", err)
		fmt.Fprintf(w, "  Warning: could not auto-carry uncommitted changes: %v; they remain on main, apply manually\n", res.ApplyError)
		return res, nil
	}
	if len(bytes.TrimSpace(diff)) == 0 {
		// Clean tracked tree — nothing to carry.
		return res, nil
	}

	res.ChangedFiles = countDiffFiles(diff)

	// Apply the diff in the worktree. Pipe via stdin so paths are interpreted
	// relative to the worktree root.
	apply := exec.Command("git", "-C", worktreePath, "apply")
	apply.Stdin = bytes.NewReader(diff)
	if out, applyErr := apply.CombinedOutput(); applyErr != nil {
		res.ApplyError = fmt.Errorf("%w\n%s", applyErr, strings.TrimSpace(string(out)))
		fmt.Fprintf(w, "  Warning: could not auto-carry uncommitted changes: %v; they remain on main, apply manually\n", res.ApplyError)
		return res, nil
	}

	res.Carried = true
	fmt.Fprintf(w, "  Copied %d uncommitted file(s) into the worktree; main left unchanged.\n", res.ChangedFiles)
	if len(res.UntrackedFiles) > 0 {
		fmt.Fprintf(w, "  Untracked files NOT carried (copy manually if needed): %s\n", strings.Join(res.UntrackedFiles, ", "))
	}
	return res, nil
}

// listUntracked returns untracked, non-ignored files in repoRoot. Best-effort:
// returns nil on error.
func listUntracked(repoRoot string) []string {
	out, err := exec.Command("git", "-C", repoRoot, "ls-files", "--others", "--exclude-standard").Output()
	if err != nil {
		return nil
	}
	var files []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			files = append(files, s)
		}
	}
	return files
}

// countDiffFiles counts the distinct files touched by a unified diff by counting
// "diff --git " header lines. Used only for the human-readable summary.
func countDiffFiles(diff []byte) int {
	n := 0
	for line := range strings.SplitSeq(string(diff), "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			n++
		}
	}
	return n
}
