package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/internal/launcher/plan"
	"github.com/shakestzd/wipnote/core/worktree"
)

// TestGeminiWorktreeCarryover_DirtyMain verifies that when a NEW worktree is
// created on a dirty main branch for a gemini launch, the shared
// emitWorktreeCarryoverMessage helper (bug-938e56ae):
//   - carries tracked changes from main into the worktree
//   - leaves main unchanged (main still dirty)
//   - emits an advisory that does NOT mention "--work-item"
func TestGeminiWorktreeCarryover_DirtyMain(t *testing.T) {
	dir := setupCarryRepo(t)

	// Dirty the main branch with a tracked file edit.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\ngemini carryover\n"), 0644); err != nil {
		t.Fatalf("edit README: %v", err)
	}

	// Create a worktree simulating what launchGeminiDefault does via
	// EnsureForTrackStatus when a trackID is provided.
	path, created, err := worktree.EnsureForTrackStatus("trk-gemini01", dir, io.Discard)
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
	// Must reassure main is unchanged — exactly once. The duplicate core-layer
	// carryover print was dropped in feat-f5fe2056, leaving the cmd advisory as
	// the single source; assert the phrase is not double-announced.
	if got := strings.Count(out, "main left unchanged"); got != 1 {
		t.Errorf("carryover must say 'main left unchanged' exactly once (single source); got %d in %q", got, out)
	}

	// Tracked change must appear in the worktree.
	wt, _ := os.ReadFile(filepath.Join(path, "README.md"))
	if !strings.Contains(string(wt), "gemini carryover") {
		t.Errorf("worktree should contain carried edit; got %q", wt)
	}

	// Main must still be dirty (unchanged by carryover).
	main, _ := os.ReadFile(filepath.Join(dir, "README.md"))
	if !strings.Contains(string(main), "gemini carryover") {
		t.Errorf("main should still contain the edit (unchanged by carryover); got %q", main)
	}
}

// TestGeminiWorktreeCarryover_ReusedWorktreeSkipsCarryover verifies that when a
// worktree is REUSED (created=false) for a gemini launch, carryover is skipped
// and no message is emitted — preventing double-applying.
func TestGeminiWorktreeCarryover_ReusedWorktreeSkipsCarryover(t *testing.T) {
	dir := setupCarryRepo(t)

	// Dirty main.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\ngemini reuse guard\n"), 0644); err != nil {
		t.Fatalf("edit README: %v", err)
	}

	// Create the worktree first so the next call is a reuse.
	path, _, err := worktree.EnsureForTrackStatus("trk-gemini-reuse01", dir, io.Discard)
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
	if strings.Contains(string(wt), "gemini reuse guard") {
		t.Errorf("reused worktree should not have changes applied; got %q", wt)
	}
}

// TestGeminiWorktreeCarryover_FeatureID verifies carryover works when a featureID
// is used (via EnsureForFeatureStatus) rather than a trackID.
func TestGeminiWorktreeCarryover_FeatureID(t *testing.T) {
	dir := setupCarryRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\ngemini feature carryover\n"), 0644); err != nil {
		t.Fatalf("edit README: %v", err)
	}

	path, created, err := worktree.EnsureForFeatureStatus("feat-gemini01", dir, io.Discard)
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
	if !strings.Contains(string(wt), "gemini feature carryover") {
		t.Errorf("worktree should contain carried edit; got %q", wt)
	}
}
