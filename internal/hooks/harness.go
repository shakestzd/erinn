package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
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
)

// String returns a human-readable name for the harness.
func (h Harness) String() string {
	switch h {
	case HarnessCodex:
		return "codex"
	case HarnessGemini:
		return "gemini"
	default:
		return "claude"
	}
}

// codexPayload is used only for harness detection and input parsing.
// It matches the flat top-level shape of a Codex CLI hook payload.
type codexPayload struct {
	SessionID      string `json:"session_id"`
	TurnID         string `json:"turn_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	Model          string `json:"model"`
	PermissionMode string `json:"permission_mode"`
	Source         string `json:"source"`
	Prompt         string `json:"prompt"`
	ToolName       string `json:"tool_name"`
}

// geminiPayload is used only for harness detection and input parsing.
// It matches the base input schema of a Gemini CLI hook payload.
// Gemini's unique top-level field is "invocation_id"; it also has
// a nested "tool" object (for BeforeTool/AfterTool events) instead
// of top-level tool_name.
type geminiPayload struct {
	InvocationID string `json:"invocation_id"`
	SessionID    string `json:"session_id"`
	CWD          string `json:"cwd"`
	Model        string `json:"model"`
	// BeforeAgent / AfterAgent prompt text field.
	Prompt string `json:"prompt"`
	// BeforeTool / AfterTool nested tool object.
	Tool struct {
		Name  string         `json:"name"`
		Input map[string]any `json:"input"`
	} `json:"tool"`
}

// DetectHarness is the exported entry point for harness detection. It calls
// detectHarness with the provided payload bytes. This is the function called
// from cmd/htmlgraph/hook.go.
func DetectHarness(payload []byte) Harness {
	return detectHarness(payload)
}

// detectHarness examines the raw payload bytes and returns the harness that
// sent them. The detection rules are:
//
//   - HarnessCodex:  top-level "hook_event_name" field is present (Codex's
//     unique field; Claude's CloudEvent does not have this field at the top level).
//   - HarnessGemini: top-level "invocation_id" field is present AND
//     "hook_event_name" is absent (Gemini's unique identifier).
//   - HarnessClaude: default fallback when no discriminating field is found.
func detectHarness(payload []byte) Harness {
	if len(payload) == 0 {
		return HarnessClaude
	}

	// Unmarshal into a generic map for field-presence checks.
	var top map[string]any
	if err := json.Unmarshal(payload, &top); err != nil {
		return HarnessClaude
	}

	// Codex: presence of "hook_event_name" is the definitive signal.
	if _, ok := top["hook_event_name"]; ok {
		return HarnessCodex
	}

	// Gemini: presence of "invocation_id" (absent from Claude/Codex payloads).
	if _, ok := top["invocation_id"]; ok {
		return HarnessGemini
	}

	return HarnessClaude
}

// parseCodexEvent converts a Codex CLI hook payload into our internal CloudEvent
// representation. Codex uses a flat JSON structure with top-level fields like
// "hook_event_name", "cwd", and "session_id". We map those into the CloudEvent
// fields that downstream handlers read.
func parseCodexEvent(raw []byte) (*CloudEvent, error) {
	var p codexPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parseCodexEvent: %w", err)
	}

	ev := &CloudEvent{
		AgentID:        "codex",
		SessionID:      p.SessionID,
		CWD:            p.CWD,
		PermissionMode: p.PermissionMode,
		Model:          p.Model,
		TranscriptPath: p.TranscriptPath,
		Source:         p.Source,
		Prompt:         p.Prompt,
		ToolName:       p.ToolName,
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
		AgentID: "gemini",
		// Gemini may use "invocation_id" as the session identifier;
		// fall back to session_id if present.
		SessionID: p.SessionID,
		CWD:       p.CWD,
		Model:     p.Model,
		Prompt:    p.Prompt,
		// BeforeTool / AfterTool: tool name is nested under "tool".
		ToolName:  p.Tool.Name,
		ToolInput: p.Tool.Input,
	}
	// If session_id is empty, use invocation_id as a surrogate so that
	// session-scoped DB lookups have something to work with.
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
//   - "continue": true/false  (required for lifecycle events)
//   - "systemMessage": "..."  (equivalent to Claude's additionalContext)
//   - "decision": "allow"|"block" and "reason" for PreToolUse decisions
func emitCodexResponse(w io.Writer, result *HookResult) error {
	type codexResponse struct {
		Continue      bool   `json:"continue"`
		SystemMessage string `json:"systemMessage,omitempty"`
		Decision      string `json:"decision,omitempty"`
		Reason        string `json:"reason,omitempty"`
		StopReason    string `json:"stopReason,omitempty"`
	}

	resp := codexResponse{
		// Default to continue=true unless the result is a hard block.
		Continue: result.Decision != "block" && result.Decision != "deny",
	}

	// Map AdditionalContext → systemMessage (Codex's inject-into-conversation field).
	if result.AdditionalContext != "" {
		resp.SystemMessage = result.AdditionalContext
	}

	// Preserve decision/reason for blocking hooks (PreToolUse equivalent).
	if result.Decision == "block" || result.Decision == "deny" {
		resp.Decision = result.Decision
		resp.Reason = result.Reason
		resp.StopReason = result.Reason
	}

	return json.NewEncoder(w).Encode(resp)
}

// emitGeminiResponse writes the Gemini CLI wire-format JSON to w.
// Gemini's output schema (from https://geminicli.com/docs/hooks/reference/):
//   - Exit code 0 with JSON output is the preferred path (not exit 2).
//   - Common output fields include "continue" and "systemPrompt" (context injection).
//   - BeforeTool blocking uses "decision": "block" and "reason".
//
// This is a best-effort implementation pending a real captured payload;
// a follow-up bug will tighten the schema once one is available.
func emitGeminiResponse(w io.Writer, result *HookResult) error {
	type geminiResponse struct {
		Continue     bool   `json:"continue"`
		SystemPrompt string `json:"systemPrompt,omitempty"`
		Decision     string `json:"decision,omitempty"`
		Reason       string `json:"reason,omitempty"`
	}

	resp := geminiResponse{
		Continue: result.Decision != "block" && result.Decision != "deny",
	}

	// Map AdditionalContext → systemPrompt (Gemini's context injection field).
	if result.AdditionalContext != "" {
		resp.SystemPrompt = result.AdditionalContext
	}

	// Preserve decision/reason for blocking hooks (BeforeTool equivalent).
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
	case HarnessCodex, HarnessGemini:
		// Codex/Gemini expect {"continue": true} on allow
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
	switch harness {
	case HarnessCodex:
		return emitCodexResponse(os.Stdout, result)
	case HarnessGemini:
		return emitGeminiResponse(os.Stdout, result)
	default:
		return emitClaudeResponse(os.Stdout, result)
	}
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
