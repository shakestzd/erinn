package hooks

// TestWorktreeRoundTripRegressionGate is the end-to-end regression test for
// plan-2133adf2 (slices 2–5). It simulates a realistic linked-worktree session
// and asserts that every capture surface stores repo-relative paths — never
// bare absolute paths. The single HostPathPattern scan at the end is the
// regression gate: any future code that bypasses normalization at PreToolUse,
// SessionStart, SubagentStart, worktree events, or CLI --files will be caught
// here.
//
// Capture surfaces exercised:
//   (a) PreToolUse tool_input normalization via normalizeToolInputPaths.
//   (b) SessionStart → sessions.project_dir (DB) + data-project-dir (HTML).
//   (c) SubagentStart → pending_subagent_starts.cwd.
//   (d) WorktreeCreate event → agent_events.input_summary.
//   (e) CLI normalizeFilesInput → returned string.

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/paths"
	worktreepkg "github.com/shakestzd/wipnote/core/worktree"
)

// makeLinkedWorktreeRepo creates:
//   - a temporary main repo with git init, user config, .wipnote/, and one commit
//   - a linked worktree at a separate temp directory
//
// Returns (mainRepoRoot, linkedWtPath). Both paths are EvalSymlinks-canonical.
// The test is skipped if git operations fail (mirrors the pattern in
// cwd_normalize_test.go).
func makeLinkedWorktreeRepo(t *testing.T) (mainRepo, linkedWt string) {
	t.Helper()

	// Main repo setup.
	main := t.TempDir()
	for _, cmd := range [][]string{
		{"git", "-C", main, "init", "-q"},
		{"git", "-C", main, "config", "user.email", "test@example.com"},
		{"git", "-C", main, "config", "user.name", "Test User"},
	} {
		if err := exec.Command(cmd[0], cmd[1:]...).Run(); err != nil {
			t.Skipf("git setup failed (%v): %v", cmd, err)
		}
	}
	// .wipnote/ must exist so resolveWipnoteAnchor accepts this repo.
	if err := os.MkdirAll(filepath.Join(main, ".wipnote", "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	// Need at least one commit before git worktree add.
	if err := exec.Command("git", "-C", main, "commit",
		"--allow-empty", "-m", "init regression test", "-q").Run(); err != nil {
		t.Skipf("git commit failed: %v", err)
	}

	// Create the linked worktree.
	wt := filepath.Join(t.TempDir(), "linked-wt")
	if err := exec.Command("git", "-C", main,
		"worktree", "add", "-q", wt, "-b", "feat-roundtrip-test").Run(); err != nil {
		t.Skipf("git worktree add failed: %v", err)
	}

	// Canonicalize both paths (eliminates macOS /private symlink prefix).
	mainC, err := filepath.EvalSymlinks(main)
	if err != nil {
		t.Fatalf("EvalSymlinks(main): %v", err)
	}
	wtC, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatalf("EvalSymlinks(wt): %v", err)
	}
	paths.ResetNormalizeCacheForTesting()
	return mainC, wtC
}

// assertNoAbsoluteHostPaths scans value for any HostPathPattern match that is
// not already wrapped in an "unresolved:" prefix and fails t if found.
//
// The "unresolved:" prefix is the *allowed* form: the contract permits it for
// paths that cannot be resolved to a local repo root.
func assertNoAbsoluteHostPaths(t *testing.T, surface, value string) {
	t.Helper()
	locs := paths.HostPathPattern.FindAllStringIndex(value, -1)
	for _, loc := range locs {
		// Walk backward to find the start of the token that contains this match.
		start := loc[0]
		// If the preceding bytes contain "unresolved:", the path is intentionally
		// marked and must be allowed.
		prefix := value
		if start > len("unresolved:") {
			prefix = value[start-len("unresolved:") : start]
		} else {
			prefix = value[:start]
		}
		if strings.HasSuffix(prefix, "unresolved:") {
			continue
		}
		tail := min(loc[1]+20, len(value))
		t.Errorf("capture surface %q contains bare absolute host path at offset %d: %q",
			surface, start, value[start:tail])
	}
}

func TestWorktreeRoundTripRegressionGate(t *testing.T) {
	// Build a real linked worktree — skip if git is unavailable.
	mainRepo, linkedWt := makeLinkedWorktreeRepo(t)
	t.Cleanup(paths.ResetNormalizeCacheForTesting)

	// Open the DB in the main repo (where .wipnote/ lives).
	dbPath := filepath.Join(mainRepo, ".wipnote", "wipnote.db")
	// Pin WIPNOTE_DB_PATH so SessionStart's daemon-routed writes (which fall back
	// to a direct write at DBPath(projectRoot) when no daemon is running) land in
	// this handle's file rather than the host cache DB. See openWipnoteTestDB.
	t.Setenv("WIPNOTE_DB_PATH", dbPath)
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	// Clear the hook-level feature-ID cache so this test is isolated.
	featureIDCache = featureIDCacheEntry{}

	// ------------------------------------------------------------------
	// Seed a parent session for FK and env-var dependencies.
	// ------------------------------------------------------------------
	parentSessionID := "test-roundtrip-parent-001"
	if err := db.InsertSession(database, &models.Session{
		SessionID:     parentSessionID,
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertSession parent: %v", err)
	}

	// Env-var setup mirrors the pattern in cwd_normalize_test.go.
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("WIPNOTE_PARENT_SESSION", "")
	t.Setenv("WIPNOTE_NESTING_DEPTH", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_SESSION_ID", "")

	// ------------------------------------------------------------------
	// (a) PreToolUse — tool_input with an absolute file_path inside the
	//     linked worktree. We call normalizeToolInputPaths directly using
	//     the real git-based resolver so the normalization actually runs.
	// ------------------------------------------------------------------
	absFilePath := filepath.Join(linkedWt, "cmd", "main.go")
	toolInput := map[string]any{
		"file_path": absFilePath,
	}
	// Use the production resolver (nil resolver arg → paths.MustNormalize
	// which calls the real git resolver). Pass mainRepo as the explicit
	// repoRoot so the test does not depend on CWD.
	normalizedJSON := normalizeToolInputPaths(toolInput, "Read", mainRepo, nil)
	if normalizedJSON == "" {
		t.Fatal("normalizeToolInputPaths returned empty string")
	}
	var parsedToolInput map[string]any
	if err := json.Unmarshal([]byte(normalizedJSON), &parsedToolInput); err != nil {
		t.Fatalf("unmarshal normalized tool_input: %v", err)
	}
	toolInputFilePath, _ := parsedToolInput["file_path"].(string)
	assertNoAbsoluteHostPaths(t, "PreToolUse.tool_input.file_path", toolInputFilePath)

	// ------------------------------------------------------------------
	// (b) SessionStart — CWD is the linked worktree's absolute path.
	//     SessionStart normalizes project_dir before writing to DB and HTML.
	// ------------------------------------------------------------------
	sessionID := "test-roundtrip-session-001"
	sessionEvent := &CloudEvent{
		SessionID: sessionID,
		CWD:       linkedWt,
	}
	if _, err := SessionStart(sessionEvent, database, mainRepo); err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	// Verify DB: sessions.project_dir
	gotSession, err := db.GetSession(database, sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	assertNoAbsoluteHostPaths(t, "SessionStart.DB.project_dir", gotSession.ProjectDir)

	// Verify HTML: data-project-dir attribute
	htmlPath := filepath.Join(mainRepo, ".wipnote", "sessions", sessionID+".html")
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read session HTML: %v", err)
	}
	htmlContent := string(htmlBytes)
	// Extract the data-project-dir attribute value for assertion.
	// We scan the whole HTML content for any bare host path — simpler and
	// catches any attribute, not just data-project-dir.
	assertNoAbsoluteHostPaths(t, "SessionStart.HTML", htmlContent)

	// ------------------------------------------------------------------
	// (c) SubagentStart — absolute CWD inside the linked worktree.
	// ------------------------------------------------------------------
	agentID := "test-roundtrip-agent-001"
	t.Setenv("WIPNOTE_SESSION_ID", parentSessionID)

	subagentEvent := &CloudEvent{
		SessionID: parentSessionID,
		AgentID:   agentID,
		AgentType: "feature-coder",
		CWD:       linkedWt,
	}
	if _, err := SubagentStart(subagentEvent, database); err != nil {
		t.Fatalf("SubagentStart: %v", err)
	}

	pending, err := db.GetPendingSubagentStart(database, agentID)
	if err != nil {
		t.Fatalf("GetPendingSubagentStart: %v", err)
	}
	if pending == nil {
		t.Fatal("no pending subagent start row found")
	}
	assertNoAbsoluteHostPaths(t, "SubagentStart.pending_subagent_starts.cwd", pending.CWD)

	// ------------------------------------------------------------------
	// (d) WorktreeCreate event — absolute WorktreePath inside the main repo.
	//     We override the package-level resolver to use the real main repo.
	// ------------------------------------------------------------------
	paths.ResetNormalizeCacheForTesting()
	oldResolver := worktreePathResolver
	worktreePathResolver = func(dir string) string {
		// Delegate to the real git-based resolver — this is production behavior.
		return paths.ResolveWipnoteAnchorForDir(dir)
	}
	t.Cleanup(func() {
		worktreePathResolver = oldResolver
		paths.ResetNormalizeCacheForTesting()
	})

	prevReindex := worktreepkg.SetReindexFnForTest(func(string, io.Writer) {})
	t.Cleanup(func() { worktreepkg.SetReindexFnForTest(prevReindex) })
	worktreeName := "feat-roundtrip-12345"
	worktreeBasePath := filepath.Join(mainRepo, ".claude", "worktrees")
	worktreeEvent := &CloudEvent{
		SessionID:        parentSessionID,
		CWD:              mainRepo,
		WorktreeBasePath: worktreeBasePath,
		WorktreeName:     worktreeName,
	}
	createdWorktreePath, err := WorktreeCreate(worktreeEvent, database)
	if err != nil {
		t.Fatalf("WorktreeCreate: %v", err)
	}
	if createdWorktreePath != filepath.Join(worktreeBasePath, worktreeName) {
		t.Fatalf("WorktreeCreate path = %q, want %q", createdWorktreePath, filepath.Join(worktreeBasePath, worktreeName))
	}

	var inputSummary string
	if err := database.QueryRow(
		`SELECT input_summary FROM agent_events WHERE session_id = ? AND tool_name = 'WorktreeCreate'`,
		parentSessionID,
	).Scan(&inputSummary); err != nil {
		t.Fatalf("query agent_events WorktreeCreate: %v", err)
	}
	assertNoAbsoluteHostPaths(t, "WorktreeCreate.agent_events.input_summary", inputSummary)

	// ------------------------------------------------------------------
	// (e) CLI normalizeFilesInput — absolute --files path inside the main
	//     repo. We call paths.MustNormalize directly (same logic as
	//     normalizeFilesInput) to remain in the hooks package without a
	//     cross-package import of cmd/wipnote.
	// ------------------------------------------------------------------
	absFiles := filepath.Join(mainRepo, "cmd", "main.go") +
		"," + filepath.Join(mainRepo, "internal", "hooks", "roundtrip_regression_test.go")

	// Replicate normalizeFilesInput logic inline (avoids importing cmd/wipnote).
	parts := strings.Split(absFiles, ",")
	var normalizedFiles []string
	for _, p := range parts {
		seg := strings.TrimSpace(p)
		if seg == "" {
			continue
		}
		normalizedFiles = append(normalizedFiles, paths.MustNormalize(seg, mainRepo))
	}
	normalizedFilesStr := strings.Join(normalizedFiles, ",")
	assertNoAbsoluteHostPaths(t, "CLI.normalizeFilesInput.result", normalizedFilesStr)
}
