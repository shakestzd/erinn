package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/internal/planyaml"
	"github.com/shakestzd/wipnote/core/workitem"
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
