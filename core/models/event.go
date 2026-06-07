package models

import (
	"time"
)

// AgentEvent mirrors a row in the agent_events SQLite table.
type AgentEvent struct {
	EventID         string    `json:"event_id"`
	AgentID         string    `json:"agent_id"`
	EventType       EventType `json:"event_type"`
	Timestamp       time.Time `json:"timestamp"`
	ToolName        string    `json:"tool_name,omitempty"`
	InputSummary    string    `json:"input_summary,omitempty"`
	ToolInput       string    `json:"tool_input,omitempty"`
	OutputSummary   string    `json:"output_summary,omitempty"`
	Context         string    `json:"context,omitempty"`
	SessionID       string    `json:"session_id"`
	FeatureID       string    `json:"feature_id,omitempty"`
	ParentAgentID   string    `json:"parent_agent_id,omitempty"`
	ParentEventID   string    `json:"parent_event_id,omitempty"`
	SubagentType    string    `json:"subagent_type,omitempty"`
	ChildSpikeCount int       `json:"child_spike_count"`
	CostTokens      int       `json:"cost_tokens"`
	ExecDuration    float64   `json:"execution_duration_seconds"`
	Status          string    `json:"status"`
	Model           string    `json:"model,omitempty"`
	ClaudeTaskID    string    `json:"claude_task_id,omitempty"`
	Source          string    `json:"source"`
	StepID          string    `json:"step_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
