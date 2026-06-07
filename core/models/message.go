package models

import "time"

// Message represents a single conversation turn from a Claude Code JSONL transcript.
type Message struct {
	ID              int       `json:"id"`
	SessionID       string    `json:"session_id"`
	AgentID         string    `json:"agent_id,omitempty"` // set for subagent transcripts
	Ordinal         int       `json:"ordinal"`
	Role            string    `json:"role"` // "user" or "assistant"
	Content         string    `json:"content"`
	Timestamp       time.Time `json:"timestamp"`
	HasThinking     bool      `json:"has_thinking"`
	HasToolUse      bool      `json:"has_tool_use"`
	ContentLength   int       `json:"content_length"`
	Model           string    `json:"model,omitempty"`
	InputTokens     int       `json:"input_tokens,omitempty"`
	OutputTokens    int       `json:"output_tokens,omitempty"`
	CacheReadTokens int       `json:"cache_read_tokens,omitempty"`
	StopReason      string    `json:"stop_reason,omitempty"`
	UUID            string    `json:"uuid,omitempty"`
	ParentUUID      string    `json:"parent_uuid,omitempty"`
}

// ToolCall represents a single tool invocation extracted from a message.
type ToolCall struct {
	ID                  int    `json:"id"`
	MessageID           int    `json:"message_id"`
	MessageOrdinal      int    `json:"message_ordinal"` // links to parent Message.Ordinal during parsing
	SessionID           string `json:"session_id"`
	ToolName            string `json:"tool_name"`
	Category            string `json:"category"` // Read, Edit, Write, Bash, Grep, Glob, Task, MCP, Other
	ToolUseID           string `json:"tool_use_id,omitempty"`
	InputJSON           string `json:"input_json,omitempty"`
	ResultContentLength int    `json:"result_content_length,omitempty"`
	SubagentSessionID   string `json:"subagent_session_id,omitempty"`
	FeatureID           string `json:"feature_id,omitempty"`
}

// ToolCategory normalises a tool name to a canonical category.
func ToolCategory(name string) string {
	switch name {
	case "Read":
		return "Read"
	case "Edit":
		return "Edit"
	case "Write", "NotebookEdit":
		return "Write"
	case "Bash":
		return "Bash"
	case "Grep":
		return "Grep"
	case "Glob":
		return "Glob"
	case "Agent", "Task", "TaskCreate", "TaskUpdate", "TaskGet", "TaskList", "TaskStop":
		return "Task"
	case "Skill", "ToolSearch":
		return "Other"
	default:
		if len(name) > 4 && name[:4] == "mcp_" {
			return "MCP"
		}
		return "Other"
	}
}
