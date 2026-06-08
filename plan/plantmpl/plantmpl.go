// Package plantmpl provides typed, component-based plan HTML generation.
//
// Each plan zone (dependency graph, design, outline, slices, questions,
// critique, finalize preview, progress bar) is a separate struct with a
// Render method. PlanPage assembles all zones into a complete HTML5 document.
//
// This replaces the monolithic plan-template.html with composable,
// testable components.
package plantmpl

import (
	"bytes"
	"embed"
	"html/template"
	"io"
	"strings"
	texttemplate "text/template"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// mdParser is a shared goldmark instance with GFM extensions enabled.
// It allows unsafe HTML so that goldmark renders raw HTML — bluemonday then
// strips the dangerous bits. This ensures we get proper fenced-code blocks,
// tables, and strikethrough while still sanitizing XSS vectors.
var mdParser = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

// mdPolicy is the bluemonday policy used to sanitize goldmark output.
// It permits standard Markdown output elements (headings, lists, code, etc.)
// while stripping <script>, event handlers, and other XSS vectors.
var mdPolicy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	// Allow class attributes on <code> and <pre> (e.g. language-go) for
	// client-side syntax highlighting if desired.
	p.AllowAttrs("class").OnElements("code", "pre")
	return p
}()

// RenderMd converts a Markdown string to sanitized HTML. It uses goldmark
// for parsing and bluemonday for sanitization so that headings, lists, code
// fences, and inline code all render correctly while XSS vectors are removed.
// Plain-text input is wrapped in a <p> tag by goldmark.
func RenderMd(src string) template.HTML {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := mdParser.Convert([]byte(src), &buf); err != nil {
		// On parse error, return the source HTML-escaped as a fallback.
		return template.HTML(template.HTMLEscapeString(src))
	}
	safe := mdPolicy.SanitizeBytes(buf.Bytes())
	return template.HTML(safe)
}

//go:embed templates/*
var templateFS embed.FS

// renderZone calls Render on a Component and returns the result as
// template.HTML so it can be embedded directly in the page template.
func renderZone(c Component) template.HTML {
	if c == nil {
		return ""
	}
	var buf bytes.Buffer
	if err := c.Render(&buf); err != nil {
		return template.HTML("<!-- render error: " + err.Error() + " -->")
	}
	return template.HTML(buf.String())
}

// renderSlices renders all SliceCards and returns the concatenated HTML.
func renderSlices(cards []SliceCard) template.HTML {
	var buf bytes.Buffer
	for i := range cards {
		if err := cards[i].Render(&buf); err != nil {
			buf.WriteString("<!-- slice render error: " + err.Error() + " -->")
		}
	}
	return template.HTML(buf.String())
}

// planPageTmpl uses text/template (not html/template) because:
//   - Zone components handle their own HTML escaping via html/template
//   - The page shell contains static JS that must survive intact
//     (including JS comment markers used by runtime HTML patching)
//   - All dynamic values inserted at the page level are either
//     pre-rendered template.HTML or known-safe format (SectionsJSON)
var planPageTmpl = texttemplate.Must(
	texttemplate.New("plan_page.gohtml").Funcs(texttemplate.FuncMap{
		"renderZone":   renderZone,
		"renderSlices": renderSlices,
		"hasPrefix":    strings.HasPrefix,
	}).ParseFS(templateFS, "templates/plan_page.gohtml"),
)

// sliceCardFuncs provides template functions for slice_card.gohtml.
// These are registered on the html/template instance used by sliceCardTmpl.
var sliceCardFuncs = template.FuncMap{
	"lower": strings.ToLower,
	"upper": strings.ToUpper,
}

// Component is anything that can render itself into a plan zone.
type Component interface {
	Render(w io.Writer) error
}

// AssetRegistry collects CSS/JS blocks from zones for deduplication.
type AssetRegistry struct {
	css []string
	js  []string
}

// AddCSS appends a CSS block to the registry.
func (a *AssetRegistry) AddCSS(block string) { a.css = append(a.css, block) }

// AddJS appends a JS block to the registry.
func (a *AssetRegistry) AddJS(block string) { a.js = append(a.js, block) }

// CSS returns all collected CSS blocks.
func (a *AssetRegistry) CSS() []string { return a.css }

// JS returns all collected JS blocks.
func (a *AssetRegistry) JS() []string { return a.js }

// RelatedWorkItem represents a linked track or feature shown in the Related Work section.
type RelatedWorkItem struct {
	ID     string // e.g. "trk-16d4519d" or "feat-17a993f0"
	Title  string
	Type   string // "track", "feature", "bug"
	Status string // "todo", "in-progress", "done"
}

// StatusClass returns the CSS badge class suffix for the work item status.
func (r *RelatedWorkItem) StatusClass() string {
	switch r.Status {
	case "done":
		return "done"
	case "in-progress":
		return "ip"
	case "blocked":
		return "blocked"
	default:
		return "todo"
	}
}

// PlanPage is the top-level struct that assembles all zones into a
// complete plan HTML document.
type PlanPage struct {
	PlanID      string
	FeatureID   string
	Title       string
	Description string
	Date        string // original YAML meta.created_at when available, else render time
	Version     int    // monotonic version counter from plan.meta.version; 0 if unset
	Status      string // "draft", "in-progress", "finalized", etc.
	Priority    string // "low", "medium", "high", "critical"

	// IsV2 marks a plan as using the v2 slice-card format where each slice
	// carries its own questions and critic_revisions. When true, the global
	// Questions and Critique sections are suppressed in favour of the per-slice UI.
	IsV2 bool

	// Zone components
	Graph     *DependencyGraph
	Design    *DesignSection
	Outline   *OutlineSection
	Slices    []SliceCard
	Questions *QuestionsSection
	Critique  *CritiqueZone
	Preview   *FinalizePreview
	Progress  *ProgressBar

	// Related work items (track, generated features)
	RelatedTrack    *RelatedWorkItem
	RelatedFeatures []RelatedWorkItem

	// Consolidated assets
	Assets *AssetRegistry
}

// StatusBadgeClass returns the CSS badge class suffix for the plan status.
func (p *PlanPage) StatusBadgeClass() string {
	switch p.Status {
	case "active", "in-progress":
		return "badge-revision"
	case "completed", "finalized", "done":
		return "badge-approved"
	case "blocked":
		return "badge-blocked"
	default:
		return "badge-pending"
	}
}

// StatusLabel returns a human-readable label for the plan status badge.
func (p *PlanPage) StatusLabel() string {
	switch p.Status {
	case "active":
		return "Active"
	case "in-progress":
		return "In Progress"
	case "completed", "finalized":
		return "Done"
	case "blocked":
		return "Blocked"
	case "draft", "todo":
		return "Draft"
	default:
		if p.Status != "" {
			return p.Status
		}
		return "Draft"
	}
}

// Render writes the complete plan HTML to w.
func (p *PlanPage) Render(w io.Writer) error {
	if p.Assets == nil {
		p.Assets = &AssetRegistry{}
	}
	if p.Status == "" {
		p.Status = "draft"
	}
	if p.Priority == "" {
		p.Priority = "medium"
	}
	return planPageTmpl.Execute(w, p)
}
