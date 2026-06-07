// Package harness is a leaf package that owns the canonical per-harness
// configuration for all AI coding harnesses supported by wipnote.
//
// It is intentionally free of imports from other internal packages to avoid
// dependency cycles. The three registry_*.go files register their configs
// via package-level init() functions.
//
// The otel.Harness constants and hooks.Harness ints are bridged only at the
// test layer (registry_test.go imports both); this package imports neither.
package harness

import "sync"

// OtelEnvFunc returns env vars to inject before launching this harness.
// Must be non-nil for all three harnesses (Claude, Codex, Gemini).
// Each harness init() panics at startup if its OtelEnv is nil.
type OtelEnvFunc func(port int, sessionID string) []string

// HooksHarness mirrors the hooks.Harness int without importing internal/hooks.
// The iota ordering MUST match hooks.HarnessClaude/Codex/Gemini exactly; this
// is verified by TestRegistry_HooksHarnessMatchesHooksConst in registry_test.go.
type HooksHarness int

const (
	// HooksClaude corresponds to hooks.HarnessClaude (iota 0).
	HooksClaude HooksHarness = iota
	// HooksCodex corresponds to hooks.HarnessCodex (iota 1).
	HooksCodex
	// HooksGemini corresponds to hooks.HarnessGemini (iota 2).
	HooksGemini
)

// HarnessConfig holds the canonical per-harness identity and configuration
// consumed by OTel ingestion, launcher env injection, and hook detection.
type HarnessConfig struct {
	// ID is the DB-canonical harness identifier written to agent_events.harness.
	// Values: "claude_code" | "codex" | "gemini_cli"
	// Must match the corresponding otel.Harness* constant (verified by test).
	ID string

	// AgentID is the value set in WIPNOTE_AGENT_ID by the launcher and read
	// by detectHarnessWithEnv for harness disambiguation.
	// Values: "claude" | "codex" | "gemini"
	AgentID string

	// ServiceNames contains the OTel resource service.name values that
	// identify signals from this harness. Codex emits two variants.
	ServiceNames []string

	// SessionAttr is the OTel attribute key whose value becomes SessionID
	// in UnifiedSignal. Claude and Gemini use "session.id"; Codex uses "conversation.id".
	SessionAttr string

	// HookEventNames lists the native hook_event_name values emitted by this
	// harness. Non-empty for Gemini only; Claude and Codex leave it nil.
	HookEventNames []string

	// HooksHarness bridges to the hooks.Harness int without importing internal/hooks.
	// Callers can cast: hooks.Harness(cfg.HooksHarness)
	HooksHarness HooksHarness

	// OtelEnv returns the OTel-related environment variables to inject when
	// launching this harness. Must be non-nil for all three harnesses
	// (Claude, Codex, Gemini). Each registry init() panics at startup if
	// OtelEnv is nil, preventing silent misconfiguration.
	OtelEnv OtelEnvFunc

	// LaunchEnv holds harness-specific env vars to inject at launch time,
	// beyond the OTel and agent-ID injections. Each entry is "KEY=VALUE".
	// Examples: CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1 for Claude.
	// These are layered on top of os.Environ() by the launcher via
	// addIfUnset semantics — explicit user-set values always win.
	LaunchEnv []string
}

// BuildAgentEnv returns the WIPNOTE_AGENT_ID and WIPNOTE_AGENT_TYPE env vars
// for this harness. Used by launchers to attribute spawned agent processes.
func (c *HarnessConfig) BuildAgentEnv() []string {
	return []string{
		"WIPNOTE_AGENT_ID=" + c.AgentID,
		"WIPNOTE_AGENT_TYPE=" + c.AgentID,
	}
}

var (
	mu       sync.RWMutex
	byID     = map[string]*HarnessConfig{}
	byAgent  = map[string]*HarnessConfig{}
	byHooks  = map[HooksHarness]*HarnessConfig{}
	allSlice []*HarnessConfig
)

// Register adds cfg to the registry. It is called from the per-harness
// init() functions in registry_claude.go, registry_codex.go, and
// registry_gemini.go. Panics on duplicate ID.
func Register(cfg *HarnessConfig) {
	mu.Lock()
	defer mu.Unlock()

	if _, exists := byID[cfg.ID]; exists {
		panic("harness: duplicate registration for ID " + cfg.ID)
	}
	byID[cfg.ID] = cfg
	byAgent[cfg.AgentID] = cfg
	byHooks[cfg.HooksHarness] = cfg
	allSlice = append(allSlice, cfg)
}

// Get returns the HarnessConfig for the given canonical ID (e.g. "claude_code",
// "codex", "gemini_cli"). Returns nil if not found.
func Get(id string) *HarnessConfig {
	mu.RLock()
	defer mu.RUnlock()
	return byID[id]
}

// GetByAgentID returns the HarnessConfig whose AgentID matches (e.g. "claude",
// "codex", "gemini"). Returns nil if not found.
func GetByAgentID(agentID string) *HarnessConfig {
	mu.RLock()
	defer mu.RUnlock()
	return byAgent[agentID]
}

// GetByHooksHarness returns the HarnessConfig for the given HooksHarness value.
// Returns nil if not found.
func GetByHooksHarness(h HooksHarness) *HarnessConfig {
	mu.RLock()
	defer mu.RUnlock()
	return byHooks[h]
}

// All returns all registered HarnessConfig entries. The order reflects
// alphabetical filename ordering of the init() registrations
// (registry_claude.go, registry_codex.go, registry_gemini.go).
func All() []*HarnessConfig {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]*HarnessConfig, len(allSlice))
	copy(out, allSlice)
	return out
}

// DisplayName is the canonical user-facing harness name (the primary OTel
// service.name): "claude-code" | "codex-cli" | "gemini-cli". This is the name
// `wipnote who` and per-harness capability tables key on.
func (c *HarnessConfig) DisplayName() string {
	if len(c.ServiceNames) > 0 {
		return c.ServiceNames[0]
	}
	return c.ID
}

// NormalizeDisplayName resolves any raw harness/agent token to its canonical
// display name (ServiceNames[0]). It accepts every form a token can take in
// the wild:
//
//   - launcher WIPNOTE_AGENT_ID / WIPNOTE_AGENT_TYPE values: "claude" |
//     "codex" | "gemini" (AgentID)
//   - DB-canonical IDs written to agent_events.harness: "claude_code" |
//     "codex" | "gemini_cli" (ID)
//   - already-canonical OTel service names: "claude-code" | "codex-cli" |
//     "codex_cli_rs" | "gemini-cli" (ServiceNames)
//
// Resolution is registry-driven (no hardcoded string table) so it stays
// correct as harnesses are added/renamed. Returns the input unchanged when no
// registered harness matches — callers then surface it as an unknown harness
// rather than misreporting it as a known one.
func NormalizeDisplayName(raw string) string {
	if raw == "" {
		return raw
	}
	mu.RLock()
	defer mu.RUnlock()
	if cfg := byAgent[raw]; cfg != nil {
		return cfg.DisplayName()
	}
	if cfg := byID[raw]; cfg != nil {
		return cfg.DisplayName()
	}
	for _, cfg := range allSlice {
		for _, sn := range cfg.ServiceNames {
			if sn == raw {
				return cfg.DisplayName()
			}
		}
	}
	return raw
}
