package hooks

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakestzd/htmlgraph/internal/otel"
	"github.com/shakestzd/htmlgraph/internal/otel/sink/ndjson"
)

// ensureSessionDir creates the per-session NDJSON directory. ndjson.New's
// contract requires the directory to exist before WriteBatch — when no
// per-session collector is running (cold backfill, session-end after
// collector exit), the hook must create it.
func ensureSessionDir(projectDir, sessionID string) error {
	dir := filepath.Join(projectDir, ".htmlgraph", "sessions", sessionID)
	return os.MkdirAll(dir, 0o755)
}

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

// backfillMissedUserPrompts scans the transcript JSONL file and emits
// UnifiedSignal rows to the session's events.ndjson for user prompts
// missed by the live hook path. The per-session otel-collect indexer
// drains NDJSON into otel_signals on its next tick.
//
// Idempotent at the otel_signals layer: signal_id is derived from rec.UUID
// via userPromptSignalID, so the indexer's INSERT OR IGNORE deduplicates
// across replays. Within this single backfill pass, the same UUID would
// produce duplicate NDJSON lines if the transcript repeats them — accepted
// because the SQLite layer dedups and re-running backfill is rare.
//
// The database parameter is retained for caller-side context but is no
// longer queried; feature_id is derived by the indexer's writer at INSERT
// time from active_work_items, so attribution stays correct without the
// hook duplicating that lookup.
//
// Returns the count of NDJSON lines emitted. Errors are non-fatal by
// convention — callers should log and continue.
func backfillMissedUserPrompts(_ *sql.DB, projectDir, sessionID, transcriptPath string) (int, error) {
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

	var signals []otel.UnifiedSignal
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

		ts := parseTranscriptTimestamp(rec.Timestamp)

		attrs := map[string]any{
			"text":   text,
			"source": "transcript_backfill",
		}
		if rec.RequestID != "" {
			attrs["request_id"] = rec.RequestID
		}

		signals = append(signals, otel.UnifiedSignal{
			Harness:       otel.HarnessClaude,
			SignalID:      userPromptSignalID(rec.UUID),
			Kind:          otel.KindLog,
			CanonicalName: otel.CanonicalUserPrompt,
			NativeName:    "user_turn",
			Timestamp:     ts,
			SessionID:     sessionID,
			SpanID:        rec.UUID,
			ParentSpan:    rec.ParentUUID,
			RawAttrs:      attrs,
		})
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan transcript: %w", err)
	}

	if len(signals) == 0 {
		return 0, nil
	}

	if err := ensureSessionDir(projectDir, sessionID); err != nil {
		debugLog(projectDir, "[user-prompt-backfill] mkdir session: %v", err)
		return 0, fmt.Errorf("mkdir session: %w", err)
	}
	snk, err := ndjson.New(projectDir, sessionID)
	if err != nil {
		debugLog(projectDir, "[user-prompt-backfill] open ndjson: %v", err)
		return 0, fmt.Errorf("open ndjson: %w", err)
	}
	defer snk.Close()

	if err := snk.WriteBatch(context.Background(), otel.HarnessClaude, nil, signals); err != nil {
		debugLog(projectDir, "[user-prompt-backfill] WriteBatch: %v", err)
		return 0, fmt.Errorf("ndjson write: %w", err)
	}
	return len(signals), nil
}

// parseTranscriptTimestamp parses an RFC3339Nano timestamp string.
// Falls back to time.Now().UTC() on parse failure or empty input.
func parseTranscriptTimestamp(s string) time.Time {
	if s != "" {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t
		}
	}
	return time.Now().UTC()
}
