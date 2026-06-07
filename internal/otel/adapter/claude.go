package adapter

import (
	"time"

	"github.com/shakestzd/wipnote/core/harness"
	"github.com/shakestzd/wipnote/internal/otel"
	"github.com/shakestzd/wipnote/internal/pricing"
)

// ClaudeAdapter converts Claude Code OTel emissions into UnifiedSignals.
// Schema derived from https://code.claude.com/docs/en/monitoring-usage
// and empirically validated against a live `claude -p` OTLP capture
// recorded in this repo's research artifacts.
//
// Identification: resource service.name == "claude-code".
//
// Span taxonomy (empirical):
//
//	claude_code.interaction        — one user prompt turn (root)
//	claude_code.llm_request        — one API call
//	claude_code.tool               — logical tool invocation (tool_name attr)
//	claude_code.tool.execution     — actual tool run (nested under .tool)
//	claude_code.tool.blocked_on_user — permission wait (nested under .tool)
//
// Log events (monitoring-usage doc + empirical):
//
//	claude_code.user_prompt, claude_code.api_request, claude_code.api_error,
//	claude_code.tool_result, claude_code.tool_decision,
//	claude_code.plugin_installed, claude_code.skill_activated,
//	claude_code.api_request_body, claude_code.api_response_body.
//
// Metrics (monitoring-usage doc):
//
//	claude_code.session.count, claude_code.token.usage,
//	claude_code.cost.usage, claude_code.lines_of_code.count,
//	claude_code.commit.count, claude_code.pull_request.count,
//	claude_code.code_edit_tool.decision, claude_code.tool_decision,
//	claude_code.active_time.total.
type ClaudeAdapter struct {
	// Pricing is used to validate vendor cost_usd (sanity check) but
	// Claude emits cost_usd directly so normally it's not needed. Kept
	// as a field so tests can substitute a custom table.
	Pricing *pricing.Table
}

// NewClaudeAdapter returns an adapter using the embedded pricing table.
func NewClaudeAdapter() *ClaudeAdapter {
	tbl, _ := pricing.Default()
	return &ClaudeAdapter{Pricing: tbl}
}

func (c *ClaudeAdapter) Name() otel.Harness { return otel.HarnessClaude }

func (c *ClaudeAdapter) Identify(res OTLPResource) bool {
	cfg := harness.Get(string(otel.HarnessClaude))
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

// ConvertMetric fans out per-type token.usage points and maps every
// other Claude metric into a single canonical token_usage or counter
// signal. The returned slice is usually 1 element; token.usage with
// multiple type dimensions produces one per dimension.
func (c *ClaudeAdapter) ConvertMetric(res OTLPResource, scope OTLPScope, m OTLPMetric) []otel.UnifiedSignal {
	base := c.baseSignal(res, scope, otel.KindMetric, m.Name, m.Timestamp, m.Attrs)

	switch m.Name {
	case "claude_code.token.usage":
		base.CanonicalName = otel.CanonicalTokenUsage
		base.Model = AttrString(m.Attrs, "model")
		tokens := int64(m.Value)
		switch AttrString(m.Attrs, "type") {
		case "input":
			base.Tokens.Input = tokens
		case "output":
			base.Tokens.Output = tokens
		case "cacheRead":
			base.Tokens.CacheRead = tokens
		case "cacheCreation":
			base.Tokens.CacheCreation = tokens
		}
	case "claude_code.cost.usage":
		base.CanonicalName = otel.CanonicalTokenUsage
		base.Model = AttrString(m.Attrs, "model")
		base.CostUSD = m.Value
		base.CostSource = otel.CostSourceVendor
	case "claude_code.session.count":
		base.CanonicalName = otel.CanonicalSessionStart
	case "claude_code.lines_of_code.count":
		base.CanonicalName = otel.CanonicalLinesOfCode
	case "claude_code.commit.count":
		base.CanonicalName = otel.CanonicalCommit
	case "claude_code.pull_request.count":
		base.CanonicalName = otel.CanonicalPullRequest
	case "claude_code.code_edit_tool.decision":
		base.CanonicalName = otel.CanonicalToolDecision
		base.ToolName = AttrString(m.Attrs, "tool_name")
		base.Decision = AttrString(m.Attrs, "decision")
		base.DecisionSource = normalizeDecisionSource(AttrString(m.Attrs, "source"))
	case "claude_code.tool_decision":
		base.CanonicalName = otel.CanonicalToolDecision
		base.ToolName = AttrString(m.Attrs, "tool_name")
		base.Decision = AttrString(m.Attrs, "decision")
		base.DecisionSource = normalizeDecisionSource(AttrString(m.Attrs, "source"))
	case "claude_code.active_time.total":
		base.CanonicalName = otel.CanonicalActiveTime
		base.DurationMs = int64(m.Value * 1000) // OTel unit "s" → ms
	default:
		base.CanonicalName = otel.CanonicalUnknown
	}

	return []otel.UnifiedSignal{base}
}

// ConvertLog maps Claude Code log events onto canonical names and
// extracts the semantically meaningful attributes into UnifiedSignal's
// typed fields (tokens, cost, duration, attempt, decision). Attributes
// not promoted to typed fields remain in RawAttrs for drill-through.
func (c *ClaudeAdapter) ConvertLog(res OTLPResource, scope OTLPScope, l OTLPLog) []otel.UnifiedSignal {
	base := c.baseSignal(res, scope, otel.KindLog, l.Name, l.Timestamp, l.Attrs)
	base.TraceID = l.TraceID
	base.SpanID = l.SpanID

	switch l.Name {
	case "user_prompt", "claude_code.user_prompt":
		base.CanonicalName = otel.CanonicalUserPrompt
	case "api_request", "claude_code.api_request":
		base.CanonicalName = otel.CanonicalAPIRequest
		base.Model = AttrString(l.Attrs, "model")
		base.DurationMs = AttrInt64(l.Attrs, "duration_ms")
		base.Tokens.Input = AttrInt64(l.Attrs, "input_tokens")
		base.Tokens.Output = AttrInt64(l.Attrs, "output_tokens")
		base.Tokens.CacheRead = AttrInt64(l.Attrs, "cache_read_tokens")
		base.Tokens.CacheCreation = AttrInt64(l.Attrs, "cache_creation_tokens")
		base.CostUSD = AttrFloat64(l.Attrs, "cost_usd")
		if base.CostUSD > 0 {
			base.CostSource = otel.CostSourceVendor
		}
	case "api_error", "claude_code.api_error":
		base.CanonicalName = otel.CanonicalAPIError
		base.Model = AttrString(l.Attrs, "model")
		base.DurationMs = AttrInt64(l.Attrs, "duration_ms")
		base.ErrorMsg = AttrString(l.Attrs, "error")
		base.Attempt = int(AttrInt64(l.Attrs, "attempt"))
		if sc := AttrString(l.Attrs, "status_code"); sc != "" && sc != "undefined" {
			base.StatusCode = int(AttrInt64(l.Attrs, "status_code"))
		}
		fval := false
		base.Success = &fval
	case "tool_result", "claude_code.tool_result":
		base.CanonicalName = otel.CanonicalToolResult
		base.ToolName = AttrString(l.Attrs, "tool_name")
		base.DurationMs = AttrInt64(l.Attrs, "duration_ms")
		base.Decision = AttrString(l.Attrs, "decision_type")
		base.DecisionSource = normalizeDecisionSource(AttrString(l.Attrs, "decision_source"))
		base.ErrorMsg = AttrString(l.Attrs, "error")
		succ := AttrString(l.Attrs, "success") == "true"
		base.Success = &succ
	case "tool_decision", "claude_code.tool_decision":
		base.CanonicalName = otel.CanonicalToolDecision
		base.ToolName = AttrString(l.Attrs, "tool_name")
		base.Decision = AttrString(l.Attrs, "decision")
		base.DecisionSource = normalizeDecisionSource(AttrString(l.Attrs, "source"))
	case "plugin_installed", "claude_code.plugin_installed":
		base.CanonicalName = otel.CanonicalPluginInstalled
	case "skill_activated", "claude_code.skill_activated":
		base.CanonicalName = otel.CanonicalSkillActivated
	case "api_request_body", "claude_code.api_request_body",
		"api_response_body", "claude_code.api_response_body":
		// Raw API bodies: OTEL_LOG_RAW_API_BODIES=1. Preserve in RawAttrs
		// but canonicalize as api_request for grouping; query_source
		// attribute distinguishes compact vs normal.
		base.CanonicalName = otel.CanonicalAPIRequest
		base.Model = AttrString(l.Attrs, "model")
	default:
		base.CanonicalName = otel.CanonicalUnknown
	}
	return []otel.UnifiedSignal{base}
}

// ConvertSpan maps Claude span taxonomy onto canonical names. Trace
// hierarchy (TraceID, SpanID, ParentSpan) flows through regardless of
// canonicalization so event-tree.js can reconstruct the tree even
// when a span's name is unrecognized.
func (c *ClaudeAdapter) ConvertSpan(res OTLPResource, scope OTLPScope, s OTLPSpan) []otel.UnifiedSignal {
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
	case "claude_code.interaction":
		base.CanonicalName = otel.CanonicalInteraction
	case "claude_code.llm_request":
		base.CanonicalName = otel.CanonicalAPIRequest
		base.Model = AttrString(s.Attrs, "model")
		base.Tokens.Input = AttrInt64(s.Attrs, "input_tokens")
		base.Tokens.Output = AttrInt64(s.Attrs, "output_tokens")
		base.Tokens.CacheRead = AttrInt64(s.Attrs, "cache_read_tokens")
		base.Tokens.CacheCreation = AttrInt64(s.Attrs, "cache_creation_tokens")
		base.Attempt = int(AttrInt64(s.Attrs, "attempt"))
	case "claude_code.tool":
		base.CanonicalName = otel.CanonicalToolResult
		base.ToolName = AttrString(s.Attrs, "tool_name")
		// Subagent invocations use the Agent tool (Task tool) — flag
		// them for easier dashboard grouping.
		if base.ToolName == "Agent" || base.ToolName == "Task" {
			base.CanonicalName = otel.CanonicalSubagent
		}
	case "claude_code.tool.execution":
		base.CanonicalName = otel.CanonicalToolExecution
	case "claude_code.tool.blocked_on_user":
		base.CanonicalName = otel.CanonicalToolBlocked
		base.Decision = AttrString(s.Attrs, "decision")
		base.DecisionSource = normalizeDecisionSource(AttrString(s.Attrs, "source"))
	default:
		base.CanonicalName = otel.CanonicalUnknown
	}
	return []otel.UnifiedSignal{base}
}

// baseSignal populates the correlation IDs and copies the attribute map
// into RawAttrs for drill-through. Every ConvertX method starts from
// this and fills canonical fields on top.
func (c *ClaudeAdapter) baseSignal(
	res OTLPResource, scope OTLPScope, kind otel.Kind, name string,
	ts time.Time, attrs map[string]any,
) otel.UnifiedSignal {
	sessionAttr := "session.id" // safe default
	if cfg := harness.Get(string(otel.HarnessClaude)); cfg != nil {
		sessionAttr = cfg.SessionAttr
	}
	sig := otel.UnifiedSignal{
		Harness:        otel.HarnessClaude,
		HarnessVersion: AttrString(res.Attrs, "service.version"),
		Kind:           kind,
		NativeName:     name,
		Timestamp:      ts,
		SessionID:      ResolveSessionID(attrs, res.Attrs, sessionAttr),
		PromptID:       AttrString(attrs, "prompt.id"),
		RawAttrs:       copyAttrs(attrs),
	}
	return sig
}

// copyAttrs returns a shallow clone of attrs. RawAttrs in the
// UnifiedSignal must be independent of the OTLPMetric/Log/Span input
// so the caller can free the decoded payload after ConvertX returns.
func copyAttrs(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// normalizeDecisionSource collapses harness-specific source strings
// onto the canonical DecisionSource* constants.
//
//	Claude: config | hook | user_permanent | user_temporary | user_abort | user_reject
//	Codex:  Config | User | AutomatedReviewer   (capitalized)
//	Gemini: (varies per approval_mode)
//
// Unrecognized values pass through unchanged so the RawAttrs drill-through
// preserves fidelity.
func normalizeDecisionSource(src string) string {
	switch src {
	case "config", "Config":
		return otel.DecisionSourceConfig
	case "hook":
		return otel.DecisionSourceHook
	case "user_permanent", "user_temporary", "user_abort", "user_reject", "User", "user":
		return otel.DecisionSourceUser
	case "AutomatedReviewer":
		return otel.DecisionSourceAutomatedReviewer
	default:
		return src
	}
}
