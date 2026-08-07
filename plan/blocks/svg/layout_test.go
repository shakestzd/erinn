package svg_test

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/blocks/svg"
)

// chainNodes/chainEdges build an n-node dependency chain with a few
// rank-skipping edges, the shape a real plan's slice graph takes.
func chainNodes(n int) []svg.LayoutNode {
	nodes := make([]svg.LayoutNode, n)
	for i := range nodes {
		nodes[i] = svg.LayoutNode{ID: fmt.Sprintf("n%d", i), Width: 160, Height: 70}
	}
	return nodes
}

func chainEdges(n int) []svg.LayoutEdge {
	var edges []svg.LayoutEdge
	for i := 0; i < n-1; i++ {
		edges = append(edges, svg.LayoutEdge{From: fmt.Sprintf("n%d", i), To: fmt.Sprintf("n%d", i+1)})
	}
	if n >= 4 {
		edges = append(edges, svg.LayoutEdge{From: "n0", To: fmt.Sprintf("n%d", n-1)})
	}
	if n >= 6 {
		edges = append(edges, svg.LayoutEdge{From: "n1", To: fmt.Sprintf("n%d", n-2)})
	}
	return edges
}

// planGraph1670cacd is the real dependency shape of wipnote's plan-1670cacd.
// slice-6 was deleted, so the surviving slices are 1,2,3,4,5,7,8,9,10. The
// slice-1 -> slice-8 edge is the interesting one: slice-8 sits three ranks
// below slice-1, so the edge has to route past the boxes for slice-2 and
// slice-3 rather than through them.
func planGraph1670cacd() ([]svg.LayoutNode, []svg.LayoutEdge) {
	ids := []string{"slice-1", "slice-2", "slice-3", "slice-4", "slice-5", "slice-7", "slice-8", "slice-9", "slice-10"}
	nodes := make([]svg.LayoutNode, 0, len(ids))
	for _, id := range ids {
		nodes = append(nodes, svg.LayoutNode{ID: id, Width: 160, Height: 70})
	}
	deps := map[string][]string{
		"slice-2":  {"slice-1"},
		"slice-3":  {"slice-1", "slice-2"},
		"slice-4":  {"slice-3"},
		"slice-5":  {"slice-4"},
		"slice-7":  {"slice-4", "slice-5"},
		"slice-8":  {"slice-1", "slice-2", "slice-3"},
		"slice-9":  {"slice-2", "slice-3", "slice-4"},
		"slice-10": {"slice-5", "slice-7", "slice-8", "slice-9"},
	}
	var edges []svg.LayoutEdge
	for _, id := range ids {
		for _, dep := range deps[id] {
			edges = append(edges, svg.LayoutEdge{From: dep, To: id})
		}
	}
	return nodes, edges
}

// ---------------------------------------------------------------------------
// TestLayout_DagreCoordinates — dagre runs under goja and produces a sane,
// deterministic placement.
// ---------------------------------------------------------------------------

func TestLayout_DagreCoordinates(t *testing.T) {
	nodes := []svg.LayoutNode{
		{ID: "a", Width: 120, Height: 60},
		{ID: "b", Width: 120, Height: 60},
		{ID: "c", Width: 120, Height: 60},
	}
	edges := []svg.LayoutEdge{{From: "a", To: "b"}, {From: "b", To: "c"}}

	res, err := svg.Layout(nodes, edges, svg.LayoutOptions{})
	if err != nil {
		t.Fatalf("Layout: %v", err)
	}

	// Results come back in input order, not dagre's iteration order.
	if len(res.Nodes) != 3 {
		t.Fatalf("got %d placed nodes, want 3", len(res.Nodes))
	}
	for i, want := range []string{"a", "b", "c"} {
		if res.Nodes[i].ID != want {
			t.Errorf("Nodes[%d].ID = %q, want %q (input order not preserved)", i, res.Nodes[i].ID, want)
		}
	}

	// Every node keeps the size it was given and lands at a real coordinate.
	for _, n := range res.Nodes {
		if n.Width != 120 || n.Height != 60 {
			t.Errorf("node %q resized to %vx%v, want 120x60", n.ID, n.Width, n.Height)
		}
		if math.IsNaN(n.X) || math.IsNaN(n.Y) {
			t.Errorf("node %q placed at NaN (%v,%v)", n.ID, n.X, n.Y)
		}
		if n.Left() < 0 || n.Top() < 0 {
			t.Errorf("node %q box starts outside the canvas at (%v,%v)", n.ID, n.Left(), n.Top())
		}
	}

	// rankdir TB: a above b above c, in strictly increasing rank order.
	a, b, c := res.Nodes[0], res.Nodes[1], res.Nodes[2]
	if !(a.Bottom() < b.Top() && b.Bottom() < c.Top()) {
		t.Errorf("TB ranks not stacked: a=%v b=%v c=%v", a.Y, b.Y, c.Y)
	}

	// The drawing is big enough to contain every box.
	for _, n := range res.Nodes {
		if n.Right() > res.Width || n.Bottom() > res.Height {
			t.Errorf("node %q (right=%v bottom=%v) escapes canvas %vx%v", n.ID, n.Right(), n.Bottom(), res.Width, res.Height)
		}
	}

	// Both edges are routed, in input order, with usable polylines.
	if len(res.Edges) != 2 {
		t.Fatalf("got %d routed edges, want 2", len(res.Edges))
	}
	if res.Edges[0].From != "a" || res.Edges[0].To != "b" {
		t.Errorf("Edges[0] = %s->%s, want a->b", res.Edges[0].From, res.Edges[0].To)
	}
	for _, e := range res.Edges {
		if len(e.Points) < 2 {
			t.Errorf("edge %s->%s has %d points, want at least 2", e.From, e.To, len(e.Points))
		}
	}

	// Same input, same bytes — layout must not drift between runs.
	again, err := svg.Layout(nodes, edges, svg.LayoutOptions{})
	if err != nil {
		t.Fatalf("Layout (second run): %v", err)
	}
	if fmt.Sprint(again) != fmt.Sprint(res) {
		t.Errorf("layout is not deterministic:\nfirst:  %v\nsecond: %v", res, again)
	}
}

func TestLayout_RejectsBadInput(t *testing.T) {
	cases := []struct {
		name  string
		nodes []svg.LayoutNode
		edges []svg.LayoutEdge
		want  string
	}{
		{"no nodes", nil, nil, "at least one node"},
		{"empty id", []svg.LayoutNode{{ID: "", Width: 10, Height: 10}}, nil, "empty ID"},
		{"duplicate id", []svg.LayoutNode{{ID: "a", Width: 10, Height: 10}, {ID: "a", Width: 10, Height: 10}}, nil, "duplicate node ID"},
		{"zero size", []svg.LayoutNode{{ID: "a"}}, nil, "positive width and height"},
		{"dangling source", []svg.LayoutNode{{ID: "a", Width: 10, Height: 10}}, []svg.LayoutEdge{{From: "zz", To: "a"}}, "unknown source node"},
		{"dangling target", []svg.LayoutNode{{ID: "a", Width: 10, Height: 10}}, []svg.LayoutEdge{{From: "a", To: "zz"}}, "unknown target node"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svg.Layout(tc.nodes, tc.edges, svg.LayoutOptions{})
			if err == nil {
				t.Fatalf("Layout accepted invalid input, want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestLayout_MultiRankEdge — the whole reason a real layout engine is needed:
// an edge that skips ranks must route AROUND the boxes it passes, not through
// them. Uses plan-1670cacd's real graph (slice-1 -> slice-8 crosses the ranks
// holding slice-2 and slice-3).
// ---------------------------------------------------------------------------

func TestLayout_MultiRankEdge(t *testing.T) {
	nodes, edges := planGraph1670cacd()

	res, err := svg.Layout(nodes, edges, svg.LayoutOptions{})
	if err != nil {
		t.Fatalf("Layout: %v", err)
	}

	const from, to = "slice-1", "slice-8"

	src, ok := res.Node(from)
	if !ok {
		t.Fatalf("%s missing from layout", from)
	}
	dst, ok := res.Node(to)
	if !ok {
		t.Fatalf("%s missing from layout", to)
	}

	// Precondition for the test to mean anything: the edge really does skip
	// ranks. slice-2 and slice-3 sit strictly between slice-1 and slice-8.
	var crossed []string
	for _, id := range []string{"slice-2", "slice-3"} {
		n, ok := res.Node(id)
		if !ok {
			t.Fatalf("%s missing from layout", id)
		}
		if n.Top() > src.Bottom() && n.Bottom() < dst.Top() {
			crossed = append(crossed, id)
		}
	}
	if len(crossed) < 2 {
		t.Fatalf("expected slice-2 and slice-3 to lie between slice-1 and slice-8, got %v", crossed)
	}

	var route *svg.RoutedEdge
	for i := range res.Edges {
		if res.Edges[i].From == from && res.Edges[i].To == to {
			route = &res.Edges[i]
			break
		}
	}
	if route == nil {
		t.Fatalf("edge %s -> %s was not routed", from, to)
	}
	// A straight two-point line could not clear the intervening boxes; dagre
	// must have inserted bend points.
	if len(route.Points) < 3 {
		t.Errorf("rank-skipping edge %s -> %s has only %d points; expected bend points", from, to, len(route.Points))
	}

	// The route must clear every OTHER node's bounding box, not just the two
	// it was known to cross.
	for _, n := range res.Nodes {
		if n.ID == from || n.ID == to {
			continue
		}
		for i, p := range route.Points {
			if pointInside(p, n) {
				t.Errorf("edge %s -> %s point %d (%v,%v) falls inside %s's box [%v,%v]-[%v,%v]",
					from, to, i, p.X, p.Y, n.ID, n.Left(), n.Top(), n.Right(), n.Bottom())
			}
		}
	}

	// Every edge in the graph must be routed and every node placed on-canvas.
	if len(res.Edges) != len(edges) {
		t.Errorf("routed %d edges, want %d", len(res.Edges), len(edges))
	}
	for _, n := range res.Nodes {
		if n.Left() < 0 || n.Top() < 0 || n.Right() > res.Width || n.Bottom() > res.Height {
			t.Errorf("node %q escapes canvas %vx%v", n.ID, res.Width, res.Height)
		}
	}
}

// pointInside reports whether p falls strictly inside n's box. A 1-unit margin
// keeps points that sit exactly on the border — which is where an edge is
// supposed to meet its own endpoints — from counting as an overlap.
func pointInside(p svg.Point, n svg.PlacedNode) bool {
	const margin = 1.0
	return p.X > n.Left()+margin && p.X < n.Right()-margin &&
		p.Y > n.Top()+margin && p.Y < n.Bottom()-margin
}

func TestLayout_RankDirLR(t *testing.T) {
	nodes := []svg.LayoutNode{
		{ID: "a", Width: 100, Height: 40},
		{ID: "b", Width: 100, Height: 40},
	}
	edges := []svg.LayoutEdge{{From: "a", To: "b"}}

	res, err := svg.Layout(nodes, edges, svg.LayoutOptions{RankDir: svg.RankDirLR})
	if err != nil {
		t.Fatalf("Layout: %v", err)
	}
	a, b := res.Nodes[0], res.Nodes[1]
	if !(a.Right() < b.Left()) {
		t.Errorf("LR layout did not place a left of b: a.Right=%v b.Left=%v", a.Right(), b.Left())
	}
}

func TestEdgePathD(t *testing.T) {
	if got := svg.EdgePathD(nil); got != "" {
		t.Errorf("EdgePathD(nil) = %q, want empty", got)
	}
	got := svg.EdgePathD([]svg.Point{{X: 0, Y: 1.5}, {X: 10, Y: 20}, {X: 10, Y: 30}})
	want := "M0 1.5 L10 20 L10 30"
	if got != want {
		t.Errorf("EdgePathD = %q, want %q", got, want)
	}
}

func TestWriteEdgePath(t *testing.T) {
	var b strings.Builder
	e := svg.RoutedEdge{From: "a", To: "b", Points: []svg.Point{{X: 1, Y: 2}, {X: 3, Y: 4}}}
	if err := svg.WriteEdgePath(&b, e, "dep-edge", "var(--text-muted)"); err != nil {
		t.Fatalf("WriteEdgePath: %v", err)
	}
	out := b.String()
	for _, want := range []string{`<path`, `class="dep-edge"`, `d="M1 2 L3 4"`, `stroke="var(--text-muted)"`, svg.ArrowheadMarkerRef} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestWriteEdgePath_RejectsLiteralColor(t *testing.T) {
	var b strings.Builder
	e := svg.RoutedEdge{From: "a", To: "b", Points: []svg.Point{{X: 1, Y: 2}, {X: 3, Y: 4}}}
	if err := svg.WriteEdgePath(&b, e, "dep-edge", "#ff0000"); err == nil {
		t.Error("WriteEdgePath accepted a literal color, want an error")
	}
}

func TestLayoutOptionsDefaults(t *testing.T) {
	// A zero LayoutOptions must behave exactly like LayoutDefaults.
	nodes, edges := planGraph1670cacd()
	zero, err := svg.Layout(nodes, edges, svg.LayoutOptions{})
	if err != nil {
		t.Fatalf("Layout(zero opts): %v", err)
	}
	explicit, err := svg.Layout(nodes, edges, svg.LayoutDefaults)
	if err != nil {
		t.Fatalf("Layout(LayoutDefaults): %v", err)
	}
	if fmt.Sprint(zero) != fmt.Sprint(explicit) {
		t.Error("zero LayoutOptions did not fall back to LayoutDefaults")
	}
}

// ---------------------------------------------------------------------------
// BenchmarkLayout_16Nodes — 16 nodes is the largest dependency graph in the
// committed plan corpus, so this is the per-render ceiling in practice.
// ---------------------------------------------------------------------------

func BenchmarkLayout_16Nodes(b *testing.B) {
	nodes, edges := chainNodes(16), chainEdges(16)
	// Pay the one-time dagre.js compile before the timer starts, so the
	// benchmark measures per-render cost rather than process startup.
	if _, err := svg.Layout(nodes, edges, svg.LayoutOptions{}); err != nil {
		b.Fatalf("warmup: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svg.Layout(nodes, edges, svg.LayoutOptions{}); err != nil {
			b.Fatalf("Layout: %v", err)
		}
	}
}

func BenchmarkLayout_5Nodes(b *testing.B) {
	nodes, edges := chainNodes(5), chainEdges(5)
	if _, err := svg.Layout(nodes, edges, svg.LayoutOptions{}); err != nil {
		b.Fatalf("warmup: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svg.Layout(nodes, edges, svg.LayoutOptions{}); err != nil {
			b.Fatalf("Layout: %v", err)
		}
	}
}
