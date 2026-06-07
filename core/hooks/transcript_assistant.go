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

// transcriptRecord is the minimal shape of one JSONL line in the Claude Code
// transcript file. Only the fields we need are decoded; unknown fields ignored.
type transcriptRecord struct {
	Type        string `json:"type"`
	UUID        string `json:"uuid"`
	ParentUUID  string `json:"parentUuid"`
	SessionID   string `json:"sessionId"`
	RequestID   string `json:"requestId"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		Role       string `json:"role"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

// extractAssistantText returns the concatenated text from an assistant record's
// content array. Returns empty string when there are no text blocks.
func extractAssistantText(rec *transcriptRecord) string {
	if rec == nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range rec.Message.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

// isHumanTextPrompt checks whether a transcript record represents a human text prompt.
// Returns true if the record is a user type with text content (not a tool_result).
// Uses the same logic as transcript_user_prompt.extractUserText to determine if
// content is a real human prompt vs. a tool_result.
func isHumanTextPrompt(rec *transcriptRecord) bool {
	if rec == nil || rec.Type != "user" {
		return false
	}
	// Reuse the user_prompt logic: parse message as JSON and check if it's a tool_result.
	// Build the raw message from rec.Message struct fields to match expected shape.
	msgData, err := json.Marshal(rec.Message)
	if err != nil {
		return false
	}
	// Now check using the same logic as extractUserText
	var msg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(msgData, &msg); err != nil {
		return false
	}
	if len(msg.Content) == 0 {
		return false
	}
	// Try legacy format: content is a plain string (would be a text prompt).
	var strContent string
	if err := json.Unmarshal(msg.Content, &strContent); err == nil {
		return strings.TrimSpace(strContent) != ""
	}
	// Try modern format: content is an array of typed blocks.
	var blocks []json.RawMessage
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return false
	}
	// Inspect first block — if it's a tool_result, this is not a human prompt.
	if len(blocks) > 0 {
		var first struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(blocks[0], &first) == nil && first.Type == "tool_result" {
			return false // tool results are not human prompts
		}
	}
	// Check for at least one text block.
	for _, raw := range blocks {
		var block struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			continue
		}
		if block.Type == "text" && block.Text != "" {
			return true
		}
	}
	return false
}

// resolveUserPromptAncestor walks the transcript chain from startParent upwards
// to find the first ancestor that is a human text prompt (a user record with
// non-tool-result content). Returns startParent unchanged if no such ancestor
// is found within 50 hops, or if the chain cannot be walked.
func resolveUserPromptAncestor(uuidToRecord map[string]*transcriptRecord, startParent string) string {
	if startParent == "" {
		return startParent
	}
	current := startParent
	for i := 0; i < 50; i++ {
		if current == "" {
			return startParent
		}
		rec, ok := uuidToRecord[current]
		if !ok {
			return startParent
		}
		if rec.Type == "user" && isHumanTextPrompt(rec) {
			return current
		}
		current = rec.ParentUUID
	}
	// Max hops reached; return startParent to avoid losing information.
	return startParent
}

// readTranscriptWithMap scans the transcript JSONL and returns both:
//  1. The most recent non-sidechain assistant record with text content
//  2. A map of all parsed records keyed by UUID for ancestor walking
//
// Returns (nil, nil, error) on I/O failure; (nil, map, nil) if no qualifying
// assistant record found but transcript was readable; (rec, map, nil) on success.
func readTranscriptWithMap(transcriptPath string) (*transcriptRecord, map[string]*transcriptRecord, error) {
	f, err := os.Open(transcriptPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil // missing file is not an error
		}
		return nil, nil, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	// Collect all lines in memory and build UUID map.
	var lines []string
	uuidToRecord := make(map[string]*transcriptRecord)
	scanner := bufio.NewScanner(f)
	// Increase buffer for very long lines (large prompts in transcript).
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
			// Try to parse and add to map (best-effort).
			var rec transcriptRecord
			if err := json.Unmarshal([]byte(line), &rec); err == nil && rec.UUID != "" {
				// Store a copy so callers can't modify map contents.
				recCopy := rec
				uuidToRecord[rec.UUID] = &recCopy
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("scan transcript: %w", err)
	}

	// Walk backwards to find the most recent qualifying record.
	for i := len(lines) - 1; i >= 0; i-- {
		var rec transcriptRecord
		if err := json.Unmarshal([]byte(lines[i]), &rec); err != nil {
			continue // malformed line — skip
		}
		if rec.Type != "assistant" {
			continue
		}
		if rec.IsSidechain {
			continue
		}
		if rec.Message.Role != "assistant" {
			continue
		}
		if extractAssistantText(&rec) == "" {
			continue // thinking-only or empty content
		}
		return &rec, uuidToRecord, nil
	}
	return nil, uuidToRecord, nil
}

// readLastAssistantRecord scans the transcript JSONL from the END and returns
// the most recent non-sidechain assistant record that has at least one non-empty
// text block. Returns nil when none is found (missing file, sidechain-only,
// thinking-only, or malformed JSONL). Reads line-by-line — never loads the
// whole file into memory at once, but does need to buffer all lines to scan
// in reverse order.
func readLastAssistantRecord(transcriptPath string) (*transcriptRecord, error) {
	rec, _, err := readTranscriptWithMap(transcriptPath)
	return rec, err
}

// assistantTextSignalID returns a deterministic signal_id for an assistant_text
// signal keyed on the record's UUID. Using the UUID ensures idempotency on Stop
// hook retries while remaining unique per assistant turn.
func assistantTextSignalID(uuid string) string {
	h := sha256.New()
	h.Write([]byte("assistant_text:" + uuid))
	return fmt.Sprintf("%x", h.Sum(nil))[:32]
}

func insertAssistantTextSignalFromHookPayload(
	database *sql.DB,
	projectDir string,
	sessionID string,
	event *CloudEvent,
	text string,
	source string,
) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}

	tsMicros := parseHookTimestampMicros(event.Timestamp)
	if tsMicros == 0 {
		tsMicros = time.Now().UnixMicro()
	}

	spanID := ""
	parentSpan := ""
	if event.TurnID != "" {
		parentSpan = event.TurnID
		spanID = "assistant:" + event.TurnID
	} else {
		spanID = "assistant:" + sessionID + ":" + fmt.Sprintf("%d", tsMicros) + ":" + assistantTextHash(text)
	}

	attrsMap := map[string]any{
		"text":   text,
		"source": source,
	}
	if event.Model != "" {
		attrsMap["model"] = event.Model
	}
	if event.TurnID != "" {
		attrsMap["turn_id"] = event.TurnID
	}
	if event.StopReason != "" {
		attrsMap["stop_reason"] = event.StopReason
		if event.StopReason != "end_turn" {
			attrsMap["interrupted"] = true
		}
	}

	attrsJSON, err := json.Marshal(attrsMap)
	if err != nil {
		debugLog(projectDir, "[assistant-text] marshal hook payload attrs: %v", err)
		return false
	}

	var featureID sql.NullString
	_ = database.QueryRow(
		`SELECT work_item_id FROM active_work_items WHERE session_id = ? AND agent_id = ?`,
		sessionID, "__root__",
	).Scan(&featureID)

	_, dbErr := database.Exec(`
		INSERT OR IGNORE INTO otel_signals (
			signal_id, harness, session_id,
			span_id, parent_span,
			kind, canonical, native, ts_micros,
			attrs_json, feature_id
		) VALUES (?, ?, ?, ?, ?, 'log', 'assistant_text', ?, ?, ?, ?)`,
		assistantTextSignalID(spanID),
		assistantTextHarness(event),
		sessionID,
		nullableStr(spanID), nullableStr(parentSpan),
		assistantTextNativeName(event),
		tsMicros,
		string(attrsJSON),
		featureID,
	)
	if dbErr != nil {
		debugLog(projectDir, "[assistant-text] insert hook payload signal: %v", dbErr)
		return false
	}
	return true
}

func parseHookTimestampMicros(raw string) int64 {
	if raw == "" {
		return 0
	}
	if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return ts.UnixMicro()
	}
	return 0
}

func assistantTextHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%x", sum[:])[:12]
}

func assistantTextHarness(event *CloudEvent) string {
	switch event.AgentID {
	case "codex":
		return "codex"
	case "gemini":
		return "gemini_cli"
	default:
		return "claude_code"
	}
}

func assistantTextNativeName(event *CloudEvent) string {
	switch event.AgentID {
	case "codex":
		return "codex.assistant_turn"
	case "gemini":
		return "gemini_cli.assistant_turn"
	default:
		return "claude_code.assistant_turn"
	}
}

// insertAssistantTextSignal writes an assistant_text otel_signals row derived
// from the last assistant record in the transcript file. It is called by the
// Stop hook handler. Non-fatal: errors are logged to debug.log only.
//
// Schema contract:
//
//	kind          = 'log'
//	canonical     = 'assistant_text'
//	span_id       = transcript record's UUID (assistant turn identity)
//	parent_span   = transcript record's parentUuid (links to user prompt UUID)
//	attrs_json    = {"text": "...", "stop_reason": "...", "request_id": "...", "sidechain": false}
func insertAssistantTextSignal(
	database *sql.DB,
	projectDir string,
	sessionID string,
	transcriptPath string,
) {
	if transcriptPath == "" {
		debugLog(projectDir, "[assistant-text] no transcript_path in Stop payload, skipping")
		return
	}

	rec, uuidToRecord, err := readTranscriptWithMap(transcriptPath)
	if err != nil {
		debugLog(projectDir, "[assistant-text] read transcript: %v", err)
		return
	}
	if rec == nil {
		// Transcript missing, sidechain-only, or no text turns yet.
		return
	}

	text := extractAssistantText(rec)
	if text == "" {
		return
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

	signalID := assistantTextSignalID(rec.UUID)

	attrsMap := map[string]any{
		"text":        text,
		"stop_reason": rec.Message.StopReason,
		"request_id":  rec.RequestID,
		"sidechain":   false,
	}
	if rec.Message.StopReason != "" && rec.Message.StopReason != "end_turn" {
		attrsMap["interrupted"] = true
	}
	attrsJSON, err := json.Marshal(attrsMap)
	if err != nil {
		debugLog(projectDir, "[assistant-text] marshal attrs: %v", err)
		return
	}

	// Look up active feature for attribution.
	var featureID sql.NullString
	_ = database.QueryRow(
		`SELECT work_item_id FROM active_work_items WHERE session_id = ? AND agent_id = ?`,
		sessionID, "__root__",
	).Scan(&featureID)

	// Walk the parent chain to find the ancestor human text prompt.
	// This handles the case where assistant text follows a tool call chain:
	// user_prompt → assistant_tool_use → user_tool_result → assistant_text
	// We want parent_span to be the user_prompt UUID, not user_tool_result.
	sigParentSpan := resolveUserPromptAncestor(uuidToRecord, rec.ParentUUID)

	// INSERT OR IGNORE ensures idempotency on hook retries — if the same
	// Stop hook fires twice for the same session, the second insert is a no-op.
	_, dbErr := database.Exec(`
		INSERT OR IGNORE INTO otel_signals (
			signal_id, harness, session_id,
			span_id, parent_span,
			kind, canonical, native, ts_micros,
			attrs_json, feature_id
		) VALUES (?, 'claude', ?, ?, ?, 'log', 'assistant_text', 'assistant_turn', ?, ?, ?)`,
		signalID, sessionID,
		nullableStr(rec.UUID), nullableStr(sigParentSpan),
		tsMicros,
		string(attrsJSON),
		featureID,
	)
	if dbErr != nil {
		debugLog(projectDir, "[assistant-text] insert signal: %v", dbErr)
	}
}
