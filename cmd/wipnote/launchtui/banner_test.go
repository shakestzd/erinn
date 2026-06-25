package launchtui_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/muesli/termenv"
	"github.com/shakestzd/wipnote/cmd/wipnote/launchtui"
)

// ansiRe matches CSI SGR escape sequences so tests can recover the plain text
// from color-rendered output.
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// Session-thread glyphs the boxless layout must emit. Kept here (mirrored from
// banner.go) so the tests pin the structural shape without importing unexported
// constants.
const (
	glyphNode = "◇"
	glyphTick = "╵"
	glyphWarn = "⚠"
	glyphRail = "│"
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
// Ascii (no-color) profile the literal content survives, the boxless glyphs are
// present, and there are no ESC sequences.
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

	// The warning glyph must survive on a monochrome terminal — the advisory
	// must never rely on color alone.
	if !strings.Contains(got, glyphWarn) {
		t.Errorf("non-TTY output missing warning glyph %q\ngot:\n%s", glyphWarn, got)
	}

	// Boxless session-thread structure: opening node, threading rail, closing tick.
	for _, glyph := range []string{glyphNode, glyphRail, glyphTick} {
		if !strings.Contains(got, glyph) {
			t.Errorf("non-TTY output missing session-thread glyph %q\ngot:\n%s", glyph, got)
		}
	}

	// No enclosing box: rounded-border corner glyphs must be absent.
	for _, box := range []string{"╭", "╮", "╰", "╯", "─"} {
		if strings.Contains(got, box) {
			t.Errorf("boxless banner must not contain box-border glyph %q\ngot:\n%s", box, got)
		}
	}

	// No raw ANSI escape codes.
	if strings.Contains(got, "\x1b") {
		t.Errorf("non-TTY output contains ESC sequences (raw ANSI):\n%s", got)
	}
}

// TestRenderLaunchBanner_AccentOnTTY verifies that when the renderer uses the
// TrueColor profile the output contains ESC sequences (color is on) and the
// literal text still survives once ANSI is stripped.
func TestRenderLaunchBanner_AccentOnTTY(t *testing.T) {
	t.Parallel()

	r := launchtui.MakeRendererForProfile(termenv.TrueColor)
	in := sampleInput()
	got := launchtui.RenderLaunchBanner(r, in)

	if !strings.Contains(got, "\x1b") {
		t.Errorf("TTY output expected ESC sequences (color enabled) but none found:\n%s", got)
	}

	plain := stripANSI(got)
	if !strings.Contains(plain, in.Session) {
		t.Errorf("TTY output missing session %q", in.Session)
	}
	if !strings.Contains(plain, in.Headline) {
		t.Errorf("TTY output (ANSI-stripped) missing headline %q\ngot:\n%s", in.Headline, plain)
	}
}

// TestRenderLaunchBanner_AccentHeadline verifies the headline is rendered in a
// single accent color on a truecolor terminal (one flat accent, not a per-rune
// gradient) and degrades to plain text with no ESC on a non-TTY.
func TestRenderLaunchBanner_AccentHeadline(t *testing.T) {
	t.Parallel()

	in := launchtui.BannerInput{Headline: "Launching Claude Code"}

	// TrueColor: the headline node + text is one accent color, so there is at
	// least one foreground-color escape.
	colorOut := launchtui.RenderLaunchBanner(launchtui.MakeRendererForProfile(termenv.TrueColor), in)
	if !strings.Contains(colorOut, "\x1b") {
		t.Errorf("expected an accent-colored headline (ESC present), got none:\n%s", colorOut)
	}

	// Ascii: no ESC, headline survives as contiguous plain text.
	asciiOut := launchtui.RenderLaunchBanner(launchtui.MakeRendererForProfile(termenv.Ascii), in)
	if strings.Contains(asciiOut, "\x1b") {
		t.Errorf("ascii headline output must not contain ESC:\n%s", asciiOut)
	}
	if !strings.Contains(asciiOut, in.Headline) {
		t.Errorf("ascii headline output missing contiguous headline %q:\n%s", in.Headline, asciiOut)
	}
	// The headline must lead with the accent node glyph.
	if !strings.Contains(asciiOut, glyphNode+" "+in.Headline) {
		t.Errorf("ascii headline output missing node-prefixed headline:\n%s", asciiOut)
	}
}

// TestRenderLaunchBanner_EmptyWarning verifies that omitting the Warning field
// does not inject a spurious warning row (no ⚠ glyph, no "Warning" text).
func TestRenderLaunchBanner_EmptyWarning(t *testing.T) {
	t.Parallel()

	r := launchtui.MakeRendererForProfile(termenv.Ascii)
	in := launchtui.BannerInput{
		Headline: "Launching...",
		Session:  "sess-abc",
	}
	got := launchtui.RenderLaunchBanner(r, in)
	if strings.Contains(got, glyphWarn) {
		t.Errorf("expected no warning row when Warning is empty, got:\n%s", got)
	}
}

// TestRenderLaunchBanner_Details verifies detail rows render their label and
// value on the threaded rail. The label's trailing colon is normalized away;
// label and value both appear on the row separated by the column gap.
func TestRenderLaunchBanner_Details(t *testing.T) {
	t.Parallel()

	r := launchtui.MakeRendererForProfile(termenv.Ascii)
	got := launchtui.RenderLaunchBanner(r, launchtui.BannerInput{
		Headline: "Codex wipnote setup",
		Details: []launchtui.BannerDetail{
			{Label: "Plugin cache", Value: "installed locally"},
			{Label: "Mirrored hooks:", Value: "none found in ~/.codex/hooks.json"},
		},
	})

	// Each row must carry both its (colon-stripped) label and value text.
	for _, want := range []string{
		"Codex wipnote setup",
		"Plugin cache",
		"installed locally",
		"Mirrored hooks",
		"none found in ~/.codex/hooks.json",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("detail banner missing %q:\n%s", want, got)
		}
	}
	// Detail rows hang off the rail.
	if !strings.Contains(got, glyphRail) {
		t.Errorf("detail banner missing rail glyph %q:\n%s", glyphRail, got)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("non-TTY detail banner contains ESC sequences:\n%s", got)
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

// TestRenderLaunchBanner_NoDoubleWarningPrefix verifies that warning text which
// already begins with "Warning:" is not rendered with a doubled "Warning:" label.
func TestRenderLaunchBanner_NoDoubleWarningPrefix(t *testing.T) {
	t.Parallel()

	r := launchtui.MakeRendererForProfile(termenv.Ascii)
	for _, prefixed := range []string{
		"Warning: main has uncommitted changes — pass --work-item <id> to isolate",
		"warning: lowercase label should also be de-duplicated",
	} {
		got := launchtui.RenderLaunchBanner(r, launchtui.BannerInput{Warning: prefixed, WarningSeverity: "red"})
		if strings.Contains(strings.ToLower(got), "warning: warning:") {
			t.Errorf("doubled warning label in output for %q:\n%s", prefixed, got)
		}
		// The substantive message must survive.
		if !strings.Contains(got, "main has uncommitted") && !strings.Contains(got, "lowercase label") {
			t.Errorf("warning body lost after prefix strip for %q:\n%s", prefixed, got)
		}
	}
}

// TestRenderLaunchBanner_StandaloneWarning verifies the special case: when there
// is no headline (the refuse / warn-only standalone render) the warning itself
// becomes the lead node line and the block still closes with the tick — boxless.
func TestRenderLaunchBanner_StandaloneWarning(t *testing.T) {
	t.Parallel()

	r := launchtui.MakeRendererForProfile(termenv.Ascii)
	got := launchtui.RenderLaunchBanner(r, launchtui.BannerInput{
		Warning:         "protected branch is dirty — pass --work-item <id> to isolate",
		WarningSeverity: "red",
	})

	// Lead node + warning glyph + body, then the continuation tick.
	if !strings.Contains(got, glyphNode) {
		t.Errorf("standalone warning missing lead node glyph:\n%s", got)
	}
	if !strings.Contains(got, glyphWarn) {
		t.Errorf("standalone warning missing warning glyph:\n%s", got)
	}
	if !strings.Contains(got, glyphTick) {
		t.Errorf("standalone warning missing continuation tick:\n%s", got)
	}
	if !strings.Contains(got, "protected branch is dirty") {
		t.Errorf("standalone warning body lost:\n%s", got)
	}
	// No box.
	for _, box := range []string{"╭", "╮", "╰", "╯"} {
		if strings.Contains(got, box) {
			t.Errorf("standalone warning must be boxless, found %q:\n%s", box, got)
		}
	}
}
