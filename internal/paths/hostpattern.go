package paths

import "regexp"

// HostPathPattern matches absolute paths that are specific to a developer's
// machine and therefore must not appear in committed work-item artifacts or
// in stored CloudEvent payloads:
//
//   - /Users/<name>/        — macOS home directories
//   - /home/<name>/         — Linux home directories
//   - /workspaces/<name>/   — GitHub Codespaces per-user workspace paths
//   - /private/var/folders/ — macOS temp directory (always machine-specific)
//
// /home/runner/ (GitHub Actions CI) is allowed via a separate filter in the
// precommit gate (see cmd/wipnote/check_host_paths.go: ciAllowPattern).
//
// This pattern is the single source of truth shared by:
//   - the precommit gate (cmd/wipnote/check_host_paths.go), and
//   - the runtime normalizer (NormalizeToRepoRelative) which marks
//     outside-repo absolute paths as "unresolved:" so the downstream
//     migration rewriter can repair them later.
//   - the render-layer sanitizer (SanitizeHostPaths) which scrubs paths
//     from user-supplied text before they are written into work-item HTML.
//
// Any future expansion (e.g. /Volumes/, C:\Users\) must be reflected in BOTH
// hostpattern_test.go and the precommit-gate tests.
var HostPathPattern = regexp.MustCompile(
	`/Users/[^/\s]+/` +
		`|/home/[^/\s]+/` +
		`|/workspaces/[^/\s]+/` +
		`|/private/var/folders/`,
)

// SanitizeHostPaths replaces host-local absolute path prefixes in s with a
// portable redaction token so they never reach committed work-item HTML.
// Only the machine-specific prefix (matched by HostPathPattern) is replaced;
// the remainder of the path component is preserved for readability.
//
// Example: "/workspaces/wipnote/foo.go" → "[host-path]/foo.go"
//
// This is the render-layer fix for bug-ff6a3286: sanitize at write time so
// the precommit gate remains a narrow safety net rather than a broad allowlist.
func SanitizeHostPaths(s string) string {
	return HostPathPattern.ReplaceAllString(s, "[host-path]/")
}
