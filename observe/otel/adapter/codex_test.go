package adapter_test

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/observe/otel"
	"github.com/shakestzd/wipnote/observe/otel/adapter"
)

func TestCodexAdapterConvertLog_UsesEventNameAttributes(t *testing.T) {
	a := adapter.NewCodexAdapter()
	res := adapter.OTLPResource{Attrs: map[string]any{
		"service.name":    "codex_cli_rs",
		"service.version": "0.128.0",
	}}
	scope := adapter.OTLPScope{Name: "codex"}
	ts := time.Unix(0, 1_777_000_000_000_000_000)

	log := adapter.OTLPLog{
		Name:      "event otel/src/events/session_telemetry.rs:999",
		Timestamp: ts,
		Attrs: map[string]any{
			"conversation.id": "codex-session",
			"event.name":      "codex.tool_result",
			"tool_name":       "mcp__playwright__browser_navigate",
			"call_id":         "call_123",
			"duration_ms":     "2408",
			"success":         "false",
			"model":           "gpt-5.5",
		},
	}

	sigs := a.ConvertLog(res, scope, log)
	if len(sigs) != 1 {
		t.Fatalf("ConvertLog returned %d signals, want 1", len(sigs))
	}
	got := sigs[0]
	if got.NativeName != "codex.tool_result" {
		t.Fatalf("NativeName = %q, want codex.tool_result", got.NativeName)
	}
	if got.CanonicalName != otel.CanonicalToolResult {
		t.Fatalf("CanonicalName = %q, want %q", got.CanonicalName, otel.CanonicalToolResult)
	}
	if got.ToolName != "mcp__playwright__browser_navigate" {
		t.Fatalf("ToolName = %q", got.ToolName)
	}
	if got.ToolUseID != "call_123" {
		t.Fatalf("ToolUseID = %q", got.ToolUseID)
	}
	if got.DurationMs != 2408 {
		t.Fatalf("DurationMs = %d, want 2408", got.DurationMs)
	}
	if got.Success == nil || *got.Success {
		t.Fatalf("Success = %v, want false", got.Success)
	}
	if got.Model != "gpt-5.5" {
		t.Fatalf("Model = %q", got.Model)
	}
}

func TestCodexAdapterConvertLog_TokenUsageFromSSEEvent(t *testing.T) {
	a := adapter.NewCodexAdapter()
	res := adapter.OTLPResource{Attrs: map[string]any{"service.name": "codex_cli_rs"}}
	scope := adapter.OTLPScope{Name: "codex"}

	log := adapter.OTLPLog{
		Name:      "event otel/src/events/session_telemetry.rs:822",
		Timestamp: time.Unix(0, 1),
		Attrs: map[string]any{
			"conversation.id":       "codex-session",
			"event.name":            "codex.sse_event",
			"event.kind":            "response.completed",
			"model":                 "gpt-5.5",
			"input_token_count":     "49621",
			"output_token_count":    "394",
			"reasoning_token_count": int64(7),
			"tool_token_count":      "50015",
		},
	}

	sig := a.ConvertLog(res, scope, log)[0]
	if sig.CanonicalName != otel.CanonicalTokenUsage {
		t.Fatalf("CanonicalName = %q, want token_usage", sig.CanonicalName)
	}
	if sig.Tokens.Input != 49621 || sig.Tokens.Output != 394 || sig.Tokens.Reasoning != 7 || sig.Tokens.Tool != 50015 {
		t.Fatalf("Tokens = %+v", sig.Tokens)
	}
}

func TestCodexAdapterConvertLog_UsesEventTimestampFallback(t *testing.T) {
	a := adapter.NewCodexAdapter()
	res := adapter.OTLPResource{Attrs: map[string]any{"service.name": "codex_cli_rs"}}
	scope := adapter.OTLPScope{Name: "codex"}
	want := time.Date(2026, 5, 8, 12, 34, 56, 789000000, time.UTC)

	log := adapter.OTLPLog{
		Name: "event otel/src/events/session_telemetry.rs:999",
		Attrs: map[string]any{
			"conversation.id": "codex-session",
			"event.name":      "user_prompt",
			"event.timestamp": want.Format(time.RFC3339Nano),
		},
	}

	sig := a.ConvertLog(res, scope, log)[0]
	if !sig.Timestamp.Equal(want) {
		t.Fatalf("Timestamp = %s, want %s", sig.Timestamp, want)
	}
	if sig.CanonicalName != otel.CanonicalUserPrompt {
		t.Fatalf("CanonicalName = %q, want %q", sig.CanonicalName, otel.CanonicalUserPrompt)
	}
}

func TestCodexAdapterConvertSpan_MCPToolsCall(t *testing.T) {
	a := adapter.NewCodexAdapter()
	res := adapter.OTLPResource{Attrs: map[string]any{"service.name": "codex_cli_rs"}}
	scope := adapter.OTLPScope{Name: "codex"}

	span := adapter.OTLPSpan{
		Name:      "mcp.tools.call",
		TraceID:   "trace",
		SpanID:    "span",
		StartTime: time.Unix(0, 1),
		EndTime:   time.Unix(0, 2_180_000_001),
		Attrs: map[string]any{
			"conversation.id":   "codex-session",
			"mcp.server.name":   "playwright",
			"tool.name":         "browser_evaluate",
			"tool.call_id":      "call_abc",
			"rpc.method":        "tools/call",
			"mcp.server.origin": "stdio",
		},
	}

	sig := a.ConvertSpan(res, scope, span)[0]
	if sig.CanonicalName != otel.CanonicalToolResult {
		t.Fatalf("CanonicalName = %q, want tool_result", sig.CanonicalName)
	}
	if sig.ToolName != "mcp__playwright__browser_evaluate" {
		t.Fatalf("ToolName = %q", sig.ToolName)
	}
	if sig.ToolUseID != "call_abc" {
		t.Fatalf("ToolUseID = %q", sig.ToolUseID)
	}
	if sig.DurationMs != 2180 {
		t.Fatalf("DurationMs = %d, want 2180", sig.DurationMs)
	}
}

func TestCodexAdapterConvertSpan_APIRequestAliases(t *testing.T) {
	a := adapter.NewCodexAdapter()
	res := adapter.OTLPResource{Attrs: map[string]any{"service.name": "codex_cli_rs"}}
	scope := adapter.OTLPScope{Name: "codex"}

	span := adapter.OTLPSpan{
		Name:      "gen_ai.client.operation",
		StartTime: time.Unix(0, 1),
		Attrs: map[string]any{
			"conversation.id":    "codex-session",
			"model":              "gpt-5.1-codex",
			"input_token_count":  "1200",
			"output_token_count": int64(345),
		},
	}

	sig := a.ConvertSpan(res, scope, span)[0]
	if sig.CanonicalName != otel.CanonicalAPIRequest {
		t.Fatalf("CanonicalName = %q, want api_request", sig.CanonicalName)
	}
	if sig.Model != "gpt-5.1-codex" {
		t.Fatalf("Model = %q", sig.Model)
	}
	if sig.Tokens.Input != 1200 || sig.Tokens.Output != 345 {
		t.Fatalf("Tokens = %+v", sig.Tokens)
	}
}
