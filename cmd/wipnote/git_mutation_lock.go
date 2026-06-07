package main

import (
	"path/filepath"

	"github.com/gofrs/flock"

	"github.com/shakestzd/wipnote/core/storage"
)

// Repo-scoped advisory lock for wipnote-owned Git mutations.
//
// WHY: Concurrent wipnote processes (multiple agents/CLIs, autocommit on
// complete, etc.) can race into git's index-writing path simultaneously. Git's
// own index.lock then makes one of them fail with transient contention, which
// runGitWithLockRetry (cmd/wipnote/git_lock_retry.go) papers over with a backoff
// loop. That backoff is necessary but reactive: it only kicks in AFTER the
// collision. runGitMutation adds a proactive layer — it serializes wipnote's
// OWN mutations behind a single repo-scoped advisory lock so they never reach
// git's index.lock contention path against each other in the first place.
//
// SCOPE / COMPLEMENTARY LAYER: The advisory lock ONLY serializes wipnote's own
// processes. External writers (IDE git integration, gitstatus daemon, the user
// running `git` by hand) do NOT honor it. Therefore runGitWithLockRetry is
// RETAINED, not replaced — runGitMutation acquires the advisory lock and then
// runs the command through runGitWithLockRetry inside the locked section, so
// external-writer contention is still handled by the backoff loop. The advisory
// lock sits in front of runGitWithLockRetry; it does not supersede it.
//
// LIBRARY: We use github.com/gofrs/flock (a maintained cross-platform wrapper
// over flock(2) on Unix and LockFileEx on Windows) rather than hand-rolling
// platform syscalls. A key property of flock(2)/LockFileEx — and the reason we
// chose an advisory OS lock over a pidfile — is that the kernel AUTO-RELEASES
// the lock when the holding process exits or is killed for any reason. The lock
// therefore can NEVER go stale: a wipnote process killed mid-commit releases the
// lock automatically and the next process proceeds without manual cleanup.
//
// LOCK FILE LOCATION: The lock lives in wipnote's per-user cache directory —
// the SAME directory that already holds the per-repo SQLite read-index
// (~/.cache/wipnote/<path-hash>/). We derive it from storage.CanonicalDBPath so
// the path-hash keying is reused, not re-derived, and so the lock is keyed to
// the real (symlink-resolved) repo path. Rationale for this location over
// .wipnote/ or .git/:
//   - It is NEVER inside .git/ (a mutation lock inside the dir git itself is
//     mutating would be self-defeating and risks confusing git tooling).
//   - It is outside the working tree, so it can never be accidentally staged or
//     committed (unlike a file in .wipnote/).
//   - It is per-user and per-repo-path, matching the lock's actual granularity:
//     one lock per repository for one user's concurrent wipnote processes.
//
// Granularity is a single lock per repo (repo-scoped, not per-file) so that all
// index/ref-writing mutations for the repo serialize against one another.

// gitMutationLockPath returns the absolute path to the repo-scoped advisory
// lock file for repoRoot. It is a package-level seam so concurrency tests can
// point all simulated processes at one shared temp lock file without touching
// the real cache dir. Production code never reassigns it.
var gitMutationLockPath = func(repoRoot string) (string, error) {
	dbPath, err := storage.CanonicalDBPath(repoRoot)
	if err != nil {
		return "", err
	}
	// Sibling of the SQLite DB in ~/.cache/wipnote/<path-hash>/.
	return filepath.Join(filepath.Dir(dbPath), "git-mutation.lock"), nil
}

// runGitMutation runs an index/ref-writing git command anchored to repoRoot
// while holding the repo-scoped advisory lock, then delegates to
// runGitWithLockRetry for the actual execution (so the external-writer backoff
// layer is preserved inside the locked section). The lock is released via defer
// before returning — and is auto-released by the kernel if the process dies.
//
// Only mutating commands (git add / git commit / ref updates) need to go
// through here. Read-only commands (git diff, git log, git status) do not take
// the lock.
//
// If the lock file path cannot be resolved or the lock cannot be acquired, the
// command is still run through runGitWithLockRetry rather than failing the
// mutation outright: the advisory lock is an optimization over git's own
// contention handling, not a correctness prerequisite. Losing it degrades to
// the pre-existing behavior (runGitWithLockRetry alone) rather than blocking
// the user's work.
func runGitMutation(repoRoot string, args ...string) ([]byte, error) {
	return withGitMutationLock(repoRoot, func() ([]byte, error) {
		return runGitWithLockRetry(repoRoot, args...)
	})
}

// runGitMutationBatch runs a SEQUENCE of mutating git commands under a SINGLE
// advisory-lock acquisition, so the whole sequence is atomic with respect to
// other wipnote git writers. Use it when several mutations must not be
// interleaved — notably `git add` then `git commit` of the same paths: if the
// lock were released between them (two separate runGitMutation calls), another
// wipnote mutation could commit or alter the staged artifacts under the wrong
// message (roborev finding on feat-76504033).
//
// It stops at the FIRST command that errors, returning that command's combined
// output and error. If every command succeeds it returns the LAST command's
// output. Each command still runs through runGitWithLockRetry inside the lock,
// preserving the external-writer backoff layer.
func runGitMutationBatch(repoRoot string, cmds ...[]string) ([]byte, error) {
	return withGitMutationLock(repoRoot, func() ([]byte, error) {
		var out []byte
		var err error
		for _, args := range cmds {
			if out, err = runGitWithLockRetry(repoRoot, args...); err != nil {
				return out, err
			}
		}
		return out, err
	})
}

// withGitMutationLock acquires the repo-scoped advisory lock, runs fn, and
// releases the lock on return (the kernel also auto-releases on process death).
// If the lock path cannot be resolved or acquired, fn is run anyway — the
// advisory lock is an optimization over git's own contention handling, not a
// correctness prerequisite, so losing it degrades to runGitWithLockRetry alone
// rather than blocking the user's work.
func withGitMutationLock(repoRoot string, fn func() ([]byte, error)) ([]byte, error) {
	lockPath, err := gitMutationLockPath(repoRoot)
	if err != nil {
		return fn()
	}

	// Ensure the parent dir exists; CanonicalDBPath's dir may not have been
	// created yet (DB lazily created on first index). flock needs the dir.
	if mkErr := storage.EnsureDBDir(lockPath); mkErr != nil {
		return fn()
	}

	lock := flock.New(lockPath)
	// Blocking Lock: serialize wipnote's own concurrent mutations rather than
	// failing fast — the whole point is that the second process WAITS for the
	// first to finish its index write instead of colliding on git's index.lock.
	if lockErr := lock.Lock(); lockErr != nil {
		return fn()
	}
	// flock auto-releases on process death; this defer covers the normal path.
	defer func() {
		_ = lock.Unlock()
	}()

	return fn()
}
