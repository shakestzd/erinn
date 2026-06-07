package graph

import (
	"testing"

	"github.com/shakestzd/wipnote/core/models"
)

// makeNode is a test helper that creates a Node with the given ID.
func makeNode(id string) *models.Node {
	return &models.Node{ID: id, Title: id, Type: "feature"}
}

// addEdge is a test helper that appends a directed edge from src to dst.
func addEdge(src, dst *models.Node) {
	src.AddEdge(models.Edge{
		TargetID:     dst.ID,
		Relationship: models.RelBlockedBy,
	})
}

// nodeIDs extracts IDs from a node slice for easy comparison.
func nodeIDs(nodes []*models.Node) []string {
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	return ids
}

// --- DetectCycles ---

func TestDetectCycles_Empty(t *testing.T) {
	cycles := DetectCycles(nil)
	if len(cycles) != 0 {
		t.Errorf("expected 0 cycles on empty graph, got %d", len(cycles))
	}
}

func TestDetectCycles_SingleNode(t *testing.T) {
	a := makeNode("a")
	cycles := DetectCycles([]*models.Node{a})
	if len(cycles) != 0 {
		t.Errorf("expected 0 cycles for single node, got %d", len(cycles))
	}
}

func TestDetectCycles_LinearChain(t *testing.T) {
	a, b, c := makeNode("a"), makeNode("b"), makeNode("c")
	addEdge(a, b)
	addEdge(b, c)
	cycles := DetectCycles([]*models.Node{a, b, c})
	if len(cycles) != 0 {
		t.Errorf("expected 0 cycles in linear chain, got %d", len(cycles))
	}
}

func TestDetectCycles_Diamond(t *testing.T) {
	// a -> b, a -> c, b -> d, c -> d  (no cycle)
	a, b, c, d := makeNode("a"), makeNode("b"), makeNode("c"), makeNode("d")
	addEdge(a, b)
	addEdge(a, c)
	addEdge(b, d)
	addEdge(c, d)
	cycles := DetectCycles([]*models.Node{a, b, c, d})
	if len(cycles) != 0 {
		t.Errorf("expected 0 cycles in diamond, got %d", len(cycles))
	}
}

func TestDetectCycles_SimpleCycle(t *testing.T) {
	// a -> b -> c -> a
	a, b, c := makeNode("a"), makeNode("b"), makeNode("c")
	addEdge(a, b)
	addEdge(b, c)
	addEdge(c, a)
	cycles := DetectCycles([]*models.Node{a, b, c})
	if len(cycles) == 0 {
		t.Fatal("expected at least one cycle, got none")
	}
	// The cycle should contain 3 nodes.
	if len(cycles[0]) != 3 {
		t.Errorf("expected cycle length 3, got %d: %v", len(cycles[0]), cycles[0])
	}
}

func TestDetectCycles_SelfLoop(t *testing.T) {
	a := makeNode("a")
	addEdge(a, a)
	cycles := DetectCycles([]*models.Node{a})
	if len(cycles) == 0 {
		t.Fatal("expected cycle for self-loop, got none")
	}
}

func TestDetectCycles_Disconnected(t *testing.T) {
	// Two disconnected components: a->b (no cycle) and c->d->c (cycle)
	a, b := makeNode("a"), makeNode("b")
	c, d := makeNode("c"), makeNode("d")
	addEdge(a, b)
	addEdge(c, d)
	addEdge(d, c)
	cycles := DetectCycles([]*models.Node{a, b, c, d})
	if len(cycles) == 0 {
		t.Fatal("expected cycle from disconnected component, got none")
	}
}

// --- FindPath ---

func TestFindPath_SameNode(t *testing.T) {
	a := makeNode("a")
	path, ok := FindPath([]*models.Node{a}, "a", "a", 5)
	if !ok {
		t.Fatal("expected path from a to a")
	}
	if len(path) != 1 || path[0].ID != "a" {
		t.Errorf("expected [a], got %v", nodeIDs(path))
	}
}

func TestFindPath_NotFound(t *testing.T) {
	a, b := makeNode("a"), makeNode("b")
	// No edge between a and b.
	_, ok := FindPath([]*models.Node{a, b}, "a", "b", 5)
	if ok {
		t.Error("expected no path, but found one")
	}
}

func TestFindPath_DirectEdge(t *testing.T) {
	a, b := makeNode("a"), makeNode("b")
	addEdge(a, b)
	path, ok := FindPath([]*models.Node{a, b}, "a", "b", 5)
	if !ok {
		t.Fatal("expected path a->b")
	}
	if got := nodeIDs(path); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("expected [a b], got %v", got)
	}
}

func TestFindPath_ShortestPath(t *testing.T) {
	// a->b->d, a->c->d  (two paths of same length; both are valid)
	a, b, c, d := makeNode("a"), makeNode("b"), makeNode("c"), makeNode("d")
	addEdge(a, b)
	addEdge(b, d)
	addEdge(a, c)
	addEdge(c, d)
	path, ok := FindPath([]*models.Node{a, b, c, d}, "a", "d", 5)
	if !ok {
		t.Fatal("expected path a->d")
	}
	if len(path) != 3 {
		t.Errorf("expected path length 3, got %d: %v", len(path), nodeIDs(path))
	}
}

func TestFindPath_MaxDepthExceeded(t *testing.T) {
	// a->b->c->d, but maxDepth=1 should not reach d
	a, b, c, d := makeNode("a"), makeNode("b"), makeNode("c"), makeNode("d")
	addEdge(a, b)
	addEdge(b, c)
	addEdge(c, d)
	_, ok := FindPath([]*models.Node{a, b, c, d}, "a", "d", 1)
	if ok {
		t.Error("expected no path with maxDepth=1 for 3-hop route")
	}
}

func TestFindPath_UnknownFromID(t *testing.T) {
	a := makeNode("a")
	_, ok := FindPath([]*models.Node{a}, "z", "a", 5)
	if ok {
		t.Error("expected false for unknown fromID")
	}
}

func TestFindPath_EmptyGraph(t *testing.T) {
	_, ok := FindPath(nil, "a", "b", 5)
	if ok {
		t.Error("expected false for empty graph")
	}
}

// --- Reachable ---

func TestReachable_Empty(t *testing.T) {
	result := Reachable(nil, "a", 3)
	if len(result) != 0 {
		t.Errorf("expected empty result for nil graph, got %d nodes", len(result))
	}
}

func TestReachable_UnknownStart(t *testing.T) {
	a := makeNode("a")
	result := Reachable([]*models.Node{a}, "z", 3)
	if len(result) != 0 {
		t.Errorf("expected empty result for unknown start, got %d", len(result))
	}
}

func TestReachable_ZeroHops(t *testing.T) {
	a, b := makeNode("a"), makeNode("b")
	addEdge(a, b)
	result := Reachable([]*models.Node{a, b}, "a", 0)
	if len(result) != 0 {
		t.Errorf("expected 0 reachable with maxHops=0, got %d", len(result))
	}
}

func TestReachable_LinearChain(t *testing.T) {
	// a->b->c->d, start=a, maxHops=2 should reach b and c only
	a, b, c, d := makeNode("a"), makeNode("b"), makeNode("c"), makeNode("d")
	addEdge(a, b)
	addEdge(b, c)
	addEdge(c, d)
	result := Reachable([]*models.Node{a, b, c, d}, "a", 2)
	if len(result) != 2 {
		t.Errorf("expected 2 reachable nodes, got %d: %v", len(result), nodeIDs(result))
	}
}

func TestReachable_AllReachable(t *testing.T) {
	a, b, c := makeNode("a"), makeNode("b"), makeNode("c")
	addEdge(a, b)
	addEdge(b, c)
	result := Reachable([]*models.Node{a, b, c}, "a", 10)
	if len(result) != 2 {
		t.Errorf("expected 2 reachable nodes, got %d: %v", len(result), nodeIDs(result))
	}
}

func TestReachable_Disconnected(t *testing.T) {
	// a->b disconnected from c->d; start=a should not reach c or d
	a, b := makeNode("a"), makeNode("b")
	c, d := makeNode("c"), makeNode("d")
	addEdge(a, b)
	addEdge(c, d)
	result := Reachable([]*models.Node{a, b, c, d}, "a", 10)
	if len(result) != 1 || result[0].ID != "b" {
		t.Errorf("expected [b], got %v", nodeIDs(result))
	}
}

func TestReachable_StarGraph(t *testing.T) {
	// hub -> a, b, c all reachable in 1 hop
	hub := makeNode("hub")
	a, b, c := makeNode("a"), makeNode("b"), makeNode("c")
	addEdge(hub, a)
	addEdge(hub, b)
	addEdge(hub, c)
	result := Reachable([]*models.Node{hub, a, b, c}, "hub", 1)
	if len(result) != 3 {
		t.Errorf("expected 3 reachable nodes, got %d: %v", len(result), nodeIDs(result))
	}
}
