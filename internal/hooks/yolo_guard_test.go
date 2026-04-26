package hooks

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/models"
	"time"
)

func init() {
	// Override mergeInProgressFn in tests to always return false, preventing
	// real git state from bleeding into test isolation.
	mergeInProgressFn = func() bool { return false }
}

// TestIsYoloFromDB verifies the SQLite-backed fallback for YOLO detection.
func TestIsYoloFromDB(t *testing.T) {
	// Create a temp directory with a real on-disk DB.
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".htmlgraph")
	os.MkdirAll(filepath.Join(hgDir, ".db"), 0o755)
	dbPath := filepath.Join(hgDir, ".db", "htmlgraph.db")
	t.Setenv("HTMLGRAPH_DB_PATH", dbPath)

	// Open and initialise the DB via the project's Open helper.
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	// Insert a test session with bypassPermissions in metadata.
	_, err = database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status, created_at)
		 VALUES (?, ?, ?, datetime('now'))`,
		"yolo-sess", "claude-code", "active",
	)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	_, err = database.Exec(
		`UPDATE sessions SET metadata = json_set(COALESCE(metadata, '{}'), '$.permission_mode', ?) WHERE session_id = ?`,
		"bypassPermissions", "yolo-sess",
	)
	if err != nil {
		t.Fatalf("update metadata: %v", err)
	}

	// Insert a session with a non-YOLO permission mode.
	_, err = database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status, created_at, metadata)
		 VALUES (?, ?, ?, datetime('now'), json_object('permission_mode', 'default'))`,
		"default-sess", "claude-code", "active",
	)
	if err != nil {
		t.Fatalf("insert default session: %v", err)
	}
	database.Close()

	// YOLO session → true.
	if !isYoloFromDB(hgDir, "yolo-sess") {
		t.Error("expected isYoloFromDB=true for bypassPermissions session")
	}

	// Non-YOLO session → false.
	if isYoloFromDB(hgDir, "default-sess") {
		t.Error("expected isYoloFromDB=false for default permission mode session")
	}

	// Unknown session → false.
	if isYoloFromDB(hgDir, "missing-sess") {
		t.Error("expected isYoloFromDB=false for missing session")
	}

	// Empty session ID → false.
	if isYoloFromDB(hgDir, "") {
		t.Error("expected isYoloFromDB=false for empty session ID")
	}
}

func TestIsYoloFromEvent(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".htmlgraph")
	os.MkdirAll(hgDir, 0o755)

	// bypassPermissions → yolo regardless of DB state.
	event := &CloudEvent{PermissionMode: "bypassPermissions", SessionID: "any-sess"}
	if !isYoloFromEvent(event, hgDir) {
		t.Error("expected yolo when permission_mode=bypassPermissions")
	}

	// Non-empty, non-bypass mode → not yolo regardless of DB state.
	event = &CloudEvent{PermissionMode: "default", SessionID: "any-sess"}
	if isYoloFromEvent(event, hgDir) {
		t.Error("expected non-yolo when permission_mode=default")
	}

	// Empty permission_mode + no DB → not yolo.
	event = &CloudEvent{PermissionMode: "", SessionID: "no-db-sess"}
	if isYoloFromEvent(event, hgDir) {
		t.Error("expected non-yolo with no permission_mode and no DB")
	}

	// Empty permission_mode + DB with bypassPermissions → yolo.
	os.MkdirAll(filepath.Join(hgDir, ".db"), 0o755)
	dbPath := filepath.Join(hgDir, ".db", "htmlgraph.db")
	t.Setenv("HTMLGRAPH_DB_PATH", dbPath)
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	_, err = database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status, created_at, metadata)
		 VALUES (?, ?, ?, datetime('now'), json_object('permission_mode', 'bypassPermissions'))`,
		"yolo-event-sess", "claude-code", "active",
	)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	database.Close()

	event = &CloudEvent{PermissionMode: "", SessionID: "yolo-event-sess"}
	if !isYoloFromEvent(event, hgDir) {
		t.Error("expected yolo from DB fallback when permission_mode is empty and DB has bypassPermissions")
	}

	// Empty permission_mode + DB with default mode → not yolo.
	database, err = db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	_, err = database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status, created_at, metadata)
		 VALUES (?, ?, ?, datetime('now'), json_object('permission_mode', 'default'))`,
		"default-event-sess", "claude-code", "active",
	)
	if err != nil {
		t.Fatalf("insert default session: %v", err)
	}
	database.Close()

	event = &CloudEvent{PermissionMode: "", SessionID: "default-event-sess"}
	if isYoloFromEvent(event, hgDir) {
		t.Error("expected non-yolo from DB fallback for default permission mode")
	}
}

func TestCheckYoloWorkItemGuard(t *testing.T) {
	tests := []struct {
		name      string
		tool      string
		featureID string
		yolo      bool
		blocked   bool
	}{
		{"write without feature in yolo blocks", "Write", "", true, true},
		{"edit without feature in yolo blocks", "Edit", "", true, true},
		{"multiedit without feature in yolo blocks", "MultiEdit", "", true, true},
		{"write with feature in yolo allows", "Write", "feat-123", true, false},
		// Guard is always-on: write without feature blocks even outside yolo.
		{"write without feature outside yolo blocks", "Write", "", false, true},
		{"read without feature in yolo allows", "Read", "", true, false},
		{"bash without feature in yolo allows", "Bash", "", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Pass nil db and empty sessionID — tests without DB fallback.
			// The featureID check is the primary path; sessionHasLinkedFeature
			// is the fallback tested separately.
			result := checkYoloWorkItemGuard(tt.tool, tt.featureID, tt.yolo, "", nil)
			if tt.blocked && result == "" {
				t.Errorf("expected block for tool=%s feature=%q yolo=%v",
					tt.tool, tt.featureID, tt.yolo)
			}
			if !tt.blocked && result != "" {
				t.Errorf("expected allow for tool=%s feature=%q yolo=%v, got: %s",
					tt.tool, tt.featureID, tt.yolo, result)
			}
		})
	}
}

// TestHasAnyActiveWorkItem verifies the DB-backed fallback used when session ID
// propagation is broken in YOLO mode (CLAUDE_ENV_FILE unset).
func TestHasAnyActiveWorkItem(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()

	// No work items → false
	if hasAnyActiveWorkItem(tdb.DB) {
		t.Error("expected false with no work items")
	}

	// Add a todo feature — still false
	tdb.addFeature("feat-todo", "feature", "Todo feature", "todo")
	if hasAnyActiveWorkItem(tdb.DB) {
		t.Error("expected false with only todo feature")
	}

	// Add an in-progress bug → true
	tdb.addFeature("bug-active", "bug", "Active bug", "in-progress")
	if !hasAnyActiveWorkItem(tdb.DB) {
		t.Error("expected true with in-progress bug")
	}

	// nil DB → false (safe guard)
	if hasAnyActiveWorkItem(nil) {
		t.Error("expected false for nil db")
	}
}

// TestCheckYoloWorkItemGuard_AnyActiveWorkItemFallback verifies that the guard
// allows edits when no session-linked feature exists but an in-progress work
// item is present — the YOLO-mode session ID mismatch fallback.
func TestCheckYoloWorkItemGuard_AnyActiveWorkItemFallback(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()

	// No active work items → blocked
	result := checkYoloWorkItemGuard("Write", "", true, "some-session", tdb.DB)
	if result == "" {
		t.Error("expected block when no active work item and session unlinked")
	}

	// Add an in-progress spike → allowed via fallback
	tdb.addFeature("spike-active", "spike", "Active spike", "in-progress")
	result = checkYoloWorkItemGuard("Write", "", true, "some-session", tdb.DB)
	if result != "" {
		t.Errorf("expected allow via hasAnyActiveWorkItem fallback, got: %s", result)
	}
}

// TestCheckYoloBashWorkItemGuard_AnyActiveWorkItemFallback verifies the same
// fallback for Bash file-write commands.
func TestCheckYoloBashWorkItemGuard_AnyActiveWorkItemFallback(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()

	event := &CloudEvent{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "sed -i 's/foo/bar/' file.go"},
	}

	// No active work items → blocked
	result := checkYoloBashWorkItemGuard(event, "", true, "some-session", tdb.DB)
	if result == "" {
		t.Error("expected block when no active work item and session unlinked")
	}

	// Add an in-progress feature → allowed via fallback
	tdb.addFeature("feat-active", "feature", "Active feature", "in-progress")
	result = checkYoloBashWorkItemGuard(event, "", true, "some-session", tdb.DB)
	if result != "" {
		t.Errorf("expected allow via hasAnyActiveWorkItem fallback, got: %s", result)
	}
}

func TestCheckYoloCommitGuard(t *testing.T) {
	tests := []struct {
		name      string
		tool      string
		cmd       string
		yolo      bool
		testRan   bool
		blocked   bool
	}{
		{"git commit without tests in yolo blocks", "Bash", "git commit -m 'foo'", true, false, true},
		{"git commit with tests in yolo allows", "Bash", "git commit -m 'foo'", true, true, false},
		{"git commit outside yolo allows", "Bash", "git commit -m 'foo'", false, false, false},
		{"git add in yolo allows", "Bash", "git add file.go", true, false, false},
		{"non-bash ignored", "Read", "git commit", true, false, false},
		{"git commit amend in yolo blocks without tests", "Bash", "git commit --amend", true, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := &CloudEvent{
				ToolName:  tt.tool,
				ToolInput: map[string]any{"command": tt.cmd},
			}
			result := checkYoloCommitGuard(event, tt.yolo, tt.testRan)
			if tt.blocked && result == "" {
				t.Errorf("expected block for cmd=%q yolo=%v testRan=%v", tt.cmd, tt.yolo, tt.testRan)
			}
			if !tt.blocked && result != "" {
				t.Errorf("expected allow for cmd=%q yolo=%v testRan=%v, got: %s", tt.cmd, tt.yolo, tt.testRan, result)
			}
		})
	}
}

// setupIsolatedProjectDir creates a temp directory with a .htmlgraph
// subdirectory and pins the resolver chain to it for the duration of
// the test. Without overriding CLAUDE_PROJECT_DIR and clearing
// HTMLGRAPH_PROJECT_DIR, paths.ResolveProjectDir would inherit the
// outer Claude Code session's env vars and resolve to the real
// htmlgraph repo root instead of the test's tempDir.
func setupIsolatedProjectDir(t *testing.T) string {
	t.Helper()
	projDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projDir, ".htmlgraph"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_PROJECT_DIR", projDir)
	t.Setenv("HTMLGRAPH_PROJECT_DIR", "")
	// HTMLGRAPH_SESSION_ID must remain non-empty so the resolver's
	// priority-3 step (CLAUDE_PROJECT_DIR check) actually fires; it
	// is gated on HTMLGRAPH_SESSION_ID being set as a stale-env guard.
	if os.Getenv("HTMLGRAPH_SESSION_ID") == "" {
		t.Setenv("HTMLGRAPH_SESSION_ID", "test-session")
	}
	return projDir
}

// TestCheckYoloCommitGuard_ProjectAwareMessage covers bug-f616c2a8.
// The error message must name the test command for the project the
// commit is being attempted in, not a hardcoded "go test or pytest"
// hybrid that confused users in single-language projects.
func TestCheckYoloCommitGuard_ProjectAwareMessage(t *testing.T) {
	cases := []struct {
		name        string
		manifest    string
		manifestSrc string
		wantSubstr  string
	}{
		{"go project", "go.mod", "module example.com/test\n", "go test ./..."},
		{"python pyproject", "pyproject.toml", "[project]\nname=\"t\"\n", "uv run pytest"},
		{"python requirements", "requirements.txt", "pytest\n", "uv run pytest"},
		{"node project", "package.json", `{"name":"t"}`, "npm test"},
		{"rust project", "Cargo.toml", "[package]\nname=\"t\"\n", "cargo test"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			projDir := setupIsolatedProjectDir(t)
			if err := os.WriteFile(filepath.Join(projDir, c.manifest), []byte(c.manifestSrc), 0o644); err != nil {
				t.Fatal(err)
			}
			event := &CloudEvent{
				ToolName:  "Bash",
				ToolInput: map[string]any{"command": "git commit -m 'x'"},
				CWD:       projDir,
			}
			msg := checkYoloCommitGuard(event, true, false)
			if msg == "" {
				t.Fatal("expected commit to be blocked, got empty message")
			}
			if !strings.Contains(msg, c.wantSubstr) {
				t.Errorf("expected message to contain %q, got: %s", c.wantSubstr, msg)
			}
			// The pre-fix hardcoded message contained both go AND pytest.
			// Make sure we don't regress to that hybrid form.
			if strings.Contains(msg, "go test") && strings.Contains(msg, "uv run pytest") {
				t.Errorf("message still emits hybrid hardcoded suggestion: %s", msg)
			}
		})
	}
}

// TestCheckYoloCommitGuard_FallbackForUnknownProjectType verifies that
// when no manifest file is found, the user still gets actionable
// guidance instead of an empty or single-language string.
func TestCheckYoloCommitGuard_FallbackForUnknownProjectType(t *testing.T) {
	projDir := setupIsolatedProjectDir(t)
	event := &CloudEvent{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "git commit -m 'x'"},
		CWD:       projDir,
	}
	msg := checkYoloCommitGuard(event, true, false)
	if msg == "" {
		t.Fatal("expected commit to be blocked, got empty message")
	}
	if !strings.Contains(msg, fallbackTestSuggestion) {
		t.Errorf("expected fallback suggestion in message, got: %s", msg)
	}
}

func TestCheckYoloWorktreeGuard(t *testing.T) {
	tests := []struct {
		name    string
		tool    string
		branch  string
		yolo    bool
		blocked bool
	}{
		{"write on main in yolo blocks", "Write", "main", true, true},
		{"write on main in yolo blocks (master)", "Write", "master", true, true},
		{"write on feature branch allows", "Write", "feat-123", true, false},
		{"write on main outside yolo allows", "Write", "main", false, false},
		{"read on main in yolo allows", "Read", "main", true, false},
		{"write on track branch allows", "Write", "trk-abc123", true, false},
		{"write on track agent branch allows", "Write", "trk-abc123/agent-task1", true, false},
		{"write on yolo-feat branch allows", "Write", "yolo-feat-123", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := checkYoloWorktreeGuard(tt.tool, tt.branch, tt.yolo)
			if tt.blocked && result == "" {
				t.Errorf("expected block")
			}
			if !tt.blocked && result != "" {
				t.Errorf("expected allow, got: %s", result)
			}
		})
	}
}

func TestCheckYoloWorktreeGuard_ErrorMessage(t *testing.T) {
	msg := checkYoloWorktreeGuard("Write", "main", true)
	if msg == "" {
		t.Fatal("expected block message")
	}
	if !strings.Contains(msg, "htmlgraph yolo") {
		t.Errorf("error message should suggest htmlgraph yolo, got: %s", msg)
	}
}

func TestCheckYoloResearchGuard(t *testing.T) {
	tests := []struct {
		name        string
		tool        string
		yolo        bool
		hasResearch bool
		blocked     bool
	}{
		{"write without research in yolo blocks", "Write", true, false, true},
		{"write with research in yolo allows", "Write", true, true, false},
		// Guard is always-on: write without research blocks even outside yolo.
		{"write outside yolo without research blocks", "Write", false, false, true},
		{"write outside yolo with research allows", "Write", false, true, false},
		{"read without research allows", "Read", true, false, false},
		{"edit without research in yolo blocks", "Edit", true, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := checkYoloResearchGuard(tt.tool, tt.yolo, tt.hasResearch)
			if tt.blocked && result == "" {
				t.Errorf("expected block")
			}
			if !tt.blocked && result != "" {
				t.Errorf("expected allow, got: %s", result)
			}
		})
	}
}

func TestCheckYoloDiffReviewGuard(t *testing.T) {
	tests := []struct {
		name       string
		cmd        string
		yolo       bool
		diffRan    bool
		blocked    bool
	}{
		{"commit without diff in yolo blocks", "git commit -m 'x'", true, false, true},
		{"commit with diff in yolo allows", "git commit -m 'x'", true, true, false},
		{"commit outside yolo allows", "git commit -m 'x'", false, false, false},
		{"non-commit allows", "git add .", true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := &CloudEvent{
				ToolName:  "Bash",
				ToolInput: map[string]any{"command": tt.cmd},
			}
			result := checkYoloDiffReviewGuard(event, tt.yolo, tt.diffRan)
			if tt.blocked && result == "" {
				t.Errorf("expected block")
			}
			if !tt.blocked && result != "" {
				t.Errorf("expected allow, got: %s", result)
			}
		})
	}
}

func TestCheckYoloCodeHealthGuard(t *testing.T) {
	// This guard checks file content length after write — tested via integration
	// Unit test covers the skip conditions
	tests := []struct {
		name    string
		tool    string
		path    string
		yolo    bool
		blocked bool
	}{
		{"non-write allows", "Read", "foo.go", true, false},
		{"outside yolo allows", "Write", "foo.go", false, false},
		{"non-go file allows", "Write", "README.md", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := &CloudEvent{
				ToolName:  tt.tool,
				ToolInput: map[string]any{"file_path": tt.path},
			}
			result := checkYoloCodeHealthGuard(event, tt.yolo)
			if tt.blocked && result == "" {
				t.Errorf("expected block")
			}
			if !tt.blocked && result != "" {
				t.Errorf("expected allow, got: %s", result)
			}
		})
	}
}

func TestCheckYoloBudgetGuard(t *testing.T) {
	tests := []struct {
		name    string
		tool    string
		cmd     string
		yolo    bool
		blocked bool
	}{
		{"non-commit allows", "Bash", "git add file.go", true, false},
		{"non-yolo allows", "Bash", "git commit -m 'foo'", false, false},
		{"non-bash allows", "Read", "git commit", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := &CloudEvent{
				ToolName:  tt.tool,
				ToolInput: map[string]any{"command": tt.cmd},
			}
			result := checkYoloBudgetGuard(event, tt.yolo)
			if tt.blocked && result == "" {
				t.Errorf("expected block")
			}
			if !tt.blocked && result != "" {
				t.Errorf("expected allow, got: %s", result)
			}
		})
	}
}

// cleanEnv returns os.Environ() with GIT_INDEX_FILE removed, preventing
// the parent git process's index lock from bleeding into child git commands.
func cleanEnv() []string {
	env := os.Environ()
	out := env[:0]
	for _, e := range env {
		if len(e) >= 14 && e[:14] == "GIT_INDEX_FILE" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// TestBranchForFilePath verifies that branchForFilePath resolves the branch
// from a linked git worktree rather than falling back to the main repo branch.
func TestBranchForFilePath(t *testing.T) {
	// Build a bare main repo with one commit on "main".
	mainRepo := t.TempDir()
	mustGit := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// Strip GIT_INDEX_FILE from env so the parent git process's index lock
		// does not affect child git commands (e.g. when running under pre-commit).
		cmd.Env = cleanEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}

	mustGit(mainRepo, "init", "-b", "main")
	mustGit(mainRepo, "config", "user.email", "test@example.com")
	mustGit(mainRepo, "config", "user.name", "Test")
	// Create an initial commit so we can branch off it.
	readme := filepath.Join(mainRepo, "README.md")
	os.WriteFile(readme, []byte("hello"), 0o644)
	mustGit(mainRepo, "add", "README.md")
	mustGit(mainRepo, "commit", "-m", "init")

	// Add a linked worktree on branch "yolo-feat-abc".
	wtDir := t.TempDir()
	mustGit(mainRepo, "worktree", "add", "-b", "yolo-feat-abc", wtDir)

	// File path inside the linked worktree.
	worktreeFile := filepath.Join(wtDir, "foo.go")

	// branchForFilePath should detect the worktree branch, not "main".
	got := branchForFilePath(worktreeFile, "main")
	if got != "yolo-feat-abc" {
		t.Errorf("expected branch %q for worktree file, got %q", "yolo-feat-abc", got)
	}

	// Empty file path → falls back to cwdBranch.
	got = branchForFilePath("", "main")
	if got != "main" {
		t.Errorf("expected fallback branch %q, got %q", "main", got)
	}

	// File path in the main repo → returns "main".
	mainFile := filepath.Join(mainRepo, "main.go")
	got = branchForFilePath(mainFile, "fallback")
	if got != "main" {
		t.Errorf("expected %q for main repo file, got %q", "main", got)
	}
}

// setupTempGitRepo creates an isolated git repo in a temp dir with an initial
// commit. Returns the repo dir. The test's working directory is changed to the
// repo dir so git commands inside the test operate on the right repo.
func setupTempGitRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	mustGitIn := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		cmd.Env = cleanEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	mustGitIn("init", "-b", "main")
	mustGitIn("config", "user.email", "test@example.com")
	mustGitIn("config", "user.name", "Test")
	// Initial commit so staging works.
	readme := filepath.Join(repoDir, "README.md")
	os.WriteFile(readme, []byte("hello"), 0o644)
	mustGitIn("add", "README.md")
	mustGitIn("commit", "-m", "init")
	// Change cwd so git commands in the tested function operate on this repo.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })
	return repoDir
}

// insertAgentEvent is a test helper that inserts a minimal agent_events row.
func insertAgentEvent(t *testing.T, database *sql.DB, eventID, sessionID, toolName, toolInput, inputSummary, status string) {
	t.Helper()
	now := time.Now().UTC()
	e := &models.AgentEvent{
		EventID:      eventID,
		AgentID:      "agent-test",
		EventType:    "tool_call",
		Timestamp:    now,
		ToolName:     toolName,
		ToolInput:    toolInput,
		InputSummary: inputSummary,
		SessionID:    sessionID,
		Status:       status,
		Source:       "test",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.InsertEvent(database, e); err != nil {
		t.Fatalf("InsertEvent(%s): %v", eventID, err)
	}
}

// TestVisualValidation_SkipsWhenNoUIFilesStaged verifies that a commit with
// only .go files staged passes immediately without requiring a screenshot
// (fix 1: staged-diff precheck).
func TestVisualValidation_SkipsWhenNoUIFilesStaged(t *testing.T) {
	repoDir := setupTempGitRepo(t)

	// Stage a Go file only.
	goFile := filepath.Join(repoDir, "main.go")
	os.WriteFile(goFile, []byte("package main\n"), 0o644)
	cmd := exec.Command("git", "add", "main.go")
	cmd.Dir = repoDir
	cmd.Env = cleanEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	// The guard should pass even with no screenshot recorded in DB.
	event := &CloudEvent{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "git commit -m 'backend only'"},
	}
	result := checkYoloUIValidationGuard(event, true, nil, "sess-go-only")
	if result != "" {
		t.Errorf("expected allow for backend-only commit, got: %s", result)
	}
}

// TestVisualValidation_FiresWhenUIFilesStaged verifies that staging a .html
// file with no screenshot recorded triggers the block (session-state path).
func TestVisualValidation_FiresWhenUIFilesStaged(t *testing.T) {
	repoDir := setupTempGitRepo(t)

	// Stage an HTML file.
	htmlFile := filepath.Join(repoDir, "index.html")
	os.WriteFile(htmlFile, []byte("<html></html>"), 0o644)
	cmd := exec.Command("git", "add", "index.html")
	cmd.Dir = repoDir
	cmd.Env = cleanEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	tdb := setupTestDB(t)
	defer tdb.DB.Close()

	// Record a UI file edit in session state so uiFileCount > 0.
	insertAgentEvent(t, tdb.DB, "evt-edit-html", "test-sess", "Edit",
		`{"file_path":"index.html"}`, "index.html", "completed")

	event := &CloudEvent{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "git commit -m 'ui change'"},
	}
	result := checkYoloUIValidationGuard(event, true, tdb.DB, "test-sess")
	if result == "" {
		t.Error("expected block when HTML file staged but no screenshot recorded")
	}
}

// TestVisualValidation_AcceptsChromeMcpScreenshot verifies that a Chrome MCP
// screenshot (tool_name=mcp__claude-in-chrome__computer, action=screenshot)
// satisfies the gate (fix 3: screenshot detection).
func TestVisualValidation_AcceptsChromeMcpScreenshot(t *testing.T) {
	repoDir := setupTempGitRepo(t)

	// Stage an HTML file.
	htmlFile := filepath.Join(repoDir, "index.html")
	os.WriteFile(htmlFile, []byte("<html></html>"), 0o644)
	cmd := exec.Command("git", "add", "index.html")
	cmd.Dir = repoDir
	cmd.Env = cleanEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	tdb := setupTestDB(t)
	defer tdb.DB.Close()

	// Record a UI file edit so uiFileCount > 0.
	insertAgentEvent(t, tdb.DB, "evt-edit-html2", "test-sess", "Edit",
		`{"file_path":"index.html"}`, "index.html", "completed")

	// Record a Chrome MCP screenshot tool call.
	insertAgentEvent(t, tdb.DB, "evt-screenshot", "test-sess",
		"mcp__claude-in-chrome__computer",
		`{"action":"screenshot"}`, "", "completed")

	event := &CloudEvent{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "git commit -m 'ui with screenshot'"},
	}
	result := checkYoloUIValidationGuard(event, true, tdb.DB, "test-sess")
	if result != "" {
		t.Errorf("expected allow after Chrome MCP screenshot, got: %s", result)
	}
}

// TestVisualValidation_IgnoresNonGitCommitBash verifies that a non-git-commit
// Bash command (e.g. gh issue create) is ignored regardless of staged files
// or screenshot state (fix 2: Bash scope).
func TestVisualValidation_IgnoresNonGitCommitBash(t *testing.T) {
	event := &CloudEvent{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "gh issue create --title 'foo'"},
	}
	// nil DB is safe here — the function must return before touching it.
	result := checkYoloUIValidationGuard(event, true, nil, "sess-gh")
	if result != "" {
		t.Errorf("expected allow for non-git-commit bash, got: %s", result)
	}
}

// TestVisualValidation_GitCommitTreeNotGated verifies that "git commit-tree"
// (a git plumbing sub-command, not the porcelain "git commit") is not gated
// by the visual-validation guard (fix C: anchor Bash command matching).
func TestVisualValidation_GitCommitTreeNotGated(t *testing.T) {
	event := &CloudEvent{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "git commit-tree HEAD~1"},
	}
	// nil DB is safe — the function must return before touching it.
	result := checkYoloUIValidationGuard(event, true, nil, "sess-plumbing")
	if result != "" {
		t.Errorf("expected allow for git commit-tree, got: %s", result)
	}
}

func TestCheckYoloStepsGuard(t *testing.T) {
	// Set up a temp .htmlgraph dir with a feature that has no steps
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".htmlgraph")
	os.MkdirAll(filepath.Join(hgDir, "features"), 0o755)

	// Feature without steps
	noSteps := `<article data-id="feat-nosteps" data-type="feature" data-status="todo">
<h1>No Steps Feature</h1></article>`
	os.WriteFile(filepath.Join(hgDir, "features", "feat-nosteps.html"), []byte(noSteps), 0o644)

	// Feature with steps
	withSteps := `<article data-id="feat-steps" data-type="feature" data-status="todo">
<h1>Steps Feature</h1>
<li data-step-id="step-1">Do thing</li>
<li data-step-id="step-2">Do other</li></article>`
	os.WriteFile(filepath.Join(hgDir, "features", "feat-steps.html"), []byte(withSteps), 0o644)

	tests := []struct {
		name   string
		cmd    string
		yolo   bool
		warned bool
	}{
		{"start without steps warns", "htmlgraph feature start feat-nosteps", true, true},
		{"start with steps allows", "htmlgraph feature start feat-steps", true, false},
		{"start outside yolo allows", "htmlgraph feature start feat-nosteps", false, false},
		{"non-start allows", "htmlgraph feature show feat-nosteps", true, false},
		{"non-bash allows", "htmlgraph feature start feat-nosteps", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := &CloudEvent{
				ToolName:  "Bash",
				ToolInput: map[string]any{"command": tt.cmd},
			}
			result := checkYoloStepsGuard(event, tt.yolo, hgDir)
			if tt.warned && result == "" {
				t.Errorf("expected warning for cmd=%q", tt.cmd)
			}
			if !tt.warned && result != "" {
				t.Errorf("expected no warning for cmd=%q, got: %s", tt.cmd, result)
			}
		})
	}
}
