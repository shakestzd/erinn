package adapter

import (
	"time"

	"github.com/shakestzd/wipnote/core/harness"
	"github.com/shakestzd/wipnote/observe/otel"
	"github.com/shakestzd/wipnote/observe/pricing"
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
		// bug-79606e3d: Claude Code carries no explicit success/status
		// attribute on this log event — it never emits one, in ~700
		// live-captured api_request logs. But reaching this event AT ALL is
		// itself the outcome signal: Claude Code logs a failed call as a
		// separate "api_error" event (CanonicalAPIError, hardcoded false
		// below), never as "api_request". So an api_request log implies
		// success by construction, mirroring the hardcoded false on api_error.
		succ := true
		base.Success = &succ
		tagSuccessSource(&base, SuccessSourceStructural)
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
		tagSuccessSource(&base, SuccessSourceStructural)
	case "tool_result", "claude_code.tool_result":
		base.CanonicalName = otel.CanonicalToolResult
		base.ToolName = AttrString(l.Attrs, "tool_name")
		base.ToolUseID = AttrString(l.Attrs, "tool_use_id")
		base.DurationMs = AttrInt64(l.Attrs, "duration_ms")
		base.Decision = AttrString(l.Attrs, "decision_type")
		base.DecisionSource = normalizeDecisionSource(AttrString(l.Attrs, "decision_source"))
		base.ErrorMsg = AttrString(l.Attrs, "error")
		// bug-a0143c2c: claude_code.tool_result is the STABLE, non-beta source
		// for tool outcomes — part of the standard log signal (gated only by
		// OTEL_LOGS_EXPORTER), unlike claude_code.tool.execution's "success"
		// span attribute below, which lives under the harness's beta Traces
		// feature and carries a published warning that span attributes may
		// change between releases. The harness emits this attribute as the
		// string "true"/"false" (never a native bool) — normalize
		// deliberately rather than incidentally: any other value (missing,
		// malformed, differently cased) is treated as false rather than left
		// ambiguous, since a log record without a parseable outcome is not
		// evidence of success.
		succ := AttrString(l.Attrs, "success") == "true"
		base.Success = &succ
		tagSuccessSource(&base, SuccessSourceLogStable)
	case "tool_decision", "claude_code.tool_decision":
		base.CanonicalName = otel.CanonicalToolDecision
		base.ToolName = AttrString(l.Attrs, "tool_name")
		base.ToolUseID = AttrString(l.Attrs, "tool_use_id")
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
	// tool_use_id (aliased gen_ai.tool.call.id on some spans) is the join key
	// CrossValidateToolOutcomes uses to reconcile this span's outcome against
	// the stable claude_code.tool_result log event for the same tool call
	// (bug-a0143c2c). Harmless when absent — only tool-family spans carry it.
	base.ToolUseID = AttrString(s.Attrs, "tool_use_id")
	if base.ToolUseID == "" {
		base.ToolUseID = AttrString(s.Attrs, "gen_ai.tool.call.id")
	}
	// bug-79606e3d: Claude Code virtually never sets the OTLP span status
	// (StatusCode stays 0/UNSET on ~99.95% of claude_code.llm_request and
	// claude_code.tool.execution spans in live capture) but DOES emit an
	// explicit "success" boolean attribute on those same spans. Prefer the
	// attribute — it's the harness's actual outcome signal — and fall back
	// to StatusCode for spans that don't carry it (e.g. claude_code.tool,
	// which reports neither and is correctly left NULL).
	//
	// bug-a0143c2c: this attribute lives under the harness's Traces feature,
	// which is explicitly beta and carries a published warning that span
	// names and attributes may change between releases — a stated
	// instability, not a hypothetical. claude_code.llm_request has NO
	// non-beta alternative for success, so it stays the highest-risk input
	// in any derivation that uses it. claude_code.tool.execution DOES have
	// one: claude_code.tool_result (the log event, see ConvertLog) reports
	// the same outcome without the beta gate. tagSuccessSource records which
	// risk category applies so a downstream consumer can tell without
	// re-deriving the harness's own instability boundaries, and
	// CrossValidateToolOutcomes reconciles the two sources for tool calls
	// whose span and log land in the same OTLP batch.
	if v, ok := AttrBool(s.Attrs, "success"); ok {
		vv := v
		base.Success = &vv
		if !vv && base.ErrorMsg == "" {
			base.ErrorMsg = AttrString(s.Attrs, "error")
		}
		if s.Name == "claude_code.llm_request" {
			tagSuccessSource(&base, SuccessSourceSpanBetaNoAlternative)
		} else {
			tagSuccessSource(&base, SuccessSourceSpanBetaHasAlternative)
		}
	} else if s.StatusCode == 1 {
		v := true
		base.Success = &v
		tagSuccessSource(&base, SuccessSourceStatusCode)
	} else if s.StatusCode == 2 {
		v := false
		base.Success = &v
		base.ErrorMsg = s.StatusMsg
		tagSuccessSource(&base, SuccessSourceStatusCode)
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

// successSource marks the provenance of a UnifiedSignal's Success value
// (bug-a0143c2c) so a downstream consumer can prefer stable inputs over beta
// ones — or notice a beta value has no stable counterpart at all — without
// re-deriving the harness's own instability boundaries from scratch.
const (
	// SuccessSourceStructural: api_request/api_error logs carry no explicit
	// outcome attribute; the outcome is inferred from which event name was
	// emitted at all (see the api_request/api_error cases in ConvertLog).
	SuccessSourceStructural = "structural"
	// SuccessSourceLogStable: claude_code.tool_result's "success" string
	// attribute — part of the standard log signal, not behind the beta gate.
	SuccessSourceLogStable = "log_stable"
	// SuccessSourceSpanBetaHasAlternative: claude_code.tool.execution's
	// "success" boolean attribute — beta (Traces feature), but
	// claude_code.tool_result reports the same outcome without the gate.
	SuccessSourceSpanBetaHasAlternative = "span_beta_has_alternative"
	// SuccessSourceSpanBetaNoAlternative: claude_code.llm_request's "success"
	// boolean attribute — beta, and no non-beta signal reports API-call
	// outcome at all. Highest-risk input in any derivation that uses it.
	SuccessSourceSpanBetaNoAlternative = "span_beta_no_alternative"
	// SuccessSourceStatusCode: the OTLP span status fallback. Native Claude
	// Code spans essentially never set this (see bug-79606e3d) — this path
	// exists for forward compatibility, not because it fires in practice.
	SuccessSourceStatusCode = "status_code"
)

// RawAttrs keys written by tagSuccessSource and CrossValidateToolOutcomes.
// Exported (leading underscore, matching the "_pending" placeholder
// convention used elsewhere in the OTel pipeline) so a consumer reading
// attrs_json — a dashboard, a rollup, a query — has a named constant instead
// of a magic string, and so it flows through with zero schema or writer
// change: queryable via json_extract like every other internal marker in
// this codebase.
const (
	SuccessSourceAttrKey              = "_success_source"
	SuccessCrossCheckAttrKey          = "_success_cross_check"
	SuccessCrossCheckSpanBetaAttrKey  = "_success_cross_check_span_beta"
	SuccessCrossCheckLogStableAttrKey = "_success_cross_check_log_stable"
)

// tagSuccessSource records source in sig.RawAttrs under SuccessSourceAttrKey.
func tagSuccessSource(sig *otel.UnifiedSignal, source string) {
	if sig.RawAttrs == nil {
		sig.RawAttrs = map[string]any{}
	}
	sig.RawAttrs[SuccessSourceAttrKey] = source
}

// CrossValidateToolOutcomes reconciles claude_code.tool.execution (span,
// beta-gated) and claude_code.tool_result (log, stable) success values for
// tool calls whose signals arrived in the SAME OTLP batch, joined on
// ToolUseID (bug-a0143c2c).
//
// Coverage is deliberately partial, and that limit is load-bearing rather
// than an oversight: the beta span and the stable log come from two
// independent exporters (traces vs. logs) that flush on independent 5s
// intervals by default, so a given tool call's pair frequently lands in
// different OTLP batches. This pass only catches the fraction that arrives
// together — it does not attempt to backfill outcomes across batches, which
// would require state persisted beyond one batch. The ClaudeAdapter this
// method hangs off is a long-lived singleton shared across every session the
// receiver ever sees (constructed once in receiver.New, never per-request),
// so adapter-side correlation state that outlives a single batch would grow
// unboundedly for exactly the common case where only one exporter is
// enabled and a tool_use_id's other half never arrives. ToolUseID is
// promoted to a typed field specifically so the pairs this pass misses
// remain reconcilable later — a query joining otel_signals on it runs
// against the durable table instead of in-memory state, so it has no such
// growth or timing problem.
//
// On a match, both signals are tagged "_success_cross_check":"match". On a
// mismatch, both are tagged "_success_cross_check":"mismatch" plus each
// other's raw value, so the divergence is visible on the signal itself
// (dashboard attrs drill-through, or a json_extract query) rather than one
// value silently winning. A ToolUseID with more than one candidate on either
// side within the batch is ambiguous and is skipped rather than guessed.
func (c *ClaudeAdapter) CrossValidateToolOutcomes(signals []otel.UnifiedSignal) []otel.UnifiedSignal {
	type candidate struct {
		execIdx, resultIdx     int
		execCount, resultCount int
	}
	byToolUseID := make(map[string]*candidate)
	for i := range signals {
		sig := &signals[i]
		if sig.ToolUseID == "" {
			continue
		}
		var c *candidate
		switch {
		case sig.Kind == otel.KindSpan && sig.CanonicalName == otel.CanonicalToolExecution:
			c = byToolUseID[sig.ToolUseID]
			if c == nil {
				c = &candidate{}
				byToolUseID[sig.ToolUseID] = c
			}
			c.execIdx = i
			c.execCount++
		case sig.Kind == otel.KindLog && sig.CanonicalName == otel.CanonicalToolResult:
			c = byToolUseID[sig.ToolUseID]
			if c == nil {
				c = &candidate{}
				byToolUseID[sig.ToolUseID] = c
			}
			c.resultIdx = i
			c.resultCount++
		}
	}
	for _, c := range byToolUseID {
		if c.execCount != 1 || c.resultCount != 1 {
			continue // ambiguous or incomplete pair this batch — skip, don't guess
		}
		execSig := &signals[c.execIdx]
		resultSig := &signals[c.resultIdx]
		if execSig.Success == nil || resultSig.Success == nil {
			continue
		}
		outcome := "match"
		if *execSig.Success != *resultSig.Success {
			outcome = "mismatch"
		}
		for _, sig := range [2]*otel.UnifiedSignal{execSig, resultSig} {
			if sig.RawAttrs == nil {
				sig.RawAttrs = map[string]any{}
			}
			sig.RawAttrs[SuccessCrossCheckAttrKey] = outcome
			if outcome == "mismatch" {
				sig.RawAttrs[SuccessCrossCheckSpanBetaAttrKey] = *execSig.Success
				sig.RawAttrs[SuccessCrossCheckLogStableAttrKey] = *resultSig.Success
			}
		}
	}
	return signals
}
