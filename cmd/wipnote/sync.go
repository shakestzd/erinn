package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// workItemFileRe matches canonical work-item HTML filenames inside
// .wipnote/<type>s/<id>.html, capturing the id stem. Used to derive a
// single-item commit message variant.
var workItemFileRe = regexp.MustCompile(`^\.wipnote/(features|bugs|spikes|tracks|plans|specs)/([A-Za-z]+-[A-Za-z0-9]+)\.html$`)

// preMergeBackupRe matches paths under .wipnote/.pre-merge-backup-*/. These
// directories are durability snapshots written by external merge tooling
// (see bug-5a938a9e) and must be excluded from sync — re-committing them
// would resurrect superseded artifacts into history.
var preMergeBackupRe = regexp.MustCompile(`^\.wipnote/\.pre-merge-backup-[^/]+/`)

// runtimeStateFiles are per-machine, per-session runtime state under .wipnote/
// that must NEVER be committed by `wipnote sync` (bug-fddf5820, finding 14).
// They are volatile (rewritten constantly), machine-local, and committing them
// churns history and leaks one machine's session/launch/socket state into the
// shared canonical store. Matched as exact repo-relative paths.
var runtimeStateFiles = map[string]bool{
	".wipnote/.active-session": true,
	".wipnote/.launch-mode":    true,
	".wipnote/.serve.lock":     true,
	".wipnote/debug.log":       true,
	".wipnote/writer.pid":      true,
}

// runtimeStateDirRe matches files under runtime-state subdirectories of
// .wipnote/ that must be excluded from sync (currently logs/). Anchored so it
// only matches the intended trees.
var runtimeStateDirRe = regexp.MustCompile(`^\.wipnote/logs/`)

// isRuntimeStateFile reports whether a repo-relative .wipnote/ path is volatile
// machine-local runtime state that sync must skip (bug-fddf5820, finding 14).
func isRuntimeStateFile(path string) bool {
	if runtimeStateFiles[path] {
		return true
	}
	return runtimeStateDirRe.MatchString(path)
}

func syncCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Batch-commit any dirty .wipnote/ files in a single commit",
		Long: `Stage and commit any modified or untracked files under .wipnote/ as
one batch commit. Closes the durability gap between state-transition commits
(create/start/complete) by flushing mid-flight add-step/edit mutations to git.

The commit message is "wipnote: sync <id>" when a single work-item HTML is
flushed, or "wipnote: sync <N> items" otherwise.

Files under .wipnote/.pre-merge-backup-*/ are excluded — those are durability
snapshots produced by external merge tooling and must not be re-committed.

Use --dry-run to list planned operations without staging or committing.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			wipnoteDir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			n, err := runSync(wipnoteDir, dryRun, os.Stdout)
			if err != nil {
				return err
			}
			if n == 0 && !dryRun {
				fmt.Println("wipnote sync: nothing to sync")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list planned operations without staging or committing")
	return cmd
}

// runSync stages and commits any dirty files under wipnoteDir/ in a single
// commit. Returns the number of files included.
//
// Filtering rules:
//   - Skip when wipnoteDir matches isTestTmpPath (defense-in-depth — tests
//     must never fire real git mutations).
//   - Skip when wipnoteDir is not inside a git repo (non-fatal; logged).
//   - Skip files under .wipnote/.pre-merge-backup-*/ (durability snapshots
//     from external merge tooling — re-committing them would resurrect
//     superseded artifacts; see bug-5a938a9e).
//
// Dry-run (dryRun=true) prints the would-stage list to out and returns the
// count without any git mutation.
//
// Non-fatal contract (matches commitWipnoteArtifact): if `git commit` fails
// for any reason other than "nothing to commit" (hook rejection, locked
// index, etc.), the function logs to stderr and returns nil so callers and
// SessionEnd auto-flush do not roll back on a git-side concern. Only
// `git status` and filesystem errors are returned as fatal.
func runSync(wipnoteDir string, dryRun bool, out io.Writer) (int, error) {
	absWipnote, err := filepath.Abs(wipnoteDir)
	if err != nil {
		absWipnote = wipnoteDir
	}
	if isTestTmpPath(absWipnote) {
		if os.Getenv("WIPNOTE_DEBUG") == "1" {
			fmt.Fprintf(stderr, "wipnote sync skipped: path looks like a test temp dir: %s\n", absWipnote)
		}
		return 0, nil
	}

	repoRoot := filepath.Dir(wipnoteDir)
	if !isGitRepo(repoRoot) {
		fmt.Fprintf(stderr, "wipnote sync skipped: %s is not inside a git repository\n", repoRoot)
		return 0, nil
	}

	files, err := dirtyWipnoteFiles(repoRoot)
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, nil
	}

	if dryRun {
		fmt.Fprintf(out, "wipnote sync --dry-run: would commit %d file(s):\n", len(files))
		for _, f := range files {
			fmt.Fprintf(out, "  %s\n", f)
		}
		return len(files), nil
	}

	// Stage each path explicitly. `git -C <repoRoot> add -f -- <relPath>` is
	// the load-bearing combination: -C anchors to the main repo (bypassing any
	// linked-worktree CWD), -f overrides the per-worktree .wipnote/ exclude
	// installed by excludeWipnoteFromWorktree so that ignored-but-real files
	// can still be staged. Without -f, `git add` refuses ignored paths with
	// "Use -f if you really want to add them" and sync becomes a no-op in
	// exactly the worktrees it is meant to flush.
	addArgs := append([]string{"-C", repoRoot, "add", "-f", "--"}, files...)
	if addOut, err := exec.Command("git", addArgs...).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("wipnote sync: git add: %s: %w", strings.TrimSpace(string(addOut)), err)
	}

	msg := syncCommitMessage(files)
	commitArgs := append([]string{"-C", repoRoot, "commit", "-m", msg, "--"}, files...)
	commitOut, err := exec.Command("git", commitArgs...).CombinedOutput()
	if err != nil {
		outStr := string(commitOut)
		if strings.Contains(outStr, "nothing to commit") || strings.Contains(outStr, "no changes added") {
			return 0, nil
		}
		// Non-fatal: files are staged on disk; the user can `git commit` by
		// hand. Return the staged count rather than 0 so the CLI wrapper does
		// not mislead the user with "nothing to sync" (roborev #1720).
		fmt.Fprintf(stderr, "wipnote sync warning: git commit failed for %d staged file(s) — please commit manually: %s\n",
			len(files), strings.TrimSpace(outStr))
		return len(files), nil
	}

	fmt.Fprintf(out, "wipnote sync: committed %d file(s) — %s\n", len(files), msg)
	return len(files), nil
}

// dirtyWipnoteFiles returns repo-relative paths of any modified, untracked,
// or ignored files under .wipnote/ inside repoRoot, with pre-merge-backup
// snapshots filtered out.
//
// --ignored=traditional is critical (roborev #1720): wipnote installs a per-
// worktree exclude on .wipnote/ (excludeWipnoteFromWorktree, see
// internal/worktree). Without --ignored, every untracked .wipnote/ file in a
// linked worktree is invisible to `git status` and sync becomes a no-op in
// exactly the worktrees it is meant to flush. "traditional" (paired with
// --untracked-files=all) expands the excluded directory into individual file
// paths — "matching" would instead collapse to the directory pattern, giving
// us nothing to stage. Pairing this with `git add -f` (in runSync) lets sync
// see and stage worktree-excluded files while the same exclude continues to
// keep .wipnote/ noise out of normal `git status` output.
//
// The "-- .wipnote/" pathspec scopes the walk to wipnote artifacts only, so
// we don't surface unrelated ignored files elsewhere in the repo.
func dirtyWipnoteFiles(repoRoot string) ([]string, error) {
	out, err := exec.Command(
		"git", "-C", repoRoot, "status", "--porcelain",
		"--untracked-files=all", "--ignored=traditional",
		"--", ".wipnote/",
	).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git status: %s: %w", strings.TrimSpace(string(out)), err)
	}

	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		// Porcelain v1 format: "XY path" where XY is the 2-char status code.
		path := strings.TrimSpace(line[3:])
		// Rename entries have the shape "old -> new"; take the new path.
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = path[idx+len(" -> "):]
		}
		// Strip surrounding quotes that git uses for paths with special chars.
		path = strings.Trim(path, `"`)
		if !strings.HasPrefix(path, ".wipnote/") {
			continue
		}
		// Skip directory entries that `--ignored=matching` may emit when a
		// directory itself is excluded (e.g. ".wipnote/" with a trailing
		// slash). Only individual files should be staged.
		if strings.HasSuffix(path, "/") {
			continue
		}
		if preMergeBackupRe.MatchString(path) {
			continue
		}
		// Never sync volatile machine-local runtime state (finding 14).
		if isRuntimeStateFile(path) {
			continue
		}
		files = append(files, path)
	}
	return files, nil
}

// syncCommitMessage builds the commit subject. When the batch is exactly one
// work-item HTML file with a derivable ID, use "wipnote: sync <id>" — that
// gives `git log` a clean per-item trail for the common single-item case.
// Otherwise use "wipnote: sync <N> items".
func syncCommitMessage(files []string) string {
	if len(files) == 1 {
		if id := extractWorkItemID(files[0]); id != "" {
			return "wipnote: sync " + id
		}
	}
	return fmt.Sprintf("wipnote: sync %d items", len(files))
}

// extractWorkItemID returns the work-item ID encoded in the filename when
// path matches the canonical .wipnote/<type>s/<id>.html shape. Returns ""
// for paths that do not encode an ID (config files, placeholders, notes).
func extractWorkItemID(path string) string {
	m := workItemFileRe.FindStringSubmatch(path)
	if m == nil {
		return ""
	}
	return m[2]
}
