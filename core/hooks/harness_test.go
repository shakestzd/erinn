package hooks

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Codex session-start payload — matches the real captured payload shape from
// /tmp/wipnote-codex-hook-payloads/session-start-86946.json.
const codexSessionStartJSON = `{
	"session_id": "019da445-8036-73c2-a8fc-dacdb57417a8",
	"transcript_path": "/Users/testuser/.codex/sessions/2026/04/19/rollout-2026-04-19T01-45-11-019da445-8036-73c2-a8fc-dacdb57417a8.jsonl",
	"cwd": "/Users/testuser/DevProjects/wipnote",
	"hook_event_name": "SessionStart",
	"model": "gpt-5.4",
	"permission_mode": "default",
	"source": "startup"
}`

// Codex user-prompt payload — matches /tmp/wipnote-codex-hook-payloads/user-prompt-86954.json.
const codexUserPromptJSON = `{
	"session_id": "019da445-8036-73c2-a8fc-dacdb57417a8",
	"turn_id": "019da445-a255-77e1-98c4-9d456711f47b",
	"transcript_path": "/Users/testuser/.codex/sessions/2026/04/19/rollout-2026-04-19T01-45-11-019da445-8036-73c2-a8fc-dacdb57417a8.jsonl",
	"cwd": "/Users/testuser/DevProjects/wipnote",
	"hook_event_name": "UserPromptSubmit",
	"model": "gpt-5.4",
	"permission_mode": "default",
	"prompt": "Do these four small tasks so I can confirm wipnote telemetry is firing."
}`

// Claude CloudEvent payload — typical SessionStart shape sent by Claude Code.
const claudeSessionStartJSON = `{
	"session_id": "sess-abc123",
	"cwd": "/Users/testuser/DevProjects/wipnote",
	"permission_mode": "default",
	"model": "claude-opus-4-5",
	"transcript_path": "/tmp/session.jsonl",
	"source": "startup"
}`

// Gemini payload — best-effort per https://geminicli.com/docs/hooks/reference/.
// geminiSessionStartJSON uses invocation_id as the discriminator (legacy / hypothetical
// future payload shape). Most real Gemini payloads do NOT include invocation_id — see
// geminiSessionStartNoInvocationIDJSON for the realistic case.
const geminiSessionStartJSON = `{
	"invocation_id": "gemini-inv-abc123",
	"session_id": "gemini-sess-xyz789",
	"cwd": "/Users/testuser/DevProjects/wipnote",
	"model": "gemini-2.5-pro"
}`

// geminiSessionStartNoInvocationIDJSON matches the real Gemini CLI hook schema:
// session_id, cwd, hook_event_name, timestamp, source — no invocation_id.
// Without WIPNOTE_AGENT_ID=gemini in the environment this payload is
// indistinguishable from a Codex payload (both have hook_event_name), causing
// the bug where Gemini agent_events rows were written with agent_id='codex'.
const geminiSessionStartNoInvocationIDJSON = `{
	"session_id": "00000000-0000-0000-0000-000000000000",
	"cwd": "/workspaces/wipnote",
	"hook_event_name": "SessionStart",
	"timestamp": "2000-01-01T00:00:00Z",
	"source": "startup"
}`

// noClaudeEnv is a getenv stub that has no CLAUDE_CODE_ENTRYPOINT, simulating
// a Codex or Gemini process environment (where Claude Code is not running).
func noClaudeEnv(key string) string { return "" }

// claudeEnv is a getenv stub that reports CLAUDE_CODE_ENTRYPOINT=cli, simulating
// a real Claude Code hook invocation environment.
func claudeEnv(key string) string {
	if key == "CLAUDE_CODE_ENTRYPOINT" {
		return "cli"
	}
	return ""
}

// geminiEnv simulates the environment set by `wipnote gemini` (buildGeminiAgentEnv):
// WIPNOTE_AGENT_ID=gemini, no CLAUDE_CODE_ENTRYPOINT.
func geminiEnv(key string) string {
	if key == "WIPNOTE_AGENT_ID" {
		return "gemini"
	}
	return ""
}

// --- detectHarness tests ---

// Tests below use detectHarnessWithEnv with explicit env stubs so that the
// result is deterministic regardless of whether CLAUDE_CODE_ENTRYPOINT happens
// to be set in the surrounding test process (e.g. when tests run inside Claude Code).

func TestDetectHarnessFromCodexPayload(t *testing.T) {
	got := detectHarnessWithEnv([]byte(codexSessionStartJSON), noClaudeEnv)
	if got != HarnessCodex {
		t.Errorf("detectHarnessWithEnv(codex session-start, noClaudeEnv) = %v, want HarnessCodex", got)
	}
}

func TestDetectHarnessFromCodexUserPromptPayload(t *testing.T) {
	got := detectHarnessWithEnv([]byte(codexUserPromptJSON), noClaudeEnv)
	if got != HarnessCodex {
		t.Errorf("detectHarnessWithEnv(codex user-prompt, noClaudeEnv) = %v, want HarnessCodex", got)
	}
}

func TestDetectHarnessFromClaudePayload(t *testing.T) {
	got := detectHarnessWithEnv([]byte(claudeSessionStartJSON), claudeEnv)
	if got != HarnessClaude {
		t.Errorf("detectHarnessWithEnv(claude session-start, claudeEnv) = %v, want HarnessClaude", got)
	}
}

func TestDetectHarnessFromGeminiPayload(t *testing.T) {
	got := detectHarnessWithEnv([]byte(geminiSessionStartJSON), noClaudeEnv)
	if got != HarnessGemini {
		t.Errorf("detectHarnessWithEnv(gemini session-start, noClaudeEnv) = %v, want HarnessGemini", got)
	}
}

// TestDetectHarness_GeminiWithoutInvocationID is a regression test for the bug
// where Gemini hook payloads that lack "invocation_id" (the real Gemini CLI
// hook schema — session 8de1df19-68e7-43c3-938e-20a2f1322363) were classified
// as HarnessCodex because both harnesses use "hook_event_name". The fix is to
// check WIPNOTE_AGENT_ID=gemini (set by the `wipnote gemini` launcher) before
// any payload-shape inspection.
func TestDetectHarness_GeminiWithoutInvocationID(t *testing.T) {
	// Real Gemini SessionStart payload: has hook_event_name but no invocation_id.
	// Without the WIPNOTE_AGENT_ID fix, detectHarness misclassifies this as Codex.
	got := detectHarnessWithEnv([]byte(geminiSessionStartNoInvocationIDJSON), geminiEnv)
	if got != HarnessGemini {
		t.Errorf("detectHarnessWithEnv(gemini SessionStart without invocation_id, geminiEnv) = %v, want HarnessGemini; "+
			"WIPNOTE_AGENT_ID=gemini must classify as Gemini even when invocation_id is absent", got)
	}
}

// TestDetectHarness_GeminiWithoutInvocationIDNoEnv asserts that a real Gemini
// payload without invocation_id AND without WIPNOTE_AGENT_ID falls back to
// HarnessCodex (payload-shape only). This documents the limitation: users who
// invoke `gemini` directly (not via `wipnote gemini`) still hit the misclassification.
func TestDetectHarness_GeminiWithoutInvocationIDNoEnv(t *testing.T) {
	got := detectHarnessWithEnv([]byte(geminiSessionStartNoInvocationIDJSON), noClaudeEnv)
	if got != HarnessCodex {
		t.Errorf("detectHarnessWithEnv(gemini SessionStart without invocation_id, noClaudeEnv) = %v, want HarnessCodex; "+
			"without WIPNOTE_AGENT_ID the payload is indistinguishable from Codex", got)
	}
}

// TestDetectHarness_GeminiWithHookEventName is a regression test for bug-57c86318:
// Gemini payloads can contain both "invocation_id" and "hook_event_name". The
// detection logic must check for "invocation_id" FIRST (Gemini-exclusive) before
// checking "hook_event_name" (which would incorrectly classify as Codex if checked
// first). Without this ordering, Gemini sessions were recorded as "codex" in the
// dashboard.
func TestDetectHarness_GeminiWithHookEventName(t *testing.T) {
	// Gemini payload with BOTH invocation_id and hook_event_name.
	geminiWithHookEventName := `{
		"invocation_id": "gemini-inv-with-event",
		"session_id": "gemini-sess-with-event",
		"cwd": "/Users/testuser/DevProjects/wipnote",
		"model": "gemini-2.5-pro",
		"hook_event_name": "BeforeAgent"
	}`

	got := detectHarnessWithEnv([]byte(geminiWithHookEventName), noClaudeEnv)
	if got != HarnessGemini {
		t.Errorf("detectHarnessWithEnv(gemini with hook_event_name, noClaudeEnv) = %v, want HarnessGemini; "+
			"invocation_id must take priority over hook_event_name for correct Gemini classification", got)
	}
}

func TestDetectHarnessEmptyPayload(t *testing.T) {
	got := detectHarnessWithEnv([]byte{}, noClaudeEnv)
	if got != HarnessClaude {
		t.Errorf("detectHarnessWithEnv(empty, noClaudeEnv) = %v, want HarnessClaude (default)", got)
	}
}

func TestDetectHarnessInvalidJSON(t *testing.T) {
	got := detectHarnessWithEnv([]byte("not-json"), noClaudeEnv)
	if got != HarnessClaude {
		t.Errorf("detectHarnessWithEnv(invalid json, noClaudeEnv) = %v, want HarnessClaude (fallback)", got)
	}
}

// TestDetectHarness_ClaudeCodeEntrypointWins asserts the fix for bug-1b095c09:
// when CLAUDE_CODE_ENTRYPOINT is set, the harness is always Claude regardless
// of whether the payload contains "hook_event_name" (which Claude Code sends
// for ALL events, previously causing false Codex classification).
func TestDetectHarness_ClaudeCodeEntrypointWins(t *testing.T) {
	// Claude Code SubagentStart payload — has hook_event_name like all Claude Code events.
	claudeSubagentStartJSON := `{
		"session_id": "db130bce-c0b4-4378-bfc6-759f6306849c",
		"cwd": "/workspaces/wipnote",
		"hook_event_name": "SubagentStart",
		"agent_id": "af0d03b76bfb578de",
		"agent_type": "wipnote:researcher"
	}`

	// With CLAUDE_CODE_ENTRYPOINT set → must be HarnessClaude.
	got := detectHarnessWithEnv([]byte(claudeSubagentStartJSON), claudeEnv)
	if got != HarnessClaude {
		t.Errorf("detectHarnessWithEnv(claude subagent-start, claudeEnv) = %v, want HarnessClaude; "+
			"CLAUDE_CODE_ENTRYPOINT must take priority over hook_event_name presence", got)
	}

	// Without CLAUDE_CODE_ENTRYPOINT → hook_event_name causes Codex classification
	// (expected legacy behaviour when running without Claude Code env).
	got2 := detectHarnessWithEnv([]byte(claudeSubagentStartJSON), noClaudeEnv)
	if got2 != HarnessCodex {
		t.Errorf("detectHarnessWithEnv(subagent-start, noClaudeEnv) = %v, want HarnessCodex (hook_event_name present)", got2)
	}
}

// TestDetectHarness_AgentIDPreservedThroughClaudeHarness asserts that when
// CLAUDE_CODE_ENTRYPOINT is set (Claude Code environment), ParseEventForHarness
// uses the Claude path and preserves the raw payload's agent_id and agent_type
// unchanged — it must NOT clobber them to "codex"/"general-purpose".
func TestDetectHarness_AgentIDPreservedThroughClaudeHarness(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")

	payload := []byte(`{
		"session_id": "db130bce-c0b4-4378-bfc6-759f6306849c",
		"cwd": "/workspaces/wipnote",
		"hook_event_name": "SubagentStart",
		"agent_id": "task-uuid-xyz",
		"agent_type": "wipnote:feature-coder"
	}`)

	harness := DetectHarness(payload)
	if harness != HarnessClaude {
		t.Fatalf("DetectHarness = %v, want HarnessClaude when CLAUDE_CODE_ENTRYPOINT is set", harness)
	}

	ev, err := ParseEventForHarness(harness, payload)
	if err != nil {
		t.Fatalf("ParseEventForHarness: %v", err)
	}
	if ev.AgentID != "task-uuid-xyz" {
		t.Errorf("AgentID = %q, want %q; CLAUDE_CODE_ENTRYPOINT must prevent Codex parser from clobbering to 'codex'", ev.AgentID, "task-uuid-xyz")
	}
	if ev.AgentType != "wipnote:feature-coder" {
		t.Errorf("AgentType = %q, want %q", ev.AgentType, "wipnote:feature-coder")
	}
}

// --- parseCodexEvent tests ---

func TestParseCodexSessionStart(t *testing.T) {
	ev, err := parseCodexEvent([]byte(codexSessionStartJSON))
	if err != nil {
		t.Fatalf("parseCodexEvent: %v", err)
	}

	if ev.SessionID != "019da445-8036-73c2-a8fc-dacdb57417a8" {
		t.Errorf("SessionID = %q, want 019da445-8036-73c2-a8fc-dacdb57417a8", ev.SessionID)
	}
	if ev.CWD != "/Users/testuser/DevProjects/wipnote" {
		t.Errorf("CWD = %q, want /Users/testuser/DevProjects/wipnote", ev.CWD)
	}
	if ev.Model != "gpt-5.4" {
		t.Errorf("Model = %q, want gpt-5.4", ev.Model)
	}
	if ev.PermissionMode != "default" {
		t.Errorf("PermissionMode = %q, want default", ev.PermissionMode)
	}
	if ev.Source != "startup" {
		t.Errorf("Source = %q, want startup", ev.Source)
	}
	if ev.TranscriptPath == "" {
		t.Error("TranscriptPath should be populated")
	}
}

func TestParseCodexUserPrompt(t *testing.T) {
	ev, err := parseCodexEvent([]byte(codexUserPromptJSON))
	if err != nil {
		t.Fatalf("parseCodexEvent: %v", err)
	}

	if ev.SessionID != "019da445-8036-73c2-a8fc-dacdb57417a8" {
		t.Errorf("SessionID = %q, want 019da445-8036-73c2-a8fc-dacdb57417a8", ev.SessionID)
	}
	if ev.Prompt == "" {
		t.Error("Prompt should be populated for UserPromptSubmit")
	}
}

func TestParseCodexToolPayload(t *testing.T) {
	payload := []byte(`{
		"session_id": "019da445-8036-73c2-a8fc-dacdb57417a8",
		"turn_id": "019da445-a255-77e1-98c4-9d456711f47b",
		"transcript_path": "/tmp/rollout.jsonl",
		"cwd": "/Users/testuser/DevProjects/wipnote",
		"hook_event_name": "PreToolUse",
		"model": "gpt-5.4",
		"permission_mode": "default",
		"tool_name": "Bash",
		"tool_input": {"command": "pwd"},
		"tool_use_id": "call-123"
	}`)

	ev, err := parseCodexEvent(payload)
	if err != nil {
		t.Fatalf("parseCodexEvent: %v", err)
	}
	if ev.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want Bash", ev.ToolName)
	}
	if ev.ToolUseID != "call-123" {
		t.Errorf("ToolUseID = %q, want call-123", ev.ToolUseID)
	}
	if got, _ := ev.ToolInput["command"].(string); got != "pwd" {
		t.Errorf("ToolInput[command] = %q, want pwd", got)
	}
}

// codexPostToolUseJSON matches the real captured payload shape from a live
// codex-cli 0.147.0 PostToolUse hook (bug-9f49a375, captured 2026-08-07 via
// an isolated CODEX_HOME + a hook command that dumped raw stdin to a file,
// then `codex exec` running `printf 'line1\nline2\n'`). Field names and
// values are unaltered from the capture; only cwd/transcript_path were
// shortened from an absolute scratchpad path for readability. It shows the
// real field is "tool_response" — never "tool_result" — and that its value
// is a bare JSON string (the tool's raw stdout), not an object.
const codexPostToolUseJSON = `{"session_id":"019fdd0e-0195-7683-8045-6eda04789f93","turn_id":"019fdd0e-01dd-7400-9c26-fa44052a22cc","transcript_path":"/tmp/codex-repro/codex_home/sessions/2026/08/07/rollout-2026-08-07T12-28-30-019fdd0e-0195-7683-8045-6eda04789f93.jsonl","cwd":"/tmp/codex-repro/project","hook_event_name":"PostToolUse","model":"gpt-5.6-sol","permission_mode":"bypassPermissions","tool_name":"Bash","tool_input":{"command":"printf 'line1\\nline2\\n'"},"tool_response":"line1\nline2\n","tool_use_id":"exec-2d018645-e9e1-4b08-8bc7-bf7e31e49f39"}`

// codexPostToolUseEmptyOutputJSON matches a real captured payload from the
// same live session for a command that produced no stdout (`echo ... >
// out.txt`, redirected). tool_response is the empty string in this case, not
// omitted and not null.
const codexPostToolUseEmptyOutputJSON = `{"session_id":"019fdd0d-8fca-7523-b4e8-c6d8b22bc927","turn_id":"019fdd0d-9012-7493-8890-0941d8d34c59","transcript_path":"/tmp/codex-repro/codex_home/sessions/2026/08/07/rollout-2026-08-07T12-28-01-019fdd0d-8fca-7523-b4e8-c6d8b22bc927.jsonl","cwd":"/tmp/codex-repro/project","hook_event_name":"PostToolUse","model":"gpt-5.6-sol","permission_mode":"bypassPermissions","tool_name":"Bash","tool_input":{"command":"echo hello-wipnote-repro > out.txt"},"tool_response":"","tool_use_id":"exec-389d018c-aeff-4b92-b8bb-8d3c2a37c582"}`

// codexPostToolUseUpdatePlanJSON matches a real captured payload from the
// same live session for the update_plan tool, confirming the bare-string
// tool_response shape holds across tool types, not just Bash.
const codexPostToolUseUpdatePlanJSON = `{"session_id":"019fdd10-2ca5-7782-8466-2804471bbea4","turn_id":"019fdd10-2ce9-7cb0-9b54-07de7d5f1d26","transcript_path":"/tmp/codex-repro/codex_home/sessions/2026/08/07/rollout-2026-08-07T12-30-52-019fdd10-2ca5-7782-8466-2804471bbea4.jsonl","cwd":"/tmp/codex-repro/project","hook_event_name":"PostToolUse","model":"gpt-5.6-sol","permission_mode":"bypassPermissions","tool_name":"update_plan","tool_input":{"plan":[{"step":"Create hello.txt with content 'hi'","status":"in_progress"},{"step":"Verify hello.txt content","status":"pending"}]},"tool_response":"Plan updated","tool_use_id":"exec-6a416813-d7b0-4de6-8253-61a3d68730b0"}`

// TestParseCodexPostToolUseUsesToolResponseField is the regression test for
// bug-9f49a375: before the fix, codexPayload declared ToolResult with
// `json:"tool_result"`, which the real "tool_response" key never matched, so
// event.ToolResult was silently left nil for every Codex tool call.
func TestParseCodexPostToolUseUsesToolResponseField(t *testing.T) {
	ev, err := parseCodexEvent([]byte(codexPostToolUseJSON))
	if err != nil {
		t.Fatalf("parseCodexEvent: %v", err)
	}
	if ev.ToolResult == nil {
		t.Fatal("ToolResult is nil — tool_response field was discarded")
	}
	if got, _ := ev.ToolResult["output"].(string); got != "line1\nline2\n" {
		t.Errorf(`ToolResult["output"] = %q, want "line1\nline2\n"`, got)
	}
}

// TestParseCodexPostToolUseUpdatePlanToolResponse confirms the bare-string
// tool_response shape (and the fix) also holds for non-Bash tools.
func TestParseCodexPostToolUseUpdatePlanToolResponse(t *testing.T) {
	ev, err := parseCodexEvent([]byte(codexPostToolUseUpdatePlanJSON))
	if err != nil {
		t.Fatalf("parseCodexEvent: %v", err)
	}
	if got, _ := ev.ToolResult["output"].(string); got != "Plan updated" {
		t.Errorf(`ToolResult["output"] = %q, want "Plan updated"`, got)
	}
}

// TestParseCodexPostToolUseEmptyOutputIsNilNotError verifies an empty
// tool_response string (a real, observed shape — not an error case) decodes
// to a nil ToolResult rather than a decode error, matching how
// isSuccess/summariseOutput treat nil today (success=true, no summary).
func TestParseCodexPostToolUseEmptyOutputIsNilNotError(t *testing.T) {
	ev, err := parseCodexEvent([]byte(codexPostToolUseEmptyOutputJSON))
	if err != nil {
		t.Fatalf("parseCodexEvent: %v", err)
	}
	if ev.ToolResult != nil {
		t.Errorf("ToolResult = %v, want nil for empty tool_response", ev.ToolResult)
	}
}

// TestParseCodexPostToolUseLegacyToolResultField is hand-written (no live
// payload has ever been observed using "tool_result" — see the codexPayload
// doc comment). It guards the fallback path in case a different Codex build
// emits the other spelling, or an object shape rather than a bare string.
func TestParseCodexPostToolUseLegacyToolResultField(t *testing.T) {
	payload := []byte(`{
		"session_id": "legacy-sess",
		"hook_event_name": "PostToolUse",
		"tool_name": "Bash",
		"tool_result": {"output": "legacy shape", "is_error": false}
	}`)

	ev, err := parseCodexEvent(payload)
	if err != nil {
		t.Fatalf("parseCodexEvent: %v", err)
	}
	if got, _ := ev.ToolResult["output"].(string); got != "legacy shape" {
		t.Errorf(`ToolResult["output"] = %q, want "legacy shape"`, got)
	}
}

// TestParseCodexPostToolUsePrefersToolResponseOverToolResult is hand-written:
// no live payload has ever sent both keys, but the merge order in
// parseCodexEvent (tool_response wins, tool_result is the fallback) is a
// deliberate choice worth locking in since tool_response is the confirmed
// field.
func TestParseCodexPostToolUsePrefersToolResponseOverToolResult(t *testing.T) {
	payload := []byte(`{
		"hook_event_name": "PostToolUse",
		"tool_response": "current shape wins",
		"tool_result": {"output": "should be ignored"}
	}`)

	ev, err := parseCodexEvent(payload)
	if err != nil {
		t.Fatalf("parseCodexEvent: %v", err)
	}
	if got, _ := ev.ToolResult["output"].(string); got != "current shape wins" {
		t.Errorf(`ToolResult["output"] = %q, want "current shape wins"`, got)
	}
}

func TestParseCodexStopPayloadCapturesAssistantFields(t *testing.T) {
	payload := []byte(`{
		"session_id": "019da445-8036-73c2-a8fc-dacdb57417a8",
		"turn_id": "019da445-a255-77e1-98c4-9d456711f47b",
		"transcript_path": "/tmp/rollout.jsonl",
		"cwd": "/Users/testuser/DevProjects/wipnote",
		"hook_event_name": "Stop",
		"model": "gpt-5.4",
		"timestamp": "2026-05-08T10:00:00Z",
		"last_assistant_message": "Codex final answer",
		"stop_reason": "end_turn"
	}`)

	ev, err := parseCodexEvent(payload)
	if err != nil {
		t.Fatalf("parseCodexEvent: %v", err)
	}
	if ev.TurnID != "019da445-a255-77e1-98c4-9d456711f47b" {
		t.Errorf("TurnID = %q", ev.TurnID)
	}
	if ev.Timestamp != "2026-05-08T10:00:00Z" {
		t.Errorf("Timestamp = %q", ev.Timestamp)
	}
	if ev.LastAssistantMessage != "Codex final answer" {
		t.Errorf("LastAssistantMessage = %q", ev.LastAssistantMessage)
	}
	if ev.StopReason != "end_turn" {
		t.Errorf("StopReason = %q", ev.StopReason)
	}
}

func TestParseCodexEventSetsAgentID(t *testing.T) {
	// Explicitly clear WIPNOTE_PARENT_AGENT so this test is not affected by
	// whatever the shell environment has set (e.g., "claude-code" in dev sessions).
	t.Setenv("WIPNOTE_PARENT_AGENT", "")

	ev, err := parseCodexEvent([]byte(codexSessionStartJSON))
	if err != nil {
		t.Fatalf("parseCodexEvent: %v", err)
	}

	if ev.AgentID != "codex" {
		t.Errorf("AgentID = %q, want codex", ev.AgentID)
	}
}

// TestParseCodexEventAgentIDHardening covers the fix for bug-bfe41623:
// parseCodexEvent must NOT override AgentID with "codex" when
// WIPNOTE_PARENT_AGENT identifies a different harness.
func TestParseCodexEventAgentIDHardening(t *testing.T) {
	tests := []struct {
		name           string
		parentAgentEnv string // value to set in WIPNOTE_PARENT_AGENT ("" = clear/unset)
		wantAgentID    string
	}{
		{
			name:           "codex harness no parent agent env → AgentID=codex",
			parentAgentEnv: "",
			wantAgentID:    "codex",
		},
		{
			name:           "codex harness WIPNOTE_PARENT_AGENT=codex → AgentID=codex",
			parentAgentEnv: "codex",
			wantAgentID:    "codex",
		},
		{
			name:           "routed through codex parser but WIPNOTE_PARENT_AGENT=claude-code → AgentID=claude-code",
			parentAgentEnv: "claude-code",
			wantAgentID:    "claude-code",
		},
		{
			name:           "routed through codex parser but WIPNOTE_PARENT_AGENT=gemini → AgentID=gemini",
			parentAgentEnv: "gemini",
			wantAgentID:    "gemini",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Always set (or clear) the env var so the test is not affected by
			// whatever value happens to be inherited from the shell (e.g. "claude-code"
			// in a live dev session, which is what triggered bug-bfe41623).
			t.Setenv("WIPNOTE_PARENT_AGENT", tt.parentAgentEnv)

			ev, err := parseCodexEvent([]byte(codexSessionStartJSON))
			if err != nil {
				t.Fatalf("parseCodexEvent: %v", err)
			}
			if ev.AgentID != tt.wantAgentID {
				t.Errorf("AgentID = %q, want %q", ev.AgentID, tt.wantAgentID)
			}
		})
	}
}

// --- parseGeminiEvent tests ---

func TestParseGeminiSessionStart(t *testing.T) {
	ev, err := parseGeminiEvent([]byte(geminiSessionStartJSON))
	if err != nil {
		t.Fatalf("parseGeminiEvent: %v", err)
	}

	// When session_id is present, it should be used.
	if ev.SessionID != "gemini-sess-xyz789" {
		t.Errorf("SessionID = %q, want gemini-sess-xyz789", ev.SessionID)
	}
	if ev.CWD != "/Users/testuser/DevProjects/wipnote" {
		t.Errorf("CWD = %q, want /Users/testuser/DevProjects/wipnote", ev.CWD)
	}
}

func TestParseGeminiSessionStartFallsBackToInvocationID(t *testing.T) {
	// When session_id is missing, invocation_id should be used as surrogate.
	payload := `{
		"invocation_id": "gemini-inv-no-session",
		"cwd": "/tmp/project",
		"model": "gemini-2.5-pro"
	}`
	ev, err := parseGeminiEvent([]byte(payload))
	if err != nil {
		t.Fatalf("parseGeminiEvent: %v", err)
	}
	if ev.SessionID != "gemini-inv-no-session" {
		t.Errorf("SessionID = %q, want gemini-inv-no-session (fallback to invocation_id)", ev.SessionID)
	}
}

func TestParseGeminiBeforeTool(t *testing.T) {
	payload := `{
		"invocation_id": "inv-abc",
		"session_id": "gemini-sess-123",
		"cwd": "/tmp/project",
		"tool": {
			"name": "run_shell_command",
			"input": {"command": "ls -la"}
		}
	}`
	ev, err := parseGeminiEvent([]byte(payload))
	if err != nil {
		t.Fatalf("parseGeminiEvent: %v", err)
	}
	if ev.ToolName != "run_shell_command" {
		t.Errorf("ToolName = %q, want run_shell_command", ev.ToolName)
	}
	if ev.ToolInput == nil {
		t.Error("ToolInput should be populated")
	}
}

func TestParseGeminiAfterAgentCapturesPromptResponse(t *testing.T) {
	payload := []byte(`{
		"invocation_id": "inv-abc",
		"session_id": "gemini-sess-123",
		"cwd": "/tmp/project",
		"model": "gemini-2.5-pro",
		"timestamp": "2026-05-08T10:00:00Z",
		"prompt_response": "Gemini final answer"
	}`)

	ev, err := parseGeminiEvent(payload)
	if err != nil {
		t.Fatalf("parseGeminiEvent: %v", err)
	}
	if ev.PromptResponse != "Gemini final answer" {
		t.Errorf("PromptResponse = %q", ev.PromptResponse)
	}
	if ev.Timestamp != "2026-05-08T10:00:00Z" {
		t.Errorf("Timestamp = %q", ev.Timestamp)
	}
	if ev.Model != "gemini-2.5-pro" {
		t.Errorf("Model = %q", ev.Model)
	}
}

func TestParseGeminiEventSetsAgentID(t *testing.T) {
	ev, err := parseGeminiEvent([]byte(geminiSessionStartJSON))
	if err != nil {
		t.Fatalf("parseGeminiEvent: %v", err)
	}

	if ev.AgentID != "gemini" {
		t.Errorf("AgentID = %q, want gemini", ev.AgentID)
	}
}

// --- emitCodexResponse tests ---

func TestEmitCodexSessionStartResponse(t *testing.T) {
	var buf bytes.Buffer
	result := &HookResult{
		AdditionalContext: "foo",
		Continue:          true,
	}
	if err := emitCodexResponse(&buf, result); err != nil {
		t.Fatalf("emitCodexResponse: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal codex response: %v", err)
	}

	if _, ok := got["systemMessage"]; ok {
		t.Errorf("systemMessage should not carry model-visible context, got %v", got["systemMessage"])
	}
	hso, ok := got["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("expected hookSpecificOutput, got %#v", got["hookSpecificOutput"])
	}
	if hso["additionalContext"] != "foo" {
		t.Errorf("additionalContext = %v, want foo", hso["additionalContext"])
	}
	if got["continue"] != true {
		t.Errorf("continue = %v, want true", got["continue"])
	}
	// "additionalContext" must NOT appear at top level.
	if _, ok := got["additionalContext"]; ok {
		t.Error("additionalContext should not appear at top level")
	}
}

func TestEmitCodexBlockResponse(t *testing.T) {
	var buf bytes.Buffer
	result := &HookResult{
		Decision: "block",
		Reason:   "no active work item",
	}
	if err := emitCodexResponseForEvent(&buf, "PreToolUse", result); err != nil {
		t.Fatalf("emitCodexResponse: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal codex response: %v", err)
	}

	if _, ok := got["continue"]; ok {
		t.Errorf("continue = %v, want omitted for Codex block decision", got["continue"])
	}
	if _, ok := got["decision"]; ok {
		t.Errorf("decision should be omitted for Codex PreToolUse block, got %v", got["decision"])
	}
	if _, ok := got["reason"]; ok {
		t.Errorf("reason should be omitted for Codex PreToolUse block, got %v", got["reason"])
	}
	hso, ok := got["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("expected hookSpecificOutput, got %#v", got["hookSpecificOutput"])
	}
	if hso["permissionDecision"] != "deny" {
		t.Errorf("permissionDecision = %v, want deny", hso["permissionDecision"])
	}
	if hso["permissionDecisionReason"] != "no active work item" {
		t.Errorf("permissionDecisionReason = %v, want no active work item", hso["permissionDecisionReason"])
	}
	if _, ok := got["stopReason"]; ok {
		t.Errorf("stopReason must be omitted for Codex PreToolUse block responses, got %v", got["stopReason"])
	}
}

func TestEmitCodexEmptyResponse(t *testing.T) {
	var buf bytes.Buffer
	result := &HookResult{}
	if err := emitCodexResponse(&buf, result); err != nil {
		t.Fatalf("emitCodexResponse: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal codex response: %v", err)
	}

	// Empty result → continue: true (non-blocking allow).
	if got["continue"] != true {
		t.Errorf("continue = %v, want true for empty result", got["continue"])
	}
}

// TestCodexHookSpecificOutput_SchemaFieldsPinned pins codexHookSpecificOutput
// (harness.go) to exactly the four fields Codex's hookSpecificOutput schema
// declares.
//
// WHY this exists (bug-c6b550fa): live verification against codex-cli
// 0.147.0 showed Codex fails OPEN on a schema mismatch — a single field it
// does not recognise inside hookSpecificOutput causes it to silently drop
// the entire deny (tool runs, no error, no warning). wipnote's struct is
// schema-clean today, but that safety is only as durable as "nobody ever
// adds a field" — e.g. a well-intentioned debug marker or wipnote-specific
// annotation slipped in here would silently disable PreToolUse gating for
// every Codex session, with zero signal.
//
// This test reflects over the live struct type (not a hand-copied literal
// list) so it fails the moment a field is added, renamed, or removed —
// before that change ever reaches a real Codex session. If this test fails
// because you intentionally need a new field, that intent must be verified
// against Codex's actual accepted schema first (see the struct doc comment)
// — do not "fix" this test by just adding the new field to the allowlist.
func TestCodexHookSpecificOutput_SchemaFieldsPinned(t *testing.T) {
	wantFields := map[string]bool{
		"hookEventName":            true,
		"additionalContext":        true,
		"permissionDecision":       true,
		"permissionDecisionReason": true,
	}

	typ := reflect.TypeOf(codexHookSpecificOutput{})
	if typ.NumField() != len(wantFields) {
		t.Fatalf("codexHookSpecificOutput has %d fields, want exactly %d (Codex's declared schema) — "+
			"a field was added or removed; see the struct's doc comment before changing this test",
			typ.NumField(), len(wantFields))
	}
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if !wantFields[name] {
			t.Fatalf("codexHookSpecificOutput has unexpected JSON field %q (go field %s) — "+
				"Codex silently drops the whole deny when hookSpecificOutput contains a field "+
				"it doesn't recognise (bug-c6b550fa); confirm this field exists in Codex's real "+
				"schema before adding it", name, typ.Field(i).Name)
		}
	}
}

// --- emitGeminiResponse tests ---

func TestEmitGeminiSessionStartResponse(t *testing.T) {
	var buf bytes.Buffer
	result := &HookResult{
		AdditionalContext: "hello from gemini handler",
	}
	if err := emitGeminiResponseForEvent(&buf, "SessionStart", result); err != nil {
		t.Fatalf("emitGeminiResponse: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal gemini response: %v", err)
	}

	if _, ok := got["systemPrompt"]; ok {
		t.Errorf("systemPrompt should not be emitted for Gemini context injection, got %v", got["systemPrompt"])
	}
	hso, ok := got["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("expected hookSpecificOutput, got %#v", got["hookSpecificOutput"])
	}
	if hso["additionalContext"] != "hello from gemini handler" {
		t.Errorf("additionalContext = %v, want 'hello from gemini handler'", hso["additionalContext"])
	}
	if got["continue"] != true {
		t.Errorf("continue = %v, want true", got["continue"])
	}
	// "additionalContext" must NOT appear in Gemini output.
	if _, ok := got["additionalContext"]; ok {
		t.Error("additionalContext should not appear in Gemini response (it's Claude-only)")
	}
}

func TestEmitGeminiBlockResponse(t *testing.T) {
	var buf bytes.Buffer
	result := &HookResult{
		Decision: "block",
		Reason:   "dangerous tool",
	}
	if err := emitGeminiResponse(&buf, result); err != nil {
		t.Fatalf("emitGeminiResponse: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal gemini response: %v", err)
	}

	if got["continue"] != false {
		t.Errorf("continue = %v, want false for block", got["continue"])
	}
	if got["decision"] != "block" {
		t.Errorf("decision = %v, want block", got["decision"])
	}
}

// --- emitClaudeResponse regression test ---

func TestEmitClaudeResponseRegressionAdditionalContext(t *testing.T) {
	var buf bytes.Buffer
	result := &HookResult{
		AdditionalContext: "regression check: must stay in additionalContext",
	}
	if err := emitClaudeResponse(&buf, result); err != nil {
		t.Fatalf("emitClaudeResponse: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal claude response: %v", err)
	}

	if got["additionalContext"] != "regression check: must stay in additionalContext" {
		t.Errorf("additionalContext = %v, want the injected text", got["additionalContext"])
	}
	// Claude uses "additionalContext", not "systemMessage".
	if _, ok := got["systemMessage"]; ok {
		t.Error("systemMessage should not appear in Claude response")
	}
}

func TestEmitClaudeBlockResponse(t *testing.T) {
	var buf bytes.Buffer
	result := &HookResult{
		Decision: "block",
		Reason:   "blocked by guard",
	}
	if err := emitClaudeResponse(&buf, result); err != nil {
		t.Fatalf("emitClaudeResponse: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal claude response: %v", err)
	}

	if got["decision"] != "block" {
		t.Errorf("decision = %v, want block", got["decision"])
	}
	if got["reason"] != "blocked by guard" {
		t.Errorf("reason = %v, want 'blocked by guard'", got["reason"])
	}
}

// --- ParseEventForHarness integration tests ---

func TestParseEventForHarnessClaude(t *testing.T) {
	ev, err := ParseEventForHarness(HarnessClaude, []byte(claudeSessionStartJSON))
	if err != nil {
		t.Fatalf("ParseEventForHarness(claude): %v", err)
	}
	if ev.SessionID != "sess-abc123" {
		t.Errorf("SessionID = %q, want sess-abc123", ev.SessionID)
	}
	// bug-190950e0: every hook handler downstream of parsing (notably the
	// AgentIdentityAdapter seam) trusts event.Harness rather than
	// redetecting or re-inferring it — this must actually be stamped.
	if ev.Harness != HarnessClaude {
		t.Errorf("Harness = %v, want HarnessClaude", ev.Harness)
	}
}

func TestParseClaudeWorktreeCreateEvent_DataEnvelopeAndPathFallback(t *testing.T) {
	payload := []byte(`{"data":{"session_id":"sess-1","cwd":"/repo","worktree_base_path":"/tmp/wt","worktree_path":"/tmp/wt/feat-123"}}`)
	ev, err := ParseClaudeWorktreeCreateEvent(payload)
	if err != nil {
		t.Fatalf("ParseClaudeWorktreeCreateEvent: %v", err)
	}
	if ev.SessionID != "sess-1" {
		t.Fatalf("SessionID = %q", ev.SessionID)
	}
	if ev.CWD != "/repo" {
		t.Fatalf("CWD = %q", ev.CWD)
	}
	if ev.WorktreeBasePath != "/tmp/wt" {
		t.Fatalf("WorktreeBasePath = %q", ev.WorktreeBasePath)
	}
	if ev.WorktreePath != "/tmp/wt/feat-123" {
		t.Fatalf("WorktreePath = %q", ev.WorktreePath)
	}
}

func TestParseEventForHarnessCodex(t *testing.T) {
	ev, err := ParseEventForHarness(HarnessCodex, []byte(codexSessionStartJSON))
	if err != nil {
		t.Fatalf("ParseEventForHarness(codex): %v", err)
	}
	if ev.SessionID != "019da445-8036-73c2-a8fc-dacdb57417a8" {
		t.Errorf("SessionID = %q", ev.SessionID)
	}
	if ev.Harness != HarnessCodex {
		t.Errorf("Harness = %v, want HarnessCodex", ev.Harness)
	}
}

// TestParseCodexEvent_RealSubagentAgentIDNotOverwritten is feat-b7bc4267's
// direct test of the fix: Codex declares a real per-subagent agent_id/
// agent_type on PreToolUse (verified against Codex's own generated JSON
// Schema, codex-rs/hooks/schema/generated/pre-tool-use.command.input.schema.json).
// Before this fix, parseCodexEvent unconditionally overwrote AgentID with
// the generic per-harness constant, silently discarding this value for
// every event, subagent or not.
func TestParseCodexEvent_RealSubagentAgentIDNotOverwritten(t *testing.T) {
	payload := `{
		"session_id": "019da445-8036-73c2-a8fc-dacdb57417a8",
		"turn_id": "019da445-a255-77e1-98c4-9d456711f47b",
		"transcript_path": "/tmp/rollout.jsonl",
		"cwd": "/repo",
		"hook_event_name": "PreToolUse",
		"model": "gpt-5.4",
		"permission_mode": "default",
		"agent_id": "codex-subagent-1",
		"agent_type": "worker",
		"tool_name": "Bash",
		"tool_use_id": "call_abc123",
		"tool_input": {"command": "wipnote feature start feat-1234"}
	}`
	ev, err := ParseEventForHarness(HarnessCodex, []byte(payload))
	if err != nil {
		t.Fatalf("ParseEventForHarness(codex): %v", err)
	}
	if ev.AgentID != "codex-subagent-1" {
		t.Fatalf("AgentID = %q, want %q (must not be overwritten with the generic harness constant)", ev.AgentID, "codex-subagent-1")
	}
	if ev.AgentType != "worker" {
		t.Fatalf("AgentType = %q, want %q", ev.AgentType, "worker")
	}
}

// TestParseCodexEvent_RootSessionKeepsGenericAgentID asserts the
// orchestrator/root case (no agent_id in the payload — Codex's Rust source
// models this as subagent: Option<SubagentHookContext> = None) still falls
// back to the generic per-harness constant, preserving isSubagentEvent's and
// resolveSessionIDWithHarness's invariant that a root-level Codex event's
// AgentID equals that constant, not an empty string. (assistantTextHarness/
// assistantTextNativeName no longer depend on this — bug-08ef82ea switched
// them to event.Harness, which is unaffected by AgentID's value either way.)
func TestParseCodexEvent_RootSessionKeepsGenericAgentID(t *testing.T) {
	ev, err := ParseEventForHarness(HarnessCodex, []byte(codexSessionStartJSON))
	if err != nil {
		t.Fatalf("ParseEventForHarness(codex): %v", err)
	}
	if ev.AgentID != codexGenericAgentID {
		t.Fatalf("AgentID = %q, want the generic harness constant %q", ev.AgentID, codexGenericAgentID)
	}
	if ev.AgentID == "" {
		t.Fatalf("root-session AgentID must not be empty — several callers (isSubagentEvent, resolveSessionIDWithHarness) depend on it being the generic constant")
	}
}

func TestParseEventForHarnessGemini(t *testing.T) {
	ev, err := ParseEventForHarness(HarnessGemini, []byte(geminiSessionStartJSON))
	if err != nil {
		t.Fatalf("ParseEventForHarness(gemini): %v", err)
	}
	if ev.SessionID != "gemini-sess-xyz789" {
		t.Errorf("SessionID = %q", ev.SessionID)
	}
	if ev.Harness != HarnessGemini {
		t.Errorf("Harness = %v, want HarnessGemini", ev.Harness)
	}
}

// --- WriteResultForHarness tests ---

// TestWriteResultForHarnessCodexWritesSystemMessage verifies that the exported
// WriteResultForHarness function routes Codex payloads correctly. Since it
// writes to os.Stdout we test the underlying emitter directly.
func TestWriteResultForHarnessCodexEmitter(t *testing.T) {
	// Verify Codex emitter produces hookSpecificOutput.additionalContext (not top-level additionalContext).
	var buf bytes.Buffer
	result := &HookResult{AdditionalContext: "test context"}
	if err := emitCodexResponseForEvent(&buf, "SessionStart", result); err != nil {
		t.Fatalf("emitCodexResponse: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	hso, ok := got["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("expected hookSpecificOutput key in Codex response, got %#v", got)
	}
	if hso["additionalContext"] != "test context" {
		t.Errorf("additionalContext = %v, want test context", hso["additionalContext"])
	}
	if _, ok := got["additionalContext"]; ok {
		t.Error("additionalContext must not appear at top level")
	}
}

// TestHarnessStringMethod verifies human-readable harness names.
func TestHarnessStringMethod(t *testing.T) {
	tests := []struct {
		harness Harness
		want    string
	}{
		{HarnessClaude, "claude"},
		{HarnessCodex, "codex"},
		{HarnessGemini, "gemini"},
	}
	for _, tt := range tests {
		if got := tt.harness.String(); got != tt.want {
			t.Errorf("Harness(%d).String() = %q, want %q", tt.harness, got, tt.want)
		}
	}
}

// --- AllowForHarness tests ---

// TestAllowForHarnessEmitsClaudeEmpty verifies that AllowForHarness(HarnessClaude)
// returns an empty HookResult that emits as {} when written via emitClaudeResponse.
func TestAllowForHarnessEmitsClaudeEmpty(t *testing.T) {
	result := AllowForHarness(HarnessClaude)

	// Result should be an empty HookResult.
	if result.Continue != false || result.Decision != "" {
		t.Errorf("AllowForHarness(HarnessClaude) = %+v, want empty HookResult", result)
	}

	// When emitted via Claude's emitter, it should produce {}.
	var buf bytes.Buffer
	if err := emitClaudeResponse(&buf, result); err != nil {
		t.Fatalf("emitClaudeResponse: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	// Empty object: should have no keys or only omitted optional fields.
	if len(got) > 0 {
		t.Errorf("Claude allow response = %+v, want empty object", got)
	}
}

// TestAllowForHarnessEmitsCodexContinue verifies that AllowForHarness(HarnessCodex)
// returns a HookResult{Continue: true} that emits as {"continue": true}.
func TestAllowForHarnessEmitsCodexContinue(t *testing.T) {
	result := AllowForHarness(HarnessCodex)

	// Result should have Continue: true.
	if !result.Continue {
		t.Errorf("AllowForHarness(HarnessCodex).Continue = %v, want true", result.Continue)
	}

	// When emitted via Codex's emitter, it should produce {"continue": true}.
	var buf bytes.Buffer
	if err := emitCodexResponse(&buf, result); err != nil {
		t.Fatalf("emitCodexResponse: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	if got["continue"] != true {
		t.Errorf("Codex allow response continue = %v, want true", got["continue"])
	}
}

// TestAllowForHarnessEmitsGeminiContinue verifies that AllowForHarness(HarnessGemini)
// returns a HookResult{Continue: true} that emits as {"continue": true}.
func TestAllowForHarnessEmitsGeminiContinue(t *testing.T) {
	result := AllowForHarness(HarnessGemini)

	// Result should have Continue: true.
	if !result.Continue {
		t.Errorf("AllowForHarness(HarnessGemini).Continue = %v, want true", result.Continue)
	}

	// When emitted via Gemini's emitter, it should produce {"continue": true}.
	var buf bytes.Buffer
	if err := emitGeminiResponse(&buf, result); err != nil {
		t.Fatalf("emitGeminiResponse: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	if got["continue"] != true {
		t.Errorf("Gemini allow response continue = %v, want true", got["continue"])
	}
}
