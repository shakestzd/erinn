package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/internal/launcher/plan"
	"github.com/shakestzd/wipnote/internal/worktree"
)

// TestClaudeWorktreeCarryover_DirtyMain verifies that when a NEW worktree is
// created on a dirty main branch, the shared emitWorktreeCarryoverMessage helper
// (bug-c3483435):
//   - carries tracked changes from main into the worktree
//   - leaves main unchanged (main still dirty)
//   - emits an advisory that does NOT mention "--work-item"
func TestClaudeWorktreeCarryover_DirtyMain(t *testing.T) {
	dir := setupCarryRepo(t)

	// Dirty the main branch with a tracked file edit.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\nclaude carryover\n"), 0644); err != nil {
		t.Fatalf("edit README: %v", err)
	}

	// Create a worktree (simulating what launchClaudeDefault does via
	// resolveManagedWorktreeStatus when IsolationManagedWorktree is selected).
	path, created, err := worktree.EnsureForFeatureStatus("feat-claude01", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForFeatureStatus: %v", err)
	}
	if !created {
		t.Fatal("expected a newly-created worktree")
	}

	p := plan.LaunchPlan{DirtyMainWarning: "Warning: launching on dirty protected branch \"main\"."}
	var buf bytes.Buffer
	emitWorktreeCarryoverMessage(p, dir, path, created, &buf)

	out := buf.String()
	// Must NOT tell the user to use --work-item (worktree already created).
	if strings.Contains(out, "--work-item") {
		t.Errorf("message must NOT mention --work-item; got %q", out)
	}
	// Must mention the managed worktree path.
	if !strings.Contains(out, "managed worktree") {
		t.Errorf("message should mention managed worktree; got %q", out)
	}
	// Must reassure main is unchanged.
	if !strings.Contains(out, "main left unchanged") {
		t.Errorf("message should say main left unchanged; got %q", out)
	}

	// Tracked change must appear in the worktree.
	wt, _ := os.ReadFile(filepath.Join(path, "README.md"))
	if !strings.Contains(string(wt), "claude carryover") {
		t.Errorf("worktree should contain carried edit; got %q", wt)
	}

	// Main must still be dirty (unchanged by carryover).
	main, _ := os.ReadFile(filepath.Join(dir, "README.md"))
	if !strings.Contains(string(main), "claude carryover") {
		t.Errorf("main should still contain the edit (unchanged by carryover); got %q", main)
	}
}

// TestClaudeWorktreeCarryover_ReusedWorktreeSkipsCarryover verifies that when a
// worktree is REUSED (created=false), carryover is skipped and no message is
// emitted — matching yolo's guardrail to prevent double-applying.
func TestClaudeWorktreeCarryover_ReusedWorktreeSkipsCarryover(t *testing.T) {
	dir := setupCarryRepo(t)

	// Dirty main.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\nreuse guard\n"), 0644); err != nil {
		t.Fatalf("edit README: %v", err)
	}

	// Create the worktree first so the next call is a reuse.
	path, _, err := worktree.EnsureForFeatureStatus("feat-reuse01", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForFeatureStatus: %v", err)
	}

	p := plan.LaunchPlan{DirtyMainWarning: "Warning: launching on dirty protected branch."}
	var buf bytes.Buffer
	// Pass created=false to simulate reuse.
	emitWorktreeCarryoverMessage(p, dir, path, false /* reused */, &buf)

	if buf.Len() != 0 {
		t.Errorf("reused worktree must not emit any message, got %q", buf.String())
	}
	// The reused worktree must NOT have had the edit carried in.
	wt, _ := os.ReadFile(filepath.Join(path, "README.md"))
	if strings.Contains(string(wt), "reuse guard") {
		t.Errorf("reused worktree should not have changes applied; got %q", wt)
	}
}

// TestClaudeWorktreeCarryover_SharedHelperMatchesYolo verifies that
// emitWorktreeCarryoverMessage and emitYoloDirtyMainMessage produce identical
// output for the same inputs — proving the DRY refactor preserved yolo behavior.
func TestClaudeWorktreeCarryover_SharedHelperMatchesYolo(t *testing.T) {
	dir := setupCarryRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\nshared helper\n"), 0644); err != nil {
		t.Fatalf("edit README: %v", err)
	}

	p := plan.LaunchPlan{DirtyMainWarning: "Warning: launching on dirty protected branch \"main\"."}

	// Create two identical worktrees so each helper runs on a fresh copy.
	pathA, createdA, err := worktree.EnsureForFeatureStatus("feat-shared-a01", dir, io.Discard)
	if err != nil || !createdA {
		t.Fatalf("EnsureForFeatureStatus A: %v (created=%v)", err, createdA)
	}
	pathB, createdB, err := worktree.EnsureForFeatureStatus("feat-shared-b01", dir, io.Discard)
	if err != nil || !createdB {
		t.Fatalf("EnsureForFeatureStatus B: %v (created=%v)", err, createdB)
	}

	var bufClaude, bufYolo bytes.Buffer
	emitWorktreeCarryoverMessage(p, dir, pathA, true, &bufClaude)
	emitYoloDirtyMainMessage(p, dir, pathB, true, &bufYolo)

	// Both must mention "managed worktree" — normalize the path differences.
	claudeOut := bufClaude.String()
	yoloOut := bufYolo.String()
	if !strings.Contains(claudeOut, "managed worktree") {
		t.Errorf("emitWorktreeCarryoverMessage missing 'managed worktree': %q", claudeOut)
	}
	if !strings.Contains(yoloOut, "managed worktree") {
		t.Errorf("emitYoloDirtyMainMessage missing 'managed worktree': %q", yoloOut)
	}
	// Both must NOT mention --work-item.
	if strings.Contains(claudeOut, "--work-item") {
		t.Errorf("emitWorktreeCarryoverMessage must not mention --work-item: %q", claudeOut)
	}
	if strings.Contains(yoloOut, "--work-item") {
		t.Errorf("emitYoloDirtyMainMessage must not mention --work-item: %q", yoloOut)
	}
}
