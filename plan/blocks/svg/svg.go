// Package svg provides low-level SVG emission primitives for wipnote's plan
// diagram and dependency-graph renderers: rect, text, line, path, and group
// elements, plus one shared arrowhead <marker> definition. It is a LEAF
// package (only the standard library — no ajstarks/svgo or similar), so it
// can sit underneath plan/blocks and any future diagram/dependency-graph
// renderer without creating an import cycle. See plan/blocks/blocks.go for
// the same leaf-package rationale applied one level up.
//
// Every label passed to WriteText is run through html.EscapeString and
// always emitted as a single contiguous <text> node — never split into
// per-character <tspan>s. This is a structural guard against bug-37801e41, a
// real regression where a plan renderer mangled literal angle brackets in
// slice titles: `wipnote relevant` runs `rg --type html` over rendered plan
// HTML, and `wipnote find plans` parses the DOM by text content, so a
// per-character split would silently break phrase matching for every
// existing plan with no error anywhere.
//
// Colors are never emitted as literal hex/rgb/hsl or named CSS colors: every
// Fill/Stroke value must be a bare CSS custom-property reference —
// var(--token), no fallback argument — so the same markup renders correctly
// in both the light and dark dashboard themes from one pass. This mirrors
// plan/blocks' wireframe.go token contract (wfTokenValRe) one level up.
package svg

import (
	"fmt"
	"html"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// themeColorRe matches a bare CSS custom-property reference with no fallback
// argument, e.g. var(--accent). Fallbacks are disallowed for the same reason
// plan/blocks' wireframe sanitizer disallows them: a raw color must never be
// able to ride in via var(--x, #fff).
var themeColorRe = regexp.MustCompile(`(?i)^var\(--[a-z0-9-]+\)$`)

// checkColor rejects any Fill/Stroke value that isn't a bare theme-variable
// reference. Empty is fine — it means the attribute is simply omitted.
func checkColor(field, v string) error {
	if v == "" {
		return nil
	}
	if !themeColorRe.MatchString(v) {
		return fmt.Errorf("svg: %s %q must be a bare var(--token) theme reference (no literal color, no fallback)", field, v)
	}
	return nil
}

// formatNum renders a coordinate/length as SVG expects: plain fixed-point,
// no exponential notation (which older SVG parsers don't accept) and no
// trailing zeros.
func formatNum(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// writeRawAttr appends a pre-escaped ` name="value"` pair to b.
func writeRawAttr(b *strings.Builder, name, value string) {
	b.WriteByte(' ')
	b.WriteString(name)
	b.WriteString(`="`)
	b.WriteString(value)
	b.WriteByte('"')
}

// writeAttr appends ` name="value"` with value HTML-escaped, skipping the
// attribute entirely when value is empty.
func writeAttr(b *strings.Builder, name, value string) {
	if value == "" {
		return
	}
	writeRawAttr(b, name, html.EscapeString(value))
}

// writeNumAttr appends ` name="value"` for a numeric attribute. Unlike
// writeAttr it always writes: 0 is a meaningful coordinate, not an absent one.
func writeNumAttr(b *strings.Builder, name string, v float64) {
	writeRawAttr(b, name, formatNum(v))
}

// Root describes the opening <svg> tag a document's elements are wrapped in.
// ViewBox defaults to "0 0 Width Height" when left empty.
type Root struct {
	Width, Height float64
	ViewBox       string
	Class         string
	ID            string
}

// WriteOpen writes the root <svg> open tag, including the xmlns namespace
// (so the fragment stays well-formed if ever saved or served as a standalone
// .svg file) and a viewBox. Pair with WriteClose.
func WriteOpen(w io.Writer, r Root) error {
	vb := r.ViewBox
	if vb == "" {
		vb = fmt.Sprintf("0 0 %s %s", formatNum(r.Width), formatNum(r.Height))
	}
	var b strings.Builder
	// id leads, ahead of the namespace: host pages and validators match on
	// the literal `<svg id="…"` prefix, and attribute order is otherwise
	// meaningless to an SVG parser.
	b.WriteString("<svg")
	writeAttr(&b, "id", r.ID)
	b.WriteString(` xmlns="http://www.w3.org/2000/svg"`)
	writeNumAttr(&b, "width", r.Width)
	writeNumAttr(&b, "height", r.Height)
	writeAttr(&b, "viewBox", vb)
	writeAttr(&b, "class", r.Class)
	b.WriteString(">")
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteClose writes the root </svg> close tag.
func WriteClose(w io.Writer) error {
	_, err := io.WriteString(w, "</svg>")
	return err
}

// Rect describes an SVG <rect>.
type Rect struct {
	X, Y, Width, Height float64
	RX                  float64 // corner radius; omitted when zero
	Class               string
	Fill                string // bare var(--token), or empty
	Stroke              string // bare var(--token), or empty
	ID                  string
}

// WriteRect writes a <rect> element to w.
func WriteRect(w io.Writer, r Rect) error {
	if err := checkColor("Fill", r.Fill); err != nil {
		return err
	}
	if err := checkColor("Stroke", r.Stroke); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("<rect")
	writeAttr(&b, "id", r.ID)
	writeAttr(&b, "class", r.Class)
	writeNumAttr(&b, "x", r.X)
	writeNumAttr(&b, "y", r.Y)
	writeNumAttr(&b, "width", r.Width)
	writeNumAttr(&b, "height", r.Height)
	if r.RX != 0 {
		writeNumAttr(&b, "rx", r.RX)
	}
	writeAttr(&b, "fill", r.Fill)
	writeAttr(&b, "stroke", r.Stroke)
	b.WriteString("/>")
	_, err := io.WriteString(w, b.String())
	return err
}

// Text describes an SVG <text> label. FontSize defaults to DefaultFontSize
// when zero; whatever value is set here MUST match the fontSize passed to
// Measure for the same label, or the two will disagree about width.
type Text struct {
	X, Y     float64
	Label    string
	Class    string
	Fill     string // bare var(--token), or empty
	Anchor   string // text-anchor: "start" (default), "middle", or "end"
	FontSize float64
	ID       string
}

// WriteText writes a <text> element to w. Label is always HTML-escaped and
// always emitted as one contiguous text node — see the package doc comment
// for why per-character <tspan>s are forbidden.
func WriteText(w io.Writer, t Text) error {
	if err := checkColor("Fill", t.Fill); err != nil {
		return err
	}
	fs := t.FontSize
	if fs == 0 {
		fs = DefaultFontSize
	}
	var b strings.Builder
	b.WriteString("<text")
	writeAttr(&b, "id", t.ID)
	writeAttr(&b, "class", t.Class)
	writeNumAttr(&b, "x", t.X)
	writeNumAttr(&b, "y", t.Y)
	if t.Anchor != "" {
		writeAttr(&b, "text-anchor", t.Anchor)
	}
	writeAttr(&b, "font-family", FontFamily)
	writeNumAttr(&b, "font-size", fs)
	writeAttr(&b, "fill", t.Fill)
	b.WriteString(">")
	b.WriteString(html.EscapeString(t.Label))
	b.WriteString("</text>")
	_, err := io.WriteString(w, b.String())
	return err
}

// Line describes an SVG <line>, the primitive used to draw plan and
// dependency-graph edges. Set MarkerEnd to ArrowheadMarkerRef to terminate
// the line in the shared arrowhead (see WriteArrowheadMarker).
type Line struct {
	X1, Y1, X2, Y2 float64
	Class          string
	Stroke         string // bare var(--token), or empty
	MarkerEnd      string
	ID             string
}

// WriteLine writes a <line> element to w.
func WriteLine(w io.Writer, l Line) error {
	if err := checkColor("Stroke", l.Stroke); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("<line")
	writeAttr(&b, "id", l.ID)
	writeAttr(&b, "class", l.Class)
	writeNumAttr(&b, "x1", l.X1)
	writeNumAttr(&b, "y1", l.Y1)
	writeNumAttr(&b, "x2", l.X2)
	writeNumAttr(&b, "y2", l.Y2)
	writeAttr(&b, "stroke", l.Stroke)
	writeAttr(&b, "marker-end", l.MarkerEnd)
	b.WriteString("/>")
	_, err := io.WriteString(w, b.String())
	return err
}

// Path describes an SVG <path>. D is raw path data (e.g. "M0 0 L10 10");
// callers are expected to build it from numeric coordinates, but it still
// passes through the same attribute escaper as every other value.
type Path struct {
	D         string
	Class     string
	Fill      string // bare var(--token), or empty
	Stroke    string // bare var(--token), or empty
	MarkerEnd string
	ID        string
}

// WritePath writes a <path> element to w.
func WritePath(w io.Writer, p Path) error {
	if err := checkColor("Fill", p.Fill); err != nil {
		return err
	}
	if err := checkColor("Stroke", p.Stroke); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("<path")
	writeAttr(&b, "id", p.ID)
	writeAttr(&b, "class", p.Class)
	writeAttr(&b, "d", p.D)
	writeAttr(&b, "fill", p.Fill)
	writeAttr(&b, "stroke", p.Stroke)
	writeAttr(&b, "marker-end", p.MarkerEnd)
	b.WriteString("/>")
	_, err := io.WriteString(w, b.String())
	return err
}

// Group describes an SVG <g> wrapper — the SVG equivalent of a <div>: a
// transform or class applied once to a set of children instead of
// duplicating it onto every child element.
type Group struct {
	Class     string
	Transform string
	ID        string
}

// WriteGroup writes the <g> open tag, invokes children (which may write any
// number of nested elements, including further groups), then writes the
// matching </g> close tag. children may be nil for an empty group.
func WriteGroup(w io.Writer, g Group, children func(io.Writer) error) error {
	var b strings.Builder
	b.WriteString("<g")
	writeAttr(&b, "id", g.ID)
	writeAttr(&b, "class", g.Class)
	writeAttr(&b, "transform", g.Transform)
	b.WriteString(">")
	if _, err := io.WriteString(w, b.String()); err != nil {
		return err
	}
	if children != nil {
		if err := children(w); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "</g>")
	return err
}

// ArrowheadMarkerID is the id every wipnote SVG diagram/dependency-graph
// consumer references via marker-end. Use ArrowheadMarkerRef rather than
// building the "url(#...)" string by hand.
const ArrowheadMarkerID = "wipnote-arrowhead"

// ArrowheadMarkerRef is the ready-to-use marker-end attribute value for the
// shared arrowhead: MarkerEnd: svg.ArrowheadMarkerRef.
const ArrowheadMarkerRef = "url(#" + ArrowheadMarkerID + ")"

// WriteArrowheadMarker writes the single shared <defs><marker>...</marker>
// block backing every arrow in wipnote's SVG diagrams. Callers write it
// exactly once per document (inside the root <svg>, anywhere before it is
// first referenced), then point any Line or Path at it via
// MarkerEnd: ArrowheadMarkerRef. Do not hand-roll a second marker — one
// definition keeps arrowhead geometry, and its theme-aware fill, consistent
// everywhere an arrow is drawn.
//
// The fill is a bare theme variable (no literal color, no fallback) so the
// arrowhead follows the same light/dark contract as every other primitive in
// this package. It relies on --accent already being in scope, which holds
// for every host page (plan, recap, dashboard) this package's output is
// embedded into.
func WriteArrowheadMarker(w io.Writer) error {
	const markup = `<defs><marker id="` + ArrowheadMarkerID + `" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0 0 L10 5 L0 10 z" fill="var(--accent)"/></marker></defs>`
	_, err := io.WriteString(w, markup)
	return err
}
