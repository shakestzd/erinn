package graph_test

import (
	"testing"

	"github.com/shakestzd/wipnote/core/graph"
)

// --- DBDetectCycles ---

func TestDBDetectCycles_Empty(t *testing.T) {
	database := openTestDB(t)
	cycles, err := graph.DBDetectCycles(database)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cycles) != 0 {
		t.Errorf("expected 0 cycles on empty graph, got %d", len(cycles))
	}
}

func TestDBDetectCycles_NoCycle(t *testing.T) {
	database := openTestDB(t)
	seedEdge(t, database, "a", "feature", "b", "feature", "blocks")
	seedEdge(t, database, "b", "feature", "c", "feature", "blocks")

	cycles, err := graph.DBDetectCycles(database)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cycles) != 0 {
		t.Errorf("expected 0 cycles in linear chain, got %d", len(cycles))
	}
}

func TestDBDetectCycles_SimpleCycle(t *testing.T) {
	database := openTestDB(t)
	seedEdge(t, database, "a", "feature", "b", "feature", "blocked_by")
	seedEdge(t, database, "b", "feature", "c", "feature", "blocked_by")
	seedEdge(t, database, "c", "feature", "a", "feature", "blocked_by")

	cycles, err := graph.DBDetectCycles(database)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cycles) == 0 {
		t.Fatal("expected at least one cycle, got none")
	}
	if len(cycles[0]) != 3 {
		t.Errorf("expected cycle length 3, got %d: %v", len(cycles[0]), cycles[0])
	}
}

func TestDBDetectCycles_SelfLoop(t *testing.T) {
	database := openTestDB(t)
	seedEdge(t, database, "a", "feature", "a", "feature", "blocked_by")

	cycles, err := graph.DBDetectCycles(database)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cycles) == 0 {
		t.Fatal("expected cycle for self-loop")
	}
}

// --- DBShortestPath ---

func TestDBShortestPath_SameNode(t *testing.T) {
	database := openTestDB(t)
	seedEdge(t, database, "a", "feature", "b", "feature", "blocks")

	path, err := graph.DBShortestPath(database, "a", "a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(path) != 1 || path[0] != "a" {
		t.Errorf("expected [a], got %v", path)
	}
}

func TestDBShortestPath_Direct(t *testing.T) {
	database := openTestDB(t)
	seedEdge(t, database, "a", "feature", "b", "feature", "blocks")

	path, err := graph.DBShortestPath(database, "a", "b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(path) != 2 || path[0] != "a" || path[1] != "b" {
		t.Errorf("expected [a b], got %v", path)
	}
}

func TestDBShortestPath_MultiHop(t *testing.T) {
	database := openTestDB(t)
	seedEdge(t, database, "a", "feature", "b", "feature", "blocks")
	seedEdge(t, database, "b", "feature", "c", "feature", "blocks")
	seedEdge(t, database, "c", "feature", "d", "feature", "blocks")

	path, err := graph.DBShortestPath(database, "a", "d")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(path) != 4 {
		t.Errorf("expected path length 4, got %d: %v", len(path), path)
	}
}

func TestDBShortestPath_NoPath(t *testing.T) {
	database := openTestDB(t)
	seedEdge(t, database, "a", "feature", "b", "feature", "blocks")
	// c is disconnected.
	seedEdge(t, database, "c", "feature", "d", "feature", "blocks")

	path, err := graph.DBShortestPath(database, "a", "d")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != nil {
		t.Errorf("expected nil path, got %v", path)
	}
}

func TestDBShortestPath_UnknownStart(t *testing.T) {
	database := openTestDB(t)
	path, err := graph.DBShortestPath(database, "x", "y")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != nil {
		t.Errorf("expected nil for unknown start, got %v", path)
	}
}

// --- DBReachable ---

func TestDBReachable_Empty(t *testing.T) {
	database := openTestDB(t)
	result, err := graph.DBReachable(database, "a", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 results, got %d", len(result))
	}
}

func TestDBReachable_LinearChain(t *testing.T) {
	database := openTestDB(t)
	seedEdge(t, database, "a", "feature", "b", "feature", "blocks")
	seedEdge(t, database, "b", "feature", "c", "feature", "blocks")
	seedEdge(t, database, "c", "feature", "d", "feature", "blocks")

	result, err := graph.DBReachable(database, "a", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 reachable (b, c), got %d: %v", len(result), result)
	}
}

func TestDBReachable_AllReachable(t *testing.T) {
	database := openTestDB(t)
	seedEdge(t, database, "a", "feature", "b", "feature", "blocks")
	seedEdge(t, database, "b", "feature", "c", "feature", "blocks")

	result, err := graph.DBReachable(database, "a", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 reachable, got %d: %v", len(result), result)
	}
}

func TestDBReachable_ZeroHops(t *testing.T) {
	database := openTestDB(t)
	seedEdge(t, database, "a", "feature", "b", "feature", "blocks")

	result, err := graph.DBReachable(database, "a", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 reachable with maxHops=0, got %d", len(result))
	}
}

func TestFormatPath(t *testing.T) {
	got := graph.FormatPath([]string{"a", "b", "c"})
	want := "a -> b -> c"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
