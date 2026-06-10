package pluginbuild

import (
	"path/filepath"
	"strings"
)

// Register hooks.json emission as a Gemini sub-emitter. Gemini's hook schema
// mirrors Claude's (same `{"hooks": {"<Event>": [...]}}` shape), so we reuse
// the orderedHookMap + claudeMatcherGroup + claudeHookEntry types declared in
// claude.go and only change the target filter passed to AppliesTo.
func init() { geminiSubEmitters = append(geminiSubEmitters, emitGeminiHooks) }

// emitGeminiHooks writes <outDir>/<t.HooksPath> using the Gemini-filtered
// subset of m.Hooks.Events. Declaration order from the manifest is preserved
// so matcher groups for the same event name stay adjacent. When no events
// are tagged for Gemini, the file is not emitted — this keeps skeleton-only
// builds (pre-Phase-3 fixtures, tests) from writing stub hooks.json files.
//
// Translation rules applied per event:
//   - Event name: use e.GeminiEventName when set, fall back to e.Name.
//   - Command var: $GEMINI_EXTENSION_DIR → ${extensionPath}.
//   - Matcher: empty string → "*" (Gemini requires an explicit wildcard).
func emitGeminiHooks(m *Manifest, repoRoot, outDir string, t Target) error {
	hooks := map[string][]claudeMatcherGroup{}
	order := []string{}

	for _, e := range m.Hooks.Events {
		if !e.AppliesTo("gemini") {
			continue
		}

		// Resolve the Gemini event name: prefer the explicit override, fall back
		// to the canonical Claude event name.
		eventName := e.GeminiEventName
		if eventName == "" {
			eventName = e.Name
		}

		cmd := e.Command
		if cmd == "" {
			// Use GeminiHandler when set, fall back to Handler.
			handler := e.Handler
			if e.GeminiHandler != "" {
				handler = e.GeminiHandler
			}
			cmd = "wipnote hook " + handler
		}
		// Variable substitution: Gemini exposes the extension directory as
		// ${extensionPath}, not $GEMINI_EXTENSION_DIR.
		cmd = strings.ReplaceAll(cmd, "$GEMINI_EXTENSION_DIR", "${extensionPath}")

		// Matcher: Gemini requires an explicit wildcard ("*") where Claude uses
		// an empty string to mean "match all".
		matcher := e.Matcher
		if matcher == "" {
			matcher = "*"
		}

		group := claudeMatcherGroup{
			Matcher: matcher,
			Hooks: []claudeHookEntry{{
				Type:    "command",
				Command: cmd,
				Timeout: e.Timeout,
			}},
		}
		if _, seen := hooks[eventName]; !seen {
			order = append(order, eventName)
		}
		hooks[eventName] = append(hooks[eventName], group)
	}

	if len(order) == 0 {
		return nil
	}
	return writeJSON(filepath.Join(outDir, t.HooksPath), orderedHookMap{keys: order, values: hooks})
}
