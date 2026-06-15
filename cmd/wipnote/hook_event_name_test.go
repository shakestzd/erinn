package main

// Regression tests for Finding 267 (roborev bug-60107613):
// hookEventNameForResponse must echo the correct harness-native event name
// for Gemini and Antigravity sessions. When the parsed CloudEvent carries a
// HookEventName (populated from hook_event_name in the incoming payload),
// that value is echoed back directly. Without it, a harness-aware fallback
// mapping is used.

import (
	"testing"

	"github.com/shakestzd/wipnote/core/hooks"
)

func TestHookEventNameForResponse_ClaudeSubcommands(t *testing.T) {
	cases := []struct {
		subcommand string
		want       string
	}{
		{"session-start", "SessionStart"},
		{"session-end", "SessionEnd"},
		{"user-prompt", "UserPromptSubmit"},
		{"pretooluse", "PreToolUse"},
		{"posttooluse", "PostToolUse"},
		{"after-agent", "AfterAgent"},
		{"after-model", "AfterModel"},
		{"task-started", "TaskStarted"},
		{"task-aborted", "TurnAborted"},
	}
	for _, tc := range cases {
		t.Run(tc.subcommand, func(t *testing.T) {
			// No HookEventName in event (Claude path: field absent).
			got := hookEventNameForResponse(tc.subcommand, &hooks.CloudEvent{})
			if got != tc.want {
				t.Errorf("subcommand %q: want %q, got %q", tc.subcommand, tc.want, got)
			}
		})
	}
}

// TestHookEventNameForResponse_EchosIncomingHookEventName asserts that when the
// CloudEvent carries a HookEventName (populated by parseGeminiEvent /
// parseAntigravityEvent from hook_event_name in the payload), that name is
// echoed back rather than the Claude canonical name.
func TestHookEventNameForResponse_EchosIncomingHookEventName(t *testing.T) {
	cases := []struct {
		subcommand    string
		hookEventName string // Gemini/Antigravity native event name in incoming payload
		want          string
	}{
		// Gemini registers user-prompt handler under "BeforeAgent"
		{"user-prompt", "BeforeAgent", "BeforeAgent"},
		// Gemini registers pretooluse handler under "BeforeTool"
		{"pretooluse", "BeforeTool", "BeforeTool"},
		// Gemini registers posttooluse handler under "AfterTool"
		{"posttooluse", "AfterTool", "AfterTool"},
		// Antigravity shares Gemini event names
		{"user-prompt", "BeforeAgent", "BeforeAgent"},
		{"pretooluse", "BeforeTool", "BeforeTool"},
		{"posttooluse", "AfterTool", "AfterTool"},
		// SessionStart is unchanged across harnesses
		{"session-start", "SessionStart", "SessionStart"},
		// AfterAgent / AfterModel: Gemini-native names echo through
		{"after-agent", "AfterAgent", "AfterAgent"},
		{"after-model", "AfterModel", "AfterModel"},
	}
	for _, tc := range cases {
		t.Run(tc.subcommand+"/"+tc.hookEventName, func(t *testing.T) {
			ev := &hooks.CloudEvent{HookEventName: tc.hookEventName}
			got := hookEventNameForResponse(tc.subcommand, ev)
			if got != tc.want {
				t.Errorf("subcommand %q hookEventName %q: want %q, got %q",
					tc.subcommand, tc.hookEventName, tc.want, got)
			}
		})
	}
}

// TestHookEventNameForResponse_EmptyEventFallsBackToMapping asserts that when
// HookEventName is empty (e.g. Claude, which does not send hook_event_name),
// the subcommand-to-event-name mapping is used as before.
func TestHookEventNameForResponse_EmptyEventFallsBackToMapping(t *testing.T) {
	got := hookEventNameForResponse("user-prompt", &hooks.CloudEvent{})
	if got != "UserPromptSubmit" {
		t.Errorf("empty HookEventName: want UserPromptSubmit, got %q", got)
	}

	got = hookEventNameForResponse("pretooluse", &hooks.CloudEvent{})
	if got != "PreToolUse" {
		t.Errorf("empty HookEventName: want PreToolUse, got %q", got)
	}

	got = hookEventNameForResponse("posttooluse", &hooks.CloudEvent{})
	if got != "PostToolUse" {
		t.Errorf("empty HookEventName: want PostToolUse, got %q", got)
	}
}
