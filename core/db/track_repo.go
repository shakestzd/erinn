package db

import (
	"database/sql"
	"fmt"
	"time"
)

// Track is a lightweight row struct for the tracks table.
// The full Node model lives in internal/models; this is for DB CRUD only.
type Track struct {
	ID          string
	Type        string
	Title       string
	Description string
	Priority    string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt time.Time
}

// GetFeatureIDsByTrack returns all feature IDs belonging to a track, combining
// three sources: (1) features.track_id column (direct attribution),
// (2) graph_edges rows of type 'part_of' or 'member_of' where to_node_id = trackID
// (edge-based attribution from migrate-tracks), and (3) graph_edges rows of type
// 'contains' where from_node_id = trackID (track→feature containment edges).
// Duplicates are deduplicated.
func GetFeatureIDsByTrack(db *sql.DB, trackID string) ([]string, error) {
	rows, err := db.Query(`
		SELECT id FROM features WHERE track_id = ?
		UNION
		SELECT from_node_id FROM graph_edges
		WHERE to_node_id = ?
		  AND relationship_type IN ('part_of', 'member_of')
		  AND from_node_id LIKE 'feat-%'
		UNION
		SELECT to_node_id FROM graph_edges
		WHERE from_node_id = ?
		  AND relationship_type = 'contains'
		  AND to_node_id LIKE 'feat-%'`,
		trackID, trackID, trackID,
	)
	if err != nil {
		return nil, fmt.Errorf("get feature IDs for track %s: %w", trackID, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// UpsertTrack inserts or updates a track row.
// On conflict by id, all mutable fields are updated.
// Tracks must be upserted BEFORE features to satisfy the FK constraint
// features.track_id → tracks.id.
func UpsertTrack(database *sql.DB, t *Track) error {
	typ := t.Type
	if typ == "" {
		typ = "track"
	}

	var completedAt sql.NullString
	if !t.CompletedAt.IsZero() {
		completedAt = sql.NullString{
			String: t.CompletedAt.UTC().Format(time.RFC3339),
			Valid:  true,
		}
	}

	_, err := database.Exec(`
		INSERT INTO tracks (id, type, title, description, priority, status,
			created_at, updated_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title        = excluded.title,
			description  = excluded.description,
			priority     = excluded.priority,
			status       = excluded.status,
			updated_at   = excluded.updated_at,
			completed_at = excluded.completed_at`,
		t.ID, typ, t.Title, nullStr(t.Description),
		nullStr(t.Priority), t.Status,
		t.CreatedAt.UTC().Format(time.RFC3339),
		t.UpdatedAt.UTC().Format(time.RFC3339),
		completedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert track %s: %w", t.ID, err)
	}
	return nil
}
