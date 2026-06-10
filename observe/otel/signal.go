// Package otel defines the canonical, harness-agnostic telemetry signal
// representation used across wipnote's OpenTelemetry ingestion pipeline.
//
// Three AI-coding harnesses emit OTel telemetry with different dialects:
// Claude Code (claude_code.*), Codex CLI (codex.*), and Gemini CLI
// (gemini_cli.* and gen_ai.*). Each is converted by a harness-specific
// Adapter (internal/otel/adapter) into UnifiedSignal, which is what
// persistence and the dashboard consume.
//
// This package is intentionally minimal and free of protobuf dependencies
// so adapters and tests can exercise the schema without the receiver.
package otel

import "time"

// Kind classifies a signal as a metric point, a log record, or a span.
type Kind string

const (
	KindMetric Kind = "metric"
	KindLog    Kind = "log"
	KindSpan   Kind = "span"
)

// Harness identifies which CLI produced the signal.
type Harness string

const (
	HarnessClaude Harness = "claude_code"
	HarnessCodex  Harness = "codex"
	HarnessGemini Harness = "gemini_cli"
)

// Canonical event names. Adapters map native harness names onto these
// so downstream code can query across harnesses. A signal's NativeName
// always preserves the original (harness-prefixed) name for drill-through.
const (
	CanonicalUserPrompt      = "user_prompt"
	CanonicalAPIRequest      = "api_request"
	CanonicalAPIError        = "api_error"
	CanonicalToolResult      = "tool_result"
	CanonicalToolDecision    = "tool_decision"
	CanonicalTokenUsage      = "token_usage"
	CanonicalSessionStart    = "session_start"
	CanonicalSessionEnd      = "session_end"
	CanonicalInteraction     = "interaction"
	CanonicalToolExecution   = "tool_execution"
	CanonicalToolBlocked     = "tool_blocked_on_user"
	CanonicalSkillActivated  = "skill_activated"
	CanonicalPluginInstalled = "plugin_installed"
	CanonicalCompaction      = "compaction"
	CanonicalRetry           = "retry"
	CanonicalSubagent        = "subagent_invocation"
	CanonicalLinesOfCode     = "lines_of_code"
	CanonicalCommit          = "commit"
	CanonicalPullRequest     = "pull_request"
	CanonicalActiveTime      = "active_time"
	CanonicalUnknown         = "unknown"
)

// Decision source values after harness-specific normalization.
// Claude Code: config|hook|user_permanent|user_temporary|user_abort|user_reject
// Codex: Config|User|AutomatedReviewer
// Gemini: approval_mode values + conseca verdicts
const (
	DecisionSourceConfig            = "config"
	DecisionSourceHook              = "hook"
	DecisionSourceUser              = "user"
	DecisionSourceAutomatedReviewer = "automated_reviewer"
)

// CostSource records how CostUSD was derived so reports can distinguish
// vendor-reported cost (Claude) from our own token-table estimates (Codex,
// Gemini), and flag unknown-model fallbacks.
type CostSource string

const (
	CostSourceVendor  CostSource = "vendor"
	CostSourceDerived CostSource = "derived"
	CostSourceUnknown CostSource = "unknown"
)

// TokenCounts captures every token dimension any harness emits. Zero-values
// mean "not reported by this signal" — adapters leave unknown fields at 0
// rather than guessing. The union is deliberately wide: a metric aggregate
// may carry Input+Output only, while an Anthropic api_request carries all
// four of Input/Output/CacheRead/CacheCreation, and a Codex turn adds
// Reasoning+Tool.
type TokenCounts struct {
	Input         int64
	Output        int64
	CacheRead     int64
	CacheCreation int64
	Thought       int64 // Gemini-specific reasoning tokens
	Tool          int64 // Gemini/Codex tool-execution tokens
	Reasoning     int64 // Codex-specific reasoning tokens
}

// Total returns the sum of every populated token dimension. It is used for
// rough aggregate displays; cost derivation uses the individual dimensions
// against the model's per-dimension pricing and should NOT rely on Total.
func (t TokenCounts) Total() int64 {
	return t.Input + t.Output + t.CacheRead + t.CacheCreation + t.Thought + t.Tool + t.Reasoning
}

// UnifiedSignal is the persistence-ready, harness-agnostic representation
// of one OTel signal. Every field except Harness, SignalID, Kind,
// CanonicalName, NativeName, Timestamp, and SessionID may be zero.
//
// SignalID must be stable across retries so duplicate OTLP exports don't
// double-count. Convention: the receiver derives it from a hash of
// (resource, scope, name, timestamp, sorted attributes).
//
// Canonical attribute mapping by harness (all adapters must populate
// Harness, SessionID, and CanonicalName on every signal):
//
//	Claude  service.name=claude-code   session.id      → SessionID
//	Codex   service.name=codex-cli     conversation.id → SessionID
//	Gemini  service.name=gemini-cli    session.id      → SessionID
//
//	PromptID sources:
//	  Claude  prompt.id (signal attr)
//	  Codex   gen_ai.prompt_id (signal attr); empty when absent — Codex does
//	          not emit a stable per-prompt ID on every signal, so callers
//	          needing prompt-level correlation must group by
//	          (SessionID, Timestamp window).
//	  Gemini  gen_ai.prompt_id (signal attr)
//
// SessionID falls back to the resource-level attribute when absent from
// the signal-level attributes (cardinality-controlled metrics omit it).
type UnifiedSignal struct {
	// identity
	Harness        Harness
	HarnessVersion string
	SignalID       string
	Kind           Kind
	CanonicalName  string
	NativeName     string
	Timestamp      time.Time

	// correlation (normalized across harnesses)
	SessionID  string // Claude session.id | Codex conversation_id | Gemini session.id
	PromptID   string // Claude prompt.id | Codex gen_ai.prompt_id (empty when absent) | Gemini gen_ai.prompt_id
	TraceID    string // W3C hex, unmodified
	SpanID     string
	ParentSpan string

	// semantic attributes (present when applicable)
	ToolName       string
	ToolUseID      string // hooks-side only; OTel doesn't carry it
	ToolInput      string // raw input arguments (available when GEMINI_TELEMETRY_TRACES=true)
	ToolOutput     string // raw call result (available when GEMINI_TELEMETRY_TRACES=true)
	Model          string
	Decision       string
	DecisionSource string

	// accounting
	Tokens     TokenCounts
	CostUSD    float64
	CostSource CostSource

	// performance / errors
	DurationMs int64
	Success    *bool
	ErrorMsg   string
	Attempt    int
	StatusCode int

	// everything the receiver saw, unmodified, for drill-through.
	// Includes attributes the canonical fields above don't surface.
	RawAttrs map[string]any
}
