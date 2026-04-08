package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// SemanticEntry holds the searchable content for a single feature in the FTS5 index.
type SemanticEntry struct {
	FeatureID      string
	Title          string
	Description    string
	Content        string
	Tags           string // space-separated tags/keywords
	TrackTitle     string
	RelatedContext string // titles of linked features via graph_edges
}

// CreateSemanticIndex creates the FTS5 virtual table for semantic search.
// Porter stemming enables "cache" to match "caching", "cached", etc.
// Column weights are applied at query time via bm25().
func CreateSemanticIndex(db *sql.DB) error {
	_, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS semantic_index USING fts5(
		feature_id UNINDEXED,
		title,
		description,
		content,
		tags,
		track_title,
		related_context,
		tokenize='porter unicode61'
	)`)
	if err != nil {
		return fmt.Errorf("create semantic_index: %w", err)
	}
	return nil
}

// UpsertSemanticEntry inserts or replaces a feature's searchable content.
// FTS5 tables don't support ON CONFLICT, so we delete-then-insert.
func UpsertSemanticEntry(db *sql.DB, e *SemanticEntry) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM semantic_index WHERE feature_id = ?`, e.FeatureID); err != nil {
		return fmt.Errorf("delete old semantic entry %s: %w", e.FeatureID, err)
	}

	_, err = tx.Exec(`INSERT INTO semantic_index
		(feature_id, title, description, content, tags, track_title, related_context)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.FeatureID, e.Title, e.Description, e.Content,
		e.Tags, e.TrackTitle, e.RelatedContext,
	)
	if err != nil {
		return fmt.Errorf("insert semantic entry %s: %w", e.FeatureID, err)
	}

	return tx.Commit()
}

// DeleteSemanticEntry removes a feature from the semantic index.
func DeleteSemanticEntry(db *sql.DB, featureID string) error {
	_, err := db.Exec(`DELETE FROM semantic_index WHERE feature_id = ?`, featureID)
	return err
}

// RebuildSemanticIndex drops and recreates the FTS5 table, then repopulates
// it from the features table enriched with graph_edges and tracks.
func RebuildSemanticIndex(db *sql.DB) (int, error) {
	db.Exec(`DROP TABLE IF EXISTS semantic_index`)

	if err := CreateSemanticIndex(db); err != nil {
		return 0, err
	}

	// Load all features with their descriptions.
	rows, err := db.Query(`
		SELECT f.id, f.title, COALESCE(f.description, ''),
		       COALESCE(f.tags, ''), COALESCE(f.track_id, ''),
		       COALESCE(t.title, '') AS track_title
		FROM features f
		LEFT JOIN tracks t ON t.id = f.track_id`)
	if err != nil {
		return 0, fmt.Errorf("load features for semantic index: %w", err)
	}
	defer rows.Close()

	type featureRow struct {
		id          string
		title       string
		description string
		tags        string
		trackID     string
		trackTitle  string
	}

	var features []featureRow
	for rows.Next() {
		var fr featureRow
		if err := rows.Scan(&fr.id, &fr.title, &fr.description,
			&fr.tags, &fr.trackID, &fr.trackTitle); err != nil {
			continue
		}
		features = append(features, fr)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Build related context from graph_edges: for each feature,
	// collect titles of features it's connected to.
	relatedCtx := buildRelatedContext(db)

	count := 0
	for _, fr := range features {
		tags := NormalizeJSONTags(fr.tags)
		entry := &SemanticEntry{
			FeatureID:      fr.id,
			Title:          fr.title,
			Description:    fr.description,
			Content:        "", // Content reserved for HTML body extraction in a future pass.
			Tags:           tags,
			TrackTitle:     fr.trackTitle,
			RelatedContext: relatedCtx[fr.id],
		}
		if err := UpsertSemanticEntry(db, entry); err != nil {
			continue
		}
		count++
	}

	// Also index tracks (stored in separate table).
	trackCount, err := indexTracks(db, relatedCtx)
	if err == nil {
		count += trackCount
	}

	return count, nil
}

// indexTracks adds tracks from the tracks table into the semantic index.
func indexTracks(db *sql.DB, relatedCtx map[string]string) (int, error) {
	rows, err := db.Query(`
		SELECT id, title, COALESCE(description, ''), COALESCE(metadata, '')
		FROM tracks`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id, title, description, metadata string
		if err := rows.Scan(&id, &title, &description, &metadata); err != nil {
			continue
		}
		entry := &SemanticEntry{
			FeatureID:      id,
			Title:          title,
			Description:    description,
			Content:        "", // Content reserved for HTML body extraction in a future pass.
			Tags:           NormalizeJSONTags(metadata),
			RelatedContext: relatedCtx[id],
		}
		if err := UpsertSemanticEntry(db, entry); err != nil {
			continue
		}
		count++
	}
	return count, rows.Err()
}

// buildRelatedContext collects titles of features linked via graph_edges
// for each feature, building a map of featureID -> space-separated related titles.
func buildRelatedContext(db *sql.DB) map[string]string {
	ctx := make(map[string]string)

	rows, err := db.Query(`
		SELECT ge.from_node_id,
		       GROUP_CONCAT(COALESCE(f.title, ge.to_node_id), ' | ')
		FROM graph_edges ge
		LEFT JOIN features f ON f.id = ge.to_node_id
		GROUP BY ge.from_node_id`)
	if err != nil {
		// graph_edges query failed; return empty context silently.
		// No logging convention exists in this package yet.
		return ctx
	}
	defer rows.Close()

	for rows.Next() {
		var fromID, titles string
		if rows.Scan(&fromID, &titles) == nil {
			ctx[fromID] = titles
		}
	}

	// Also add reverse direction (to_node_id -> from_node titles).
	rows2, err := db.Query(`
		SELECT ge.to_node_id,
		       GROUP_CONCAT(COALESCE(f.title, ge.from_node_id), ' | ')
		FROM graph_edges ge
		LEFT JOIN features f ON f.id = ge.from_node_id
		GROUP BY ge.to_node_id`)
	if err != nil {
		// Reverse graph_edges query failed; return partial context silently.
		return ctx
	}
	defer rows2.Close()

	for rows2.Next() {
		var toID, titles string
		if rows2.Scan(&toID, &titles) == nil {
			if existing, ok := ctx[toID]; ok {
				ctx[toID] = existing + " | " + titles
			} else {
				ctx[toID] = titles
			}
		}
	}

	return ctx
}

// normalizeJSONTags extracts tag strings from a JSON array like ["tag1","tag2"]
// and returns them space-separated for FTS5 indexing.
func NormalizeJSONTags(jsonTags string) string {
	jsonTags = strings.TrimSpace(jsonTags)
	if jsonTags == "" || jsonTags == "null" || jsonTags == "[]" {
		return ""
	}
	var tags []string
	if err := json.Unmarshal([]byte(jsonTags), &tags); err != nil {
		return jsonTags
	}
	return strings.Join(tags, " ")
}
