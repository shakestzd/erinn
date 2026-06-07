package adapter

import (
	"fmt"
	"strconv"
	"time"

	"github.com/shakestzd/wipnote/core/harness"
	"github.com/shakestzd/wipnote/internal/otel"
)

// CodexAdapter converts Codex CLI OTel emissions into UnifiedSignals.
// Schema derived from https://github.com/openai/codex/pull/2103.
//
// Identification: resource service.name == "codex-cli".
//
// Session ID attribute: conversation.id (signal-level, with resource fallback).
// PromptID: gen_ai.prompt_id when present; otherwise empty. Codex does not
// emit a stable per-prompt ID on every signal, so callers needing prompt-level
// correlation must group by (SessionID, Timestamp window).
//
// Canonical attribute mapping:
//
//	service.name=codex-cli   conversation.id → SessionID
//	gen_ai.prompt_id         → PromptID (when present)
type CodexAdapter struct{}

// NewCodexAdapter returns a CodexAdapter ready for use.
func NewCodexAdapter() *CodexAdapter {
	return &CodexAdapter{}
}

func (c *CodexAdapter) Name() otel.Harness { return otel.HarnessCodex }

func (c *CodexAdapter) Identify(res OTLPResource) bool {
	cfg := harness.Get(string(otel.HarnessCodex))
	if cfg == nil {
		return false
	}
	svc := AttrString(res.Attrs, "service.name")
	for _, name := range cfg.ServiceNames {
		if name == svc {
			return true
		}
	}
	return false
}

// ConvertMetric maps Codex metric data points into canonical signals.
// Unknown metric names produce CanonicalUnknown, preserving all attrs in
// RawAttrs for drill-through.
func (c *CodexAdapter) ConvertMetric(res OTLPResource, scope OTLPScope, m OTLPMetric) []otel.UnifiedSignal {
	base := c.baseSignal(res, scope, otel.KindMetric, m.Name, m.Timestamp, m.Attrs)

	switch m.Name {
	case "codex.token.usage", "gen_ai.client.token.usage":
		base.CanonicalName = otel.CanonicalTokenUsage
		base.Model = AttrString(m.Attrs, "gen_ai.response.model")
		if base.Model == "" {
			base.Model = AttrString(m.Attrs, "model")
		}
		switch AttrString(m.Attrs, "gen_ai.token.type") {
		case "input":
			base.Tokens.Input = int64(m.Value)
		case "output":
			base.Tokens.Output = int64(m.Value)
		case "reasoning":
			base.Tokens.Reasoning = int64(m.Value)
		}
	case "codex.session.count":
		base.CanonicalName = otel.CanonicalSessionStart
	case "codex.cost.usage":
		base.CanonicalName = otel.CanonicalTokenUsage
		base.CostUSD = m.Value
		base.CostSource = otel.CostSourceVendor
		base.Model = AttrString(m.Attrs, "model")
	default:
		base.CanonicalName = otel.CanonicalUnknown
	}

	return []otel.UnifiedSignal{base}
}

// ConvertLog maps Codex log events onto canonical names. The event naming
// convention follows OpenAI's gen_ai semconv where applicable.
func (c *CodexAdapter) ConvertLog(res OTLPResource, scope OTLPScope, l OTLPLog) []otel.UnifiedSignal {
	nativeName := codexLogNativeName(l)
	base := c.baseSignal(res, scope, otel.KindLog, nativeName, codexLogTimestamp(l), l.Attrs)
	base.TraceID = l.TraceID
	base.SpanID = l.SpanID

	switch nativeName {
	case "codex.conversation_starts":
		base.CanonicalName = otel.CanonicalSessionStart
	case "codex.user_prompt", "user_prompt":
		base.CanonicalName = otel.CanonicalUserPrompt
		base.Model = AttrString(l.Attrs, "model")
	case "codex.api_request", "gen_ai.client.operation.duration",
		"codex.websocket_connect", "codex.websocket_request":
		base.CanonicalName = otel.CanonicalAPIRequest
		base.Model = firstCodexString(l.Attrs, "gen_ai.response.model", "gen_ai.request.model", "model")
		base.DurationMs = firstCodexInt64(l.Attrs, "duration_ms", "gen_ai.client.operation.duration_ms")
		base.Tokens.Input = firstCodexInt64(l.Attrs, "gen_ai.usage.input_tokens", "input_token_count", "input_tokens")
		base.Tokens.Output = firstCodexInt64(l.Attrs, "gen_ai.usage.output_tokens", "output_token_count", "output_tokens")
		base.Tokens.Reasoning = firstCodexInt64(l.Attrs, "gen_ai.usage.reasoning_tokens", "reasoning_token_count", "reasoning_tokens")
		if success := AttrString(l.Attrs, "success"); success != "" {
			v := success == "true"
			base.Success = &v
		}
	case "codex.api_error":
		base.CanonicalName = otel.CanonicalAPIError
		base.Model = AttrString(l.Attrs, "model")
		base.ErrorMsg = AttrString(l.Attrs, "error")
		base.Attempt = int(AttrInt64(l.Attrs, "attempt"))
		fval := false
		base.Success = &fval
	case "codex.tool_result", "tool_result":
		base.CanonicalName = otel.CanonicalToolResult
		base.ToolName = AttrString(l.Attrs, "tool_name")
		base.ToolUseID = AttrString(l.Attrs, "call_id")
		base.DurationMs = AttrInt64(l.Attrs, "duration_ms")
		base.Decision = AttrString(l.Attrs, "decision")
		base.DecisionSource = normalizeDecisionSource(AttrString(l.Attrs, "source"))
		succ := AttrString(l.Attrs, "success") == "true"
		base.Success = &succ
		base.Model = AttrString(l.Attrs, "model")
	case "codex.tool_decision", "tool_decision":
		base.CanonicalName = otel.CanonicalToolDecision
		base.ToolName = AttrString(l.Attrs, "tool_name")
		base.ToolUseID = AttrString(l.Attrs, "call_id")
		base.Decision = AttrString(l.Attrs, "decision")
		base.DecisionSource = normalizeDecisionSource(AttrString(l.Attrs, "source"))
		base.Model = AttrString(l.Attrs, "model")
	case "codex.sse_event":
		base.CanonicalName = otel.CanonicalTokenUsage
		base.Model = AttrString(l.Attrs, "model")
		base.Tokens.Input = AttrInt64(l.Attrs, "input_token_count")
		base.Tokens.Output = AttrInt64(l.Attrs, "output_token_count")
		base.Tokens.Reasoning = AttrInt64(l.Attrs, "reasoning_token_count")
		base.Tokens.Tool = AttrInt64(l.Attrs, "tool_token_count")
	default:
		base.CanonicalName = otel.CanonicalUnknown
	}

	return []otel.UnifiedSignal{base}
}

func codexLogTimestamp(l OTLPLog) time.Time {
	if !l.Timestamp.IsZero() {
		return l.Timestamp
	}
	if ts := AttrString(l.Attrs, "event.timestamp"); ts != "" {
		if parsed, err := parseCodexTimestamp(ts); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// ConvertSpan maps Codex span taxonomy onto canonical names.
func (c *CodexAdapter) ConvertSpan(res OTLPResource, scope OTLPScope, s OTLPSpan) []otel.UnifiedSignal {
	base := c.baseSignal(res, scope, otel.KindSpan, s.Name, s.StartTime, s.Attrs)
	base.TraceID = s.TraceID
	base.SpanID = s.SpanID
	base.ParentSpan = s.ParentSpanID
	base.DurationMs = AttrInt64(s.Attrs, "duration_ms")
	if base.DurationMs == 0 && !s.EndTime.IsZero() && !s.StartTime.IsZero() {
		base.DurationMs = s.EndTime.Sub(s.StartTime).Milliseconds()
	}
	if s.StatusCode == 1 {
		v := true
		base.Success = &v
	} else if s.StatusCode == 2 {
		v := false
		base.Success = &v
		base.ErrorMsg = s.StatusMsg
	}

	switch s.Name {
	case "codex.interaction", "ai.interaction":
		base.CanonicalName = otel.CanonicalInteraction
	case "codex.llm_request", "gen_ai.client.operation":
		base.CanonicalName = otel.CanonicalAPIRequest
		base.Model = firstCodexString(s.Attrs, "gen_ai.response.model", "gen_ai.request.model", "model")
		base.Tokens.Input = firstCodexInt64(s.Attrs, "gen_ai.usage.input_tokens", "input_token_count", "input_tokens")
		base.Tokens.Output = firstCodexInt64(s.Attrs, "gen_ai.usage.output_tokens", "output_token_count", "output_tokens")
		base.Tokens.Reasoning = firstCodexInt64(s.Attrs, "gen_ai.usage.reasoning_tokens", "reasoning_token_count", "reasoning_tokens")
	case "codex.tool", "ai.tool":
		base.CanonicalName = otel.CanonicalToolResult
		base.ToolName = AttrString(s.Attrs, "tool_name")
		if base.ToolName == "" {
			base.ToolName = AttrString(s.Attrs, "gen_ai.tool.name")
		}
	case "mcp.tools.call":
		base.CanonicalName = otel.CanonicalToolResult
		base.ToolName = codexMCPToolName(s.Attrs)
		base.ToolUseID = AttrString(s.Attrs, "tool.call_id")
	default:
		base.CanonicalName = otel.CanonicalUnknown
	}

	return []otel.UnifiedSignal{base}
}

// baseSignal populates the correlation IDs common to all Codex signals.
// SessionID is sourced from the registry-defined SessionAttr (conversation.id),
// falling back to the resource-level attribute when absent (mirrors
// the Claude adapter's session.id resource fallback).
func (c *CodexAdapter) baseSignal(
	res OTLPResource, scope OTLPScope, kind otel.Kind, name string,
	ts time.Time, attrs map[string]any,
) otel.UnifiedSignal {
	sessionAttr := "conversation.id" // safe default
	if cfg := harness.Get(string(otel.HarnessCodex)); cfg != nil {
		sessionAttr = cfg.SessionAttr
	}
	sig := otel.UnifiedSignal{
		Harness:        otel.HarnessCodex,
		HarnessVersion: AttrString(res.Attrs, "service.version"),
		Kind:           kind,
		NativeName:     name,
		Timestamp:      ts,
		SessionID:      ResolveSessionID(attrs, res.Attrs, sessionAttr),
		PromptID:       AttrString(attrs, "gen_ai.prompt_id"),
		RawAttrs:       copyAttrs(attrs),
	}
	return sig
}

func codexLogNativeName(l OTLPLog) string {
	if eventName := AttrString(l.Attrs, "event.name"); eventName != "" {
		return eventName
	}
	return l.Name
}

func codexMCPToolName(attrs map[string]any) string {
	tool := AttrString(attrs, "tool.name")
	if tool == "" {
		return ""
	}
	server := AttrString(attrs, "mcp.server.name")
	if server == "" {
		return tool
	}
	return "mcp__" + server + "__" + tool
}

func parseCodexTimestamp(ts string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999Z07:00",
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
	} {
		if parsed, err := time.Parse(layout, ts); err == nil {
			return parsed, nil
		}
	}
	if n, err := strconv.ParseInt(ts, 10, 64); err == nil {
		switch {
		case n >= 1e18:
			return time.Unix(0, n).UTC(), nil
		case n >= 1e15:
			return time.UnixMicro(n).UTC(), nil
		case n >= 1e12:
			return time.UnixMilli(n).UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable timestamp: %q", ts)
}

func firstCodexString(attrs map[string]any, keys ...string) string {
	for _, key := range keys {
		if v := AttrString(attrs, key); v != "" {
			return v
		}
	}
	return ""
}

func firstCodexInt64(attrs map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if v := AttrInt64(attrs, key); v != 0 {
			return v
		}
	}
	return 0
}
