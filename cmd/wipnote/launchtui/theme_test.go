package launchtui_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/shakestzd/wipnote/cmd/wipnote/launchtui"
)

// fgHex extracts the hex string from a style's foreground color.
// Returns "" if no foreground is set.
func fgHex(s lipgloss.Style) string {
	c, ok := s.GetForeground().(lipgloss.Color)
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(string(c), "#"))
}

// bgHex extracts the hex string from a style's background color.
func bgHex(s lipgloss.Style) string {
	c, ok := s.GetBackground().(lipgloss.Color)
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(string(c), "#"))
}

// TestWipnoteTheme_PaletteTokens verifies that the hex tokens sourced from
// plugin/static/has-styles.css are baked into the exported styles.
// We inspect each style's colour value directly (not rendered ANSI) so the
// test is stable across terminal environments.
func TestWipnoteTheme_PaletteTokens(t *testing.T) {
	t.Parallel()

	s := launchtui.NewStyles()

	cases := []struct {
		name    string
		got     string
		wantHex string
	}{
		{"Accent fg", fgHex(s.Accent), "cdff00"},
		{"AccentText fg", fgHex(s.AccentText), "0a0a0a"},
		{"BgPrimary bg", bgHex(s.BgPrimary), "151518"},
		{"BgSecondary bg", bgHex(s.BgSecondary), "1c1c20"},
		{"TextPrimary fg", fgHex(s.TextPrimary), "e0ded8"},
		{"Muted fg", fgHex(s.Muted), "707070"},
		{"StatusBlue fg", fgHex(s.StatusBlue), "3b82f6"},
		{"StatusGreen fg", fgHex(s.StatusGreen), "22c55e"},
		{"StatusRed fg", fgHex(s.StatusRed), "ef4444"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.wantHex {
				t.Errorf("%s: got colour %q, want %q", tc.name, tc.got, tc.wantHex)
			}
		})
	}
}

// TestWipnoteTheme_HuhTheme verifies that WipnoteTheme() returns a non-nil
// *huh.Theme and that the focused SelectSelector uses the accent colour.
func TestWipnoteTheme_HuhTheme(t *testing.T) {
	t.Parallel()

	theme := launchtui.WipnoteTheme()
	if theme == nil {
		t.Fatal("WipnoteTheme() returned nil")
	}

	got := fgHex(theme.Focused.SelectSelector)
	const wantAccent = "cdff00"
	if got != wantAccent {
		t.Errorf("focused SelectSelector foreground: got %q, want %q", got, wantAccent)
	}
}
