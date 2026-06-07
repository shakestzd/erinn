package harness

import "fmt"

// _claudeOtelID must match otel.HarnessClaude ("claude_code").
// Cross-package assertion is in registry_test.go (TestRegistry_IDsMatchOtelConsts).
const _claudeOtelID = "claude_code"

// claudeOtelEnv returns the OTel-related environment variables to inject when
// launching Claude Code. The port comes from the per-session collector spawned
// by the launcher. The sessionID argument is ignored by Claude (no session.id
// env var) and is present only to satisfy the OtelEnvFunc signature.
func claudeOtelEnv(port int, sessionID string) []string {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	return []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY=1",
		"CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1",
		"OTEL_METRICS_EXPORTER=otlp",
		"OTEL_LOGS_EXPORTER=otlp",
		"OTEL_TRACES_EXPORTER=otlp",
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT=" + endpoint,
		// Privacy note: these three default-on flags cause Claude Code to emit
		// potentially sensitive content via OTel. They mirror the launcher's
		// historical default; users can override per-key to "0" before launch.
		//   OTEL_LOG_TOOL_DETAILS=0  → suppress bash commands, skill names, MCP tool names
		//   OTEL_LOG_USER_PROMPTS=0  → suppress user prompt content
		//   OTEL_LOG_TOOL_CONTENT=0  → suppress tool input/output payloads
		"OTEL_LOG_TOOL_DETAILS=1",
		"OTEL_LOG_USER_PROMPTS=1",
		"OTEL_LOG_TOOL_CONTENT=1",
	}
}

func init() {
	// Compile-time-ish guard: the condition is always false, but the const
	// reference forces the compiler to keep it, and a future typo edit would
	// be caught at test time by TestRegistry_IDsMatchOtelConsts.
	if _claudeOtelID != "claude_code" {
		panic("harness: _claudeOtelID mismatch — must equal otel.HarnessClaude")
	}
	cfg := &HarnessConfig{
		ID:             _claudeOtelID,
		AgentID:        "claude",
		ServiceNames:   []string{"claude-code"},
		SessionAttr:    "session.id",
		HookEventNames: nil,
		HooksHarness:   HooksClaude,
		OtelEnv:        claudeOtelEnv,
		LaunchEnv:      []string{"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1"},
	}
	if cfg.OtelEnv == nil {
		panic("harness: claude OtelEnv must be non-nil")
	}
	Register(cfg)
}
