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

// TestClaudeAdapter_APIRequestLogImpliesSuccess covers bug-79606e3d: an
// api_request log event carries no explicit success/status attribute in
// live capture, but reaching this event (rather than the separate
// api_error event) is itself the success signal.
func TestClaudeAdapter_APIRequestLogImpliesSuccess(t *testing.T) {
	a := adapter.NewClaudeAdapter()
	res := claudeRes("2.1.42")
	log := adapter.OTLPLog{
		Name:      "api_request",
		Timestamp: time.Now(),
		Attrs: map[string]any{
			"event.name": "api_request",
			"session.id": "s1",
			"model":      "claude-haiku-4-5-20251001",
		},
	}
	sigs := a.ConvertLog(res, adapter.OTLPScope{}, log)
	if sigs[0].Success == nil || !*sigs[0].Success {
		t.Errorf("Success = %v, want true", sigs[0].Success)
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

// TestClaudeAdapter_Span_ToolResultLeavesSuccessNil documents a deliberate
// bug-79606e3d decision: claude_code.tool spans carry no success/error/
// status_code attribute of any kind in live capture (the outcome is only
// reported via the separate tool_result LOG event), so the adapter leaves
// Success nil here rather than guessing. Forcing true would silently
// deflate the failure rate this data is meant to expose.
func TestClaudeAdapter_Span_ToolResultLeavesSuccessNil(t *testing.T) {
	a := adapter.NewClaudeAdapter()
	res := claudeRes("2.1.42")
	span := adapter.OTLPSpan{
		Name:      "claude_code.tool",
		SpanID:    "eeee",
		StartTime: time.Now(),
		EndTime:   time.Now().Add(time.Second),
		Attrs: map[string]any{
			"session.id": "s1",
			"tool_name":  "Bash",
		},
	}
	sigs := a.ConvertSpan(res, adapter.OTLPScope{}, span)
	if sigs[0].Success != nil {
		t.Errorf("Success = %v, want nil (claude_code.tool carries no outcome attr)", *sigs[0].Success)
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

// TestClaudeAdapter_Span_LLMRequestSuccessAttr covers bug-79606e3d:
// claude_code.llm_request spans carry an explicit "success" boolean
// attribute (empirically ~100% of live-captured spans) even though the
// OTLP span status field is left UNSET, so the adapter must read the
// attribute rather than relying on StatusCode alone.
func TestClaudeAdapter_Span_LLMRequestSuccessAttr(t *testing.T) {
	a := adapter.NewClaudeAdapter()
	res := claudeRes("2.1.42")

	okSpan := adapter.OTLPSpan{
		Name:      "claude_code.llm_request",
		SpanID:    "aaaa",
		StartTime: time.Now(),
		EndTime:   time.Now().Add(time.Second),
		// StatusCode intentionally left 0 (UNSET) — the harness never sets it.
		Attrs: map[string]any{
			"session.id": "s1",
			"model":      "claude-sonnet-5",
			"success":    true,
		},
	}
	sigs := a.ConvertSpan(res, adapter.OTLPScope{}, okSpan)
	if sigs[0].Success == nil || !*sigs[0].Success {
		t.Errorf("Success = %v, want true", sigs[0].Success)
	}

	errSpan := okSpan
	errSpan.SpanID = "bbbb"
	errSpan.Attrs = map[string]any{
		"session.id": "s1",
		"model":      "claude-sonnet-5",
		"success":    false,
		"error":      "upstream timeout",
	}
	sigs = a.ConvertSpan(res, adapter.OTLPScope{}, errSpan)
	if sigs[0].Success == nil || *sigs[0].Success {
		t.Errorf("Success = %v, want false", sigs[0].Success)
	}
	if sigs[0].ErrorMsg != "upstream timeout" {
		t.Errorf("ErrorMsg = %q, want upstream timeout", sigs[0].ErrorMsg)
	}
}

// TestClaudeAdapter_Span_ToolExecutionSuccessAttr covers bug-79606e3d for
// claude_code.tool.execution spans, which carry the same "success" boolean
// attribute pattern as claude_code.llm_request.
func TestClaudeAdapter_Span_ToolExecutionSuccessAttr(t *testing.T) {
	a := adapter.NewClaudeAdapter()
	res := claudeRes("2.1.42")

	okSpan := adapter.OTLPSpan{
		Name:      "claude_code.tool.execution",
		SpanID:    "cccc",
		StartTime: time.Now(),
		EndTime:   time.Now().Add(time.Millisecond * 50),
		Attrs: map[string]any{
			"session.id": "s1",
			"success":    true,
		},
	}
	sigs := a.ConvertSpan(res, adapter.OTLPScope{}, okSpan)
	if sigs[0].CanonicalName != otel.CanonicalToolExecution {
		t.Fatalf("canonical = %q", sigs[0].CanonicalName)
	}
	if sigs[0].Success == nil || !*sigs[0].Success {
		t.Errorf("Success = %v, want true", sigs[0].Success)
	}

	errSpan := okSpan
	errSpan.SpanID = "dddd"
	errSpan.Attrs = map[string]any{
		"session.id": "s1",
		"success":    false,
		"error":      "Shell command failed",
	}
	sigs = a.ConvertSpan(res, adapter.OTLPScope{}, errSpan)
	if sigs[0].Success == nil || *sigs[0].Success {
		t.Errorf("Success = %v, want false", sigs[0].Success)
	}
	if sigs[0].ErrorMsg != "Shell command failed" {
		t.Errorf("ErrorMsg = %q, want Shell command failed", sigs[0].ErrorMsg)
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

// TestClaudeAdapter_ToolUseIDPromoted verifies bug-a0143c2c's prerequisite:
// both claude_code.tool_result (log) and claude_code.tool.execution (span)
// promote their tool_use_id attribute to the typed ToolUseID field, since
// CrossValidateToolOutcomes joins on it. The span also accepts the
// gen_ai.tool.call.id alias when tool_use_id itself is absent.
func TestClaudeAdapter_ToolUseIDPromoted(t *testing.T) {
	a := adapter.NewClaudeAdapter()
	res := claudeRes("2.1.42")

	log := adapter.OTLPLog{
		Name:      "tool_result",
		Timestamp: time.Now(),
		Attrs: map[string]any{
			"session.id":  "s1",
			"success":     "true",
			"tool_use_id": "toolu_log1",
		},
	}
	logSigs := a.ConvertLog(res, adapter.OTLPScope{}, log)
	if logSigs[0].ToolUseID != "toolu_log1" {
		t.Errorf("log ToolUseID = %q, want toolu_log1", logSigs[0].ToolUseID)
	}

	span := adapter.OTLPSpan{
		Name:      "claude_code.tool.execution",
		SpanID:    "span1",
		StartTime: time.Now(),
		EndTime:   time.Now().Add(time.Millisecond),
		Attrs: map[string]any{
			"session.id":  "s1",
			"success":     true,
			"tool_use_id": "toolu_span1",
		},
	}
	spanSigs := a.ConvertSpan(res, adapter.OTLPScope{}, span)
	if spanSigs[0].ToolUseID != "toolu_span1" {
		t.Errorf("span ToolUseID = %q, want toolu_span1", spanSigs[0].ToolUseID)
	}

	aliasSpan := span
	aliasSpan.SpanID = "span2"
	aliasSpan.Attrs = map[string]any{
		"session.id":          "s1",
		"success":             true,
		"gen_ai.tool.call.id": "toolu_alias1",
	}
	aliasSigs := a.ConvertSpan(res, adapter.OTLPScope{}, aliasSpan)
	if aliasSigs[0].ToolUseID != "toolu_alias1" {
		t.Errorf("span ToolUseID (alias) = %q, want toolu_alias1", aliasSigs[0].ToolUseID)
	}
}

// TestClaudeAdapter_SuccessSourceTags is the load-bearing test for
// bug-a0143c2c's decision: every Success derivation site must be tagged with
// its provenance, distinguishing the two categories of beta risk
// (llm_request has no non-beta alternative; tool.execution does) from the
// two non-beta sources (structural inference, and the stable tool_result
// log). This is what "reduces exposure" without touching any existing
// Success value — a consumer can filter on the tag without knowing the
// harness's own beta boundaries.
func TestClaudeAdapter_SuccessSourceTags(t *testing.T) {
	a := adapter.NewClaudeAdapter()
	res := claudeRes("2.1.42")

	t.Run("api_request log is structural", func(t *testing.T) {
		log := adapter.OTLPLog{Name: "api_request", Timestamp: time.Now(),
			Attrs: map[string]any{"session.id": "s1", "model": "claude-sonnet-5"}}
		sigs := a.ConvertLog(res, adapter.OTLPScope{}, log)
		assertSuccessSource(t, sigs[0], adapter.SuccessSourceStructural)
	})

	t.Run("api_error log is structural", func(t *testing.T) {
		log := adapter.OTLPLog{Name: "api_error", Timestamp: time.Now(),
			Attrs: map[string]any{"session.id": "s1"}}
		sigs := a.ConvertLog(res, adapter.OTLPScope{}, log)
		assertSuccessSource(t, sigs[0], adapter.SuccessSourceStructural)
	})

	t.Run("tool_result log is stable, not beta", func(t *testing.T) {
		log := adapter.OTLPLog{Name: "tool_result", Timestamp: time.Now(),
			Attrs: map[string]any{"session.id": "s1", "success": "true"}}
		sigs := a.ConvertLog(res, adapter.OTLPScope{}, log)
		assertSuccessSource(t, sigs[0], adapter.SuccessSourceLogStable)
	})

	t.Run("llm_request span is beta with NO alternative", func(t *testing.T) {
		span := adapter.OTLPSpan{Name: "claude_code.llm_request", SpanID: "s",
			StartTime: time.Now(), EndTime: time.Now().Add(time.Second),
			Attrs: map[string]any{"session.id": "s1", "success": true}}
		sigs := a.ConvertSpan(res, adapter.OTLPScope{}, span)
		assertSuccessSource(t, sigs[0], adapter.SuccessSourceSpanBetaNoAlternative)
	})

	t.Run("tool.execution span is beta WITH an alternative", func(t *testing.T) {
		span := adapter.OTLPSpan{Name: "claude_code.tool.execution", SpanID: "s",
			StartTime: time.Now(), EndTime: time.Now().Add(time.Millisecond),
			Attrs: map[string]any{"session.id": "s1", "success": true}}
		sigs := a.ConvertSpan(res, adapter.OTLPScope{}, span)
		assertSuccessSource(t, sigs[0], adapter.SuccessSourceSpanBetaHasAlternative)
	})

	t.Run("status code fallback is tagged separately from the beta attribute", func(t *testing.T) {
		span := adapter.OTLPSpan{Name: "claude_code.llm_request", SpanID: "s",
			StartTime: time.Now(), EndTime: time.Now().Add(time.Second),
			StatusCode: 1, // OK — no "success" attribute present
			Attrs:      map[string]any{"session.id": "s1"}}
		sigs := a.ConvertSpan(res, adapter.OTLPScope{}, span)
		assertSuccessSource(t, sigs[0], adapter.SuccessSourceStatusCode)
	})
}

func assertSuccessSource(t *testing.T, sig otel.UnifiedSignal, want string) {
	t.Helper()
	got, _ := sig.RawAttrs[adapter.SuccessSourceAttrKey].(string)
	if got != want {
		t.Errorf("%s = %q, want %q", adapter.SuccessSourceAttrKey, got, want)
	}
}

// TestClaudeAdapter_CrossValidateToolOutcomes is the load-bearing test for
// the "cross-validate" half of bug-a0143c2c's decision: a matching pair, a
// diverging pair, and the two cases where the pass must decline to guess
// (only one side present; more than one candidate for the same ToolUseID).
func TestClaudeAdapter_CrossValidateToolOutcomes(t *testing.T) {
	a := adapter.NewClaudeAdapter()

	toolExecSig := func(toolUseID string, success bool) otel.UnifiedSignal {
		s := success
		return otel.UnifiedSignal{
			Kind: otel.KindSpan, CanonicalName: otel.CanonicalToolExecution,
			ToolUseID: toolUseID, Success: &s, RawAttrs: map[string]any{},
		}
	}
	toolResultSig := func(toolUseID string, success bool) otel.UnifiedSignal {
		s := success
		return otel.UnifiedSignal{
			Kind: otel.KindLog, CanonicalName: otel.CanonicalToolResult,
			ToolUseID: toolUseID, Success: &s, RawAttrs: map[string]any{},
		}
	}

	t.Run("matching pair is tagged match", func(t *testing.T) {
		signals := []otel.UnifiedSignal{
			toolExecSig("toolu_1", true),
			toolResultSig("toolu_1", true),
		}
		out := a.CrossValidateToolOutcomes(signals)
		for i, sig := range out {
			if got := sig.RawAttrs[adapter.SuccessCrossCheckAttrKey]; got != "match" {
				t.Errorf("signal[%d] %s = %v, want match", i, adapter.SuccessCrossCheckAttrKey, got)
			}
		}
	})

	t.Run("diverging pair is tagged mismatch with both raw values", func(t *testing.T) {
		signals := []otel.UnifiedSignal{
			toolExecSig("toolu_2", true),    // beta span says success
			toolResultSig("toolu_2", false), // stable log says failure
		}
		out := a.CrossValidateToolOutcomes(signals)
		for i, sig := range out {
			if got := sig.RawAttrs[adapter.SuccessCrossCheckAttrKey]; got != "mismatch" {
				t.Fatalf("signal[%d] %s = %v, want mismatch", i, adapter.SuccessCrossCheckAttrKey, got)
			}
		}
		if v, _ := out[0].RawAttrs[adapter.SuccessCrossCheckSpanBetaAttrKey].(bool); !v {
			t.Errorf("%s = %v, want true (the span's own value)", adapter.SuccessCrossCheckSpanBetaAttrKey, v)
		}
		if v, _ := out[0].RawAttrs[adapter.SuccessCrossCheckLogStableAttrKey].(bool); v {
			t.Errorf("%s = %v, want false (the log's own value)", adapter.SuccessCrossCheckLogStableAttrKey, v)
		}
	})

	t.Run("only the span present this batch: no tag, not a false match", func(t *testing.T) {
		signals := []otel.UnifiedSignal{toolExecSig("toolu_3", true)}
		out := a.CrossValidateToolOutcomes(signals)
		if _, ok := out[0].RawAttrs[adapter.SuccessCrossCheckAttrKey]; ok {
			t.Errorf("unpaired span was tagged %v, want no tag", out[0].RawAttrs[adapter.SuccessCrossCheckAttrKey])
		}
	})

	t.Run("ambiguous: two spans for the same ToolUseID are skipped, not guessed", func(t *testing.T) {
		signals := []otel.UnifiedSignal{
			toolExecSig("toolu_4", true),
			toolExecSig("toolu_4", false), // duplicate — should never happen, but must not crash or guess
			toolResultSig("toolu_4", true),
		}
		out := a.CrossValidateToolOutcomes(signals)
		for i, sig := range out {
			if _, ok := sig.RawAttrs[adapter.SuccessCrossCheckAttrKey]; ok {
				t.Errorf("signal[%d] tagged %v under ambiguous input, want no tag", i, sig.RawAttrs[adapter.SuccessCrossCheckAttrKey])
			}
		}
	})

	t.Run("unrelated ToolUseIDs are not cross-matched", func(t *testing.T) {
		signals := []otel.UnifiedSignal{
			toolExecSig("toolu_a", true),
			toolResultSig("toolu_b", false),
		}
		out := a.CrossValidateToolOutcomes(signals)
		for i, sig := range out {
			if _, ok := sig.RawAttrs[adapter.SuccessCrossCheckAttrKey]; ok {
				t.Errorf("signal[%d] tagged under unrelated ToolUseIDs, want no tag", i)
			}
		}
	})
}

// TestClaudeAdapter_ToolUseIDPromoted_AllObservedCarriers is the closing test
// for bug-5652a5ba: the tool_use_id column was writer-wired (writer.go binds
// nullStr(s.ToolUseID) into it correctly) but populated on zero of ~39,700
// live rows because no adapter conversion ever set the typed field, despite
// the attribute sitting in attrs_json on every one of the five canonical/kind
// combinations a live capture actually carries it on: tool_result (both log
// and span), tool_execution (span), tool_decision (log), and
// subagent_invocation (span — claude_code.tool with tool_name=Agent/Task).
// Each case here is a live-capture-shaped native name, so a live signal that
// carries the attribute but leaves ToolUseID empty would be a real
// regression of the exact bug this closes, not a hypothetical.
func TestClaudeAdapter_ToolUseIDPromoted_AllObservedCarriers(t *testing.T) {
	a := adapter.NewClaudeAdapter()
	res := claudeRes("2.1.42")

	t.Run("tool_result log", func(t *testing.T) {
		log := adapter.OTLPLog{Name: "claude_code.tool_result", Timestamp: time.Now(),
			Attrs: map[string]any{"session.id": "s1", "success": "true", "tool_use_id": "toolu_1"}}
		sigs := a.ConvertLog(res, adapter.OTLPScope{}, log)
		if sigs[0].ToolUseID != "toolu_1" {
			t.Errorf("ToolUseID = %q, want toolu_1", sigs[0].ToolUseID)
		}
	})

	t.Run("tool_decision log", func(t *testing.T) {
		log := adapter.OTLPLog{Name: "claude_code.tool_decision", Timestamp: time.Now(),
			Attrs: map[string]any{"session.id": "s1", "tool_name": "Edit", "decision": "accept", "tool_use_id": "toolu_2"}}
		sigs := a.ConvertLog(res, adapter.OTLPScope{}, log)
		if sigs[0].ToolUseID != "toolu_2" {
			t.Errorf("ToolUseID = %q, want toolu_2", sigs[0].ToolUseID)
		}
	})

	t.Run("tool_execution span", func(t *testing.T) {
		span := adapter.OTLPSpan{Name: "claude_code.tool.execution", SpanID: "s",
			StartTime: time.Now(), EndTime: time.Now().Add(time.Millisecond),
			Attrs: map[string]any{"session.id": "s1", "success": true, "tool_use_id": "toolu_3"}}
		sigs := a.ConvertSpan(res, adapter.OTLPScope{}, span)
		if sigs[0].ToolUseID != "toolu_3" {
			t.Errorf("ToolUseID = %q, want toolu_3", sigs[0].ToolUseID)
		}
	})

	t.Run("subagent_invocation span (claude_code.tool, tool_name=Agent)", func(t *testing.T) {
		span := adapter.OTLPSpan{Name: "claude_code.tool", SpanID: "s",
			StartTime: time.Now(), EndTime: time.Now().Add(time.Second),
			Attrs: map[string]any{"session.id": "s1", "tool_name": "Agent", "tool_use_id": "toolu_4"}}
		sigs := a.ConvertSpan(res, adapter.OTLPScope{}, span)
		if sigs[0].CanonicalName != otel.CanonicalSubagent {
			t.Fatalf("canonical = %q, want subagent_invocation", sigs[0].CanonicalName)
		}
		if sigs[0].ToolUseID != "toolu_4" {
			t.Errorf("ToolUseID = %q, want toolu_4", sigs[0].ToolUseID)
		}
	})

	t.Run("claude_code.tool span (non-Agent) also carries it", func(t *testing.T) {
		span := adapter.OTLPSpan{Name: "claude_code.tool", SpanID: "s",
			StartTime: time.Now(), EndTime: time.Now().Add(time.Second),
			Attrs: map[string]any{"session.id": "s1", "tool_name": "Bash", "tool_use_id": "toolu_5"}}
		sigs := a.ConvertSpan(res, adapter.OTLPScope{}, span)
		if sigs[0].ToolUseID != "toolu_5" {
			t.Errorf("ToolUseID = %q, want toolu_5", sigs[0].ToolUseID)
		}
	})
}
