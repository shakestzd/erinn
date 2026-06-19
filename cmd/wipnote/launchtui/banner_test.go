package launchtui_test

import (
	"strings"
	"testing"

	"github.com/muesli/termenv"
	"github.com/shakestzd/wipnote/cmd/wipnote/launchtui"
)

// sampleInput is a populated BannerInput used by multiple sub-tests.
func sampleInput() launchtui.BannerInput {
	return launchtui.BannerInput{
		Headline:     "Launching Claude Code (default mode)...",
		PluginSource: "/home/user/.local/share/wipnote/plugin",
		Session:      "wipnote-20260618-15.04.05",
		Warning:      "working tree is dirty; commit or stash before launching",
	}
}

// TestRenderLaunchBanner_PlainOnNonTTY verifies that when the renderer uses the
// Ascii (no-color) profile:
//  1. The literal session ID appears in the output.
//  2. The literal warning text appears in the output.
//  3. No ESC (\x1b) escape sequences are present.
func TestRenderLaunchBanner_PlainOnNonTTY(t *testing.T) {
	t.Parallel()

	r := launchtui.MakeRendererForProfile(termenv.Ascii)
	in := sampleInput()
	got := launchtui.RenderLaunchBanner(r, in)

	// Literal content must survive.
	if !strings.Contains(got, in.Session) {
		t.Errorf("non-TTY output missing session %q\ngot:\n%s", in.Session, got)
	}
	if !strings.Contains(got, in.Warning) {
		t.Errorf("non-TTY output missing warning %q\ngot:\n%s", in.Warning, got)
	}
	if !strings.Contains(got, in.Headline) {
		t.Errorf("non-TTY output missing headline %q\ngot:\n%s", in.Headline, got)
	}
	if !strings.Contains(got, in.PluginSource) {
		t.Errorf("non-TTY output missing plugin source %q\ngot:\n%s", in.PluginSource, got)
	}

	// No raw ANSI escape codes.
	if strings.Contains(got, "\x1b") {
		t.Errorf("non-TTY output contains ESC sequences (raw ANSI):\n%s", got)
	}
}

// TestRenderLaunchBanner_AccentOnTTY verifies that when the renderer uses the
// TrueColor profile the output contains ESC sequences (color is on).
func TestRenderLaunchBanner_AccentOnTTY(t *testing.T) {
	t.Parallel()

	r := launchtui.MakeRendererForProfile(termenv.TrueColor)
	in := sampleInput()
	got := launchtui.RenderLaunchBanner(r, in)

	// With color enabled the rendered string must contain at least one ESC byte.
	if !strings.Contains(got, "\x1b") {
		t.Errorf("TTY output expected ESC sequences (color enabled) but none found:\n%s", got)
	}

	// The literal text must still be present even with color codes.
	if !strings.Contains(got, in.Session) {
		t.Errorf("TTY output missing session %q", in.Session)
	}
	if !strings.Contains(got, in.Headline) {
		t.Errorf("TTY output missing headline %q", in.Headline)
	}
}

// TestRenderLaunchBanner_EmptyWarning verifies that omitting the Warning field
// does not inject a spurious "Warning:" line.
func TestRenderLaunchBanner_EmptyWarning(t *testing.T) {
	t.Parallel()

	r := launchtui.MakeRendererForProfile(termenv.Ascii)
	in := launchtui.BannerInput{
		Headline: "Launching...",
		Session:  "sess-abc",
	}
	got := launchtui.RenderLaunchBanner(r, in)
	if strings.Contains(got, "Warning") {
		t.Errorf("expected no warning line when Warning is empty, got:\n%s", got)
	}
}

// TestRenderLaunchBanner_NilRenderer ensures RenderLaunchBanner does not panic
// when passed a nil renderer (falls back to lipgloss default).
func TestRenderLaunchBanner_NilRenderer(t *testing.T) {
	t.Parallel()

	in := launchtui.BannerInput{Headline: "hello"}
	// Must not panic.
	got := launchtui.RenderLaunchBanner(nil, in)
	if !strings.Contains(got, "hello") {
		t.Errorf("nil-renderer output missing headline, got: %q", got)
	}
}
