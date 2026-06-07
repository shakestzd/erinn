// Package ingest — Gemini CLI session JSON parser.
//
// Gemini CLI stores sessions as single JSON files under
// ~/.gemini/tmp/<project-slug>/chats/*.json. Each file contains a top-level
// object with a "messages" array. Messages use type "user" or "gemini" (not
// "assistant"), content is a plain string for gemini turns and an array of
// {text} objects for user turns, and tool calls appear in a top-level
// "toolCalls" array on each gemini message rather than as content blocks.
package ingest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/models"
)

// GeminiSessionFile is a discovered Gemini session JSON file.
type GeminiSessionFile struct {
	Path      string
	SessionID string
	Project   string
	Size      int64
}

// geminiSession is the top-level structure of a Gemini session JSON file.
type geminiSession struct {
	SessionID   string          `json:"sessionId"`
	ProjectHash string          `json:"projectHash"`
	StartTime   string          `json:"startTime"`
	LastUpdated string          `json:"lastUpdated"`
	Messages    []geminiMessage `json:"messages"`
}

// geminiMessage represents a single turn in a Gemini session.
type geminiMessage struct {
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"` // "user" or "gemini"
	Content   json.RawMessage `json:"content"`
	Thoughts  []geminiThought `json:"thoughts"`
	Tokens    *geminiTokens   `json:"tokens"`
	Model     string          `json:"model"`
	ToolCalls []geminiTool    `json:"toolCalls"`
}

type geminiThought struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
	Timestamp   string `json:"timestamp"`
}

type geminiTokens struct {
	Input    int `json:"input"`
	Output   int `json:"output"`
	Cached   int `json:"cached"`
	Thoughts int `json:"thoughts"`
	Tool     int `json:"tool"`
	Total    int `json:"total"`
}

type geminiTool struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// ParseGeminiFile reads a Gemini CLI session JSON file and returns a ParseResult
// compatible with the rest of the ingest pipeline (messages and tool calls).
func ParseGeminiFile(path string) (*ParseResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	info, _ := f.Stat()
	result, err := parseGemini(f)
	if err != nil {
		return nil, err
	}
	if info != nil {
		result.FileSize = info.Size()
	}
	return result, nil
}

func parseGemini(r io.Reader) (*ParseResult, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read gemini session: %w", err)
	}

	var session geminiSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("unmarshal gemini session: %w", err)
	}

	result := &ParseResult{
		SessionID: session.SessionID,
	}

	modelCounts := map[string]int{}
	ordinal := 0

	for _, m := range session.Messages {
		switch m.Type {
		case "user":
			msg := parseGeminiUserMessage(m, ordinal)
			if msg == nil {
				continue
			}
			msg.SessionID = result.SessionID
			result.Messages = append(result.Messages, *msg)
			ordinal++

		case "gemini":
			msg, tools := parseGeminiAssistantMessage(m, ordinal)
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

	return result, nil
}

func parseGeminiUserMessage(m geminiMessage, ordinal int) *models.Message {
	content := extractGeminiUserContent(m.Content)
	if content == "" || IsSystemMessage(content) {
		return nil
	}

	ts := parseGeminiTimestamp(m.Timestamp)
	return &models.Message{
		Ordinal:       ordinal,
		Role:          "user",
		Content:       content,
		Timestamp:     ts,
		ContentLength: len(content),
		UUID:          m.ID,
	}
}

func parseGeminiAssistantMessage(m geminiMessage, ordinal int) (*models.Message, []models.ToolCall) {
	content := extractGeminiAssistantContent(m.Content)
	ts := parseGeminiTimestamp(m.Timestamp)
	hasThinking := len(m.Thoughts) > 0
	hasToolUse := len(m.ToolCalls) > 0

	inputTokens := 0
	outputTokens := 0
	cacheReadTokens := 0
	if m.Tokens != nil {
		inputTokens = m.Tokens.Input
		outputTokens = m.Tokens.Output
		cacheReadTokens = m.Tokens.Cached
	}

	var tools []models.ToolCall
	for _, tc := range m.ToolCalls {
		argsJSON := ""
		if tc.Args != nil {
			argsJSON = string(tc.Args)
		}
		tools = append(tools, models.ToolCall{
			ToolName:  tc.Name,
			Category:  geminiToolCategory(tc.Name),
			ToolUseID: tc.ID,
			InputJSON: argsJSON,
		})
	}

	msg := &models.Message{
		Ordinal:         ordinal,
		Role:            "assistant",
		Content:         content,
		Timestamp:       ts,
		HasThinking:     hasThinking,
		HasToolUse:      hasToolUse,
		ContentLength:   len(content),
		Model:           m.Model,
		InputTokens:     inputTokens,
		OutputTokens:    outputTokens,
		CacheReadTokens: cacheReadTokens,
		UUID:            m.ID,
	}
	return msg, tools
}

// extractGeminiUserContent parses the user message content field which may be
// a plain string or an array of {text: "..."} objects.
func extractGeminiUserContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Try plain string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	// Try array of text objects.
	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}

	return ""
}

// extractGeminiAssistantContent extracts the text content from a gemini message.
// Gemini uses a plain string for content, not an array of blocks.
func extractGeminiAssistantContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

func parseGeminiTimestamp(ts string) time.Time {
	if ts == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t, _ = time.Parse("2006-01-02T15:04:05.000Z", ts)
	}
	return t
}

// geminiToolCategory maps Gemini CLI tool names to canonical wipnote categories.
// Accepts both modern Gemini names (emitted by the generator) and legacy names
// for backward compatibility with historical sessions.
func geminiToolCategory(name string) string {
	switch name {
	case "read_file":
		return "Read"
	case "write_file", "replace_file":
		return "Write"
	case "edit_file", "replace_string_in_file", "replace":
		return "Edit"
	case "run_shell_command", "run_in_shell":
		return "Bash"
	case "search_file_content", "grep", "grep_search":
		return "Grep"
	case "list_directory", "glob":
		return "Glob"
	case "web_fetch", "web_search", "google_web_search":
		return "Other"
	default:
		return "Other"
	}
}
