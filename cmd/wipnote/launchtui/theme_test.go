package launchtui_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/shakestzd/wipnote/cmd/wipnote/launchtui"
)

// fgHex extracts the hex string from a style's foreground color. For an
// AdaptiveColor (feat-e97607b3) it returns the Dark value, since the palette
// tokens are the dark-terminal colors. Returns "" if no foreground is set.
func fgHex(s lipgloss.Style) string {
	switch c := s.GetForeground().(type) {
	case lipgloss.Color:
		return strings.ToLower(strings.TrimPrefix(string(c), "#"))
	case lipgloss.AdaptiveColor:
		return strings.ToLower(strings.TrimPrefix(c.Dark, "#"))
	default:
		return ""
	}
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

// TestStyles_AccentIsAdaptive verifies the banner accent is an AdaptiveColor so
// it reads on light AND dark terminals (feat-e97607b3), with the dark value
// preserving the lime palette token and the light value a readable dark lime.
func TestStyles_AccentIsAdaptive(t *testing.T) {
	t.Parallel()

	s := launchtui.NewStyles()
	ac, ok := s.Accent.GetForeground().(lipgloss.AdaptiveColor)
	if !ok {
		t.Fatalf("Accent foreground should be AdaptiveColor, got %T", s.Accent.GetForeground())
	}
	if strings.ToLower(strings.TrimPrefix(ac.Dark, "#")) != "cdff00" {
		t.Errorf("adaptive accent Dark: got %q, want #cdff00", ac.Dark)
	}
	if strings.ToLower(strings.TrimPrefix(ac.Light, "#")) != "4d7c0f" {
		t.Errorf("adaptive accent Light: got %q, want #4d7c0f", ac.Light)
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
