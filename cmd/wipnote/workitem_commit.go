package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type workitemArtifactCommitPolicy string

const (
	workitemArtifactCommitPolicySeparate workitemArtifactCommitPolicy = "separate"
	workitemArtifactCommitPolicyDefer    workitemArtifactCommitPolicy = "defer"
)

// workitemArtifactCommitPolicyForEnv returns the commit policy requested by
// WIPNOTE_ARTIFACT_COMMIT_POLICY. defer is the default so deferred artifact
// commits are on by default; "separate" is an explicit legacy opt-in.
func workitemArtifactCommitPolicyForEnv() workitemArtifactCommitPolicy {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WIPNOTE_ARTIFACT_COMMIT_POLICY"))) {
	case "", string(workitemArtifactCommitPolicyDefer):
		return workitemArtifactCommitPolicyDefer
	case string(workitemArtifactCommitPolicySeparate):
		return workitemArtifactCommitPolicySeparate
	default:
		return workitemArtifactCommitPolicyDefer
	}
}

func workitemArtifactRelPath(typeName, id string) string {
	return filepath.Join(".wipnote", typeName+"s", id+".html")
}

func workitemArtifactCommitMessage(id, action string) string {
	if action == "" {
		action = "update"
	}
	return "wipnote: " + action + " " + id
}

// persistWorkitemArtifactTransition applies the configured artifact commit
// policy for non-transactional work-item transitions. "separate" preserves the
// legacy direct autocommit behavior; "defer" records a durable commit intent in
// the outbox and leaves git mutation for `wipnote commit-queue flush`.
func persistWorkitemArtifactTransition(wipnoteDir, typeName, id, action string) error {
	switch workitemArtifactCommitPolicyForEnv() {
	case workitemArtifactCommitPolicyDefer:
		return enqueueWorkitemArtifactCommitIntent(wipnoteDir, typeName, id, action)
	default:
		return commitWipnoteArtifact(wipnoteDir, typeName, id, action)
	}
}

func enqueueWorkitemArtifactCommitIntent(wipnoteDir, typeName, id, action string) error {
	repoRoot := filepath.Dir(wipnoteDir)

	absWipnote, err := filepath.Abs(wipnoteDir)
	if err != nil {
		absWipnote = wipnoteDir
	}
	if isTestTmpPath(absWipnote) {
		if os.Getenv("WIPNOTE_DEBUG") == "1" {
			fmt.Fprintf(stderr, "artifact commit defer skipped: path looks like a test temp dir: %s\n", absWipnote)
		}
		return nil
	}

	if !isGitRepo(repoRoot) {
		fmt.Fprintf(stderr, "artifact commit defer skipped: %s is not inside a git repository\n", repoRoot)
		return nil
	}

	relPath := workitemArtifactRelPath(typeName, id)
	msg := workitemArtifactCommitMessage(id, action)
	if err := recordCommitIntent(repoRoot, []string{relPath}, msg, id, action); err != nil {
		return fmt.Errorf("artifact commit defer: record intent for %s: %w", id, err)
	}
	return nil
}

// testTmpComponentRe matches any single path component that looks like a Go
// test scratch directory name. These directories live inside the project tree
// (to avoid /tmp noexec in devcontainers) but must never trigger real git
// mutations during test runs.
var testTmpComponentRe = regexp.MustCompile(`^\.(test-tmp|tmp-gotest)$`)

// isTestTmpPath returns true when absPath contains a component matching
// testTmpComponentRe or begins with the well-known prefixes "tmp/go-tmp/" or
// "tmp/gotmp.". This is a defense-in-depth guard: even if a test
// mis-configures isolation, real git commits are prevented.
func isTestTmpPath(absPath string) bool {
	// Walk every component of the path.
	for p := absPath; ; {
		dir, base := filepath.Split(filepath.Clean(p))
		if testTmpComponentRe.MatchString(base) {
			return true
		}
		// Also catch absolute components like "/tmp/go-tmp/" by checking
		// the full remaining prefix.
		if strings.Contains(absPath, "/tmp/go-tmp/") || strings.Contains(absPath, "/tmp/gotmp.") {
			return true
		}
		if dir == p || dir == "" || dir == "." {
			break
		}
		p = dir
	}
	return false
}

// commitWipnoteArtifact stages and commits the work-item HTML file for the
// given typeName and id to the main git repository that contains wipnoteDir.
//
// typeName is "feature", "bug", or "spike"; the HTML lives at
// .wipnote/<typeName>s/<id>.html relative to the project root.
//
// action is the state-transition verb embedded in the commit message —
// "create", "start", "complete", "reopen", "block" — producing messages like
// "wipnote: start feat-XYZ" or "wipnote: complete feat-XYZ". The action gives
// `git log` a clean per-transition trail for each work item.
//
// Design: `git -C <repoRoot>` anchors the command to the main repository even
// when the caller's shell CWD is inside a linked worktree. The per-worktree
// gitignore installed by excludeWipnoteFromWorktree (internal/worktree) only
// affects the worktree's own `git status` display; explicit paths passed to
// `git add --` bypass the exclude entirely, so the file is staged and committed
// in the main repo regardless of CWD.
//
// Non-fatal contract: if the project is not in a git repo, or if git commit
// fails for any reason (hook rejection, locked index, nothing to commit), the
// function logs to stderr and returns nil. The caller must not make completion
// of the work item depend on the git commit succeeding.
func commitWipnoteArtifact(wipnoteDir, typeName, id, action string) error {
	// Derive the repo root: wipnoteDir is .wipnote/ inside the project root.
	repoRoot := filepath.Dir(wipnoteDir)

	// Defense-in-depth: reject paths that look like Go test scratch
	// directories. These live inside the project tree (see .gitignore comment
	// "Go test scratch space") but must never trigger real git mutations.
	// Silent skip — this is never a caller error, just a mis-configured test.
	absWipnote, err := filepath.Abs(wipnoteDir)
	if err != nil {
		absWipnote = wipnoteDir
	}
	if isTestTmpPath(absWipnote) {
		if os.Getenv("WIPNOTE_DEBUG") == "1" {
			fmt.Fprintf(stderr, "autocommit skipped: path looks like a test temp dir: %s\n", absWipnote)
		}
		return nil
	}

	if !isGitRepo(repoRoot) {
		fmt.Fprintf(stderr, "autocommit skipped: %s is not inside a git repository\n", repoRoot)
		return nil
	}

	relPath := workitemArtifactRelPath(typeName, id)
	absPath := filepath.Join(repoRoot, relPath)
	msg := workitemArtifactCommitMessage(id, action)

	// Hold ONE advisory lock across the whole add → diff → commit sequence so no
	// other wipnote git mutation can interleave between staging and committing
	// this artifact (roborev #3713). Inside the lock we call runGitWithLockRetry
	// directly (the external-writer backoff layer) — not runGitMutation, which
	// would re-acquire the lock per call.
	var resultErr error
	_, lockErr := withGitMutationLock(repoRoot, func() ([]byte, error) {
		// Stage the file. Explicit path avoids sweeping unrelated changes.
		if addOut, err := runGitWithLockRetry(repoRoot, "add", "--", absPath); err != nil {
			resultErr = fmt.Errorf("autocommit: git add %s: %s: %w", relPath, strings.TrimSpace(string(addOut)), err)
			return nil, err
		}
		// Nothing staged for this file → nothing new to commit.
		if exec.Command("git", "-C", repoRoot, "diff", "--cached", "--quiet", "--", absPath).Run() == nil {
			return nil, nil
		}
		// Commit only the artifact file — never touch the broader index.
		if commitOut, err := runGitWithLockRetry(repoRoot, "commit", "-m", msg, "--", absPath); err != nil {
			outStr := string(commitOut)
			if strings.Contains(outStr, "nothing to commit") || strings.Contains(outStr, "no changes added") {
				return nil, nil
			}
			fmt.Fprintf(stderr, "autocommit warning: git commit failed for %s (artifact persisted to disk — please commit manually): %s\n",
				id, strings.TrimSpace(outStr))
		}
		return nil, nil
	})
	// Propagate a lock-wrapper/callback error if the callback did not already set
	// a more specific one, so a failure can never be reported as success (#3721).
	if resultErr == nil {
		resultErr = lockErr
	}
	return resultErr
}

// errReadOnlyFS is the sentinel returned (wrapped) by commitWipnoteArtifactStrict
// when git reports the index is on a read-only filesystem. Callers use
// errors.Is(err, errReadOnlyFS) to distinguish this environmental condition
// from real commit failures (hook rejection, lock contention, etc.).
var errReadOnlyFS = errors.New("git index is on a read-only filesystem")

// isReadOnlyFSError reports whether git subprocess output indicates the index
// could not be written because the filesystem is read-only. The signal comes
// from the git subprocess output string, not from a Go syscall error.
func isReadOnlyFSError(output string) bool {
	return strings.Contains(output, "Read-only file system")
}

// commitWipnoteArtifactStrict is the fatal-on-failure variant of
// commitWipnoteArtifact, used ONLY from the complete path so that a failed
// artifact commit can trigger a compensating re-open instead of leaving the
// item silently "done" with no durable record.
//
// It mirrors commitWipnoteArtifact's staging/commit logic but RETURNS an error
// when `git add` or `git commit` fails for a real reason (hook rejection,
// locked index, permission denied). The genuinely benign cases —
// non-git project, test-tmp path, or "nothing to commit" because the file is
// already committed and unchanged — are NOT failures: they return (false, nil)
// where the bool reports whether a new commit was actually created.
//
// The committed bool lets the caller distinguish a legitimate idempotent no-op
// (HEAD must NOT have advanced) from an expected commit (HEAD MUST advance),
// so the post-commit invariants do not false-fail the no-op path.
func commitWipnoteArtifactStrict(wipnoteDir, typeName, id, action string) (committed bool, err error) {
	repoRoot := filepath.Dir(wipnoteDir)

	absWipnote, aerr := filepath.Abs(wipnoteDir)
	if aerr != nil {
		absWipnote = wipnoteDir
	}
	if isTestTmpPath(absWipnote) {
		if os.Getenv("WIPNOTE_DEBUG") == "1" {
			fmt.Fprintf(stderr, "autocommit skipped: path looks like a test temp dir: %s\n", absWipnote)
		}
		return false, nil
	}

	if !isGitRepo(repoRoot) {
		fmt.Fprintf(stderr, "autocommit skipped: %s is not inside a git repository\n", repoRoot)
		return false, nil
	}

	relPath := workitemArtifactRelPath(typeName, id)
	absPath := filepath.Join(repoRoot, relPath)
	msg := workitemArtifactCommitMessage(id, action)

	// Hold ONE advisory lock across add → diff → commit so the staged-index
	// transaction cannot interleave with another wipnote git mutation (roborev
	// #3713). committed/resultErr are captured from inside the locked section.
	committed = false
	err = nil
	_, lockErr := withGitMutationLock(repoRoot, func() ([]byte, error) {
		if addOut, addErr := runGitWithLockRetry(repoRoot, "add", "--", absPath); addErr != nil {
			addOutStr := strings.TrimSpace(string(addOut))
			if isReadOnlyFSError(addOutStr) {
				err = fmt.Errorf("strict autocommit: git add %s: %s: %w", relPath, addOutStr, errReadOnlyFS)
			} else {
				err = fmt.Errorf("strict autocommit: git add %s: %s: %w", relPath, addOutStr, addErr)
			}
			return nil, addErr
		}
		// Nothing staged → legitimate idempotent no-op (committed stays false).
		if exec.Command("git", "-C", repoRoot, "diff", "--cached", "--quiet", "--", absPath).Run() == nil {
			return nil, nil
		}
		commitOut, commitErr := runGitWithLockRetry(repoRoot, "commit", "-m", msg, "--", absPath)
		if commitErr != nil {
			outStr := string(commitOut)
			if strings.Contains(outStr, "nothing to commit") || strings.Contains(outStr, "no changes added") {
				return nil, nil
			}
			outStrTrimmed := strings.TrimSpace(outStr)
			if isReadOnlyFSError(outStrTrimmed) {
				err = fmt.Errorf("strict autocommit: git commit failed for %s: %s: %w", id, outStrTrimmed, errReadOnlyFS)
			} else {
				err = fmt.Errorf("strict autocommit: git commit failed for %s: %s: %w", id, outStrTrimmed, commitErr)
			}
			return nil, commitErr
		}
		committed = true
		return nil, nil
	})
	// Propagate a lock-wrapper/callback error if none was captured (#3721).
	if err == nil {
		err = lockErr
	}
	return committed, err
}

// strictCommitFn is the injection seam for the strict artifact commit used by
// the transactional complete path. Tests override it to force a failure
// without needing a real read-only .git. Production code never reassigns it.
var strictCommitFn = commitWipnoteArtifactStrict

// artifactHeadCommit returns the SHA of the most recent commit that touched the
// work-item artifact at absPath, or "" when the file has no commit history yet
// (or git errors — treated as "no history" so a first-ever commit still reads
// as an advance). It anchors to repoRoot so it works from inside a worktree.
func artifactHeadCommit(repoRoot, absPath string) string {
	out, err := exec.Command(
		"git", "-C", repoRoot, "log", "-1", "--format=%H", "--", absPath,
	).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// artifactPathDirty reports whether the work-item artifact at absPath has any
// unstaged or uncommitted changes (i.e. `git status --porcelain` for that
// single path is non-empty). Used as post-commit invariant (a): after a
// successful strict commit the item's own artifact must be clean.
func artifactPathDirty(repoRoot, absPath string) bool {
	out, err := exec.Command(
		"git", "-C", repoRoot, "status", "--porcelain", "--", absPath,
	).CombinedOutput()
	if err != nil {
		// If we cannot determine cleanliness, treat as dirty so the
		// transactional path errs toward the compensating re-open.
		return true
	}
	return strings.TrimSpace(string(out)) != ""
}

// commitArtifactTransactional runs the strict artifact commit for the complete
// path and asserts the two post-commit invariants:
//
//	(a) the item's own artifact path is clean (no unstaged/uncommitted file)
//	(b) the artifact's HEAD commit advanced past preHead — but ONLY when a
//	    commit was actually expected (something was staged). A legitimate
//	    idempotent no-op (file already committed and unchanged) leaves HEAD
//	    unmoved and is NOT a failure.
//
// On any failure it returns a non-nil error; the caller is responsible for the
// compensating re-open. preHead is the artifact's HEAD SHA captured BEFORE
// col.Complete flushed the canonical HTML to disk.
func commitArtifactTransactional(wipnoteDir, typeName, id, preHead string) error {
	committed, err := strictCommitFn(wipnoteDir, typeName, id, "complete")
	if err != nil {
		return err
	}

	repoRoot := filepath.Dir(wipnoteDir)
	if !isGitRepo(repoRoot) {
		// Non-git project: the artifact lives on disk only; there is no
		// commit to assert. Completion proceeds (mirrors the non-fatal
		// contract's non-git skip — not a transactional failure).
		return nil
	}
	absPath := filepath.Join(wipnoteDir, typeName+"s", id+".html")

	if artifactPathDirty(repoRoot, absPath) {
		return fmt.Errorf("post-commit invariant (a) failed: %s still has uncommitted changes after strict commit", filepath.Join(".wipnote", typeName+"s", id+".html"))
	}

	if committed {
		postHead := artifactHeadCommit(repoRoot, absPath)
		if postHead == "" || postHead == preHead {
			return fmt.Errorf("post-commit invariant (b) failed: artifact HEAD did not advance for %s (pre=%q post=%q)", id, preHead, postHead)
		}
	}
	return nil
}

// checkUncommittedSourceCompleteGate refuses completion when tracked files
// outside .wipnote/ have uncommitted changes. Completion auto-commits only the
// work-item artifact, so allowing dirty source by default makes the "done"
// signal stronger than the durable implementation state.
func checkUncommittedSourceCompleteGate(wipnoteDir, id string, allowDirty bool) error {
	repoRoot := filepath.Dir(wipnoteDir)
	if !isGitRepo(repoRoot) {
		return nil
	}

	files, err := dirtyTrackedSourceFiles(repoRoot)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	if allowDirty {
		fmt.Fprintf(os.Stderr, "allow-dirty warning: completing %s with uncommitted source changes:\n", id)
		for _, f := range files {
			fmt.Fprintf(os.Stderr, "  %s\n", f)
		}
		return nil
	}

	return fmt.Errorf(
		"refusing to complete %s with uncommitted source changes outside .wipnote/:\n%s\n\nCommit the implementation first, for example:\n  git add %s && git commit -m %q\n\nTo bypass intentionally, rerun with --allow-dirty",
		id,
		formatPathList(files),
		strings.Join(shellQuotePaths(files), " "),
		id+": commit implementation",
	)
}

func dirtyTrackedSourceFiles(repoRoot string) ([]string, error) {
	out, err := exec.Command(
		"git", "-C", repoRoot, "status", "--porcelain=v1", "-z",
		"--untracked-files=no",
	).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git status: %s: %w", strings.TrimSpace(string(out)), err)
	}

	var files []string
	for _, entry := range parsePorcelainZ(out) {
		if entry.Path == ".claude/settings.local.json" {
			continue
		}
		if isWipnotePath(entry.Path) {
			continue
		}
		files = append(files, entry.Path)
	}
	sort.Strings(files)
	return files, nil
}

type porcelainEntry struct {
	Status string
	Path   string
}

func parsePorcelainZ(out []byte) []porcelainEntry {
	var entries []porcelainEntry
	for len(out) > 0 {
		nul := bytes.IndexByte(out, 0)
		if nul < 0 {
			break
		}
		record := string(out[:nul])
		out = out[nul+1:]
		if len(record) < 4 {
			continue
		}
		status := record[:2]
		path := filepath.ToSlash(record[3:])
		entries = append(entries, porcelainEntry{Status: status, Path: path})

		// In porcelain v1 -z, renamed/copied entries carry the destination in
		// the first record and the source path in the following NUL record.
		if strings.ContainsAny(status, "RC") {
			nul = bytes.IndexByte(out, 0)
			if nul < 0 {
				break
			}
			out = out[nul+1:]
		}
	}
	return entries
}

func isWipnotePath(path string) bool {
	path = filepath.ToSlash(path)
	return path == ".wipnote" || strings.HasPrefix(path, ".wipnote/")
}

func formatPathList(paths []string) string {
	var b strings.Builder
	for _, path := range paths {
		fmt.Fprintf(&b, "  %s\n", path)
	}
	return strings.TrimRight(b.String(), "\n")
}

func shellQuotePaths(paths []string) []string {
	quoted := make([]string, 0, len(paths))
	for _, path := range paths {
		quoted = append(quoted, shellQuote(path))
	}
	return quoted
}

func shellQuote(path string) string {
	if path != "" && strings.IndexFunc(path, func(r rune) bool {
		return !(r == '/' || r == '.' || r == '-' || r == '_' ||
			(r >= '0' && r <= '9') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= 'a' && r <= 'z'))
	}) < 0 {
		return path
	}
	return "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
}

// shouldAutocommitWorkitemArtifact returns true when a work-item type supports
// the artifact commit policy flow. Plans are excluded because they have their
// own atomic YAML+HTML commit path (commitPlanChange in plan_yaml_cmds.go);
// auto-committing only the rendered HTML would leave the authoritative YAML
// out of sync (roborev #1662). Future work-item types must opt in explicitly.
func shouldAutocommitWorkitemArtifact(typeName string) bool {
	switch typeName {
	case "feature", "bug", "spike":
		return true
	default:
		return false
	}
}

// actionFromStatus maps a wipnote work-item status value to the verb used in
// the auto-commit message ("wipnote: <action> <id>"). "in-progress" becomes
// "start" since that's the human-readable transition verb; "done" becomes
// "complete". Other statuses pass through verbatim, used as-is for messages
// like "wipnote: blocked <id>" or "wipnote: todo <id>" (rare resets).
func actionFromStatus(status string) string {
	switch status {
	case "in-progress":
		return "start"
	case "done":
		return "complete"
	default:
		return status
	}
}
