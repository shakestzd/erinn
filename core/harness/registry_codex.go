package harness

import (
	"fmt"
)

// _codexOtelID must match otel.HarnessCodex ("codex").
// Cross-package assertion is in registry_test.go (TestRegistry_IDsMatchOtelConsts).
const _codexOtelID = "codex"

// Env var name constants for the Codex OTel env injection.
// Declared here so the names are co-located with the function that uses them.
const (
	codexEnvOTLPEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"
	codexEnvServiceName  = "OTEL_SERVICE_NAME"
	codexEnvOtelSession  = "WIPNOTE_OTEL_SESSION"
)

// codexOtelEnv returns the OTel-related environment variables to inject when
// launching the Codex CLI. The port and sessionID come from the per-session
// collector spawned by the launcher.
func codexOtelEnv(port int, sessionID string) []string {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	return []string{
		codexEnvOTLPEndpoint + "=" + endpoint,
		codexEnvServiceName + "=codex-cli",
		codexEnvOtelSession + "=" + sessionID,
	}
}

func init() {
	if _codexOtelID != "codex" {
		panic("harness: _codexOtelID mismatch — must equal otel.HarnessCodex")
	}
	cfg := &HarnessConfig{
		ID:      _codexOtelID,
		AgentID: "codex",
		// Codex emits two service.name variants: the TypeScript CLI and the Rust rewrite.
		ServiceNames:   []string{"codex-cli", "codex_cli_rs"},
		SessionAttr:    "conversation.id",
		HookEventNames: nil,
		HooksHarness:   HooksCodex,
		OtelEnv:        codexOtelEnv,
	}
	if cfg.OtelEnv == nil {
		panic("harness: codex OtelEnv must be non-nil")
	}
	Register(cfg)
}
