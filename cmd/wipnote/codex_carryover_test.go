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

// TestCodexWorktreeCarryover_DirtyMain verifies that when a NEW worktree is
// created on a dirty main branch for a codex launch, the shared
// emitWorktreeCarryoverMessage helper (bug-938e56ae):
//   - carries tracked changes from main into the worktree
//   - leaves main unchanged (main still dirty)
//   - emits an advisory that does NOT mention "--work-item"
func TestCodexWorktreeCarryover_DirtyMain(t *testing.T) {
	dir := setupCarryRepo(t)

	// Dirty the main branch with a tracked file edit.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\ncodex carryover\n"), 0644); err != nil {
		t.Fatalf("edit README: %v", err)
	}

	// Create a worktree simulating what launchCodexDefault does via
	// EnsureForTrackStatus when a trackID is provided.
	path, created, err := worktree.EnsureForTrackStatus("trk-codex01", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForTrackStatus: %v", err)
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
	if !strings.Contains(string(wt), "codex carryover") {
		t.Errorf("worktree should contain carried edit; got %q", wt)
	}

	// Main must still be dirty (unchanged by carryover).
	main, _ := os.ReadFile(filepath.Join(dir, "README.md"))
	if !strings.Contains(string(main), "codex carryover") {
		t.Errorf("main should still contain the edit (unchanged by carryover); got %q", main)
	}
}

// TestCodexWorktreeCarryover_ReusedWorktreeSkipsCarryover verifies that when a
// worktree is REUSED (created=false) for a codex launch, carryover is skipped
// and no message is emitted — preventing double-applying.
func TestCodexWorktreeCarryover_ReusedWorktreeSkipsCarryover(t *testing.T) {
	dir := setupCarryRepo(t)

	// Dirty main.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\ncodex reuse guard\n"), 0644); err != nil {
		t.Fatalf("edit README: %v", err)
	}

	// Create the worktree first so the next call is a reuse.
	path, _, err := worktree.EnsureForTrackStatus("trk-codex-reuse01", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForTrackStatus: %v", err)
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
	if strings.Contains(string(wt), "codex reuse guard") {
		t.Errorf("reused worktree should not have changes applied; got %q", wt)
	}
}

// TestCodexWorktreeCarryover_FeatureID verifies carryover works when a featureID
// is used (via EnsureForFeatureStatus) rather than a trackID.
func TestCodexWorktreeCarryover_FeatureID(t *testing.T) {
	dir := setupCarryRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\ncodex feature carryover\n"), 0644); err != nil {
		t.Fatalf("edit README: %v", err)
	}

	path, created, err := worktree.EnsureForFeatureStatus("feat-codex01", dir, io.Discard)
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
	if strings.Contains(out, "--work-item") {
		t.Errorf("message must NOT mention --work-item; got %q", out)
	}
	if !strings.Contains(out, "managed worktree") {
		t.Errorf("message should mention managed worktree; got %q", out)
	}

	wt, _ := os.ReadFile(filepath.Join(path, "README.md"))
	if !strings.Contains(string(wt), "codex feature carryover") {
		t.Errorf("worktree should contain carried edit; got %q", wt)
	}
}
