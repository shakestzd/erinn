package graph

import (
	"database/sql"
	"fmt"
)

// FindOrphans returns node IDs that have zero edges (neither source nor target).
// Checks both features and tracks tables against graph_edges.
func FindOrphans(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`
		SELECT id FROM features
		WHERE id NOT IN (SELECT from_node_id FROM graph_edges)
		  AND id NOT IN (SELECT to_node_id FROM graph_edges)
		UNION
		SELECT id FROM tracks
		WHERE id NOT IN (SELECT from_node_id FROM graph_edges)
		  AND id NOT IN (SELECT to_node_id FROM graph_edges)`)
	if err != nil {
		return nil, fmt.Errorf("find orphans: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan orphan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// FindHubs returns node IDs that participate in at least minEdges edges
// (counting both incoming and outgoing). Ordered by edge count descending.
func FindHubs(db *sql.DB, minEdges int) ([]NodeResult, error) {
	rows, err := db.Query(`
		SELECT node_id, COUNT(*) as edge_count FROM (
			SELECT from_node_id AS node_id FROM graph_edges
			UNION ALL
			SELECT to_node_id AS node_id FROM graph_edges
		) GROUP BY node_id
		HAVING edge_count >= ?
		ORDER BY edge_count DESC`, minEdges)
	if err != nil {
		return nil, fmt.Errorf("find hubs: %w", err)
	}
	defer rows.Close()

	type hubEntry struct {
		id        string
		edgeCount int
	}
	var entries []hubEntry
	var ids []string
	for rows.Next() {
		var h hubEntry
		if err := rows.Scan(&h.id, &h.edgeCount); err != nil {
			return nil, fmt.Errorf("scan hub: %w", err)
		}
		entries = append(entries, h)
		ids = append(ids, h.id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	resolved := ResolveToMap(db, ids)
	results := make([]NodeResult, len(entries))
	for i, h := range entries {
		if r, ok := resolved[h.id]; ok {
			results[i] = r
		} else {
			results[i] = NodeResult{ID: h.id}
		}
	}
	return results, nil
}

// FindBottlenecks returns nodes that block the most other nodes via
// "blocked_by" edges (i.e., nodes that appear as to_node_id in blocked_by
// edges). Ordered by block count descending.
func FindBottlenecks(db *sql.DB) ([]BottleneckResult, error) {
	rows, err := db.Query(`
		SELECT to_node_id, COUNT(*) as block_count
		FROM graph_edges
		WHERE relationship_type = 'blocked_by'
		GROUP BY to_node_id
		ORDER BY block_count DESC`)
	if err != nil {
		return nil, fmt.Errorf("find bottlenecks: %w", err)
	}
	defer rows.Close()

	var entries []BottleneckResult
	var ids []string
	for rows.Next() {
		var b BottleneckResult
		if err := rows.Scan(&b.ID, &b.BlockCount); err != nil {
			return nil, fmt.Errorf("scan bottleneck: %w", err)
		}
		entries = append(entries, b)
		ids = append(ids, b.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	resolved := ResolveToMap(db, ids)
	for i := range entries {
		if r, ok := resolved[entries[i].ID]; ok {
			entries[i].Title = r.Title
			entries[i].Status = r.Status
		}
	}
	return entries, nil
}

// BottleneckResult represents a node that blocks other nodes.
type BottleneckResult struct {
	ID         string
	Title      string
	Status     string
	BlockCount int
}
