package svg

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"

	"github.com/dop251/goja"
)

// dagreJS is dagre 0.8.5's UMD bundle, vendored verbatim (MIT — the license
// header is preserved inside the file). It is the same bundle d2 embeds for
// its own dagre layout engine.
//
// It is run inside a goja interpreter, not a browser: the bundle's UMD
// preamble falls through its window/global/self probes to `this`, which under
// goja is globalThis, so `globalThis.dagre` ends up defined with no DOM, no
// window, and no module loader in scope. The only scaffolding it needs beyond
// that is dagreSetupJS and a console shim.
//
//go:embed dagre.js
var dagreJS string

// dagreSetupJS allocates the single graph object each layout run populates.
// Both default-label functions are required: dagre reads a label object off
// every node and edge, and graphlib returns undefined for them unless a
// default factory is registered.
const dagreSetupJS = `
var g = new dagre.graphlib.Graph({ compound: true, multigraph: true });
g.setDefaultNodeLabel(function () { return {}; });
g.setDefaultEdgeLabel(function () { return {}; });
`

// extractJS pulls the whole layout result back across the Go/JS boundary in a
// single round-trip. dagre stores geometry as plain JS objects, so the two
// Array.map calls below re-shape them into exactly the fields Go needs and
// JSON.stringify serializes the lot at once. The obvious alternative — reading
// g.node(id) / g.edge(e) one at a time from Go — costs a separate
// goja→Go value conversion per node and per edge and is measurably slower.
const extractJS = `JSON.stringify({
  w: g.graph().width,
  h: g.graph().height,
  nodes: g.nodes().map(function (id) {
    var n = g.node(id);
    return { id: id, x: n.x, y: n.y, width: n.width, height: n.height };
  }),
  edges: g.edges().map(function (e) {
    return { v: e.v, w: e.w, points: g.edge(e).points };
  })
})`

var (
	dagreProgOnce sync.Once
	dagreProg     *goja.Program
	dagreProgErr  error
)

// dagreProgram compiles the embedded bundle to goja bytecode exactly once per
// process. Compilation dominates the cost of a layout run (tens of ms against
// a couple of ms to instantiate a runtime from the compiled program), so it
// must never happen per render. The compiled *goja.Program is immutable and
// safe to share; the *goja.Runtime built from it in Layout is not, which is
// why each call gets its own.
func dagreProgram() (*goja.Program, error) {
	dagreProgOnce.Do(func() {
		dagreProg, dagreProgErr = goja.Compile("dagre.js", dagreJS, false)
	})
	return dagreProg, dagreProgErr
}

// RankDir values accepted by LayoutOptions.RankDir, matching dagre's own
// rankdir vocabulary: top-to-bottom, bottom-to-top, left-to-right,
// right-to-left.
const (
	RankDirTB = "TB"
	RankDirBT = "BT"
	RankDirLR = "LR"
	RankDirRL = "RL"
)

// LayoutNode is one box to place. Width and Height are the box's full extent
// in SVG user units; the caller sizes them (typically from Measure) because
// this package never renders text to discover its own metrics.
type LayoutNode struct {
	ID            string
	Width, Height float64
}

// LayoutEdge is one directed dependency, From depends-on-by To — i.e. the
// arrow points From → To.
type LayoutEdge struct {
	From, To string
}

// LayoutOptions mirrors the subset of dagre's graph configuration wipnote
// uses. Zero values fall back to LayoutDefaults, so the zero LayoutOptions is
// a usable top-to-bottom layout.
type LayoutOptions struct {
	RankDir string  // one of RankDirTB/BT/LR/RL; default RankDirTB
	NodeSep float64 // gap between adjacent nodes in the same rank
	RankSep float64 // gap between ranks
	EdgeSep float64 // gap between adjacent edges
	MarginX float64 // horizontal padding added around the whole drawing
	MarginY float64 // vertical padding added around the whole drawing
}

// LayoutDefaults are the option values used for any field left zero. They are
// tuned for the plan dependency graph: enough rank separation that a
// rank-skipping edge has room to route around the boxes it passes.
var LayoutDefaults = LayoutOptions{
	RankDir: RankDirTB,
	NodeSep: 40,
	RankSep: 56,
	EdgeSep: 16,
	MarginX: 24,
	MarginY: 24,
}

// withDefaults returns o with every zero field replaced by its LayoutDefaults
// counterpart.
func (o LayoutOptions) withDefaults() LayoutOptions {
	if o.RankDir == "" {
		o.RankDir = LayoutDefaults.RankDir
	}
	if o.NodeSep == 0 {
		o.NodeSep = LayoutDefaults.NodeSep
	}
	if o.RankSep == 0 {
		o.RankSep = LayoutDefaults.RankSep
	}
	if o.EdgeSep == 0 {
		o.EdgeSep = LayoutDefaults.EdgeSep
	}
	if o.MarginX == 0 {
		o.MarginX = LayoutDefaults.MarginX
	}
	if o.MarginY == 0 {
		o.MarginY = LayoutDefaults.MarginY
	}
	return o
}

// Point is a single coordinate on a routed edge polyline.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// PlacedNode is a LayoutNode with its assigned position. X and Y are the
// box's CENTER, matching dagre's convention — use Left/Top for the top-left
// corner an SVG <rect> wants.
type PlacedNode struct {
	ID     string  `json:"id"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Left is the x coordinate of the box's left edge.
func (n PlacedNode) Left() float64 { return n.X - n.Width/2 }

// Top is the y coordinate of the box's top edge.
func (n PlacedNode) Top() float64 { return n.Y - n.Height/2 }

// Right is the x coordinate of the box's right edge.
func (n PlacedNode) Right() float64 { return n.X + n.Width/2 }

// Bottom is the y coordinate of the box's bottom edge.
func (n PlacedNode) Bottom() float64 { return n.Y + n.Height/2 }

// RoutedEdge is a LayoutEdge with the polyline dagre routed for it. Points is
// ordered From → To and always has at least two entries; for edges that skip
// ranks it contains the intermediate bend points that steer the line clear of
// the nodes in between.
type RoutedEdge struct {
	From, To string
	Points   []Point
}

// LayoutResult is a complete placement. Width and Height are the drawing's
// full extent including the configured margins, ready to use as the root
// <svg> width/height and viewBox.
type LayoutResult struct {
	Width, Height float64
	Nodes         []PlacedNode
	Edges         []RoutedEdge
}

// Node returns the placed node with the given id.
func (r LayoutResult) Node(id string) (PlacedNode, bool) {
	for _, n := range r.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return PlacedNode{}, false
}

// Layout runs dagre over nodes/edges and returns their placement.
//
// Nodes and Edges come back in the order they were passed in, not in dagre's
// internal iteration order, so the emitted SVG is byte-stable across runs and
// across machines for the same input.
//
// Cost grows superlinearly with node count (roughly O(n^1.3) in the sizes
// wipnote renders): single-digit milliseconds for a handful of nodes, around
// a tenth of a second at a few dozen. One call should cover one plan, not a
// whole corpus.
func Layout(nodes []LayoutNode, edges []LayoutEdge, opts LayoutOptions) (LayoutResult, error) {
	if len(nodes) == 0 {
		return LayoutResult{}, fmt.Errorf("svg: Layout needs at least one node")
	}
	index := make(map[string]int, len(nodes))
	for i, n := range nodes {
		if n.ID == "" {
			return LayoutResult{}, fmt.Errorf("svg: Layout node %d has an empty ID", i)
		}
		if _, dup := index[n.ID]; dup {
			return LayoutResult{}, fmt.Errorf("svg: Layout has duplicate node ID %q", n.ID)
		}
		if n.Width <= 0 || n.Height <= 0 {
			return LayoutResult{}, fmt.Errorf("svg: Layout node %q needs a positive width and height (got %vx%v)", n.ID, n.Width, n.Height)
		}
		index[n.ID] = i
	}
	// dagre silently invents a default-labelled node for any edge endpoint it
	// has not seen, which then lays out at NaN because it has no dimensions.
	// Rejecting the dangling edge here turns that into an error the caller
	// can act on instead of a graph full of NaN coordinates.
	for i, e := range edges {
		if _, ok := index[e.From]; !ok {
			return LayoutResult{}, fmt.Errorf("svg: Layout edge %d references unknown source node %q", i, e.From)
		}
		if _, ok := index[e.To]; !ok {
			return LayoutResult{}, fmt.Errorf("svg: Layout edge %d references unknown target node %q", i, e.To)
		}
	}

	prog, err := dagreProgram()
	if err != nil {
		return LayoutResult{}, fmt.Errorf("svg: compiling dagre.js: %w", err)
	}

	vm := goja.New()
	if err := installConsole(vm); err != nil {
		return LayoutResult{}, fmt.Errorf("svg: installing console shim: %w", err)
	}
	if _, err := vm.RunProgram(prog); err != nil {
		return LayoutResult{}, fmt.Errorf("svg: evaluating dagre.js: %w", err)
	}
	if _, err := vm.RunString(dagreSetupJS); err != nil {
		return LayoutResult{}, fmt.Errorf("svg: dagre setup: %w", err)
	}
	if _, err := vm.RunString(buildGraphJS(nodes, edges, opts.withDefaults())); err != nil {
		return LayoutResult{}, fmt.Errorf("svg: loading graph: %w", err)
	}
	if _, err := vm.RunString("dagre.layout(g)"); err != nil {
		return LayoutResult{}, fmt.Errorf("svg: dagre.layout: %w", err)
	}
	raw, err := vm.RunString(extractJS)
	if err != nil {
		return LayoutResult{}, fmt.Errorf("svg: extracting layout: %w", err)
	}

	var out struct {
		W     float64      `json:"w"`
		H     float64      `json:"h"`
		Nodes []PlacedNode `json:"nodes"`
		Edges []struct {
			V      string  `json:"v"`
			W      string  `json:"w"`
			Points []Point `json:"points"`
		} `json:"edges"`
	}
	if err := json.Unmarshal([]byte(raw.String()), &out); err != nil {
		return LayoutResult{}, fmt.Errorf("svg: decoding layout: %w", err)
	}

	res := LayoutResult{Width: snap(out.W), Height: snap(out.H), Nodes: make([]PlacedNode, len(nodes))}
	seen := make([]bool, len(nodes))
	for _, pn := range out.Nodes {
		i, ok := index[pn.ID]
		if !ok {
			// A node dagre invented for itself; the dangling-edge guard above
			// should make this unreachable.
			continue
		}
		pn.X, pn.Y = snap(pn.X), snap(pn.Y)
		pn.Width, pn.Height = snap(pn.Width), snap(pn.Height)
		res.Nodes[i] = pn
		seen[i] = true
	}
	for i, ok := range seen {
		if !ok {
			return LayoutResult{}, fmt.Errorf("svg: dagre did not place node %q", nodes[i].ID)
		}
	}

	routed := make(map[[2]string][]Point, len(out.Edges))
	for _, e := range out.Edges {
		for i := range e.Points {
			e.Points[i].X, e.Points[i].Y = snap(e.Points[i].X), snap(e.Points[i].Y)
		}
		routed[[2]string{e.V, e.W}] = e.Points
	}
	res.Edges = make([]RoutedEdge, 0, len(edges))
	for _, e := range edges {
		pts, ok := routed[[2]string{e.From, e.To}]
		if !ok {
			return LayoutResult{}, fmt.Errorf("svg: dagre did not route edge %q -> %q", e.From, e.To)
		}
		res.Edges = append(res.Edges, RoutedEdge{From: e.From, To: e.To, Points: pts})
	}
	return res, nil
}

// buildGraphJS renders the node/edge population as a JS source fragment. Every
// identifier goes through json.Marshal so a slice title or id containing a
// quote or backslash cannot terminate the string literal it sits in.
func buildGraphJS(nodes []LayoutNode, edges []LayoutEdge, opts LayoutOptions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "g.setGraph({rankdir:%q, nodesep:%s, ranksep:%s, edgesep:%s, marginx:%s, marginy:%s});\n",
		opts.RankDir,
		formatNum(opts.NodeSep), formatNum(opts.RankSep), formatNum(opts.EdgeSep),
		formatNum(opts.MarginX), formatNum(opts.MarginY))
	for _, n := range nodes {
		fmt.Fprintf(&b, "g.setNode(%s, {width:%s, height:%s});\n", jsString(n.ID), formatNum(n.Width), formatNum(n.Height))
	}
	for _, e := range edges {
		fmt.Fprintf(&b, "g.setEdge(%s, %s, {});\n", jsString(e.From), jsString(e.To))
	}
	return b.String()
}

// snap rounds a layout coordinate to two decimal places. dagre works in
// float64 and hands back values like 150.79999999999998; a hundredth of a
// pixel is far below anything a browser can paint, and the noise would
// otherwise land verbatim in committed plan HTML and churn the diff on every
// re-render.
func snap(v float64) float64 {
	return math.Round(v*100) / 100
}

// jsString quotes s as a JS string literal.
func jsString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		// json.Marshal of a string only fails on invalid UTF-8, which it
		// replaces rather than erroring; this branch is defensive.
		return `""`
	}
	return string(b)
}

// installConsole gives the bundle the console.log/console.error it expects to
// exist. dagre only reaches for it on internal errors, but an undefined
// `console` turns those into an unrelated ReferenceError that hides the real
// failure. Output is discarded: a layout run is not a place to write to the
// process's stdio.
func installConsole(vm *goja.Runtime) error {
	noop := func(goja.FunctionCall) goja.Value { return goja.Undefined() }
	console := vm.NewObject()
	if err := console.Set("log", noop); err != nil {
		return err
	}
	if err := console.Set("error", noop); err != nil {
		return err
	}
	if err := console.Set("warn", noop); err != nil {
		return err
	}
	return vm.Set("console", console)
}

// EdgePathD builds the SVG path data for a routed edge: a move to the first
// point followed by a line to each subsequent one. Feed it to Path.D.
func EdgePathD(points []Point) string {
	if len(points) == 0 {
		return ""
	}
	var b strings.Builder
	for i, p := range points {
		if i == 0 {
			b.WriteString("M")
		} else {
			b.WriteString(" L")
		}
		b.WriteString(formatNum(p.X))
		b.WriteByte(' ')
		b.WriteString(formatNum(p.Y))
	}
	return b.String()
}

// WriteEdgePath writes a routed edge as a <path>, terminated in the shared
// arrowhead. It is the one-call form of Path{D: EdgePathD(e.Points), ...}.
func WriteEdgePath(w io.Writer, e RoutedEdge, class, stroke string) error {
	return WritePath(w, Path{
		D:         EdgePathD(e.Points),
		Class:     class,
		Fill:      "",
		Stroke:    stroke,
		MarkerEnd: ArrowheadMarkerRef,
	})
}
