package launchtui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// BannerInput carries all the fields RenderLaunchBanner will render.
// Callers build this struct from their local context; the renderer stays thin.
type BannerInput struct {
	// Headline is the primary line, e.g. "Launching Claude Code (default mode)…"
	Headline string
	// PluginSource is the resolved plugin-dir path. Empty to omit the line.
	PluginSource string
	// Session is the session name/ID. Empty to omit the line.
	Session string
	// Details are optional label/value rows for launcher status summaries.
	Details []BannerDetail
	// Warning is optional advisory text (e.g. dirty-branch warning).
	// Empty string means no warning row is rendered.
	Warning string
	// WarningSeverity controls which status color the warning uses.
	// Accepted values: "red", "amber" (default when empty: advisory/amber).
	WarningSeverity string
}

// BannerDetail is a label/value row rendered inside a launch banner.
type BannerDetail struct {
	Label string
	Value string
}

// Session-thread glyphs. The launch is the opening node of the session's
// lineage thread; an accent rail threads the rows; the continuation tick says
// the thread continues into the session. These are intentionally NOT a box —
// there is no enclosing frame.
const (
	glyphNode  = "◇" // the launch node (and the warning node)
	glyphTick  = "╵" // continuation tick: the thread runs on into the session
	glyphWarn  = "⚠" // warning marker — must survive a monochrome terminal
	railGap    = "   "
	railIndent = "│" + railGap // gutter for detail rows
	valueGap   = "   "
)

// AdaptiveColor tokens for the session-thread layout. AdaptiveColor picks the
// Light or Dark hex at render time from the terminal background so the output
// reads on light AND dark terminals.
var (
	threadAccent = lipgloss.AdaptiveColor{Light: "#0D9488", Dark: "#2DD4BF"} // teal — node/rail/headline
	threadLabel  = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"} // dim — detail labels
	threadWarn   = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#F59E0B"} // amber — advisory
	threadDanger = lipgloss.AdaptiveColor{Light: "#DC2626", Dark: "#EF4444"} // red — refusal
)

// RenderLaunchBanner renders the launch as a BOXLESS "session-thread" block:
// an accent node opens the thread, dim-labelled rows hang off an accent rail,
// and a lone continuation tick closes it. There is no enclosing frame.
//
// It uses the provided lipgloss.Renderer so callers can inject a specific color
// profile for tests. Pass nil to use the lipgloss default renderer (auto-
// detected from the real terminal). lipgloss strips ANSI codes automatically
// when the renderer's output is not a TTY (Ascii profile), so log-scrape
// contracts are preserved — the glyphs and words remain, only color is dropped.
func RenderLaunchBanner(r *lipgloss.Renderer, in BannerInput) string {
	newStyle := lipgloss.NewStyle
	if r != nil {
		newStyle = r.NewStyle
	}

	accent := newStyle().Foreground(threadAccent)
	headline := newStyle().Foreground(threadAccent).Bold(true)
	label := newStyle().Foreground(threadLabel)

	// SPECIAL CASE — standalone warning / refuse render (no headline). There is
	// no launch following, so the warning itself becomes the lead node line.
	if in.Headline == "" && in.Warning != "" {
		sev := newStyle().Foreground(warnColor(in.WarningSeverity))
		var b strings.Builder
		b.WriteString(sev.Render(glyphNode+" "+glyphWarn+" ") + sev.Render(warningText(in.Warning)))
		b.WriteString("\n")
		b.WriteString(accent.Render(glyphTick))
		return b.String()
	}

	if in.Headline == "" {
		// Nothing to render (no headline, no warning).
		return ""
	}

	// Gather detail rows (label + value) so we can align values in a column.
	type row struct{ label, value string }
	var rows []row
	if in.PluginSource != "" {
		rows = append(rows, row{"plugin", in.PluginSource})
	}
	if in.Session != "" {
		rows = append(rows, row{"session", in.Session})
	}
	for _, d := range in.Details {
		l := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(d.Label), ":"))
		v := strings.TrimSpace(d.Value)
		if l == "" && v == "" {
			continue
		}
		rows = append(rows, row{l, v})
	}

	// Compute the max label width so all values align in a column.
	maxLabel := 0
	for _, rw := range rows {
		if w := lipgloss.Width(rw.label); w > maxLabel {
			maxLabel = w
		}
	}

	var b strings.Builder

	// Node line: accent node + accent bold headline.
	b.WriteString(headline.Render(glyphNode + " " + in.Headline))

	// Detail rows: accent rail gutter, dim padded label, gap, plain value.
	for _, rw := range rows {
		pad := maxLabel - lipgloss.Width(rw.label)
		if pad < 0 {
			pad = 0
		}
		b.WriteString("\n")
		b.WriteString(accent.Render(railIndent))
		b.WriteString(label.Render(rw.label + strings.Repeat(" ", pad)))
		b.WriteString(valueGap)
		b.WriteString(rw.value) // plain / default fg
	}

	// Warning row (advisory): severity-colored node + ⚠ + words. The glyph AND
	// the words are always present so the warning survives a monochrome terminal.
	if in.Warning != "" {
		sev := newStyle().Foreground(warnColor(in.WarningSeverity))
		b.WriteString("\n")
		b.WriteString(sev.Render(glyphNode+railGap) + sev.Render(glyphWarn+" "+warningText(in.Warning)))
	}

	// Continuation tick: the thread runs on into the session.
	b.WriteString("\n")
	b.WriteString(accent.Render(glyphTick))

	return b.String()
}

// warnColor maps a severity label to its thread color. "red" → danger;
// anything else (including the empty default) is an advisory → amber.
func warnColor(severity string) lipgloss.AdaptiveColor {
	if strings.EqualFold(severity, "red") {
		return threadDanger
	}
	return threadWarn
}

// warningText strips a redundant leading "Warning:" label (case-insensitive)
// and collapses internal newlines into a single tight line so the advisory
// renders as one threaded row rather than a multi-line block.
func warningText(s string) string {
	t := strings.TrimSpace(s)
	if len(t) >= len("warning:") && strings.EqualFold(t[:len("warning:")], "warning:") {
		t = strings.TrimSpace(t[len("warning:"):])
	}
	// Collapse any embedded newlines/indentation into single spaces.
	fields := strings.Fields(t)
	return strings.Join(fields, " ")
}

// MakeRendererForProfile is a test helper (also usable from callers) that
// creates a lipgloss.Renderer with the given termenv profile forced via
// SetColorProfile so the renderer's explicitColorProfile flag is set.
// The renderer uses a discarded strings.Builder as the underlying writer;
// rendered output is returned as string values, not written to the writer.
func MakeRendererForProfile(profile termenv.Profile) *lipgloss.Renderer {
	r := lipgloss.NewRenderer(&strings.Builder{}, termenv.WithProfile(profile))
	r.SetColorProfile(profile)
	return r
}
