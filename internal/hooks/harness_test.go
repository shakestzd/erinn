package hooks

import (
	"bytes"
	"encoding/json"
	"testing"
)

// Codex session-start payload — matches the real captured payload shape from
// /tmp/htmlgraph-codex-hook-payloads/session-start-86946.json.
const codexSessionStartJSON = `{
	"session_id": "019da445-8036-73c2-a8fc-dacdb57417a8",
	"transcript_path": "/Users/shakes/.codex/sessions/2026/04/19/rollout-2026-04-19T01-45-11-019da445-8036-73c2-a8fc-dacdb57417a8.jsonl",
	"cwd": "/Users/shakes/DevProjects/htmlgraph",
	"hook_event_name": "SessionStart",
	"model": "gpt-5.4",
	"permission_mode": "default",
	"source": "startup"
}`

// Codex user-prompt payload — matches /tmp/htmlgraph-codex-hook-payloads/user-prompt-86954.json.
const codexUserPromptJSON = `{
	"session_id": "019da445-8036-73c2-a8fc-dacdb57417a8",
	"turn_id": "019da445-a255-77e1-98c4-9d456711f47b",
	"transcript_path": "/Users/shakes/.codex/sessions/2026/04/19/rollout-2026-04-19T01-45-11-019da445-8036-73c2-a8fc-dacdb57417a8.jsonl",
	"cwd": "/Users/shakes/DevProjects/htmlgraph",
	"hook_event_name": "UserPromptSubmit",
	"model": "gpt-5.4",
	"permission_mode": "default",
	"prompt": "Do these four small tasks so I can confirm HtmlGraph telemetry is firing."
}`

// Claude CloudEvent payload — typical SessionStart shape sent by Claude Code.
const claudeSessionStartJSON = `{
	"session_id": "sess-abc123",
	"cwd": "/Users/shakes/DevProjects/htmlgraph",
	"permission_mode": "default",
	"model": "claude-opus-4-5",
	"transcript_path": "/tmp/session.jsonl",
	"source": "startup"
}`

// Gemini payload — best-effort per https://geminicli.com/docs/hooks/reference/.
// The unique discriminator is the "invocation_id" field.
const geminiSessionStartJSON = `{
	"invocation_id": "gemini-inv-abc123",
	"session_id": "gemini-sess-xyz789",
	"cwd": "/Users/shakes/DevProjects/htmlgraph",
	"model": "gemini-2.5-pro"
}`

// --- detectHarness tests ---

func TestDetectHarnessFromCodexPayload(t *testing.T) {
	got := detectHarness([]byte(codexSessionStartJSON))
	if got != HarnessCodex {
		t.Errorf("detectHarness(codex session-start) = %v, want HarnessCodex", got)
	}
}

func TestDetectHarnessFromCodexUserPromptPayload(t *testing.T) {
	got := detectHarness([]byte(codexUserPromptJSON))
	if got != HarnessCodex {
		t.Errorf("detectHarness(codex user-prompt) = %v, want HarnessCodex", got)
	}
}

func TestDetectHarnessFromClaudePayload(t *testing.T) {
	got := detectHarness([]byte(claudeSessionStartJSON))
	if got != HarnessClaude {
		t.Errorf("detectHarness(claude session-start) = %v, want HarnessClaude", got)
	}
}

func TestDetectHarnessFromGeminiPayload(t *testing.T) {
	got := detectHarness([]byte(geminiSessionStartJSON))
	if got != HarnessGemini {
		t.Errorf("detectHarness(gemini session-start) = %v, want HarnessGemini", got)
	}
}

func TestDetectHarnessEmptyPayload(t *testing.T) {
	got := detectHarness([]byte{})
	if got != HarnessClaude {
		t.Errorf("detectHarness(empty) = %v, want HarnessClaude (default)", got)
	}
}

func TestDetectHarnessInvalidJSON(t *testing.T) {
	got := detectHarness([]byte("not-json"))
	if got != HarnessClaude {
		t.Errorf("detectHarness(invalid json) = %v, want HarnessClaude (fallback)", got)
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
	if ev.CWD != "/Users/shakes/DevProjects/htmlgraph" {
		t.Errorf("CWD = %q, want /Users/shakes/DevProjects/htmlgraph", ev.CWD)
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

func TestParseCodexEventSetsAgentID(t *testing.T) {
	ev, err := parseCodexEvent([]byte(codexSessionStartJSON))
	if err != nil {
		t.Fatalf("parseCodexEvent: %v", err)
	}

	if ev.AgentID != "codex" {
		t.Errorf("AgentID = %q, want codex", ev.AgentID)
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
	if ev.CWD != "/Users/shakes/DevProjects/htmlgraph" {
		t.Errorf("CWD = %q, want /Users/shakes/DevProjects/htmlgraph", ev.CWD)
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

	if got["continue"] != false {
		t.Errorf("continue = %v, want false for block decision", got["continue"])
	}
	if got["decision"] != "block" {
		t.Errorf("decision = %v, want block", got["decision"])
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
