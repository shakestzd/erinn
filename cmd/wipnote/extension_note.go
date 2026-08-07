package main

import "fmt"

// replacedExtensionNote returns a trailing note for post-uninstall failure
// errors, telling the user the previous extension install was already removed
// and how to reinstall. Returns "" when no replacement happened so callers can
// append it unconditionally. Used by the Antigravity --init path (and formerly
// the retired Gemini --init path); its success-only setup banner would otherwise
// hide the removal on failure (unit-tested; roborev 568).
func replacedExtensionNote(replaced bool, harness, installDir, reinstallCmd string) string {
	if !replaced {
		return ""
	}
	return fmt.Sprintf("\n(the previous wipnote %s extension at %s was already removed; reinstall with: %s)", harness, installDir, reinstallCmd)
}
