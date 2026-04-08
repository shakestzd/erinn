package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// SemanticResult is a ranked search hit from the semantic index.
type SemanticResult struct {
	FeatureID string  `json:"feature_id"`
	Title     string  `json:"title"`
	Type      string  `json:"type"`
	Status    string  `json:"status"`
	Priority  string  `json:"priority"`
	TrackID   string  `json:"track_id"`
	Rank      float64 `json:"rank"`
	Snippet   string  `json:"snippet"`
}

// SemanticSearch performs a BM25-ranked full-text search across all indexed features.
// Column weights (bm25 args): title=10, description=5, content=2, tags=8, track_title=3, related_context=4.
func SemanticSearch(db *sql.DB, query string, limit int) ([]SemanticResult, error) {
	if limit <= 0 {
		limit = 20
	}

	ftsQuery := SanitizeFTSQuery(query)
	if ftsQuery == "" {
		return nil, nil
	}

	return querySemanticIndex(db, ftsQuery, "", limit)
}

// SemanticRelated finds features semantically similar to a given feature.
// It extracts the feature's indexed content and uses it as a search query,
// excluding the feature itself from results.
func SemanticRelated(db *sql.DB, featureID string, limit int) ([]SemanticResult, error) {
	if limit <= 0 {
		limit = 10
	}

	var title, tags string
	err := db.QueryRow(`SELECT title, tags FROM semantic_index WHERE feature_id = ?`,
		featureID).Scan(&title, &tags)
	if err != nil {
		return nil, fmt.Errorf("feature %s not in semantic index: %w", featureID, err)
	}

	combined := title
	if tags != "" {
		combined += " " + tags
	}

	ftsQuery := SanitizeFTSQuery(combined)
	if ftsQuery == "" {
		return nil, nil
	}

	return querySemanticIndex(db, ftsQuery, featureID, limit)
}

// querySemanticIndex runs a BM25-ranked FTS5 query against semantic_index.
// When excludeID is non-empty, that feature is excluded from results.
func querySemanticIndex(db *sql.DB, ftsQuery string, excludeID string, limit int) ([]SemanticResult, error) {
	var query string
	var args []any

	if excludeID == "" {
		query = `
			SELECT
				si.feature_id, si.title,
				COALESCE(f.type, t.type, ''),
				COALESCE(f.status, t.status, ''),
				COALESCE(f.priority, t.priority, ''),
				COALESCE(f.track_id, ''),
				bm25(semantic_index, 0.0, 10.0, 5.0, 2.0, 8.0, 3.0, 4.0) AS rank,
				snippet(semantic_index, 2, '<b>', '</b>', '...', 32) AS snippet
			FROM semantic_index si
			LEFT JOIN features f ON f.id = si.feature_id
			LEFT JOIN tracks t ON t.id = si.feature_id
			WHERE semantic_index MATCH ?
			ORDER BY rank
			LIMIT ?`
		args = []any{ftsQuery, limit}
	} else {
		query = `
			SELECT
				si.feature_id, si.title,
				COALESCE(f.type, t.type, ''),
				COALESCE(f.status, t.status, ''),
				COALESCE(f.priority, t.priority, ''),
				COALESCE(f.track_id, ''),
				bm25(semantic_index, 0.0, 10.0, 5.0, 2.0, 8.0, 3.0, 4.0) AS rank,
				snippet(semantic_index, 2, '<b>', '</b>', '...', 32) AS snippet
			FROM semantic_index si
			LEFT JOIN features f ON f.id = si.feature_id
			LEFT JOIN tracks t ON t.id = si.feature_id
			WHERE semantic_index MATCH ?
			  AND si.feature_id != ?
			ORDER BY rank
			LIMIT ?`
		args = []any{ftsQuery, excludeID, limit}
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("semantic query: %w", err)
	}
	defer rows.Close()

	var results []SemanticResult
	for rows.Next() {
		var r SemanticResult
		if err := rows.Scan(&r.FeatureID, &r.Title, &r.Type, &r.Status,
			&r.Priority, &r.TrackID, &r.Rank, &r.Snippet); err != nil {
			continue
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SanitizeFTSQuery converts user input into a safe FTS5 query.
// Strips all FTS5 syntax characters, splits on whitespace, filters
// reserved operators, and appends prefix wildcards for better recall.
func SanitizeFTSQuery(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}

	// Replace all characters that have special meaning in FTS5 with spaces.
	// Hyphens: "in-progress" is parsed as column:query syntax.
	// Apostrophes: break tokenization.
	replacer := strings.NewReplacer(
		"(", " ", ")", " ", "*", " ", "\"", " ", "'", " ",
		":", " ", "^", " ", "{", " ", "}", " ", "-", " ",
		"+", " ", "~", " ", "[", " ", "]", " ", "/", " ",
		"\\", " ", "@", " ", "#", " ", "$", " ", "%", " ",
		"&", " ", "!", " ", "?", " ", ",", " ", ";", " ",
		".", " ", "<", " ", ">", " ", "|", " ", "=", " ",
	)
	cleaned := replacer.Replace(input)

	ftsOps := map[string]bool{
		"AND": true, "OR": true, "NOT": true, "NEAR": true,
	}

	var terms []string
	for _, word := range strings.Fields(cleaned) {
		if word == "" || ftsOps[strings.ToUpper(word)] {
			continue
		}
		terms = append(terms, word+"*")
	}

	if len(terms) == 0 {
		return ""
	}

	return strings.Join(terms, " ")
}
