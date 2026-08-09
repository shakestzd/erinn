package graph

import (
	"database/sql"
	"fmt"
)

// Edge origin tags. Mechanical writers that synthesize graph_edges rows from
// some other source of truth (a plan YAML's slice.deps, a batch-apply spec,
// …) stamp one of these into the edge's metadata JSON under the "origin" key
// so that consumers can tell a derived edge apart from a human-asserted one
// (e.g. via `wipnote link add --rel blocks`, which leaves metadata NULL).
//
// EdgeOriginPlanSlice specifically marks blocked_by edges that encode a plan
// slice's authoring order (slice N declared "deps: [M]" in the plan YAML).
// That ordering is not an asserted cross-project dependency, so it must be
// excluded from FindBottlenecks — see bug-d0489158. It is written by both
// reindex_plan_edges.go (rebuilt on every reindex) and plan_wire.go (written
// once when a plan is wired to a track), since both derive from the same
// slice.deps field.
const EdgeOriginPlanSlice = "plan_slice_deps"

// EdgeOriginBatchApply marks blocked_by edges created by `wipnote batch
// apply` from a batch spec's inline `blocked_by:` declarations. Unlike
// EdgeOriginPlanSlice these represent a genuine asserted dependency between
// sibling features in the batch (equivalent in intent to `link add`, just
// authored in bulk) — so they are NOT excluded from FindBottlenecks. The tag
// exists purely for measurability: see bug-f55532ba.
const EdgeOriginBatchApply = "batch_apply"

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
//
// archSrc resolves titles for architecture cards, which are genuine hubs:
// every card carries a learned_from/has_learning edge pair per linked work
// item. Passing nil leaves those rows with bare IDs.
func FindHubs(db *sql.DB, archSrc ArchSource, minEdges int) ([]NodeResult, error) {
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

	resolved := ResolveToMap(db, archSrc, ids)
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
//
// Edges tagged with EdgeOriginPlanSlice are excluded: they encode a plan's
// slice authoring order, not an asserted cross-project dependency, and
// counting them would make a plan's foundational slice look like a
// project-wide bottleneck (bug-d0489158).
func FindBottlenecks(db *sql.DB) ([]BottleneckResult, error) {
	rows, err := db.Query(`
		SELECT to_node_id, COUNT(*) as block_count
		FROM graph_edges
		WHERE relationship_type = 'blocked_by'
		  AND COALESCE(json_extract(metadata, '$.origin'), '') != ?
		GROUP BY to_node_id
		ORDER BY block_count DESC`, EdgeOriginPlanSlice)
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

	// No arch source: blocked_by edges never target an architecture card
	// (cards only ever carry learned_from/has_learning), so no ID reaching
	// here can be an arch node.
	resolved := ResolveToMap(db, nil, ids)
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
