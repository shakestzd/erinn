package harness_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/harness"
	"github.com/shakestzd/wipnote/core/hooks"
)

// TestRegistry_All_HasThree verifies that All() returns exactly three entries
// with the expected harness IDs.
func TestRegistry_All_HasFour(t *testing.T) {
	all := harness.All()
	if len(all) != 4 {
		t.Fatalf("All() returned %d entries, want 4", len(all))
	}

	ids := make([]string, len(all))
	for i, cfg := range all {
		ids[i] = cfg.ID
	}
	sort.Strings(ids)

	want := []string{"antigravity_cli", "claude_code", "codex", "gemini_cli"}
	for i, id := range want {
		if ids[i] != id {
			t.Errorf("All() IDs[%d] = %q, want %q", i, ids[i], id)
		}
	}
}

// TestRegistry_Get_ByID checks lookup by canonical harness ID.
func TestRegistry_Get_ByID(t *testing.T) {
	cfg := harness.Get("codex")
	if cfg == nil {
		t.Fatal("Get(\"codex\") returned nil")
	}
	if cfg.AgentID != "codex" {
		t.Errorf("Get(\"codex\").AgentID = %q, want \"codex\"", cfg.AgentID)
	}

	if got := harness.Get("nonexistent"); got != nil {
		t.Errorf("Get(\"nonexistent\") = %v, want nil", got)
	}
}

// TestRegistry_GetByAgentID checks lookup by WIPNOTE_AGENT_ID value.
func TestRegistry_GetByAgentID(t *testing.T) {
	cfg := harness.GetByAgentID("gemini")
	if cfg == nil {
		t.Fatal("GetByAgentID(\"gemini\") returned nil")
	}
	if cfg.ID != "gemini_cli" {
		t.Errorf("GetByAgentID(\"gemini\").ID = %q, want \"gemini_cli\"", cfg.ID)
	}

	if got := harness.GetByAgentID("nonexistent"); got != nil {
		t.Errorf("GetByAgentID(\"nonexistent\") = %v, want nil", got)
	}
}

// TestRegistry_GetByHooksHarness checks lookup by HooksHarness enum value.
func TestRegistry_GetByHooksHarness(t *testing.T) {
	cfg := harness.GetByHooksHarness(harness.HooksGemini)
	if cfg == nil {
		t.Fatal("GetByHooksHarness(HooksGemini) returned nil")
	}
	if cfg.ID != "gemini_cli" {
		t.Errorf("GetByHooksHarness(HooksGemini).ID = %q, want \"gemini_cli\"", cfg.ID)
	}
}

// TestRegistry_GeminiEventNames verifies the Gemini hook event names are populated.
func TestRegistry_GeminiEventNames(t *testing.T) {
	cfg := harness.Get("gemini_cli")
	if cfg == nil {
		t.Fatal("Get(\"gemini_cli\") returned nil")
	}
	if len(cfg.HookEventNames) == 0 {
		t.Fatal("Get(\"gemini_cli\").HookEventNames is empty")
	}

	found := false
	for _, name := range cfg.HookEventNames {
		if name == "BeforeAgent" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Get(\"gemini_cli\").HookEventNames does not contain \"BeforeAgent\"; got %v", cfg.HookEventNames)
	}
}

// Note: the harness-ID ↔ otel.Harness constant cross-check lives on the otel
// side (internal/otel/harness_bridge_test.go) so this package — destined for
// core/ — never imports otel. otel may import core/harness; core may not import
// otel.

// TestRegistry_HooksHarnessMatchesHooksConst verifies iota ordering alignment
// between internal/harness and internal/hooks.
func TestRegistry_HooksHarnessMatchesHooksConst(t *testing.T) {
	tests := []struct {
		harnessVal harness.HooksHarness
		hooksVal   hooks.Harness
		name       string
	}{
		{harness.HooksClaude, hooks.HarnessClaude, "Claude"},
		{harness.HooksCodex, hooks.HarnessCodex, "Codex"},
		{harness.HooksGemini, hooks.HarnessGemini, "Gemini"},
		{harness.HooksAntigravity, hooks.HarnessAntigravity, "Antigravity"},
	}

	for _, tt := range tests {
		if int(tt.harnessVal) != int(tt.hooksVal) {
			t.Errorf("%s: harness.Hooks%s=%d != hooks.Harness%s=%d",
				tt.name, tt.name, int(tt.harnessVal), tt.name, int(tt.hooksVal))
		}
	}
}

// TestCodexOtelEnv verifies that the Codex OtelEnv function returns the
// expected OTel environment variables including the service name and port.
func TestCodexOtelEnv(t *testing.T) {
	cfg := harness.Get("codex")
	if cfg == nil {
		t.Fatal("Get(\"codex\") returned nil")
	}
	if cfg.OtelEnv == nil {
		t.Fatal("Get(\"codex\").OtelEnv is nil — must be non-nil for Codex")
	}

	got := cfg.OtelEnv(9999, "sess-abc")
	if got == nil {
		t.Fatal("OtelEnv(9999, \"sess-abc\") returned nil")
	}

	// Must contain OTEL_SERVICE_NAME=codex-cli.
	foundServiceName := false
	foundPort := false
	for _, e := range got {
		if e == "OTEL_SERVICE_NAME=codex-cli" {
			foundServiceName = true
		}
		if strings.Contains(e, "9999") {
			foundPort = true
		}
	}
	if !foundServiceName {
		t.Errorf("OtelEnv missing OTEL_SERVICE_NAME=codex-cli; got %v", got)
	}
	if !foundPort {
		t.Errorf("OtelEnv missing entry containing port 9999; got %v", got)
	}
}

// TestGeminiOtelEnv verifies that the Gemini OtelEnv function returns the
// expected OTel environment variables including GEMINI_TELEMETRY_ENABLED=true.
func TestGeminiOtelEnv(t *testing.T) {
	cfg := harness.Get("gemini_cli")
	if cfg == nil {
		t.Fatal("Get(\"gemini_cli\") returned nil")
	}
	if cfg.OtelEnv == nil {
		t.Fatal("Get(\"gemini_cli\").OtelEnv is nil — must be non-nil for Gemini")
	}

	got := cfg.OtelEnv(8080, "sess-xyz")
	if got == nil {
		t.Fatal("OtelEnv(8080, \"sess-xyz\") returned nil")
	}

	foundEnabled := false
	for _, e := range got {
		if e == "GEMINI_TELEMETRY_ENABLED=true" {
			foundEnabled = true
			break
		}
	}
	if !foundEnabled {
		t.Errorf("OtelEnv missing GEMINI_TELEMETRY_ENABLED=true; got %v", got)
	}
}

func TestAntigravityOtelEnv(t *testing.T) {
	cfg := harness.Get("antigravity_cli")
	if cfg == nil {
		t.Fatal("Get(\"antigravity_cli\") returned nil")
	}
	if cfg.OtelEnv == nil {
		t.Fatal("Get(\"antigravity_cli\").OtelEnv is nil -- must be non-nil for Antigravity")
	}

	got := cfg.OtelEnv(7777, "sess-antigravity")
	if got == nil {
		t.Fatal("OtelEnv(7777, \"sess-antigravity\") returned nil")
	}

	foundServiceName := false
	foundPort := false
	for _, e := range got {
		if e == "OTEL_SERVICE_NAME=antigravity-cli" {
			foundServiceName = true
		}
		if strings.Contains(e, "7777") {
			foundPort = true
		}
	}
	if !foundServiceName {
		t.Errorf("OtelEnv missing OTEL_SERVICE_NAME=antigravity-cli; got %v", got)
	}
	if !foundPort {
		t.Errorf("OtelEnv missing entry containing port 7777; got %v", got)
	}
}

// TestRegistry_ClaudeOtelEnv_NoLongerNil verifies that Claude's OtelEnv field
// is non-nil — the registry now centralizes Claude OTel config alongside Codex
// and Gemini.
func TestRegistry_ClaudeOtelEnv_NoLongerNil(t *testing.T) {
	cfg := harness.Get("claude_code")
	if cfg == nil {
		t.Fatal("Get(\"claude_code\") returned nil")
	}
	if cfg.OtelEnv == nil {
		t.Error("Get(\"claude_code\").OtelEnv is nil — must be non-nil (centralized in registry)")
	}
}

// TestRegistry_ClaudeOtelEnv_HasAllKeys verifies that claudeOtelEnv returns
// all 10 expected KEY=VALUE pairs with the endpoint pointing at the given port.
func TestRegistry_ClaudeOtelEnv_HasAllKeys(t *testing.T) {
	cfg := harness.Get("claude_code")
	if cfg == nil {
		t.Fatal("Get(\"claude_code\") returned nil")
	}
	if cfg.OtelEnv == nil {
		t.Fatal("Get(\"claude_code\").OtelEnv is nil")
	}

	got := cfg.OtelEnv(9999, "")
	if len(got) != 10 {
		t.Fatalf("OtelEnv(9999, \"\") returned %d entries, want 10; got %v", len(got), got)
	}

	wantPairs := []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY=1",
		"CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1",
		"OTEL_METRICS_EXPORTER=otlp",
		"OTEL_LOGS_EXPORTER=otlp",
		"OTEL_TRACES_EXPORTER=otlp",
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:9999",
		"OTEL_LOG_TOOL_DETAILS=1",
		"OTEL_LOG_USER_PROMPTS=1",
		"OTEL_LOG_TOOL_CONTENT=1",
	}

	gotSet := make(map[string]bool, len(got))
	for _, kv := range got {
		gotSet[kv] = true
	}
	for _, want := range wantPairs {
		if !gotSet[want] {
			t.Errorf("OtelEnv(9999, \"\") missing %q; got %v", want, got)
		}
	}
}

// TestBuildAgentEnv_Codex verifies that BuildAgentEnv returns the correct
// WIPNOTE_AGENT_ID and WIPNOTE_AGENT_TYPE values for Codex.
func TestBuildAgentEnv_Codex(t *testing.T) {
	cfg := harness.Get("codex")
	if cfg == nil {
		t.Fatal("Get(\"codex\") returned nil")
	}

	got := cfg.BuildAgentEnv()
	want := []string{"WIPNOTE_AGENT_ID=codex", "WIPNOTE_AGENT_TYPE=codex"}
	if len(got) != len(want) {
		t.Fatalf("BuildAgentEnv() = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("BuildAgentEnv()[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestBuildAgentEnv_Gemini verifies that BuildAgentEnv returns the correct
// WIPNOTE_AGENT_ID and WIPNOTE_AGENT_TYPE values for Gemini.
func TestBuildAgentEnv_Gemini(t *testing.T) {
	cfg := harness.Get("gemini_cli")
	if cfg == nil {
		t.Fatal("Get(\"gemini_cli\") returned nil")
	}

	got := cfg.BuildAgentEnv()
	want := []string{"WIPNOTE_AGENT_ID=gemini", "WIPNOTE_AGENT_TYPE=gemini"}
	if len(got) != len(want) {
		t.Fatalf("BuildAgentEnv() = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("BuildAgentEnv()[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestRegistry_LaunchEnv_Claude verifies that the Claude harness registry entry
// contains exactly the expected LaunchEnv values and that Codex and Gemini leave
// LaunchEnv empty/nil.
func TestRegistry_LaunchEnv_Claude(t *testing.T) {
	cfg := harness.Get("claude_code")
	if cfg == nil {
		t.Fatal("Get(\"claude_code\") returned nil")
	}

	want := []string{"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1"}
	if len(cfg.LaunchEnv) != len(want) {
		t.Fatalf("Get(\"claude_code\").LaunchEnv = %v, want %v", cfg.LaunchEnv, want)
	}
	for i, w := range want {
		if cfg.LaunchEnv[i] != w {
			t.Errorf("Get(\"claude_code\").LaunchEnv[%d] = %q, want %q", i, cfg.LaunchEnv[i], w)
		}
	}
}

// TestRegistry_LaunchEnv_CodexEmpty verifies that the Codex harness has no LaunchEnv.
func TestRegistry_LaunchEnv_CodexEmpty(t *testing.T) {
	cfg := harness.Get("codex")
	if cfg == nil {
		t.Fatal("Get(\"codex\") returned nil")
	}
	if len(cfg.LaunchEnv) != 0 {
		t.Errorf("Get(\"codex\").LaunchEnv = %v, want empty/nil", cfg.LaunchEnv)
	}
}

// TestRegistry_LaunchEnv_GeminiEmpty verifies that the Gemini harness has no LaunchEnv.
func TestRegistry_LaunchEnv_GeminiEmpty(t *testing.T) {
	cfg := harness.Get("gemini_cli")
	if cfg == nil {
		t.Fatal("Get(\"gemini_cli\") returned nil")
	}
	if len(cfg.LaunchEnv) != 0 {
		t.Errorf("Get(\"gemini_cli\").LaunchEnv = %v, want empty/nil", cfg.LaunchEnv)
	}
}
