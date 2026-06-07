package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/internal/hooks"
	"github.com/shakestzd/wipnote/core/models"
	worktreepkg "github.com/shakestzd/wipnote/internal/worktree"
)

func setupGitRepoForHookWorktreeCreate(t *testing.T) string {
	t.Helper()
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	run("add", "README.md")
	run("commit", "-m", "initial")
	return repoRoot
}

func captureHookCommand(t *testing.T, stdin []byte, fn func() error) (string, string, error) {
	t.Helper()
	stdinFile, err := os.CreateTemp(t.TempDir(), "stdin-*.json")
	if err != nil {
		t.Fatalf("create stdin temp: %v", err)
	}
	if _, err := stdinFile.Write(stdin); err != nil {
		t.Fatalf("write stdin temp: %v", err)
	}
	if _, err := stdinFile.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek stdin temp: %v", err)
	}
	defer stdinFile.Close()

	oldStdin, oldStdout, oldStderr := os.Stdin, os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stdin, os.Stdout, os.Stderr = stdinFile, outW, errW
	runErr := fn()
	_ = outW.Close()
	_ = errW.Close()
	os.Stdin, os.Stdout, os.Stderr = oldStdin, oldStdout, oldStderr

	var stdout, stderr bytes.Buffer
	if _, err := io.Copy(&stdout, outR); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if _, err := io.Copy(&stderr, errR); err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	return stdout.String(), stderr.String(), runErr
}

func TestHookWorktreeCreate_PrintsBarePathAndCreatesGitWorktree(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("WIPNOTE_SESSION_ID", "test-hook-worktree-create")
	repoRoot := setupGitRepoForHookWorktreeCreate(t)
	basePath := filepath.Join(t.TempDir(), "claude-worktrees")
	worktreeName := "feat-hook-stdout"
	wantPath := filepath.Join(basePath, worktreeName)

	dbPath, err := hooks.DBPath(repoRoot)
	if err != nil {
		t.Fatalf("DBPath: %v", err)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	now := time.Now().UTC()
	if err := db.InsertSession(database, &models.Session{
		SessionID:     "test-hook-worktree-create",
		AgentAssigned: "claude-code",
		CreatedAt:     now,
		Status:        "active",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	database.Close()

	prev := worktreepkg.SetReindexFnForTest(func(string, io.Writer) {})
	t.Cleanup(func() { worktreepkg.SetReindexFnForTest(prev) })

	payload, err := json.Marshal(map[string]string{
		"session_id":         "test-hook-worktree-create",
		"hook_event_name":    "WorktreeCreate",
		"cwd":                repoRoot,
		"worktree_base_path": basePath,
		"worktree_name":      worktreeName,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	stdout, stderr, runErr := captureHookCommand(t, payload, func() error {
		cmd := hookCmd()
		cmd.SetArgs([]string{"worktree-create"})
		return cmd.Execute()
	})
	if runErr != nil {
		t.Fatalf("hook worktree-create: %v\nstderr=%s", runErr, stderr)
	}
	if stdout != wantPath+"\n" {
		t.Fatalf("stdout = %q, want bare path %q", stdout, wantPath+"\n")
	}
	if strings.Contains(stdout, "{") || strings.Contains(stdout, "continue") {
		t.Fatalf("stdout contains JSON envelope: %q", stdout)
	}
	if info, err := os.Stat(wantPath); err != nil || !info.IsDir() {
		t.Fatalf("created path is not a directory: info=%v err=%v", info, err)
	}
	if out, err := exec.Command("git", "-C", wantPath, "rev-parse", "--is-inside-work-tree").Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("created path is not a git worktree: out=%q err=%v", out, err)
	}
}

func TestHookWorktreeCreate_DataEnvelopePrintsBarePath(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("WIPNOTE_SESSION_ID", "test-hook-worktree-create-data")
	repoRoot := setupGitRepoForHookWorktreeCreate(t)
	basePath := filepath.Join(t.TempDir(), "claude-worktrees")
	worktreeName := "feat-hook-data"
	wantPath := filepath.Join(basePath, worktreeName)

	seedHookWorktreeCreateSession(t, repoRoot, "test-hook-worktree-create-data")
	prev := worktreepkg.SetReindexFnForTest(func(string, io.Writer) {})
	t.Cleanup(func() { worktreepkg.SetReindexFnForTest(prev) })

	payload, err := json.Marshal(map[string]any{
		"data": map[string]string{
			"session_id":         "test-hook-worktree-create-data",
			"hook_event_name":    "WorktreeCreate",
			"cwd":                repoRoot,
			"worktree_base_path": basePath,
			"worktree_name":      worktreeName,
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	stdout, stderr, runErr := captureHookCommand(t, payload, func() error {
		cmd := hookCmd()
		cmd.SetArgs([]string{"worktree-create"})
		return cmd.Execute()
	})
	if runErr != nil {
		t.Fatalf("hook worktree-create: %v\nstderr=%s", runErr, stderr)
	}
	if stdout != wantPath+"\n" {
		t.Fatalf("stdout = %q, want bare path %q", stdout, wantPath+"\n")
	}
	if info, err := os.Stat(wantPath); err != nil || !info.IsDir() {
		t.Fatalf("created path is not a directory: info=%v err=%v", info, err)
	}
}

func TestHookWorktreeCreate_TopLevelWorktreePathWithoutClaudeEnv(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("WIPNOTE_SESSION_ID", "test-hook-worktree-create-path")
	repoRoot := setupGitRepoForHookWorktreeCreate(t)
	basePath := filepath.Join(t.TempDir(), "claude-worktrees")
	wantPath := filepath.Join(basePath, "feat-hook-path")

	seedHookWorktreeCreateSession(t, repoRoot, "test-hook-worktree-create-path")
	prev := worktreepkg.SetReindexFnForTest(func(string, io.Writer) {})
	t.Cleanup(func() { worktreepkg.SetReindexFnForTest(prev) })

	payload, err := json.Marshal(map[string]string{
		"session_id":         "test-hook-worktree-create-path",
		"hook_event_name":    "WorktreeCreate",
		"cwd":                repoRoot,
		"worktree_base_path": basePath,
		"worktree_path":      wantPath,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	stdout, stderr, runErr := captureHookCommand(t, payload, func() error {
		cmd := hookCmd()
		cmd.SetArgs([]string{"worktree-create"})
		return cmd.Execute()
	})
	if runErr != nil {
		t.Fatalf("hook worktree-create: %v\nstderr=%s", runErr, stderr)
	}
	if stdout != wantPath+"\n" {
		t.Fatalf("stdout = %q, want bare path %q", stdout, wantPath+"\n")
	}
	if strings.Contains(stdout, "{") || strings.Contains(stdout, "continue") {
		t.Fatalf("stdout contains JSON envelope: %q", stdout)
	}
	if out, err := exec.Command("git", "-C", wantPath, "rev-parse", "--is-inside-work-tree").Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("created path is not a git worktree: out=%q err=%v", out, err)
	}
}

func TestHookWorktreeCreate_MissingFieldsPrintsNoJSON(t *testing.T) {
	stdout, _, runErr := captureHookCommand(t, []byte(`{}`), func() error {
		cmd := hookCmd()
		cmd.SetArgs([]string{"worktree-create"})
		return cmd.Execute()
	})
	if runErr == nil {
		t.Fatal("expected hook worktree-create with empty payload to fail")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
}

func seedHookWorktreeCreateSession(t *testing.T, repoRoot, sessionID string) {
	t.Helper()
	dbPath, err := hooks.DBPath(repoRoot)
	if err != nil {
		t.Fatalf("DBPath: %v", err)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	now := time.Now().UTC()
	if err := db.InsertSession(database, &models.Session{
		SessionID:     sessionID,
		AgentAssigned: "claude-code",
		CreatedAt:     now,
		Status:        "active",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
}
