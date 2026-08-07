package plantmpl

import (
	"fmt"
	"html/template"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/shakestzd/wipnote/plan/blocks/svg"
)

// DependencyGraph renders the dependency graph zone showing slice
// relationships and approval status.
//
// The graph is drawn at render time, not in the browser: node placement and
// edge routing run through dagre (see plan/blocks/svg.Layout) and the result
// is emitted as a static inline <svg>. Committed plan HTML therefore shows a
// real graph with no JavaScript executed and no CDN fetch — it is legible in
// a diff viewer, in a GitHub blob view, and offline.
type DependencyGraph struct {
	Nodes []GraphNode
}

// GraphNode represents a single node in the dependency graph.
type GraphNode struct {
	Num       int
	Name      string
	Status    string // "pending", "approved", "revision", "discuss", "blocked"
	Deps      string // comma-separated dep numbers
	Files     int
	Issues    int // unresolved critic_revisions count
	Questions int // open questions count
}

// ApprovalStatusToGraphStatus maps a SliceCard approval status string to the
// status key the graph colors nodes by. Unknown values map to "pending" so
// the graph is never left with an unrecognized color key.
func ApprovalStatusToGraphStatus(approvalStatus string) string {
	switch approvalStatus {
	case "approved":
		return "approved"
	case "revision", "changes_requested", "rejected":
		return "revision"
	case "discuss":
		return "discuss"
	case "blocked":
		return "blocked"
	default:
		return "pending"
	}
}

// statusStroke maps a graph status to the theme token its node outline is
// drawn in. Every value is a bare var(--token) reference: plan pages define
// these in both the dark :root block and the [data-theme="light"] override,
// so one emitted document reads correctly in both themes.
func statusStroke(status string) string {
	switch status {
	case "approved":
		return "var(--approved)"
	case "revision":
		return "var(--revision)"
	case "discuss":
		return "var(--discuss)"
	case "blocked":
		return "var(--blocked)"
	default:
		return "var(--pending)"
	}
}

// Node box geometry. Sizes are in SVG user units, which are CSS pixels once
// the document is inlined into the plan page.
const (
	graphFontSize   = 12.0 // primary label
	graphMetaSize   = 10.0 // secondary "N files" line
	graphLineHeight = 16.0
	graphPadX       = 14.0
	graphPadY       = 12.0
	graphMinWidth   = 128.0
	// graphMaxLabelRunes caps how much of a slice title is painted into the
	// box, so one long title cannot stretch every rank. The untruncated title
	// still ships in the #graph-data island's data-name attribute, which is
	// what `wipnote relevant` greps and what the dashboard reads.
	graphMaxLabelRunes = 30
)

// truncateLabel shortens s to at most graphMaxLabelRunes runes, marking the
// cut with a single-rune ellipsis. It counts runes, not bytes, so a title
// with multi-byte characters is not cut mid-character.
func truncateLabel(s string) string {
	if utf8.RuneCountInString(s) <= graphMaxLabelRunes {
		return s
	}
	runes := []rune(s)
	return strings.TrimRight(string(runes[:graphMaxLabelRunes-1]), " ") + "…"
}

// primaryLabel is the node's headline: its slice number, title, and any
// triage counts that need a reviewer's attention.
func (n GraphNode) primaryLabel() string {
	label := "#" + strconv.Itoa(n.Num)
	if n.Name != "" {
		label += " " + truncateLabel(n.Name)
	}
	if n.Issues > 0 {
		label += fmt.Sprintf("  ⚠%d", n.Issues)
	}
	if n.Questions > 0 {
		label += fmt.Sprintf("  ?%d", n.Questions)
	}
	return label
}

// metaLabel is the node's second line: how many files the slice touches.
func (n GraphNode) metaLabel() string {
	if n.Files == 1 {
		return "1 file"
	}
	return strconv.Itoa(n.Files) + " files"
}

// depNums parses the comma-separated Deps attribute into slice numbers,
// discarding blanks and anything non-numeric. Malformed dependency text
// yields a graph with fewer edges rather than a render error — a plan is
// still worth showing when one of its dep lists is garbled.
func (n GraphNode) depNums() []int {
	if strings.TrimSpace(n.Deps) == "" {
		return nil
	}
	var out []int
	for _, part := range strings.Split(n.Deps, ",") {
		num, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		out = append(out, num)
	}
	return out
}

// nodeID is the layout key for a slice, and the suffix of the SVG element id
// each node group carries.
func nodeID(num int) string { return strconv.Itoa(num) }

// depGraphSVGID is the id of the dependency graph's root <svg>. The plan
// stylesheet, the dashboard, and `wipnote plan validate` all key off it.
const depGraphSVGID = "dep-graph-svg"

var depGraphTmpl = template.Must(
	template.ParseFS(templateFS, "templates/dependency_graph.gohtml"),
)

// depGraphView is what the zone template renders: the node data island plus
// the finished SVG document.
type depGraphView struct {
	Nodes []GraphNode
	SVG   template.HTML
}

// Render writes the dependency graph zone HTML to w.
func (g *DependencyGraph) Render(w io.Writer) error {
	markup, err := g.renderSVG()
	if err != nil {
		return err
	}
	return depGraphTmpl.Execute(w, depGraphView{Nodes: g.Nodes, SVG: template.HTML(markup)})
}

// renderSVG lays the graph out and returns the complete inline <svg> element.
// A graph with no nodes yields an empty <svg> carrying only the id, which is
// what every consumer (CSS, the dashboard, `wipnote plan validate`) keys off.
func (g *DependencyGraph) renderSVG() (string, error) {
	if len(g.Nodes) == 0 {
		var b strings.Builder
		if err := svg.WriteOpen(&b, svg.Root{ID: depGraphSVGID, Class: "dep-graph-static"}); err != nil {
			return "", err
		}
		if err := svg.WriteClose(&b); err != nil {
			return "", err
		}
		return b.String(), nil
	}

	res, err := g.layout()
	if err != nil {
		return "", err
	}

	byNum := make(map[string]GraphNode, len(g.Nodes))
	for _, n := range g.Nodes {
		byNum[nodeID(n.Num)] = n
	}

	var b strings.Builder
	if err := svg.WriteOpen(&b, svg.Root{
		ID:     depGraphSVGID,
		Class:  "dep-graph-static",
		Width:  res.Width,
		Height: res.Height,
	}); err != nil {
		return "", err
	}
	if err := svg.WriteArrowheadMarker(&b); err != nil {
		return "", err
	}

	// Edges first so node boxes paint over the line ends rather than the
	// other way round.
	if err := svg.WriteGroup(&b, svg.Group{Class: "dep-edges"}, func(w io.Writer) error {
		for _, e := range res.Edges {
			if err := svg.WriteEdgePath(w, e, "dep-edge", "var(--text-muted)"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return "", err
	}

	if err := svg.WriteGroup(&b, svg.Group{Class: "dep-nodes"}, func(w io.Writer) error {
		for _, placed := range res.Nodes {
			node := byNum[placed.ID]
			if err := writeGraphNode(w, node, placed); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return "", err
	}

	if err := svg.WriteClose(&b); err != nil {
		return "", err
	}
	return b.String(), nil
}

// layout converts the zone's nodes into a dagre layout run.
func (g *DependencyGraph) layout() (svg.LayoutResult, error) {
	present := make(map[int]bool, len(g.Nodes))
	for _, n := range g.Nodes {
		present[n.Num] = true
	}

	nodes := make([]svg.LayoutNode, 0, len(g.Nodes))
	var edges []svg.LayoutEdge
	for _, n := range g.Nodes {
		w, h := nodeBoxSize(n)
		nodes = append(nodes, svg.LayoutNode{ID: nodeID(n.Num), Width: w, Height: h})
		for _, dep := range n.depNums() {
			// A dep pointing at a slice that was deleted from the plan (or
			// never existed) is dropped rather than fatal: dagre would
			// otherwise invent a zero-sized node and place the whole graph
			// at NaN.
			if !present[dep] || dep == n.Num {
				continue
			}
			edges = append(edges, svg.LayoutEdge{From: nodeID(dep), To: nodeID(n.Num)})
		}
	}
	return svg.Layout(nodes, edges, svg.LayoutOptions{})
}

// nodeBoxSize measures a node's two label lines and returns the box that
// holds them with padding. Widths come from svg.Measure, which is a pure
// arithmetic function over the monospace stack svg.WriteText sets — so the
// box a CI runner computes is the box the browser paints.
func nodeBoxSize(n GraphNode) (width, height float64) {
	primary := svg.Measure(n.primaryLabel(), graphFontSize)
	meta := svg.Measure(n.metaLabel(), graphMetaSize)
	width = primary
	if meta > width {
		width = meta
	}
	width += graphPadX * 2
	if width < graphMinWidth {
		width = graphMinWidth
	}
	height = graphPadY*2 + graphLineHeight*2
	return width, height
}

// writeGraphNode emits one node: an anchor to the slice card, wrapping the
// box and its two label lines.
func writeGraphNode(w io.Writer, node GraphNode, placed svg.PlacedNode) error {
	// The anchor is what makes the static graph navigable with no script:
	// clicking a node jumps to the matching slice card, the same #slice-N
	// target the sidebar nav and plan review already resolve against.
	if _, err := io.WriteString(w, `<a href="#slice-`+nodeID(node.Num)+`">`); err != nil {
		return err
	}

	status := node.Status
	if status == "" {
		status = "pending"
	}
	group := svg.Group{
		ID:    "graph-node-" + nodeID(node.Num),
		Class: "dep-node dep-node-" + status,
	}
	err := svg.WriteGroup(w, group, func(w io.Writer) error {
		if err := svg.WriteRect(w, svg.Rect{
			X:      placed.Left(),
			Y:      placed.Top(),
			Width:  placed.Width,
			Height: placed.Height,
			RX:     6,
			Class:  "dep-node-box",
			Fill:   "var(--bg-input)",
			Stroke: statusStroke(status),
		}); err != nil {
			return err
		}
		// Two baselines inside the box: the content block is
		// 2*graphLineHeight tall and vertically centred, and each baseline
		// sits one font-size below its line's top edge.
		contentTop := placed.Top() + (placed.Height-graphLineHeight*2)/2
		if err := svg.WriteText(w, svg.Text{
			X:        placed.X,
			Y:        contentTop + graphFontSize,
			Label:    node.primaryLabel(),
			Class:    "dep-node-label",
			Anchor:   "middle",
			FontSize: graphFontSize,
			Fill:     "var(--text)",
		}); err != nil {
			return err
		}
		return svg.WriteText(w, svg.Text{
			X:        placed.X,
			Y:        contentTop + graphLineHeight + graphMetaSize,
			Label:    node.metaLabel(),
			Class:    "dep-node-meta",
			Anchor:   "middle",
			FontSize: graphMetaSize,
			Fill:     "var(--text-muted)",
		})
	})
	if err != nil {
		return err
	}

	_, err = io.WriteString(w, "</a>")
	return err
}

// SVG lays the graph out and returns the finished inline <svg>, for the plan
// page template — which renders the dependency zone inline rather than
// through depGraphTmpl. The (value, error) signature is deliberate: both
// text/template and html/template abort execution when a called method's
// second return value is non-nil, so a graph that cannot be laid out fails
// the plan render loudly instead of emitting a page with a silently blank
// graph zone.
func (g *DependencyGraph) SVG() (template.HTML, error) {
	markup, err := g.renderSVG()
	if err != nil {
		return "", err
	}
	return template.HTML(markup), nil
}
