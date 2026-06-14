package harness

import (
	"fmt"
)

// _antigravityOtelID must match otel.HarnessAntigravity ("antigravity_cli").
const _antigravityOtelID = "antigravity_cli"

// antigravityOtelEnv returns the OTel-related environment variables to inject when
// launching the Antigravity CLI.
func antigravityOtelEnv(port int, sessionID string) []string {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	return []string{
		// Standard OpenTelemetry environment variables
		"OTEL_EXPORTER_OTLP_ENDPOINT=" + endpoint,
		"OTEL_EXPORTER_OTLP_PROTOCOL=http",
		"OTEL_SERVICE_NAME=antigravity-cli",
		"WIPNOTE_OTEL_SESSION=" + sessionID,
		// Fallbacks for Gemini compatibility/engine settings
		"GEMINI_TELEMETRY_ENABLED=true",
		"GEMINI_TELEMETRY_USE_COLLECTOR=true",
		"GEMINI_TELEMETRY_TRACES=true",
		"GEMINI_TELEMETRY_OTLP_ENDPOINT=" + endpoint,
		"GEMINI_TELEMETRY_OTLP_PROTOCOL=http",
	}
}

func init() {
	if _antigravityOtelID != "antigravity_cli" {
		panic("harness: _antigravityOtelID mismatch — must equal otel.HarnessAntigravity")
	}
	cfg := &HarnessConfig{
		ID:           _antigravityOtelID,
		AgentID:      "antigravity",
		ServiceNames: []string{"antigravity-cli"},
		SessionAttr:  "session.id",

		HooksHarness: HooksAntigravity,
		OtelEnv:      antigravityOtelEnv,
	}
	if cfg.OtelEnv == nil {
		panic("harness: antigravity OtelEnv must be non-nil")
	}
	Register(cfg)
}
