package harness

import (
	"fmt"
)

// _geminiOtelID must match otel.HarnessGemini ("gemini_cli").
// Cross-package assertion is in registry_test.go (TestRegistry_IDsMatchOtelConsts).
const _geminiOtelID = "gemini_cli"

// Env var name constants for the Gemini OTel env injection.
// Declared here so the names are co-located with the function that uses them.
const (
	geminiEnvTelemetryEnabled      = "GEMINI_TELEMETRY_ENABLED"
	geminiEnvTelemetryUseCollector = "GEMINI_TELEMETRY_USE_COLLECTOR"
	geminiEnvTelemetryTraces       = "GEMINI_TELEMETRY_TRACES"
	geminiEnvTelemetryOTLPEndpoint = "GEMINI_TELEMETRY_OTLP_ENDPOINT"
	geminiEnvTelemetryOTLPProtocol = "GEMINI_TELEMETRY_OTLP_PROTOCOL"
	geminiEnvOtelSession           = "WIPNOTE_OTEL_SESSION"
)

// geminiOtelEnv returns the OTel-related environment variables to inject when
// launching the Gemini CLI. The port and sessionID come from the per-session
// collector spawned by the launcher.
func geminiOtelEnv(port int, sessionID string) []string {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	return []string{
		geminiEnvTelemetryEnabled + "=true",
		geminiEnvTelemetryUseCollector + "=true",
		geminiEnvTelemetryTraces + "=true",
		geminiEnvTelemetryOTLPEndpoint + "=" + endpoint,
		geminiEnvTelemetryOTLPProtocol + "=http",
		geminiEnvOtelSession + "=" + sessionID,
	}
}

func init() {
	if _geminiOtelID != "gemini_cli" {
		panic("harness: _geminiOtelID mismatch — must equal otel.HarnessGemini")
	}
	cfg := &HarnessConfig{
		ID:           _geminiOtelID,
		AgentID:      "gemini",
		ServiceNames: []string{"gemini-cli"},
		SessionAttr:  "session.id",
		// Gemini-native hook_event_name values used by detectHarnessWithEnv for
		// payload-only discrimination when WIPNOTE_AGENT_ID is not set.
		HookEventNames: []string{
			"BeforeAgent",
			"AfterAgent",
			"AfterModel",
			"BeforeTool",
			"AfterTool",
		},
		HooksHarness: HooksGemini,
		OtelEnv:      geminiOtelEnv,
	}
	if cfg.OtelEnv == nil {
		panic("harness: gemini OtelEnv must be non-nil")
	}
	Register(cfg)
}
