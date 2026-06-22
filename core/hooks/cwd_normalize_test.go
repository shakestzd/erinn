package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/paths"
)

// makeTestRepo creates a tmpdir with .wipnote/ and a git repo.
// Returns the canonical (EvalSymlinks-resolved) path.
func makeTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	if err := exec.Command("git", "-C", dir, "init", "-q").Run(); err != nil {
		t.Skipf("git init failed: %v", err)
	}
	if err := exec.Command("git", "-C", dir, "config", "user.email", "test@example.com").Run(); err != nil {
		t.Skipf("git config user.email failed: %v", err)
	}
	if err := exec.Command("git", "-C", dir, "config", "user.name", "Test User").Run(); err != nil {
		t.Skipf("git config user.name failed: %v", err)
	}
	canon, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	paths.ResetNormalizeCacheForTesting()
	return canon
}

// TestCWDNormalize_MainWorktree_StoresRelative verifies that a SessionStart
// from the main worktree stores project_dir as the repo-relative canonical
// root (".") rather than an absolute path.
func TestCWDNormalize_MainWorktree_StoresRelative(t *testing.T) {
	projectDir := makeTestRepo(t)

	database, err := openWipnoteTestDB(t, projectDir)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	sessionID := "test-cwd-main-worktree-001"
	event := &CloudEvent{SessionID: sessionID, CWD: projectDir}

	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("WIPNOTE_PARENT_SESSION", "")
	t.Setenv("WIPNOTE_NESTING_DEPTH", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_SESSION_ID", "")

	_, err = SessionStart(event, database, projectDir)
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	got, err := db.GetSession(database, sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	// Main worktree → stored as repo-relative canonical root (".").
	if filepath.IsAbs(got.ProjectDir) {
		t.Errorf("project_dir should be relative, got absolute: %q", got.ProjectDir)
	}
	if strings.HasPrefix(got.ProjectDir, "unresolved:") {
		t.Errorf("project_dir should not be unresolved for local session, got: %q", got.ProjectDir)
	}
}

// TestCWDNormalize_LinkedWorktree_CollapsesToRoot verifies that a SessionStart
// from a linked worktree collapses project_dir to the same canonical root
// relative form as the main worktree.
func TestCWDNormalize_LinkedWorktree_CollapsesToRoot(t *testing.T) {
	mainRepo := makeTestRepo(t)
	// Need at least one commit before adding a worktree.
	if err := exec.Command("git", "-C", mainRepo, "commit",
		"--allow-empty", "-m", "init", "-q").Run(); err != nil {
		t.Skipf("git commit failed: %v", err)
	}
	wtPath := filepath.Join(t.TempDir(), "linked-wt")
	if err := exec.Command("git", "-C", mainRepo, "worktree", "add",
		"-q", wtPath, "-b", "feat-normalize-test").Run(); err != nil {
		t.Skipf("git worktree add failed: %v", err)
	}
	wtPath, err := filepath.EvalSymlinks(wtPath)
	if err != nil {
		t.Fatalf("EvalSymlinks(wt): %v", err)
	}
	paths.ResetNormalizeCacheForTesting()

	dbPath := filepath.Join(mainRepo, ".wipnote", "wipnote.db")
	// Pin WIPNOTE_DB_PATH so SessionStart's daemon-routed writes land in the same
	// file we read back from (see openWipnoteTestDB); the projectDir passed below
	// (mainRepo) is the canonical root, so DBPath would otherwise resolve the host
	// cache DB, not this handle's file.
	t.Setenv("WIPNOTE_DB_PATH", dbPath)
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	sessionID := "test-cwd-linked-worktree-001"
	// SessionStart is called with mainRepo as projectDir (ResolveProjectDir
	// already resolves linked worktrees to the main repo root).
	event := &CloudEvent{SessionID: sessionID, CWD: wtPath}

	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("WIPNOTE_PARENT_SESSION", "")
	t.Setenv("WIPNOTE_NESTING_DEPTH", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_SESSION_ID", "")

	_, err = SessionStart(event, database, mainRepo)
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	got, err := db.GetSession(database, sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	// Linked worktree → collapses to canonical root (".") same as main worktree.
	if filepath.IsAbs(got.ProjectDir) {
		t.Errorf("project_dir should be relative (worktree collapse), got absolute: %q", got.ProjectDir)
	}
	if strings.HasPrefix(got.ProjectDir, "unresolved:") {
		t.Errorf("project_dir should not be unresolved for local worktree session, got: %q", got.ProjectDir)
	}
}

// TestCWDNormalize_ForeignCWD_StoredWithUnresolvedPrefix verifies that when
// the SessionStart projectDir is an absolute path that cannot be resolved as
// a local wipnote repo (foreign-machine ingest scenario), it is stored with
// the "unresolved:" prefix.
func TestCWDNormalize_ForeignCWD_StoredWithUnresolvedPrefix(t *testing.T) {
	// The local project dir (where our DB lives).
	localProjectDir := makeTestRepo(t)

	dbPath := filepath.Join(localProjectDir, ".wipnote", "wipnote.db")
	// Pin WIPNOTE_DB_PATH to the LOCAL db: SessionStart is called below with a
	// FOREIGN projectDir, so DBPath(foreignDir) would resolve an unrelated path.
	// The daemon-routed writes must land in this local handle's file (see
	// openWipnoteTestDB).
	t.Setenv("WIPNOTE_DB_PATH", dbPath)
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	sessionID := "test-cwd-foreign-001"
	// foreignDir is an absolute path that does not exist on this machine and
	// does not resolve to any local wipnote repo — simulating a path from a
	// transcript ingested from another machine.
	foreignDir := "/home/other-user-x7k2/their-project"

	event := &CloudEvent{SessionID: sessionID, CWD: foreignDir}

	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("WIPNOTE_PARENT_SESSION", "")
	t.Setenv("WIPNOTE_NESTING_DEPTH", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_SESSION_ID", "")

	// Pass the foreignDir as projectDir — simulates RenderIngestedSessionHTML
	// passing the foreign source dir to CreateSessionHTML via s.ProjectDir.
	_, err = SessionStart(event, database, foreignDir)
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	got, err := db.GetSession(database, sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	// Foreign machine path → stored with "unresolved:" prefix.
	if !strings.HasPrefix(got.ProjectDir, "unresolved:") {
		t.Errorf("foreign project_dir should have unresolved: prefix, got: %q", got.ProjectDir)
	}
}

// TestCWDNormalize_AlreadyRelative_PassThrough verifies that if project_dir is
// already a relative string (e.g., from a re-ingest of normalized data), it is
// stored unchanged.
func TestCWDNormalize_AlreadyRelative_PassThrough(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "wipnote.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	relPath := "some/relative/path"
	s := &models.Session{
		SessionID:     "test-already-relative-001",
		AgentAssigned: "test-agent",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
		ProjectDir:    relPath,
	}
	if err := db.InsertSession(database, s); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	got, err := db.GetSession(database, s.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ProjectDir != relPath {
		t.Errorf("relative project_dir round-trip: got %q, want %q", got.ProjectDir, relPath)
	}
}

// TestCWDNormalize_SubagentStart_StoresRelativeCWD verifies that a subagent's
// CWD is stored repo-relative in pending_subagent_starts.
func TestCWDNormalize_SubagentStart_StoresRelativeCWD(t *testing.T) {
	projectDir := makeTestRepo(t)

	database, err := openWipnoteTestDB(t, projectDir)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	// Seed the parent session so FK constraints are satisfied.
	parentSessionID := "test-subagent-parent-001"
	if err := db.InsertSession(database, &models.Session{
		SessionID:     parentSessionID,
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertSession parent: %v", err)
	}

	agentID := "test-subagent-agent-001"
	event := &CloudEvent{
		SessionID: parentSessionID,
		AgentID:   agentID,
		AgentType: "feature-coder",
		CWD:       projectDir,
	}

	t.Setenv("CLAUDE_SESSION_ID", parentSessionID)
	t.Setenv("WIPNOTE_AGENT_ID", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_SESSION_ID", parentSessionID)

	_, err = SubagentStart(event, database)
	if err != nil {
		t.Fatalf("SubagentStart: %v", err)
	}

	// Read pending_subagent_starts and verify CWD is relative.
	pending, err := db.GetPendingSubagentStart(database, agentID)
	if err != nil {
		t.Fatalf("GetPendingSubagentStart: %v", err)
	}
	if pending == nil {
		t.Fatal("no pending subagent start row found")
	}
	if filepath.IsAbs(pending.CWD) {
		t.Errorf("pending_subagent_starts.cwd should be relative, got absolute: %q", pending.CWD)
	}
	if strings.HasPrefix(pending.CWD, "unresolved:") {
		t.Errorf("pending cwd should not be unresolved for local session, got: %q", pending.CWD)
	}
}

// TestCWDNormalize_SessionHTML_DataProjectDir verifies that the data-project-dir
// attribute in the rendered session HTML reflects the normalized (relative) value.
func TestCWDNormalize_SessionHTML_DataProjectDir(t *testing.T) {
	projectDir := makeTestRepo(t)
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote", "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	s := &models.Session{
		SessionID:     "test-cwd-html-attr-001",
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
		// s.ProjectDir = absolute path → should be normalized to relative.
		ProjectDir: projectDir,
	}

	CreateSessionHTML(projectDir, s)

	htmlPath := filepath.Join(projectDir, ".wipnote", "sessions", "test-cwd-html-attr-001.html")
	data, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read session HTML: %v", err)
	}
	content := string(data)

	// data-project-dir must not be an absolute path.
	if strings.Contains(content, `data-project-dir="`+projectDir+`"`) {
		t.Errorf("data-project-dir should be normalized (not absolute), found raw absolute path in HTML")
	}
	// Must contain a data-project-dir attribute at all.
	if !strings.Contains(content, `data-project-dir="`) {
		t.Error("missing data-project-dir attribute in session HTML")
	}
}
