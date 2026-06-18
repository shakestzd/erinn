package blocks

import (
	"html/template"
	"io"
	"regexp"
)

// rawColorRe matches raw CSS colors: hex (#abc / #1a2b3c / #1a2b3cff) and the
// rgb()/rgba()/hsl()/hsla() functions. It is intentionally identical to the
// validator's pattern in plan/planyaml (validate.go) so the renderer and the
// schema validator never disagree about what a "raw color" is. Wireframes MUST
// use design tokens (CSS custom properties like var(--wf-fg)) instead of
// baked-in colors.
var rawColorRe = regexp.MustCompile(`(?i)#[0-9a-f]{3,8}\b|\brgba?\(|\bhsla?\(`)

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

// SafeBody returns Body as trusted template.HTML. The template only calls this
// on the no-raw-color branch, so color-bearing markup never reaches the
// unescaped path.
func (wf *Wireframe) SafeBody() template.HTML {
	return template.HTML(wf.Body)
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
