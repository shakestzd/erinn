package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// InsertEdge upserts a row into graph_edges. Uses INSERT OR REPLACE for
// idempotency. HTML is the canonical source of truth; SQLite is the queryable
// read index. Callers should treat errors as non-fatal and continue.
func InsertEdge(
	db *sql.DB,
	edgeID, fromNodeID, fromNodeType, toNodeID, toNodeType, relType string,
	metadata map[string]string,
) error {
	var metaJSON []byte
	if len(metadata) > 0 {
		var err error
		metaJSON, err = json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("marshal edge metadata: %w", err)
		}
	}

	_, err := db.Exec(`
		INSERT OR REPLACE INTO graph_edges
			(edge_id, from_node_id, from_node_type, to_node_id, to_node_type,
			 relationship_type, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		edgeID, fromNodeID, fromNodeType, toNodeID, toNodeType, relType, nullBytes(metaJSON),
	)
	if err != nil {
		return fmt.Errorf("insert edge %s: %w", edgeID, err)
	}
	return nil
}

// DeleteEdge removes the graph_edges row matching the
// (fromNodeID, toNodeID, relType) triple.
func DeleteEdge(db *sql.DB, fromNodeID, toNodeID, relType string) error {
	_, err := db.Exec(`
		DELETE FROM graph_edges
		WHERE from_node_id = ? AND to_node_id = ? AND relationship_type = ?`,
		fromNodeID, toNodeID, relType,
	)
	if err != nil {
		return fmt.Errorf("delete edge %s->%s (%s): %w", fromNodeID, toNodeID, relType, err)
	}
	return nil
}

// nullBytes returns nil when b is empty, satisfying sql.DB's NULL handling.
func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
