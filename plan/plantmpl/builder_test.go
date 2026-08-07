package plantmpl_test

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/plantmpl"
)

// ---------------------------------------------------------------------------
// BuildFromTopic
// ---------------------------------------------------------------------------

func TestBuildFromTopicBasicFields(t *testing.T) {
	page := plantmpl.BuildFromTopic("plan-abc12345", "Auth Rewrite", "Rewrite auth for compliance", "2026-04-04")

	if page.PlanID != "plan-abc12345" {
		t.Errorf("PlanID: got %q, want %q", page.PlanID, "plan-abc12345")
	}
	if page.Title != "Auth Rewrite" {
		t.Errorf("Title: got %q, want %q", page.Title, "Auth Rewrite")
	}
	if page.Description != "Rewrite auth for compliance" {
		t.Errorf("Description: got %q", page.Description)
	}
	if page.Date != "2026-04-04" {
		t.Errorf("Date: got %q", page.Date)
	}
	if page.Status != "draft" {
		t.Errorf("Status: got %q, want %q", page.Status, "draft")
	}
}

func TestBuildFromTopicRenderValid(t *testing.T) {
	page := plantmpl.BuildFromTopic("plan-test0001", "Test Plan", "A description", "2026-04-04")

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	for _, want := range []string{
		"<!DOCTYPE html>",
		"plan-test0001",
		"Test Plan",
		"A description",
		`data-status="draft"`,
		"btn-finalize",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

// ---------------------------------------------------------------------------
// BuildFromWorkItem
// ---------------------------------------------------------------------------

func TestBuildFromWorkItemWithSlices(t *testing.T) {
	page := plantmpl.BuildFromWorkItem("plan-feat0001", "feat-abc", "My Feature", "Feature desc", "2026-04-04")
	page.Slices = []plantmpl.SliceCard{
		{Num: 1, ID: "feat-s1", Title: "Slice One", Status: "pending"},
		{Num: 2, ID: "feat-s2", Title: "Slice Two", Deps: "1", Status: "pending"},
	}
	page.Graph = &plantmpl.DependencyGraph{
		Nodes: []plantmpl.GraphNode{
			{Num: 1, Name: "Slice One", Status: "pending"},
			{Num: 2, Name: "Slice Two", Status: "pending", Deps: "1"},
		},
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()

	// Check data-node elements from graph.
	if !strings.Contains(html, `data-node="1"`) {
		t.Error("output missing data-node=1")
	}
	if !strings.Contains(html, `data-node="2"`) {
		t.Error("output missing data-node=2")
	}

	// Check slice card elements.
	if !strings.Contains(html, `data-slice="1"`) {
		t.Error("output missing data-slice=1")
	}
	if !strings.Contains(html, `data-slice="2"`) {
		t.Error("output missing data-slice=2")
	}

	// Check feature ID reference.
	if !strings.Contains(html, "feat-abc") {
		t.Error("output missing feature ID")
	}
}

func TestBuildFromWorkItemWithDesignAndOutline(t *testing.T) {
	page := plantmpl.BuildFromWorkItem("plan-design01", "trk-abc", "Design Test", "desc", "2026-04-04")
	page.Design = &plantmpl.DesignSection{
		Content: template.HTML("<p>Design rationale here</p>"),
	}
	page.Outline = &plantmpl.OutlineSection{
		Content: template.HTML("<p>Outline content here</p>"),
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "Design rationale here") {
		t.Error("output missing design content")
	}
	if !strings.Contains(html, "Outline content here") {
		t.Error("output missing outline content")
	}
}

// ---------------------------------------------------------------------------
// SectionsJSON / SliceCount
// ---------------------------------------------------------------------------

func TestSectionsJSONNoSlices(t *testing.T) {
	page := plantmpl.BuildFromTopic("plan-noslice", "No Slices", "", "2026-04-04")
	got := page.SectionsJSON()
	if got != `["design"]` {
		t.Errorf("SectionsJSON: got %q, want %q", got, `["design"]`)
	}
}

func TestSectionsJSONWithSlices(t *testing.T) {
	page := plantmpl.BuildFromTopic("plan-sliced", "Sliced", "", "2026-04-04")
	page.Slices = []plantmpl.SliceCard{
		{Num: 1}, {Num: 2}, {Num: 3},
	}
	got := page.SectionsJSON()
	want := `["design","slice-1","slice-2","slice-3"]`
	if got != want {
		t.Errorf("SectionsJSON: got %q, want %q", got, want)
	}
}

func TestSliceCount(t *testing.T) {
	page := plantmpl.BuildFromTopic("plan-cnt", "Count", "", "2026-04-04")
	if page.SliceCount() != 0 {
		t.Errorf("SliceCount empty: got %d", page.SliceCount())
	}
	page.Slices = []plantmpl.SliceCard{{Num: 1}, {Num: 2}}
	if page.SliceCount() != 2 {
		t.Errorf("SliceCount: got %d, want 2", page.SliceCount())
	}
}

func TestTotalSections(t *testing.T) {
	page := plantmpl.BuildFromTopic("plan-tot", "Total", "", "2026-04-04")
	if page.TotalSections() != 1 {
		t.Errorf("TotalSections empty: got %d, want 1", page.TotalSections())
	}
	page.Slices = []plantmpl.SliceCard{{Num: 1}, {Num: 2}}
	if page.TotalSections() != 3 {
		t.Errorf("TotalSections: got %d, want 3", page.TotalSections())
	}
}

// ---------------------------------------------------------------------------
// PlanPage.Render — full CRISPI structure
// ---------------------------------------------------------------------------

func TestPlanPageRenderCRISPIStructure(t *testing.T) {
	page := plantmpl.BuildFromTopic("plan-crispi01", "CRISPI Test", "desc", "2026-04-04")

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()

	// Must have CDN links. d3 and dagre-d3 are deliberately NOT in this list:
	// the dependency graph is laid out by dagre in Go at render time and
	// emitted as a static inline SVG, so the page no longer loads either
	// library. See TestPlanPageRenderNoGraphScripts.
	for _, cdn := range []string{
		"fonts.googleapis.com",
		"highlight.min.js",
	} {
		if !strings.Contains(html, cdn) {
			t.Errorf("output missing CDN link for %q", cdn)
		}
	}

	// Must have sidebar with scroll-spy links.
	if !strings.Contains(html, "plan-sidebar") {
		t.Error("output missing plan-sidebar")
	}
	if !strings.Contains(html, `data-spy=`) {
		t.Error("output missing scroll-spy data attributes")
	}

	// Must have theme toggle.
	if !strings.Contains(html, "themeToggle") {
		t.Error("output missing theme toggle")
	}

	// Must have SECTIONS_JSON markers for JS.
	if !strings.Contains(html, "PLAN_SECTIONS_JSON") {
		t.Error("output missing PLAN_SECTIONS_JSON marker")
	}

	// Must have finalize button.
	if !strings.Contains(html, "btn-finalize") {
		t.Error("output missing btn-finalize")
	}

	// Must have dep-graph-svg.
	if !strings.Contains(html, "dep-graph-svg") {
		t.Error("output missing dep-graph-svg")
	}

	// Must have graph-data container.
	if !strings.Contains(html, "graph-data") {
		t.Error("output missing graph-data container")
	}
}

func TestPlanPageRenderWithAllZones(t *testing.T) {
	page := &plantmpl.PlanPage{
		PlanID:      "plan-full0001",
		FeatureID:   "trk-full",
		Title:       "Full Plan",
		Description: "All zones populated",
		Date:        "2026-04-04",
		Status:      "draft",
		Graph: &plantmpl.DependencyGraph{
			Nodes: []plantmpl.GraphNode{
				{Num: 1, Name: "First", Status: "pending", Files: 3},
			},
		},
		Design:  &plantmpl.DesignSection{Content: "<p>Design</p>"},
		Outline: &plantmpl.OutlineSection{Content: "<p>Outline</p>"},
		Slices: []plantmpl.SliceCard{
			{Num: 1, ID: "feat-s1", Title: "First Slice", Status: "pending"},
		},
		Questions: &plantmpl.QuestionsSection{
			Cards: []plantmpl.DecisionCard{
				{ID: "q1", Text: "Which approach?", Options: []string{"A", "B"}},
			},
		},
		Critique: &plantmpl.CritiqueZone{},
		Progress: &plantmpl.ProgressBar{Approved: 0, Total: 3, Pending: 3},
	}

	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()

	for _, want := range []string{
		`data-node="1"`,
		`data-slice="1"`,
		"Design",
		"Outline",
		"Which approach?",
		"progress-zone",
		"btn-finalize",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

// ---------------------------------------------------------------------------
// Static dependency graph on the assembled page
// ---------------------------------------------------------------------------

// planPageWithGraph renders a plan whose dependency graph has a rank-skipping
// edge (slice 3 depends on slice 1 as well as slice 2).
func planPageWithGraph(t *testing.T) string {
	t.Helper()
	page := &plantmpl.PlanPage{
		PlanID:    "plan-graph001",
		FeatureID: "trk-graph",
		Title:     "Graph Plan",
		Date:      "2026-04-04",
		Status:    "draft",
		Graph: &plantmpl.DependencyGraph{
			Nodes: []plantmpl.GraphNode{
				{Num: 1, Name: "Shared SVG primitives", Status: "approved", Files: 2},
				{Num: 2, Name: "Static dependency graph", Status: "pending", Deps: "1", Files: 6},
				{Num: 3, Name: "Dual-format validator", Status: "pending", Deps: "1,2", Files: 2},
			},
		},
		Slices: []plantmpl.SliceCard{
			{Num: 1, ID: "feat-s1", Title: "Shared SVG primitives", Status: "done"},
			{Num: 2, ID: "feat-s2", Title: "Static dependency graph", Status: "pending"},
			{Num: 3, ID: "feat-s3", Title: "Dual-format validator", Status: "pending"},
		},
	}
	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return buf.String()
}

// TestPlanPageRenderNoGraphScripts is the point of the static graph: a
// committed plan page shows its dependency graph with no script executed and
// no CDN reachable. It replaces the old assertion that the page must load
// d3 and dagre-d3.
func TestPlanPageRenderNoGraphScripts(t *testing.T) {
	html := planPageWithGraph(t)

	for _, forbidden := range []string{
		"d3js.org",
		"cdn.jsdelivr.net/npm/dagre-d3",
		"dagreD3",
		"renderGraph",
	} {
		if strings.Contains(html, forbidden) {
			t.Errorf("plan page still references %q — the dependency graph must render without JavaScript", forbidden)
		}
	}

	// The graph is really drawn, not an empty placeholder waiting for script.
	if !strings.Contains(html, `data-graph-render="static"`) {
		t.Error(`output missing data-graph-render="static" marker`)
	}
	if !strings.Contains(html, `class="dep-node-box"`) {
		t.Error("output has no drawn node boxes")
	}
	if !strings.Contains(html, `class="dep-edge"`) {
		t.Error("output has no drawn edges")
	}
	if strings.Contains(html, `<svg id="dep-graph-svg" width="100%"></svg>`) {
		t.Error("output still contains the empty client-rendered <svg> placeholder")
	}
}

// TestPlanPageGraphAnchorsAndData verifies the two contracts other tools
// resolve against survive the rewrite: the #graph-data bridge divs the
// dashboard reads, and the #slice-N anchors plan review and the sidebar nav
// share with the graph nodes.
func TestPlanPageGraphAnchorsAndData(t *testing.T) {
	html := planPageWithGraph(t)

	for _, want := range []string{
		`id="graph-data"`,
		`data-node="1"`, `data-name="Shared SVG primitives"`,
		`data-status="approved"`, `data-deps="1,2"`, `data-files="6"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("output missing graph-data attribute %q", want)
		}
	}

	for n := 1; n <= 3; n++ {
		anchor := fmt.Sprintf(`<a href="#slice-%d">`, n)
		if !strings.Contains(html, anchor) {
			t.Errorf("graph node %d is not linked to its slice card (%s)", n, anchor)
		}
		target := fmt.Sprintf(`id="slice-%d"`, n)
		if !strings.Contains(html, target) {
			t.Errorf("anchor target %s has no matching slice card", target)
		}
	}
}

// TestPlanPageGraphGreppable guards `wipnote relevant`, which finds work by
// running ripgrep over rendered plan HTML: a known slice title must still be
// a plain contiguous match in the assembled page.
func TestPlanPageGraphGreppable(t *testing.T) {
	html := planPageWithGraph(t)

	for _, title := range []string{
		"Shared SVG primitives",
		"Static dependency graph",
		"Dual-format validator",
	} {
		if !strings.Contains(html, title) {
			t.Errorf("slice title %q is not findable in the rendered plan page", title)
		}
	}
}

// ---------------------------------------------------------------------------
// PlanMeta
// ---------------------------------------------------------------------------

func TestPlanMeta(t *testing.T) {
	page := plantmpl.BuildFromTopic("plan-meta01", "Meta", "", "2026-04-04")
	got := page.PlanMeta()
	if got != "0 slices \u00b7 Created 2026-04-04" {
		t.Errorf("PlanMeta no slices: got %q", got)
	}

	page.Slices = []plantmpl.SliceCard{{Num: 1}, {Num: 2}}
	got = page.PlanMeta()
	if got != "2 slices \u00b7 Created 2026-04-04" {
		t.Errorf("PlanMeta with slices: got %q", got)
	}
}
