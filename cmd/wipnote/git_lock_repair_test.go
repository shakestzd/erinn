package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- helpers ----

func noLiveWriter(_ string) bool  { return false }
func hasLiveWriter(_ string) bool { return true }

// makeFakeLock creates a lock file at path with a specific mtime age ago.
func makeFakeLock(t *testing.T, dir, name string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte{}, 0o644); err != nil {
		t.Fatalf("makeFakeLock: %v", err)
	}
	now := time.Now()
	mtime := now.Add(-age)
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatalf("makeFakeLock chtimes: %v", err)
	}
	return p
}

// ---- TestDetectGitLocks ----

func TestDetectGitLocks_NoLocks(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	locks := detectGitLocks(gitDir)
	if len(locks) != 0 {
		t.Errorf("expected no locks, got %d", len(locks))
	}
}

func TestDetectGitLocks_FindsIndexLock(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeFakeLock(t, gitDir, "index.lock", 15*time.Minute)
	locks := detectGitLocks(gitDir)
	if len(locks) != 1 {
		t.Fatalf("expected 1 lock, got %d", len(locks))
	}
	if locks[0].Name != "index.lock" {
		t.Errorf("expected index.lock, got %s", locks[0].Name)
	}
}

func TestDetectGitLocks_FindsMultipleLocks(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeFakeLock(t, gitDir, "index.lock", 20*time.Minute)
	makeFakeLock(t, gitDir, "config.lock", 5*time.Minute)
	locks := detectGitLocks(gitDir)
	if len(locks) != 2 {
		t.Errorf("expected 2 locks, got %d", len(locks))
	}
}

// TestDetectGitLocks_ScansMultipleDirsAndDedupes models a linked worktree: the
// per-worktree git dir holds index.lock while the shared common dir holds
// config.lock. detectGitLocks must report locks from BOTH dirs (roborev #3641)
// and must not double-count when the same dir is passed twice.
func TestDetectGitLocks_ScansMultipleDirsAndDedupes(t *testing.T) {
	base := t.TempDir()
	perWorktree := filepath.Join(base, ".git", "worktrees", "feat-abc")
	common := filepath.Join(base, ".git")
	if err := os.MkdirAll(perWorktree, 0o755); err != nil {
		t.Fatal(err)
	}
	makeFakeLock(t, perWorktree, "index.lock", 20*time.Minute)
	makeFakeLock(t, common, "config.lock", 20*time.Minute)

	locks := detectGitLocks(perWorktree, common)
	if len(locks) != 2 {
		t.Fatalf("expected locks from both dirs, got %d: %+v", len(locks), locks)
	}
	names := map[string]bool{}
	for _, l := range locks {
		names[l.Name] = true
	}
	if !names["index.lock"] || !names["config.lock"] {
		t.Errorf("expected both index.lock (per-worktree) and config.lock (common), got %+v", locks)
	}

	// De-dup: passing the same dir twice must not double-count.
	deduped := detectGitLocks(common, common)
	if len(deduped) != 1 {
		t.Errorf("expected de-dup to 1 lock for repeated dir, got %d", len(deduped))
	}
}

// ---- TestLockAge ----

func TestLockAge_AboveThreshold(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	age := 20 * time.Minute
	makeFakeLock(t, gitDir, "index.lock", age)
	locks := detectGitLocks(gitDir)
	if len(locks) != 1 {
		t.Fatalf("expected 1 lock")
	}
	now := time.Now()
	measured := now.Sub(locks[0].ModTime)
	// allow 5s slop
	if measured < age-5*time.Second || measured > age+5*time.Second {
		t.Errorf("age %v not close to expected %v", measured, age)
	}
}

// ---- TestReportGitLockState (dry-run, non-destructive) ----

func TestReportGitLockState_DryRunReportsStale(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockPath := makeFakeLock(t, gitDir, "index.lock", 20*time.Minute)

	// Confirm lock still exists before report
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatal("lock should exist before report")
	}

	now := time.Now()
	out := reportGitLockStateWith(dir, []string{gitDir}, now, noLiveWriter, defaultMaxLockAge)

	// Must still exist (no deletion)
	if _, err := os.Stat(lockPath); err != nil {
		t.Error("dry-run must not delete the lock file")
	}
	if !strings.Contains(out, "index.lock") {
		t.Errorf("report should mention index.lock, got:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "stale") {
		t.Errorf("report should indicate stale, got:\n%s", out)
	}
	if strings.Contains(strings.ToLower(out), "deleted") || strings.Contains(strings.ToLower(out), "removed") {
		t.Errorf("dry-run report must not say deleted/removed:\n%s", out)
	}
}

func TestReportGitLockState_NoLocks(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	out := reportGitLockStateWith(dir, []string{gitDir}, now, noLiveWriter, defaultMaxLockAge)
	if !strings.Contains(out, "no lock files") {
		t.Errorf("expected 'no lock files', got:\n%s", out)
	}
}

func TestReportGitLockState_ActiveWriter(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeFakeLock(t, gitDir, "index.lock", 20*time.Minute)
	now := time.Now()
	out := reportGitLockStateWith(dir, []string{gitDir}, now, hasLiveWriter, defaultMaxLockAge)
	if !strings.Contains(strings.ToLower(out), "active") && !strings.Contains(strings.ToLower(out), "live") {
		t.Errorf("expected active/live writer mention, got:\n%s", out)
	}
}

// ---- TestRepairGitLock (--fix path) ----

// TestRepairGitLock_NeverDeletesSharedCommonDirLock is the regression for the
// roborev #3659 HIGH: a stale lock in the common (shared) git dir must NOT be
// removed by --fix even when the local worktree shows no live writer, because it
// may be owned by the main worktree or another linked worktree. The per-worktree
// lock in the same pass IS removed, proving the queue still drains.
func TestRepairGitLock_NeverDeletesSharedCommonDirLock(t *testing.T) {
	base := t.TempDir()
	perWorktree := filepath.Join(base, ".git", "worktrees", "feat-abc")
	common := filepath.Join(base, ".git")
	if err := os.MkdirAll(perWorktree, 0o755); err != nil {
		t.Fatal(err)
	}
	wtLock := makeFakeLock(t, perWorktree, "index.lock", 20*time.Minute)
	sharedLock := makeFakeLock(t, common, "config.lock", 20*time.Minute)

	now := time.Now()
	// gitDirs[0] is the primary (per-worktree) dir; common is shared. No local
	// live writer in either liveness check.
	repaired, skipped, err := repairGitLocksWith(
		[]string{perWorktree, common}, perWorktree, now, noLiveWriter, noLiveWriter, defaultMaxLockAge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repaired != 1 {
		t.Errorf("expected exactly the per-worktree lock removed (1), got %d", repaired)
	}
	if skipped != 1 {
		t.Errorf("expected the shared common-dir lock skipped (1), got %d", skipped)
	}
	if _, statErr := os.Stat(wtLock); !os.IsNotExist(statErr) {
		t.Error("per-worktree index.lock should have been removed")
	}
	if _, statErr := os.Stat(sharedLock); statErr != nil {
		t.Error("shared common-dir config.lock must NOT be removed (#3659)")
	}
}

func TestRepairGitLock_DeletesStaleWhenNoLiveWriter(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockPath := makeFakeLock(t, gitDir, "index.lock", 20*time.Minute)

	now := time.Now()
	// Both initial scan and final re-check: no live writer
	repaired, skipped, err := repairGitLocksWith([]string{gitDir}, gitDir, now, noLiveWriter, noLiveWriter, defaultMaxLockAge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repaired != 1 {
		t.Errorf("expected 1 repaired, got %d", repaired)
	}
	if skipped != 0 {
		t.Errorf("expected 0 skipped, got %d", skipped)
	}
	if _, statErr := os.Stat(lockPath); !os.IsNotExist(statErr) {
		t.Error("expected lock file to be deleted")
	}
}

func TestRepairGitLock_RefusesWhenLiveWriterDetectedInitially(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockPath := makeFakeLock(t, gitDir, "index.lock", 20*time.Minute)

	now := time.Now()
	repaired, skipped, err := repairGitLocksWith([]string{gitDir}, gitDir, now, hasLiveWriter, hasLiveWriter, defaultMaxLockAge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repaired != 0 {
		t.Errorf("expected 0 repaired, got %d", repaired)
	}
	if skipped != 1 {
		t.Errorf("expected 1 skipped, got %d", skipped)
	}
	if _, statErr := os.Stat(lockPath); statErr != nil {
		t.Error("lock file must NOT be deleted when live writer present")
	}
}

func TestRepairGitLock_RefusesWhenAgeBelowThreshold(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Young lock: 2 minutes old, threshold is 10 minutes
	lockPath := makeFakeLock(t, gitDir, "index.lock", 2*time.Minute)

	now := time.Now()
	repaired, skipped, err := repairGitLocksWith([]string{gitDir}, gitDir, now, noLiveWriter, noLiveWriter, defaultMaxLockAge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repaired != 0 {
		t.Errorf("expected 0 repaired (too young), got %d", repaired)
	}
	if skipped != 1 {
		t.Errorf("expected 1 skipped, got %d", skipped)
	}
	if _, statErr := os.Stat(lockPath); statErr != nil {
		t.Error("young lock must NOT be deleted")
	}
}

func TestRepairGitLock_FinalRecheckAborts(t *testing.T) {
	// SAFETY: final pre-unlink re-check must fire.
	// Initial scan: no writer. Final re-check (immediately before unlink): writer appeared.
	// Must NOT delete.
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockPath := makeFakeLock(t, gitDir, "index.lock", 20*time.Minute)

	now := time.Now()
	// initialCheck: no live writer; finalRecheck: live writer appeared
	repaired, skipped, err := repairGitLocksWith([]string{gitDir}, gitDir, now, noLiveWriter, hasLiveWriter, defaultMaxLockAge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repaired != 0 {
		t.Errorf("expected 0 repaired when final re-check finds writer, got %d", repaired)
	}
	if skipped != 1 {
		t.Errorf("expected 1 skipped, got %d", skipped)
	}
	if _, statErr := os.Stat(lockPath); statErr != nil {
		t.Error("lock must NOT be deleted when final re-check detects live writer")
	}
}

func TestRepairGitLock_CustomMaxAge(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 3-minute-old lock, custom threshold of 2 minutes → should delete
	lockPath := makeFakeLock(t, gitDir, "index.lock", 3*time.Minute)
	now := time.Now()
	repaired, _, err := repairGitLocksWith([]string{gitDir}, gitDir, now, noLiveWriter, noLiveWriter, 2*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repaired != 1 {
		t.Errorf("expected 1 repaired with custom threshold, got %d", repaired)
	}
	if _, statErr := os.Stat(lockPath); !os.IsNotExist(statErr) {
		t.Error("expected lock to be deleted with custom threshold")
	}
}

// ---- TestDoctorIntegration: reportGitLockState wired into doctor ----

func TestDoctorReport_IncludesGitLockSection(t *testing.T) {
	// reportGitLockState must appear in the full doctor report
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	report := runDoctorReport(dir)
	if !strings.Contains(report, "git lock") {
		t.Errorf("doctor report should contain 'git lock' section, got:\n%s", report)
	}
}

// TestIsGitProcessArgv is the regression for roborev #3703: liveness detection
// must match real git processes by argv[0] basename, NOT any cmdline containing
// the substring "git" — otherwise `wipnote launcher git-lock --fix` detects
// itself and the repair self-blocks.
func TestIsGitProcessArgv(t *testing.T) {
	cases := []struct {
		argv []string
		want bool
	}{
		{[]string{"git", "commit"}, true},
		{[]string{"/usr/bin/git", "status"}, true},
		{[]string{"git-remote-https", "origin"}, true},
		{[]string{"wipnote", "launcher", "git-lock", "--fix"}, false},
		{[]string{"/home/u/.local/bin/wipnote", "launcher", "git-lock"}, false},
		{[]string{"bash", "-c", "git status"}, false}, // argv[0] is bash, not git
		{nil, false},
	}
	for _, c := range cases {
		if got := isGitProcessArgv(c.argv); got != c.want {
			t.Errorf("isGitProcessArgv(%v) = %v, want %v", c.argv, got, c.want)
		}
	}
}

// TestRepairGitLock_SharedLockNameNeverRemovedEvenInPrimaryDir is the regression
// for roborev #3711 (High): config.lock/packed-refs.lock are repository-common
// locks. Even in the main worktree (where --git-dir == --git-common-dir, so they
// scan from the primary dir), --fix must NOT remove them — another linked
// worktree the local liveness scan can't see may own them. Per-worktree
// index.lock in the same pass IS removed.
func TestRepairGitLock_SharedLockNameNeverRemovedEvenInPrimaryDir(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	idx := makeFakeLock(t, gitDir, "index.lock", 20*time.Minute)
	cfg := makeFakeLock(t, gitDir, "config.lock", 20*time.Minute)

	// Confirm config.lock is flagged Shared even from the primary (single) dir.
	for _, l := range detectGitLocks(gitDir) {
		if l.Name == "config.lock" && !l.Shared {
			t.Error("config.lock must be Shared even when scanned from the primary dir")
		}
		if l.Name == "index.lock" && l.Shared {
			t.Error("index.lock must NOT be Shared")
		}
	}

	repaired, skipped, err := repairGitLocksWith(
		[]string{gitDir}, dir, time.Now(), noLiveWriter, noLiveWriter, defaultMaxLockAge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repaired != 1 {
		t.Errorf("only the per-worktree index.lock should be removed, got repaired=%d", repaired)
	}
	if skipped != 1 {
		t.Errorf("config.lock (shared name) must be skipped, got skipped=%d", skipped)
	}
	if _, e := os.Stat(idx); !os.IsNotExist(e) {
		t.Error("index.lock should have been removed")
	}
	if _, e := os.Stat(cfg); e != nil {
		t.Error("config.lock must NOT be removed (shared lock name, #3711)")
	}
}

// TestGitLockCmd_RejectsNonPositiveMaxAge is the regression for roborev #3711
// (Low): the safety contract requires a positive age threshold.
func TestGitLockCmd_RejectsNonPositiveMaxAge(t *testing.T) {
	for _, age := range []string{"--max-age=0", "--max-age=-5"} {
		cmd := launcherGitLockCmd()
		cmd.SetArgs([]string{"--fix", age})
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "max-age") {
			t.Errorf("%s: expected a max-age validation error, got %v", age, err)
		}
	}
}
