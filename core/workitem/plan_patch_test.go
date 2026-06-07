package workitem_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/models"
)

// crispiPlanHTML is a minimal CRISPI plan file that contains data-zone= markers
// so isCRISPIPlanFile detects it as a CRISPI plan. It simulates the output of
// plan generate / plan set-section and includes custom content that must be
// preserved after link add / plan start / plan complete operations.
const crispiPlanHTML = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Plan: Test</title></head>
<body>
<article id="PLAN_ID" data-feature-id="feat-abc123" data-status="draft">
<!-- ZONE 1: Dependency Graph -->
<section class="dep-graph" data-zone="dependency-graph">
  <h2>Dependency Graph</h2>
  <div id="graph-data" style="display:none"></div>
</section>
<!-- A: Design Discussion -->
<details class="section-card" data-phase="design" data-status="pending">
  <summary>A. Design Discussion</summary>
  <div class="section-body">
    <p>CUSTOM_DESIGN_CONTENT_MARKER</p>
    <!--PLAN_DESIGN_CONTENT-->
  </div>
</details>
<!-- C: Vertical Slices -->
<details class="section-card" data-phase="slices" data-status="pending">
  <summary>C. Vertical Slices</summary>
  <div class="section-body">
    <div class="slice-card" data-slice="1">Slice One</div>
    <!--PLAN_SLICE_CARDS-->
  </div>
</details>
</article>
<script>var x = 'interactive-js-preserved';</script>
</body>
</html>
`

// writeCRISPIPlanFile writes a synthetic CRISPI plan HTML to dir/plans/planID.html.
func writeCRISPIPlanFile(t *testing.T, dir, planID string) {
	t.Helper()
	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}
	content := strings.ReplaceAll(crispiPlanHTML, "PLAN_ID", planID)
	if err := os.WriteFile(filepath.Join(plansDir, planID+".html"), []byte(content), 0o644); err != nil {
		t.Fatalf("write crispi plan: %v", err)
	}
}

// readPlanFile returns the content of a plan HTML file.
func readPlanFile(t *testing.T, dir, planID string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "plans", planID+".html"))
	if err != nil {
		t.Fatalf("read plan file: %v", err)
	}
	return string(data)
}

// TestPlanAddEdge_PreservesCRISPIContent verifies that adding an edge to a
// CRISPI-style plan file does not destroy the custom HTML content (design
// sections, slice cards, JS, CSS). This is the core regression test for
// bug-1d56f9c4.
func TestPlanAddEdge_PreservesCRISPIContent(t *testing.T) {
	p := newTestProject(t)
	dir := p.ProjectDir

	// Create a plan node (writes generic HTML initially).
	node, err := p.Plans.Create("Test Plan")
	if err != nil {
		t.Fatalf("Create plan: %v", err)
	}
	planID := node.ID

	// Replace the generic HTML with synthetic CRISPI content.
	writeCRISPIPlanFile(t, dir, planID)

	// Verify the custom content is present before the operation.
	before := readPlanFile(t, dir, planID)
	if !strings.Contains(before, "CUSTOM_DESIGN_CONTENT_MARKER") {
		t.Fatal("setup: custom marker not found in plan file before AddEdge")
	}
	if !strings.Contains(before, "interactive-js-preserved") {
		t.Fatal("setup: JS content not found in plan file before AddEdge")
	}

	// Add an edge — this is the operation that triggered the bug.
	edge := models.Edge{
		TargetID:     "trk-abc123",
		Relationship: models.RelImplementedIn,
		Title:        "My Track",
		Since:        time.Now().UTC(),
	}
	_, err = p.Plans.AddEdge(planID, edge)
	if err != nil {
		t.Fatalf("AddEdge: %v", err)
	}

	after := readPlanFile(t, dir, planID)

	// Custom content must be preserved.
	if !strings.Contains(after, "CUSTOM_DESIGN_CONTENT_MARKER") {
		t.Error("AddEdge destroyed CUSTOM_DESIGN_CONTENT_MARKER")
	}
	if !strings.Contains(after, "interactive-js-preserved") {
		t.Error("AddEdge destroyed JS content")
	}
	if !strings.Contains(after, `data-zone="dependency-graph"`) {
		t.Error("AddEdge destroyed data-zone section")
	}
	if !strings.Contains(after, `data-slice="1"`) {
		t.Error("AddEdge destroyed slice card")
	}

	// The edge must appear in the nav section.
	if !strings.Contains(after, "trk-abc123") {
		t.Error("AddEdge: edge target ID not found in plan file")
	}
	if !strings.Contains(after, `data-relationship="implemented_in"`) {
		t.Error("AddEdge: edge relationship not found in plan file")
	}
}

// TestPlanStart_PreservesCRISPIContent verifies that plan start (status change)
// does not destroy CRISPI interactive HTML.
func TestPlanStart_PreservesCRISPIContent(t *testing.T) {
	p := newTestProject(t)
	dir := p.ProjectDir

	node, err := p.Plans.Create("Start Test Plan")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	planID := node.ID
	writeCRISPIPlanFile(t, dir, planID)

	_, err = p.Plans.Start(planID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	after := readPlanFile(t, dir, planID)

	if !strings.Contains(after, "CUSTOM_DESIGN_CONTENT_MARKER") {
		t.Error("Start destroyed CUSTOM_DESIGN_CONTENT_MARKER")
	}
	if !strings.Contains(after, "interactive-js-preserved") {
		t.Error("Start destroyed JS content")
	}
	// Status attribute must be updated.
	if !strings.Contains(after, `data-status="in-progress"`) {
		t.Error("Start: data-status not updated to in-progress")
	}
}

// TestPlanComplete_PreservesCRISPIContent verifies that plan complete does
// not destroy CRISPI interactive HTML.
func TestPlanComplete_PreservesCRISPIContent(t *testing.T) {
	p := newTestProject(t)
	dir := p.ProjectDir

	node, err := p.Plans.Create("Complete Test Plan")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	planID := node.ID
	writeCRISPIPlanFile(t, dir, planID)

	_, err = p.Plans.Complete(planID)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	after := readPlanFile(t, dir, planID)

	if !strings.Contains(after, "CUSTOM_DESIGN_CONTENT_MARKER") {
		t.Error("Complete destroyed CUSTOM_DESIGN_CONTENT_MARKER")
	}
	if !strings.Contains(after, "interactive-js-preserved") {
		t.Error("Complete destroyed JS content")
	}
	// Status must be updated.
	if !strings.Contains(after, `data-status="done"`) {
		t.Error("Complete: data-status not updated to done")
	}
}

// TestPlanAddEdge_GenericPlan verifies that AddEdge on a non-CRISPI plan
// still works via the standard WriteNodeHTML path.
func TestPlanAddEdge_GenericPlan(t *testing.T) {
	p := newTestProject(t)

	node, err := p.Plans.Create("Generic Plan")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	edge := models.Edge{
		TargetID:     "trk-generic",
		Relationship: models.RelImplementedIn,
		Title:        "Generic Track",
	}
	updated, err := p.Plans.AddEdge(node.ID, edge)
	if err != nil {
		t.Fatalf("AddEdge: %v", err)
	}

	if _, ok := updated.Edges[string(models.RelImplementedIn)]; !ok {
		t.Error("AddEdge: edge not present in returned node")
	}

	// Re-read to verify round-trip.
	got, err := p.Plans.Get(node.ID)
	if err != nil {
		t.Fatalf("Get after AddEdge: %v", err)
	}
	edges := got.Edges[string(models.RelImplementedIn)]
	if len(edges) == 0 {
		t.Error("AddEdge: edge not persisted after round-trip")
	}
	if edges[0].TargetID != "trk-generic" {
		t.Errorf("AddEdge: target ID = %q, want trk-generic", edges[0].TargetID)
	}
}

// TestPlanAddEdge_MultipleEdges verifies that calling AddEdge multiple times
// accumulates edges without overwriting previous ones.
func TestPlanAddEdge_MultipleEdges(t *testing.T) {
	p := newTestProject(t)
	dir := p.ProjectDir

	node, err := p.Plans.Create("Multi-Edge Plan")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	planID := node.ID
	writeCRISPIPlanFile(t, dir, planID)

	for _, targetID := range []string{"trk-first", "trk-second"} {
		edge := models.Edge{
			TargetID:     targetID,
			Relationship: models.RelImplementedIn,
			Title:        targetID,
		}
		if _, err := p.Plans.AddEdge(planID, edge); err != nil {
			t.Fatalf("AddEdge %s: %v", targetID, err)
		}
	}

	after := readPlanFile(t, dir, planID)
	if !strings.Contains(after, "trk-first") {
		t.Error("first edge lost after second AddEdge")
	}
	if !strings.Contains(after, "trk-second") {
		t.Error("second edge not present")
	}
	if !strings.Contains(after, "CUSTOM_DESIGN_CONTENT_MARKER") {
		t.Error("custom content destroyed by multiple AddEdge calls")
	}
}
