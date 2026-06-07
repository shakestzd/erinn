package hooks

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// userTranscriptRecord is the minimal shape of a user JSONL line in the Claude
// Code transcript. Content is kept as raw JSON because it can be either a plain
// string (legacy) or an array of typed blocks (modern).
type userTranscriptRecord struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	ParentUUID  string          `json:"parentUuid"`
	SessionID   string          `json:"sessionId"`
	RequestID   string          `json:"requestId"`
	Timestamp   string          `json:"timestamp"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
}

// userMessagePayload holds the decoded message fields once we've handled
// the two content shapes.
type userMessagePayload struct {
	text string
}

// extractUserText decodes the message field and returns the human text content.
// Returns empty string when:
//   - content is a tool_result (not a human prompt)
//   - content array has only image blocks
//   - content is empty or unrecognizable
func extractUserText(rawMessage json.RawMessage) string {
	if len(rawMessage) == 0 {
		return ""
	}

	// Decode into a struct that gives us raw content for flexible parsing.
	var msg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(rawMessage, &msg); err != nil {
		return ""
	}

	if len(msg.Content) == 0 {
		return ""
	}

	// Try legacy format: content is a plain string.
	var strContent string
	if err := json.Unmarshal(msg.Content, &strContent); err == nil {
		return strings.TrimSpace(strContent)
	}

	// Try modern format: content is an array of typed blocks.
	var blocks []json.RawMessage
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return ""
	}

	// Inspect first block — if it's a tool_result, skip this record entirely.
	if len(blocks) > 0 {
		var first struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(blocks[0], &first) == nil && first.Type == "tool_result" {
			return "" // tool results are not human prompts
		}
	}

	// Extract the first text block.
	for _, raw := range blocks {
		var block struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			continue
		}
		if block.Type == "text" && block.Text != "" {
			return strings.TrimSpace(block.Text)
		}
	}

	return "" // image-only or no text blocks
}

// userPromptSignalID returns a deterministic signal_id for a user_prompt signal
// keyed on the record's UUID, ensuring idempotency on repeated backfill runs.
func userPromptSignalID(uuid string) string {
	h := sha256.New()
	h.Write([]byte("user_prompt:" + uuid))
	return fmt.Sprintf("%x", h.Sum(nil))[:32]
}

// backfillMissedUserPrompts scans the transcript JSONL file and inserts
// otel_signals rows for user prompts that were missed by the live hook path.
//
// It is idempotent: records already captured (span_id = uuid exists with
// canonical='user_prompt') are skipped. Running backfill multiple times
// produces no duplicates.
//
// Returns the count of newly inserted rows. Errors are non-fatal by convention —
// callers should log and continue.
func backfillMissedUserPrompts(database *sql.DB, projectDir, sessionID, transcriptPath string) (int, error) {
	if transcriptPath == "" {
		return 0, nil
	}

	f, err := os.Open(transcriptPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // missing file is not an error
		}
		return 0, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	// Look up active feature for attribution (best-effort).
	var featureID sql.NullString
	_ = database.QueryRow(
		`SELECT work_item_id FROM active_work_items WHERE session_id = ? AND agent_id = ?`,
		sessionID, "__root__",
	).Scan(&featureID)

	inserted := 0
	scanner := bufio.NewScanner(f)
	// Increase buffer for very long lines (large prompts in transcript).
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var rec userTranscriptRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // malformed line — skip
		}
		if rec.Type != "user" {
			continue
		}
		if rec.IsSidechain {
			continue // subagent internals — not main-thread user prompts
		}
		if rec.UUID == "" {
			continue
		}

		text := extractUserText(rec.Message)
		if text == "" {
			continue // tool_result, image-only, or empty
		}

		// Parse the record timestamp; fall back to now on parse failure.
		var tsMicros int64
		if rec.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil {
				tsMicros = t.UnixMicro()
			}
		}
		if tsMicros == 0 {
			tsMicros = time.Now().UnixMicro()
		}

		signalID := userPromptSignalID(rec.UUID)

		attrsMap := map[string]any{
			"text":   text,
			"source": "transcript_backfill",
		}
		if rec.RequestID != "" {
			attrsMap["request_id"] = rec.RequestID
		}
		attrsJSON, err := json.Marshal(attrsMap)
		if err != nil {
			continue
		}

		// INSERT OR IGNORE keyed on signal_id for idempotency.
		res, dbErr := database.Exec(`
			INSERT OR IGNORE INTO otel_signals (
				signal_id, harness, session_id,
				span_id, parent_span,
				kind, canonical, native, ts_micros,
				attrs_json, feature_id
			) VALUES (?, 'claude', ?, ?, ?, 'log', 'user_prompt', 'user_turn', ?, ?, ?)`,
			signalID, sessionID,
			nullableStr(rec.UUID), nullableStr(rec.ParentUUID),
			tsMicros,
			string(attrsJSON),
			featureID,
		)
		if dbErr != nil {
			debugLog(projectDir, "[user-prompt-backfill] insert signal: %v", dbErr)
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	if err := scanner.Err(); err != nil {
		return inserted, fmt.Errorf("scan transcript: %w", err)
	}

	return inserted, nil
}
