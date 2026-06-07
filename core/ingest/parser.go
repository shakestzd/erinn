// Package ingest reads Claude Code JSONL session files and extracts
// structured messages and tool calls for storage in wipnote's database.
package ingest

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/tidwall/gjson"
)

// EventID generates a deterministic event ID from a session, tool use ID,
// tool name, and index. Uses SHA-256 hash formatted as "evt-" + 8 hex chars.
// Shared between the cmd/wipnote ingest pipeline and the internal/hooks
// session HTML renderer so both agree on event_id for the same logical call.
func EventID(sessionID, toolUseID, toolName string, index int) string {
	key := sessionID + "|" + toolUseID
	if toolUseID == "" {
		key = sessionID + "|" + toolName + "|" + strconv.Itoa(index)
	}
	h := sha256.Sum256([]byte(key))
	return fmt.Sprintf("evt-%x", h[:4])
}

// ParseResult holds the structured output of parsing a JSONL session file.
//
// Title carries the resolved session title with user intent preserved:
// `custom-title` events (user-authored) always win over `ai-title` events
// (Claude Code-authored). Tracking only a single field here and resolving
// precedence during parsing prevents an `ai-title` written later in the
// same transcript from silently overwriting a `custom-title` the user
// had set earlier.
type ParseResult struct {
	SessionID string
	Messages  []models.Message
	ToolCalls []models.ToolCall
	Title     string
	Model     string // most-used model
	FileSize  int64

	// hasCustomTitle records whether any `custom-title` event was observed
	// in this transcript; used by the parser to ignore later `ai-title`
	// events so the user-authored title is never overwritten.
	hasCustomTitle bool
}

// ParseFile reads an entire Claude Code JSONL session file and returns
// structured messages and tool calls. It filters out system meta messages,
// compact summaries, and file-history snapshots.
func ParseFile(path string) (*ParseResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	info, _ := f.Stat()
	result, err := parse(f)
	if err != nil {
		return nil, err
	}
	if info != nil {
		result.FileSize = info.Size()
	}
	return result, nil
}

// ParseFileFrom reads only the bytes starting at offset, for incremental sync.
func ParseFileFrom(path string, offset int64) (*ParseResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek to %d: %w", offset, err)
	}

	info, _ := f.Stat()
	result, err := parse(f)
	if err != nil {
		return nil, err
	}
	if info != nil {
		result.FileSize = info.Size()
	}
	return result, nil
}

func parse(r io.Reader) (*ParseResult, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10 MB max line

	result := &ParseResult{}
	ordinal := 0
	modelCounts := map[string]int{}

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 {
			continue
		}

		lineType := gjson.Get(line, "type").String()
		sessionID := gjson.Get(line, "sessionId").String()
		if result.SessionID == "" && sessionID != "" {
			result.SessionID = sessionID
		}

		switch lineType {
		case "custom-title":
			if v := gjson.Get(line, "customTitle").String(); v != "" {
				result.Title = v
				result.hasCustomTitle = true
			}
			continue
		case "ai-title":
			// User-authored titles always win: skip ai-title if a
			// custom-title was seen earlier in this transcript.
			if result.hasCustomTitle {
				continue
			}
			if v := gjson.Get(line, "aiTitle").String(); v != "" {
				result.Title = v
			}
			continue
		case "file-history-snapshot", "queue-operation", "system":
			continue
		case "user":
			if gjson.Get(line, "isMeta").Bool() || gjson.Get(line, "isCompactSummary").Bool() {
				continue
			}
			if gjson.Get(line, "isSidechain").Bool() {
				continue
			}
			msg := parseUserMessage(line, ordinal)
			if msg == nil {
				continue
			}
			msg.SessionID = result.SessionID
			result.Messages = append(result.Messages, *msg)
			ordinal++

		case "assistant":
			msg, tools := parseAssistantMessage(line, ordinal)
			if msg == nil {
				continue
			}
			msg.SessionID = result.SessionID
			result.Messages = append(result.Messages, *msg)

			for i := range tools {
				tools[i].SessionID = result.SessionID
				tools[i].MessageOrdinal = ordinal
			}
			result.ToolCalls = append(result.ToolCalls, tools...)

			if msg.Model != "" {
				modelCounts[msg.Model]++
			}
			ordinal++
		}
	}

	// Determine most-used model.
	maxCount := 0
	for m, c := range modelCounts {
		if c > maxCount {
			result.Model = m
			maxCount = c
		}
	}

	return result, scanner.Err()
}

func parseUserMessage(line string, ordinal int) *models.Message {
	content := extractContent(line)
	if content == "" {
		return nil
	}
	// Filter system messages embedded as user turns.
	if IsSystemMessage(content) {
		return nil
	}

	ts := parseTimestamp(line)
	uuid := gjson.Get(line, "uuid").String()
	parentUUID := gjson.Get(line, "parentUuid").String()

	// Check if this is a tool_result (user turn carrying tool output).
	contentArr := gjson.Get(line, "message.content")
	hasToolResult := false
	if contentArr.IsArray() {
		contentArr.ForEach(func(_, v gjson.Result) bool {
			if v.Get("type").String() == "tool_result" {
				hasToolResult = true
				return false
			}
			return true
		})
	}
	// Skip pure tool_result turns — they're captured via ToolCall.ResultContentLength.
	if hasToolResult {
		return nil
	}

	return &models.Message{
		Ordinal:       ordinal,
		Role:          "user",
		Content:       content,
		Timestamp:     ts,
		ContentLength: len(content),
		UUID:          uuid,
		ParentUUID:    parentUUID,
	}
}

func parseAssistantMessage(line string, ordinal int) (*models.Message, []models.ToolCall) {
	ts := parseTimestamp(line)
	uuid := gjson.Get(line, "uuid").String()
	parentUUID := gjson.Get(line, "parentUuid").String()
	model := gjson.Get(line, "message.model").String()
	stopReason := gjson.Get(line, "message.stop_reason").String()

	// Token usage.
	usage := gjson.Get(line, "message.usage")
	inputTokens := int(usage.Get("input_tokens").Int())
	outputTokens := int(usage.Get("output_tokens").Int())
	cacheRead := int(usage.Get("cache_read_input_tokens").Int())

	// Walk content blocks.
	var textParts []string
	var tools []models.ToolCall
	hasThinking := false
	hasToolUse := false

	contentArr := gjson.Get(line, "message.content")
	if contentArr.IsArray() {
		contentArr.ForEach(func(_, block gjson.Result) bool {
			blockType := block.Get("type").String()
			switch blockType {
			case "text":
				textParts = append(textParts, block.Get("text").String())
			case "thinking":
				hasThinking = true
			case "tool_use":
				hasToolUse = true
				toolName := block.Get("name").String()
				tools = append(tools, models.ToolCall{
					ToolName:  toolName,
					Category:  models.ToolCategory(toolName),
					ToolUseID: block.Get("id").String(),
					InputJSON: block.Get("input").Raw,
				})
			}
			return true
		})
	}

	content := strings.Join(textParts, "\n")

	msg := &models.Message{
		Ordinal:         ordinal,
		Role:            "assistant",
		Content:         content,
		Timestamp:       ts,
		HasThinking:     hasThinking,
		HasToolUse:      hasToolUse,
		ContentLength:   len(content),
		Model:           model,
		InputTokens:     inputTokens,
		OutputTokens:    outputTokens,
		CacheReadTokens: cacheRead,
		StopReason:      stopReason,
		UUID:            uuid,
		ParentUUID:      parentUUID,
	}
	return msg, tools
}

func extractContent(line string) string {
	msg := gjson.Get(line, "message.content")
	if msg.Type == gjson.String {
		return msg.String()
	}
	if msg.IsArray() {
		var parts []string
		msg.ForEach(func(_, v gjson.Result) bool {
			if v.Get("type").String() == "text" {
				parts = append(parts, v.Get("text").String())
			}
			return true
		})
		return strings.Join(parts, "\n")
	}
	return ""
}

func parseTimestamp(line string) time.Time {
	ts := gjson.Get(line, "timestamp").String()
	if ts == "" {
		ts = gjson.Get(line, "snapshot.timestamp").String()
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t, _ = time.Parse("2006-01-02T15:04:05.000Z", ts)
	}
	return t
}
