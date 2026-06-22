package hooks

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/paths"
)

func TestSessionStartStoresProjectDir(t *testing.T) {
	// Set up a temporary project directory with a .wipnote subdir.
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	// Open an in-memory SQLite database.
	database, err := openWipnoteTestDB(t, projectDir)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	sessionID := "test-session-project-dir-001"
	// Set CWD to the temp projectDir so resolveWorktreeParentSession does not
	// accidentally read the real .active-session from the developer's worktree.
	event := &CloudEvent{SessionID: sessionID, CWD: projectDir}

	// Unset env vars that would override session ID or mark this as a subagent.
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("WIPNOTE_PARENT_SESSION", "")
	t.Setenv("WIPNOTE_NESTING_DEPTH", "")
	t.Setenv("CLAUDE_ENV_FILE", "") // prevent writing to a real env file

	_, err = SessionStart(event, database, projectDir)
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	// Retrieve the session from DB and verify project_dir is stored.
	got, err := db.GetSession(database, sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if want := paths.NormalizeProjectDir(projectDir); got.ProjectDir != want {
		t.Errorf("project_dir mismatch: got %q, want %q", got.ProjectDir, want)
	}
}

func TestSessionStartActiveSessionContainsProjectDir(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	database, err := openWipnoteTestDB(t, projectDir)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	sessionID := "test-session-active-file-001"
	// Set CWD to the temp projectDir so resolveWorktreeParentSession does not
	// accidentally read the real .active-session from the developer's worktree.
	event := &CloudEvent{SessionID: sessionID, CWD: projectDir}

	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("WIPNOTE_PARENT_SESSION", "")
	t.Setenv("WIPNOTE_NESTING_DEPTH", "")
	t.Setenv("CLAUDE_ENV_FILE", "") // force fallback to .active-session

	_, err = SessionStart(event, database, projectDir)
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	// .active-session should have been written (CLAUDE_ENV_FILE unset path).
	active := ReadActiveSession(projectDir)
	if active == nil {
		t.Fatal("ReadActiveSession returned nil — .active-session not written")
	}
	if active.ProjectDir != projectDir {
		t.Errorf(".active-session project_dir mismatch: got %q, want %q", active.ProjectDir, projectDir)
	}
}

func TestSessionStartContinueHandoffAdditionalContext(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	database, err := openWipnoteTestDB(t, projectDir)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	if err := db.InsertSession(database, &models.Session{
		SessionID:     "sess-continue-old",
		AgentAssigned: "claude-code",
		Status:        "completed",
		CreatedAt:     time.Now().UTC(),
		ProjectDir:    paths.NormalizeProjectDir(projectDir),
	}); err != nil {
		t.Fatalf("InsertSession prior: %v", err)
	}

	event := &CloudEvent{SessionID: "sess-continue-new", CWD: projectDir}
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("WIPNOTE_PARENT_SESSION", "")
	t.Setenv("WIPNOTE_NESTING_DEPTH", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("WIPNOTE_CONTINUED_FROM", "sess-continue-old")
	t.Setenv("WIPNOTE_CONTINUE_HANDOFF_B64",
		base64.StdEncoding.EncodeToString([]byte("## Continued Session Handoff\n\nResume the existing worktree.")))

	result, err := SessionStart(event, database, projectDir)
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if result == nil || !strings.Contains(result.AdditionalContext, "Continued Session Handoff") {
		t.Fatalf("AdditionalContext missing continue handoff: %#v", result)
	}

	sess, err := db.GetSession(database, "sess-continue-new")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.ContinuedFrom != "sess-continue-old" {
		t.Fatalf("ContinuedFrom = %q, want sess-continue-old", sess.ContinuedFrom)
	}
}

// TestSessionStartWorktreeParentSessionIDPopulated verifies that when a
// subagent session is started with a known parent session ID (as
// resolveWorktreeParentSession would provide), the new session row gets
// parent_session_id set and is_subagent = true.
//
// We test the upsertSession path directly rather than going through
// resolveWorktreeParentSession (which requires a real git worktree) to keep
// the test hermetic.  The FK constraint previously caused INSERT OR IGNORE to
// silently drop the row when the parent session was absent from the test DB.
func TestSessionStartWorktreeParentSessionIDPopulated(t *testing.T) {
	// Set up the project directory.
	mainDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mainDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	database, err := db.Open(filepath.Join(mainDir, ".wipnote", "wipnote.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	// Insert the parent (outer YOLO) session so FK constraint is satisfied.
	parentSessionID := "parent-yolo-session-001"
	if err := db.InsertSession(database, &models.Session{
		SessionID:     parentSessionID,
		AgentAssigned: "claude-code",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
		ProjectDir:    mainDir,
	}); err != nil {
		t.Fatalf("InsertSession parent: %v", err)
	}

	// Write .active-session as the outer YOLO session would have done so that
	// ReadActiveSession can return the parent session ID.
	WriteActiveSession(parentSessionID, mainDir)

	// Verify ReadActiveSession round-trips correctly.
	as := ReadActiveSession(mainDir)
	if as == nil || as.SessionID != parentSessionID {
		t.Fatalf("ReadActiveSession: got %v, want session_id=%q", as, parentSessionID)
	}

	// Simulate what SessionStart does after resolveWorktreeParentSession
	// returns (parentSessionID, true): upsert the subagent session with
	// parent_session_id and is_subagent = true.
	subSessionID := "sub-worktree-session-001"
	if err := upsertSession(database, &models.Session{
		SessionID:       subSessionID,
		AgentAssigned:   "claude-code",
		Status:          "active",
		CreatedAt:       time.Now().UTC(),
		ProjectDir:      mainDir,
		ParentSessionID: parentSessionID,
		IsSubagent:      true,
	}); err != nil {
		t.Fatalf("upsertSession subagent: %v", err)
	}

	got, err := db.GetSession(database, subSessionID)
	if err != nil {
		t.Fatalf("GetSession subagent: %v", err)
	}
	if got.ParentSessionID != parentSessionID {
		t.Errorf("parent_session_id: got %q, want %q", got.ParentSessionID, parentSessionID)
	}
	if !got.IsSubagent {
		t.Error("is_subagent: got false, want true")
	}
}

// Regression for bug-71fc095f: Claude Code session 8d53982f had split-brain HTML
// files in two projects because the user cd'd between projects during the session.
// Each hook fire resolved projectDir from EventCWD walk-up and wrote the session
// HTML to whichever project the user was sitting in at the moment.
//
// Fix: ResolveProjectDir now prefers CLAUDE_PROJECT_DIR (set at session launch)
// over EventCWD walk-up, gated on WIPNOTE_SESSION_ID being present.
func TestSessionStart_PrefersClaudeProjectDirOverCWD(t *testing.T) {
	// Project A: where Claude Code was launched (CLAUDE_PROJECT_DIR).
	projectA := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectA, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote in A: %v", err)
	}

	// Project B: where the user cd'd during the session.
	projectB := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectB, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote in B: %v", err)
	}

	// Simulate the session environment: CLAUDE_PROJECT_DIR points at A,
	// WIPNOTE_SESSION_ID confirms this is a live session (not a stale shell var).
	const testSessionID = "regression-bug-71fc095f"
	t.Setenv("CLAUDE_PROJECT_DIR", projectA)
	t.Setenv("WIPNOTE_SESSION_ID", testSessionID)
	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	t.Setenv("WIPNOTE_PARENT_SESSION", "")
	t.Setenv("WIPNOTE_NESTING_DEPTH", "")
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("CLAUDE_ENV_FILE", "") // prevent real env file writes

	// Open DB in project A (the correct project).
	database, err := db.Open(filepath.Join(projectA, ".wipnote", "wipnote.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	// projectDir is resolved via ResolveProjectDir using the hook's EventCWD=projectB.
	// Before the fix: returns projectB (CWD walk-up wins). After: returns projectA.
	resolvedDir := ResolveProjectDir(projectB, testSessionID)
	if resolvedDir != projectA {
		t.Errorf("ResolveProjectDir(cwd=projectB) = %q, want projectA %q\n"+
			"(CLAUDE_PROJECT_DIR should win over EventCWD — bug-71fc095f regression)",
			resolvedDir, projectA)
	}

	// Fire SessionStart with the correctly resolved project dir (project A).
	event := &CloudEvent{SessionID: testSessionID, CWD: projectB}
	_, err = SessionStart(event, database, resolvedDir)
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	// Session HTML must land in project A, not project B.
	sessionHTMLInA := filepath.Join(projectA, ".wipnote", "sessions", testSessionID+".html")
	if _, err := os.Stat(sessionHTMLInA); err != nil {
		t.Errorf("session HTML not found in project A (%s): %v", sessionHTMLInA, err)
	}
	sessionHTMLInB := filepath.Join(projectB, ".wipnote", "sessions", testSessionID+".html")
	if _, err := os.Stat(sessionHTMLInB); err == nil {
		t.Errorf("session HTML found in project B (%s) — split-brain bug-71fc095f not fixed", sessionHTMLInB)
	}
}

func TestInsertAndGetSessionProjectDir(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "wipnote.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	s := &models.Session{
		SessionID:     "sess-proj-dir-test",
		AgentAssigned: "test-agent",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
		ProjectDir:    "/home/user/myproject",
	}
	if err := db.InsertSession(database, s); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	got, err := db.GetSession(database, s.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ProjectDir != s.ProjectDir {
		t.Errorf("project_dir round-trip: got %q, want %q", got.ProjectDir, s.ProjectDir)
	}
}

func TestSessionStartUsesOnDemandAttributionGuidance(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	database, err := openWipnoteTestDB(t, projectDir)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	// Add an open work item to verify SessionStart does not eagerly list it.
	now := time.Now().UTC()
	if err := db.InsertFeature(database, &db.Feature{
		ID:        "feat-001",
		Type:      "feature",
		Title:     "Auth system",
		Status:    "in-progress",
		Priority:  "high",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("InsertFeature: %v", err)
	}

	sessionID := "test-session-attribution"
	event := &CloudEvent{SessionID: sessionID, CWD: projectDir}

	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("WIPNOTE_PARENT_SESSION", "")
	t.Setenv("WIPNOTE_NESTING_DEPTH", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	if err := os.WriteFile(filepath.Join(projectDir, ".wipnote", ".launch-mode"), []byte(`{"mode":"wipnote-claude"}`), 0o644); err != nil {
		t.Fatalf("write .launch-mode: %v", err)
	}

	result, err := SessionStart(event, database, projectDir)
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	// SessionStart should return concise retrieval guidance.
	if result.AdditionalContext == "" {
		t.Fatal("expected AdditionalContext with startup guidance")
	}

	if testContainsStr(result.AdditionalContext, "Open work items") {
		t.Errorf("startup guidance should not list open work items, got: %s", result.AdditionalContext)
	}

	if testContainsStr(result.AdditionalContext, "Auth system") {
		t.Errorf("startup guidance should not include open item titles, got: %s", result.AdditionalContext)
	}

	if !testContainsStr(result.AdditionalContext, "`wipnote wip`") {
		t.Errorf("startup guidance should mention 'wipnote wip', got: %s", result.AdditionalContext)
	}

	if !testContainsStr(result.AdditionalContext, "`wipnote relevant <topic>`") {
		t.Errorf("startup guidance should mention 'wipnote relevant <topic>', got: %s", result.AdditionalContext)
	}

	if !testContainsStr(result.AdditionalContext, "`wipnote help --compact`") {
		t.Errorf("startup guidance should mention 'wipnote help --compact', got: %s", result.AdditionalContext)
	}
}

// TestSessionStartNoOpenItemsNonBareLaunch verifies that startup guidance still
// appears even when there are no open work items, but the bare-launch nudge
// stays suppressed for launcher-managed sessions.
func TestSessionStartNoOpenItemsNonBareLaunch(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	database, err := openWipnoteTestDB(t, projectDir)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	// No features added — no open work items.
	sessionID := "test-session-no-items-non-bare"
	event := &CloudEvent{SessionID: sessionID, CWD: projectDir}

	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("WIPNOTE_PARENT_SESSION", "")
	t.Setenv("WIPNOTE_NESTING_DEPTH", "")
	t.Setenv("CLAUDE_ENV_FILE", "")

	// Simulate a non-bare launch: write .launch-mode with a recent timestamp
	// so bareLaunchNudge detects it as launched via wipnote claude.
	launchModeFile := filepath.Join(projectDir, ".wipnote", ".launch-mode")
	launchModeData := []byte(`{"mode":"wipnote-claude","pid":1234,"timestamp":"2024-01-01T12:00:00Z"}`)
	if err := os.WriteFile(launchModeFile, launchModeData, 0o644); err != nil {
		t.Fatalf("write .launch-mode: %v", err)
	}

	result, err := SessionStart(event, database, projectDir)
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	if result.AdditionalContext == "" {
		t.Fatal("AdditionalContext should contain startup guidance for non-bare launch with no open items")
	}

	if testContainsStr(result.AdditionalContext, "wipnote claude") {
		t.Errorf("non-bare launch guidance should not include the bare-launch nudge, got: %s", result.AdditionalContext)
	}

	if !testContainsStr(result.AdditionalContext, "`wipnote wip`") {
		t.Errorf("startup guidance should mention 'wipnote wip', got: %s", result.AdditionalContext)
	}
}

// TestSessionStartNoOpenItemsBareLaunch verifies that when there are no open
// work items but the session was started bare (no .launch-mode or stale),
// SessionStart returns startup guidance plus the bare-launch nudge.
func TestSessionStartNoOpenItemsBareLaunch(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	database, err := openWipnoteTestDB(t, projectDir)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	// No features added — no open work items.
	sessionID := "test-session-no-items-bare"
	event := &CloudEvent{SessionID: sessionID, CWD: projectDir}

	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("WIPNOTE_PARENT_SESSION", "")
	t.Setenv("WIPNOTE_NESTING_DEPTH", "")
	t.Setenv("CLAUDE_ENV_FILE", "")

	// Do NOT write .launch-mode, or write it with an old timestamp so
	// bareLaunchNudge detects a bare launch and emits the nudge.
	// bareLaunchNudge returns non-empty text when .launch-mode is absent or >30s old.

	result, err := SessionStart(event, database, projectDir)
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	if result.AdditionalContext == "" {
		t.Fatal("AdditionalContext should contain startup guidance for bare launch with no open items")
	}

	if !testContainsStr(result.AdditionalContext, "wipnote claude") {
		t.Errorf("bareLaunchNudge text should mention 'wipnote claude', got: %s", result.AdditionalContext)
	}

	if !testContainsStr(result.AdditionalContext, "`wipnote help --compact`") {
		t.Errorf("startup guidance should mention 'wipnote help --compact', got: %s", result.AdditionalContext)
	}
}

// testContainsStr is a helper for test assertions (avoids import cycle).
func testContainsStr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
