package graph

import (
	"github.com/shakestzd/wipnote/core/models"
)

// ByStatus filters nodes by status.
func ByStatus(nodes []*models.Node, status models.NodeStatus) []*models.Node {
	var out []*models.Node
	for _, n := range nodes {
		if n.Status == status {
			out = append(out, n)
		}
	}
	return out
}

// ByType filters nodes by type string (e.g. "feature", "spike", "bug").
func ByType(nodes []*models.Node, nodeType string) []*models.Node {
	var out []*models.Node
	for _, n := range nodes {
		if n.Type == nodeType {
			out = append(out, n)
		}
	}
	return out
}

// ByTrack filters nodes belonging to a specific track.
func ByTrack(nodes []*models.Node, trackID string) []*models.Node {
	var out []*models.Node
	for _, n := range nodes {
		if n.TrackID == trackID {
			out = append(out, n)
		}
	}
	return out
}

// FindByID returns the first node with the given ID, or nil.
func FindByID(nodes []*models.Node, id string) *models.Node {
	for _, n := range nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// buildIndex returns a map from node ID to *Node for O(1) lookups.
func buildIndex(nodes []*models.Node) map[string]*models.Node {
	idx := make(map[string]*models.Node, len(nodes))
	for _, n := range nodes {
		idx[n.ID] = n
	}
	return idx
}

// neighbors returns the IDs of all nodes directly reachable via any edge from n.
func neighbors(n *models.Node) []string {
	var ids []string
	for _, edges := range n.Edges {
		for _, e := range edges {
			ids = append(ids, e.TargetID)
		}
	}
	return ids
}

// DetectCycles finds circular dependencies using DFS with three-color marking.
// Returns a list of cycles; each cycle is an ordered slice of node IDs.
// Handles disconnected graphs by iterating over all unvisited nodes.
func DetectCycles(nodes []*models.Node) [][]string {
	idx := buildIndex(nodes)
	const (
		white = 0 // unvisited
		gray  = 1 // in current DFS path
		black = 2 // fully visited
	)
	color := make(map[string]int, len(nodes))
	stack := make([]string, 0, len(nodes))
	var cycles [][]string

	var dfs func(id string)
	dfs = func(id string) {
		color[id] = gray
		stack = append(stack, id)
		if n, ok := idx[id]; ok {
			for _, nb := range neighbors(n) {
				switch color[nb] {
				case gray:
					// Back-edge found — extract the cycle segment from the stack.
					start := len(stack) - 1
					for start > 0 && stack[start] != nb {
						start--
					}
					cycle := make([]string, len(stack)-start)
					copy(cycle, stack[start:])
					cycles = append(cycles, cycle)
				case white:
					dfs(nb)
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
	}

	for _, n := range nodes {
		if color[n.ID] == white {
			dfs(n.ID)
		}
	}
	return cycles
}

// FindPath returns the shortest path between fromID and toID using BFS,
// stopping after maxDepth hops. Returns the path nodes and true on success.
func FindPath(nodes []*models.Node, fromID, toID string, maxDepth int) ([]*models.Node, bool) {
	idx := buildIndex(nodes)
	if _, ok := idx[fromID]; !ok {
		return nil, false
	}
	if fromID == toID {
		return []*models.Node{idx[fromID]}, true
	}
	type entry struct {
		id   string
		path []string
	}
	visited := map[string]bool{fromID: true}
	queue := []entry{{id: fromID, path: []string{fromID}}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if len(cur.path)-1 >= maxDepth {
			continue
		}
		n, ok := idx[cur.id]
		if !ok {
			continue
		}
		for _, nb := range neighbors(n) {
			if visited[nb] {
				continue
			}
			newPath := append(append([]string(nil), cur.path...), nb)
			if nb == toID {
				result := make([]*models.Node, 0, len(newPath))
				for _, id := range newPath {
					result = append(result, idx[id])
				}
				return result, true
			}
			visited[nb] = true
			queue = append(queue, entry{id: nb, path: newPath})
		}
	}
	return nil, false
}

// Reachable returns all nodes reachable from startID within maxHops hops using BFS.
// The start node itself is not included in the result.
func Reachable(nodes []*models.Node, startID string, maxHops int) []*models.Node {
	idx := buildIndex(nodes)
	if _, ok := idx[startID]; !ok {
		return nil
	}
	type entry struct {
		id   string
		hops int
	}
	visited := map[string]bool{startID: true}
	queue := []entry{{id: startID, hops: 0}}
	var result []*models.Node
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.id != startID {
			result = append(result, idx[cur.id])
		}
		if cur.hops >= maxHops {
			continue
		}
		n, ok := idx[cur.id]
		if !ok {
			continue
		}
		for _, nb := range neighbors(n) {
			if !visited[nb] {
				visited[nb] = true
				queue = append(queue, entry{id: nb, hops: cur.hops + 1})
			}
		}
	}
	return result
}
