package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/shakestzd/wipnote/core/harness"
)

// Harness identifies the AI coding harness that invoked this hook.
type Harness int

const (
	// HarnessClaude is the default harness (Claude Code via CloudEvent JSON).
	HarnessClaude Harness = iota
	// HarnessCodex is the OpenAI Codex CLI harness. Payload has a top-level
	// "hook_event_name" field, which distinguishes it from Claude's CloudEvent.
	HarnessCodex
	// HarnessGemini is the Google Gemini CLI harness. Payload has a top-level
	// "invocation_id" field and no "hook_event_name" field.
	HarnessGemini
	// HarnessAntigravity is the Google Antigravity CLI harness.
	HarnessAntigravity
)

// String returns a human-readable name for the harness.
func (h Harness) String() string {
	switch h {
	case HarnessCodex:
		return "codex"
	case HarnessGemini:
		return "gemini"
	case HarnessAntigravity:
		return "antigravity"
	default:
		return "claude"
	}
}

// codexPayload is used only for harness detection and input parsing.
// It matches the flat top-level shape of a Codex CLI hook payload.
type codexPayload struct {
	SessionID            string         `json:"session_id"`
	TurnID               string         `json:"turn_id"`
	TranscriptPath       string         `json:"transcript_path"`
	CWD                  string         `json:"cwd"`
	HookEventName        string         `json:"hook_event_name"`
	Model                string         `json:"model"`
	PermissionMode       string         `json:"permission_mode"`
	Timestamp            string         `json:"timestamp"`
	Source               string         `json:"source"`
	Prompt               string         `json:"prompt"`
	LastAssistantMessage string         `json:"last_assistant_message"`
	StopReason           string         `json:"stop_reason"`
	ToolName             string         `json:"tool_name"`
	ToolInput            map[string]any `json:"tool_input"`
	ToolUseID            string         `json:"tool_use_id"`
	ToolResult           map[string]any `json:"tool_result"`
	TaskID               string         `json:"task_id"`
	TaskData             map[string]any `json:"task"`
	TaskSubject          string         `json:"task_subject"`
}

// geminiPayload is used only for harness detection and input parsing.
// It matches the base input schema of a Gemini CLI hook payload.
// Gemini's unique top-level field is "invocation_id"; it also has
// a nested "tool" object (for BeforeTool/AfterTool events) instead
// of top-level tool_name.
type geminiPayload struct {
	InvocationID   string `json:"invocation_id"`
	SessionID      string `json:"session_id"`
	CWD            string `json:"cwd"`
	Model          string `json:"model"`
	TranscriptPath string `json:"transcript_path"`
	HookEventName  string `json:"hook_event_name"`
	Timestamp      string `json:"timestamp"`
	// BeforeAgent / AfterAgent prompt text field.
	Prompt         string `json:"prompt"`
	PromptResponse string `json:"prompt_response"`
	// BeforeTool / AfterTool nested tool object.
	Tool struct {
		Name  string         `json:"name"`
		Input map[string]any `json:"input"`
	} `json:"tool"`
	// AfterModel LLM request/response fields (available when hook_event_name == "AfterModel").
	LLMRequest  map[string]any `json:"llm_request,omitempty"`
	LLMResponse map[string]any `json:"llm_response,omitempty"`
}

// harnessFromConfig bridges a *harness.HarnessConfig to the hooks.Harness int
// using the HooksHarness field populated in slice 1. Returns HarnessClaude when
// cfg is nil so callers always get a safe default.
func harnessFromConfig(cfg *harness.HarnessConfig) Harness {
	if cfg == nil {
		return HarnessClaude
	}
	return Harness(cfg.HooksHarness)
}

// DetectHarness is the exported entry point for harness detection. It calls
// detectHarness with the provided payload bytes. This is the function called
// from cmd/wipnote/hook.go.
func DetectHarness(payload []byte) Harness {
	return detectHarness(payload)
}

// detectHarness examines the raw payload bytes and the process environment to
// determine the harness that sent them. The detection rules are (in priority order):
//
//  1. HarnessClaude: CLAUDE_CODE_ENTRYPOINT env var is set (Claude Code sets this
//     in every hook invocation; Codex and Gemini do not). This takes priority over
//     payload-based detection because Claude Code also sends "hook_event_name" in
//     its payloads, which previously caused false Codex classification.
//  2. HarnessGemini: WIPNOTE_AGENT_ID=gemini (set by `wipnote gemini` launcher).
//     This must be checked before payload fields because real Gemini hook payloads
//     do NOT include "invocation_id" — the Gemini hook schema uses hook_event_name
//     like Codex, making payload-only detection ambiguous.
//  3. HarnessCodex:  WIPNOTE_AGENT_ID=codex (set by `wipnote codex` launcher).
//  4. HarnessGemini: WIPNOTE_AGENT_ID absent AND top-level "invocation_id" present.
//  5. HarnessGemini: hook_event_name is a Gemini-native value (BeforeAgent, AfterAgent,
//     AfterModel, BeforeTool, AfterTool) — reliable payload-only discriminator when the
//     wipnote launcher is not used (i.e. `gemini` run directly).
//  6. HarnessCodex:  any other "hook_event_name" present.
//  7. HarnessClaude: default fallback.
func detectHarness(payload []byte) Harness {
	return detectHarnessWithEnv(payload, os.Getenv)
}

// detectHarnessWithEnv is the testable core of detectHarness. getenv is
// injected so tests can control environment without os.Setenv races.
func detectHarnessWithEnv(payload []byte, getenv func(string) string) Harness {
	// CLAUDE_CODE_ENTRYPOINT is set by Claude Code in every hook invocation.
	// Its presence is the most reliable signal that hooks are running inside
	// Claude Code — even when the payload also contains "hook_event_name"
	// (which Claude Code sends for all events, contra the previous assumption
	// that "hook_event_name" was Codex-exclusive).
	if getenv("CLAUDE_CODE_ENTRYPOINT") != "" {
		return HarnessClaude
	}

	// WIPNOTE_AGENT_ID is set by the wipnote launcher before spawning the
	// harness process (buildGeminiAgentEnv sets WIPNOTE_AGENT_ID=gemini;
	// the Codex launcher sets WIPNOTE_AGENT_ID=codex). Use it as a reliable
	// env-based discriminator when payload-shape alone is ambiguous.
	//
	// This is necessary because real Gemini CLI hook payloads do NOT include
	// "invocation_id" — the official Gemini hook schema only has session_id,
	// cwd, hook_event_name, timestamp, and event-specific fields. Without this
	// check, every Gemini hook event is misclassified as HarnessCodex (because
	// hook_event_name IS present), causing agent_id='codex' to be written to
	// agent_events for Gemini sessions (the bug reported for session 8de1df19).
	if id := getenv("WIPNOTE_AGENT_ID"); id != "" {
		if cfg := harness.GetByAgentID(id); cfg != nil {
			return Harness(cfg.HooksHarness)
		}
	}

	if len(payload) == 0 {
		return HarnessClaude
	}

	// Unmarshal into a generic map for field-presence checks.
	var top map[string]any
	if err := json.Unmarshal(payload, &top); err != nil {
		return HarnessClaude
	}

	// Gemini: presence of "invocation_id" (future-proofing; real Gemini payloads omit it).
	if _, ok := top["invocation_id"]; ok {
		return HarnessGemini
	}

	// Gemini: hook_event_name values that Codex never emits. Gemini CLI uses its own
	// event naming convention (BeforeAgent, AfterAgent, AfterModel, BeforeTool, AfterTool)
	// rather than Codex's UserPromptSubmit / PreToolUse / PostToolUse names. This is the
	// only reliable payload-only discriminator when WIPNOTE_AGENT_ID is not set (i.e. when
	// `gemini` is run directly rather than via `wipnote gemini`).
	// Event names are read from the registry (HookEventNames field) rather than
	// hardcoded here — adding new Gemini events only requires updating registry_gemini.go.
	if name, _ := top["hook_event_name"].(string); name != "" {
		for _, cfg := range harness.All() {
			for _, n := range cfg.HookEventNames {
				if n == name {
					return harnessFromConfig(cfg)
				}
			}
		}
	}

	// Codex: presence of "hook_event_name" when not inside Claude Code.
	if _, ok := top["hook_event_name"]; ok {
		return HarnessCodex
	}

	return HarnessClaude
}

// parseCodexEvent converts a Codex CLI hook payload into our internal CloudEvent
// representation. Codex uses a flat JSON structure with top-level fields like
// "hook_event_name", "cwd", and "session_id". We map those into the CloudEvent
// fields that downstream handlers read.
//
// Hardening: if WIPNOTE_PARENT_AGENT is set to a value other than "codex"
// (e.g. "claude-code"), we use that as AgentID rather than hard-coding "codex".
// This prevents misclassification when a stale env or mis-routed payload reaches
// this parser for a non-Codex harness.
func parseCodexEvent(raw []byte) (*CloudEvent, error) {
	var p codexPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parseCodexEvent: %w", err)
	}

	agentID := harness.GetByHooksHarness(harness.HooksCodex).AgentID
	if parent := strings.TrimSpace(os.Getenv("WIPNOTE_PARENT_AGENT")); parent != "" && parent != agentID {
		agentID = parent
	}

	ev := &CloudEvent{
		AgentID:              agentID,
		SessionID:            p.SessionID,
		CWD:                  p.CWD,
		HookEventName:        p.HookEventName,
		PermissionMode:       p.PermissionMode,
		Timestamp:            p.Timestamp,
		Model:                p.Model,
		TranscriptPath:       p.TranscriptPath,
		Source:               p.Source,
		Prompt:               p.Prompt,
		TurnID:               p.TurnID,
		LastAssistantMessage: p.LastAssistantMessage,
		StopReason:           p.StopReason,
		ToolName:             p.ToolName,
		ToolInput:            p.ToolInput,
		ToolUseID:            p.ToolUseID,
		ToolResult:           p.ToolResult,
		TaskID:               p.TaskID,
		TaskData:             p.TaskData,
		TaskSubject:          p.TaskSubject,
	}
	return ev, nil
}

// parseGeminiEvent converts a Gemini CLI hook payload into our internal
// CloudEvent representation. Gemini uses a base input schema with an
// "invocation_id" field. Tool information is nested under a "tool" object for
// BeforeTool/AfterTool events. This parser is best-effort until a real captured
// payload is available for full verification.
func parseGeminiEvent(raw []byte) (*CloudEvent, error) {
	var p geminiPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parseGeminiEvent: %w", err)
	}

	ev := &CloudEvent{
		AgentID: harness.GetByHooksHarness(harness.HooksGemini).AgentID,
		// Gemini may use "invocation_id" as the session identifier;
		// fall back to session_id if present.
		SessionID:      p.SessionID,
		CWD:            p.CWD,
		HookEventName:  p.HookEventName,
		Model:          p.Model,
		TranscriptPath: p.TranscriptPath,
		Timestamp:      p.Timestamp,
		Prompt:         p.Prompt,
		PromptResponse: p.PromptResponse,
		// BeforeTool / AfterTool: tool name is nested under "tool".
		ToolName:  p.Tool.Name,
		ToolInput: p.Tool.Input,
		// AfterModel: LLM request/response payloads.
		LLMRequest:  p.LLMRequest,
		LLMResponse: p.LLMResponse,
	}
	// If session_id is empty, use invocation_id as a surrogate so that
	// session-scoped DB lookups have something to work with.
	if ev.SessionID == "" && p.InvocationID != "" {
		ev.SessionID = p.InvocationID
	}
	return ev, nil
}

// parseAntigravityEvent converts an Antigravity CLI hook payload into our internal
// CloudEvent representation. Antigravity uses the same base input schema as Gemini.
func parseAntigravityEvent(raw []byte) (*CloudEvent, error) {
	var p geminiPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parseAntigravityEvent: %w", err)
	}

	ev := &CloudEvent{
		AgentID:        harness.GetByHooksHarness(harness.HooksAntigravity).AgentID,
		SessionID:      p.SessionID,
		CWD:            p.CWD,
		HookEventName:  p.HookEventName,
		Model:          p.Model,
		TranscriptPath: p.TranscriptPath,
		Timestamp:      p.Timestamp,
		Prompt:         p.Prompt,
		PromptResponse: p.PromptResponse,
		// BeforeTool / AfterTool: tool name is nested under "tool".
		ToolName:  p.Tool.Name,
		ToolInput: p.Tool.Input,
		// AfterModel: LLM request/response payloads.
		LLMRequest:  p.LLMRequest,
		LLMResponse: p.LLMResponse,
	}
	if ev.SessionID == "" && p.InvocationID != "" {
		ev.SessionID = p.InvocationID
	}
	return ev, nil
}

// HookResponse is the normalised internal response that all handlers return.
// It is an alias for HookResult so the rest of the codebase is unchanged; the
// harness-specific emitters read from it.
//
// Fields:
//   - Continue:         non-blocking hooks should set this to true.
//   - Decision:         "allow" | "block" | "deny" (blocking hooks only).
//   - Reason:           human-readable reason (used when Decision != "").
//   - AdditionalContext: Claude's inject-into-conversation field.
//
// Emitters map these fields to the harness-specific wire format.
type HookResponse = HookResult

// emitClaudeResponse writes the Claude Code wire-format JSON to w.
// Claude expects "additionalContext" (for injecting text) and "decision" for
// blocking. An empty object "{}" means "no opinion / allow".
func emitClaudeResponse(w io.Writer, result *HookResult) error {
	return json.NewEncoder(w).Encode(result)
}

// emitCodexResponse writes the Codex CLI wire-format JSON to w.
// Codex expects:
//   - "continue": true/false for lifecycle events
//   - "systemMessage": "..." for user-visible hook messages
//   - "hookSpecificOutput.additionalContext" for model-visible context injection
//   - "hookSpecificOutput.permissionDecision" for PreToolUse allow/deny decisions
func emitCodexResponse(w io.Writer, result *HookResult) error {
	return emitCodexResponseForEvent(w, "", result)
}

func emitCodexResponseForEvent(w io.Writer, hookEventName string, result *HookResult) error {
	type codexHookSpecificOutput struct {
		HookEventName            string `json:"hookEventName,omitempty"`
		AdditionalContext        string `json:"additionalContext,omitempty"`
		PermissionDecision       string `json:"permissionDecision,omitempty"`
		PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
	}
	type codexResponse struct {
		Continue           *bool                    `json:"continue,omitempty"`
		SystemMessage      string                   `json:"systemMessage,omitempty"`
		Decision           string                   `json:"decision,omitempty"`
		Reason             string                   `json:"reason,omitempty"`
		HookSpecificOutput *codexHookSpecificOutput `json:"hookSpecificOutput,omitempty"`
	}

	continueTrue := true
	resp := codexResponse{}
	if result.Message != "" {
		resp.SystemMessage = result.Message
	}

	hookOutput := codexHookSpecificOutput{HookEventName: hookEventName}
	if result.HookSpecificOutput != nil {
		if result.HookSpecificOutput.HookEventName != "" {
			hookOutput.HookEventName = result.HookSpecificOutput.HookEventName
		}
		hookOutput.AdditionalContext = result.HookSpecificOutput.AdditionalContext
		hookOutput.PermissionDecision = result.HookSpecificOutput.PermissionDecision
		hookOutput.PermissionDecisionReason = result.HookSpecificOutput.PermissionDecisionReason
	}
	if result.AdditionalContext != "" {
		hookOutput.AdditionalContext = result.AdditionalContext
	}
	if hookOutput.HookEventName == "" && hookOutput.AdditionalContext != "" {
		hookOutput.HookEventName = "SessionStart"
	}

	if result.Decision == "block" || result.Decision == "deny" {
		if hookEventName == "PreToolUse" {
			hookOutput.PermissionDecision = "deny"
			hookOutput.PermissionDecisionReason = result.Reason
		} else {
			resp.Decision = result.Decision
			resp.Reason = result.Reason
		}
	} else if hookEventName != "PreToolUse" {
		resp.Continue = &continueTrue
	}

	if hookOutput.AdditionalContext != "" || hookOutput.PermissionDecision != "" || hookOutput.PermissionDecisionReason != "" {
		resp.HookSpecificOutput = &hookOutput
	}

	return json.NewEncoder(w).Encode(resp)
}

// emitGeminiResponse writes the Gemini CLI wire-format JSON to w.
// Gemini uses hookSpecificOutput.additionalContext for model-visible context.
// systemMessage is display/status text, not the prompt append channel.
func emitGeminiResponse(w io.Writer, result *HookResult) error {
	return emitGeminiResponseForEvent(w, "", result)
}

func emitGeminiResponseForEvent(w io.Writer, hookEventName string, result *HookResult) error {
	type geminiHookSpecificOutput struct {
		HookEventName     string `json:"hookEventName,omitempty"`
		AdditionalContext string `json:"additionalContext,omitempty"`
	}
	type geminiResponse struct {
		Continue           bool                      `json:"continue"`
		SystemMessage      string                    `json:"systemMessage,omitempty"`
		Decision           string                    `json:"decision,omitempty"`
		Reason             string                    `json:"reason,omitempty"`
		HookSpecificOutput *geminiHookSpecificOutput `json:"hookSpecificOutput,omitempty"`
	}

	resp := geminiResponse{
		Continue: result.Decision != "block" && result.Decision != "deny",
	}
	if result.Message != "" {
		resp.SystemMessage = result.Message
	}
	if result.AdditionalContext != "" {
		resp.HookSpecificOutput = &geminiHookSpecificOutput{
			HookEventName:     hookEventName,
			AdditionalContext: result.AdditionalContext,
		}
	}
	if result.Decision == "block" || result.Decision == "deny" {
		resp.Decision = result.Decision
		resp.Reason = result.Reason
	}

	return json.NewEncoder(w).Encode(resp)
}

// AllowForHarness returns a harness-appropriate "allow" response that can be
// written to stdout via WriteResultForHarness. For Claude, this is an empty
// HookResult{}. For Codex/Gemini, this is a HookResult{Continue: true} which
// will be emitted as their respective wire formats ({"continue": true}).
func AllowForHarness(harness Harness) *HookResult {
	switch harness {
	case HarnessCodex, HarnessGemini, HarnessAntigravity:
		// Codex/Gemini/Antigravity expect {"continue": true} on allow
		return &HookResult{Continue: true}
	default:
		// Claude expects {} (empty object) on allow
		return &HookResult{}
	}
}

// WriteResultForHarness encodes result as JSON to stdout using the wire format
// appropriate for the detected harness. This replaces the harness-agnostic
// WriteResult call in runHookNamed.
func WriteResultForHarness(harness Harness, result *HookResult) error {
	return WriteResultForHarnessEvent(harness, "", result)
}

// WriteResultForHarnessEvent encodes result as JSON with the hook event name
// needed by Codex/Gemini hookSpecificOutput responses.
func WriteResultForHarnessEvent(harness Harness, hookEventName string, result *HookResult) error {
	switch harness {
	case HarnessCodex:
		return emitCodexResponseForEvent(os.Stdout, hookEventName, result)
	case HarnessAntigravity:
		return emitAntigravityResponseForEvent(os.Stdout, hookEventName, result)
	case HarnessGemini:
		return emitGeminiResponseForEvent(os.Stdout, hookEventName, result)
	default:
		return emitClaudeResponse(os.Stdout, result)
	}
}

// emitAntigravityResponseForEvent writes the Antigravity CLI (agy) hook-result
// wire format. agy decodes a hook's stdout as a protojson hook-result message
// and STRICT-rejects unknown fields — the Gemini-shaped {"continue":true,...}
// response fails with `unknown field "continue"` (verified live, agy v1.0.8),
// so every hook's output was being discarded. A no-op / allow must therefore be
// an empty object "{}".
//
// On the PreInvocation event, wipnote injects model-visible context via the
// result's injectSteps[].systemMessage channel: the orchestrator system prompt
// (from the file named by WIPNOTE_ANTIGRAVITY_SYSTEM_MD, written by the
// launcher) plus any per-prompt additionalContext the handler produced. agy
// merges duplicate system messages across turns, so re-injecting every turn is
// cheap. This is the only working channel to convey the wipnote orchestrator
// directives to agy (GEMINI_SYSTEM_MD / additionalContext / plugin context are
// all ignored by agy).
//
// Block/deny enforcement (PreToolUse) is not yet wired for agy: a blocking
// result still emits "{}" (allow). agy's pre-tool deny shape needs live
// confirmation; until then this is no worse than the prior all-rejected state.
func emitAntigravityResponseForEvent(w io.Writer, hookEventName string, result *HookResult) error {
	if hookEventName == "PreInvocation" {
		var sb strings.Builder
		if p := os.Getenv("WIPNOTE_ANTIGRAVITY_SYSTEM_MD"); p != "" {
			if data, err := os.ReadFile(p); err == nil {
				sb.WriteString(strings.TrimSpace(string(data)))
			}
		}
		if result != nil && result.AdditionalContext != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(strings.TrimSpace(result.AdditionalContext))
		}
		if sb.Len() > 0 {
			type injectedStep struct {
				SystemMessage string `json:"systemMessage"`
			}
			type preInvocationResult struct {
				InjectSteps []injectedStep `json:"injectSteps"`
			}
			return json.NewEncoder(w).Encode(preInvocationResult{
				InjectSteps: []injectedStep{{SystemMessage: sb.String()}},
			})
		}
	}
	// Allow / no-op. agy's strict protojson decoder accepts an empty object.
	_, err := io.WriteString(w, "{}\n")
	return err
}

// ParseEventForHarness reads the raw payload bytes and returns a CloudEvent
// parsed according to the given harness's input schema. For Claude, the
// existing JSON unmarshal path is used (CloudEvent struct tags handle it
// directly). For Codex and Gemini, dialect-specific parsers normalise the
// flat/nested payloads into CloudEvent.
func ParseEventForHarness(harness Harness, raw []byte) (*CloudEvent, error) {
	switch harness {
	case HarnessCodex:
		return parseCodexEvent(raw)
	case HarnessGemini:
		return parseGeminiEvent(raw)
	case HarnessAntigravity:
		return parseAntigravityEvent(raw)
	default:
		// Claude: standard CloudEvent unmarshal (existing behaviour).
		if len(raw) == 0 {
			return &CloudEvent{}, nil
		}
		var ev CloudEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, fmt.Errorf("parsing CloudEvent: %w", err)
		}
		return &ev, nil
	}
}

// ParseClaudeWorktreeCreateEvent parses Claude Code's WorktreeCreate payload.
// WorktreeCreate is a Claude-only replacement hook, and observed Claude
// payloads may wrap event-specific fields inside a top-level "data" object.
// Keep this parser separate from generic harness detection so manual repros
// with hook_event_name are not misclassified as Codex payloads.
func ParseClaudeWorktreeCreateEvent(raw []byte) (*CloudEvent, error) {
	ev, err := ParseEventForHarness(HarnessClaude, raw)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return ev, nil
	}

	var envelope struct {
		Data *CloudEvent `json:"data"`
		// Claude Code (observed 2026-06-11) sends the worktree name as a bare
		// top-level "name" field with no worktree_name/worktree_base_path.
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("parsing WorktreeCreate event: %w", err)
	}
	if envelope.Data != nil {
		overlayMissingCloudEventFields(ev, envelope.Data)
	}
	if ev.WorktreeName == "" {
		ev.WorktreeName = envelope.Name
	}
	return ev, nil
}

func overlayMissingCloudEventFields(dst, src *CloudEvent) {
	if dst == nil || src == nil {
		return
	}
	if dst.SessionID == "" {
		dst.SessionID = src.SessionID
	}
	if dst.CWD == "" {
		dst.CWD = src.CWD
	}
	if dst.TranscriptPath == "" {
		dst.TranscriptPath = src.TranscriptPath
	}
	if dst.WorktreeName == "" {
		dst.WorktreeName = src.WorktreeName
	}
	if dst.WorktreeBasePath == "" {
		dst.WorktreeBasePath = src.WorktreeBasePath
	}
	if dst.WorktreePath == "" {
		dst.WorktreePath = src.WorktreePath
	}
}
