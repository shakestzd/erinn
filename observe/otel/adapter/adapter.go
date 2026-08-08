// Package adapter converts harness-specific OTel payloads into the
// canonical UnifiedSignal representation defined in internal/otel.
//
// The Adapter interface intentionally works against minimal, protobuf-free
// intermediate types (OTLPResource, OTLPMetric, OTLPLog, OTLPSpan) so
// tests can construct payloads directly and so the receiver can evolve
// its protobuf plumbing without churning every adapter.
//
// Adapters for Claude Code, Codex CLI, and Gemini CLI are registered by
// name at package init. The Registry.Resolve function inspects OTLPResource
// attributes (service.name, gen_ai.agent.name) and picks the right adapter.
package adapter

import (
	"sync"
	"time"

	"github.com/shakestzd/wipnote/observe/otel"
)

// OTLPResource is the subset of an OTLP ResourceSpans/ResourceLogs/
// ResourceMetrics Resource the adapter needs.
type OTLPResource struct {
	// Attrs is the flattened OTel resource attribute map (e.g. service.name,
	// service.version, telemetry.sdk.name). Values follow OTel AnyValue
	// flattening: string, int64, float64, bool, []any, map[string]any.
	Attrs map[string]any
}

// OTLPScope mirrors an OTLP InstrumentationScope. For Claude Code this is
// typically "com.anthropic.claude_code"; for Gemini the scope carries
// GenAI semconv metadata.
type OTLPScope struct {
	Name    string
	Version string
	Attrs   map[string]any
}

// MetricKind classifies the OTLP metric point type. Histogram aggregates
// arrive with bucket counts elsewhere; adapters typically collapse them
// to a representative Value (sum/mean) and preserve the raw data via
// Attrs for drill-through.
type MetricKind string

const (
	MetricKindCounter   MetricKind = "counter"
	MetricKindGauge     MetricKind = "gauge"
	MetricKindHistogram MetricKind = "histogram"
)

// OTLPMetric captures a single metric data point. Adapters receive one
// call per (metric_name, attribute-set) tuple; the receiver fans out
// multi-point aggregates before calling Convert.
type OTLPMetric struct {
	Name      string
	Kind      MetricKind
	Unit      string
	Timestamp time.Time
	Value     float64 // counter delta, gauge snapshot, or histogram sum
	Count     uint64  // populated for histograms
	Attrs     map[string]any
	StartTime time.Time
}

// OTLPLog captures one OTLP log/event record. OTel LogRecord fields map
// straight through: Severity is the numeric severity (1-24), Body may
// be a structured map or a plain string.
type OTLPLog struct {
	Name           string
	Timestamp      time.Time
	ObservedTime   time.Time
	SeverityNumber int32
	SeverityText   string
	Body           any
	Attrs          map[string]any
	TraceID        string // W3C hex, may be empty
	SpanID         string
}

// OTLPSpan captures an OTLP Span. TraceID and SpanID are hex-encoded
// (per the OTLP/HTTP JSON spec deviation); binary-encoded OTLP/gRPC
// is converted to hex before the adapter sees it.
type OTLPSpan struct {
	Name         string
	TraceID      string
	SpanID       string
	ParentSpanID string
	Kind         int32 // 1=internal, 2=server, 3=client, 4=producer, 5=consumer
	StartTime    time.Time
	EndTime      time.Time
	StatusCode   int32 // 0=unset, 1=ok, 2=error
	StatusMsg    string
	Attrs        map[string]any
	Events       []OTLPSpanEvent
}

// OTLPSpanEvent is a timestamped annotation within a span (e.g. tool
// input/output content when OTEL_LOG_TOOL_CONTENT=1).
type OTLPSpanEvent struct {
	Name      string
	Timestamp time.Time
	Attrs     map[string]any
}

// Adapter converts one harness's OTLP payloads into UnifiedSignals.
//
// Identify inspects the OTLP resource to decide whether this adapter owns
// the signal. A request whose service.name is "claude-code" is owned by
// the Claude adapter, "codex" by Codex, and so on.
//
// Convert* methods may return zero, one, or multiple signals per input.
// Returning zero is valid (e.g. for heartbeat metrics with no wipnote
// meaning). Returning multiple is valid for metric aggregates that carry
// per-dimension token counts needing one signal per dimension.
type Adapter interface {
	Name() otel.Harness
	Identify(res OTLPResource) bool
	ConvertMetric(res OTLPResource, scope OTLPScope, m OTLPMetric) []otel.UnifiedSignal
	ConvertLog(res OTLPResource, scope OTLPScope, l OTLPLog) []otel.UnifiedSignal
	ConvertSpan(res OTLPResource, scope OTLPScope, s OTLPSpan) []otel.UnifiedSignal
}

// Registry owns adapter lookup. The receiver calls Resolve exactly once
// per OTLP batch (batches are resource-scoped) and reuses the result for
// every signal in the batch.
type Registry struct {
	mu       sync.RWMutex
	adapters []Adapter
}

// NewRegistry returns an empty Registry. Callers register adapters
// before serving traffic; dynamic registration is not supported.
func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds an adapter. Registration order is the Identify probe
// order — the first adapter whose Identify returns true wins. The Claude
// adapter should register first, then Codex, then Gemini, so the fastest
// positive match lands first for the most common case.
func (r *Registry) Register(a Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters = append(r.adapters, a)
}

// Resolve returns the adapter that owns this resource, or nil if none
// claim it. Unclaimed resources are dropped; the caller is expected to
// count them for an observability-of-observability metric.
func (r *Registry) Resolve(res OTLPResource) Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, a := range r.adapters {
		if a.Identify(res) {
			return a
		}
	}
	return nil
}

// Adapters returns a snapshot of the registered adapters. Useful for
// /api/otel/status dashboards and tests.
func (r *Registry) Adapters() []Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Adapter, len(r.adapters))
	copy(out, r.adapters)
	return out
}

// ResolveSessionID returns the session ID for a signal under the given
// attribute key, falling back to the resource-level attribute when the
// signal-level attribute is missing or empty. This is the canonical
// pattern every adapter follows: cardinality-controlled metrics may
// strip per-data-point attributes, leaving only the resource. Used by
// ClaudeAdapter ("session.id"), CodexAdapter ("conversation.id"), and
// GeminiAdapter ("session.id").
func ResolveSessionID(signalAttrs, resAttrs map[string]any, key string) string {
	if v := AttrString(signalAttrs, key); v != "" {
		return v
	}
	return AttrString(resAttrs, key)
}

// AttrString returns the string-valued attribute at key, or "" if missing
// or not a string. Adapters use this heavily for semconv lookups.
func AttrString(attrs map[string]any, key string) string {
	if v, ok := attrs[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// AttrBool returns the bool-valued attribute at key and whether it was
// present as a native bool. Several harness spans (Claude Code's
// claude_code.llm_request and claude_code.tool.execution, empirically)
// carry an explicit "success" boolean attribute even though the OTLP
// span-level status field is left UNSET, so adapters prefer this over
// StatusCode when it's present.
func AttrBool(attrs map[string]any, key string) (bool, bool) {
	v, ok := attrs[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// AttrInt64 returns the int64-valued attribute at key. Accepts int,
// int32, int64, float64 (narrowed), and string (parsed). Returns 0 on
// any failure — adapters treat 0 as "not reported."
func AttrInt64(attrs map[string]any, key string) int64 {
	v, ok := attrs[key]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	case string:
		var n int64
		for i := 0; i < len(x); i++ {
			if x[i] < '0' || x[i] > '9' {
				return 0
			}
			n = n*10 + int64(x[i]-'0')
		}
		return n
	}
	return 0
}

// AttrFloat64 returns the float64-valued attribute at key. Accepts
// float64, float32, int64, and string (parsed). Returns 0 on any failure.
func AttrFloat64(attrs map[string]any, key string) float64 {
	v, ok := attrs[key]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		return parseFloat(x)
	}
	return 0
}

// parseFloat is a minimal float parser that handles the decimal strings
// OTel emits without pulling in strconv just for this helper. It matches
// strconv.ParseFloat(s, 64) behavior for non-scientific decimals.
func parseFloat(s string) float64 {
	if s == "" {
		return 0
	}
	neg := false
	i := 0
	if s[0] == '-' {
		neg = true
		i = 1
	} else if s[0] == '+' {
		i = 1
	}
	var whole float64
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		whole = whole*10 + float64(s[i]-'0')
	}
	var frac float64
	var scale float64 = 1
	if i < len(s) && s[i] == '.' {
		i++
		for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
			frac = frac*10 + float64(s[i]-'0')
			scale *= 10
		}
	}
	v := whole + frac/scale
	if neg {
		v = -v
	}
	return v
}
