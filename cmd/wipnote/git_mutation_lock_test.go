package main

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

// TestRunGitMutation_InvokesRunGitWithLockRetryInsideLock verifies the advisory
// lock helper composes with — does NOT replace — runGitWithLockRetry: the
// external-writer retry layer must still run inside the locked section. We
// observe this by having gitRunner record that it was called and by asserting
// the repo-scoped flock was held for the duration of the runner call.
func TestRunGitMutation_InvokesRunGitWithLockRetryInsideLock(t *testing.T) {
	repoRoot := t.TempDir()

	origRunner := gitRunner
	origLockPath := gitMutationLockPath
	t.Cleanup(func() {
		gitRunner = origRunner
		gitMutationLockPath = origLockPath
	})

	lockFile := filepath.Join(repoRoot, "git-mutation.lock")
	gitMutationLockPath = func(_ string) (string, error) { return lockFile, nil }

	var ranInsideLock bool
	gitRunner = func(_ string, _ ...string) ([]byte, error) {
		// While runGitWithLockRetry executes, the advisory lock must be held,
		// so a fresh non-blocking TryLock from another flock handle must fail.
		probe := flock.New(lockFile)
		locked, err := probe.TryLock()
		if err == nil && !locked {
			ranInsideLock = true
		}
		if locked {
			_ = probe.Unlock()
		}
		return []byte("ok"), nil
	}

	out, err := runGitMutation(repoRoot, "add", "--", "x")
	if err != nil {
		t.Fatalf("runGitMutation returned err: %v", err)
	}
	if string(out) != "ok" {
		t.Errorf("output = %q, want %q", out, "ok")
	}
	if !ranInsideLock {
		t.Error("gitRunner did not execute while the advisory lock was held — lock not acquired before runGitWithLockRetry")
	}
}

// TestRunGitMutation_SerializesConcurrentProcesses verifies the repo-scoped
// advisory lock serializes concurrent wipnote mutations: with N goroutines
// (simulating N wipnote processes sharing one lock file) the critical section
// is never entered by two at once. maxConcurrent must stay at 1.
func TestRunGitMutation_SerializesConcurrentProcesses(t *testing.T) {
	repoRoot := t.TempDir()
	lockFile := filepath.Join(repoRoot, "git-mutation.lock")

	origRunner := gitRunner
	origLockPath := gitMutationLockPath
	t.Cleanup(func() {
		gitRunner = origRunner
		gitMutationLockPath = origLockPath
	})
	gitMutationLockPath = func(_ string) (string, error) { return lockFile, nil }

	var inCritical int32
	var maxConcurrent int32
	gitRunner = func(_ string, _ ...string) ([]byte, error) {
		cur := atomic.AddInt32(&inCritical, 1)
		for {
			prev := atomic.LoadInt32(&maxConcurrent)
			if cur <= prev || atomic.CompareAndSwapInt32(&maxConcurrent, prev, cur) {
				break
			}
		}
		// Hold the section briefly to widen the race window.
		time.Sleep(2 * time.Millisecond)
		atomic.AddInt32(&inCritical, -1)
		return nil, nil
	}

	const workers = 8
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := runGitMutation(repoRoot, "commit", "-m", "x"); err != nil {
				t.Errorf("runGitMutation err: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&maxConcurrent); got != 1 {
		t.Errorf("max concurrent critical-section entries = %d, want 1 (advisory lock did not serialize)", got)
	}
}

// TestRunGitMutation_AutoReleasesAfterReturn verifies the advisory lock is
// released when runGitMutation returns, so a subsequent acquisition succeeds.
// This mirrors flock's auto-release-on-handle-drop semantics: once we Unlock
// (via defer) the next caller may immediately acquire.
func TestRunGitMutation_AutoReleasesAfterReturn(t *testing.T) {
	repoRoot := t.TempDir()
	lockFile := filepath.Join(repoRoot, "git-mutation.lock")

	origRunner := gitRunner
	origLockPath := gitMutationLockPath
	t.Cleanup(func() {
		gitRunner = origRunner
		gitMutationLockPath = origLockPath
	})
	gitMutationLockPath = func(_ string) (string, error) { return lockFile, nil }
	gitRunner = func(_ string, _ ...string) ([]byte, error) { return nil, nil }

	if _, err := runGitMutation(repoRoot, "add", "--", "x"); err != nil {
		t.Fatalf("first mutation err: %v", err)
	}

	// After return the lock must be free: a non-blocking TryLock succeeds.
	probe := flock.New(lockFile)
	locked, err := probe.TryLock()
	if err != nil {
		t.Fatalf("TryLock err: %v", err)
	}
	if !locked {
		t.Fatal("advisory lock was NOT released after runGitMutation returned")
	}
	_ = probe.Unlock()
}

// TestRunGitMutation_PropagatesRunnerError verifies that a hard error from the
// underlying git command (not lock contention) propagates unchanged, so the
// lock layer is transparent to callers' existing error handling.
func TestRunGitMutation_PropagatesRunnerError(t *testing.T) {
	repoRoot := t.TempDir()
	lockFile := filepath.Join(repoRoot, "git-mutation.lock")

	origRunner := gitRunner
	origLockPath := gitMutationLockPath
	t.Cleanup(func() {
		gitRunner = origRunner
		gitMutationLockPath = origLockPath
	})
	gitMutationLockPath = func(_ string) (string, error) { return lockFile, nil }

	wantErr := errors.New("exit status 1")
	gitRunner = func(_ string, _ ...string) ([]byte, error) {
		return []byte("error: hook declined"), wantErr
	}

	out, err := runGitMutation(repoRoot, "commit", "-m", "x")
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if string(out) != "error: hook declined" {
		t.Errorf("output = %q", out)
	}
}
