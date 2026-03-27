package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/shakestzd/htmlgraph/internal/models"
)

// InsertSession creates a new session row.
func InsertSession(db *sql.DB, s *models.Session) error {
	_, err := db.Exec(`
		INSERT INTO sessions (session_id, agent_assigned, parent_session_id,
			parent_event_id, created_at, status, start_commit,
			is_subagent, model, active_feature_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.SessionID, s.AgentAssigned, nullStr(s.ParentSessionID),
		nullStr(s.ParentEventID), s.CreatedAt.UTC().Format(time.RFC3339),
		s.Status, nullStr(s.StartCommit),
		s.IsSubagent, nullStr(s.Model), nullStr(s.ActiveFeatureID),
	)
	if err != nil {
		return fmt.Errorf("insert session %s: %w", s.SessionID, err)
	}
	return nil
}

// GetSession retrieves a session by ID.
func GetSession(db *sql.DB, sessionID string) (*models.Session, error) {
	row := db.QueryRow(`
		SELECT session_id, agent_assigned, parent_session_id,
			parent_event_id, created_at, completed_at,
			total_events, total_tokens_used, context_drift,
			status, is_subagent, model, active_feature_id
		FROM sessions WHERE session_id = ?`, sessionID)

	s := &models.Session{}
	var parentSess, parentEvt, completedAt, model, activeFeat sql.NullString
	var createdStr string

	err := row.Scan(
		&s.SessionID, &s.AgentAssigned, &parentSess,
		&parentEvt, &createdStr, &completedAt,
		&s.TotalEvents, &s.TotalTokensUsed, &s.ContextDrift,
		&s.Status, &s.IsSubagent, &model, &activeFeat,
	)
	if err != nil {
		return nil, fmt.Errorf("get session %s: %w", sessionID, err)
	}

	s.ParentSessionID = parentSess.String
	s.ParentEventID = parentEvt.String
	s.Model = model.String
	s.ActiveFeatureID = activeFeat.String
	s.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)

	if completedAt.Valid {
		t, _ := time.Parse(time.RFC3339, completedAt.String)
		s.CompletedAt = &t
	}
	return s, nil
}

// UpdateSessionStatus sets the status and optionally the completed_at timestamp.
func UpdateSessionStatus(db *sql.DB, sessionID, status string) error {
	var completedAt *string
	if status == "completed" || status == "failed" {
		now := time.Now().UTC().Format(time.RFC3339)
		completedAt = &now
	}
	_, err := db.Exec(`
		UPDATE sessions SET status = ?, completed_at = COALESCE(?, completed_at)
		WHERE session_id = ?`,
		status, completedAt, sessionID,
	)
	return err
}

// ListSessions returns sessions ordered by created_at DESC with an optional
// active-only filter and row limit.
func ListSessions(db *sql.DB, activeOnly bool, limit int) ([]*models.Session, error) {
	if limit <= 0 {
		limit = 10
	}

	query := `
		SELECT session_id, agent_assigned, created_at, completed_at, status, model
		FROM sessions`
	if activeOnly {
		query += " WHERE status = 'active'"
	}
	query += " ORDER BY created_at DESC LIMIT ?"

	rows, err := db.Query(query, limit)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*models.Session
	for rows.Next() {
		s := &models.Session{}
		var completedAt, model sql.NullString
		var createdStr string

		if err := rows.Scan(
			&s.SessionID, &s.AgentAssigned, &createdStr,
			&completedAt, &s.Status, &model,
		); err != nil {
			return nil, fmt.Errorf("scan session row: %w", err)
		}
		s.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		s.Model = model.String
		if completedAt.Valid {
			t, _ := time.Parse(time.RFC3339, completedAt.String)
			s.CompletedAt = &t
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// MostRecentActiveSession returns the session_id of the latest active session,
// or ("", nil) if none exists.
func MostRecentActiveSession(db *sql.DB) (string, error) {
	row := db.QueryRow(`
		SELECT session_id FROM sessions
		WHERE status = 'active'
		ORDER BY created_at DESC LIMIT 1`)
	var id string
	if err := row.Scan(&id); err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("most recent active session: %w", err)
	}
	return id, nil
}

// nullStr converts an empty string to sql.NullString.
func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
