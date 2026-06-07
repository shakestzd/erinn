package models

import (
	"encoding/json"
	"time"
)

// Session represents a Claude Code (or other AI) working session.
type Session struct {
	SessionID       string     `json:"session_id"`
	AgentAssigned   string     `json:"agent_assigned"`
	ParentSessionID string     `json:"parent_session_id,omitempty"`
	ParentEventID   string     `json:"parent_event_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`

	TotalEvents     int     `json:"total_events"`
	TotalTokensUsed int     `json:"total_tokens_used"`
	ContextDrift    float64 `json:"context_drift"`

	Status           string `json:"status"` // active, completed, paused, failed
	TranscriptID     string `json:"transcript_id,omitempty"`
	TranscriptPath   string `json:"transcript_path,omitempty"`
	TranscriptSynced string `json:"transcript_synced,omitempty"`

	StartCommit string `json:"start_commit,omitempty"`
	EndCommit   string `json:"end_commit,omitempty"`
	IsSubagent  bool   `json:"is_subagent"`

	FeaturesWorkedOn json.RawMessage `json:"features_worked_on,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`

	LastUserQueryAt string `json:"last_user_query_at,omitempty"`
	LastUserQuery   string `json:"last_user_query,omitempty"`
	HandoffNotes    string `json:"handoff_notes,omitempty"`
	RecommendedNext string `json:"recommended_next,omitempty"`

	Blockers           json.RawMessage `json:"blockers,omitempty"`
	RecommendedContext json.RawMessage `json:"recommended_context,omitempty"`
	ContinuedFrom      string          `json:"continued_from,omitempty"`

	CostBudget            *float64 `json:"cost_budget,omitempty"`
	CostThresholdBreached int      `json:"cost_threshold_breached"`
	PredictedCost         float64  `json:"predicted_cost"`
	Model                 string   `json:"model,omitempty"`
	ActiveFeatureID       string   `json:"active_feature_id,omitempty"`
	GitRemoteURL          string   `json:"git_remote_url,omitempty"`
	ProjectDir            string   `json:"project_dir,omitempty"`

	// Provenance — who/what created this session. Populated at SessionStart
	// from harness env vars and the wipnote binary version. Sessions written
	// before this feature was added leave these empty (rendered as "unknown").
	CreatedByAgent      string `json:"created_by_agent,omitempty"`
	CreatedByModel      string `json:"created_by_model,omitempty"`
	CreatedByRole       string `json:"created_by_role,omitempty"`
	CreatedByCLIVersion string `json:"created_by_cli_version,omitempty"`

	Adherence *SessionAdherence `json:"adherence,omitempty"`

	// SessionFamilyID groups N sessions that are continuations of each other
	// (resumed, parallel roots in the same logical work context). Unlike
	// ContinuedFrom (a single-parent FK), multiple sessions may share one family.
	// When empty, the dashboard falls back to treating the session as its own family.
	SessionFamilyID string `json:"session_family_id,omitempty"`
}

type SessionAdherenceStatus string

const (
	SessionAdherencePass SessionAdherenceStatus = "pass"
	SessionAdherenceFail SessionAdherenceStatus = "fail"
	SessionAdherenceWarn SessionAdherenceStatus = "warn"
	SessionAdherenceNA   SessionAdherenceStatus = "not_applicable"
)

type SessionAdherenceCheck struct {
	Key     string                 `json:"key"`
	Label   string                 `json:"label"`
	Status  SessionAdherenceStatus `json:"status"`
	Summary string                 `json:"summary,omitempty"`
	Count   int                    `json:"count,omitempty"`
	Items   []string               `json:"items,omitempty"`
}

type SessionAdherence struct {
	Score      int                     `json:"score"`
	Applicable int                     `json:"applicable"`
	Passed     int                     `json:"passed"`
	Warned     int                     `json:"warned"`
	Failed     int                     `json:"failed"`
	Checks     []SessionAdherenceCheck `json:"checks,omitempty"`
}

type SessionAdherenceTrendPoint struct {
	SessionID  string    `json:"session_id"`
	Harness    string    `json:"harness"`
	CreatedAt  time.Time `json:"created_at"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
	Score      int       `json:"score"`
	Passed     int       `json:"passed"`
	Warned     int       `json:"warned"`
	Failed     int       `json:"failed"`
}

// ActivityEntry is a lightweight view used in dashboard activity feeds.
type ActivityEntry struct {
	EventID   string    `json:"event_id"`
	SessionID string    `json:"session_id"`
	AgentID   string    `json:"agent_id"`
	EventType EventType `json:"event_type"`
	ToolName  string    `json:"tool_name"`
	Summary   string    `json:"summary"`
	Timestamp time.Time `json:"timestamp"`
	FeatureID string    `json:"feature_id,omitempty"`
	ParentID  string    `json:"parent_event_id,omitempty"`
	Model     string    `json:"model,omitempty"`
}
