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
// The query is sanitized to prevent FTS5 syntax errors from user input.
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

	// Extract the feature's title and tags to use as the similarity query.
	var title, tags string
	err := db.QueryRow(`SELECT title, tags FROM semantic_index WHERE feature_id = ?`,
		featureID).Scan(&title, &tags)
	if err != nil {
		return nil, fmt.Errorf("feature %s not in semantic index: %w", featureID, err)
	}

	// Combine title + tags as the similarity query.
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
// When excludeID is non-empty, that feature is excluded from results (used by SemanticRelated).
func querySemanticIndex(db *sql.DB, ftsQuery string, excludeID string, limit int) ([]SemanticResult, error) {
	var query string
	var args []any

	if excludeID == "" {
		query = `
			SELECT
				si.feature_id,
				si.title,
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
				si.feature_id,
				si.title,
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

// sanitizeFTSQuery converts user input into a safe FTS5 query.
// It splits on whitespace and joins with implicit AND,
// stripping FTS5 operators that could cause syntax errors.
func SanitizeFTSQuery(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}

	// FTS5 special characters that need to be removed from user input.
	replacer := strings.NewReplacer(
		"(", " ",
		")", " ",
		"*", " ",
		"\"", " ",
		":", " ",
		"^", " ",
		"{", " ",
		"}", " ",
		"-", " ",
	)
	cleaned := replacer.Replace(input)

	// Split into words, filter out FTS5 operators.
	ftsOps := map[string]bool{
		"AND": true, "OR": true, "NOT": true, "NEAR": true,
	}

	var terms []string
	for _, word := range strings.Fields(cleaned) {
		word = strings.TrimSpace(word)
		if word == "" || ftsOps[strings.ToUpper(word)] {
			continue
		}
		// Use prefix matching for better recall: each term matches as a prefix.
		terms = append(terms, word+"*")
	}

	if len(terms) == 0 {
		return ""
	}

	return strings.Join(terms, " ")
}
