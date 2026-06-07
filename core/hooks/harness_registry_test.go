package hooks

import (
	"testing"

	"github.com/shakestzd/wipnote/core/harness"
)

// TestHarnessFromConfig verifies that harnessFromConfig correctly bridges a
// *harness.HarnessConfig to the hooks.Harness int via the HooksHarness field.
func TestHarnessFromConfig(t *testing.T) {
	cfg := harness.Get("codex")
	if cfg == nil {
		t.Fatal("harness.Get(\"codex\") returned nil; registry not initialized")
	}
	got := harnessFromConfig(cfg)
	if got != HarnessCodex {
		t.Errorf("harnessFromConfig(codex cfg) = %v, want HarnessCodex", got)
	}
}

// TestHarnessFromConfig_Nil verifies that harnessFromConfig(nil) returns
// HarnessClaude (the safe default).
func TestHarnessFromConfig_Nil(t *testing.T) {
	got := harnessFromConfig(nil)
	if got != HarnessClaude {
		t.Errorf("harnessFromConfig(nil) = %v, want HarnessClaude", got)
	}
}

// TestDetectHarness_FromRegistry_Gemini verifies that detectHarnessWithEnv
// correctly classifies a payload whose hook_event_name is a Gemini-native
// value ("BeforeAgent") using registry lookup rather than a hardcoded slice.
// This must still pass after the switch-to-registry refactor.
func TestDetectHarness_FromRegistry_Gemini(t *testing.T) {
	payload := []byte(`{
		"session_id": "gemini-sess-reg-test",
		"cwd": "/tmp",
		"hook_event_name": "BeforeAgent"
	}`)

	got := detectHarnessWithEnv(payload, noClaudeEnv)
	if got != HarnessGemini {
		t.Errorf("detectHarnessWithEnv(hook_event_name=BeforeAgent, noClaudeEnv) = %v, want HarnessGemini; "+
			"registry-driven detection must still classify Gemini-native events correctly", got)
	}
}

// TestDetectHarness_FromRegistry_GeminiAllEvents verifies all Gemini-native
// hook_event_name values are classified as HarnessGemini via registry lookup.
func TestDetectHarness_FromRegistry_GeminiAllEvents(t *testing.T) {
	cfg := harness.Get("gemini_cli")
	if cfg == nil {
		t.Fatal("harness.Get(\"gemini_cli\") returned nil; registry not initialized")
	}
	if len(cfg.HookEventNames) == 0 {
		t.Fatal("Gemini registry entry has no HookEventNames; registry_gemini.go may be incomplete")
	}

	for _, eventName := range cfg.HookEventNames {
		eventName := eventName
		t.Run(eventName, func(t *testing.T) {
			payload := []byte(`{"session_id":"s","cwd":"/","hook_event_name":"` + eventName + `"}`)
			got := detectHarnessWithEnv(payload, noClaudeEnv)
			if got != HarnessGemini {
				t.Errorf("detectHarnessWithEnv(hook_event_name=%q) = %v, want HarnessGemini", eventName, got)
			}
		})
	}
}
