package main

import (
	"database/sql"
	"time"
)

// normalizeTimes returns sensible defaults for zero-value timestamps.
func normalizeTimes(createdAt, updatedAt time.Time) (time.Time, time.Time) {
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	return createdAt, updatedAt
}

// purgeStaleEntries removes features, tracks, and graph_edges whose IDs are no
// longer backed by an HTML file. Returns counts of purged features+tracks and edges.
func purgeStaleEntries(database *sql.DB, validIDs map[string]bool) (int, int) {
	staleFeatureIDs := collectStaleIDs(database, "SELECT id FROM features", validIDs)
	purged := deleteByIDs(database, "DELETE FROM features WHERE id = ?", staleFeatureIDs)

	// Purge stale tracks (HTML files deleted from .htmlgraph/tracks/).
	staleTrackIDs := collectStaleIDs(database, "SELECT id FROM tracks", validIDs)
	purged += deleteByIDs(database, "DELETE FROM tracks WHERE id = ?", staleTrackIDs)

	// Purge edges that reference deleted node IDs (either endpoint).
	staleEdgeIDs := collectStaleEdgeIDs(database, validIDs)
	edgesPurged := deleteByIDs(database, "DELETE FROM graph_edges WHERE edge_id = ?", staleEdgeIDs)

	return purged, edgesPurged
}

// collectStaleIDs queries all IDs from a single-column SELECT and returns those
// not present in validIDs.
func collectStaleIDs(database *sql.DB, query string, validIDs map[string]bool) []string {
	rows, err := database.Query(query)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var stale []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && !validIDs[id] {
			stale = append(stale, id)
		}
	}
	return stale
}

// collectStaleEdgeIDs returns edge_ids where either endpoint (from_node_id or
// to_node_id) refers to a node no longer backed by an HTML file.
func collectStaleEdgeIDs(database *sql.DB, validIDs map[string]bool) []string {
	rows, err := database.Query("SELECT edge_id, from_node_id, to_node_id FROM graph_edges")
	if err != nil {
		return nil
	}
	defer rows.Close()

	var stale []string
	for rows.Next() {
		var edgeID, fromID, toID string
		if rows.Scan(&edgeID, &fromID, &toID) == nil {
			if !validIDs[fromID] || !validIDs[toID] {
				stale = append(stale, edgeID)
			}
		}
	}
	return stale
}

// deleteByIDs executes a parameterised DELETE for each ID and returns the count
// of successful deletions.
func deleteByIDs(database *sql.DB, query string, ids []string) int {
	count := 0
	for _, id := range ids {
		if _, err := database.Exec(query, id); err == nil {
			count++
		}
	}
	return count
}

// normalizeStatus maps HTML statuses to the features table CHECK constraint values.
// features table allows: todo, in-progress, blocked, done, active, ended, stale
func normalizeStatus(status string) string {
	switch status {
	case "todo", "in-progress", "blocked", "done", "active", "ended", "stale":
		return status
	case "completed":
		return "done"
	case "in_progress":
		return "in-progress"
	case "archived", "cancelled":
		return "ended"
	case "pending", "identified":
		return "todo"
	default:
		return "todo"
	}
}

// mapNodeType converts HTML node types to the features table CHECK constraint values.
// features table allows: feature, bug, spike, chore, epic, task
func mapNodeType(nodeType string) string {
	switch nodeType {
	case "feature":
		return "feature"
	case "bug":
		return "bug"
	case "spike":
		return "spike"
	case "track":
		return "epic"
	case "chore":
		return "chore"
	case "plan", "spec":
		return "task"
	default:
		return "feature"
	}
}
