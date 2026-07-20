package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

// TestRewriteYAML_PreservesGraphEdges is the GH#157 / bug-b12b537b regression:
// wipnote plan rewrite-yaml rebuilds the plan's HTML from the YAML body, but
// graph edges (caused_by, relates_to, part_of, ...) live only in the HTML
// <nav data-graph-edges> section — they are never part of the YAML. Before the
// fix, the re-render silently dropped every edge. This test builds a plan with
// three edge types, rewrites it, and asserts all edges survive.
func TestRewriteYAML_PreservesGraphEdges(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	t.Chdir(dir)
	// findWipnoteDir() checks WIPNOTE_PROJECT_DIR before the process CWD, so
	// an ambient value from the outer wipnote session (set when this test
	// runs under `wipnote claude`) would otherwise redirect resolution to the
	// real repo instead of this test's temp dir.
	t.Setenv("WIPNOTE_PROJECT_DIR", dir)

	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "plans"), 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}

	planID := "plan-edge0001"
	plan := planyaml.NewPlan(planID, "Edge Preservation Test", "verifies rewrite-yaml keeps edges")
	plan.Design = planyaml.PlanDesign{Problem: "p", Goals: []string{"g"}, Constraints: []string{"c"}}
	planPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatalf("save initial plan yaml: %v", err)
	}

	// Render the initial HTML from the YAML, mirroring `plan create-yaml`.
	if err := renderPlanToFileQuiet(wipnoteDir, planID); err != nil {
		t.Fatalf("initial render: %v", err)
	}

	// Add three edge types directly through the same collection the `link
	// add` CLI command uses.
	p, err := workitem.Open(wipnoteDir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	defer p.Close()

	wantEdges := []models.Edge{
		{TargetID: "spk-caused0001", Relationship: models.RelCausedBy, Title: "Caused By Target", Since: time.Now().UTC()},
		{TargetID: "plan-related001", Relationship: models.RelRelatesTo, Title: "Related Plan", Since: time.Now().UTC()},
		{TargetID: "trk-parent0001", Relationship: models.RelPartOf, Title: "Parent Track", Since: time.Now().UTC()},
	}
	for _, e := range wantEdges {
		if _, err := p.Plans.AddEdge(planID, e); err != nil {
			t.Fatalf("AddEdge %v: %v", e, err)
		}
	}

	// Sanity check: edges are present before the rewrite.
	htmlPath := filepath.Join(wipnoteDir, "plans", planID+".html")
	before, err := htmlparse.ParseFile(htmlPath)
	if err != nil {
		t.Fatalf("parse html before rewrite: %v", err)
	}
	if got := countEdges(before.Edges); got != len(wantEdges) {
		t.Fatalf("precondition: expected %d edges before rewrite, got %d (%v)", len(wantEdges), got, before.Edges)
	}

	// Build a rewrite YAML with the same meta.id but different content —
	// this is what a plan-authoring agent would submit via rewrite-yaml.
	rewrite := planyaml.NewPlan(planID, "Edge Preservation Test (rewritten)", "rewritten body")
	rewrite.Meta.SchemaVersion = "" // legacy: skip the v4 research-citation gate for this test
	rewrite.Design = planyaml.PlanDesign{Problem: "p2", Goals: []string{"g2"}, Constraints: []string{"c2"}}
	rewritePath := filepath.Join(t.TempDir(), "rewrite.yaml")
	if err := planyaml.Save(rewritePath, rewrite); err != nil {
		t.Fatalf("save rewrite yaml: %v", err)
	}

	if err := runRewriteYAML(planID, rewritePath); err != nil {
		t.Fatalf("runRewriteYAML: %v", err)
	}

	after, err := htmlparse.ParseFile(htmlPath)
	if err != nil {
		t.Fatalf("parse html after rewrite: %v", err)
	}
	if got := countEdges(after.Edges); got != len(wantEdges) {
		t.Fatalf("rewrite-yaml dropped edges: expected %d edges after rewrite, got %d (%v)", len(wantEdges), got, after.Edges)
	}
	for _, want := range wantEdges {
		if !hasEdge(after.Edges, want) {
			t.Errorf("edge %+v missing after rewrite-yaml (bug-b12b537b / GH#157 regression)", want)
		}
	}
}

func countEdges(edges map[string][]models.Edge) int {
	total := 0
	for _, es := range edges {
		total += len(es)
	}
	return total
}

func hasEdge(edges map[string][]models.Edge, want models.Edge) bool {
	for _, e := range edges[string(want.Relationship)] {
		if e.TargetID == want.TargetID && e.Relationship == want.Relationship {
			return true
		}
	}
	return false
}
