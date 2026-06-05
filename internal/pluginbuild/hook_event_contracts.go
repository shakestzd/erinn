package pluginbuild

import "fmt"

// hookEventClassification categorises a Claude Code hook event by the contract
// it imposes on the hook process's stdout.
//
// Claude Code hook docs: https://code.claude.com/docs/en/hooks
// Worktree replacement contract: https://code.claude.com/docs/en/hooks#worktreecreate
type hookEventClassification int

const (
	// Observational events ignore stdout entirely. The hook process can write
	// anything (or nothing); Claude Code discards it. Observe-only registration
	// is the only correct use for these events.
	Observational hookEventClassification = iota

	// AdditiveControlling events honour a JSON HookResult on stdout
	// ({continue, decision, additionalContext, …}). Observe-only registration
	// is safe — omitting a JSON body or returning {} is treated as "allow".
	AdditiveControlling

	// ReplacementOnRegistration events do NOT read a JSON HookResult. Instead
	// Claude Code expects the hook to emit a raw replacement value on stdout
	// (e.g. an absolute directory path). Such events may only be registered
	// when their CLI command bypasses wipnote's JSON HookResult output layer.
	ReplacementOnRegistration
)

// hookEventContractSpec records the classification and a short contract note
// for a single Claude Code hook event name.
type hookEventContractSpec struct {
	// Classification describes how Claude Code processes the hook's stdout.
	Classification hookEventClassification
	// Note is a human-readable summary of the stdout contract.
	Note string
}

// hookEventContractSpecs is the authoritative table of every known Claude Code
// hook event name and its stdout contract classification.
//
// Source: https://code.claude.com/docs/en/hooks (verified 2026-05-29)
//
// When a new Claude event is registered in manifest.json, its entry MUST be
// added here. The test TestHookEventContracts enforces completeness.
var hookEventContractSpecs = map[string]hookEventContractSpec{

	// --- ReplacementOnRegistration ---

	// WorktreeCreate: CC expects a raw absolute path on stdout pointing to the
	// worktree directory the hook created. The worktree-create CLI command has
	// a dedicated bare-path output path and does not use the JSON hook emitter.
	"WorktreeCreate": {
		Classification: ReplacementOnRegistration,
		Note:           "CC expects a raw absolute path on stdout (the created worktree directory); handler must bypass JSON HookResult output.",
	},

	// --- AdditiveControlling ---
	// These events honour a JSON HookResult. Observe-only registration is safe.

	"PreToolUse": {
		Classification: AdditiveControlling,
		Note:           "CC reads JSON HookResult; decision:block is honoured. Safe to register observe-only (return {} or omit body).",
	},
	"PostToolUse": {
		Classification: AdditiveControlling,
		Note:           "CC reads JSON HookResult; additionalContext is injected into context. Safe to register observe-only.",
	},
	"UserPromptSubmit": {
		Classification: AdditiveControlling,
		Note:           "CC reads JSON HookResult; additionalContext is prepended to the user message. Safe to register observe-only.",
	},
	"Stop": {
		Classification: AdditiveControlling,
		Note:           "CC reads JSON HookResult; decision:block prevents session end. Safe to register observe-only.",
	},
	"SubagentStop": {
		Classification: AdditiveControlling,
		Note:           "CC reads JSON HookResult; decision:block prevents subagent stop. Safe to register observe-only.",
	},
	"PreCompact": {
		Classification: AdditiveControlling,
		Note:           "CC reads JSON HookResult; additionalContext is available. Safe to register observe-only.",
	},
	"TaskCreated": {
		Classification: AdditiveControlling,
		Note:           "CC reads JSON HookResult. Safe to register observe-only.",
	},
	"TaskCompleted": {
		Classification: AdditiveControlling,
		Note:           "CC reads JSON HookResult. Safe to register observe-only.",
	},
	"PermissionRequest": {
		Classification: AdditiveControlling,
		Note:           "CC reads JSON HookResult; decision:approve/deny controls permission outcome. Safe to register observe-only.",
	},
	"PostToolUseFailure": {
		Classification: AdditiveControlling,
		Note:           "CC reads JSON HookResult. Safe to register observe-only.",
	},
	"TeammateIdle": {
		Classification: AdditiveControlling,
		Note:           "CC reads JSON HookResult. Safe to register observe-only.",
	},

	// --- Observational ---
	// stdout is ignored entirely. Observe-only is the only correct registration.

	"SessionStart": {
		Classification: Observational,
		Note:           "stdout ignored; side-effects only (telemetry, setup).",
	},
	"SessionEnd": {
		Classification: Observational,
		Note:           "stdout ignored; side-effects only (telemetry, cleanup).",
	},
	"PostCompact": {
		Classification: Observational,
		Note:           "stdout ignored; side-effects only.",
	},
	"SubagentStart": {
		Classification: Observational,
		Note:           "stdout ignored; observe-only is the only correct use.",
	},
	"InstructionsLoaded": {
		Classification: Observational,
		Note:           "stdout ignored; observe-only is the only correct use.",
	},
	"ConfigChange": {
		Classification: Observational,
		Note:           "stdout ignored; observe-only is the only correct use.",
	},
	"WorktreeRemove": {
		Classification: Observational,
		Note:           "stdout ignored; side-effects only (cleanup after worktree removal).",
	},
	"AfterAgent": {
		Classification: Observational,
		Note:           "stdout ignored; observe-only is the only correct use.",
	},
	"AfterModel": {
		Classification: Observational,
		Note:           "stdout ignored; observe-only is the only correct use.",
	},
}

// claudeHookEventNames returns the deduplicated set of event names registered
// for the Claude target in the given manifest.
func claudeHookEventNames(m *Manifest) []string {
	seen := make(map[string]struct{})
	var names []string
	for _, e := range m.Hooks.Events {
		if !e.AppliesTo("claude") {
			continue
		}
		if _, ok := seen[e.Name]; ok {
			continue
		}
		seen[e.Name] = struct{}{}
		names = append(names, e.Name)
	}
	return names
}

// checkHookEventContracts validates that every Claude-registered event in m
// has a spec in hookEventContractSpecs (completeness) and that any
// ReplacementOnRegistration registration uses a dedicated raw-output handler.
//
// Returns a slice of violation strings; nil means all checks pass.
func checkHookEventContracts(m *Manifest) []string {
	var violations []string
	for _, name := range claudeHookEventNames(m) {
		spec, ok := hookEventContractSpecs[name]
		if !ok {
			violations = append(violations, fmt.Sprintf(
				"hook event %q is registered for claude but has no entry in hookEventContractSpecs; "+
					"add it with an appropriate classification before shipping",
				name,
			))
			continue
		}
		if spec.Classification == ReplacementOnRegistration && name != "WorktreeCreate" {
			violations = append(violations, fmt.Sprintf(
				"hook event %q is classified ReplacementOnRegistration and MUST NOT be registered for claude: "+
					"wipnote hooks emit JSON HookResult and cannot fulfill CC's raw-value replacement contract; "+
					"do not register %s (see bug-80b27913 / WorktreeCreate)",
				name, name,
			))
		}
	}
	return violations
}
