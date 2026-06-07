package db

import (
	"database/sql"
	"time"
)

// AgentRootSentinel is stored as agent_id when WIPNOTE_AGENT_ID is unset,
// representing the top-level (non-subagent) session owner.
const AgentRootSentinel = "__root__"

// NormaliseAgentID returns agentID if non-empty, otherwise AgentRootSentinel.
// Use this before every call to SetActiveWorkItem / GetActiveWorkItem.
func NormaliseAgentID(agentID string) string {
	if agentID == "" {
		return AgentRootSentinel
	}
	return agentID
}

// SetActiveWorkItem claims workItemID for the (sessionID, agentID) pair.
// Uses INSERT OR REPLACE so the call is idempotent and overwrites any stale
// claim without raising a constraint error.
func SetActiveWorkItem(db *sql.DB, sessionID, agentID, workItemID string) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(
		`INSERT OR REPLACE INTO active_work_items (session_id, agent_id, work_item_id, claimed_at)
		 VALUES (?, ?, ?, ?)`,
		sessionID, agentID, workItemID,
		time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	)
	return err
}

// GetActiveWorkItem returns the work item currently claimed by (sessionID, agentID),
// or empty string if no claim exists.
func GetActiveWorkItem(db *sql.DB, sessionID, agentID string) string {
	if db == nil || sessionID == "" {
		return ""
	}
	var id sql.NullString
	db.QueryRow(
		`SELECT work_item_id FROM active_work_items WHERE session_id = ? AND agent_id = ?`,
		sessionID, agentID,
	).Scan(&id)
	return id.String
}

// ClearActiveWorkItem removes the (sessionID, agentID) claim.
// Called when a feature is completed, abandoned, or otherwise leaves in-progress.
func ClearActiveWorkItem(db *sql.DB, sessionID, agentID string) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(
		`DELETE FROM active_work_items WHERE session_id = ? AND agent_id = ?`,
		sessionID, agentID,
	)
	return err
}

// ActiveWorkItemsForSession returns all current claims in a session, keyed by
// agent_id. Used by the dashboard / statusline to show per-agent WIP.
func ActiveWorkItemsForSession(db *sql.DB, sessionID string) (map[string]string, error) {
	if db == nil || sessionID == "" {
		return nil, nil
	}
	rows, err := db.Query(
		`SELECT agent_id, work_item_id FROM active_work_items WHERE session_id = ?`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var agentID, workItemID string
		if err := rows.Scan(&agentID, &workItemID); err != nil {
			return nil, err
		}
		result[agentID] = workItemID
	}
	return result, rows.Err()
}

// GetActiveWorkItemWithFallback returns the active work item for (sessionID, agentID),
// falling back to the legacy sessions.active_feature_id column if no row exists
// in active_work_items. This provides compatibility during the transition period
// before active_feature_id is retired.
func GetActiveWorkItemWithFallback(db *sql.DB, sessionID, agentID string) string {
	if db == nil || sessionID == "" {
		return ""
	}
	// Prefer new per-agent table.
	if id := GetActiveWorkItem(db, sessionID, agentID); id != "" {
		return id
	}
	// Fallback to legacy column.
	var id sql.NullString
	db.QueryRow(
		`SELECT active_feature_id FROM sessions WHERE session_id = ?`, sessionID,
	).Scan(&id)
	return id.String
}
