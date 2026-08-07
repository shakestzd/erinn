package pluginbuild

import (
	"fmt"
	"sort"
	"strings"
)

// hookEventNameSpec records one canonical hook event name and the harnesses
// whose event vocabulary actually contains it.
//
// This table answers a single question: "does harness H dispatch an event that
// this manifest registration can bind to?" It is deliberately separate from
// hookEventContractSpecs, which answers a different one ("how does Claude Code
// process this hook's stdout?").
//
// Name is the canonical name written in manifest.json. Harnesses maps a target
// name to the event name that target actually dispatches — identical to Name
// for Claude and Codex, translated for Antigravity (UserPromptSubmit ->
// PreInvocation). A canonical name with an empty Harnesses map is *recognized*
// but registerable nowhere; that distinction produces a more specific build
// error than an outright unknown name (same two-tier scheme as
// filterAgentFrontmatter's "not recognized in shared source" vs "unsupported
// for this harness").
//
// WHY THIS EXISTS: no harness validates hook event names. Codex silently drops
// unrecognized names at dispatch — no warning, no error. Antigravity's own
// generator (writeAntigravityHooks) likewise skips names it cannot translate. A
// typo or a name invented from an unverified source therefore produces a hook
// that never fires, forever, with no signal. This table plus checkHookEventNames
// is the only place that failure can be caught.
//
// THIS LIST IS HAND-MAINTAINED AND CANNOT BE GENERATED. Do not go looking for
// the automated version; it was searched for and does not exist. Against Codex
// 0.147.0, all three plausible backstops were tested and all three came back
// empty:
//
//   - `codex doctor` has no hooks diagnostic of any kind.
//   - Unrecognized event names in hooks.json are dropped at dispatch, silently.
//   - `--strict-config` does NOT validate hook event names, and in fact does not
//     behave as its own help text claims for unknown TOML keys supplied via
//     file. Tested with an isolated CODEX_HOME and a config.toml carrying both
//     `[hooks.events] TaskStarted = true` and a control nonsense top-level key
//     (`totally_bogus_top_level_key_xyz = true`), run through
//     `codex --strict-config doctor` and `codex --strict-config exec`. Neither
//     key produced an error — config parsed clean and exec proceeded as far as
//     real API calls before failing on auth, i.e. validation was long past.
//
// There is consequently no programmatic way to make Codex validate or enumerate
// its hook event names. This table does not get to be self-maintaining: it needs
// the same per-release re-verification discipline as the rest of the
// upstream-harness monitoring described in CLAUDE.md ("Monitoring Upstream
// Harnesses"). Re-verify on a cadence, not on suspicion — the failure mode is
// silent, so nothing will prompt you.
//
// CAUTIONARY TALE: the phantom registrations this gate was built to catch
// (TaskStarted/TaskComplete/TurnAborted, bug-e39d408f) most likely originated in
// someone reading Codex CLOUD's task-tracking vocabulary (`codex cloud`, whose
// RawRecord schema carries task_id/task_subject) as if it were local hook
// vocabulary. Two different Codex surfaces, two different vocabularies. When
// adding an entry, confirm the name appears in the local hook dispatch path
// specifically — plausibility is not provenance.
type hookEventNameSpec struct {
	// Name is the canonical event name as written in manifest.json.
	Name string
	// Harnesses maps target name -> the event name that target dispatches.
	Harnesses map[string]string
	// DocURL cites the upstream documentation for the harness vocabulary.
	DocURL string
	// Provenance records how the entry was verified. Required for every spec.
	Provenance string
}

// Verified vocabularies, by harness:
//
//	claude      — https://code.claude.com/docs/en/hooks (verified 2026-05-29,
//	              same audit that produced hookEventContractSpecs).
//	codex       — Codex CLI 0.147.0, verified three independent ways
//	              (2026-08-07, trk-1db7bf72): the HookEventNameWire enum and 11
//	              embedded JSON schemas extracted from the Rust binary; a live
//	              black-box ~/.codex/hooks.json dispatch test; and
//	              learn.chatgpt.com/docs/hooks. Exactly 11 events, no more.
//	antigravity — Antigravity CLI (agy) v1.0.8, verified live (feat-c08b20a6):
//	              only five names register a command handler.
//
// EXTENSION POINT: adding a harness means adding its target to knownHookHarnesses
// and adding that harness to the Harnesses map of every event it dispatches.
// Never add a harness to an entry from an unverified source — an unverifiable
// name is exactly the failure this table exists to prevent, and omitting it
// produces a loud build error rather than a silent dead hook.
var hookEventNameSpecs = []hookEventNameSpec{
	{
		Name:       "SessionStart",
		DocURL:     "https://code.claude.com/docs/en/hooks",
		Provenance: "Claude Code hooks doc; Codex 0.147.0 HookEventNameWire. agy v1.0.8 has no working command-hook for session start.",
		Harnesses:  map[string]string{"claude": "SessionStart", "codex": "SessionStart"},
	},
	{
		Name:       "SessionEnd",
		DocURL:     "https://code.claude.com/docs/en/hooks",
		Provenance: "Claude Code hooks doc; Codex 0.147.0 HookEventNameWire.",
		Harnesses:  map[string]string{"claude": "SessionEnd", "codex": "SessionEnd"},
	},
	{
		Name:       "UserPromptSubmit",
		DocURL:     "https://code.claude.com/docs/en/hooks",
		Provenance: "Claude Code hooks doc; Codex 0.147.0 HookEventNameWire; agy v1.0.8 dispatches this as PreInvocation (start of each agent invocation).",
		Harnesses:  map[string]string{"claude": "UserPromptSubmit", "codex": "UserPromptSubmit", "antigravity": "PreInvocation"},
	},
	{
		Name:       "PreToolUse",
		DocURL:     "https://code.claude.com/docs/en/hooks",
		Provenance: "Claude Code hooks doc; Codex 0.147.0 HookEventNameWire; agy v1.0.8 uses the same name.",
		Harnesses:  map[string]string{"claude": "PreToolUse", "codex": "PreToolUse", "antigravity": "PreToolUse"},
	},
	{
		Name:       "PostToolUse",
		DocURL:     "https://code.claude.com/docs/en/hooks",
		Provenance: "Claude Code hooks doc; Codex 0.147.0 HookEventNameWire; agy v1.0.8 uses the same name.",
		Harnesses:  map[string]string{"claude": "PostToolUse", "codex": "PostToolUse", "antigravity": "PostToolUse"},
	},
	{
		Name:       "Stop",
		DocURL:     "https://code.claude.com/docs/en/hooks",
		Provenance: "Claude Code hooks doc; Codex 0.147.0 HookEventNameWire; agy v1.0.8 uses the same name.",
		Harnesses:  map[string]string{"claude": "Stop", "codex": "Stop", "antigravity": "Stop"},
	},
	{
		Name:       "SubagentStart",
		DocURL:     "https://code.claude.com/docs/en/hooks",
		Provenance: "Claude Code hooks doc; Codex 0.147.0 HookEventNameWire.",
		Harnesses:  map[string]string{"claude": "SubagentStart", "codex": "SubagentStart"},
	},
	{
		Name:       "SubagentStop",
		DocURL:     "https://code.claude.com/docs/en/hooks",
		Provenance: "Claude Code hooks doc; Codex 0.147.0 HookEventNameWire.",
		Harnesses:  map[string]string{"claude": "SubagentStop", "codex": "SubagentStop"},
	},
	{
		Name:       "PermissionRequest",
		DocURL:     "https://code.claude.com/docs/en/hooks",
		Provenance: "Claude Code hooks doc; Codex 0.147.0 HookEventNameWire.",
		Harnesses:  map[string]string{"claude": "PermissionRequest", "codex": "PermissionRequest"},
	},
	{
		Name:       "PreCompact",
		DocURL:     "https://code.claude.com/docs/en/hooks",
		Provenance: "Claude Code hooks doc; Codex 0.147.0 HookEventNameWire.",
		Harnesses:  map[string]string{"claude": "PreCompact", "codex": "PreCompact"},
	},
	{
		Name:       "PostCompact",
		DocURL:     "https://code.claude.com/docs/en/hooks",
		Provenance: "Claude Code hooks doc; Codex 0.147.0 HookEventNameWire.",
		Harnesses:  map[string]string{"claude": "PostCompact", "codex": "PostCompact"},
	},
	{
		Name:       "PostToolUseFailure",
		DocURL:     "https://code.claude.com/docs/en/hooks",
		Provenance: "Claude Code hooks doc. Absent from Codex 0.147.0's 11-event enum and from agy v1.0.8.",
		Harnesses:  map[string]string{"claude": "PostToolUseFailure"},
	},
	{
		Name:       "TeammateIdle",
		DocURL:     "https://code.claude.com/docs/en/hooks",
		Provenance: "Claude Code agent-teams hook. Absent from Codex 0.147.0 and agy v1.0.8.",
		Harnesses:  map[string]string{"claude": "TeammateIdle"},
	},
	{
		Name:       "TaskCreated",
		DocURL:     "https://code.claude.com/docs/en/hooks",
		Provenance: "Claude Code task-board hook. Absent from Codex 0.147.0 and agy v1.0.8.",
		Harnesses:  map[string]string{"claude": "TaskCreated"},
	},
	{
		Name:       "TaskCompleted",
		DocURL:     "https://code.claude.com/docs/en/hooks",
		Provenance: "Claude Code task-board hook. Absent from Codex 0.147.0 and agy v1.0.8.",
		Harnesses:  map[string]string{"claude": "TaskCompleted"},
	},
	{
		Name:       "InstructionsLoaded",
		DocURL:     "https://code.claude.com/docs/en/hooks",
		Provenance: "Claude Code hooks doc. Absent from Codex 0.147.0 and agy v1.0.8.",
		Harnesses:  map[string]string{"claude": "InstructionsLoaded"},
	},
	{
		Name:       "ConfigChange",
		DocURL:     "https://code.claude.com/docs/en/hooks",
		Provenance: "Claude Code hooks doc. Absent from Codex 0.147.0 and agy v1.0.8.",
		Harnesses:  map[string]string{"claude": "ConfigChange"},
	},
	{
		Name:       "WorktreeCreate",
		DocURL:     "https://code.claude.com/docs/en/hooks#worktreecreate",
		Provenance: "Claude Code worktree replacement hook; see hookEventContractSpecs for its raw-stdout contract. Absent from Codex 0.147.0 and agy v1.0.8.",
		Harnesses:  map[string]string{"claude": "WorktreeCreate"},
	},
	{
		Name:       "WorktreeRemove",
		DocURL:     "https://code.claude.com/docs/en/hooks",
		Provenance: "Claude Code hooks doc. Absent from Codex 0.147.0 and agy v1.0.8.",
		Harnesses:  map[string]string{"claude": "WorktreeRemove"},
	},
	{
		Name:       "AfterAgent",
		DocURL:     "https://github.com/google-gemini/gemini-cli",
		Provenance: "Gemini-CLI-era name retained as agy's canonical spelling for end-of-invocation; agy v1.0.8 dispatches it as PostInvocation. Not a Claude or Codex event.",
		Harnesses:  map[string]string{"antigravity": "PostInvocation"},
	},
	{
		Name:       "AfterModel",
		DocURL:     "https://github.com/google-gemini/gemini-cli",
		Provenance: "Gemini CLI name with no agy v1.0.8 equivalent and no Claude/Codex counterpart. Recognized so a registration attempt fails with a specific message rather than being read as a typo; `wipnote hook after-model` remains callable directly.",
	},
}

// knownHookHarnesses is the set of targets whose event vocabulary this table
// can validate. A manifest registration naming a target outside this set cannot
// be checked and is rejected — an unvalidatable registration is the same silent
// no-op the gate exists to prevent.
var knownHookHarnesses = map[string]struct{}{
	"claude":      {},
	"codex":       {},
	"antigravity": {},
}

var (
	// hookEventNameIndex maps canonical event name -> spec.
	hookEventNameIndex = indexHookEventNameSpecs(hookEventNameSpecs)
	// harnessHookEventNames maps harness -> canonical name -> dispatched name.
	harnessHookEventNames = hookEventNameAllowlists(hookEventNameSpecs)
)

func indexHookEventNameSpecs(specs []hookEventNameSpec) map[string]hookEventNameSpec {
	out := make(map[string]hookEventNameSpec, len(specs))
	for _, spec := range specs {
		out[spec.Name] = spec
	}
	return out
}

func hookEventNameAllowlists(specs []hookEventNameSpec) map[string]map[string]string {
	out := make(map[string]map[string]string, len(knownHookHarnesses))
	for harness := range knownHookHarnesses {
		out[harness] = map[string]string{}
	}
	for _, spec := range specs {
		for harness, native := range spec.Harnesses {
			if _, ok := out[harness]; !ok {
				out[harness] = map[string]string{}
			}
			out[harness][spec.Name] = native
		}
	}
	return out
}

// hookEventNamesForHarness lists the canonical names valid for harness, sorted.
// Used in build-error messages so the fix is visible without opening this file.
func hookEventNamesForHarness(harness string) []string {
	allow := harnessHookEventNames[harness]
	names := make([]string, 0, len(allow))
	for name := range allow {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// checkHookEventNames validates every (event, target) pair in m against the
// per-harness vocabulary. It returns one violation string per bad pair, in
// manifest order; nil means the manifest is clean.
//
// This is a HARD gate, wired into Manifest.validate so `wipnote plugin
// build-ports` and `check-ports` refuse to run rather than emitting a tree with
// a hook that can never fire. A warning would be the wrong severity: the whole
// defect class is that a wrong name already behaves exactly like a warning
// nobody reads.
func checkHookEventNames(m *Manifest) []string {
	var violations []string
	for i, e := range m.Hooks.Events {
		for _, target := range e.Targets {
			if _, known := knownHookHarnesses[target]; !known {
				violations = append(violations, fmt.Sprintf(
					"hooks.events[%d] (%s/%s): target %q has no hook event vocabulary in hookEventNameSpecs; "+
						"add it to knownHookHarnesses with a verified event list before registering hooks for it",
					i, e.Name, e.Handler, target,
				))
				continue
			}
			spec, ok := hookEventNameIndex[e.Name]
			if !ok {
				violations = append(violations, fmt.Sprintf(
					"hooks.events[%d]: event name %q is not a recognized hook event for any harness; "+
						"%s does not dispatch it, so the %q handler would never run. "+
						"Fix the name, or add a verified entry to hookEventNameSpecs in port/pluginbuild/hook_event_names.go",
					i, e.Name, target, e.Handler,
				))
				continue
			}
			if _, valid := spec.Harnesses[target]; !valid {
				violations = append(violations, fmt.Sprintf(
					"hooks.events[%d]: event %q is not dispatched by %s, so the %q handler would never run. "+
						"%s events: %s",
					i, e.Name, target, e.Handler,
					target, strings.Join(hookEventNamesForHarness(target), ", "),
				))
			}
		}
	}
	return violations
}
