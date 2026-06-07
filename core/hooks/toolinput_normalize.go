package hooks

import (
	"encoding/json"
	"path/filepath"

	"github.com/shakestzd/wipnote/core/paths"
)

// pathKeysToNormalize lists the tool_input keys that may contain file-system
// paths and should be stored repo-relative. "command" (Bash) and "pattern"
// (Grep) are deliberately absent — commands and regex search patterns often
// contain absolute-path-like substrings and must be preserved verbatim for
// debugging fidelity.
var pathKeysToNormalize = []string{
	"file_path",
	"notebook_path",
	"path",
}

// normalizeToolInputPaths returns a JSON string of the tool_input map with
// absolute file-system paths in known keys replaced by repo-relative values.
//
// Rules:
//   - nil input → return "".
//   - "command" key (Bash) is never mutated.
//   - Each key in pathKeysToNormalize whose value is a non-empty string is
//     passed through paths.NormalizeWithResolver (or paths.MustNormalize when
//     resolver is nil). Already-relative values pass through unchanged.
//   - "pattern" (Grep) is left untouched: it is a search pattern, not a path,
//     and normalising it would corrupt the captured query (e.g. searching
//     for "/workspaces/myrepo/internal" must be preserved verbatim).
//   - Marshal/unmarshal errors cause the caller to fall back to the original
//     raw tool_input JSON (caller responsibility).
//
// The resolver parameter is the test-injectable form of the anchor resolver;
// pass nil to use the production resolver (paths.MustNormalize).
func normalizeToolInputPaths(input map[string]any, _, repoRoot string, resolver func(string) string) string {
	if input == nil {
		return ""
	}

	// Shallow-clone so we never mutate the caller's map.
	cloned := make(map[string]any, len(input))
	for k, v := range input {
		cloned[k] = v
	}

	for _, key := range pathKeysToNormalize {
		v, ok := cloned[key].(string)
		if !ok || v == "" {
			continue
		}
		// Only normalize absolute paths; relative values pass through unchanged.
		if !filepath.IsAbs(v) {
			continue
		}
		var normalized string
		if resolver != nil {
			normalized, _ = paths.NormalizeWithResolver(v, repoRoot, resolver)
		} else {
			normalized = paths.MustNormalize(v, repoRoot)
		}
		if normalized != "" {
			cloned[key] = normalized
		}
	}

	b, err := json.Marshal(cloned)
	if err != nil {
		// Marshal should never fail on a map[string]any that was just
		// successfully unmarshaled, but be safe.
		return ""
	}
	return string(b)
}

// normalizeToolInputJSON is the production entry point used by recordEventAndAllow.
// It accepts the already-marshaled toolInputStr and re-normalizes it, returning
// the original string on any failure so capture is never lost.
func normalizeToolInputJSON(toolInputStr, toolName string) string {
	if toolInputStr == "" {
		return toolInputStr
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(toolInputStr), &input); err != nil {
		// Malformed JSON — pass through unchanged.
		debugLog("", "[wipnote] normalizeToolInputJSON: unmarshal failed for %s: %v", toolName, err)
		return toolInputStr
	}
	result := normalizeToolInputPaths(input, toolName, "", nil)
	if result == "" {
		return toolInputStr
	}
	return result
}
