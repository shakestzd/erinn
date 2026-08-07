package blocks

import (
	"bytes"
	"html/template"
	"io"
	"strconv"

	"github.com/shakestzd/wipnote/plan/blocks/svg"
)

// Diagram layout constants, in SVG user units (px). Node height/padding are
// tuned to the block's monospace label font (svg.DefaultFontSize); the gap
// between nodes reserves room for a drawn edge with its arrowhead, not just
// whitespace.
const (
	diagramNodeHeight = 36.0
	diagramNodePad    = 14.0
	diagramEdgeGapLR  = 40.0
	diagramEdgeGapTB  = 32.0
	diagramMargin     = 6.0
)

// Diagram renders a flow diagram: an ordered sequence of steps connected by
// drawn SVG edges terminated in the shared arrowhead marker (plan/blocks/svg).
// Direction is "lr" (left-to-right, default) or "tb" (top-to-bottom).
//
// This replaces the block's original Mermaid-free, pure-HTML/CSS rendering
// (bordered divs joined by a Unicode arrow glyph): the corpus audit behind
// feat-47793a68 found zero authored blocks anywhere containing a real
// <svg>/<canvas> element, and every diagram edge was a glyph, not a drawn
// line. Node boxes and labels are measured deterministically via
// svg.MeasureDefault (no client-side layout engine, no font-file lookup), so
// rendering is still exact and reproducible server-side.
type Diagram struct {
	Title     string
	Steps     []string
	Direction string
}

// Render writes the diagram block HTML to w.
func (d *Diagram) Render(w io.Writer) error {
	dir := d.Direction
	if dir != "tb" {
		dir = "lr"
	}
	svgHTML, err := d.renderSVG(dir == "tb")
	if err != nil {
		return err
	}
	return blockTmpl.ExecuteTemplate(w, "diagram", struct {
		Title string
		Dir   string
		SVG   template.HTML
	}{Title: d.Title, Dir: dir, SVG: svgHTML})
}

// diagramNode is one measured, positioned step box in the flow.
type diagramNode struct {
	Label               string
	X, Y, Width, Height float64
}

// renderSVG draws the measured/positioned steps as rounded rects with
// centered labels, connected by drawn <path> edges terminated in the shared
// arrowhead marker — never a Unicode arrow glyph. Returns empty when there
// are no steps, matching the previous renderer's graceful degradation.
func (d *Diagram) renderSVG(vertical bool) (template.HTML, error) {
	nodes, width, height := layoutDiagram(d.Steps, vertical)
	if len(nodes) == 0 {
		return "", nil
	}
	var buf bytes.Buffer
	if err := svg.WriteOpen(&buf, svg.Root{Width: width, Height: height, Class: "diagram-canvas"}); err != nil {
		return "", err
	}
	if err := svg.WriteArrowheadMarker(&buf); err != nil {
		return "", err
	}
	for i, n := range nodes {
		if i > 0 {
			if err := writeDiagramEdge(&buf, nodes[i-1], n, vertical); err != nil {
				return "", err
			}
		}
		if err := writeDiagramNode(&buf, n); err != nil {
			return "", err
		}
	}
	if err := svg.WriteClose(&buf); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

// layoutDiagram measures each step label (deterministic monospace width, see
// svg.Measure) and packs the nodes edge-to-edge — left-to-right or
// top-to-bottom — with a fixed gap reserved for the connecting arrow.
// Returns the positioned nodes and the overall canvas size.
func layoutDiagram(steps []string, vertical bool) ([]diagramNode, float64, float64) {
	if vertical {
		return layoutDiagramVertical(steps)
	}
	return layoutDiagramHorizontal(steps)
}

// layoutDiagramHorizontal positions nodes left-to-right at a fixed height.
func layoutDiagramHorizontal(steps []string) ([]diagramNode, float64, float64) {
	nodes := make([]diagramNode, len(steps))
	x := diagramMargin
	for i, s := range steps {
		w := svg.MeasureDefault(s) + diagramNodePad*2
		nodes[i] = diagramNode{Label: s, X: x, Y: diagramMargin, Width: w, Height: diagramNodeHeight}
		x += w + diagramEdgeGapLR
	}
	if len(steps) == 0 {
		return nodes, 0, 0
	}
	return nodes, x - diagramEdgeGapLR + diagramMargin, diagramNodeHeight + diagramMargin*2
}

// layoutDiagramVertical positions nodes top-to-bottom, left-aligned, each
// keeping its own measured width.
func layoutDiagramVertical(steps []string) ([]diagramNode, float64, float64) {
	nodes := make([]diagramNode, len(steps))
	y, maxW := diagramMargin, 0.0
	for i, s := range steps {
		w := svg.MeasureDefault(s) + diagramNodePad*2
		nodes[i] = diagramNode{Label: s, X: diagramMargin, Y: y, Width: w, Height: diagramNodeHeight}
		if w > maxW {
			maxW = w
		}
		y += diagramNodeHeight + diagramEdgeGapTB
	}
	if len(steps) == 0 {
		return nodes, 0, 0
	}
	return nodes, maxW + diagramMargin*2, y - diagramEdgeGapTB + diagramMargin
}

// writeDiagramNode draws one step as a rounded rect with a centered label.
func writeDiagramNode(w io.Writer, n diagramNode) error {
	if err := svg.WriteRect(w, svg.Rect{
		X: n.X, Y: n.Y, Width: n.Width, Height: n.Height, RX: 6,
		Class: "diagram-node-rect", Fill: "var(--bg-input)", Stroke: "var(--border)",
	}); err != nil {
		return err
	}
	return svg.WriteText(w, svg.Text{
		X: n.X + n.Width/2, Y: n.Y + n.Height/2 + 4,
		Label: n.Label, Class: "diagram-node-label", Fill: "var(--text)", Anchor: "middle",
	})
}

// writeDiagramEdge draws the connecting line from the trailing edge of `from`
// to the leading edge of `to`, terminated in the shared arrowhead marker.
func writeDiagramEdge(w io.Writer, from, to diagramNode, vertical bool) error {
	var x1, y1, x2, y2 float64
	if vertical {
		x1, y1 = from.X+from.Width/2, from.Y+from.Height
		x2, y2 = to.X+to.Width/2, to.Y
	} else {
		x1, y1 = from.X+from.Width, from.Y+from.Height/2
		x2, y2 = to.X, to.Y+to.Height/2
	}
	d := "M" + fnum(x1) + " " + fnum(y1) + " L" + fnum(x2) + " " + fnum(y2)
	return svg.WritePath(w, svg.Path{D: d, Class: "diagram-edge", Stroke: "var(--accent)", MarkerEnd: svg.ArrowheadMarkerRef})
}

// fnum renders a path coordinate the same way the svg package's internal
// formatter does: plain fixed-point, no exponential notation, no trailing
// zeros. Duplicated here (rather than exported from svg) because it is only
// ever needed to hand-assemble a Path.D string — every other coordinate in
// this package goes through svg's own numeric fields directly.
func fnum(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
