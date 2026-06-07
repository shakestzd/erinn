package graph

import (
	"database/sql"
	"fmt"
)

// SessionInfo holds session metadata for cross-session queries.
type SessionInfo struct {
	SessionID string
	Agent     string
	Status    string
	CreatedAt string
}

// FeatureSessionLink represents a feature-session relationship with context.
type FeatureSessionLink struct {
	FeatureID string
	Title     string
	Status    string
	Source    string // "agent_events", "feature_files", or "active_feature"
}

// SessionsForFeature returns sessions that worked on a feature, deduped
// across agent_events, feature_files, and sessions.active_feature_id.
func SessionsForFeature(db *sql.DB, featureID string) ([]SessionInfo, error) {
	rows, err := db.Query(`
		SELECT DISTINCT s.session_id, s.agent_assigned, s.status, s.created_at
		FROM sessions s
		WHERE s.session_id IN (
			SELECT session_id FROM agent_events WHERE feature_id = ?
			UNION
			SELECT session_id FROM feature_files WHERE feature_id = ?
			UNION
			SELECT session_id FROM sessions WHERE active_feature_id = ?
		)
		ORDER BY s.created_at DESC`,
		featureID, featureID, featureID)
	if err != nil {
		return nil, fmt.Errorf("sessions for feature: %w", err)
	}
	defer rows.Close()

	var results []SessionInfo
	for rows.Next() {
		var s SessionInfo
		if err := rows.Scan(&s.SessionID, &s.Agent, &s.Status, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		results = append(results, s)
	}
	return results, rows.Err()
}

// FeaturesForSession returns features that a session touched, deduped
// across agent_events, feature_files, and sessions.active_feature_id.
func FeaturesForSession(db *sql.DB, sessionID string) ([]FeatureSessionLink, error) {
	rows, err := db.Query(`
		SELECT DISTINCT f.id, f.title, f.status, src.source
		FROM features f
		JOIN (
			SELECT feature_id, 'agent_events' AS source
			FROM agent_events
			WHERE session_id = ? AND feature_id IS NOT NULL AND feature_id != ''
			UNION
			SELECT feature_id, 'feature_files' AS source
			FROM feature_files
			WHERE session_id = ? AND feature_id IS NOT NULL AND feature_id != ''
			UNION
			SELECT active_feature_id, 'active_feature' AS source
			FROM sessions
			WHERE session_id = ? AND active_feature_id IS NOT NULL AND active_feature_id != ''
		) src ON f.id = src.feature_id
		ORDER BY f.status, f.id`,
		sessionID, sessionID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("features for session: %w", err)
	}
	defer rows.Close()

	var results []FeatureSessionLink
	for rows.Next() {
		var l FeatureSessionLink
		if err := rows.Scan(&l.FeatureID, &l.Title, &l.Status, &l.Source); err != nil {
			return nil, fmt.Errorf("scan feature link: %w", err)
		}
		results = append(results, l)
	}
	return results, rows.Err()
}
