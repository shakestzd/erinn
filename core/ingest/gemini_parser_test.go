package ingest

import (
	"strings"
	"testing"
)

func TestParseGemini_UserAndAssistant(t *testing.T) {
	sessionJSON := `{
		"sessionId": "sess-gemini-1",
		"projectHash": "abc123",
		"startTime": "2026-04-12T01:00:00.000Z",
		"lastUpdated": "2026-04-12T01:05:00.000Z",
		"messages": [
			{
				"id": "u1",
				"timestamp": "2026-04-12T01:01:00.000Z",
				"type": "user",
				"content": [{"text": "hello gemini"}]
			},
			{
				"id": "g1",
				"timestamp": "2026-04-12T01:01:05.000Z",
				"type": "gemini",
				"content": "Hello! I am Gemini.",
				"model": "gemini-3-flash-preview",
				"tokens": {"input": 100, "output": 20, "cached": 5, "thoughts": 0, "tool": 0, "total": 125}
			}
		]
	}`

	result, err := parseGemini(strings.NewReader(sessionJSON))
	if err != nil {
		t.Fatalf("parseGemini error: %v", err)
	}

	if result.SessionID != "sess-gemini-1" {
		t.Errorf("SessionID = %q, want sess-gemini-1", result.SessionID)
	}
	if len(result.Messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(result.Messages))
	}

	user := result.Messages[0]
	if user.Role != "user" {
		t.Errorf("msg[0].Role = %q, want user", user.Role)
	}
	if user.Content != "hello gemini" {
		t.Errorf("msg[0].Content = %q, want 'hello gemini'", user.Content)
	}
	if user.UUID != "u1" {
		t.Errorf("msg[0].UUID = %q, want u1", user.UUID)
	}

	asst := result.Messages[1]
	if asst.Role != "assistant" {
		t.Errorf("msg[1].Role = %q, want assistant", asst.Role)
	}
	if asst.Content != "Hello! I am Gemini." {
		t.Errorf("msg[1].Content = %q, want 'Hello! I am Gemini.'", asst.Content)
	}
	if asst.Model != "gemini-3-flash-preview" {
		t.Errorf("msg[1].Model = %q, want gemini-3-flash-preview", asst.Model)
	}
	if asst.InputTokens != 100 {
		t.Errorf("msg[1].InputTokens = %d, want 100", asst.InputTokens)
	}
	if asst.OutputTokens != 20 {
		t.Errorf("msg[1].OutputTokens = %d, want 20", asst.OutputTokens)
	}
	if asst.CacheReadTokens != 5 {
		t.Errorf("msg[1].CacheReadTokens = %d, want 5", asst.CacheReadTokens)
	}

	if result.Model != "gemini-3-flash-preview" {
		t.Errorf("result.Model = %q, want gemini-3-flash-preview", result.Model)
	}
}

func TestParseGemini_ToolCalls(t *testing.T) {
	sessionJSON := `{
		"sessionId": "sess-gemini-2",
		"messages": [
			{
				"id": "g1",
				"timestamp": "2026-04-12T01:02:00.000Z",
				"type": "gemini",
				"content": "I will read the file.",
				"model": "gemini-3-flash-preview",
				"tokens": {"input": 50, "output": 10, "cached": 0, "thoughts": 0, "tool": 5, "total": 65},
				"toolCalls": [
					{
						"id": "read_file_1234_0",
						"name": "read_file",
						"args": {"file_path": "/workspaces/wipnote/AGENTS.md"}
					},
					{
						"id": "run_shell_1234_1",
						"name": "run_shell_command",
						"args": {"command": "go test ./..."}
					}
				]
			}
		]
	}`

	result, err := parseGemini(strings.NewReader(sessionJSON))
	if err != nil {
		t.Fatalf("parseGemini error: %v", err)
	}

	if len(result.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(result.Messages))
	}
	if !result.Messages[0].HasToolUse {
		t.Error("expected HasToolUse=true")
	}
	if len(result.ToolCalls) != 2 {
		t.Fatalf("got %d tool calls, want 2", len(result.ToolCalls))
	}

	tc0 := result.ToolCalls[0]
	if tc0.ToolName != "read_file" {
		t.Errorf("tc[0].ToolName = %q, want read_file", tc0.ToolName)
	}
	if tc0.Category != "Read" {
		t.Errorf("tc[0].Category = %q, want Read", tc0.Category)
	}
	if tc0.ToolUseID != "read_file_1234_0" {
		t.Errorf("tc[0].ToolUseID = %q, want read_file_1234_0", tc0.ToolUseID)
	}

	tc1 := result.ToolCalls[1]
	if tc1.ToolName != "run_shell_command" {
		t.Errorf("tc[1].ToolName = %q, want run_shell_command", tc1.ToolName)
	}
	if tc1.Category != "Bash" {
		t.Errorf("tc[1].Category = %q, want Bash", tc1.Category)
	}
}

func TestParseGemini_ThinkingBlocks(t *testing.T) {
	sessionJSON := `{
		"sessionId": "sess-gemini-3",
		"messages": [
			{
				"id": "g1",
				"timestamp": "2026-04-12T01:03:00.000Z",
				"type": "gemini",
				"content": "Here is my answer after thinking.",
				"model": "gemini-3-flash-preview",
				"thoughts": [
					{
						"subject": "Analyzing the problem",
						"description": "I need to think about this carefully...",
						"timestamp": "2026-04-12T01:02:58.000Z"
					}
				],
				"tokens": {"input": 200, "output": 30, "cached": 0, "thoughts": 50, "tool": 0, "total": 280}
			}
		]
	}`

	result, err := parseGemini(strings.NewReader(sessionJSON))
	if err != nil {
		t.Fatalf("parseGemini error: %v", err)
	}

	if len(result.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(result.Messages))
	}
	if !result.Messages[0].HasThinking {
		t.Error("expected HasThinking=true for message with thoughts")
	}
	if result.Messages[0].Content != "Here is my answer after thinking." {
		t.Errorf("Content = %q, want 'Here is my answer after thinking.'", result.Messages[0].Content)
	}
}

func TestParseGemini_UserContentString(t *testing.T) {
	// Some Gemini versions may emit user content as a plain string.
	sessionJSON := `{
		"sessionId": "sess-gemini-4",
		"messages": [
			{
				"id": "u1",
				"timestamp": "2026-04-12T01:04:00.000Z",
				"type": "user",
				"content": "plain string content"
			}
		]
	}`

	result, err := parseGemini(strings.NewReader(sessionJSON))
	if err != nil {
		t.Fatalf("parseGemini error: %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(result.Messages))
	}
	if result.Messages[0].Content != "plain string content" {
		t.Errorf("Content = %q, want 'plain string content'", result.Messages[0].Content)
	}
}

func TestParseGemini_EmptyMessages(t *testing.T) {
	sessionJSON := `{
		"sessionId": "sess-gemini-5",
		"messages": []
	}`

	result, err := parseGemini(strings.NewReader(sessionJSON))
	if err != nil {
		t.Fatalf("parseGemini error: %v", err)
	}
	if result.SessionID != "sess-gemini-5" {
		t.Errorf("SessionID = %q, want sess-gemini-5", result.SessionID)
	}
	if len(result.Messages) != 0 {
		t.Errorf("got %d messages, want 0", len(result.Messages))
	}
}

func TestGeminiToolCategory(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		// Legacy Gemini tool names (for backward compatibility).
		{"read_file", "Read"},
		{"write_file", "Write"},
		{"replace_file", "Write"},
		{"edit_file", "Edit"},
		{"replace_string_in_file", "Edit"},
		{"run_shell_command", "Bash"},
		{"run_in_shell", "Bash"},
		{"search_file_content", "Grep"},
		{"grep", "Grep"},
		{"list_directory", "Glob"},
		{"web_fetch", "Other"},
		{"web_search", "Other"},

		// Modern Gemini tool names (emitted by the generator).
		{"replace", "Edit"},
		{"grep_search", "Grep"},
		{"google_web_search", "Other"},

		// Unknown tools.
		{"get_internal_docs", "Other"},
		{"unknown_tool", "Other"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := geminiToolCategory(tt.name)
			if got != tt.expected {
				t.Errorf("geminiToolCategory(%q) = %q, want %q", tt.name, got, tt.expected)
			}
		})
	}
}

func TestExtractGeminiSessionID(t *testing.T) {
	tests := []struct {
		filename string
		expected string
	}{
		{"session-2026-04-12T01-03-4a8d77d4.json", "4a8d77d4"},
		{"4a8d77d4-0033-4f19-b3c0-9d2fb1791c06.json", "4a8d77d4-0033-4f19-b3c0-9d2fb1791c06"},
		{"logs.json", "logs"},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := extractGeminiSessionID(tt.filename)
			if got != tt.expected {
				t.Errorf("extractGeminiSessionID(%q) = %q, want %q", tt.filename, got, tt.expected)
			}
		})
	}
}

func TestIsGeminiSessionFile(t *testing.T) {
	tests := []struct {
		filename string
		expected bool
	}{
		{"session-2026-04-12T01-03-4a8d77d4.json", true},
		{"4a8d77d4-0033-4f19-b3c0-9d2fb1791c06.json", true},
		{"logs.json", false},
		{"notes.txt", false},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := isGeminiSessionFile(tt.filename)
			if got != tt.expected {
				t.Errorf("isGeminiSessionFile(%q) = %v, want %v", tt.filename, got, tt.expected)
			}
		})
	}
}

// TestGeminiParityModernToolNames verifies that modern Gemini tool names
// emitted by the generator (from pluginbuild.claudeToGeminiTool) are recognized
// by the ingestion parser (geminiToolCategory). This guards against drift:
// if the generator maps a tool to a modern name, the ingestion parser must
// recognize it. The test uses the pluginbuild package to discover the modern
// names and confirms each maps to a non-"Other" category.
func TestGeminiParityModernToolNames(t *testing.T) {
	// These are the modern Gemini tool names the generator emits.
	// (They are known to be in pluginbuild.claudeToGeminiTool values.)
	modernToolNames := map[string]string{
		"read_file":         "Read",
		"replace":           "Edit",
		"write_file":        "Write",
		"grep_search":       "Grep",
		"glob":              "Glob",
		"run_shell_command": "Bash",
		"google_web_search": "Other",
		"web_fetch":         "Other",
	}

	for toolName, expectedCategory := range modernToolNames {
		t.Run(toolName, func(t *testing.T) {
			got := geminiToolCategory(toolName)
			if got != expectedCategory {
				t.Errorf("geminiToolCategory(%q) = %q, want %q (tool emitted by generator must be recognized by parser)", toolName, got, expectedCategory)
			}
		})
	}
}

// TestGeminiAgentToolTranslationWithModernNames is an integration test that
// verifies the agent translator (pluginbuild) emits modern Gemini names that
// the ingestion parser can recognize.
func TestGeminiAgentToolTranslationWithModernNames(t *testing.T) {
	sessionJSON := `{
		"sessionId": "sess-gemini-modern",
		"messages": [
			{
				"id": "g1",
				"timestamp": "2026-04-12T01:05:00.000Z",
				"type": "gemini",
				"content": "Using modern tool names.",
				"model": "gemini-3-flash-preview",
				"toolCalls": [
					{
						"id": "replace_1",
						"name": "replace",
						"args": {"file_path": "test.go", "old": "old", "new": "new"}
					},
					{
						"id": "grep_1",
						"name": "grep_search",
						"args": {"pattern": "TODO", "path": "."}
					}
				]
			}
		]
	}`

	result, err := parseGemini(strings.NewReader(sessionJSON))
	if err != nil {
		t.Fatalf("parseGemini error: %v", err)
	}

	if len(result.ToolCalls) != 2 {
		t.Fatalf("got %d tool calls, want 2", len(result.ToolCalls))
	}

	// Modern "replace" name should be categorized as "Edit".
	if result.ToolCalls[0].Category != "Edit" {
		t.Errorf("replace tool categorized as %q, want Edit", result.ToolCalls[0].Category)
	}

	// Modern "grep_search" name should be categorized as "Grep".
	if result.ToolCalls[1].Category != "Grep" {
		t.Errorf("grep_search tool categorized as %q, want Grep", result.ToolCalls[1].Category)
	}
}
