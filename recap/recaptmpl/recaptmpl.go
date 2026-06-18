// Package recaptmpl provides typed, component-based recap HTML generation.
//
// It mirrors plan/plantmpl: each recap zone (outcome narrative, file tree,
// annotated diff, lineage chain) is a struct with a Render method, and RecapPage
// assembles the zones into a complete, self-contained HTML5 document. The
// collector (internal/recap) owns data; this package owns rendering only.
//
// Template split (matches plantmpl):
//   - The outer page shell uses text/template, because it embeds static JS/CSS
//     that must survive intact and all dynamic values are either pre-rendered
//     template.HTML zones or known-safe scalar fields escaped explicitly.
//   - Inner zones use html/template, so every dynamic value coming from the
//     collector (file paths, diff lines, lineage titles) is contextually
//     auto-escaped.
//
// Syntax highlighting: the standalone artifact never inlines a highlighter. Diff
// code sits in <pre><code class="language-…"> so it renders as plain text when
// opened directly (no Prism present) and is colorized only on the dashboard
// injection path, which CDN-loads Prism. The file-tree zone reuses the shared
// plan/blocks renderers verbatim.
package recaptmpl

import (
	"bytes"
	"embed"
	htmlesc "html"
	"html/template"
	"io"
	texttemplate "text/template"
)

//go:embed templates/*
var templateFS embed.FS

// Component is anything that can render itself into a recap zone. It mirrors
// plantmpl.Component so zones compose uniformly.
type Component interface {
	Render(w io.Writer) error
}

// renderZone calls Render on a Component and returns the result as template.HTML
// so it can be embedded directly in the page shell. A nil component renders to
// the empty string, which is how optional zones (e.g. lineage when ungrounded)
// are omitted gracefully.
func renderZone(c Component) template.HTML {
	if c == nil || isNil(c) {
		return ""
	}
	var buf bytes.Buffer
	if err := c.Render(&buf); err != nil {
		return template.HTML("<!-- render error: " + htmlesc.EscapeString(err.Error()) + " -->")
	}
	return template.HTML(buf.String())
}

// recapPageTmpl uses text/template (not html/template) for the same reasons as
// plantmpl's page shell: zones self-escape, the shell carries static markup, and
// scalar fields are escaped explicitly via htmlEscape.
var recapPageTmpl = texttemplate.Must(
	texttemplate.New("recap_page.gohtml").Funcs(texttemplate.FuncMap{
		"renderZone": renderZone,
		"htmlEscape": htmlesc.EscapeString,
	}).ParseFS(templateFS, "templates/recap_page.gohtml"),
)

// RecapPage is the top-level struct that assembles all recap zones into a
// complete, self-contained HTML document.
type RecapPage struct {
	// Title is shown in <title> and the header; derived from the outcome.
	Title string
	// Outcome is the human-readable summary of what the change set achieved.
	Outcome string
	// Input/Kind/GitRange describe provenance for the header subline.
	Input    string
	Kind     string
	GitRange string
	Grounded bool

	// Zone components. Lineage is nil for ungrounded recaps (omitted).
	Files   *FileTreeZone
	Diff    *DiffZone
	Lineage *LineageChain
}

// Render writes the complete recap HTML to w.
func (p *RecapPage) Render(w io.Writer) error {
	return recapPageTmpl.Execute(w, p)
}

// isNil reports whether a Component interface holds a typed nil pointer, so that
// renderZone treats e.g. (*LineageChain)(nil) the same as an untyped nil.
func isNil(c Component) bool {
	switch v := c.(type) {
	case *FileTreeZone:
		return v == nil
	case *DiffZone:
		return v == nil
	case *LineageChain:
		return v == nil
	default:
		return false
	}
}
