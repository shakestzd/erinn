// Package launchtui provides a shared dashboard-palette theme for the wipnote
// TUI launcher. It encodes the palette tokens from plugin/static/has-styles.css
// as lipgloss styles and exposes a huh theme constructor so all launcher
// chooser screens share a single source of truth.
package launchtui

import (
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// Palette hex constants — mirrored verbatim from plugin/static/has-styles.css.
// Update here (and there) together; never define these values elsewhere.
const (
	ColAccent      = "#CDFF00" // --accent
	ColAccentText  = "#0A0A0A" // --accent-text
	ColBgPrimary   = "#151518" // --bg-primary
	ColBgSecondary = "#1C1C20" // --bg-secondary
	ColTextPrimary = "#E0DED8" // --text-primary
	ColTextMuted   = "#707070" // --text-muted / --status-todo
	ColBorder      = "#333338" // --border
	ColStatusBlue  = "#3b82f6" // --status-ip
	ColStatusGreen = "#22c55e" // --status-done
	ColStatusRed   = "#ef4444" // --status-blocked
)

// Styles carries the named lipgloss styles that make up the dashboard palette.
// Use NewStyles() to obtain a populated instance.
type Styles struct {
	// Structural / background
	BgPrimary   lipgloss.Style
	BgSecondary lipgloss.Style
	Frame       lipgloss.Style

	// Typography
	Title         lipgloss.Style
	SectionHeader lipgloss.Style
	TextPrimary   lipgloss.Style
	Muted         lipgloss.Style

	// Accent
	Accent     lipgloss.Style // lime-green foreground
	AccentText lipgloss.Style // dark foreground for text on an accent bg

	// Status
	StatusBlue  lipgloss.Style
	StatusGreen lipgloss.Style
	StatusRed   lipgloss.Style
}

// NewStyles constructs a Styles instance populated with the dashboard palette.
func NewStyles() Styles {
	return Styles{
		BgPrimary:   lipgloss.NewStyle().Background(lipgloss.Color(ColBgPrimary)),
		BgSecondary: lipgloss.NewStyle().Background(lipgloss.Color(ColBgSecondary)),
		Frame: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(ColBorder)).
			Background(lipgloss.Color(ColBgPrimary)).
			Padding(0, 1),

		Title: lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColAccent)).
			Bold(true),
		SectionHeader: lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColTextPrimary)).
			Bold(true).
			Underline(true),
		TextPrimary: lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColTextPrimary)),
		Muted: lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColTextMuted)),

		Accent: lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColAccent)),
		AccentText: lipgloss.NewStyle().
			Foreground(lipgloss.Color(ColAccentText)),

		StatusBlue:  lipgloss.NewStyle().Foreground(lipgloss.Color(ColStatusBlue)),
		StatusGreen: lipgloss.NewStyle().Foreground(lipgloss.Color(ColStatusGreen)),
		StatusRed:   lipgloss.NewStyle().Foreground(lipgloss.Color(ColStatusRed)),
	}
}

// WipnoteTheme returns a *huh.Theme styled with the dashboard palette.
// The returned theme is a copy of huh's base theme with palette overrides
// applied, so unset fields retain sensible defaults.
func WipnoteTheme() *huh.Theme {
	t := huh.ThemeBase()

	accent := lipgloss.Color(ColAccent)
	accentText := lipgloss.Color(ColAccentText)
	bg := lipgloss.Color(ColBgPrimary)
	text := lipgloss.Color(ColTextPrimary)
	muted := lipgloss.Color(ColTextMuted)

	// --- Focused field styles ---
	t.Focused.Base = t.Focused.Base.Background(bg)
	t.Focused.Title = lipgloss.NewStyle().Foreground(accent).Bold(true)
	t.Focused.Description = lipgloss.NewStyle().Foreground(muted)
	t.Focused.SelectSelector = lipgloss.NewStyle().Foreground(accent).Bold(true)
	t.Focused.Option = lipgloss.NewStyle().Foreground(text)
	t.Focused.SelectedOption = lipgloss.NewStyle().
		Foreground(accentText).
		Background(accent).
		Bold(true)
	t.Focused.SelectedPrefix = lipgloss.NewStyle().Foreground(accent)
	t.Focused.UnselectedOption = lipgloss.NewStyle().Foreground(text)
	t.Focused.FocusedButton = lipgloss.NewStyle().
		Foreground(accentText).
		Background(accent).
		Padding(0, 1).
		Bold(true)
	t.Focused.BlurredButton = lipgloss.NewStyle().
		Foreground(text).
		Background(lipgloss.Color(ColBgSecondary)).
		Padding(0, 1)

	// --- Blurred field styles (unfocused) ---
	t.Blurred.Base = t.Blurred.Base.Background(bg)
	t.Blurred.Title = lipgloss.NewStyle().Foreground(muted)
	t.Blurred.Description = lipgloss.NewStyle().Foreground(muted)
	t.Blurred.SelectSelector = lipgloss.NewStyle().Foreground(muted)
	t.Blurred.Option = lipgloss.NewStyle().Foreground(muted)
	t.Blurred.SelectedOption = lipgloss.NewStyle().Foreground(text)
	t.Blurred.FocusedButton = t.Focused.FocusedButton
	t.Blurred.BlurredButton = t.Focused.BlurredButton

	return t
}
