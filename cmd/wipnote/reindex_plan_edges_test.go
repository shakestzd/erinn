package main

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

// buildTestPlan writes a plan YAML with a foundational slice (num 1) and a
// dependent slice (num 2, deps: [1]) — the shape that produced the
// bottleneck-contamination bug: slice 1's feature would otherwise look like
// it blocks slice 2's feature on a genuine cross-project dependency.
func buildTestPlan(t *testing.T, wipnoteDir, planID, feat1, feat2 string) {
	t.Helper()
	plan := planyaml.NewPlan(planID, "Test plan", "for reindexPlanEdges tests")
	plan.Slices = []planyaml.PlanSlice{
		{ID: feat1, Num: 1, Title: "Foundation"},
		{ID: feat2, Num: 2, Title: "Dependent", Deps: []int{1}},
	}
	path := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	if err := planyaml.Save(path, plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}
}

func edgeMetadata(t *testing.T, database *sql.DB, fromID, toID, relType string) map[string]string {
	t.Helper()
	var raw sql.NullString
	row := database.QueryRow(
		`SELECT metadata FROM graph_edges WHERE from_node_id = ? AND to_node_id = ? AND relationship_type = ?`,
		fromID, toID, relType,
	)
	if err := row.Scan(&raw); err != nil {
		t.Fatalf("scan edge metadata %s->%s: %v", fromID, toID, err)
	}
	if !raw.Valid || raw.String == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw.String), &m); err != nil {
		t.Fatalf("unmarshal edge metadata: %v", err)
	}
	return m
}

func TestReindexPlanEdges_StampsPlanSliceOrigin(t *testing.T) {
	dir := t.TempDir()
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	buildTestPlan(t, dir, "plan-origin1", "feat-origin1a", "feat-origin1b")

	total, upserted, errs := reindexPlanEdges(database, dir)
	if total != 1 || errs != 0 {
		t.Fatalf("total=%d errs=%d, want total=1 errs=0", total, errs)
	}
	if upserted == 0 {
		t.Fatalf("expected at least one edge upserted")
	}

	meta := edgeMetadata(t, database, "feat-origin1b", "feat-origin1a", "blocked_by")
	if meta == nil {
		t.Fatalf("expected metadata on plan-slice blocked_by edge, got none")
	}
	if meta["origin"] != graph.EdgeOriginPlanSlice {
		t.Errorf("origin = %q, want %q", meta["origin"], graph.EdgeOriginPlanSlice)
	}
	if meta["plan_id"] != "plan-origin1" {
		t.Errorf("plan_id = %q, want plan-origin1", meta["plan_id"])
	}
}

// TestReindexPlanEdges_ExcludedFromBottlenecks is the end-to-end regression
// check for bug-d0489158: a plan's foundational slice must not surface as a
// project bottleneck purely because it is slice 1 of N in a plan's authoring
// order.
func TestReindexPlanEdges_ExcludedFromBottlenecks(t *testing.T) {
	dir := t.TempDir()
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	for _, id := range []string{"feat-bn1", "feat-bn2", "feat-bn3", "feat-bn4"} {
		if _, err := database.Exec(
			`INSERT INTO features (id, type, title, status) VALUES (?, 'feature', ?, 'todo')`,
			id, id,
		); err != nil {
			t.Fatalf("seed feature %s: %v", id, err)
		}
	}

	// One plan: slice 1 (feat-bn1) is depended on by slices 2, 3, 4.
	plan := planyaml.NewPlan("plan-bn", "Bottleneck plan", "")
	plan.Slices = []planyaml.PlanSlice{
		{ID: "feat-bn1", Num: 1, Title: "Foundation"},
		{ID: "feat-bn2", Num: 2, Title: "Dependent A", Deps: []int{1}},
		{ID: "feat-bn3", Num: 3, Title: "Dependent B", Deps: []int{1}},
		{ID: "feat-bn4", Num: 4, Title: "Dependent C", Deps: []int{1}},
	}
	if err := planyaml.Save(filepath.Join(dir, "plans", "plan-bn.yaml"), plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	if _, _, errs := reindexPlanEdges(database, dir); errs != 0 {
		t.Fatalf("reindexPlanEdges errs = %d, want 0", errs)
	}

	bns, err := graph.FindBottlenecks(database)
	if err != nil {
		t.Fatalf("FindBottlenecks: %v", err)
	}
	for _, b := range bns {
		if b.ID == "feat-bn1" {
			t.Fatalf("feat-bn1 appeared as a bottleneck (block_count=%d) — plan-slice ordering leaked into FindBottlenecks", b.BlockCount)
		}
	}
}
