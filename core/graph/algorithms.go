package graph

import (
	"database/sql"
	"fmt"
	"strings"
)

// DBDetectCycles finds all circular dependencies in the graph_edges table
// using DFS with three-color marking. Returns a list of cycles, each being
// an ordered slice of node IDs.
func DBDetectCycles(db *sql.DB) ([][]string, error) {
	adj, err := loadAdjacencyList(db)
	if err != nil {
		return nil, fmt.Errorf("detect cycles: %w", err)
	}

	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(adj))
	var stack []string
	var cycles [][]string

	var dfs func(id string)
	dfs = func(id string) {
		color[id] = gray
		stack = append(stack, id)
		for _, nb := range adj[id] {
			switch color[nb] {
			case gray:
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
		stack = stack[:len(stack)-1]
		color[id] = black
	}

	for id := range adj {
		if color[id] == white {
			dfs(id)
		}
	}
	return cycles, nil
}

// DBShortestPath returns the BFS shortest path between two nodes using
// the graph_edges table. Returns node IDs in order, or nil if no path exists.
func DBShortestPath(db *sql.DB, fromID, toID string) ([]string, error) {
	adj, err := loadAdjacencyList(db)
	if err != nil {
		return nil, fmt.Errorf("shortest path: %w", err)
	}

	if _, ok := adj[fromID]; !ok {
		return nil, nil
	}
	if fromID == toID {
		return []string{fromID}, nil
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
		for _, nb := range adj[cur.id] {
			if visited[nb] {
				continue
			}
			newPath := make([]string, len(cur.path)+1)
			copy(newPath, cur.path)
			newPath[len(cur.path)] = nb
			if nb == toID {
				return newPath, nil
			}
			visited[nb] = true
			queue = append(queue, entry{id: nb, path: newPath})
		}
	}
	return nil, nil
}

// DBReachable returns all node IDs reachable from startID within maxHops
// hops using BFS. The start node is not included in results.
func DBReachable(db *sql.DB, startID string, maxHops int) ([]string, error) {
	adj, err := loadAdjacencyList(db)
	if err != nil {
		return nil, fmt.Errorf("reachable: %w", err)
	}

	if _, ok := adj[startID]; !ok {
		return nil, nil
	}

	type entry struct {
		id   string
		hops int
	}
	visited := map[string]bool{startID: true}
	queue := []entry{{id: startID, hops: 0}}
	var result []string

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.id != startID {
			result = append(result, cur.id)
		}
		if cur.hops >= maxHops {
			continue
		}
		for _, nb := range adj[cur.id] {
			if !visited[nb] {
				visited[nb] = true
				queue = append(queue, entry{id: nb, hops: cur.hops + 1})
			}
		}
	}
	return result, nil
}

// loadAdjacencyList reads all edges from graph_edges into an adjacency list.
// Also ensures every to_node_id appears as a key so DFS visits all nodes.
func loadAdjacencyList(db *sql.DB) (map[string][]string, error) {
	rows, err := db.Query(`SELECT from_node_id, to_node_id FROM graph_edges`)
	if err != nil {
		return nil, fmt.Errorf("query edges: %w", err)
	}
	defer rows.Close()

	adj := make(map[string][]string)
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			return nil, fmt.Errorf("scan edge: %w", err)
		}
		adj[from] = append(adj[from], to)
		if _, ok := adj[to]; !ok {
			adj[to] = nil
		}
	}
	return adj, rows.Err()
}

// resolveNodeIDs converts a list of IDs to NodeResults with metadata.
func resolveNodeIDs(db *sql.DB, ids []string) ([]NodeResult, error) {
	q := &QueryBuilder{db: db}
	return q.resolveNodes(ids)
}

// ResolveNodeID looks up a single node ID and returns its metadata.
func ResolveNodeID(db *sql.DB, id string) (NodeResult, error) {
	results, err := resolveNodeIDs(db, []string{id})
	if err != nil {
		return NodeResult{}, err
	}
	if len(results) == 0 {
		return NodeResult{ID: id}, nil
	}
	return results[0], nil
}

// FormatNodeLabel returns "id (title)" or just "id" if title is empty.
func FormatNodeLabel(id string, results map[string]NodeResult) string {
	if r, ok := results[id]; ok && r.Title != "" {
		title := r.Title
		if len(title) > 40 {
			title = title[:39] + "…"
		}
		return fmt.Sprintf("%s (%s)", id, title)
	}
	return id
}

// ResolveToMap resolves a set of IDs and returns a lookup map.
func ResolveToMap(db *sql.DB, ids []string) map[string]NodeResult {
	m := make(map[string]NodeResult, len(ids))
	results, err := resolveNodeIDs(db, ids)
	if err != nil {
		return m
	}
	for _, r := range results {
		m[r.ID] = r
	}
	return m
}

// AllUniqueIDs extracts all unique IDs from a list of cycles.
func AllUniqueIDs(cycles [][]string) []string {
	seen := make(map[string]bool)
	var ids []string
	for _, cycle := range cycles {
		for _, id := range cycle {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// FormatPath returns a human-readable "a -> b -> c" path string.
func FormatPath(path []string) string {
	return strings.Join(path, " -> ")
}
