package main

import (
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
