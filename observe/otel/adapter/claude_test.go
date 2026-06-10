package adapter_test

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/observe/otel"
	"github.com/shakestzd/wipnote/observe/otel/adapter"
)

func claudeRes(version string) adapter.OTLPResource {
	return adapter.OTLPResource{Attrs: map[string]any{
		"service.name":    "claude-code",
		"service.version": version,
	}}
}

func TestClaudeAdapter_Identify(t *testing.T) {
	a := adapter.NewClaudeAdapter()
	if !a.Identify(claudeRes("2.1.42")) {
		t.Error("did not identify claude-code resource")
	}
	if a.Identify(adapter.OTLPResource{Attrs: map[string]any{"service.name": "codex"}}) {
		t.Error("incorrectly identified codex resource as claude")
	}
}

// TestClaudeAdapter_APIRequestLog reproduces one of the empirical
// api_request log events: input=10, output=577, cost_usd=0.00804885,
// model=claude-haiku-4-5-20251001.
func TestClaudeAdapter_APIRequestLog(t *testing.T) {
	a := adapter.NewClaudeAdapter()
	res := claudeRes("2.1.42")
	scope := adapter.OTLPScope{Name: "com.anthropic.claude_code"}
	ts := time.Unix(0, 1735000000000000000)

	log := adapter.OTLPLog{
		Name:      "api_request",
		Timestamp: ts,
		Attrs: map[string]any{
			"event.name":            "api_request",
			"session.id":            "6bfe7f17-971d-4c30-99f2-1c8b91c87f2b",
			"prompt.id":             "c1be9d1e-10c3-4662-99cf-3d0760787b4c",
			"model":                 "claude-haiku-4-5-20251001",
			"input_tokens":          int64(10),
			"output_tokens":         int64(577),
			"cache_read_tokens":     int64(23276),
			"cache_creation_tokens": int64(2261),
			"cost_usd":              "0.00804885",
			"duration_ms":           int64(5835),
		},
	}
	sigs := a.ConvertLog(res, scope, log)
	if len(sigs) != 1 {
		t.Fatalf("got %d signals, want 1", len(sigs))
	}
	s := sigs[0]
	if s.Harness != otel.HarnessClaude {
		t.Errorf("Harness = %q", s.Harness)
	}
	if s.CanonicalName != otel.CanonicalAPIRequest {
		t.Errorf("CanonicalName = %q", s.CanonicalName)
	}
	if s.NativeName != "api_request" {
		t.Errorf("NativeName = %q", s.NativeName)
	}
	if s.SessionID != "6bfe7f17-971d-4c30-99f2-1c8b91c87f2b" {
		t.Errorf("SessionID = %q", s.SessionID)
	}
	if s.PromptID != "c1be9d1e-10c3-4662-99cf-3d0760787b4c" {
		t.Errorf("PromptID = %q", s.PromptID)
	}
	if s.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Model = %q", s.Model)
	}
	if s.Tokens.Input != 10 || s.Tokens.Output != 577 ||
		s.Tokens.CacheRead != 23276 || s.Tokens.CacheCreation != 2261 {
		t.Errorf("Tokens = %+v", s.Tokens)
	}
	if s.CostUSD != 0.00804885 {
		t.Errorf("CostUSD = %v, want 0.00804885", s.CostUSD)
	}
	if s.CostSource != otel.CostSourceVendor {
		t.Errorf("CostSource = %q, want vendor", s.CostSource)
	}
	if s.DurationMs != 5835 {
		t.Errorf("DurationMs = %d", s.DurationMs)
	}
}

func TestClaudeAdapter_ToolDecision(t *testing.T) {
	a := adapter.NewClaudeAdapter()
	res := claudeRes("2.1.42")
	log := adapter.OTLPLog{
		Name:      "tool_decision",
		Timestamp: time.Now(),
		Attrs: map[string]any{
			"event.name": "tool_decision",
			"session.id": "s1",
			"prompt.id":  "p1",
			"tool_name":  "Bash",
			"decision":   "accept",
			"source":     "config",
		},
	}
	sigs := a.ConvertLog(res, adapter.OTLPScope{}, log)
	if len(sigs) != 1 || sigs[0].CanonicalName != otel.CanonicalToolDecision {
		t.Fatalf("expected tool_decision canonical; got %+v", sigs)
	}
	s := sigs[0]
	if s.ToolName != "Bash" || s.Decision != "accept" {
		t.Errorf("tool attrs = %+v", s)
	}
	if s.DecisionSource != otel.DecisionSourceConfig {
		t.Errorf("DecisionSource = %q, want config", s.DecisionSource)
	}
}

func TestClaudeAdapter_APIError(t *testing.T) {
	a := adapter.NewClaudeAdapter()
	res := claudeRes("2.1.42")
	log := adapter.OTLPLog{
		Name:      "api_error",
		Timestamp: time.Now(),
		Attrs: map[string]any{
			"event.name":  "api_error",
			"session.id":  "s1",
			"model":       "claude-sonnet-4-6",
			"error":       "upstream timeout",
			"attempt":     int64(11), // > default max retries (10)
			"duration_ms": int64(30000),
			"status_code": "503",
		},
	}
	sigs := a.ConvertLog(res, adapter.OTLPScope{}, log)
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signal")
	}
	s := sigs[0]
	if s.CanonicalName != otel.CanonicalAPIError {
		t.Errorf("canonical = %q", s.CanonicalName)
	}
	if s.Attempt != 11 {
		t.Errorf("Attempt = %d", s.Attempt)
	}
	if s.ErrorMsg != "upstream timeout" {
		t.Errorf("ErrorMsg = %q", s.ErrorMsg)
	}
	if s.StatusCode != 503 {
		t.Errorf("StatusCode = %d", s.StatusCode)
	}
	if s.Success == nil || *s.Success {
		t.Errorf("Success = %v, want false", s.Success)
	}
}

func TestClaudeAdapter_TokenUsageFanout(t *testing.T) {
	// claude_code.token.usage is a Sum metric; each data point carries
	// a type=input|output|cacheRead|cacheCreation attr. The adapter
	// routes each into the matching Tokens dimension.
	a := adapter.NewClaudeAdapter()
	res := claudeRes("2.1.42")

	inputMetric := adapter.OTLPMetric{
		Name:      "claude_code.token.usage",
		Kind:      adapter.MetricKindCounter,
		Unit:      "tokens",
		Timestamp: time.Now(),
		Value:     1420,
		Attrs: map[string]any{
			"session.id": "s1",
			"type":       "input",
			"model":      "claude-opus-4-7",
		},
	}
	sigs := a.ConvertMetric(res, adapter.OTLPScope{}, inputMetric)
	if len(sigs) != 1 || sigs[0].Tokens.Input != 1420 || sigs[0].Tokens.Output != 0 {
		t.Errorf("input dimension not routed correctly: %+v", sigs[0].Tokens)
	}
	if sigs[0].CanonicalName != otel.CanonicalTokenUsage {
		t.Errorf("canonical = %q", sigs[0].CanonicalName)
	}

	cacheReadMetric := inputMetric
	cacheReadMetric.Value = 23276
	cacheReadMetric.Attrs = map[string]any{"session.id": "s1", "type": "cacheRead", "model": "claude-opus-4-7"}
	sigs = a.ConvertMetric(res, adapter.OTLPScope{}, cacheReadMetric)
	if sigs[0].Tokens.CacheRead != 23276 {
		t.Errorf("cacheRead dimension: %+v", sigs[0].Tokens)
	}
}

func TestClaudeAdapter_Span_Interaction(t *testing.T) {
	a := adapter.NewClaudeAdapter()
	res := claudeRes("2.1.42")
	start := time.Now()
	end := start.Add(25368 * time.Millisecond)
	span := adapter.OTLPSpan{
		Name:      "claude_code.interaction",
		TraceID:   "a4e28f48fbdb6644a92b208f2145aee1",
		SpanID:    "7d7f9ea011223344",
		StartTime: start,
		EndTime:   end,
		Attrs: map[string]any{
			"session.id":              "6bfe7f17-971d-4c30-99f2-1c8b91c87f2b",
			"interaction.sequence":    int64(1),
			"interaction.duration_ms": int64(25368),
		},
	}
	sigs := a.ConvertSpan(res, adapter.OTLPScope{}, span)
	if len(sigs) != 1 {
		t.Fatalf("want 1 signal")
	}
	s := sigs[0]
	if s.CanonicalName != otel.CanonicalInteraction {
		t.Errorf("canonical = %q", s.CanonicalName)
	}
	if s.TraceID != span.TraceID || s.SpanID != span.SpanID {
		t.Errorf("IDs not propagated: %s / %s", s.TraceID, s.SpanID)
	}
	if s.DurationMs != 25368 {
		t.Errorf("DurationMs = %d", s.DurationMs)
	}
}

func TestClaudeAdapter_Span_SubagentDetectedByToolNameAgent(t *testing.T) {
	// claude_code.tool with tool_name=Agent is a Task subagent call.
	// The adapter flips canonical name from tool_result to subagent_invocation
	// so dashboard groups them separately.
	a := adapter.NewClaudeAdapter()
	res := claudeRes("2.1.42")
	span := adapter.OTLPSpan{
		Name:         "claude_code.tool",
		TraceID:      "a4e28f48fbdb6644a92b208f2145aee1",
		SpanID:       "2339e940aabbccdd",
		ParentSpanID: "7d7f9ea011223344",
		StartTime:    time.Now(),
		EndTime:      time.Now().Add(10 * time.Second),
		Attrs: map[string]any{
			"session.id":  "s1",
			"tool_name":   "Agent",
			"duration_ms": int64(10563),
		},
	}
	sigs := a.ConvertSpan(res, adapter.OTLPScope{}, span)
	if sigs[0].CanonicalName != otel.CanonicalSubagent {
		t.Errorf("Agent span canonical = %q, want subagent_invocation", sigs[0].CanonicalName)
	}
	if sigs[0].ParentSpan != "7d7f9ea011223344" {
		t.Errorf("ParentSpan = %q", sigs[0].ParentSpan)
	}
}

func TestClaudeAdapter_Span_LLMRequestTokens(t *testing.T) {
	// claude_code.llm_request span carries the same token attrs as the
	// api_request log — confirming the adapter extracts both paths.
	a := adapter.NewClaudeAdapter()
	res := claudeRes("2.1.42")
	span := adapter.OTLPSpan{
		Name:      "claude_code.llm_request",
		TraceID:   "a4e28f48fbdb6644a92b208f2145aee1",
		SpanID:    "196c2e90aabbccdd",
		StartTime: time.Now(),
		EndTime:   time.Now().Add(5873 * time.Millisecond),
		Attrs: map[string]any{
			"session.id":            "s1",
			"model":                 "claude-haiku-4-5-20251001",
			"input_tokens":          int64(10),
			"output_tokens":         int64(577),
			"cache_read_tokens":     int64(23276),
			"cache_creation_tokens": int64(2261),
			"attempt":               int64(1),
		},
	}
	sigs := a.ConvertSpan(res, adapter.OTLPScope{}, span)
	s := sigs[0]
	if s.CanonicalName != otel.CanonicalAPIRequest {
		t.Errorf("canonical = %q", s.CanonicalName)
	}
	if s.Tokens.Input != 10 || s.Tokens.CacheCreation != 2261 {
		t.Errorf("tokens extracted wrong: %+v", s.Tokens)
	}
	if s.Attempt != 1 {
		t.Errorf("Attempt = %d", s.Attempt)
	}
}

func TestClaudeAdapter_ToolResultSuccessFlag(t *testing.T) {
	a := adapter.NewClaudeAdapter()
	res := claudeRes("2.1.42")
	log := adapter.OTLPLog{
		Name:      "tool_result",
		Timestamp: time.Now(),
		Attrs: map[string]any{
			"event.name":      "tool_result",
			"session.id":      "s1",
			"tool_name":       "Bash",
			"success":         "true",
			"duration_ms":     int64(6799),
			"decision_type":   "accept",
			"decision_source": "config",
		},
	}
	sigs := a.ConvertLog(res, adapter.OTLPScope{}, log)
	s := sigs[0]
	if s.CanonicalName != otel.CanonicalToolResult {
		t.Errorf("canonical = %q", s.CanonicalName)
	}
	if s.Success == nil || !*s.Success {
		t.Errorf("Success = %v", s.Success)
	}
	if s.Decision != "accept" || s.DecisionSource != otel.DecisionSourceConfig {
		t.Errorf("decision fields = %+v", s)
	}
}

func TestClaudeAdapter_SessionIDResourceFallback(t *testing.T) {
	// Metrics with OTEL_METRICS_INCLUDE_SESSION_ID=false may omit session.id
	// from the data point. The adapter should fall back to resource attrs.
	a := adapter.NewClaudeAdapter()
	res := adapter.OTLPResource{Attrs: map[string]any{
		"service.name": "claude-code",
		"session.id":   "from-resource",
	}}
	metric := adapter.OTLPMetric{
		Name:      "claude_code.session.count",
		Kind:      adapter.MetricKindCounter,
		Timestamp: time.Now(),
		Value:     1,
		Attrs:     map[string]any{}, // no session.id on the data point
	}
	sigs := a.ConvertMetric(res, adapter.OTLPScope{}, metric)
	if sigs[0].SessionID != "from-resource" {
		t.Errorf("SessionID = %q, want from-resource", sigs[0].SessionID)
	}
}
