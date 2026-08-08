package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

func TestWirePlan_BasicWiring(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a project and track + features.
	p, err := workitem.Open(dir, "test-agent")
	if err != nil {
		t.Fatal(err)
	}

	track, err := p.Tracks.Create("My Track")
	if err != nil {
		t.Fatal(err)
	}

	feat1, err := p.Features.Create("Slice Alpha",
		workitem.FeatWithTrack(track.ID),
	)
	if err != nil {
		t.Fatal(err)
	}

	feat2, err := p.Features.Create("Slice Beta",
		workitem.FeatWithTrack(track.ID),
	)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()

	// Create a YAML plan with two approved slices.
	planID := "plan-testwire1"
	plan := &planyaml.PlanYAML{}
	plan.Meta.ID = planID
	plan.Meta.Title = "Wire Test Plan"
	plan.Meta.Status = "ready"
	plan.Meta.Version = 1
	plan.Slices = []planyaml.PlanSlice{
		{Num: 1, ID: "s1", Title: "Slice Alpha", Approved: true},
		{Num: 2, ID: "s2", Title: "Slice Beta", Approved: true, Deps: []int{1}},
	}

	planPath := filepath.Join(plansDir, planID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatal(err)
	}

	// Run wirePlan.
	if err := wirePlan(dir, planID, track.ID); err != nil {
		t.Fatalf("wirePlan: %v", err)
	}

	// Reload plan and check status.
	updated, err := planyaml.Load(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Meta.Status != "finalized" {
		t.Errorf("plan status = %q, want finalized", updated.Meta.Status)
	}
	if updated.Meta.TrackID != track.ID {
		t.Errorf("plan track_id = %q, want %q", updated.Meta.TrackID, track.ID)
	}

	// Verify planned_in edges were added to features.
	p2, err := workitem.Open(dir, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()

	for _, featID := range []string{feat1.ID, feat2.ID} {
		feat, err := p2.Features.Get(featID)
		if err != nil {
			t.Fatalf("get feature %s: %v", featID, err)
		}
		edges := feat.Edges["planned_in"]
		found := false
		for _, e := range edges {
			if e.TargetID == planID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("feature %s missing planned_in edge to %s", featID, planID)
		}
	}

	// Verify blocked_by edge: feat2 → feat1.
	feat2Node, err := p2.Features.Get(feat2.ID)
	if err != nil {
		t.Fatal(err)
	}
	blockedEdges := feat2Node.Edges["blocked_by"]
	foundDep := false
	for _, e := range blockedEdges {
		if e.TargetID == feat1.ID {
			foundDep = true
			break
		}
	}
	if !foundDep {
		t.Errorf("feat2 missing blocked_by edge to feat1")
	}
}

// TestWirePlan_BlockedByCarriesPlanSliceOrigin is the bug-f55532ba regression
// check: plan_wire.go's blocked_by edges must stamp the same
// graph.EdgeOriginPlanSlice metadata that reindex_plan_edges.go stamps on
// the equivalent edge it later rebuilds from the same slice.deps field, so
// that (a) the two writers agree and (b) graph.FindBottlenecks can exclude
// this authoring-order signal (bug-d0489158) regardless of which writer
// created the row.
func TestWirePlan_BlockedByCarriesPlanSliceOrigin(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}

	p, err := workitem.Open(dir, "test-agent")
	if err != nil {
		t.Fatal(err)
	}

	track, err := p.Tracks.Create("Origin Track")
	if err != nil {
		t.Fatal(err)
	}

	feat1, err := p.Features.Create("Slice Alpha", workitem.FeatWithTrack(track.ID))
	if err != nil {
		t.Fatal(err)
	}
	feat2, err := p.Features.Create("Slice Beta", workitem.FeatWithTrack(track.ID))
	if err != nil {
		t.Fatal(err)
	}
	p.Close()

	planID := "plan-testorigin"
	plan := &planyaml.PlanYAML{}
	plan.Meta.ID = planID
	plan.Meta.Title = "Origin Test Plan"
	plan.Meta.Status = "ready"
	plan.Meta.Version = 1
	plan.Slices = []planyaml.PlanSlice{
		{Num: 1, ID: "s1", Title: "Slice Alpha", Approved: true},
		{Num: 2, ID: "s2", Title: "Slice Beta", Approved: true, Deps: []int{1}},
	}
	planPath := filepath.Join(plansDir, planID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatal(err)
	}

	if err := wirePlan(dir, planID, track.ID); err != nil {
		t.Fatalf("wirePlan: %v", err)
	}

	p2, err := workitem.Open(dir, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()

	// Verify the canonical (HTML) edge exists...
	feat2Node, err := p2.Features.Get(feat2.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range feat2Node.Edges["blocked_by"] {
		if e.TargetID == feat1.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("feat2 missing blocked_by edge to feat1")
	}

	// ...and that the SQLite dual-write (what graph.FindBottlenecks actually
	// queries) carries the origin tag. Edge.Properties does not round-trip
	// through canonical HTML (edgeData in htmlwriter.go has no properties
	// field), so the SQLite row — populated at AddEdge time via
	// Collection.AddEdge's dual-write — is the only place this is checkable.
	meta := edgeMetadata(t, p2.DB, feat2.ID, feat1.ID, "blocked_by")
	if meta == nil {
		t.Fatalf("expected metadata on plan_wire blocked_by edge, got none")
	}
	if meta["origin"] != graph.EdgeOriginPlanSlice {
		t.Errorf("origin = %q, want %q", meta["origin"], graph.EdgeOriginPlanSlice)
	}
	if meta["plan_id"] != planID {
		t.Errorf("plan_id = %q, want %q", meta["plan_id"], planID)
	}
}

func TestWirePlan_NoApprovedSlicesTreatsAllAsApproved(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}

	p, err := workitem.Open(dir, "test-agent")
	if err != nil {
		t.Fatal(err)
	}

	track, err := p.Tracks.Create("Track No Approved")
	if err != nil {
		t.Fatal(err)
	}

	_, err = p.Features.Create("Slice One",
		workitem.FeatWithTrack(track.ID),
	)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()

	planID := "plan-testwire2"
	plan := &planyaml.PlanYAML{}
	plan.Meta.ID = planID
	plan.Meta.Title = "No Approved Plan"
	plan.Meta.Status = "ready"
	plan.Meta.Version = 1
	// Approved: false (default) — all slices should be treated as approved.
	plan.Slices = []planyaml.PlanSlice{
		{Num: 1, ID: "s1", Title: "Slice One", Approved: false},
	}

	planPath := filepath.Join(plansDir, planID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatal(err)
	}

	if err := wirePlan(dir, planID, track.ID); err != nil {
		t.Fatalf("wirePlan: %v", err)
	}

	updated, err := planyaml.Load(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Meta.Status != "finalized" {
		t.Errorf("plan status = %q, want finalized", updated.Meta.Status)
	}
}

func TestWirePlan_InvalidTrack(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}

	planID := "plan-testwire3"
	plan := &planyaml.PlanYAML{}
	plan.Meta.ID = planID
	plan.Meta.Title = "Bad Track Plan"
	plan.Meta.Status = "ready"
	plan.Meta.Version = 1

	planPath := filepath.Join(plansDir, planID+".yaml")
	if err := planyaml.Save(planPath, plan); err != nil {
		t.Fatal(err)
	}

	err := wirePlan(dir, planID, "trk-doesnotexist")
	if err == nil {
		t.Error("expected error for invalid track, got nil")
	}
}
