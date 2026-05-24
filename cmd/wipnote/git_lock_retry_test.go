package main

import (
	"errors"
	"testing"
	"time"
)

// TestIsGitLockContention_TableDriven covers the lock-signature detector with
// real-world git error messages and benign outputs that must never be retried.
func TestIsGitLockContention_TableDriven(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "classic index.lock File exists",
			output: "fatal: Unable to create '/repo/.git/index.lock': File exists.\n\nAnother git process seems to be running in this repository, e.g.\nan editor opened by 'git commit'.",
			want:   true,
		},
		{
			name:   "index.lock Unable to create variant",
			output: "fatal: Unable to create '/home/user/project/.git/index.lock': File exists",
			want:   true,
		},
		{
			name:   "Another git process seems to be running standalone",
			output: "Another git process seems to be running in this repository",
			want:   true,
		},
		{
			name:   "index.lock without qualifying phrase",
			output: "fatal: index.lock was deleted",
			want:   false,
		},
		{
			name:   "nothing to commit",
			output: "On branch main\nnothing to commit, working tree clean",
			want:   false,
		},
		{
			name:   "no changes added",
			output: "no changes added to commit (use \"git add\" and/or \"git commit -a\")",
			want:   false,
		},
		{
			name:   "hook declined",
			output: "error: hook declined to update refs/heads/main",
			want:   false,
		},
		{
			name:   "permission denied",
			output: "error: open(\".git/COMMIT_EDITMSG\"): Permission denied",
			want:   false,
		},
		{
			name:   "empty string",
			output: "",
			want:   false,
		},
		{
			name:   "generic git error",
			output: "fatal: not a git repository (or any of the parent directories): .git",
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isGitLockContention(tc.output); got != tc.want {
				t.Errorf("isGitLockContention(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}

// TestRunGitWithLockRetry_RetriesOnLockThenSucceeds injects a runner that
// returns lock contention twice then succeeds. The helper must retry (with
// stubbed zero-duration sleeps) and return the success result. The runner must
// be called exactly 3 times.
func TestRunGitWithLockRetry_RetriesOnLockThenSucceeds(t *testing.T) {
	lockOutput := []byte("fatal: Unable to create '/repo/.git/index.lock': File exists")
	lockErr := errors.New("exit status 128")
	successOutput := []byte("")
	var callCount int

	origRunner := gitRunner
	origSleep := gitLockSleep
	origBackoff := gitLockBackoff
	t.Cleanup(func() {
		gitRunner = origRunner
		gitLockSleep = origSleep
		gitLockBackoff = origBackoff
	})

	var sleepCalls []time.Duration
	gitLockSleep = func(d time.Duration) { sleepCalls = append(sleepCalls, d) }
	gitLockBackoff = []time.Duration{0, 0, 0} // three retries, no real wait

	gitRunner = func(_ string, _ ...string) ([]byte, error) {
		callCount++
		if callCount <= 2 {
			return lockOutput, lockErr
		}
		return successOutput, nil
	}

	out, err := runGitWithLockRetry("/fake/repo", "add", "--", "/fake/repo/.wipnote/bugs/bug-1.html")
	if err != nil {
		t.Errorf("expected success after retries, got err: %v", err)
	}
	if string(out) != "" {
		t.Errorf("expected empty success output, got: %q", out)
	}
	if callCount != 3 {
		t.Errorf("runner called %d times, want 3 (initial + 2 retries)", callCount)
	}
	if len(sleepCalls) != 2 {
		t.Errorf("sleep called %d times, want 2", len(sleepCalls))
	}
}

// TestRunGitWithLockRetry_NoRetryOnNonLockError injects a runner that returns
// a non-lock error (hook rejection). The helper must return immediately after
// the first call — no sleeps, no retries — preserving the existing hard-fail
// contract for real errors.
func TestRunGitWithLockRetry_NoRetryOnNonLockError(t *testing.T) {
	hookOutput := []byte("error: hook declined to update refs/heads/main")
	hookErr := errors.New("exit status 1")
	var callCount int

	origRunner := gitRunner
	origSleep := gitLockSleep
	origBackoff := gitLockBackoff
	t.Cleanup(func() {
		gitRunner = origRunner
		gitLockSleep = origSleep
		gitLockBackoff = origBackoff
	})

	var sleepCalls int
	gitLockSleep = func(_ time.Duration) { sleepCalls++ }
	gitLockBackoff = []time.Duration{0, 0, 0}

	gitRunner = func(_ string, _ ...string) ([]byte, error) {
		callCount++
		return hookOutput, hookErr
	}

	out, err := runGitWithLockRetry("/fake/repo", "commit", "-m", "wipnote: complete bug-1", "--", "/fake/repo/.wipnote/bugs/bug-1.html")
	if err == nil {
		t.Error("expected non-nil error for hook rejection, got nil")
	}
	if string(out) != string(hookOutput) {
		t.Errorf("output = %q, want %q", out, hookOutput)
	}
	if callCount != 1 {
		t.Errorf("runner called %d times, want exactly 1 (no retry for non-lock error)", callCount)
	}
	if sleepCalls != 0 {
		t.Errorf("sleep called %d times, want 0 (no retry for non-lock error)", sleepCalls)
	}
}

// TestRunGitWithLockRetry_ExhaustsBackoffAndReturnsLastError verifies that
// when every attempt returns lock contention, the helper returns the last
// error after exhausting the backoff schedule (not a special sentinel error).
func TestRunGitWithLockRetry_ExhaustsBackoffAndReturnsLastError(t *testing.T) {
	lockOutput := []byte("fatal: Unable to create '/repo/.git/index.lock': File exists")
	lockErr := errors.New("exit status 128")
	var callCount int

	origRunner := gitRunner
	origSleep := gitLockSleep
	origBackoff := gitLockBackoff
	t.Cleanup(func() {
		gitRunner = origRunner
		gitLockSleep = origSleep
		gitLockBackoff = origBackoff
	})

	gitLockSleep = func(_ time.Duration) {}
	gitLockBackoff = []time.Duration{0, 0, 0} // 3 retries

	gitRunner = func(_ string, _ ...string) ([]byte, error) {
		callCount++
		return lockOutput, lockErr
	}

	out, err := runGitWithLockRetry("/fake/repo", "add", "--", "/fake/repo/.wipnote/bugs/bug-1.html")
	if err == nil {
		t.Error("expected non-nil error when all retries exhausted, got nil")
	}
	if string(out) != string(lockOutput) {
		t.Errorf("output = %q, want %q", out, lockOutput)
	}
	// 1 initial + 3 retries = 4 total
	if callCount != 4 {
		t.Errorf("runner called %d times, want 4 (1 initial + 3 retries)", callCount)
	}
}

// TestRunGitWithLockRetry_SuccessOnFirstAttemptNoSleep verifies that a
// first-attempt success incurs no sleep and exactly one runner call.
func TestRunGitWithLockRetry_SuccessOnFirstAttemptNoSleep(t *testing.T) {
	origRunner := gitRunner
	origSleep := gitLockSleep
	t.Cleanup(func() {
		gitRunner = origRunner
		gitLockSleep = origSleep
	})

	var sleepCalls int
	var callCount int
	gitLockSleep = func(_ time.Duration) { sleepCalls++ }
	gitRunner = func(_ string, _ ...string) ([]byte, error) {
		callCount++
		return []byte(""), nil
	}

	_, err := runGitWithLockRetry("/fake/repo", "add", "--", "file.html")
	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
	if callCount != 1 {
		t.Errorf("runner called %d times, want 1", callCount)
	}
	if sleepCalls != 0 {
		t.Errorf("sleep called %d times, want 0", sleepCalls)
	}
}
