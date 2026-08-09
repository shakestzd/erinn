package graph_test

import (
	"testing"

	"github.com/shakestzd/wipnote/core/graph"
)

// --- FindOrphans ---

func TestFindOrphans_Empty(t *testing.T) {
	database := openTestDB(t)
	orphans, err := graph.FindOrphans(database)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("expected 0 orphans on empty DB, got %d", len(orphans))
	}
}

func TestFindOrphans_AllLinked(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "feat-a", "A", "todo")
	seedFeature(t, database, "feat-b", "B", "todo")
	seedEdge(t, database, "feat-a", "feature", "feat-b", "feature", "blocks")

	orphans, err := graph.FindOrphans(database)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("expected 0 orphans when all linked, got %d: %v", len(orphans), orphans)
	}
}

func TestFindOrphans_OneOrphan(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "feat-a", "A", "todo")
	seedFeature(t, database, "feat-b", "B", "todo")
	seedFeature(t, database, "feat-c", "C", "todo")
	seedEdge(t, database, "feat-a", "feature", "feat-b", "feature", "blocks")

	orphans, err := graph.FindOrphans(database)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orphans) != 1 || orphans[0] != "feat-c" {
		t.Errorf("expected [feat-c], got %v", orphans)
	}
}

func TestFindOrphans_IncludesTracks(t *testing.T) {
	database := openTestDB(t)
	seedTrack(t, database, "trk-1", "Track 1", "active")
	seedFeature(t, database, "feat-a", "A", "todo")
	// feat-a is linked but trk-1 is not.
	seedEdge(t, database, "feat-a", "feature", "feat-a", "feature", "relates_to")

	orphans, err := graph.FindOrphans(database)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, o := range orphans {
		if o == "trk-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected trk-1 in orphans, got %v", orphans)
	}
}

// --- FindHubs ---

func TestFindHubs_Empty(t *testing.T) {
	database := openTestDB(t)
	hubs, err := graph.FindHubs(database, nil, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hubs) != 0 {
		t.Errorf("expected 0 hubs, got %d", len(hubs))
	}
}

func TestFindHubs_StarGraph(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "hub", "Hub Node", "todo")
	seedFeature(t, database, "a", "A", "todo")
	seedFeature(t, database, "b", "B", "todo")
	seedFeature(t, database, "c", "C", "todo")
	seedEdge(t, database, "hub", "feature", "a", "feature", "contains")
	seedEdge(t, database, "hub", "feature", "b", "feature", "contains")
	seedEdge(t, database, "hub", "feature", "c", "feature", "contains")

	hubs, err := graph.FindHubs(database, nil, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hubs) != 1 || hubs[0].ID != "hub" {
		t.Errorf("expected [hub] with minEdges=3, got %v", hubs)
	}
}

func TestFindHubs_MinEdgesFilter(t *testing.T) {
	database := openTestDB(t)
	seedEdge(t, database, "a", "feature", "b", "feature", "blocks")
	// a has 1 edge, b has 1 edge. minEdges=2 should return nothing.
	hubs, err := graph.FindHubs(database, nil, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hubs) != 0 {
		t.Errorf("expected 0 hubs with minEdges=2, got %d", len(hubs))
	}
}

// --- FindBottlenecks ---

func TestFindBottlenecks_Empty(t *testing.T) {
	database := openTestDB(t)
	bns, err := graph.FindBottlenecks(database)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(bns) != 0 {
		t.Errorf("expected 0 bottlenecks, got %d", len(bns))
	}
}

func TestFindBottlenecks_SingleBlocker(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "feat-a", "Blocker", "in-progress")
	seedFeature(t, database, "feat-b", "B", "blocked")
	seedFeature(t, database, "feat-c", "C", "blocked")
	seedEdge(t, database, "feat-b", "feature", "feat-a", "feature", "blocked_by")
	seedEdge(t, database, "feat-c", "feature", "feat-a", "feature", "blocked_by")

	bns, err := graph.FindBottlenecks(database)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(bns) != 1 {
		t.Fatalf("expected 1 bottleneck, got %d", len(bns))
	}
	if bns[0].ID != "feat-a" {
		t.Errorf("expected feat-a, got %s", bns[0].ID)
	}
	if bns[0].BlockCount != 2 {
		t.Errorf("expected block count 2, got %d", bns[0].BlockCount)
	}
	if bns[0].Title != "Blocker" {
		t.Errorf("expected title 'Blocker', got %q", bns[0].Title)
	}
}

func TestFindBottlenecks_OrderedByCount(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "feat-x", "X", "todo")
	seedFeature(t, database, "feat-y", "Y", "todo")
	// x blocks 1, y blocks 3
	seedEdge(t, database, "a", "feature", "feat-x", "feature", "blocked_by")
	seedEdge(t, database, "b", "feature", "feat-y", "feature", "blocked_by")
	seedEdge(t, database, "c", "feature", "feat-y", "feature", "blocked_by")
	seedEdge(t, database, "d", "feature", "feat-y", "feature", "blocked_by")

	bns, err := graph.FindBottlenecks(database)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(bns) < 2 {
		t.Fatalf("expected at least 2 bottlenecks, got %d", len(bns))
	}
	if bns[0].ID != "feat-y" {
		t.Errorf("expected feat-y first (most blocking), got %s", bns[0].ID)
	}
}
