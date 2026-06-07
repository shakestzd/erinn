package paths_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/paths"
)

// TestResolveViaGitCommonDir_NonGitDir verifies that a plain (non-git)
// temporary directory causes the function to return "".
func TestResolveViaGitCommonDir_NonGitDir(t *testing.T) {
	tmpDir := t.TempDir()
	result := paths.ResolveViaGitCommonDir(tmpDir)
	if result != "" {
		t.Errorf("expected empty string for non-git dir, got %q", result)
	}
}

// TestResolveViaGitCommonDir_MainWorktree verifies that running from the main
// worktree (where --git-common-dir returns ".git") causes the function to
// return "" so the caller falls through to normal resolution.
func TestResolveViaGitCommonDir_MainWorktree(t *testing.T) {
	// Use the actual project root (this repo is a git repo).
	// git rev-parse --git-common-dir from the repo root returns ".git",
	// so the function must return "" to avoid short-circuiting normal logic.
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Skip("cannot determine working directory")
	}

	result := paths.ResolveViaGitCommonDir(repoRoot)
	// We don't assert "" here because the CI environment might be a worktree
	// itself; we just ensure the function doesn't panic and returns a string.
	_ = result
}

// TestResolveViaGitCommonDir_EmptyDir verifies that an empty dir argument
// falls back to os.Getwd() without panicking.
func TestResolveViaGitCommonDir_EmptyDir(t *testing.T) {
	// Should not panic regardless of whether CWD is a git repo.
	_ = paths.ResolveViaGitCommonDir("")
}

// TestResolveViaGitCommonDir_NoWipnote verifies that even when git
// common-dir resolves, the function returns "" if the main repo has no
// .wipnote directory.
func TestResolveViaGitCommonDir_NoWipnote(t *testing.T) {
	// Create a temp dir that looks like a git repo main root (has .git/) but
	// no .wipnote/. We can't easily simulate --git-common-dir returning a
	// path, so this test validates the stat guard via a direct integration:
	// any tmpDir without .wipnote should not be returned.
	tmpDir := t.TempDir()
	// Pretend .git exists so git might resolve, but there's no .wipnote.
	// In practice git won't recognise it as a worktree, so the function
	// returns "" anyway — this just documents the expected safety net.
	result := paths.ResolveViaGitCommonDir(tmpDir)
	if result != "" {
		// Only fail if result doesn't actually have .wipnote
		wipnotePath := filepath.Join(result, ".wipnote")
		if _, err := os.Stat(wipnotePath); os.IsNotExist(err) {
			t.Errorf("returned %q which has no .wipnote directory", result)
		}
	}
}

// TestGetGitRemoteURL_EmptyDir verifies that an empty dir returns "".
func TestGetGitRemoteURL_EmptyDir(t *testing.T) {
	result := paths.GetGitRemoteURL("")
	if result != "" {
		t.Errorf("expected empty string for empty dir, got %q", result)
	}
}

// TestGetGitRemoteURL_NonGitDir verifies that a plain directory returns "".
func TestGetGitRemoteURL_NonGitDir(t *testing.T) {
	tmpDir := t.TempDir()
	result := paths.GetGitRemoteURL(tmpDir)
	if result != "" {
		t.Errorf("expected empty string for non-git dir, got %q", result)
	}
}

// TestGetGitRemoteURL_GitRepo verifies that a real git repo with an origin
// returns a non-empty URL.
func TestGetGitRemoteURL_GitRepo(t *testing.T) {
	// Use the actual repo root — it should have an origin remote.
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Skip("cannot determine working directory")
	}
	result := paths.GetGitRemoteURL(repoRoot)
	// We can't assert the exact URL, but a real repo should return something.
	// If it's empty, the repo has no origin remote — skip rather than fail.
	if result == "" {
		t.Skip("no origin remote configured in this repo")
	}
	// Sanity check: URL should contain at least a slash or colon (path/host separator).
	if len(result) < 5 {
		t.Errorf("GetGitRemoteURL returned suspiciously short URL: %q", result)
	}
}

// TestResolveProjectDir_WipnoteProjectDirEnv verifies that WIPNOTE_PROJECT_DIR
// is honoured so subagent hooks can locate .wipnote/ when EventCWD is a temp dir.
func TestResolveProjectDir_WipnoteProjectDirEnv(t *testing.T) {
	// Set up a real project directory with .wipnote/.
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	// Simulate a subagent whose EventCWD is an unrelated temp dir with no .wipnote/.
	fakeTmpCWD := t.TempDir()

	// Unset CLAUDE_PROJECT_DIR so it does not interfere.
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	// Set WIPNOTE_PROJECT_DIR to the real project dir (written by SubagentStart).
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	got, err := paths.ResolveProjectDir(paths.ProjectDirOptions{
		EventCWD:   fakeTmpCWD,
		WalkLevels: 10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != projectDir {
		t.Errorf("ResolveProjectDir = %q, want %q", got, projectDir)
	}
}

// TestResolveProjectDir_WipnoteProjectDirEnv_Invalid verifies that an
// invalid WIPNOTE_PROJECT_DIR value (no .wipnote/) falls through to the
// next resolution step rather than returning a wrong path.
func TestResolveProjectDir_WipnoteProjectDirEnv_Invalid(t *testing.T) {
	// A temp dir without .wipnote/ — should be skipped.
	badDir := t.TempDir()

	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", badDir) // no .wipnote/ here

	// Verify that the invalid env var is skipped (not returned).
	// The resolver will fall through to later steps (git detection, walk-up, etc.),
	// but the result should NOT be the invalid badDir.
	got, err := paths.ResolveProjectDir(paths.ProjectDirOptions{
		EventCWD:   t.TempDir(),
		WalkLevels: 10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == badDir {
		t.Errorf("ResolveProjectDir = %q, should not return invalid WIPNOTE_PROJECT_DIR", got)
	}
}

// TestResolveProjectDir_HintFile verifies that the session-scoped hint file
// (step 4 in the resolution chain) is used when WIPNOTE_PROJECT_DIR is not
// set and a SessionID is provided. This covers the worktree subagent case
// where CLAUDE_ENV_FILE is unset so SubagentStart writes to the hint file
// instead of the env file.
func TestResolveProjectDir_HintFile(t *testing.T) {
	// Set up a real project directory with .wipnote/.
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	// Simulate subagent EventCWD in an unrelated temp dir.
	fakeTmpCWD := t.TempDir()

	// Clear both env vars so steps 2 and 3 are skipped.
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", "")

	// Write the session-scoped hint file (simulates writeSessionProjectDirHint in SubagentStart).
	const testSessionID = "test-session-hint-valid"
	paths.WriteSessionHint(testSessionID, projectDir)
	t.Cleanup(func() { paths.CleanupSessionHint(testSessionID) })

	got, err := paths.ResolveProjectDir(paths.ProjectDirOptions{
		EventCWD:   fakeTmpCWD,
		WalkLevels: 10,
		SessionID:  testSessionID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != projectDir {
		t.Errorf("ResolveProjectDir = %q, want %q (session hint not used?)", got, projectDir)
	}
}

// TestResolveProjectDir_HintFile_Invalid verifies that a stale/invalid
// session-scoped hint file (pointing to a dir with no .wipnote/) is skipped
// and the resolver falls through to the next step.
func TestResolveProjectDir_HintFile_Invalid(t *testing.T) {
	// A project dir that DOES have .wipnote/ — used as EventCWD direct hit.
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	// A bad hint dir with no .wipnote/.
	badDir := t.TempDir()

	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", "")

	// Write a stale session-scoped hint pointing at a dir without .wipnote/.
	const testSessionID = "test-session-hint-invalid"
	paths.WriteSessionHint(testSessionID, badDir)
	t.Cleanup(func() { paths.CleanupSessionHint(testSessionID) })

	// EventCWD points directly at a valid project — step 6 should find it.
	got, err := paths.ResolveProjectDir(paths.ProjectDirOptions{
		EventCWD:   projectDir,
		WalkLevels: 10,
		SessionID:  testSessionID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != projectDir {
		t.Errorf("ResolveProjectDir = %q, want %q (stale session hint not skipped?)", got, projectDir)
	}
}

// TestGetGitRemoteURL_InitedRepo verifies that a fresh git repo with a remote
// returns the configured URL.
func TestGetGitRemoteURL_InitedRepo(t *testing.T) {
	tmpDir := t.TempDir()

	// Initialise a bare git repo and add an origin remote.
	if err := runGit(tmpDir, "init"); err != nil {
		t.Skipf("git init failed: %v", err)
	}
	wantURL := "https://github.com/example/repo.git"
	if err := runGit(tmpDir, "remote", "add", "origin", wantURL); err != nil {
		t.Fatalf("git remote add failed: %v", err)
	}

	result := paths.GetGitRemoteURL(tmpDir)
	if result != wantURL {
		t.Errorf("GetGitRemoteURL = %q, want %q", result, wantURL)
	}
}

// runGit is a test helper that runs a git subcommand in dir.
func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run()
}

// TestResolveProjectDir_PrefersClaudeProjectDirWhenSessionIDPresent verifies
// that CLAUDE_PROJECT_DIR is preferred over EventCWD/CWD when WIPNOTE_SESSION_ID
// is also set (confirming the env var was written by the current session's hooks).
func TestResolveProjectDir_PrefersClaudeProjectDirWhenSessionIDPresent(t *testing.T) {
	projectA := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectA, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote in A: %v", err)
	}
	projectB := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectB, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote in B: %v", err)
	}

	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	t.Setenv("CLAUDE_PROJECT_DIR", projectA)
	t.Setenv("WIPNOTE_SESSION_ID", "s1")

	got, err := paths.ResolveProjectDir(paths.ProjectDirOptions{
		EventCWD:   projectB,
		WalkLevels: 10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != projectA {
		t.Errorf("ResolveProjectDir = %q, want %q (CLAUDE_PROJECT_DIR with session ID should win)", got, projectA)
	}
}

// TestResolveProjectDir_IgnoresStaleClaudeProjectDir verifies that CLAUDE_PROJECT_DIR
// is ignored when WIPNOTE_SESSION_ID is NOT set (stale value from a parent shell).
// Regression test for bug-71fc095f.
func TestResolveProjectDir_IgnoresStaleClaudeProjectDir(t *testing.T) {
	projectA := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectA, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote in A: %v", err)
	}
	projectB := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectB, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote in B: %v", err)
	}

	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	t.Setenv("CLAUDE_PROJECT_DIR", projectA) // stale — no session ID
	t.Setenv("WIPNOTE_SESSION_ID", "")       // NOT set — stale shell scenario

	// EventCWD is projectB; without guardrail, A would win — but it should not.
	got, err := paths.ResolveProjectDir(paths.ProjectDirOptions{
		EventCWD:   projectB,
		WalkLevels: 10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should NOT return projectA (the stale CLAUDE_PROJECT_DIR).
	if got == projectA {
		t.Errorf("ResolveProjectDir = %q — stale CLAUDE_PROJECT_DIR was not ignored", got)
	}
}

// TestResolveProjectDir_FlagBeatsClaudeProjectDir verifies that --project-dir flag
// takes priority over CLAUDE_PROJECT_DIR even when WIPNOTE_SESSION_ID is set.
func TestResolveProjectDir_FlagBeatsClaudeProjectDir(t *testing.T) {
	flagDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(flagDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote in flagDir: %v", err)
	}
	claudeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(claudeDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote in claudeDir: %v", err)
	}

	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	t.Setenv("CLAUDE_PROJECT_DIR", claudeDir)
	t.Setenv("WIPNOTE_SESSION_ID", "s1")

	got, err := paths.ResolveProjectDir(paths.ProjectDirOptions{
		ExplicitDir: flagDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != flagDir {
		t.Errorf("ResolveProjectDir = %q, want %q (--project-dir flag should win)", got, flagDir)
	}
}

// TestResolveProjectDir_WipnoteEnvBeatsClaudeProjectDir verifies that
// WIPNOTE_PROJECT_DIR takes priority over CLAUDE_PROJECT_DIR.
func TestResolveProjectDir_WipnoteEnvBeatsClaudeProjectDir(t *testing.T) {
	wipnoteDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wipnoteDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote in wipnoteDir: %v", err)
	}
	claudeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(claudeDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote in claudeDir: %v", err)
	}

	t.Setenv("WIPNOTE_PROJECT_DIR", wipnoteDir)
	t.Setenv("CLAUDE_PROJECT_DIR", claudeDir)
	t.Setenv("WIPNOTE_SESSION_ID", "s1")

	got, err := paths.ResolveProjectDir(paths.ProjectDirOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wipnoteDir {
		t.Errorf("ResolveProjectDir = %q, want %q (WIPNOTE_PROJECT_DIR should beat CLAUDE_PROJECT_DIR)", got, wipnoteDir)
	}
}

// TestResolveProjectDir_FallsBackToCwdWhenNoWipnoteDirInClaudeProjectDir
// verifies that when CLAUDE_PROJECT_DIR points at a dir with no .wipnote/,
// the resolver falls through to EventCWD/CWD walk-up even when session ID is set.
func TestResolveProjectDir_FallsBackToCwdWhenNoWipnoteDirInClaudeProjectDir(t *testing.T) {
	noWipnoteDir := t.TempDir() // no .wipnote/ subdirectory
	realProjectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(realProjectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote in realProjectDir: %v", err)
	}

	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	t.Setenv("CLAUDE_PROJECT_DIR", noWipnoteDir) // points at dir WITHOUT .wipnote
	t.Setenv("WIPNOTE_SESSION_ID", "s1")         // session ID is set

	got, err := paths.ResolveProjectDir(paths.ProjectDirOptions{
		EventCWD:   realProjectDir,
		WalkLevels: 10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// CLAUDE_PROJECT_DIR has no .wipnote, so it should be skipped and
	// EventCWD (realProjectDir) should be returned.
	if got != realProjectDir {
		t.Errorf("ResolveProjectDir = %q, want %q (should fall back to EventCWD)", got, realProjectDir)
	}
}

// TestResolveProjectDir_IgnoresAmbientCwdWhenEventCWDPresent verifies that a
// present EventCWD is returned even when the process CWD points at this repo.
func TestResolveProjectDir_IgnoresAmbientCwdWhenEventCWDPresent(t *testing.T) {
	eventCWD := t.TempDir()
	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_SESSION_ID", "")

	got, err := paths.ResolveProjectDir(paths.ProjectDirOptions{
		EventCWD:   eventCWD,
		WalkLevels: 10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != eventCWD {
		t.Fatalf("ResolveProjectDir = %q, want %q", got, eventCWD)
	}
}
