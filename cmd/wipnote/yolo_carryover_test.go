package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/worktree"
	"github.com/shakestzd/wipnote/internal/launcher/plan"
)

// setupCarryRepo builds a git repo on branch "main" with a committed tracked file,
// so callers can dirty it and exercise carryover/messaging. It also stubs the
// worktree reindex subprocess to a no-op for the duration of the test, so
// EnsureFor* does not fork the test binary.
func setupCarryRepo(t *testing.T) string {
	t.Helper()
	prev := worktree.SetReindexFnForTest(func(string, io.Writer) {})
	t.Cleanup(func() { worktree.SetReindexFnForTest(prev) })
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", ".")
	run("commit", "-m", "initial")
	return dir
}

// TestEmitYoloDirtyMainMessage_ReusedWorktreeSkipsCarryover verifies the
// guardrail (bug-bcf8a311): when the worktree was REUSED (created=false), no
// carryover is attempted and no message is emitted, even if main is dirty. This
// prevents double-applying into a worktree that already holds prior work.
func TestEmitYoloDirtyMainMessage_ReusedWorktreeSkipsCarryover(t *testing.T) {
	dir := setupCarryRepo(t)
	// Dirty main.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\nedit\n"), 0644); err != nil {
		t.Fatalf("edit README: %v", err)
	}
	// Create the worktree, then call with created=false to simulate reuse.
	path, _, err := worktree.EnsureForTrackStatus("trk-reuse01", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForTrackStatus: %v", err)
	}

	p := plan.LaunchPlan{DirtyMainWarning: "Warning: launching on dirty protected branch \"main\"."}
	var buf bytes.Buffer
	emitYoloDirtyMainMessage(p, dir, path, false /* reused */, &buf)

	if buf.Len() != 0 {
		t.Errorf("reused worktree must not emit any message, got %q", buf.String())
	}
	// The reused worktree must NOT have had the edit carried in.
	got, _ := os.ReadFile(filepath.Join(path, "README.md"))
	if strings.Contains(string(got), "edit") {
		t.Errorf("reused worktree should not have carried changes applied; got %q", got)
	}
}

// TestEmitYoloDirtyMainMessage_DirtyWorktreeMessageNoWorkItem verifies the
// accurate dirty-main messaging (bug-7d4b6c63): when a worktree is freshly
// created on a dirty main, the message reports isolation + carryover and does
// NOT advise "--work-item" (the worktree already exists).
func TestEmitYoloDirtyMainMessage_DirtyWorktreeMessageNoWorkItem(t *testing.T) {
	dir := setupCarryRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\ncarried edit\n"), 0644); err != nil {
		t.Fatalf("edit README: %v", err)
	}
	path, created, err := worktree.EnsureForTrackStatus("trk-msg01", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForTrackStatus: %v", err)
	}
	if !created {
		t.Fatalf("expected created worktree")
	}

	p := plan.LaunchPlan{DirtyMainWarning: "Warning: launching on dirty protected branch \"main\"."}
	var buf bytes.Buffer
	emitYoloDirtyMainMessage(p, dir, path, true, &buf)

	out := buf.String()
	if strings.Contains(out, "--work-item") {
		t.Errorf("worktree-creation message must NOT mention --work-item; got %q", out)
	}
	if !strings.Contains(out, "managed worktree") {
		t.Errorf("message should mention the managed worktree; got %q", out)
	}
	// Exactly once — the duplicate core-layer carryover print was dropped in
	// feat-f5fe2056, leaving the cmd advisory as the single source.
	if got := strings.Count(out, "main left unchanged"); got != 1 {
		t.Errorf("carryover must reassure 'main left unchanged' exactly once (single source); got %d in %q", got, out)
	}
	// The edit was carried into the worktree, and main is still dirty.
	wt, _ := os.ReadFile(filepath.Join(path, "README.md"))
	if !strings.Contains(string(wt), "carried edit") {
		t.Errorf("expected carried edit in worktree; got %q", wt)
	}
	main, _ := os.ReadFile(filepath.Join(dir, "README.md"))
	if !strings.Contains(string(main), "carried edit") {
		t.Errorf("main should still contain the edit (unchanged); got %q", main)
	}
}

// TestEmitYoloDirtyMainMessage_CleanMainNoMessage verifies that when main is
// clean (no DirtyMainWarning), the advisory is not emitted even on a newly
// created worktree.
func TestEmitYoloDirtyMainMessage_CleanMainNoMessage(t *testing.T) {
	dir := setupCarryRepo(t)
	path, created, err := worktree.EnsureForTrackStatus("trk-cleanmsg01", dir, io.Discard)
	if err != nil {
		t.Fatalf("EnsureForTrackStatus: %v", err)
	}
	if !created {
		t.Fatalf("expected created worktree")
	}

	p := plan.LaunchPlan{} // clean main: no DirtyMainWarning
	var buf bytes.Buffer
	emitYoloDirtyMainMessage(p, dir, path, true, &buf)

	if strings.Contains(buf.String(), "Dirty main detected") {
		t.Errorf("clean main must not emit the dirty-main advisory; got %q", buf.String())
	}
}

// TestApplyLaunchPlanOpts_SuppressesDirtyWarning verifies the boxless-banner
// contract (bug-0f6af202): applyLaunchPlanOpts NEVER prints a separate dirty-main
// block (the advisory now folds into the single launch banner), and the returned
// plan ALWAYS carries DirtyMainWarning so both the carryover path and the banner
// can consume it. bannerDirtyWarning then decides where it surfaces: hidden when
// a worktree will be created (carryover owns it), shown otherwise.
func TestApplyLaunchPlanOpts_SuppressesDirtyWarning(t *testing.T) {
	dir := setupCarryRepo(t)
	// Dirty the protected branch.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\ndirty\n"), 0644); err != nil {
		t.Fatalf("edit README: %v", err)
	}

	// Worktree-creating launch: no inline print, plan retains DirtyMainWarning,
	// and bannerDirtyWarning hides it (carryover message will cover it).
	var suppressed bytes.Buffer
	p := applyLaunchPlanOpts(dir, dir, "trk-suppress01", false, true, &suppressed)
	if suppressed.Len() != 0 {
		t.Errorf("applyLaunchPlanOpts must not print any separate block; got %q", suppressed.String())
	}
	if p.DirtyMainWarning == "" {
		t.Errorf("plan should still carry DirtyMainWarning for the carryover/enforce paths")
	}
	if got := bannerDirtyWarning(p, true /* willCreateWorktree */); got != "" {
		t.Errorf("worktree-creating launch: banner advisory must be hidden; got %q", got)
	}

	// In-place launch: no inline print either, but bannerDirtyWarning surfaces the
	// advisory so it folds into the single launch banner.
	var shown bytes.Buffer
	p2 := applyLaunchPlanOpts(dir, dir, "trk-suppress01", false, false, &shown)
	if shown.Len() != 0 {
		t.Errorf("applyLaunchPlanOpts must not print any separate block; got %q", shown.String())
	}
	advisory := bannerDirtyWarning(p2, false /* willCreateWorktree */)
	if !strings.Contains(advisory, "--work-item") {
		t.Errorf("in-place dirty launch: banner advisory should name --work-item; got %q", advisory)
	}
}
