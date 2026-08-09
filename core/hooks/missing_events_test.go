package hooks

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
	worktreepkg "github.com/shakestzd/wipnote/core/worktree"
)

// setupMissingEventsDB creates a temp project dir with .wipnote/ and an
// active session, returning the database and the session ID.
func setupMissingEventsDB(t *testing.T) (*testDB, string) {
	t.Helper()
	td := setupTestDB(t)

	// Link the test session to a project dir so ResolveProjectDir works.
	projectDir := t.TempDir()
	t.Setenv("WIPNOTE_SESSION_ID", "test-sess")
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	return td, "test-sess"
}

func setupGitRepoForWorktreeCreate(t *testing.T) string {
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

	prev := worktreepkg.SetReindexFnForTest(func(string, io.Writer) {})
	t.Cleanup(func() { worktreepkg.SetReindexFnForTest(prev) })

	return repoRoot
}

// --- PreCompact ---

// TestPreCompact_RecordsCheckpoint verifies that PreCompact records a
// checkpoint event with the expected tool_name.
func TestPreCompact_RecordsCheckpoint(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID: sessionID,
		CWD:       t.TempDir(),
	}

	result, err := PreCompact(event, td.DB)
	if err != nil {
		t.Fatalf("PreCompact: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true from PreCompact")
	}

	var count int
	if err := td.DB.QueryRow(
		`SELECT COUNT(*) FROM agent_events WHERE session_id = ? AND tool_name = 'PreCompact'`,
		sessionID,
	).Scan(&count); err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 PreCompact event, got %d", count)
	}
}

// TestPreCompact_NoSessionID_ReturnsContinue verifies that PreCompact returns
// Continue without error when there is no session ID.
func TestPreCompact_NoSessionID_ReturnsContinue(t *testing.T) {
	td := setupTestDB(t)
	t.Setenv("WIPNOTE_SESSION_ID", "")

	event := &CloudEvent{SessionID: "", CWD: t.TempDir()}
	result, err := PreCompact(event, td.DB)
	if err != nil {
		t.Fatalf("PreCompact: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true when no session ID")
	}
}

// TestPreCompact_DecodesAndRecordsTriggerAndStats verifies that PreCompact
// decodes compaction_trigger and context_stats from the payload and records
// them (trigger in input_summary, context_stats JSON in output_summary).
// feat-16c79e70.
func TestPreCompact_DecodesAndRecordsTriggerAndStats(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID:         sessionID,
		CWD:               t.TempDir(),
		CompactionTrigger: "auto",
		ContextStats: map[string]any{
			"input_tokens": float64(123456),
			"messages":     float64(42),
		},
	}

	result, err := PreCompact(event, td.DB)
	if err != nil {
		t.Fatalf("PreCompact: %v", err)
	}
	if result == nil || !result.Continue {
		t.Fatal("expected Continue=true from PreCompact")
	}
	// Observe-only: must NOT block.
	if result.Decision != "" {
		t.Errorf("expected no decision (observe-only), got %q", result.Decision)
	}

	var inputSummary, outputSummary string
	if err := td.DB.QueryRow(
		`SELECT input_summary, output_summary FROM agent_events WHERE session_id = ? AND tool_name = 'PreCompact'`,
		sessionID,
	).Scan(&inputSummary, &outputSummary); err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	if !strings.Contains(inputSummary, "trigger=auto") {
		t.Errorf("input_summary %q missing trigger=auto", inputSummary)
	}
	var stats map[string]any
	if err := json.Unmarshal([]byte(outputSummary), &stats); err != nil {
		t.Fatalf("output_summary is not valid context_stats JSON: %v (raw=%q)", err, outputSummary)
	}
	if stats["messages"] != float64(42) {
		t.Errorf("context_stats[messages] = %v, want 42", stats["messages"])
	}
}

func TestStop_PrefersLastAssistantMessageOverTranscript(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		// bug-fa036758: Harness, not AgentID, now discriminates
		// assistant-text harness attribution.
		Harness:              HarnessCodex,
		AgentID:              "codex",
		SessionID:            sessionID,
		CWD:                  t.TempDir(),
		TurnID:               "codex-turn-1",
		Model:                "gpt-5.4",
		Timestamp:            "2026-05-08T10:00:00Z",
		LastAssistantMessage: "captured from stop payload",
		StopReason:           "end_turn",
	}

	result, err := Stop(event, td.DB)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if result == nil || !result.Continue {
		t.Fatal("expected Continue=true from Stop")
	}

	var attrsRaw, harness string
	if err := td.DB.QueryRow(`
		SELECT attrs_json, harness FROM otel_signals
		WHERE session_id = ? AND canonical = 'assistant_text'`,
		sessionID,
	).Scan(&attrsRaw, &harness); err != nil {
		t.Fatalf("query assistant_text: %v", err)
	}
	if harness != "codex" {
		t.Errorf("harness = %q, want codex", harness)
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(attrsRaw), &attrs); err != nil {
		t.Fatalf("unmarshal attrs: %v", err)
	}
	if attrs["text"] != "captured from stop payload" {
		t.Errorf("attrs[text] = %q", attrs["text"])
	}
	if attrs["source"] != "hook_payload" {
		t.Errorf("attrs[source] = %q", attrs["source"])
	}
}

func TestAfterAgent_InsertsGeminiPromptResponse(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		// bug-fa036758: Harness, not AgentID, now discriminates
		// assistant-text harness attribution.
		Harness:        HarnessGemini,
		AgentID:        "gemini",
		SessionID:      sessionID,
		CWD:            t.TempDir(),
		Model:          "gemini-2.5-pro",
		Timestamp:      "2026-05-08T10:00:00Z",
		PromptResponse: "Gemini captured response",
	}

	result, err := AfterAgent(event, td.DB)
	if err != nil {
		t.Fatalf("AfterAgent: %v", err)
	}
	if result == nil || !result.Continue {
		t.Fatal("expected Continue=true from AfterAgent")
	}

	var harness, native, attrsRaw string
	if err := td.DB.QueryRow(`
		SELECT harness, native, attrs_json
		FROM otel_signals
		WHERE session_id = ? AND canonical = 'assistant_text'`,
		sessionID,
	).Scan(&harness, &native, &attrsRaw); err != nil {
		t.Fatalf("query assistant_text: %v", err)
	}
	if harness != "gemini_cli" {
		t.Errorf("harness = %q, want gemini_cli", harness)
	}
	if native != "gemini_cli.assistant_turn" {
		t.Errorf("native = %q, want gemini_cli.assistant_turn", native)
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(attrsRaw), &attrs); err != nil {
		t.Fatalf("unmarshal attrs: %v", err)
	}
	if attrs["text"] != "Gemini captured response" {
		t.Errorf("attrs[text] = %q", attrs["text"])
	}
	if attrs["source"] != "hook_payload" {
		t.Errorf("attrs[source] = %q", attrs["source"])
	}
	if attrs["model"] != "gemini-2.5-pro" {
		t.Errorf("attrs[model] = %q", attrs["model"])
	}
}

// --- InstructionsLoaded ---

// TestInstructionsLoaded_RecordsCheckpoint verifies that InstructionsLoaded
// records a checkpoint event with the expected tool_name.
func TestInstructionsLoaded_RecordsCheckpoint(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID: sessionID,
		CWD:       t.TempDir(),
	}

	result, err := InstructionsLoaded(event, td.DB)
	if err != nil {
		t.Fatalf("InstructionsLoaded: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true from InstructionsLoaded")
	}

	var count int
	if err := td.DB.QueryRow(
		`SELECT COUNT(*) FROM agent_events WHERE session_id = ? AND tool_name = 'InstructionsLoaded'`,
		sessionID,
	).Scan(&count); err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 InstructionsLoaded event, got %d", count)
	}
}

// TestInstructionsLoaded_DecodesAndRecordsFields verifies that
// InstructionsLoaded decodes file_path/load_reason/memory_type/globs and
// records them (path/reason/memory in input_summary, full detail JSON in
// output_summary). feat-cd937fc9.
func TestInstructionsLoaded_DecodesAndRecordsFields(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID:  sessionID,
		CWD:        t.TempDir(),
		FilePath:   "/repo/CLAUDE.md",
		LoadReason: "startup",
		MemoryType: "project",
		Globs:      []string{"**/CLAUDE.md"},
	}

	result, err := InstructionsLoaded(event, td.DB)
	if err != nil {
		t.Fatalf("InstructionsLoaded: %v", err)
	}
	if result == nil || !result.Continue {
		t.Fatal("expected Continue=true from InstructionsLoaded")
	}

	var inputSummary, outputSummary string
	if err := td.DB.QueryRow(
		`SELECT input_summary, output_summary FROM agent_events WHERE session_id = ? AND tool_name = 'InstructionsLoaded'`,
		sessionID,
	).Scan(&inputSummary, &outputSummary); err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	if !strings.Contains(inputSummary, "/repo/CLAUDE.md") {
		t.Errorf("input_summary %q missing file path", inputSummary)
	}
	if !strings.Contains(inputSummary, "reason=startup") {
		t.Errorf("input_summary %q missing load reason", inputSummary)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(outputSummary), &detail); err != nil {
		t.Fatalf("output_summary not valid JSON: %v (raw=%q)", err, outputSummary)
	}
	if detail["memory_type"] != "project" {
		t.Errorf("detail[memory_type] = %v, want project", detail["memory_type"])
	}
	if detail["file_path"] != "/repo/CLAUDE.md" {
		t.Errorf("detail[file_path] = %v", detail["file_path"])
	}
}

// TestWorktreeRemove_NoAdditionalContext verifies that the cleanup in
// feat-cd937fc9 removed the dead additionalContext return (CC ignores it for
// this Observational event) while keeping the checkpoint recording.
func TestWorktreeRemove_NoAdditionalContext(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID:    sessionID,
		CWD:          t.TempDir(),
		WorktreePath: "/repo/.claude/worktrees/feat-aabbccdd",
	}

	result, err := WorktreeRemove(event, td.DB)
	if err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if result == nil || !result.Continue {
		t.Fatal("expected Continue=true from WorktreeRemove")
	}
	if result.AdditionalContext != "" {
		t.Errorf("expected no additionalContext (CC ignores it for Observational WorktreeRemove), got %q", result.AdditionalContext)
	}

	var count int
	if err := td.DB.QueryRow(
		`SELECT COUNT(*) FROM agent_events WHERE session_id = ? AND tool_name = 'WorktreeRemove'`,
		sessionID,
	).Scan(&count); err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 WorktreeRemove checkpoint, got %d", count)
	}
}

// --- PermissionRequest ---

// TestPermissionRequest_RecordsCheckpoint verifies PermissionRequest records
// a checkpoint with the tool name in the summary.
func TestPermissionRequest_RecordsCheckpoint(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID: sessionID,
		CWD:       t.TempDir(),
		ToolName:  "Bash",
	}

	result, err := PermissionRequest(event, td.DB)
	if err != nil {
		t.Fatalf("PermissionRequest: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true from PermissionRequest")
	}

	var inputSummary string
	if err := td.DB.QueryRow(
		`SELECT input_summary FROM agent_events WHERE session_id = ? AND tool_name = 'PermissionRequest'`,
		sessionID,
	).Scan(&inputSummary); err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	if inputSummary != "Permission requested for tool: Bash" {
		t.Errorf("unexpected input_summary: %q", inputSummary)
	}
}

// TestPermissionRequest_NoToolName_RecordsGenericSummary verifies that when
// no tool name is present, a generic summary is recorded.
func TestPermissionRequest_NoToolName_RecordsGenericSummary(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID: sessionID,
		CWD:       t.TempDir(),
	}

	result, err := PermissionRequest(event, td.DB)
	if err != nil {
		t.Fatalf("PermissionRequest: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true from PermissionRequest")
	}

	var inputSummary string
	if err := td.DB.QueryRow(
		`SELECT input_summary FROM agent_events WHERE session_id = ? AND tool_name = 'PermissionRequest'`,
		sessionID,
	).Scan(&inputSummary); err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	if inputSummary != "Permission requested" {
		t.Errorf("unexpected input_summary: %q", inputSummary)
	}
}

// --- ConfigChange ---

// TestConfigChange_RecordsPermissionMode verifies that ConfigChange persists
// the permission_mode where the YOLO fallback reads it. It used to be an UPDATE
// of the sessions.metadata JSON column in the per-project read index; that index
// is gone, so the record is now a per-session file under .wipnote/
// (feat-fc3cc9e0). The round trip is asserted through the reader the guards
// actually use, not by inspecting the file directly.
func TestConfigChange_RecordsPermissionMode(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	clearNestedEnv(t)
	projectDir := t.TempDir()
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	event := &CloudEvent{
		SessionID:      sessionID,
		CWD:            projectDir,
		PermissionMode: "bypassPermissions",
	}

	result, err := ConfigChange(event, td.DB)
	if err != nil {
		t.Fatalf("ConfigChange: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true from ConfigChange")
	}

	if got := recordedSessionPermissionMode(wipnoteDir, sessionID); got != "bypassPermissions" {
		t.Errorf("expected recorded permission_mode=bypassPermissions, got %q", got)
	}
	if !isYoloFromRecordedMode(wipnoteDir, sessionID) {
		t.Error("recorded bypassPermissions must resolve as YOLO posture")
	}
}

// TestConfigChange_EmptyPermissionMode_NoUpdate verifies that ConfigChange
// returns Continue without DB update when PermissionMode is empty.
func TestConfigChange_EmptyPermissionMode_NoUpdate(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID:      sessionID,
		CWD:            t.TempDir(),
		PermissionMode: "",
	}

	result, err := ConfigChange(event, td.DB)
	if err != nil {
		t.Fatalf("ConfigChange: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true from ConfigChange")
	}

	// Metadata should remain NULL (not set).
	var metadata interface{}
	if err := td.DB.QueryRow(
		`SELECT metadata FROM sessions WHERE session_id = ?`,
		sessionID,
	).Scan(&metadata); err != nil {
		t.Fatalf("query sessions metadata: %v", err)
	}
	if metadata != nil {
		t.Errorf("expected metadata to remain NULL, got %v", metadata)
	}
}

// --- WorktreeCreate ---

// TestWorktreeCreate_RecordsCheckpoint verifies that WorktreeCreate creates
// the requested git worktree, returns its path, and records a checkpoint event
// with the worktree path in the summary.
func TestWorktreeCreate_RecordsCheckpoint(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)
	repoRoot := setupGitRepoForWorktreeCreate(t)
	basePath := filepath.Join(repoRoot, ".claude", "worktrees")
	worktreeName := "feat-aabbccdd"
	worktreePath := filepath.Join(basePath, worktreeName)

	event := &CloudEvent{
		SessionID:        sessionID,
		CWD:              repoRoot,
		WorktreeBasePath: basePath,
		WorktreeName:     worktreeName,
	}

	got, err := WorktreeCreate(event, td.DB)
	if err != nil {
		t.Fatalf("WorktreeCreate: %v", err)
	}
	if got != worktreePath {
		t.Fatalf("WorktreeCreate path = %q, want %q", got, worktreePath)
	}
	if info, err := os.Stat(got); err != nil || !info.IsDir() {
		t.Fatalf("created path is not a directory: info=%v err=%v", info, err)
	}
	if out, err := exec.Command("git", "-C", got, "rev-parse", "--is-inside-work-tree").Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("created path is not a git worktree: out=%q err=%v", out, err)
	}

	var inputSummary string
	if err := td.DB.QueryRow(
		`SELECT input_summary FROM agent_events WHERE session_id = ? AND tool_name = 'WorktreeCreate'`,
		sessionID,
	).Scan(&inputSummary); err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	expected := "Worktree created: .claude/worktrees/" + worktreeName
	if inputSummary != expected {
		t.Errorf("unexpected input_summary: got %q, want %q", inputSummary, expected)
	}
}

// TestWorktreeCreate_NoPath_RecordsGenericSummary verifies missing replacement
// hook fields abort cleanly and record no generic checkpoint.
func TestWorktreeCreate_NoPath_RecordsGenericSummary(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID: sessionID,
		CWD:       t.TempDir(),
	}

	if _, err := WorktreeCreate(event, td.DB); err == nil {
		t.Fatal("expected missing WorktreeCreate fields to error")
	}

	var count int
	if err := td.DB.QueryRow(
		`SELECT COUNT(*) FROM agent_events WHERE session_id = ? AND tool_name = 'WorktreeCreate'`,
		sessionID,
	).Scan(&count); err != nil {
		t.Fatalf("query agent_events count: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no WorktreeCreate checkpoint on missing fields, got %d", count)
	}
}

// TestWorktreeCreate_NilDB_StillCreatesAndReturnsPath verifies the
// canonical-first fallback (feat-075c110d): when the derived-index handle is
// unavailable (writer_unavailable → nil *sql.DB), WorktreeCreate MUST still
// create the worktree and return its bare path (the #119 stdout contract),
// skipping only the best-effort checkpoint write. It must NOT panic on the
// nil handle and must NOT error.
func TestWorktreeCreate_NilDB_StillCreatesAndReturnsPath(t *testing.T) {
	repoRoot := setupGitRepoForWorktreeCreate(t)
	basePath := filepath.Join(repoRoot, ".claude", "worktrees")
	worktreeName := "feat-deadbeef"
	worktreePath := filepath.Join(basePath, worktreeName)

	event := &CloudEvent{
		SessionID:        "sess-nil-db",
		CWD:              repoRoot,
		WorktreeBasePath: basePath,
		WorktreeName:     worktreeName,
	}

	got, err := WorktreeCreate(event, nil)
	if err != nil {
		t.Fatalf("WorktreeCreate with nil DB: %v", err)
	}
	if got != worktreePath {
		t.Fatalf("WorktreeCreate path = %q, want %q", got, worktreePath)
	}
	if info, err := os.Stat(got); err != nil || !info.IsDir() {
		t.Fatalf("created path is not a directory: info=%v err=%v", info, err)
	}
}

// TestWorktreeCreate_MissingFields_NilDB_StillErrors verifies the contract
// boundary: even on the fallback (nil DB) path, a genuine failure (missing
// worktree_name / worktree_base_path) must STILL return an error so the
// command exits non-zero with no path on stdout.
func TestWorktreeCreate_MissingFields_NilDB_StillErrors(t *testing.T) {
	event := &CloudEvent{SessionID: "sess-x", CWD: t.TempDir()}
	if got, err := WorktreeCreate(event, nil); err == nil {
		t.Fatalf("expected error on missing fields with nil DB, got path=%q nil err", got)
	}
}

// --- TaskCreated ---

// TestTaskCreated_RecordsEventWithSubject verifies that TaskCreated records a
// checkpoint event with the task subject in the summary.
func TestTaskCreated_RecordsEventWithSubject(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID: sessionID,
		CWD:       t.TempDir(),
		TaskID:    "task-001",
		TaskData:  map[string]any{"subject": "Run tests"},
	}

	result, err := TaskCreated(event, td.DB)
	if err != nil {
		t.Fatalf("TaskCreated: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true from TaskCreated")
	}

	var inputSummary string
	if err := td.DB.QueryRow(
		`SELECT input_summary FROM agent_events WHERE session_id = ? AND tool_name = 'TaskCreate'`,
		sessionID,
	).Scan(&inputSummary); err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	if inputSummary != "Task created: Run tests" {
		t.Errorf("unexpected input_summary: %q", inputSummary)
	}
}

// TestTaskCreated_NoSubject_FallsBackToTaskID verifies that when no subject is
// provided, the task ID is used in the summary.
func TestTaskCreated_NoSubject_FallsBackToTaskID(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID: sessionID,
		CWD:       t.TempDir(),
		TaskID:    "task-xyz",
		TaskData:  map[string]any{},
	}

	result, err := TaskCreated(event, td.DB)
	if err != nil {
		t.Fatalf("TaskCreated: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true from TaskCreated")
	}

	var inputSummary string
	if err := td.DB.QueryRow(
		`SELECT input_summary FROM agent_events WHERE session_id = ? AND tool_name = 'TaskCreate'`,
		sessionID,
	).Scan(&inputSummary); err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	if inputSummary != "Task created: task_id=task-xyz" {
		t.Errorf("unexpected input_summary: %q", inputSummary)
	}
}

// --- TaskCompleted ---

// TestTaskCompleted_RecordsEventWithSubject verifies that TaskCompleted records
// a task_completed event with the subject in the summary.
func TestTaskCompleted_RecordsEventWithSubject(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID: sessionID,
		CWD:       t.TempDir(),
		TaskID:    "task-001",
		TaskData:  map[string]any{"subject": "Run tests"},
	}

	result, err := TaskCompleted(event, td.DB)
	if err != nil {
		t.Fatalf("TaskCompleted: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true from TaskCompleted")
	}

	var inputSummary, status string
	if err := td.DB.QueryRow(
		`SELECT input_summary, status FROM agent_events WHERE session_id = ? AND tool_name = 'TaskComplete'`,
		sessionID,
	).Scan(&inputSummary, &status); err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	if inputSummary != "Task completed: Run tests" {
		t.Errorf("unexpected input_summary: %q", inputSummary)
	}
	if status != "completed" {
		t.Errorf("expected status=completed, got %q", status)
	}
}

// TestTaskCompleted_NoSubject_FallsBackToTaskID verifies that when no subject
// is provided, the task ID is used in the summary.
func TestTaskCompleted_NoSubject_FallsBackToTaskID(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID: sessionID,
		CWD:       t.TempDir(),
		TaskID:    "task-abc",
		TaskData:  map[string]any{},
	}

	result, err := TaskCompleted(event, td.DB)
	if err != nil {
		t.Fatalf("TaskCompleted: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true from TaskCompleted")
	}

	var inputSummary string
	if err := td.DB.QueryRow(
		`SELECT input_summary FROM agent_events WHERE session_id = ? AND tool_name = 'TaskComplete'`,
		sessionID,
	).Scan(&inputSummary); err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	if inputSummary != "Task completed: task_id=task-abc" {
		t.Errorf("unexpected input_summary: %q", inputSummary)
	}
}

// --- TeammateIdle (Agent Teams) ---

// TestTeammateIdle_RecordsTeammateName verifies that when a teammate name and
// idle reason are present, the summary includes both.
func TestTeammateIdle_RecordsTeammateName(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID:    sessionID,
		CWD:          t.TempDir(),
		TeammateName: "implementer",
		IdleReason:   "waiting",
	}

	result, err := TeammateIdle(event, td.DB)
	if err != nil {
		t.Fatalf("TeammateIdle: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true")
	}

	var inputSummary string
	if err := td.DB.QueryRow(
		`SELECT input_summary FROM agent_events WHERE session_id = ? AND tool_name = 'TeammateIdle'`,
		sessionID,
	).Scan(&inputSummary); err != nil {
		t.Fatalf("query: %v", err)
	}
	expected := "Teammate implementer went idle (reason: waiting)"
	if inputSummary != expected {
		t.Errorf("input_summary = %q, want %q", inputSummary, expected)
	}
}

// TestTeammateIdle_NoTeammate_GenericSummary verifies legacy behavior when
// no teammate fields are present.
func TestTeammateIdle_NoTeammate_GenericSummary(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{SessionID: sessionID, CWD: t.TempDir()}

	result, err := TeammateIdle(event, td.DB)
	if err != nil {
		t.Fatalf("TeammateIdle: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true")
	}

	var inputSummary string
	if err := td.DB.QueryRow(
		`SELECT input_summary FROM agent_events WHERE session_id = ? AND tool_name = 'TeammateIdle'`,
		sessionID,
	).Scan(&inputSummary); err != nil {
		t.Fatalf("query: %v", err)
	}
	if inputSummary != "Teammate agent went idle" {
		t.Errorf("input_summary = %q, want %q", inputSummary, "Teammate agent went idle")
	}
}

// --- TaskCreated (Agent Teams typed fields) ---

// TestTaskCreated_PrefersTypedSubject verifies that TaskSubject takes priority
// over TaskData["subject"] when both are present.
func TestTaskCreated_PrefersTypedSubject(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID:   sessionID,
		CWD:         t.TempDir(),
		TaskID:      "task-typed",
		TaskSubject: "Build widget",
		TaskData:    map[string]any{"subject": "Old subject"},
	}

	result, err := TaskCreated(event, td.DB)
	if err != nil {
		t.Fatalf("TaskCreated: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true")
	}

	var inputSummary string
	if err := td.DB.QueryRow(
		`SELECT input_summary FROM agent_events WHERE session_id = ? AND tool_name = 'TaskCreate'`,
		sessionID,
	).Scan(&inputSummary); err != nil {
		t.Fatalf("query: %v", err)
	}
	if inputSummary != "Task created: Build widget" {
		t.Errorf("input_summary = %q, want %q", inputSummary, "Task created: Build widget")
	}
}

// TestTaskCreated_FallsBackToTaskData verifies that when TaskSubject is empty,
// the handler falls back to TaskData["subject"].
func TestTaskCreated_FallsBackToTaskData(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID: sessionID,
		CWD:       t.TempDir(),
		TaskID:    "task-fallback",
		TaskData:  map[string]any{"subject": "Fallback subject"},
	}

	result, err := TaskCreated(event, td.DB)
	if err != nil {
		t.Fatalf("TaskCreated: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true")
	}

	var inputSummary string
	if err := td.DB.QueryRow(
		`SELECT input_summary FROM agent_events WHERE session_id = ? AND tool_name = 'TaskCreate'`,
		sessionID,
	).Scan(&inputSummary); err != nil {
		t.Fatalf("query: %v", err)
	}
	if inputSummary != "Task created: Fallback subject" {
		t.Errorf("input_summary = %q, want %q", inputSummary, "Task created: Fallback subject")
	}
}

// --- TaskCreated (EventTaskCreated constant) ---

// TestTaskCompleted_EmptyFeatureID_SkipsQualityGate verifies that when no
// feature is actively claimed (featureID == ""), the quality gate block is
// skipped entirely — no quality_gate event is recorded and no BlockExit2Error
// is returned.
func TestTaskCompleted_EmptyFeatureID_SkipsQualityGate(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)
	// No active_work_items row inserted → cachedGetActiveFeatureID returns "".

	event := &CloudEvent{
		SessionID: sessionID,
		CWD:       t.TempDir(),
		TaskID:    "task-nofeature",
		TaskData:  map[string]any{"subject": "No feature task"},
	}

	result, err := TaskCompleted(event, td.DB)
	if err != nil {
		t.Fatalf("TaskCompleted returned unexpected error: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true when featureID is empty")
	}

	// No quality_gate event should be recorded.
	var count int
	if err := td.DB.QueryRow(
		`SELECT COUNT(*) FROM agent_events WHERE session_id = ? AND event_type = 'quality_gate'`,
		sessionID,
	).Scan(&count); err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 quality_gate events when featureID is empty, got %d", count)
	}
}

// TestTaskCreated_UsesEventTaskCreated verifies that TaskCreated records with
// event_type='task_created' instead of 'check_point'.
func TestTaskCreated_UsesEventTaskCreated(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	event := &CloudEvent{
		SessionID: sessionID,
		CWD:       t.TempDir(),
		TaskID:    "task-type-check",
		TaskData:  map[string]any{"subject": "Type check"},
	}

	_, err := TaskCreated(event, td.DB)
	if err != nil {
		t.Fatalf("TaskCreated: %v", err)
	}

	var eventType string
	if err := td.DB.QueryRow(
		`SELECT event_type FROM agent_events WHERE session_id = ? AND tool_name = 'TaskCreate'`,
		sessionID,
	).Scan(&eventType); err != nil {
		t.Fatalf("query: %v", err)
	}
	if eventType != "task_created" {
		t.Errorf("event_type = %q, want %q", eventType, "task_created")
	}
}

// --- SessionResume ---

// TestSessionResume_ReactivatesCompletedSession verifies that SessionResume
// sets a completed session back to active.
func TestSessionResume_ReactivatesCompletedSession(t *testing.T) {
	database, projectDir := setupLifecycleDB(t)

	sessionID := "resume-test-session-001"
	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)

	// Insert a completed session.
	if err := db.InsertSession(database, &models.Session{
		SessionID:     sessionID,
		AgentAssigned: "claude-code",
		Status:        "completed",
		CreatedAt:     time.Now().UTC(),
		ProjectDir:    projectDir,
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	event := &CloudEvent{SessionID: sessionID, CWD: projectDir}
	result, err := SessionResume(event, database, projectDir)
	if err != nil {
		t.Fatalf("SessionResume: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true from SessionResume")
	}
	if result.AdditionalContext == "" {
		t.Error("expected AdditionalContext to be set (resume confirmation message)")
	}

	// Verify session is now active.
	sess, err := db.GetSession(database, sessionID)
	if err != nil || sess == nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Status != "active" {
		t.Errorf("expected session status=active after resume, got %q", sess.Status)
	}
}

// TestSessionResume_NoSessionID_ReturnsContinue verifies that SessionResume
// returns Continue without error when there is no session ID.
func TestSessionResume_NoSessionID_ReturnsContinue(t *testing.T) {
	database, projectDir := setupLifecycleDB(t)
	t.Setenv("WIPNOTE_SESSION_ID", "")

	event := &CloudEvent{SessionID: "", CWD: projectDir}
	result, err := SessionResume(event, database, projectDir)
	if err != nil {
		t.Fatalf("SessionResume: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true when no session ID")
	}
}

// --- Stop ---

// TestStop_FinalizesSessionHTML verifies that Stop calls FinalizeSessionHTML
// so the event-count badge is correct for harnesses (e.g. Codex) that map
// their task-complete event to this handler instead of SessionEnd.
func TestStop_FinalizesSessionHTML(t *testing.T) {
	projectDir := t.TempDir()
	sessionsDir := filepath.Join(projectDir, ".wipnote", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	database, err := db.Open(filepath.Join(t.TempDir(), "wipnote.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	sessionID := "stop-finalize-test-001"
	sess := &models.Session{
		SessionID:     sessionID,
		AgentAssigned: "codex",
		Status:        "active",
		CreatedAt:     time.Now().UTC(),
	}
	if err := db.InsertSession(database, sess); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	// Create session HTML file so FinalizeSessionHTML has something to update.
	CreateSessionHTML(projectDir, sess)

	t.Setenv("WIPNOTE_SESSION_ID", sessionID)
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)
	t.Setenv("WIPNOTE_AGENT_TYPE", "codex")
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("WIPNOTE_PARENT_SESSION", "")

	event := &CloudEvent{SessionID: sessionID, CWD: projectDir}
	result, err := Stop(event, database)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if result == nil || !result.Continue {
		t.Error("expected Continue=true from Stop")
	}

	htmlPath := filepath.Join(sessionsDir, sessionID+".html")
	data, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read session HTML: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, `data-status="completed"`) {
		t.Error("Stop should have set data-status=completed in session HTML")
	}
	if strings.Contains(content, `data-status="active"`) {
		t.Error("data-status should NOT still be active after Stop")
	}
}
