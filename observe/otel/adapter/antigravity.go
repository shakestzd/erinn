package adapter

import (
	"time"

	"github.com/shakestzd/wipnote/core/harness"
	"github.com/shakestzd/wipnote/observe/otel"
)

// AntigravityAdapter converts Antigravity CLI OTel emissions into UnifiedSignals.
// Identification: resource service.name == "antigravity-cli".
// Session ID attribute: session.id (signal-level, with resource fallback).
type AntigravityAdapter struct{}

// NewAntigravityAdapter returns a AntigravityAdapter ready for use.
func NewAntigravityAdapter() *AntigravityAdapter {
	return &AntigravityAdapter{}
}

func (a *AntigravityAdapter) Name() otel.Harness { return otel.HarnessAntigravity }

func (a *AntigravityAdapter) Identify(res OTLPResource) bool {
	cfg := harness.Get(string(otel.HarnessAntigravity))
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

// ConvertMetric maps Antigravity metric data points into canonical signals.
func (a *AntigravityAdapter) ConvertMetric(res OTLPResource, scope OTLPScope, m OTLPMetric) []otel.UnifiedSignal {
	base := a.baseSignal(res, scope, otel.KindMetric, m.Name, m.Timestamp, m.Attrs)

	switch m.Name {
	case "gemini_cli.token.usage", "gen_ai.client.token.usage":
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
		case "thought":
			base.Tokens.Thought = int64(m.Value)
		case "tool":
			base.Tokens.Tool = int64(m.Value)
		}
	case "gemini_cli.session.count":
		base.CanonicalName = otel.CanonicalSessionStart
	case "gemini_cli.tool.decision":
		base.CanonicalName = otel.CanonicalToolDecision
		base.ToolName = AttrString(m.Attrs, "tool_name")
		base.Decision = AttrString(m.Attrs, "decision")
		base.DecisionSource = normalizeDecisionSource(AttrString(m.Attrs, "approval_mode"))
	default:
		base.CanonicalName = otel.CanonicalUnknown
	}

	return []otel.UnifiedSignal{base}
}

// ConvertLog maps Antigravity log events onto canonical names.
func (a *AntigravityAdapter) ConvertLog(res OTLPResource, scope OTLPScope, l OTLPLog) []otel.UnifiedSignal {
	base := a.baseSignal(res, scope, otel.KindLog, l.Name, l.Timestamp, l.Attrs)
	base.TraceID = l.TraceID
	base.SpanID = l.SpanID

	switch l.Name {
	case "gemini_cli.user_prompt", "user_prompt":
		base.CanonicalName = otel.CanonicalUserPrompt
	case "gemini_cli.api_request", "gen_ai.client.operation.duration":
		base.CanonicalName = otel.CanonicalAPIRequest
		base.Model = AttrString(l.Attrs, "gen_ai.response.model")
		if base.Model == "" {
			base.Model = AttrString(l.Attrs, "model")
		}
		base.DurationMs = AttrInt64(l.Attrs, "duration_ms")
		base.Tokens.Input = AttrInt64(l.Attrs, "gen_ai.usage.input_tokens")
		base.Tokens.Output = AttrInt64(l.Attrs, "gen_ai.usage.output_tokens")
		base.Tokens.Thought = AttrInt64(l.Attrs, "gen_ai.usage.thought_tokens")
		base.Tokens.Tool = AttrInt64(l.Attrs, "gen_ai.usage.tool_tokens")
	case "gemini_cli.api_error":
		base.CanonicalName = otel.CanonicalAPIError
		base.Model = AttrString(l.Attrs, "model")
		base.ErrorMsg = AttrString(l.Attrs, "error")
		base.Attempt = int(AttrInt64(l.Attrs, "attempt"))
		fval := false
		base.Success = &fval
	case "gemini_cli.tool_result", "tool_result":
		base.CanonicalName = otel.CanonicalToolResult
		base.ToolName = AttrString(l.Attrs, "tool_name")
		base.DurationMs = AttrInt64(l.Attrs, "duration_ms")
		base.Decision = AttrString(l.Attrs, "decision")
		base.DecisionSource = normalizeDecisionSource(AttrString(l.Attrs, "approval_mode"))
		succ := AttrString(l.Attrs, "success") == "true"
		base.Success = &succ
	case "gemini_cli.tool_decision", "tool_decision":
		base.CanonicalName = otel.CanonicalToolDecision
		base.ToolName = AttrString(l.Attrs, "tool_name")
		base.Decision = AttrString(l.Attrs, "decision")
		base.DecisionSource = normalizeDecisionSource(AttrString(l.Attrs, "approval_mode"))
	default:
		base.CanonicalName = otel.CanonicalUnknown
	}

	return []otel.UnifiedSignal{base}
}

// ConvertSpan maps Antigravity span taxonomy onto canonical names.
func (a *AntigravityAdapter) ConvertSpan(res OTLPResource, scope OTLPScope, s OTLPSpan) []otel.UnifiedSignal {
	base := a.baseSignal(res, scope, otel.KindSpan, s.Name, s.StartTime, s.Attrs)
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
	case "gemini_cli.interaction", "ai.interaction":
		base.CanonicalName = otel.CanonicalInteraction
	case "gemini_cli.llm_request", "gen_ai.client.operation":
		base.CanonicalName = otel.CanonicalAPIRequest
		base.Model = AttrString(s.Attrs, "gen_ai.response.model")
		if base.Model == "" {
			base.Model = AttrString(s.Attrs, "gen_ai.request.model")
		}
		base.Tokens.Input = AttrInt64(s.Attrs, "gen_ai.usage.input_tokens")
		base.Tokens.Output = AttrInt64(s.Attrs, "gen_ai.usage.output_tokens")
		base.Tokens.Thought = AttrInt64(s.Attrs, "gen_ai.usage.thought_tokens")
		base.Tokens.Tool = AttrInt64(s.Attrs, "gen_ai.usage.tool_tokens")
	case "gemini_cli.tool", "ai.tool":
		base.CanonicalName = otel.CanonicalToolResult
		base.ToolName = AttrString(s.Attrs, "tool_name")
		if base.ToolName == "" {
			base.ToolName = AttrString(s.Attrs, "gen_ai.tool.name")
		}
		base.ToolInput = AttrString(s.Attrs, "gen_ai.tool.call.arguments")
		base.ToolOutput = AttrString(s.Attrs, "gen_ai.tool.call.result")
	default:
		base.CanonicalName = otel.CanonicalUnknown
	}

	return []otel.UnifiedSignal{base}
}

// baseSignal populates the correlation IDs common to all Antigravity signals.
func (a *AntigravityAdapter) baseSignal(
	res OTLPResource, scope OTLPScope, kind otel.Kind, name string,
	ts time.Time, attrs map[string]any,
) otel.UnifiedSignal {
	sessionAttr := "session.id" // safe default
	if cfg := harness.Get(string(otel.HarnessAntigravity)); cfg != nil {
		sessionAttr = cfg.SessionAttr
	}
	sig := otel.UnifiedSignal{
		Harness:        otel.HarnessAntigravity,
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
