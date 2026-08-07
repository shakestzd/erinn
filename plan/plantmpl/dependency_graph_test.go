package plantmpl_test

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/plantmpl"
)

// ---------------------------------------------------------------------------
// DependencyGraph.Render — structural output
// ---------------------------------------------------------------------------

func TestDependencyGraphRenderZoneClass(t *testing.T) {
	g := &plantmpl.DependencyGraph{}

	var buf bytes.Buffer
	if err := g.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `class="dep-graph"`) {
		t.Error(`output missing class="dep-graph"`)
	}
	if !strings.Contains(html, `data-zone="dependency-graph"`) {
		t.Error(`output missing data-zone="dependency-graph"`)
	}
}

func TestDependencyGraphRenderSVGPlaceholder(t *testing.T) {
	g := &plantmpl.DependencyGraph{}

	var buf bytes.Buffer
	if err := g.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `<svg id="dep-graph-svg"`) {
		t.Error(`output missing <svg id="dep-graph-svg"`)
	}
}

func TestDependencyGraphRenderNodeAttributes(t *testing.T) {
	g := &plantmpl.DependencyGraph{
		Nodes: []plantmpl.GraphNode{
			{Num: 1, Name: "Feature name", Status: "pending", Deps: "2,3", Files: 5},
		},
	}

	var buf bytes.Buffer
	if err := g.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `data-node="1"`) {
		t.Error(`output missing data-node="1"`)
	}
	if !strings.Contains(html, `data-name="Feature name"`) {
		t.Error(`output missing data-name="Feature name"`)
	}
	if !strings.Contains(html, `data-status="pending"`) {
		t.Error(`output missing data-status="pending"`)
	}
	if !strings.Contains(html, `data-deps="2,3"`) {
		t.Error(`output missing data-deps="2,3"`)
	}
	if !strings.Contains(html, `data-files="5"`) {
		t.Error(`output missing data-files="5"`)
	}
}

func TestDependencyGraphRenderEmptyNodesSection(t *testing.T) {
	g := &plantmpl.DependencyGraph{Nodes: nil}

	var buf bytes.Buffer
	if err := g.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `id="graph-data"`) {
		t.Error(`output missing id="graph-data" div`)
	}
	// No data-node elements should be present.
	if strings.Contains(html, `data-node=`) {
		t.Error("empty Nodes should not produce data-node elements")
	}
}

func TestDependencyGraphRenderFilesOmittedWhenZero(t *testing.T) {
	g := &plantmpl.DependencyGraph{
		Nodes: []plantmpl.GraphNode{
			{Num: 2, Name: "No Files", Status: "approved", Deps: "", Files: 0},
		},
	}

	var buf bytes.Buffer
	if err := g.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if strings.Contains(html, `data-files=`) {
		t.Error("data-files attribute should not render when Files is 0")
	}
}

func TestDependencyGraphRenderDefaultStatusPending(t *testing.T) {
	g := &plantmpl.DependencyGraph{
		Nodes: []plantmpl.GraphNode{
			{Num: 3, Name: "No Status"},
		},
	}

	var buf bytes.Buffer
	if err := g.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `data-status="pending"`) {
		t.Error("empty Status should default to pending in data-status attribute")
	}
}

func TestDependencyGraphRenderMultipleNodes(t *testing.T) {
	g := &plantmpl.DependencyGraph{
		Nodes: []plantmpl.GraphNode{
			{Num: 1, Name: "Alpha", Status: "approved", Deps: ""},
			{Num: 2, Name: "Beta", Status: "pending", Deps: "1"},
			{Num: 3, Name: "Gamma", Status: "", Deps: "1,2", Files: 3},
		},
	}

	var buf bytes.Buffer
	if err := g.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	for _, want := range []string{
		`data-node="1"`, `data-name="Alpha"`, `data-status="approved"`,
		`data-node="2"`, `data-name="Beta"`, `data-deps="1"`,
		`data-node="3"`, `data-name="Gamma"`, `data-deps="1,2"`, `data-files="3"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("output missing %q", want)
		}
	}
	// Gamma has empty Status — should default to pending.
	if !strings.Contains(html, `data-status="pending"`) {
		t.Error(`Gamma node with empty Status should render data-status="pending"`)
	}
}

func TestDependencyGraphRenderTriageBadges(t *testing.T) {
	g := &plantmpl.DependencyGraph{
		Nodes: []plantmpl.GraphNode{
			{Num: 1, Name: "Alpha", Status: "revision", Issues: 3, Questions: 2},
			{Num: 2, Name: "Beta", Status: "approved", Issues: 0, Questions: 0},
		},
	}

	var buf bytes.Buffer
	if err := g.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `data-issues="3"`) {
		t.Error(`node with Issues=3 should emit data-issues="3"`)
	}
	if !strings.Contains(html, `data-questions="2"`) {
		t.Error(`node with Questions=2 should emit data-questions="2"`)
	}
	// Beta has zero issues/questions — attributes should be omitted
	if strings.Count(html, `data-issues=`) != 1 {
		t.Error(`zero-issues node should not emit data-issues attribute`)
	}
	if strings.Count(html, `data-questions=`) != 1 {
		t.Error(`zero-questions node should not emit data-questions attribute`)
	}
}

func TestApprovalStatusToGraphStatus(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"approved", "approved"},
		{"revision", "revision"},
		{"changes_requested", "revision"},
		{"rejected", "revision"},
		{"discuss", "discuss"},
		{"blocked", "blocked"},
		{"pending", "pending"},
		{"", "pending"},
		{"unknown_value", "pending"},
	}
	for _, tc := range cases {
		got := plantmpl.ApprovalStatusToGraphStatus(tc.input)
		if got != tc.want {
			t.Errorf("ApprovalStatusToGraphStatus(%q) = %q; want %q", tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Static SVG rendering — the graph is drawn in Go, not by the browser
// ---------------------------------------------------------------------------

// goldenGraph is the fixture behind the golden file: three slices in a chain
// with a rank-skipping 1 -> 3 dependency, one node per status colour.
func goldenGraph() *plantmpl.DependencyGraph {
	return &plantmpl.DependencyGraph{Nodes: []plantmpl.GraphNode{
		{Num: 1, Name: "Foundation", Status: "approved", Files: 3},
		{Num: 2, Name: "Static graph", Status: "pending", Deps: "1", Files: 5},
		{Num: 3, Name: "Validator", Status: "revision", Deps: "1,2", Files: 1},
	}}
}

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata golden files from current output")

// TestRenderGraph_Static pins the exact emitted SVG. Plan HTML is committed to
// git, so any geometry change — a dagre upgrade, a font-metric tweak, a
// padding constant — rewrites every plan in the repo. The golden makes that
// churn something a reviewer opts into rather than discovers in a diff.
func TestRenderGraph_Static(t *testing.T) {
	markup, err := goldenGraph().SVG()
	if err != nil {
		t.Fatalf("SVG: %v", err)
	}
	got := string(markup) + "\n"

	path := filepath.Join("testdata", "dependency_graph_static.svg")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("golden updated: %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (re-run with -update-golden to create it): %v", err)
	}
	if got != string(want) {
		t.Errorf("rendered SVG does not match %s.\n--- got ---\n%s\n--- want ---\n%s\n(re-run with -update-golden if the change is intended)", path, got, want)
	}
}

// TestRenderGraph_StaticNoScript is the load-bearing property of this zone:
// the graph must be complete in the markup, with nothing left for a browser
// to compute. No <script>, no external fetch, no empty placeholder.
func TestRenderGraph_StaticNoScript(t *testing.T) {
	var buf bytes.Buffer
	if err := goldenGraph().Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()

	lower := strings.ToLower(out)
	for _, forbidden := range []string{"<script", "d3js.org", "dagre-d3", "cdn.jsdelivr.net", `src="http`, "url(http"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("static graph output contains %q — the graph must need no client-side rendering:\n%s", forbidden, out)
		}
	}

	// The zone announces its format so `wipnote plan validate` can tell a
	// static graph from a legacy d3 one.
	if !strings.Contains(out, `data-graph-render="static"`) {
		t.Error(`output missing data-graph-render="static" marker`)
	}

	// Real geometry: one <rect> and two <text> lines per node, one <path> per
	// edge, and a viewBox sized to hold them.
	if got := strings.Count(out, "<rect"); got != 3 {
		t.Errorf("got %d <rect> elements, want 3 (one per node)", got)
	}
	if got := strings.Count(out, `class="dep-edge"`); got != 3 {
		t.Errorf("got %d edges, want 3 (1->2, 1->3, 2->3)", got)
	}
	if !strings.Contains(out, `viewBox="0 0 `) {
		t.Error("output missing a sized viewBox")
	}

	// Node colours are theme tokens, so one committed document is readable in
	// both the light and dark plan themes.
	for _, token := range []string{"var(--approved)", "var(--pending)", "var(--revision)", "var(--bg-input)"} {
		if !strings.Contains(out, token) {
			t.Errorf("output missing theme token %q", token)
		}
	}
	if strings.Contains(out, "#6b7280") || strings.Contains(out, "#16a34a") {
		t.Error("output contains a literal hex colour; node paint must be theme tokens only")
	}
}

// TestRenderGraph_StaticAnchors verifies the no-JS navigation contract: each
// node links to its slice card via the same #slice-N anchor the sidebar nav
// and plan review resolve against.
func TestRenderGraph_StaticAnchors(t *testing.T) {
	var buf bytes.Buffer
	if err := goldenGraph().Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()

	for n := 1; n <= 3; n++ {
		anchor := fmt.Sprintf(`<a href="#slice-%d">`, n)
		if !strings.Contains(out, anchor) {
			t.Errorf("output missing node anchor %s", anchor)
		}
		id := fmt.Sprintf(`id="graph-node-%d"`, n)
		if !strings.Contains(out, id) {
			t.Errorf("output missing node group %s", id)
		}
	}
	if got, want := strings.Count(out, "<a href=\"#slice-"), 3; got != want {
		t.Errorf("got %d node anchors, want %d", got, want)
	}
}

// TestGraph_Greppable guards bug-37801e41's blast radius: `wipnote relevant`
// finds work by running ripgrep over rendered plan HTML, so a slice title must
// survive rendering as a contiguous, searchable run of text — including titles
// long enough to be truncated in the drawn box, and titles containing the
// angle brackets and ampersands that broke the earlier renderer.
func TestGraph_Greppable(t *testing.T) {
	const longTitle = "Static dependency graph via embedded dagre.js layout engine"
	const trickyTitle = "Handle <T> & \"quoted\" identifiers"

	g := &plantmpl.DependencyGraph{Nodes: []plantmpl.GraphNode{
		{Num: 1, Name: longTitle, Status: "approved", Files: 4},
		{Num: 2, Name: trickyTitle, Status: "pending", Deps: "1", Files: 2},
	}}

	var buf bytes.Buffer
	if err := g.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()

	// The full untruncated title is always present in the data island, which
	// is what ripgrep matches against.
	if !strings.Contains(out, longTitle) {
		t.Errorf("full slice title %q is not findable in rendered output", longTitle)
	}
	// The drawn label is truncated, but the leading phrase stays contiguous —
	// never split into per-character <tspan>s.
	if !strings.Contains(out, "#1 Static dependency graph") {
		t.Error("drawn node label is not a contiguous run of text")
	}
	if strings.Contains(out, "<tspan") {
		t.Error("labels must never be split into <tspan> elements (breaks phrase matching)")
	}
	// Angle brackets and ampersands are escaped, not mangled or dropped.
	if !strings.Contains(out, "Handle &lt;T&gt; &amp;") {
		t.Errorf("special characters in %q were not escaped intact:\n%s", trickyTitle, out)
	}
}

// TestDependencyGraphDropsDanglingDeps covers the real corpus shape where a
// slice was deleted mid-plan: surviving slices still name it in their deps.
// The graph must drop the edge and render, not fail.
func TestDependencyGraphDropsDanglingDeps(t *testing.T) {
	g := &plantmpl.DependencyGraph{Nodes: []plantmpl.GraphNode{
		{Num: 1, Name: "First", Status: "approved"},
		// slice 6 was deleted from the plan; slice 7 still lists it.
		{Num: 7, Name: "Later", Status: "pending", Deps: "1,6, ,junk"},
	}}

	var buf bytes.Buffer
	if err := g.Render(&buf); err != nil {
		t.Fatalf("Render with dangling dep: %v", err)
	}
	out := buf.String()
	if got := strings.Count(out, `class="dep-edge"`); got != 1 {
		t.Errorf("got %d edges, want 1 (only 1 -> 7 survives)", got)
	}
	if !strings.Contains(out, `id="graph-node-7"`) {
		t.Error("slice 7 should still be drawn despite its dangling dep")
	}
}

// TestDependencyGraphSelfDepIgnored: a slice listing itself as a dependency
// would make dagre draw a self-loop and inflate the layout; drop it.
func TestDependencyGraphSelfDepIgnored(t *testing.T) {
	g := &plantmpl.DependencyGraph{Nodes: []plantmpl.GraphNode{
		{Num: 1, Name: "Only", Status: "pending", Deps: "1"},
	}}
	var buf bytes.Buffer
	if err := g.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(buf.String(), `class="dep-edge"`) {
		t.Error("a self-dependency should not produce an edge")
	}
}
