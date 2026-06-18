package blocks

import (
	"html/template"
	"io"
	"regexp"

	"github.com/microcosm-cc/bluemonday"
)

// rawColorRe matches raw CSS colors: hex (#abc / #1a2b3c / #1a2b3cff) and the
// rgb()/rgba()/hsl()/hsla() functions. It is intentionally identical to the
// validator's pattern in plan/planyaml (validate.go) so the renderer and the
// schema validator never disagree about what a "raw color" is. Wireframes MUST
// use design tokens (CSS custom properties like var(--wf-fg)) instead of
// baked-in colors.
var rawColorRe = regexp.MustCompile(`(?i)#[0-9a-f]{3,8}\b|\brgba?\(|\bhsla?\(`)

// Wireframe style-value allowlists. bluemonday keeps a CSS declaration only when
// its value matches; anything else (url(...), expression(...), javascript:, raw
// colors) is dropped. Color properties accept ONLY var(--wf-*) tokens, so the
// "tokens, not hex" contract is enforced even for color baked into style.
var (
	wfTokenValRe   = regexp.MustCompile(`(?i)^var\(--wf-[a-z0-9-]+\)$`)
	wfLengthValRe  = regexp.MustCompile(`(?i)^(auto|0|(-?\d+(\.\d+)?(px|em|rem|%|vh|vw|fr)?)(\s+-?\d+(\.\d+)?(px|em|rem|%|vh|vw|fr)?){0,3})$`)
	wfKeywordValRe = regexp.MustCompile(`(?i)^[a-z][a-z-]*$`)
	wfNumberValRe  = regexp.MustCompile(`^\d+(\.\d+)?$`)
	wfBorderValRe  = regexp.MustCompile(`(?i)^\d+px\s+(solid|dashed|dotted)\s+var\(--wf-[a-z0-9-]+\)$`)
)

// wfPolicy is a strict allowlist sanitizer for wireframe author markup. Built
// from an empty policy, so only the explicitly permitted structural/presentational
// elements and attributes survive — <script>, <iframe>, on* event handlers,
// javascript: URLs, and any unlisted element/attribute are stripped. This is the
// trust boundary: wireframe HTML comes from plan YAML authored by a person, but it
// is rendered into committed plan/recap artifacts and injected into the dashboard,
// so it must be sanitized before it can be marked template.HTML.
var wfPolicy = func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements(
		"div", "span", "p", "section", "article", "header", "footer", "nav",
		"main", "aside", "ul", "ol", "li", "dl", "dt", "dd",
		"h1", "h2", "h3", "h4", "h5", "h6",
		"table", "thead", "tbody", "tfoot", "tr", "td", "th",
		"button", "label", "figure", "figcaption", "br", "hr",
		"strong", "em", "b", "i", "small", "code", "pre", "img",
	)
	p.AllowAttrs("class").Globally()
	p.AllowAttrs("alt", "width", "height").OnElements("img")
	// Color-bearing properties: var(--wf-*) tokens only.
	p.AllowStyles("color", "background", "background-color", "border-color",
		"outline-color", "fill", "stroke").Matching(wfTokenValRe).Globally()
	// Sizing/spacing: lengths and shorthands only.
	p.AllowStyles("padding", "padding-top", "padding-right", "padding-bottom", "padding-left",
		"margin", "margin-top", "margin-right", "margin-bottom", "margin-left",
		"width", "height", "min-width", "min-height", "max-width", "max-height",
		"gap", "row-gap", "column-gap", "border-radius", "border-width",
		"font-size", "line-height", "top", "left", "right", "bottom").Matching(wfLengthValRe).Globally()
	// Presentational keywords (no functions, no URLs).
	p.AllowStyles("display", "flex-direction", "flex-wrap", "justify-content",
		"align-items", "align-content", "text-align", "position", "font-weight",
		"font-style", "text-decoration", "text-transform", "white-space",
		"box-sizing", "overflow", "overflow-x", "overflow-y").Matching(wfKeywordValRe).Globally()
	// Unitless numerics.
	p.AllowStyles("flex", "flex-grow", "flex-shrink", "opacity", "z-index", "order").Matching(wfNumberValRe).Globally()
	// Border shorthand restricted to a token color.
	p.AllowStyles("border", "border-top", "border-right", "border-bottom", "border-left").Matching(wfBorderValRe).Globally()
	return p
}()

// wireframeTokens is the scoped --wf-* token set every wireframe inherits. Each
// token aliases a base dashboard token (declared on :root in the plan/recap page
// shells) so wireframes track the canonical palette and theme switches for free,
// while still giving wireframe authors a small, stable, wireframe-specific
// vocabulary to target. This mirrors BuilderIO's "tokens, not hex" rule: authors
// reference var(--wf-*), never raw colors.
//
// The fallback values (the second arg of each var()) keep wireframes legible
// when rendered as a standalone fragment outside the dashboard shell. Keeping the
// set small and self-contained — rather than letting wireframes touch arbitrary
// dashboard tokens — decouples the wireframe contract from internal dashboard
// token churn: this map is the entire public surface.
const wireframeTokens = `--wf-bg:var(--bg-card,#18181c);` +
	`--wf-surface:var(--bg-input,#222228);` +
	`--wf-fg:var(--text,#f0f0ed);` +
	`--wf-muted:var(--text-muted,#9898a0);` +
	`--wf-border:var(--border,#3a3a42);` +
	`--wf-accent:var(--accent,#cdff00);` +
	`--wf-radius:var(--radius,8px);`

// Wireframe renders an HTML/CSS sketch of a UI change built entirely from design
// tokens (no raw hex/rgb/hsl colors). It is the wipnote-native "wireframe" block,
// shared verbatim between plan slice blocks and recap before/after panels so a
// proposed UI and a shipped UI render through one code path.
//
// Body is raw author markup that may reference var(--wf-*) tokens; because the
// whole point of a wireframe is structural HTML/CSS, Body is emitted unescaped —
// but ONLY after RawColors() confirms it carries no baked-in colors. Title is an
// optional caption (e.g. "Before", "After"). Anchor, when set, is stamped as the
// element id and data-block-anchor so the dashboard annotation dropdown can
// target the block (contract: slice-<num>-block-<name>-<idx>).
type Wireframe struct {
	Title  string
	Body   string
	Anchor string
}

// RawColors reports whether Body contains raw hex/rgb/hsl colors. A wireframe
// that fails this check is rejected by the renderer (it renders an error notice
// instead of the markup), keeping the renderer consistent with the schema
// validator which rejects the same plans at author time.
func (wf *Wireframe) RawColors() bool {
	return rawColorRe.MatchString(wf.Body)
}

// SafeBody returns the wireframe body sanitized through the strict wfPolicy
// allowlist, as template.HTML. Author markup is run through bluemonday first, so
// <script>, <iframe>, event handlers, javascript: URLs, and any style value
// outside the var(--wf-*) token contract are removed before the result is marked
// trusted. The template calls this only on the no-raw-color branch; RawColors()
// rejects baked-in colors up front, and the sanitizer is defense-in-depth on top.
func (wf *Wireframe) SafeBody() template.HTML {
	return template.HTML(wfPolicy.Sanitize(wf.Body))
}

// Tokens returns the scoped --wf-* token declarations for this wireframe's style
// attribute. Returned as template.CSS so html/template treats it as a trusted
// style value rather than escaping the parentheses in var(...).
func (wf *Wireframe) Tokens() template.CSS {
	return template.CSS(wireframeTokens)
}

// Render writes the wireframe block HTML to w.
func (wf *Wireframe) Render(w io.Writer) error {
	return blockTmpl.ExecuteTemplate(w, "wireframe", wf)
}
