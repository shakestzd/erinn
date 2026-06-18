// Package lineage holds the pure bidirectional BFS walk over the graph_edges
// table. It is the single source of truth for lineage traversal, consumed by
// both the `wipnote lineage` CLI command and the recap collector.
//
// The package takes only a *sql.DB; it performs no rendering and depends on no
// cobra/cmd wiring. Import direction is internal-only: cmd/wipnote and
// internal/recap import this; this imports core/db and core/graph.
package lineage

import (
	"database/sql"
	"fmt"
	"strings"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/models"
)

// Node is one hop in a forward or backward chain. It is the wire format for the
// CLI's --json output and a convenient internal representation for tree
// rendering and recap lineage chains.
type Node struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title,omitempty"`
	EdgeType string `json:"edge_type"`
	Depth    int    `json:"depth"`
	// Parent is the node ID that this hop was discovered from during BFS. For
	// the pivot's direct neighbours it equals the pivot. Used by the tree
	// renderer to build a real adjacency structure so branched walks don't
	// visually attach grandchildren to the wrong parent.
	Parent string `json:"parent,omitempty"`
	// Timestamp is populated for --timeline rendering by joining git_commits /
	// agent_events. Empty when no temporal data is available.
	Timestamp string `json:"timestamp,omitempty"`
	// Direction records which side of a bidirectional walk a node came from:
	// "ancestor" (backward) or "descendant" (forward). The pure BFS walk leaves
	// it empty; callers that combine both directions (e.g. internal/recap) tag it
	// so consumers can render ancestry and consequences separately.
	Direction string `json:"direction,omitempty"`
}

// AllRels lists all 10 relationship types we traverse. We do NOT subset: any of
// these can carry causal meaning depending on the slice in question.
var AllRels = []string{
	string(models.RelBlocks),
	string(models.RelBlockedBy),
	string(models.RelRelatesTo),
	string(models.RelImplements),
	string(models.RelCausedBy),
	string(models.RelSpawnedFrom),
	string(models.RelImplementedIn),
	string(models.RelPartOf),
	string(models.RelContains),
	string(models.RelPlannedIn),
}

// ForwardWalk performs a BFS following from_node_id = current outward. Returns
// nodes in BFS order, each annotated with the edge type that reached it and the
// hop depth (1-indexed).
func ForwardWalk(db *sql.DB, root string, rels []string, maxDepth int) ([]Node, error) {
	return BFSWalk(db, root, rels, maxDepth, true)
}

// BackwardWalk performs a BFS following to_node_id = current inward — i.e.
// "who points at me?".
func BackwardWalk(db *sql.DB, root string, rels []string, maxDepth int) ([]Node, error) {
	return BFSWalk(db, root, rels, maxDepth, false)
}

// BFSWalk is the shared BFS engine for both directions. When forward=true it
// follows from->to edges; when false it follows to->from edges.
func BFSWalk(db *sql.DB, root string, rels []string, maxDepth int, forward bool) ([]Node, error) {
	if maxDepth <= 0 || len(rels) == 0 {
		return nil, nil
	}

	query := neighborQuery(rels, forward)

	type queueEntry struct {
		id    string
		depth int
	}
	visited := map[string]bool{root: true}
	queue := []queueEntry{{id: root, depth: 0}}
	var result []Node

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= maxDepth {
			continue
		}
		nodes, err := queryNeighbors(db, query, cur.id, rels)
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			if visited[n.id] {
				continue
			}
			visited[n.id] = true
			result = append(result, Node{
				ID:       n.id,
				Type:     n.ntype,
				EdgeType: n.rel,
				Depth:    cur.depth + 1,
				Parent:   cur.id,
			})
			queue = append(queue, queueEntry{id: n.id, depth: cur.depth + 1})
		}
	}

	resolveTitles(db, result)
	return result, nil
}

// neighborRow is a raw graph_edges neighbour before BFS bookkeeping.
type neighborRow struct {
	id    string
	ntype string
	rel   string
}

// queryNeighbors runs the directional neighbour query for one node, with a
// SQLITE_BUSY retry around the query only.
//
// bug-7dbaf552: the retry unit is THIS QUERY ONLY, never the BFS iteration. On
// a contended DELETE-journal DB a single neighbour query can transiently
// SQLITE_BUSY; without a retry the whole walk fails. db.Query returns err
// WITHOUT a usable rows handle on BUSY, so there is nothing to leak between
// attempts; rows is closed exactly once after iteration. We defensively Close
// any non-nil rows from a BUSY attempt before retrying to make the invariant
// explicit and robust to driver changes.
func queryNeighbors(db *sql.DB, query, id string, rels []string) ([]neighborRow, error) {
	args := make([]any, 0, 1+len(rels))
	args = append(args, id)
	for _, r := range rels {
		args = append(args, r)
	}
	var rows *sql.Rows
	err := dbpkg.RetryOnBusy(dbpkg.DefaultBusyBackoff, func() error {
		r, qerr := db.Query(query, args...)
		if qerr != nil {
			if r != nil { // defensive: never carry an open lock into a retry
				r.Close()
			}
			return qerr
		}
		rows = r
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("query neighbors of %s: %w", id, err)
	}
	defer rows.Close()

	var out []neighborRow
	for rows.Next() {
		var nr neighborRow
		if err := rows.Scan(&nr.id, &nr.ntype, &nr.rel); err != nil {
			return nil, fmt.Errorf("scan neighbor: %w", err)
		}
		out = append(out, nr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate neighbors of %s: %w", id, err)
	}
	return out, nil
}

// neighborQuery builds the directional neighbour SELECT with one placeholder
// per relationship type.
func neighborQuery(rels []string, forward bool) string {
	placeholders := strings.Repeat("?,", len(rels))
	placeholders = placeholders[:len(placeholders)-1]
	if forward {
		return fmt.Sprintf(
			`SELECT to_node_id, to_node_type, relationship_type
			 FROM graph_edges
			 WHERE from_node_id = ? AND relationship_type IN (%s)`,
			placeholders,
		)
	}
	return fmt.Sprintf(
		`SELECT from_node_id, from_node_type, relationship_type
		 FROM graph_edges
		 WHERE to_node_id = ? AND relationship_type IN (%s)`,
		placeholders,
	)
}

// resolveTitles fills Node.Title in one shot for display.
func resolveTitles(db *sql.DB, nodes []Node) {
	if len(nodes) == 0 {
		return
	}
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	labels := graph.ResolveToMap(db, ids)
	for i := range nodes {
		if r, ok := labels[nodes[i].ID]; ok {
			nodes[i].Title = r.Title
		}
	}
}

// AnnotateTimestamps fills in Node.Timestamp by joining git_commits
// (commit_hash) and agent_events (session_id). Best-effort: missing rows
// silently leave Timestamp empty so timeline rendering still includes them.
func AnnotateTimestamps(db *sql.DB, nodes []Node) {
	for i := range nodes {
		var ts sql.NullString
		// bug-7dbaf552: best-effort timestamp lookups still wrapped in
		// RetryOnBusy so a transient SQLITE_BUSY doesn't silently blank the
		// timeline column under contention. sql.ErrNoRows is NOT a BusyError, so
		// RetryOnBusy returns it immediately and we keep the best-effort
		// (ignore-error) behaviour.
		_ = dbpkg.RetryOnBusy(dbpkg.DefaultBusyBackoff, func() error {
			return db.QueryRow(
				`SELECT timestamp FROM git_commits WHERE commit_hash = ? LIMIT 1`,
				nodes[i].ID,
			).Scan(&ts)
		})
		if !ts.Valid || ts.String == "" {
			_ = dbpkg.RetryOnBusy(dbpkg.DefaultBusyBackoff, func() error {
				return db.QueryRow(
					`SELECT MIN(timestamp) FROM agent_events WHERE session_id = ?`,
					nodes[i].ID,
				).Scan(&ts)
			})
		}
		if ts.Valid {
			nodes[i].Timestamp = ts.String
		}
	}
}
