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
	// Shell-quote the executable path: agy runs the statusLine command through a
	// shell, so a path containing spaces would otherwise be split into multiple
	// argv words and fail with ENOENT. shellQuote leaves space-free paths bare.
	return shellQuote(exe) + " statusline --cache"
}

// isWipnoteStatusLineCommand reports whether cmd is a wipnote-managed statusline
// command (so re-launch can refresh the binary path without clobbering a
// user-authored custom statusLine). It matches the exact launcher-owned shape —
// a wipnote executable (bare name, or a path/quoted-path whose basename is
// wipnote*) followed by exactly "statusline --cache" — so a different tool that
// happens to expose a "statusline --cache" command is not misclassified as ours
// (CLAUDE.md "Hook State": anchor to the specific command shape).
func isWipnoteStatusLineCommand(cmd string) bool {
	rest, ok := strings.CutSuffix(strings.TrimSpace(cmd), "statusline --cache")
	if !ok {
		return false
	}
	exe := strings.TrimSpace(rest)
	// Strip the single quotes shellQuote adds around paths with spaces.
	if len(exe) >= 2 && exe[0] == '\'' && exe[len(exe)-1] == '\'' {
		exe = exe[1 : len(exe)-1]
	}
	if exe == "" {
		return false
	}
	// Match only the concrete launcher-generated basenames, not a broad
	// "wipnote*" prefix — otherwise a user tool like "wipnote-theme" with a
	// "statusline --cache" command would be wrongly claimed and clobbered.
	switch filepath.Base(exe) {
	case "wipnote", "wipnote.exe", "wipnote-dev", "wipnote.test":
		return true
	default:
		return false
	}
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
		// A literal `null` (or a non-object top-level value) unmarshals into a
		// nil map; re-init so the later statusLine assignment can't panic.
		if settings == nil {
			settings = map[string]any{}
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
	// Atomic write (temp file + rename, via the shared helper) so a concurrent
	// agy read or a parallel `wipnote antigravity` launch never observes a torn
	// settings.json. MkdirAll is handled inside atomicWriteFile.
	if err := atomicWriteFile(path, out, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write agy settings.json; status line not configured: %v\n", err)
		return
	}

	// This only runs when the statusLine was absent or its path changed (the
	// merge is a no-op otherwise), so it is not per-launch noise. Surface the
	// one global-config mutation so it is not silent and is easy to opt out of.
	fmt.Fprintf(os.Stderr,
		"wipnote: configured agy status line in %s (active work item; set WIPNOTE_ANTIGRAVITY_STATUSLINE=0 to disable)\n",
		path)
}
