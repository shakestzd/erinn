package indexer

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/observe/otel"
)

// TestParseLine_ValidSpan verifies all fields are mapped correctly for a span line.
func TestParseLine_ValidSpan(t *testing.T) {
	successVal := true
	line := `{"kind":"span","harness":"claude_code","ts":"2026-04-24T19:00:00.123456789Z","signal_id":"sig-abc","session_id":"ses-123","prompt_id":"p-456","canonical":"api_request","native":"claude_code.api_request","trace_id":"tr-abc","span_id":"sp-abc","parent_span":"sp-par","tool_name":"Bash","model":"claude-3-5","tokens_input":100,"tokens_output":50,"cost_usd":0.001,"duration_ms":500,"success":true,"error_msg":"","attrs":{"foo":"bar"}}`

	sig, err := parseLine([]byte(line))
	if err != nil {
		t.Fatalf("parseLine: %v", err)
	}
	if sig == nil {
		t.Fatal("parseLine returned nil for valid line")
	}

	if sig.Signal.Kind != otel.KindSpan {
		t.Errorf("Kind: got %q, want %q", sig.Signal.Kind, otel.KindSpan)
	}
	if sig.Signal.Harness != otel.HarnessClaude {
		t.Errorf("Harness: got %q, want %q", sig.Signal.Harness, otel.HarnessClaude)
	}
	want := time.Date(2026, 4, 24, 19, 0, 0, 123456789, time.UTC)
	if !sig.Signal.Timestamp.Equal(want) {
		t.Errorf("Timestamp: got %v, want %v", sig.Signal.Timestamp, want)
	}
	if sig.Signal.SignalID != "sig-abc" {
		t.Errorf("SignalID: got %q", sig.Signal.SignalID)
	}
	if sig.Signal.SessionID != "ses-123" {
		t.Errorf("SessionID: got %q", sig.Signal.SessionID)
	}
	if sig.Signal.PromptID != "p-456" {
		t.Errorf("PromptID: got %q", sig.Signal.PromptID)
	}
	if sig.Signal.CanonicalName != "api_request" {
		t.Errorf("CanonicalName: got %q", sig.Signal.CanonicalName)
	}
	if sig.Signal.NativeName != "claude_code.api_request" {
		t.Errorf("NativeName: got %q", sig.Signal.NativeName)
	}
	if sig.Signal.TraceID != "tr-abc" {
		t.Errorf("TraceID: got %q", sig.Signal.TraceID)
	}
	if sig.Signal.SpanID != "sp-abc" {
		t.Errorf("SpanID: got %q", sig.Signal.SpanID)
	}
	if sig.Signal.ParentSpan != "sp-par" {
		t.Errorf("ParentSpan: got %q", sig.Signal.ParentSpan)
	}
	if sig.Signal.ToolName != "Bash" {
		t.Errorf("ToolName: got %q", sig.Signal.ToolName)
	}
	if sig.Signal.Model != "claude-3-5" {
		t.Errorf("Model: got %q", sig.Signal.Model)
	}
	if sig.Signal.Tokens.Input != 100 {
		t.Errorf("Tokens.Input: got %d", sig.Signal.Tokens.Input)
	}
	if sig.Signal.Tokens.Output != 50 {
		t.Errorf("Tokens.Output: got %d", sig.Signal.Tokens.Output)
	}
	if sig.Signal.CostUSD != 0.001 {
		t.Errorf("CostUSD: got %f", sig.Signal.CostUSD)
	}
	if sig.Signal.DurationMs != 500 {
		t.Errorf("DurationMs: got %d", sig.Signal.DurationMs)
	}
	if sig.Signal.Success == nil || *sig.Signal.Success != successVal {
		t.Errorf("Success: got %v", sig.Signal.Success)
	}
}

// TestParseLine_CollectorStart verifies that collector_start lines are skipped.
func TestParseLine_CollectorStart(t *testing.T) {
	line := `{"kind":"collector_start","harness":"claude_code","ts":"2026-04-24T19:00:00Z","signal_id":"cs-001","session_id":"ses-123"}`
	sig, err := parseLine([]byte(line))
	if err != nil {
		t.Fatalf("parseLine returned error for collector_start: %v", err)
	}
	if sig != nil {
		t.Errorf("expected nil for collector_start, got %+v", sig)
	}
}

// TestParseLine_InvalidJSON verifies that invalid JSON returns error and nil signal.
func TestParseLine_InvalidJSON(t *testing.T) {
	line := `{not valid json`
	sig, err := parseLine([]byte(line))
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
	if sig != nil {
		t.Errorf("expected nil signal for invalid JSON, got %+v", sig)
	}
}

// TestParseLine_MetricKind verifies metric kind mapping.
func TestParseLine_MetricKind(t *testing.T) {
	line := `{"kind":"metric","harness":"codex","ts":"2026-04-24T19:00:00Z","signal_id":"m-1","session_id":"ses-1","canonical":"token_usage","native":"codex.token_usage"}`
	sig, err := parseLine([]byte(line))
	if err != nil {
		t.Fatalf("parseLine: %v", err)
	}
	if sig == nil {
		t.Fatal("parseLine returned nil for metric")
	}
	if sig.Signal.Kind != otel.KindMetric {
		t.Errorf("Kind: got %q, want %q", sig.Signal.Kind, otel.KindMetric)
	}
	if sig.Signal.Harness != otel.HarnessCodex {
		t.Errorf("Harness: got %q, want %q", sig.Signal.Harness, otel.HarnessCodex)
	}
}

// TestParseLine_LogKind verifies log kind mapping.
func TestParseLine_LogKind(t *testing.T) {
	line := `{"kind":"log","harness":"gemini_cli","ts":"2026-04-24T19:00:00Z","signal_id":"l-1","session_id":"ses-2","canonical":"session_start","native":"gemini_cli.session_start"}`
	sig, err := parseLine([]byte(line))
	if err != nil {
		t.Fatalf("parseLine: %v", err)
	}
	if sig == nil {
		t.Fatal("parseLine returned nil for log")
	}
	if sig.Signal.Kind != otel.KindLog {
		t.Errorf("Kind: got %q, want %q", sig.Signal.Kind, otel.KindLog)
	}
}

// TestParseLine_UnknownKind verifies that unknown kind values are skipped.
func TestParseLine_UnknownKind(t *testing.T) {
	line := `{"kind":"unknown_future_kind","harness":"claude_code","ts":"2026-04-24T19:00:00Z","signal_id":"u-1","session_id":"ses-1"}`
	sig, err := parseLine([]byte(line))
	if err != nil {
		t.Fatalf("parseLine returned error for unknown kind: %v", err)
	}
	if sig != nil {
		t.Errorf("expected nil for unknown kind, got %+v", sig)
	}
}

// TestParseLine_SuccessFalse verifies that success=false is parsed correctly.
func TestParseLine_SuccessFalse(t *testing.T) {
	line := `{"kind":"span","harness":"claude_code","ts":"2026-04-24T19:00:00Z","signal_id":"f-1","session_id":"ses-1","canonical":"api_error","native":"claude_code.api_error","success":false}`
	sig, err := parseLine([]byte(line))
	if err != nil {
		t.Fatalf("parseLine: %v", err)
	}
	if sig == nil {
		t.Fatal("parseLine returned nil")
	}
	if sig.Signal.Success == nil {
		t.Fatal("Success should not be nil")
	}
	if *sig.Signal.Success != false {
		t.Errorf("Success: got %v, want false", *sig.Signal.Success)
	}
}
