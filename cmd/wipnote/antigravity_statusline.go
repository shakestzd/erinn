package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// antigravitySettingsPath returns the per-user Antigravity CLI settings file.
// agy reads its statusLine config from this file (verified live against agy
// v1.0.8: settings key "statusLine"; statusline_runner; /statusline command).
func antigravitySettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
}

// antigravityStatusLineCommand returns the shell command agy should run to
// render the wipnote status line. It points at this wipnote binary's
// `statusline --cache` mode, which renders the project-scoped active work item
// without depending on a wipnote-keyed session (agy hooks do not reliably key
// to wipnote sessions). Falls back to the bare command name when the absolute
// executable path cannot be resolved.
func antigravityStatusLineCommand() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return "wipnote statusline --cache"
	}
	return exe + " statusline --cache"
}

// isWipnoteStatusLineCommand reports whether cmd is a wipnote-managed statusline
// command (so re-launch can refresh the binary path without clobbering a
// user-authored custom statusLine).
func isWipnoteStatusLineCommand(cmd string) bool {
	return cmd != "" && (strings.Contains(cmd, "statusline --cache") || strings.Contains(cmd, "wipnote statusline"))
}

// mergeAntigravityStatusLine sets a wipnote-managed statusLine into the given
// settings map and reports whether the map changed. It is non-clobbering: if a
// statusLine already exists and is NOT wipnote-managed (a user's custom
// command), the map is left untouched. A wipnote-managed entry is refreshed in
// place (e.g. when the binary path changes) and reported as changed only if the
// command string actually differs.
func mergeAntigravityStatusLine(settings map[string]any, command string) bool {
	desired := map[string]any{
		"type":    "command",
		"command": command,
		"padding": 0,
	}

	existing, ok := settings["statusLine"].(map[string]any)
	if ok {
		cur, _ := existing["command"].(string)
		if !isWipnoteStatusLineCommand(cur) {
			// User-authored statusLine — never clobber.
			return false
		}
		if cur == command {
			return false // already current
		}
	}
	settings["statusLine"] = desired
	return true
}

// ensureAntigravityStatusLine merges a wipnote statusLine command into the
// per-user agy settings.json so the active work item is surfaced in agy's
// status line. It is best-effort and non-fatal: any error is reported to stderr
// and the launch proceeds. Skipped when WIPNOTE_ANTIGRAVITY_STATUSLINE is
// explicitly disabled. agy auto-disables a failing statusline command, so a
// stale or mis-piped command degrades gracefully rather than breaking launch.
func ensureAntigravityStatusLine(dryRun bool) {
	if isExplicitlyDisabled(os.Getenv("WIPNOTE_ANTIGRAVITY_STATUSLINE")) {
		return
	}
	path := antigravitySettingsPath()
	if path == "" {
		return
	}

	settings := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, &settings); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not parse agy settings.json; skipping statusline setup: %v\n", err)
			return
		}
	}

	if !mergeAntigravityStatusLine(settings, antigravityStatusLineCommand()) {
		return // already configured or user-customized
	}

	if dryRun {
		fmt.Printf("[dry-run] would set agy statusLine.command in %s\n", path)
		return
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not encode agy settings.json: %v\n", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not create agy settings dir: %v\n", err)
		return
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write agy settings.json: %v\n", err)
		return
	}
}
