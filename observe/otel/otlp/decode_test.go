package otlp_test

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/observe/otel/otlp"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// Fixtures drawn from the empirical live OTLP run against claude -p.
// Matching identifiers, attribute names, and values keep the Phase 1
// decoder test grounded in real payloads, not aspirational ones.
var (
	liveTraceID = []byte{
		0xa4, 0xe2, 0x8f, 0x48, 0xfb, 0xdb, 0x66, 0x44,
		0xa9, 0x2b, 0x20, 0x8f, 0x21, 0x45, 0xae, 0xe1,
	}
	liveRootSpan = []byte{
		0x7d, 0x7f, 0x9e, 0xa0, 0x11, 0x22, 0x33, 0x44,
	}
	liveBashSpan = []byte{
		0xf2, 0xb6, 0x65, 0x05, 0xaa, 0xbb, 0xcc, 0xdd,
	}
)

func kvString(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{
		Key:   k,
		Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}},
	}
}

func kvInt(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{
		Key:   k,
		Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}},
	}
}

func TestDecodeTraces_LiveFixture(t *testing.T) {
	// Mirror the two-span subset captured from the Claude empirical run:
	// root claude_code.interaction → child claude_code.tool (tool_name=Bash).
	res := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
		kvString("service.name", "claude-code"),
		kvString("service.version", "2.1.42"),
	}}
	scope := &commonpb.InstrumentationScope{Name: "com.anthropic.claude_code"}

	now := uint64(time.Unix(0, 1735000000000000000).UnixNano())
	req := []*tracepb.ResourceSpans{{
		Resource: res,
		ScopeSpans: []*tracepb.ScopeSpans{{
			Scope: scope,
			Spans: []*tracepb.Span{
				{
					TraceId:           liveTraceID,
					SpanId:            liveRootSpan,
					Name:              "claude_code.interaction",
					StartTimeUnixNano: now,
					EndTimeUnixNano:   now + 25_368_000_000,
					Attributes: []*commonpb.KeyValue{
						kvString("session.id", "6bfe7f17-971d-4c30-99f2-1c8b91c87f2b"),
						kvInt("interaction.sequence", 1),
					},
				},
				{
					TraceId:           liveTraceID,
					SpanId:            liveBashSpan,
					ParentSpanId:      liveRootSpan,
					Name:              "claude_code.tool",
					StartTimeUnixNano: now + 5_000_000_000,
					EndTimeUnixNano:   now + 7_766_000_000,
					Attributes: []*commonpb.KeyValue{
						kvString("tool_name", "Bash"),
						kvString("full_command", "env | grep TRACEPARENT"),
					},
				},
			},
		}},
	}}

	decoded := otlp.DecodeTraces(req)
	if len(decoded) != 1 {
		t.Fatalf("Decoded count = %d, want 1", len(decoded))
	}
	d := decoded[0]
	if got := d.Resource.Attrs["service.name"]; got != "claude-code" {
		t.Errorf("resource service.name = %v, want claude-code", got)
	}
	if len(d.Spans) != 2 {
		t.Fatalf("spans = %d, want 2", len(d.Spans))
	}

	root := d.Spans[0]
	if root.Span.TraceID != "a4e28f48fbdb6644a92b208f2145aee1" {
		t.Errorf("TraceID = %q", root.Span.TraceID)
	}
	if root.Span.SpanID != "7d7f9ea011223344" {
		t.Errorf("SpanID = %q", root.Span.SpanID)
	}
	if root.Span.ParentSpanID != "" {
		t.Errorf("root ParentSpanID = %q, want empty", root.Span.ParentSpanID)
	}

	child := d.Spans[1]
	if child.Span.ParentSpanID != "7d7f9ea011223344" {
		t.Errorf("child ParentSpanID = %q", child.Span.ParentSpanID)
	}
	if child.Span.Attrs["tool_name"] != "Bash" {
		t.Errorf("child tool_name = %v", child.Span.Attrs["tool_name"])
	}
}

func TestDecodeTraces_RejectsInvalidIDs(t *testing.T) {
	req := []*tracepb.ResourceSpans{{
		Resource: &resourcepb.Resource{},
		ScopeSpans: []*tracepb.ScopeSpans{{
			Scope: &commonpb.InstrumentationScope{},
			Spans: []*tracepb.Span{{
				TraceId: make([]byte, 16), // all zeros → invalid
				SpanId:  []byte{1, 2, 3, 4, 5, 6, 7, 8},
				Name:    "bogus",
			}},
		}},
	}}
	d := otlp.DecodeTraces(req)
	if len(d[0].Spans) != 0 {
		t.Errorf("invalid trace ID produced %d spans, want 0", len(d[0].Spans))
	}
}

func TestDecodeMetrics_TokenUsageFanout(t *testing.T) {
	// Mirror claude_code.token.usage: one metric with 4 data points
	// (type=input|output|cacheRead|cacheCreation). Adapter expects
	// one OTLPMetric per data point.
	req := []*metricspb.ResourceMetrics{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
			kvString("service.name", "claude-code"),
		}},
		ScopeMetrics: []*metricspb.ScopeMetrics{{
			Scope: &commonpb.InstrumentationScope{Name: "com.anthropic.claude_code"},
			Metrics: []*metricspb.Metric{{
				Name: "claude_code.token.usage",
				Unit: "tokens",
				Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
					AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
					IsMonotonic:            true,
					DataPoints: []*metricspb.NumberDataPoint{
						{
							TimeUnixNano: uint64(time.Now().UnixNano()),
							Value:        &metricspb.NumberDataPoint_AsInt{AsInt: 10},
							Attributes: []*commonpb.KeyValue{
								kvString("type", "input"),
								kvString("model", "claude-haiku-4-5-20251001"),
							},
						},
						{
							TimeUnixNano: uint64(time.Now().UnixNano()),
							Value:        &metricspb.NumberDataPoint_AsInt{AsInt: 577},
							Attributes: []*commonpb.KeyValue{
								kvString("type", "output"),
								kvString("model", "claude-haiku-4-5-20251001"),
							},
						},
					},
				}},
			}},
		}},
	}}
	d := otlp.DecodeMetrics(req)
	if len(d) != 1 {
		t.Fatalf("Decoded count = %d", len(d))
	}
	if len(d[0].Metrics) != 2 {
		t.Fatalf("metrics = %d, want 2 (one per data point)", len(d[0].Metrics))
	}
	first := d[0].Metrics[0]
	if first.Metric.Name != "claude_code.token.usage" {
		t.Errorf("Name = %q", first.Metric.Name)
	}
	if first.Metric.Attrs["type"] != "input" || first.Metric.Value != 10 {
		t.Errorf("first point attrs=%v value=%v", first.Metric.Attrs, first.Metric.Value)
	}
}

func TestDecodeLogs_ExtractsEventName(t *testing.T) {
	// Claude Code emits event.name as an attribute rather than on the
	// LogRecord.EventName field. The decoder must extract it regardless.
	req := []*logspb.ResourceLogs{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
			kvString("service.name", "claude-code"),
		}},
		ScopeLogs: []*logspb.ScopeLogs{{
			Scope: &commonpb.InstrumentationScope{},
			LogRecords: []*logspb.LogRecord{{
				TimeUnixNano: uint64(time.Now().UnixNano()),
				Attributes: []*commonpb.KeyValue{
					kvString("event.name", "api_request"),
					kvString("model", "claude-haiku-4-5-20251001"),
					kvInt("input_tokens", 10),
				},
			}},
		}},
	}}
	d := otlp.DecodeLogs(req)
	if len(d[0].Logs) != 1 {
		t.Fatalf("logs = %d", len(d[0].Logs))
	}
	if got := d[0].Logs[0].Log.Name; got != "api_request" {
		t.Errorf("Name = %q, want api_request", got)
	}
}
