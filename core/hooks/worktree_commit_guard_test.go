package hooks

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestCheckSubagentCommitGuard_SubagentOnMain verifies that a sub-agent session
// is blocked from committing on main/master, while an orchestrator (no parent)
// is always allowed.
func TestCheckSubagentCommitGuard_SubagentOnMain(t *testing.T) {
	// Sub-agent (has parent) on main → must be blocked.
	t.Run("subagent_on_main_is_blocked", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := initTestGitRepoOnBranch(t, tmpDir, "main"); err != nil {
			t.Skipf("cannot init git repo: %v", err)
		}
		commitEvent := &CloudEvent{
			ToolName:  "Bash",
			SessionID: "sub-sess-abc12345",
			CWD:       tmpDir,
			ToolInput: map[string]any{
				"command": "git commit -m \"test commit\"",
			},
		}
		reason := checkSubagentCommitGuard(commitEvent, "parent-sess-xyz", tmpDir)
		if reason == "" {
			t.Fatal("expected sub-agent commit on main to be blocked, but it was allowed")
		}
		if !strings.Contains(reason, "blocked") {
			t.Errorf("expected 'blocked' in reason, got: %q", reason)
		}
		if !strings.Contains(reason, "main") {
			t.Errorf("expected branch 'main' in reason, got: %q", reason)
		}
	})

	// Orchestrator (no parent) on main → must be allowed.
	t.Run("orchestrator_on_main_is_allowed", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := initTestGitRepoOnBranch(t, tmpDir, "main"); err != nil {
			t.Skipf("cannot init git repo: %v", err)
		}
		commitEvent := &CloudEvent{
			ToolName:  "Bash",
			SessionID: "sub-sess-abc12345",
			CWD:       tmpDir,
			ToolInput: map[string]any{
				"command": "git commit -m \"test commit\"",
			},
		}
		reason := checkSubagentCommitGuard(commitEvent, "", tmpDir)
		if reason != "" {
			t.Errorf("expected orchestrator commit on main to be allowed, got: %q", reason)
		}
	})

	// Sub-agent with WIPNOTE_AGENT_BRANCH=1 → escape hatch, allowed.
	t.Run("subagent_with_agent_branch_env_is_allowed", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := initTestGitRepoOnBranch(t, tmpDir, "main"); err != nil {
			t.Skipf("cannot init git repo: %v", err)
		}
		commitEvent := &CloudEvent{
			ToolName:  "Bash",
			SessionID: "sub-sess-abc12345",
			CWD:       tmpDir,
			ToolInput: map[string]any{
				"command": "git commit -m \"test commit\"",
			},
		}
		t.Setenv("WIPNOTE_AGENT_BRANCH", "1")
		reason := checkSubagentCommitGuard(commitEvent, "parent-sess-xyz", tmpDir)
		if reason != "" {
			t.Errorf("expected WIPNOTE_AGENT_BRANCH=1 to allow commit, got: %q", reason)
		}
	})

	// Sub-agent on a worktree-agent-* branch → escape hatch, allowed.
	t.Run("subagent_on_agent_branch_is_allowed", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := initTestGitRepoOnBranch(t, tmpDir, "worktree-agent-feat-123"); err != nil {
			t.Skipf("cannot init git repo: %v", err)
		}
		agentBranchEvent := &CloudEvent{
			ToolName:  "Bash",
			SessionID: "sub-sess-abc12345",
			CWD:       tmpDir,
			ToolInput: map[string]any{
				"command": "git commit -m \"test\"",
			},
		}
		reason := checkSubagentCommitGuard(agentBranchEvent, "parent-sess-xyz", tmpDir)
		if reason != "" {
			t.Errorf("expected worktree-agent-* branch to allow commit, got: %q", reason)
		}
	})

	// Non-Bash tool → always allowed.
	t.Run("non_bash_tool_is_allowed", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := initTestGitRepoOnBranch(t, tmpDir, "main"); err != nil {
			t.Skipf("cannot init git repo: %v", err)
		}
		writeEvent := &CloudEvent{
			ToolName:  "Write",
			SessionID: "sub-sess-abc12345",
			CWD:       tmpDir,
			ToolInput: map[string]any{
				"file_path": "/tmp/foo.go",
				"content":   "package main",
			},
		}
		reason := checkSubagentCommitGuard(writeEvent, "parent-sess-xyz", tmpDir)
		if reason != "" {
			t.Errorf("expected non-Bash tool to be allowed, got: %q", reason)
		}
	})

	// Bash but not a commit command → allowed.
	t.Run("non_commit_bash_is_allowed", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := initTestGitRepoOnBranch(t, tmpDir, "main"); err != nil {
			t.Skipf("cannot init git repo: %v", err)
		}
		lsEvent := &CloudEvent{
			ToolName:  "Bash",
			SessionID: "sub-sess-abc12345",
			CWD:       tmpDir,
			ToolInput: map[string]any{
				"command": "git status",
			},
		}
		reason := checkSubagentCommitGuard(lsEvent, "parent-sess-xyz", tmpDir)
		if reason != "" {
			t.Errorf("expected non-commit Bash to be allowed, got: %q", reason)
		}
	})
}

// initTestGitRepoOnBranch initialises a minimal git repo in dir on the given branch.
func initTestGitRepoOnBranch(t *testing.T, dir, branch string) error {
	t.Helper()
	run := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("init"); err != nil {
		return fmt.Errorf("git init: %v\n%s", err, out)
	}
	if out, err := run("config", "user.email", "test@test.com"); err != nil {
		return fmt.Errorf("git config email: %v\n%s", err, out)
	}
	if out, err := run("config", "user.name", "Test"); err != nil {
		return fmt.Errorf("git config name: %v\n%s", err, out)
	}
	// Write a file and commit so HEAD exists before branch rename.
	if err := os.WriteFile(dir+"/README", []byte("test"), 0o644); err != nil {
		return fmt.Errorf("write README: %v", err)
	}
	if out, err := run("add", "."); err != nil {
		return fmt.Errorf("git add: %v\n%s", err, out)
	}
	if out, err := run("commit", "-m", "init"); err != nil {
		return fmt.Errorf("git commit: %v\n%s", err, out)
	}
	// Rename current branch to the target branch.
	if out, err := run("branch", "-m", branch); err != nil {
		return fmt.Errorf("git branch -m: %v\n%s", err, out)
	}
	return nil
}
