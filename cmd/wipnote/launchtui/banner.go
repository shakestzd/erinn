package launchtui

import (
	"fmt"
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
	// Accepted values: "red" (default when non-empty), "amber".
	WarningSeverity string
}

// BannerDetail is a label/value row rendered inside a launch banner.
type BannerDetail struct {
	Label string
	Value string
}

// RenderLaunchBanner renders a framed dashboard block containing the launch
// banner fields. It uses the provided lipgloss.Renderer so callers can inject
// a specific color profile for tests. Pass nil to use the lipgloss default
// renderer (auto-detected from the real terminal).
//
// lipgloss strips ANSI codes automatically when the renderer's output is not a
// TTY (Ascii profile), so log-scrape contracts are preserved without any extra
// conditional logic here.
func RenderLaunchBanner(r *lipgloss.Renderer, in BannerInput) string {
	s := newStylesForRenderer(r)

	var lines []string

	if in.Headline != "" {
		lines = append(lines, s.Accent.Render(in.Headline))
	}
	if in.PluginSource != "" {
		lines = append(lines, s.Muted.Render("  Plugin: ")+s.TextPrimary.Render(in.PluginSource))
	}
	if in.Session != "" {
		lines = append(lines, s.Muted.Render("  Session: ")+s.TextPrimary.Render(in.Session))
	}
	for _, detail := range in.Details {
		if detail.Label == "" && detail.Value == "" {
			continue
		}
		label := strings.TrimSpace(detail.Label)
		if label != "" && !strings.HasSuffix(label, ":") {
			label += ":"
		}
		if label != "" {
			label += " "
		}
		lines = append(lines, s.Muted.Render("  "+label)+s.TextPrimary.Render(strings.TrimSpace(detail.Value)))
	}
	if in.Warning != "" {
		warnStyle := warnStyle(s, in.WarningSeverity)
		lines = append(lines, warnStyle.Render("  Warning: "+stripWarningPrefix(in.Warning)))
	}

	body := strings.Join(lines, "\n")
	return s.Frame.Render(body)
}

// stripWarningPrefix removes a redundant leading "Warning:" label
// (case-insensitive) from supplied text so RenderLaunchBanner's own "Warning: "
// prefix is not doubled (e.g. plan.DirtyMainWarning already starts with "Warning:").
func stripWarningPrefix(s string) string {
	t := strings.TrimSpace(s)
	if len(t) >= len("warning:") && strings.EqualFold(t[:len("warning:")], "warning:") {
		return strings.TrimSpace(t[len("warning:"):])
	}
	return t
}

// warnStyle returns the appropriate status style for the given severity label.
func warnStyle(s Styles, severity string) lipgloss.Style {
	if strings.EqualFold(severity, "amber") {
		return s.StatusRed.Bold(true) // lipgloss has no amber token; red bold reads as warning
	}
	return s.StatusRed
}

// newStylesForRenderer constructs Styles bound to r. When r is nil the default
// global renderer (auto-detected terminal) is used.
func newStylesForRenderer(r *lipgloss.Renderer) Styles {
	if r == nil {
		return NewStyles()
	}
	return newStylesWithRenderer(r)
}

// newStylesWithRenderer is like NewStyles but every style is derived from r so
// the color profile (and therefore ANSI stripping) is controlled by the caller.
func newStylesWithRenderer(r *lipgloss.Renderer) Styles {
	newStyle := r.NewStyle

	return Styles{
		BgPrimary:   newStyle().Background(lipgloss.Color(ColBgPrimary)),
		BgSecondary: newStyle().Background(lipgloss.Color(ColBgSecondary)),
		Frame: newStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(ColBorder)).
			Background(lipgloss.Color(ColBgPrimary)).
			Padding(0, 1),

		Title: newStyle().
			Foreground(lipgloss.Color(ColAccent)).
			Bold(true),
		SectionHeader: newStyle().
			Foreground(lipgloss.Color(ColTextPrimary)).
			Bold(true).
			Underline(true),
		TextPrimary: newStyle().
			Foreground(lipgloss.Color(ColTextPrimary)),
		Muted: newStyle().
			Foreground(lipgloss.Color(ColTextMuted)),

		Accent: newStyle().
			Foreground(lipgloss.Color(ColAccent)),
		AccentText: newStyle().
			Foreground(lipgloss.Color(ColAccentText)),

		StatusBlue:  newStyle().Foreground(lipgloss.Color(ColStatusBlue)),
		StatusGreen: newStyle().Foreground(lipgloss.Color(ColStatusGreen)),
		StatusRed:   newStyle().Foreground(lipgloss.Color(ColStatusRed)),
	}
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

// formatBannerLabel formats a label+value pair using muted label and primary value.
// Exported for use by yolo launcher (slice 4) so it can assemble custom lines.
func formatBannerLabel(s Styles, label, value string) string {
	return fmt.Sprintf("%s%s", s.Muted.Render(label), s.TextPrimary.Render(value))
}
