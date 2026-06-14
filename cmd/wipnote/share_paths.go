package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// resolveSharedTreePath resolves the on-disk path to a bundled harness tree
// laid down by Phase A of the marketplace-to-bundled-plugin migration.
//
// Resolution order:
//  1. Per-tree env var override (WIPNOTE_PLUGIN_DIR / WIPNOTE_CODEX_DIR /
//     WIPNOTE_GEMINI_DIR) — for advanced users and tests; usually unset.
//  2. Standard local install: ~/.local/share/wipnote/<treeName>/ — used by
//     `wipnote build` (dev mirror) and the curl-install script.
//  3. Homebrew install: <prefix>/share/wipnote/<treeName>/, discovered by
//     walking up two parents from the running binary (bin/wipnote ->
//     prefix/share/wipnote/<treeName>).
//  4. Dev fallback: walk up from CWD looking for go.mod; if found, return
//     <project-root>/<sourceSubpath>. This makes `wipnote claude` Just Work
//     in a checked-out wipnote repo without explicit --dev.
//
// treeName is one of "plugin", "codex-marketplace", or "gemini-extension".
// Returns an absolute path on success, or a clear error if nothing is found.
func sharedTreeRefreshHint() string {
	ver := strings.TrimPrefix(version, "v")
	if isReleaseSemver(ver) {
		return fmt.Sprintf("Refresh bundled assets with 'wipnote upgrade --version %s' or install via 'brew install wipnote'.", ver)
	}
	return "Refresh bundled assets with 'wipnote upgrade' or install via 'brew install wipnote'."
}

func isReleaseSemver(ver string) bool {
	parts := strings.Split(ver, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		if _, err := strconv.Atoi(part); err != nil {
			return false
		}
	}
	return true
}

func resolveSharedTreePath(treeName string) (string, error) {
	envVar, sourceSubpath, ok := sharedTreeMetadata(treeName)
	if !ok {
		return "", fmt.Errorf("unknown harness tree %q", treeName)
	}

	// 1. Env var override.
	if override := os.Getenv(envVar); override != "" {
		if isValidHarnessTree(override, treeName) {
			debugLog(fmt.Sprintf("resolveSharedTreePath(%s): using env override %s", treeName, override))
			return override, nil
		}
		return "", fmt.Errorf("%s=%s is set but does not contain a valid %s tree", envVar, override, treeName)
	}

	// 2. ~/.local/share/wipnote/<treeName>/
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, ".local", "share", "wipnote", treeName)
		if isValidHarnessTree(candidate, treeName) {
			debugLog(fmt.Sprintf("resolveSharedTreePath(%s): using local install %s", treeName, candidate))
			return candidate, nil
		}
	}

	// 3. Homebrew install: <prefix>/share/wipnote/<treeName>/.
	//    The running binary lives at <prefix>/bin/wipnote on Homebrew (it is a
	//    symlink into the Cellar, but for the purposes of finding the sibling
	//    share/ dir, the symlink's parent is the bin dir we want).
	if exe, err := os.Executable(); err == nil {
		// Don't EvalSymlinks — the symlink at <prefix>/bin/wipnote is the
		// anchor for finding <prefix>/share/wipnote/. Following the symlink
		// lands inside the Cellar, which has no share/ sibling.
		binDir := filepath.Dir(exe)
		prefix := filepath.Dir(binDir)
		candidate := filepath.Join(prefix, "share", "wipnote", treeName)
		if isValidHarnessTree(candidate, treeName) {
			debugLog(fmt.Sprintf("resolveSharedTreePath(%s): using brew install %s", treeName, candidate))
			return candidate, nil
		}
	}

	// 4. Dev fallback: walk up from CWD looking for go.mod.
	if devPath, err := devSourceTreePath(sourceSubpath); err == nil {
		if isValidHarnessTree(devPath, treeName) {
			debugLog(fmt.Sprintf("resolveSharedTreePath(%s): using dev source %s", treeName, devPath))
			return devPath, nil
		}
	}

	return "", fmt.Errorf(
		"wipnote %s tree not found.\n"+
			"  %s\n"+
			"  Developers can run 'wipnote build' from the wipnote source checkout.\n"+
			"  Override with %s=<path> if installed in a non-standard location.",
		treeName, sharedTreeRefreshHint(), envVar,
	)
}

// sharedTreeMetadata returns the env var name and source-tree subpath for a
// given harness tree. (envVar, sourceSubpath, ok).
func sharedTreeMetadata(treeName string) (string, string, bool) {
	switch treeName {
	case "plugin":
		return "WIPNOTE_PLUGIN_DIR", "plugin", true
	case "codex-marketplace":
		return "WIPNOTE_CODEX_DIR", filepath.Join("port", "packages", "codex-marketplace"), true
	case "gemini-extension":
		return "WIPNOTE_GEMINI_DIR", filepath.Join("port", "packages", "gemini-extension"), true
	case "antigravity-extension":
		return "WIPNOTE_ANTIGRAVITY_DIR", filepath.Join("port", "packages", "antigravity-extension"), true
	default:
		return "", "", false
	}
}

// isValidHarnessTree returns true when path contains the expected manifest
// for the given tree. Cheap sanity check so we don't hand the harness CLI a
// stale empty directory.
func isValidHarnessTree(path, treeName string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	var sentinels []string
	switch treeName {
	case "plugin":
		sentinels = []string{filepath.Join(".claude-plugin", "plugin.json")}
	case "codex-marketplace":
		sentinels = []string{
			"marketplace.json", // bundled tarball (GoReleaser flat layout)
			filepath.Join(".agents", "plugins", "marketplace.json"), // dev source (deep layout)
		}
	case "gemini-extension":
		sentinels = []string{"gemini-extension.json"}
	case "antigravity-extension":
		sentinels = []string{"plugin.json"}
	default:
		return false
	}
	for _, s := range sentinels {
		if _, err := os.Stat(filepath.Join(path, s)); err == nil {
			return true
		}
	}
	return false
}

// devSourceTreePath walks up from CWD looking for a go.mod that identifies
// the wipnote project root, and returns <projectRoot>/<sourceSubpath>.
// Returns an error if no project root is found within a reasonable depth.
func devSourceTreePath(sourceSubpath string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for range 8 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, sourceSubpath), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("dev source tree not found (no go.mod walking up from %s)", cwd)
}
