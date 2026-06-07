package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/shakestzd/wipnote/core/models"
)

// InsertMessage stores a transcript message. Returns the auto-generated ID.
func InsertMessage(db *sql.DB, m *models.Message) (int64, error) {
	res, err := db.Exec(`
		INSERT OR IGNORE INTO messages
			(session_id, agent_id, ordinal, role, content, timestamp,
			 has_thinking, has_tool_use, content_length,
			 model, input_tokens, output_tokens, cache_read_tokens,
			 stop_reason, uuid, parent_uuid)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.SessionID, nullStr(m.AgentID), m.Ordinal, m.Role, m.Content,
		m.Timestamp.UTC().Format(time.RFC3339Nano),
		boolToInt(m.HasThinking), boolToInt(m.HasToolUse), m.ContentLength,
		nullStr(m.Model), m.InputTokens, m.OutputTokens, m.CacheReadTokens,
		nullStr(m.StopReason), nullStr(m.UUID), nullStr(m.ParentUUID),
	)
	if err != nil {
		return 0, fmt.Errorf("insert message (session %s ord %d): %w", m.SessionID, m.Ordinal, err)
	}
	return res.LastInsertId()
}

// InsertToolCall stores a tool call extracted from a message.
func InsertToolCall(db *sql.DB, tc *models.ToolCall) error {
	_, err := db.Exec(`
		INSERT INTO tool_calls
			(message_id, session_id, tool_name, category, tool_use_id,
			 input_json, result_content_length, subagent_session_id, feature_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tc.MessageID, tc.SessionID, tc.ToolName, tc.Category,
		nullStr(tc.ToolUseID), nullStr(tc.InputJSON),
		tc.ResultContentLength, nullStr(tc.SubagentSessionID), nullStr(tc.FeatureID),
	)
	if err != nil {
		return fmt.Errorf("insert tool_call: %w", err)
	}
	return nil
}

// ListMessages returns messages for a session ordered by ordinal.
func ListMessages(db *sql.DB, sessionID string, limit int) ([]models.Message, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := db.Query(`
		SELECT id, session_id, ordinal, role, content, timestamp,
		       has_thinking, has_tool_use, content_length,
		       COALESCE(model, ''), input_tokens, output_tokens, cache_read_tokens,
		       COALESCE(stop_reason, ''), COALESCE(uuid, ''), COALESCE(parent_uuid, '')
		FROM messages
		WHERE session_id = ?
		ORDER BY ordinal DESC
		LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("list messages for %s: %w", sessionID, err)
	}
	defer rows.Close()

	var messages []models.Message
	for rows.Next() {
		var m models.Message
		var ts string
		var hasThinking, hasToolUse int
		if err := rows.Scan(
			&m.ID, &m.SessionID, &m.Ordinal, &m.Role, &m.Content, &ts,
			&hasThinking, &hasToolUse, &m.ContentLength,
			&m.Model, &m.InputTokens, &m.OutputTokens, &m.CacheReadTokens,
			&m.StopReason, &m.UUID, &m.ParentUUID,
		); err != nil {
			continue
		}
		m.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		m.HasThinking = hasThinking != 0
		m.HasToolUse = hasToolUse != 0
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// ListToolCalls returns tool calls for a session.
func ListToolCalls(db *sql.DB, sessionID string) ([]models.ToolCall, error) {
	rows, err := db.Query(`
		SELECT id, COALESCE(message_id, 0), session_id, tool_name, category,
		       COALESCE(tool_use_id, ''), COALESCE(input_json, ''),
		       result_content_length, COALESCE(subagent_session_id, ''),
		       COALESCE(feature_id, '')
		FROM tool_calls
		WHERE session_id = ?
		ORDER BY id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list tool_calls for %s: %w", sessionID, err)
	}
	defer rows.Close()

	var calls []models.ToolCall
	for rows.Next() {
		var tc models.ToolCall
		if err := rows.Scan(
			&tc.ID, &tc.MessageID, &tc.SessionID, &tc.ToolName, &tc.Category,
			&tc.ToolUseID, &tc.InputJSON,
			&tc.ResultContentLength, &tc.SubagentSessionID, &tc.FeatureID,
		); err != nil {
			continue
		}
		calls = append(calls, tc)
	}
	return calls, rows.Err()
}

// CountMessages returns the number of messages for a session.
func CountMessages(db *sql.DB, sessionID string) (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, sessionID).Scan(&count)
	return count, err
}

// UpdateTranscriptSync marks a session as transcript-synced.
func UpdateTranscriptSync(db *sql.DB, sessionID, transcriptPath string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		UPDATE sessions
		SET transcript_path = ?, transcript_synced = ?
		WHERE session_id = ?`,
		transcriptPath, now, sessionID,
	)
	return err
}

// DeleteSessionMessages removes all messages and tool_calls for a session.
func DeleteSessionMessages(db *sql.DB, sessionID string) error {
	_, err := db.Exec(`DELETE FROM messages WHERE session_id = ?`, sessionID)
	if err != nil {
		return fmt.Errorf("delete messages for %s: %w", sessionID, err)
	}
	_, err = db.Exec(`DELETE FROM tool_calls WHERE session_id = ?`, sessionID)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
