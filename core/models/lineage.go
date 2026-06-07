package models

import "time"

// LineageTrace records the full delegation chain for a session.
// The path field stores the ordered list of agent names from root to this node,
// enabling full ancestry queries without recursive traversal.
type LineageTrace struct {
	TraceID       string     `json:"trace_id"`
	RootSessionID string     `json:"root_session_id"`
	SessionID     string     `json:"session_id"`
	AgentName     string     `json:"agent_name"`
	Depth         int        `json:"depth"`
	Path          []string   `json:"path"` // JSON-serialized in DB
	FeatureID     string     `json:"feature_id,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	Status        string     `json:"status"` // "active" | "completed"
}
