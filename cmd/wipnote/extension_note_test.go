package main

import (
	"strings"
	"testing"
)

// TestReplacedExtensionNote covers the post-uninstall failure note shared by the
// gemini/antigravity --init paths (roborev 568). The success-only setup banner
// would otherwise hide that the previous install was removed, so the note must
// appear in failure errors when (and only when) a replacement occurred.
func TestReplacedExtensionNote(t *testing.T) {
	// No replacement → empty note so callers can append unconditionally.
	if got := replacedExtensionNote(false, "Gemini", "/x", "wipnote gemini --init --force"); got != "" {
		t.Errorf("expected empty note when not replaced, got %q", got)
	}

	got := replacedExtensionNote(true, "Gemini", "/home/u/.gemini/extensions/wipnote", "wipnote gemini --init --force")
	if !strings.HasPrefix(got, "\n") {
		t.Errorf("note should start on its own line, got %q", got)
	}
	for _, want := range []string{
		"Gemini",
		"/home/u/.gemini/extensions/wipnote",
		"already removed",
		"wipnote gemini --init --force",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("note missing %q; got %q", want, got)
		}
	}

	// Harness + reinstall command are parameterized (antigravity reuses it).
	agy := replacedExtensionNote(true, "Antigravity", "/p", "wipnote antigravity --init --force")
	if !strings.Contains(agy, "Antigravity") || !strings.Contains(agy, "wipnote antigravity --init --force") {
		t.Errorf("antigravity note not parameterized correctly; got %q", agy)
	}
}
