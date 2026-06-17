package hooks

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/db"
)

func newExecContextDB(t *testing.T, projectDir string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	database, err := db.Open(filepath.Join(projectDir, ".wipnote", "wipnote.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	return database
}

func clearSessionEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("WIPNOTE_PARENT_SESSION", "")
	t.Setenv("WIPNOTE_NESTING_DEPTH", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("WIPNOTE_SESSION_ID", "")
	t.Setenv("WIPNOTE_SESSION_FAMILY_ID", "")
}

func TestSessionStart_RecordsExecWorktree(t *testing.T) {
	t.Run("main worktree stores empty", func(t *testing.T) {
		clearSessionEnv(t)
		projectDir := t.TempDir()
		database := newExecContextDB(t, projectDir)
		defer database.Close()

		sessionID := "exec-main-worktree-001"
		event := &CloudEvent{SessionID: sessionID, CWD: projectDir}
		if _, err := SessionStart(event, database, projectDir); err != nil {
			t.Fatalf("SessionStart: %v", err)
		}

		got, err := db.GetSession(database, sessionID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if got.ExecWorktreePath != "" {
			t.Errorf("exec_worktree_path = %q, want empty", got.ExecWorktreePath)
		}
	})

	t.Run("linked worktree stores repo-relative subpath", func(t *testing.T) {
		clearSessionEnv(t)
		mainDir := t.TempDir()
		database := newExecContextDB(t, mainDir)
		defer database.Close()

		execDir := filepath.Join(mainDir, ".claude", "worktrees", "agent-xyz")
		if err := os.MkdirAll(execDir, 0o755); err != nil {
			t.Fatalf("mkdir execDir: %v", err)
		}

		sessionID := "exec-linked-worktree-001"
		event := &CloudEvent{SessionID: sessionID, CWD: execDir}
		if _, err := SessionStart(event, database, mainDir); err != nil {
			t.Fatalf("SessionStart: %v", err)
		}

		got, err := db.GetSession(database, sessionID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if got.ExecWorktreePath != ".claude/worktrees/agent-xyz" {
			t.Errorf("exec_worktree_path = %q, want %q", got.ExecWorktreePath, ".claude/worktrees/agent-xyz")
		}
	})
}

func TestSessionStart_HarnessPersisted(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want string
	}{
		{"claude accepted", "claude", "claude"},
		{"codex accepted", "codex", "codex"},
		{"gemini accepted", "gemini", "gemini"},
		{"antigravity accepted", "antigravity", "antigravity"},
		{"case-insensitive", "Claude", "claude"},
		{"trimmed", "  codex  ", "codex"},
		{"garbage rejected", "rogue-cli", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearSessionEnv(t)
			t.Setenv("WIPNOTE_HARNESS", tc.env)
			projectDir := t.TempDir()
			database := newExecContextDB(t, projectDir)
			defer database.Close()

			sessionID := "harness-test-" + tc.name
			event := &CloudEvent{SessionID: sessionID, CWD: projectDir}
			if _, err := SessionStart(event, database, projectDir); err != nil {
				t.Fatalf("SessionStart: %v", err)
			}
			got, err := db.GetSession(database, sessionID)
			if err != nil {
				t.Fatalf("GetSession: %v", err)
			}
			if got.Harness != tc.want {
				t.Errorf("harness = %q, want %q", got.Harness, tc.want)
			}
		})
	}
}

func TestExecWorktreeRelPath(t *testing.T) {
	main := t.TempDir()
	if got := execWorktreeRelPath(main, main); got != "" {
		t.Errorf("same dir got %q, want empty", got)
	}
	if got := execWorktreeRelPath("", main); got != "" {
		t.Errorf("empty cwd got %q, want empty", got)
	}
	if got := execWorktreeRelPath(filepath.Join(main, "wt", "a"), main); got != "wt/a" {
		t.Errorf("nested worktree got %q, want wt/a", got)
	}
	if got := execWorktreeRelPath(t.TempDir(), main); got != "" {
		t.Errorf("outside repo got %q, want empty", got)
	}
}

func TestGitBranch(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "trk-test-branch")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", ".")
	run("commit", "-m", "init")

	if got := gitBranch(dir); got != "trk-test-branch" {
		t.Errorf("gitBranch = %q, want %q", got, "trk-test-branch")
	}
}
