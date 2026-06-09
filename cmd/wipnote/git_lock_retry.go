package main

import (
	"os/exec"
	"strings"
	"time"
)

// gitLockBackoff is the retry delay schedule for git index-lock contention.
// Three retries after the initial attempt: ~200ms, ~600ms, ~1800ms.
// Mirrors internal/db.DefaultBusyBackoff — same contention philosophy, same
// values, kept local so cmd/wipnote does not import internal/db.
var gitLockBackoff = []time.Duration{
	200 * time.Millisecond,
	600 * time.Millisecond,
	1800 * time.Millisecond,
}

// gitLockSleep is the sleep seam for tests. Production code never reassigns it.
var gitLockSleep = time.Sleep

// gitRunner is the command-execution seam for tests. It mirrors the real
// exec.Command("git", "-C", repoRoot, args...).CombinedOutput() call so tests
// can inject lock-contention or hard errors without a real git repo.
// Production code never reassigns it.
var gitRunner = func(repoRoot string, args ...string) ([]byte, error) {
	all := append([]string{"-C", repoRoot}, args...)
	return exec.Command("git", all...).CombinedOutput()
}

// isGitLockContention returns true when the combined output of a git command
// indicates a transient index-lock conflict — either the classic lock-file
// collision ("index.lock" + "File exists" | "Unable to create") or the
// "Another git process seems to be running" message. Only these specific
// signatures are matched; generic failures (hook rejection, permission denied,
// nothing-to-commit) return false and are never retried.
func isGitLockContention(output string) bool {
	if strings.Contains(output, "Another git process seems to be running") {
		return true
	}
	if strings.Contains(output, "index.lock") &&
		(strings.Contains(output, "File exists") || strings.Contains(output, "Unable to create")) &&
		!strings.Contains(output, "Read-only file system") {
		return true
	}
	return false
}

// runGitWithLockRetry runs a git command anchored to repoRoot and retries
// transparently when the command fails with a transient index-lock contention
// error. It makes one initial attempt plus len(gitLockBackoff) retries,
// sleeping the corresponding backoff duration between attempts. The first
// non-lock result (success or hard error) is returned immediately. After all
// retries are exhausted, the last output+error is returned to the caller.
//
// ONLY index-lock contention is retried — every other error (hook rejection,
// permission denied, "nothing to commit") is returned immediately with no
// sleep, preserving all existing post-call logic unchanged.
//
// The lock file is never deleted; the holder (gitstatus daemon, IDE) releases
// it within milliseconds and retrying is sufficient.
func runGitWithLockRetry(repoRoot string, args ...string) ([]byte, error) {
	out, err := gitRunner(repoRoot, args...)
	if err == nil || !isGitLockContention(string(out)) {
		return out, err
	}
	for _, d := range gitLockBackoff {
		gitLockSleep(d)
		out, err = gitRunner(repoRoot, args...)
		if err == nil || !isGitLockContention(string(out)) {
			return out, err
		}
	}
	return out, err
}
