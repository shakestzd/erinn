package hooks

import (
	"bytes"
	"encoding/json"
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

	if got["systemMessage"] != "foo" {
		t.Errorf("systemMessage = %v, want foo", got["systemMessage"])
	}
	if got["continue"] != true {
		t.Errorf("continue = %v, want true", got["continue"])
	}
	// "additionalContext" must NOT appear in Codex output.
	if _, ok := got["additionalContext"]; ok {
		t.Error("additionalContext should not appear in Codex response (it's Claude-only)")
	}
}

func TestEmitCodexBlockResponse(t *testing.T) {
	var buf bytes.Buffer
	result := &HookResult{
		Decision: "block",
		Reason:   "no active work item",
	}
	if err := emitCodexResponse(&buf, result); err != nil {
		t.Fatalf("emitCodexResponse: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal codex response: %v", err)
	}

	if _, ok := got["continue"]; ok {
		t.Errorf("continue = %v, want omitted for Codex block decision", got["continue"])
	}
	if got["decision"] != "block" {
		t.Errorf("decision = %v, want block", got["decision"])
	}
	if got["reason"] != "no active work item" {
		t.Errorf("reason = %v, want no active work item", got["reason"])
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

// --- emitGeminiResponse tests ---

func TestEmitGeminiSessionStartResponse(t *testing.T) {
	var buf bytes.Buffer
	result := &HookResult{
		AdditionalContext: "hello from gemini handler",
	}
	if err := emitGeminiResponse(&buf, result); err != nil {
		t.Fatalf("emitGeminiResponse: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal gemini response: %v", err)
	}

	if got["systemPrompt"] != "hello from gemini handler" {
		t.Errorf("systemPrompt = %v, want 'hello from gemini handler'", got["systemPrompt"])
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
}

func TestParseEventForHarnessGemini(t *testing.T) {
	ev, err := ParseEventForHarness(HarnessGemini, []byte(geminiSessionStartJSON))
	if err != nil {
		t.Fatalf("ParseEventForHarness(gemini): %v", err)
	}
	if ev.SessionID != "gemini-sess-xyz789" {
		t.Errorf("SessionID = %q", ev.SessionID)
	}
}

// --- WriteResultForHarness tests ---

// TestWriteResultForHarnessCodexWritesSystemMessage verifies that the exported
// WriteResultForHarness function routes Codex payloads correctly. Since it
// writes to os.Stdout we test the underlying emitter directly.
func TestWriteResultForHarnessCodexEmitter(t *testing.T) {
	// Verify Codex emitter produces systemMessage (not additionalContext).
	var buf bytes.Buffer
	result := &HookResult{AdditionalContext: "test context"}
	if err := emitCodexResponse(&buf, result); err != nil {
		t.Fatalf("emitCodexResponse: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if _, ok := got["systemMessage"]; !ok {
		t.Error("expected systemMessage key in Codex response")
	}
	if _, ok := got["additionalContext"]; ok {
		t.Error("additionalContext must not appear in Codex response")
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
