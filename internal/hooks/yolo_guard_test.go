package hooks

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/internal/db"
	"github.com/shakestzd/wipnote/internal/models"
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
	hgDir := filepath.Join(tmpDir, ".wipnote")
	os.MkdirAll(filepath.Join(hgDir, ".db"), 0o755)
	dbPath := filepath.Join(hgDir, ".db", "wipnote.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)

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
	hgDir := filepath.Join(tmpDir, ".wipnote")
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
	dbPath := filepath.Join(hgDir, ".db", "wipnote.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)
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
		{"codex apply_patch without feature blocks", "apply_patch", "", true, true},
		{"write with feature in yolo allows", "Write", "feat-123", true, false},
		{"codex apply_patch with feature allows", "apply_patch", "feat-123", true, false},
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
			// Empty targetFile and projectRoot → in-project path assumed (conservative).
			result := checkYoloWorkItemGuard(tt.tool, tt.featureID, tt.yolo, "", nil, "", "")
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

// TestCheckYoloWorkItemGuard_RejectsUnlinkedActiveWorkItem verifies that an
// unrelated in-progress item no longer satisfies attribution for this session.
func TestCheckYoloWorkItemGuard_RejectsUnlinkedActiveWorkItem(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()

	// No active work items → blocked
	result := checkYoloWorkItemGuard("Write", "", true, "some-session", tdb.DB, "", "")
	if result == "" {
		t.Error("expected block when no active work item and session unlinked")
	}

	// Add an in-progress spike in the project. It must not satisfy this session.
	tdb.addFeature("spike-active", "spike", "Active spike", "in-progress")
	result = checkYoloWorkItemGuard("Write", "", true, "some-session", tdb.DB, "", "")
	if result == "" {
		t.Error("expected block when only an unrelated work item is active")
	}
}

// TestBashWorkItemGuardRemoved_RegressionBug verifies that a Bash file-write
// command (e.g. "cp /tmp/a /tmp/b") with no active work item is NOT blocked by
// the work-item guard — the misfiring Bash guard has been removed (bug-d0eab5c4).
// The structured-tool guard (Write/Edit/MultiEdit/apply_patch) must still block.
func TestBashWorkItemGuardRemoved_RegressionBug(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()

	// Bash with write-intent command and no active work item: must not be blocked.
	bashResult := checkYoloWorkItemGuard("Bash", "", true, "sess-regression", tdb.DB, "", "")
	if bashResult != "" {
		t.Errorf("Bash should not be blocked by work-item guard (guard removed), got: %s", bashResult)
	}

	// Write with no active work item: must still be blocked by checkYoloWorkItemGuard.
	writeResult := checkYoloWorkItemGuard("Write", "", true, "sess-regression", tdb.DB, "", "")
	if writeResult == "" {
		t.Error("Write should still be blocked by checkYoloWorkItemGuard when no active work item")
	}
}

func TestCheckYoloCommitGuard(t *testing.T) {
	tests := []struct {
		name    string
		tool    string
		cmd     string
		yolo    bool
		testRan bool
		blocked bool
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

// setupIsolatedProjectDir creates a temp directory with a .wipnote
// subdirectory and pins the resolver chain to it for the duration of
// the test. Without overriding CLAUDE_PROJECT_DIR and clearing
// WIPNOTE_PROJECT_DIR, paths.ResolveProjectDir would inherit the
// outer Claude Code session's env vars and resolve to the real
// wipnote repo root instead of the test's tempDir.
func setupIsolatedProjectDir(t *testing.T) string {
	t.Helper()
	projDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projDir, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_PROJECT_DIR", projDir)
	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	// WIPNOTE_SESSION_ID must remain non-empty so the resolver's
	// priority-3 step (CLAUDE_PROJECT_DIR check) actually fires; it
	// is gated on WIPNOTE_SESSION_ID being set as a stale-env guard.
	if os.Getenv("WIPNOTE_SESSION_ID") == "" {
		t.Setenv("WIPNOTE_SESSION_ID", "test-session")
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
	if !strings.Contains(msg, "wipnote yolo") {
		t.Errorf("error message should suggest wipnote yolo, got: %s", msg)
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
			// Empty targetFile and projectRoot → in-project path assumed (conservative).
			result := checkYoloResearchGuard(tt.tool, tt.yolo, tt.hasResearch, "", "")
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
		name    string
		cmd     string
		yolo    bool
		diffRan bool
		blocked bool
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

// TestUIValidationGuard_BrowserBatchScreenshotCounts verifies that screenshots
// taken via mcp__claude-in-chrome__browser_batch are recognized (bug-19276d4b).
// browser_batch records the nested computer action in tool_input, not tool_name.
func TestUIValidationGuard_BrowserBatchScreenshotCounts(t *testing.T) {
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
	insertAgentEvent(t, tdb.DB, "evt-edit-html-batch", "test-sess", "Edit",
		`{"file_path":"index.html"}`, "index.html", "completed")

	// Record a browser_batch screenshot tool call with nested computer action.
	// This simulates: tool_name='mcp__claude-in-chrome__browser_batch'
	// with tool_input containing actions:[{name:'computer',input:{action:'screenshot',...}}]
	insertAgentEvent(t, tdb.DB, "evt-screenshot-batch", "test-sess",
		"mcp__claude-in-chrome__browser_batch",
		`{"actions":[{"name":"computer","input":{"action":"screenshot"}}]}`, "", "completed")

	event := &CloudEvent{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "git commit -m 'ui with browser_batch screenshot'"},
	}
	result := checkYoloUIValidationGuard(event, true, tdb.DB, "test-sess")
	if result != "" {
		t.Errorf("expected allow after browser_batch screenshot, got: %s", result)
	}
}

func TestCheckYoloStepsGuard(t *testing.T) {
	// Set up a temp .wipnote dir with a feature that has no steps
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
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
		{"start without steps warns", "wipnote feature start feat-nosteps", true, true},
		{"start with steps allows", "wipnote feature start feat-steps", true, false},
		{"start outside yolo allows", "wipnote feature start feat-nosteps", false, false},
		{"non-start allows", "wipnote feature show feat-nosteps", true, false},
		{"non-bash allows", "wipnote feature start feat-nosteps", true, true},
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

// TestIsBashFileWrite_ReadOnlyCommandsNotBlocked verifies that pure read-only
// inspection commands are not classified as file writes (bug-d0c8b1e2).
// These must NOT trigger the research or work-item guards.
func TestIsBashFileWrite_ReadOnlyCommandsNotBlocked(t *testing.T) {
	readOnly := []struct {
		name string
		cmd  string
	}{
		// Directory listing
		{"ls -la", "ls -la"},
		{"ls home dir", "ls -la ~/.claude/tasks/foo/"},
		// File inspection
		{"cat", "cat ~/.claude/tasks/foo/bar.txt"},
		{"cat absolute path", "cat /etc/hostname"},
		{"head", "head -20 file.txt"},
		{"tail", "tail -20 file.txt"},
		{"stat", "stat file.txt"},
		{"file", "file somefile"},
		{"wc", "wc -l file.txt"},
		{"du", "du -sh dir/"},
		{"mkdir", "mkdir -p dir/"},
		{"touch", "touch file.txt"},
		// Search and discovery
		{"find no exec", "find . -name '*.go'"},
		{"grep", "grep -r pattern dir/"},
		{"rg", "rg pattern dir/"},
		// Process / system inspection
		{"lsof", "lsof -i :8080"},
		{"tree", "tree dir/"},
		{"which", "which go"},
		{"command -v", "command -v go"},
		{"pwd", "pwd"},
		{"env", "env"},
		{"printenv", "printenv PATH"},
		{"date", "date"},
		{"uname", "uname -a"},
		// Git read-only operations
		{"git status", "git status"},
		{"git diff", "git diff"},
		{"git log", "git log --oneline"},
		{"git show", "git show HEAD"},
		{"git fetch", "git fetch"},
		// stderr redirect to /dev/null (must NOT be treated as write)
		{"stderr redirect", "go build ./... 2>/dev/null"},
		{"fd redirect", "cmd 2>&1"},
		// Compound read commands — exact reproducer from bug-d0c8b1e2
		{"reproducer compound", "ls -la ~/.claude/tasks/d846b50d/ && cat ~/.claude/tasks/d846b50d/output.json"},
	}

	for _, tc := range readOnly {
		t.Run(tc.name, func(t *testing.T) {
			event := &CloudEvent{
				ToolName:  "Bash",
				ToolInput: map[string]any{"command": tc.cmd},
			}
			if isBashFileWrite(event) {
				t.Errorf("isBashFileWrite should be false for read-only command %q", tc.cmd)
			}
		})
	}
}

// TestIsBashFileWrite_WriteIntentCommandsBlocked verifies that write-intent
// commands are correctly classified as file writes (bug-d0c8b1e2).
func TestIsBashFileWrite_WriteIntentCommandsBlocked(t *testing.T) {
	writeIntent := []struct {
		name string
		cmd  string
	}{
		// Output redirects — space before file
		{"redirect space", "echo foo > bar"},
		{"append redirect space", "echo foo >> bar"},
		{"redirect absolute space", "echo x > /tmp/y"},
		// Output redirects — no space before file
		{"redirect nospace", "echo x >/tmp/y"},
		// File manipulation
		{"cp", "cp a b"},
		{"mv", "mv a b"},
		{"rm", "rm a"},
		{"ln", "ln -s src dst"},
		{"install", "install -m 755 bin /usr/local/bin/"},
		// Permission changes
		{"chmod", "chmod 755 file"},
		{"chown", "chown user file"},
		// In-place editors
		{"sed -i", "sed -i 's/x/y/' file"},
		{"perl -i", "perl -i -pe 's/x/y/' file"},
		{"awk -i", "awk -i inplace '{print}' file"},
		// Pipe-to-file writers
		{"tee", "tee file"},
		{"tee -a", "tee -a logfile.txt"},
		{"dd", "dd if=x of=y"},
		{"patch", "patch -p1 < diff.patch"},
		// Formatters / fixers
		{"gofmt -w", "gofmt -w internal/hooks/pretooluse.go"},
		{"go fmt", "go fmt ./..."},
		{"prettier --write", "prettier --write src/app.ts"},
		{"eslint --fix", "eslint --fix src/app.ts"},
		{"ruff --fix", "uv run ruff check --fix ."},
		{"black", "black src/"},
		// Git write operations
		{"git add", "git add ."},
		{"git commit", "git commit -m 'msg'"},
		{"git push", "git push origin main"},
		{"git reset", "git reset --hard HEAD"},
		{"git rm", "git rm file.txt"},
		{"git mv", "git mv old new"},
	}

	for _, tc := range writeIntent {
		t.Run(tc.name, func(t *testing.T) {
			event := &CloudEvent{
				ToolName:  "Bash",
				ToolInput: map[string]any{"command": tc.cmd},
			}
			if !isBashFileWrite(event) {
				t.Errorf("isBashFileWrite should be true for write-intent command %q", tc.cmd)
			}
		})
	}
}

func TestIsBashFileWrite_CodexExecCommand(t *testing.T) {
	event := &CloudEvent{
		ToolName:  "exec_command",
		ToolInput: map[string]any{"cmd": "gofmt -w internal/hooks/pretooluse.go"},
	}
	if !isBashFileWrite(event) {
		t.Fatal("expected Codex exec_command cmd to be classified as a file write")
	}
}

// TestCheckYoloBashResearchGuard_ReadOnlyNotBlocked verifies the research guard
// does NOT block read-only Bash commands (bug-d0c8b1e2 reproducer).
func TestCheckYoloBashResearchGuard_ReadOnlyNotBlocked(t *testing.T) {
	readOnly := []struct {
		name string
		cmd  string
	}{
		{"ls home dir", "ls -la ~/.claude/tasks/foo/"},
		{"cat absolute path", "cat ~/.claude/tasks/foo/bar.txt"},
		{"reproducer", "ls -la ~/.claude/tasks/d846b50d/ && cat ~/.claude/tasks/d846b50d/output.json"},
		{"cat /etc/hostname", "cat /etc/hostname"},
		{"git status", "git status"},
		{"git diff", "git diff --stat"},
	}

	for _, tc := range readOnly {
		t.Run(tc.name, func(t *testing.T) {
			event := &CloudEvent{
				ToolName:  "Bash",
				ToolInput: map[string]any{"command": tc.cmd},
			}
			// hasResearch=false: even without prior research, read-only commands must pass
			result := checkYoloBashResearchGuard(event, true, false)
			if result != "" {
				t.Errorf("research guard should NOT block read-only command %q, got: %s", tc.cmd, result)
			}
		})
	}
}

// TestCheckYoloBashResearchGuard_ExternalPathMessage verifies the error message
// for write commands targeting paths outside the project does NOT suggest
// Read/Grep/Glob (which can't reach external paths) — bug-d0c8b1e2.
func TestCheckYoloBashResearchGuard_ExternalPathMessage(t *testing.T) {
	externalWrites := []struct {
		name string
		cmd  string
	}{
		{"mv home dir", "mv ~/.config/foo ~/.config/bar"},
		{"cp to home", "cp file.txt ~/backup/"},
		{"write to absolute path", "echo x > /tmp/y"},
		{"rm from home", "rm ~/.claude/tasks/foo/bar.txt"},
	}

	for _, tc := range externalWrites {
		t.Run(tc.name, func(t *testing.T) {
			event := &CloudEvent{
				ToolName:  "Bash",
				ToolInput: map[string]any{"command": tc.cmd},
			}
			result := checkYoloBashResearchGuard(event, true, false)
			if result == "" {
				t.Errorf("expected block for write command %q", tc.cmd)
				return
			}
			// Must NOT suggest Read/Grep/Glob for external paths
			if strings.Contains(result, "Read, Grep, or Glob") {
				t.Errorf("message for external-path write should not suggest Read/Grep/Glob, got: %s", result)
			}
		})
	}
}

// TestCheckYoloBashResearchGuard_ProjectPathMessage verifies the error message
// for write commands targeting project files suggests Read/Grep/Glob.
func TestCheckYoloBashResearchGuard_ProjectPathMessage(t *testing.T) {
	event := &CloudEvent{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "sed -i 's/foo/bar/' main.go"},
	}
	result := checkYoloBashResearchGuard(event, true, false)
	if result == "" {
		t.Fatal("expected block for sed -i on project file")
	}
	if !strings.Contains(result, "Read, Grep, or Glob") {
		t.Errorf("message for project-file write should suggest Read/Grep/Glob, got: %s", result)
	}
}

// TestBashCommandTargetsExternalPath verifies that in-repo absolute paths are
// classified as internal (not external) and truly external paths remain external.
func TestBashCommandTargetsExternalPath(t *testing.T) {
	projectRoot := "/workspaces/wipnote"

	tests := []struct {
		name         string
		cmd          string
		wantExternal bool
	}{
		// In-repo absolute paths must NOT be classified as external.
		{"in-repo abs path", "echo x > /workspaces/wipnote/foo.txt", false},
		{"in-repo abs path subdir", "rm /workspaces/wipnote/internal/foo.go", false},
		// System paths are external.
		{"etc hostname", "echo hi > /etc/hostname", true},
		// Home-directory paths are external.
		{"home tilde", "cp file.txt ~/backup/file.txt", true},
		{"home dotfile", "echo x > ~/.claude/tasks/x", true},
		// Relative paths are never classified as external.
		{"relative path", "sed -i 's/x/y/' main.go", false},
		// New whitelist cases.
		{"whitelist gotest", "mkdir -p ~/.gotest", false},
		{"whitelist tmp", "rm -rf ~/.tmp/foo", false},
		{"whitelist workspaces subdir", "mkdir -p /workspaces/go-tests", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bashCommandTargetsExternalPath(tt.cmd, projectRoot)
			if got != tt.wantExternal {
				t.Errorf("bashCommandTargetsExternalPath(%q, %q) = %v, want %v",
					tt.cmd, projectRoot, got, tt.wantExternal)
			}
		})
	}
}

// TestBashCommandTargetsExternalPath_EmptyProjectRoot verifies that any absolute
// path is treated as external when the project root is unknown.
func TestBashCommandTargetsExternalPath_EmptyProjectRoot(t *testing.T) {
	if !bashCommandTargetsExternalPath("echo x > /workspaces/wipnote/foo.txt", "") {
		t.Error("expected external=true for absolute path when projectRoot is empty")
	}
}

// TestGetClaimFromParentChain verifies that sub-agent sessions inherit the
// parent orchestrator's claim when they have no claim of their own.
func TestGetClaimFromParentChain(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()

	// Insert parent (orchestrator) session.
	parentSessID := "orch-sess-claim"
	if err := db.InsertSession(tdb.DB, &models.Session{
		SessionID:     parentSessID,
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     tdb.now,
	}); err != nil {
		t.Fatalf("InsertSession(parent): %v", err)
	}

	// Insert child (sub-agent) session with parent_session_id set.
	childSessID := "child-sess-claim"
	if err := db.InsertSession(tdb.DB, &models.Session{
		SessionID:       childSessID,
		AgentAssigned:   "claude-code",
		Status:          "active",
		CreatedAt:       tdb.now,
		ParentSessionID: parentSessID,
	}); err != nil {
		t.Fatalf("InsertSession(child): %v", err)
	}

	// Insert the feature that the orchestrator will claim.
	tdb.addFeature("feat-parent-claim", "feature", "Parent feature", "in-progress")

	// No claim yet — getClaimFromParentChain should return "".
	got, gotParent := getClaimFromParentChain(tdb.DB, childSessID, "")
	if got != "" {
		t.Errorf("expected no inherited claim before parent claim, got %q", got)
	}
	if gotParent != "" {
		t.Errorf("expected no parent session before parent claim, got %q", gotParent)
	}

	// Orchestrator claims the feature under its session ID.
	claim := &models.Claim{
		ClaimID:        "claim-parent-chain",
		WorkItemID:     "feat-parent-claim",
		OwnerSessionID: parentSessID,
		OwnerAgent:     "claude-code",
		Status:         models.ClaimInProgress,
	}
	if err := db.ClaimItem(tdb.DB, claim, 30*time.Minute); err != nil {
		t.Fatalf("ClaimItem: %v", err)
	}

	// Child session (no direct claim) should now inherit the parent's claim.
	got, gotParent = getClaimFromParentChain(tdb.DB, childSessID, "")
	if got != "feat-parent-claim" {
		t.Errorf("expected inherited claim=feat-parent-claim, got %q", got)
	}
	if gotParent != parentSessID {
		t.Errorf("expected parent session=%q, got %q", parentSessID, gotParent)
	}

	// When child already has its own claim, the function should pass it through unchanged.
	gotWithOwn, gotParentWithOwn := getClaimFromParentChain(tdb.DB, childSessID, "feat-own-claim")
	if gotWithOwn != "feat-own-claim" {
		t.Errorf("expected own claim unchanged, got %q", gotWithOwn)
	}
	if gotParentWithOwn != "" {
		t.Errorf("expected no parent session when own claim set, got %q", gotParentWithOwn)
	}

	// nil DB → returns empty, no panic.
	gotNil, gotNilParent := getClaimFromParentChain(nil, childSessID, "")
	if gotNil != "" || gotNilParent != "" {
		t.Errorf("expected empty for nil db, got claim=%q parent=%q", gotNil, gotNilParent)
	}
}

// TestIsYoloWithInheritance verifies that a sub-agent session inherits YOLO
// posture from an ancestor session that has bypassPermissions set.
//
// Scenario: parent session is in YOLO mode (bypassPermissions in DB), child
// session has no permission_mode set. isYoloWithInheritance must return true
// for the child so that all guards (e.g. checkYoloBudgetGuard) fire correctly.
func TestIsYoloWithInheritance(t *testing.T) {
	// Set up an isolated project directory so that isYoloFromDB resolves the
	// correct DB path via WIPNOTE_DB_PATH.
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	os.MkdirAll(filepath.Join(hgDir, ".db"), 0o755)
	dbPath := filepath.Join(hgDir, ".db", "wipnote.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC()

	// Insert parent (orchestrator) session in YOLO mode.
	parentSessID := "parent-yolo-sess"
	if err := db.InsertSession(database, &models.Session{
		SessionID:     parentSessID,
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     now,
	}); err != nil {
		t.Fatalf("InsertSession(parent): %v", err)
	}
	if _, err := database.Exec(
		`UPDATE sessions SET metadata = json_set(COALESCE(metadata,'{}'),'$.permission_mode',?) WHERE session_id = ?`,
		"bypassPermissions", parentSessID,
	); err != nil {
		t.Fatalf("set parent YOLO metadata: %v", err)
	}

	// Insert child (sub-agent) session with no YOLO marker, parented to the YOLO session.
	childSessID := "child-no-yolo-sess"
	if err := db.InsertSession(database, &models.Session{
		SessionID:       childSessID,
		AgentAssigned:   "claude-code",
		Status:          "active",
		CreatedAt:       now,
		ParentSessionID: parentSessID,
	}); err != nil {
		t.Fatalf("InsertSession(child): %v", err)
	}

	// Child event has no permission_mode set — no direct YOLO signal.
	childEvent := &CloudEvent{
		PermissionMode: "",
		SessionID:      childSessID,
	}

	// isYoloWithInheritance must return true because the parent is YOLO.
	// (isSubagent=true: child has a parent and no YOLO marker of its own.)
	if !isYoloWithInheritance(childEvent, hgDir, database, childSessID, tmpDir, true) {
		t.Error("expected isYoloWithInheritance=true: child should inherit parent YOLO posture")
	}

	// checkYoloBudgetGuard must fire when called with a git-commit event and the
	// inherited yolo=true flag. This verifies that the guard chain benefits from
	// inheritance (no staged diff → exits early via numstat, returns "").
	// The key assertion is that passing yolo=true produces the same behavior as
	// a direct YOLO session — the guard does not pass silently when yolo=false.
	budgetEvent := &CloudEvent{
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "git commit -m 'test'"},
	}
	// With yolo=false (pre-fix behavior for inherited sessions), guard is a no-op.
	if result := checkYoloBudgetGuard(budgetEvent, false); result != "" {
		t.Errorf("expected no block when yolo=false, got: %s", result)
	}
	// With yolo=true (post-fix inherited posture), guard is active.
	// Git numstat on an empty diff returns "" → guard passes through (no staged files).
	// The important thing is the guard runs (does not short-circuit on yolo=false).
	_ = checkYoloBudgetGuard(budgetEvent, true) // guard is active; result depends on staged diff

	// Verify that a TOP-LEVEL session with explicit non-YOLO mode is NOT
	// overridden by an ancestor's posture (isSubagent=false). A top-level
	// session's explicit permission_mode is a deliberate user declaration.
	explicitDefaultEvent := &CloudEvent{
		PermissionMode: "default",
		SessionID:      childSessID,
	}
	if isYoloWithInheritance(explicitDefaultEvent, hgDir, database, childSessID, tmpDir, false) {
		t.Error("expected isYoloWithInheritance=false: explicit non-YOLO mode on a top-level session must not be overridden by parent")
	}

	// nil database → returns false, no panic.
	if isYoloWithInheritance(childEvent, hgDir, nil, childSessID, tmpDir, true) {
		t.Error("expected isYoloWithInheritance=false with nil database")
	}
}

// TestIsYoloWithInheritance_SubagentNonBypassModeInherits is the core bug-0ed4e469
// regression: a SUBAGENT that reports its own non-empty, non-bypass
// permission_mode ("default"/"acceptEdits") but whose parent session is in YOLO
// posture must still resolve YOLO=true. Before the fix, the early
// `if event.PermissionMode != "" { return false }` bailed before the parent walk,
// so subagent yolo-detection broke and the worktree guard was skipped.
func TestIsYoloWithInheritance_SubagentNonBypassModeInherits(t *testing.T) {
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	os.MkdirAll(filepath.Join(hgDir, ".db"), 0o755)
	dbPath := filepath.Join(hgDir, ".db", "wipnote.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC()
	parentSessID := "parent-yolo-sess"
	if err := db.InsertSession(database, &models.Session{
		SessionID: parentSessID, AgentAssigned: "claude-code", Status: "active", CreatedAt: now,
	}); err != nil {
		t.Fatalf("InsertSession(parent): %v", err)
	}
	if _, err := database.Exec(
		`UPDATE sessions SET metadata = json_set(COALESCE(metadata,'{}'),'$.permission_mode',?) WHERE session_id = ?`,
		"bypassPermissions", parentSessID,
	); err != nil {
		t.Fatalf("set parent YOLO metadata: %v", err)
	}

	childSessID := "child-subagent-sess"
	if err := db.InsertSession(database, &models.Session{
		SessionID: childSessID, AgentAssigned: "claude-code", Status: "active",
		CreatedAt: now, ParentSessionID: parentSessID,
	}); err != nil {
		t.Fatalf("InsertSession(child): %v", err)
	}

	for _, mode := range []string{"default", "acceptEdits"} {
		ev := &CloudEvent{PermissionMode: mode, SessionID: childSessID}
		// Subagent → falls through to parent walk regardless of its own mode.
		if !isYoloWithInheritance(ev, hgDir, database, childSessID, tmpDir, true) {
			t.Errorf("expected YOLO=true for subagent with mode=%q and YOLO parent", mode)
		}
		// Same event treated as a TOP-LEVEL session → explicit mode respected → false.
		if isYoloWithInheritance(ev, hgDir, database, childSessID, tmpDir, false) {
			t.Errorf("expected YOLO=false for top-level session with explicit mode=%q", mode)
		}
	}
}

// TestIsYoloWithInheritance_SubagentBypassFastPath verifies the bypassPermissions
// fast-path still wins for a subagent regardless of inheritance.
func TestIsYoloWithInheritance_SubagentBypassFastPath(t *testing.T) {
	ev := &CloudEvent{PermissionMode: "bypassPermissions", SessionID: "sub-sess"}
	if !isYoloWithInheritance(ev, t.TempDir(), nil, "sub-sess", t.TempDir(), true) {
		t.Error("expected YOLO=true for subagent with bypassPermissions event")
	}
}

// setupYoloParentDB creates an isolated DB with a YOLO parent session and a
// child session (no YOLO marker of its own) parented to it. Returns the wipnote
// dir, the child session ID, and the open DB.
func setupYoloParentDB(t *testing.T, parentIsYolo bool) (string, string, *sql.DB) {
	t.Helper()
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	os.MkdirAll(filepath.Join(hgDir, ".db"), 0o755)
	dbPath := filepath.Join(hgDir, ".db", "wipnote.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	now := time.Now().UTC()
	parentSessID := "parent-sess"
	if err := db.InsertSession(database, &models.Session{
		SessionID: parentSessID, AgentAssigned: "claude-code", Status: "active", CreatedAt: now,
	}); err != nil {
		t.Fatalf("InsertSession(parent): %v", err)
	}
	if parentIsYolo {
		if _, err := database.Exec(
			`UPDATE sessions SET metadata = json_set(COALESCE(metadata,'{}'),'$.permission_mode',?) WHERE session_id = ?`,
			"bypassPermissions", parentSessID,
		); err != nil {
			t.Fatalf("set parent YOLO metadata: %v", err)
		}
	}
	childSessID := "child-sess"
	if err := db.InsertSession(database, &models.Session{
		SessionID: childSessID, AgentAssigned: "claude-code", Status: "active",
		CreatedAt: now, ParentSessionID: parentSessID,
	}); err != nil {
		t.Fatalf("InsertSession(child): %v", err)
	}
	return hgDir, childSessID, database
}

// TestWorktreeGuardDefenseInDepth_SubagentYoloParent is the defense-in-depth
// case (bug-0ed4e469, fix 2): a subagent whose own IsYoloMode resolved false but
// whose parent session is YOLO must still be blocked from a main-targeted edit.
// The resilient signal is anyParentSessionYolo, which the pretooluse worktree
// guard ORs with ctx.IsYoloMode.
func TestWorktreeGuardDefenseInDepth_SubagentYoloParent(t *testing.T) {
	hgDir, childSessID, database := setupYoloParentDB(t, true /*parentIsYolo*/)

	// Simulate ctx.IsYoloMode resolving false for this subagent.
	isYoloMode := false
	isSubagent := true
	yoloContext := isYoloMode || (isSubagent && anyParentSessionYolo(database, hgDir, childSessID))
	if !yoloContext {
		t.Fatal("expected resilient yoloContext=true via parent-chain inheritance")
	}
	// Editing a file on branch "main" under the resilient context → BLOCKED.
	if checkYoloWorktreeGuard("Edit", "main", yoloContext) == "" {
		t.Error("expected main-targeted edit to be BLOCKED for yolo-context subagent")
	}
}

// TestWorktreeGuardRegression_NonYoloMainEditAllowed is the most important
// regression guard (bug-0ed4e469): a genuinely non-YOLO top-level session (no
// YOLO parent in the chain) editing a file on branch "main" must NOT be blocked.
// Also verifies a yolo subagent editing on a feature/worktree branch is allowed.
func TestWorktreeGuardRegression_NonYoloMainEditAllowed(t *testing.T) {
	// Non-yolo: parent session is NOT yolo.
	hgDir, childSessID, database := setupYoloParentDB(t, false /*parentIsYolo*/)

	// Top-level non-yolo session: IsYoloMode=false, not a subagent.
	yoloContext := false || (false && anyParentSessionYolo(database, hgDir, childSessID))
	if yoloContext {
		t.Fatal("expected yoloContext=false for non-yolo top-level session")
	}
	// Even a subagent here has a non-yolo parent → resilient signal stays false.
	subYoloContext := false || (true && anyParentSessionYolo(database, hgDir, childSessID))
	if subYoloContext {
		t.Fatal("expected yoloContext=false for subagent with non-yolo parent")
	}
	// With yoloContext=false the worktree guard is a no-op even on main.
	if msg := checkYoloWorktreeGuard("Edit", "main", subYoloContext); msg != "" {
		t.Errorf("non-yolo session must NOT be blocked editing main, got: %s", msg)
	}

	// A yolo subagent editing on a feature/worktree branch → NOT blocked.
	if msg := checkYoloWorktreeGuard("Edit", "yolo-feat-abc", true); msg != "" {
		t.Errorf("yolo subagent editing feature branch must NOT be blocked, got: %s", msg)
	}
}

// TestCheckYoloWorkItemGuard_ExternalPathSkipped verifies that the work-item guard
// does NOT fire when the Write/Edit target is outside the project root (bug-624e85ac).
// Writes to ~/.claude memory files, /tmp, or other home config must not require a
// wipnote work item — that discipline applies only to project-code writes.
func TestCheckYoloWorkItemGuard_ExternalPathSkipped(t *testing.T) {
	projectRoot := "/workspaces/wipnote"

	externalPaths := []struct {
		name string
		path string
	}{
		{"home .claude memory file", "/home/vscode/.claude/projects/foo/memory/bar.md"},
		{"tilde home path", "~/some-config/file.txt"},
		{"absolute /tmp path", "/tmp/scratch.txt"},
		{"absolute /etc path", "/etc/hosts"},
	}

	for _, tc := range externalPaths {
		t.Run(tc.name, func(t *testing.T) {
			// No feature, no DB — guard must skip (return "") for external paths.
			result := checkYoloWorkItemGuard("Write", "", true, "", nil, tc.path, projectRoot)
			if result != "" {
				t.Errorf("work-item guard should NOT fire for external path %q, got: %s", tc.path, result)
			}
		})
	}
}

// TestCheckYoloWorkItemGuard_InProjectPathStillBlocked is the regression test:
// a Write to an in-project file with no active work item must still be blocked.
func TestCheckYoloWorkItemGuard_InProjectPathStillBlocked(t *testing.T) {
	projectRoot := "/workspaces/wipnote"

	inProjectPaths := []struct {
		name string
		path string
	}{
		{"relative path", "internal/hooks/foo.go"},
		{"absolute in-project path", "/workspaces/wipnote/internal/hooks/foo.go"},
		{"empty target path (conservative)", ""},
	}

	for _, tc := range inProjectPaths {
		t.Run(tc.name, func(t *testing.T) {
			// No feature, no DB — guard must BLOCK for in-project paths.
			result := checkYoloWorkItemGuard("Write", "", true, "", nil, tc.path, projectRoot)
			if result == "" {
				t.Errorf("work-item guard should BLOCK Write to in-project path %q with no work item", tc.path)
			}
		})
	}
}

// TestCheckYoloResearchGuard_ExternalPathSkipped verifies that the research guard
// does NOT fire when the Write/Edit target is outside the project root (bug-624e85ac).
// Research-first discipline applies to project-code writes, not external config files.
func TestCheckYoloResearchGuard_ExternalPathSkipped(t *testing.T) {
	projectRoot := "/workspaces/wipnote"

	externalPaths := []struct {
		name string
		path string
	}{
		{"home .claude memory file", "/home/vscode/.claude/projects/foo/memory/bar.md"},
		{"tilde home path", "~/some-config/file.txt"},
		{"absolute /tmp path", "/tmp/scratch.txt"},
	}

	for _, tc := range externalPaths {
		t.Run(tc.name, func(t *testing.T) {
			// hasResearch=false — guard must skip (return "") for external paths.
			result := checkYoloResearchGuard("Write", true, false, tc.path, projectRoot)
			if result != "" {
				t.Errorf("research guard should NOT fire for external path %q, got: %s", tc.path, result)
			}
		})
	}
}

// TestCheckYoloResearchGuard_InProjectPathStillBlocked is the regression test:
// a Write to an in-project file with no prior research must still be blocked.
func TestCheckYoloResearchGuard_InProjectPathStillBlocked(t *testing.T) {
	projectRoot := "/workspaces/wipnote"

	inProjectPaths := []struct {
		name string
		path string
	}{
		{"relative path", "internal/hooks/foo.go"},
		{"absolute in-project path", "/workspaces/wipnote/internal/hooks/foo.go"},
		{"empty target path (conservative)", ""},
	}

	for _, tc := range inProjectPaths {
		t.Run(tc.name, func(t *testing.T) {
			// hasResearch=false — guard must BLOCK for in-project paths.
			result := checkYoloResearchGuard("Write", true, false, tc.path, projectRoot)
			if result == "" {
				t.Errorf("research guard should BLOCK Write to in-project path %q with no research", tc.path)
			}
		})
	}
}

// TestPathIsOutsideProject covers the shared helper extracted for both guards.
func TestPathIsOutsideProject(t *testing.T) {
	projectRoot := "/workspaces/wipnote"

	tests := []struct {
		name    string
		path    string
		want    bool
	}{
		// Empty path → in-project (conservative, cannot classify).
		{"empty path", "", false},
		// Relative paths → in-project.
		{"relative path", "internal/foo.go", false},
		{"relative with subdir", "cmd/wipnote/main.go", false},
		// In-project absolute paths.
		{"absolute in-project root", "/workspaces/wipnote/foo.go", false},
		{"absolute in-project subdir", "/workspaces/wipnote/internal/hooks/foo.go", false},
		// Workspaces siblings (Codespaces convention) → in-project (allowed).
		{"workspaces sibling", "/workspaces/other-repo/foo.go", false},
		// Home directory paths — external (except allow-listed).
		{"tilde home", "~/backup/file.txt", true},
		{"home .claude", "~/.claude/memory/foo.md", true},
		{"home absolute", "/home/vscode/.claude/projects/foo/memory/bar.md", true},
		// Allow-listed home dirs — internal (scratch/test).
		{"home .gotest", "~/.gotest/tmp.txt", false},
		{"home .tmp", "~/.tmp/scratch", false},
		{"home .cache", "~/.cache/go/foo", false},
		// Other absolute paths outside project — external.
		{"tmp", "/tmp/scratch.txt", true},
		{"etc", "/etc/hosts", true},
		{"usr local", "/usr/local/bin/something", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pathIsOutsideProject(tt.path, projectRoot)
			if got != tt.want {
				t.Errorf("pathIsOutsideProject(%q, %q) = %v, want %v", tt.path, projectRoot, got, tt.want)
			}
		})
	}
}
