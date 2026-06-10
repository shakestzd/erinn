package otlp

import (
	"fmt"
	"time"

	"github.com/shakestzd/wipnote/observe/otel/adapter"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// Decoded carries the intermediate-type output of a single OTLP request
// after it's been converted from protobuf-land to adapter-friendly Go.
// The receiver builds one Decoded per HTTP request and hands it off to
// the adapter pipeline in one call.
type Decoded struct {
	Spans    []ScopedSpan
	Metrics  []ScopedMetric
	Logs     []ScopedLog
	Resource adapter.OTLPResource // resource is shared across all scopes in a request
}

// ScopedSpan / ScopedMetric / ScopedLog carry the instrumentation scope
// alongside the signal so adapters can use scope.name (e.g.
// "com.anthropic.claude_code") without rederiving it.
type ScopedSpan struct {
	Scope adapter.OTLPScope
	Span  adapter.OTLPSpan
}

type ScopedMetric struct {
	Scope  adapter.OTLPScope
	Metric adapter.OTLPMetric
}

type ScopedLog struct {
	Scope adapter.OTLPScope
	Log   adapter.OTLPLog
}

// DecodeTraces converts an OTLP ExportTraceServiceRequest payload into
// intermediate spans. One Decoded per resource — OTLP requests may carry
// multiple ResourceSpans, but wipnote flattens to one since we key
// persistence off the resource's service.name.
//
// Invalid trace/span IDs (zero bytes or wrong length) are logged by the
// caller; this function drops them silently to keep the decode path
// fast. Invariant: every span in the output satisfies ValidTraceID and
// ValidSpanID.
func DecodeTraces(resourceSpans []*tracepb.ResourceSpans) []Decoded {
	out := make([]Decoded, 0, len(resourceSpans))
	for _, rs := range resourceSpans {
		d := Decoded{Resource: decodeResource(rs.GetResource())}
		for _, ss := range rs.GetScopeSpans() {
			scope := decodeScope(ss.GetScope())
			for _, s := range ss.GetSpans() {
				if !ValidTraceID(s.GetTraceId()) || !ValidSpanID(s.GetSpanId()) {
					continue
				}
				d.Spans = append(d.Spans, ScopedSpan{
					Scope: scope,
					Span:  decodeSpan(s),
				})
			}
		}
		out = append(out, d)
	}
	return out
}

// DecodeMetrics converts an OTLP ExportMetricsServiceRequest payload.
// Sum/Gauge/Histogram points are fanned out into per-point OTLPMetric
// values so adapters see one call per (metric, attribute-set). This
// matches how Claude Code emits claude_code.token.usage (one data point
// per type=input|output|cacheRead|cacheCreation).
func DecodeMetrics(resourceMetrics []*metricspb.ResourceMetrics) []Decoded {
	out := make([]Decoded, 0, len(resourceMetrics))
	for _, rm := range resourceMetrics {
		d := Decoded{Resource: decodeResource(rm.GetResource())}
		for _, sm := range rm.GetScopeMetrics() {
			scope := decodeScope(sm.GetScope())
			for _, m := range sm.GetMetrics() {
				fanoutMetric(&d, scope, m)
			}
		}
		out = append(out, d)
	}
	return out
}

// DecodeLogs converts an OTLP ExportLogsServiceRequest payload.
func DecodeLogs(resourceLogs []*logspb.ResourceLogs) []Decoded {
	out := make([]Decoded, 0, len(resourceLogs))
	for _, rl := range resourceLogs {
		d := Decoded{Resource: decodeResource(rl.GetResource())}
		for _, sl := range rl.GetScopeLogs() {
			scope := decodeScope(sl.GetScope())
			for _, l := range sl.GetLogRecords() {
				d.Logs = append(d.Logs, ScopedLog{
					Scope: scope,
					Log:   decodeLog(l),
				})
			}
		}
		out = append(out, d)
	}
	return out
}

func decodeResource(r *resourcepb.Resource) adapter.OTLPResource {
	if r == nil {
		return adapter.OTLPResource{Attrs: map[string]any{}}
	}
	return adapter.OTLPResource{Attrs: flattenKVs(r.GetAttributes())}
}

func decodeScope(s *commonpb.InstrumentationScope) adapter.OTLPScope {
	if s == nil {
		return adapter.OTLPScope{}
	}
	return adapter.OTLPScope{
		Name:    s.GetName(),
		Version: s.GetVersion(),
		Attrs:   flattenKVs(s.GetAttributes()),
	}
}

func decodeSpan(s *tracepb.Span) adapter.OTLPSpan {
	status := s.GetStatus()
	var statusCode int32
	var statusMsg string
	if status != nil {
		statusCode = int32(status.GetCode())
		statusMsg = status.GetMessage()
	}
	events := s.GetEvents()
	evs := make([]adapter.OTLPSpanEvent, 0, len(events))
	for _, e := range events {
		evs = append(evs, adapter.OTLPSpanEvent{
			Name:      e.GetName(),
			Timestamp: time.Unix(0, int64(e.GetTimeUnixNano())),
			Attrs:     flattenKVs(e.GetAttributes()),
		})
	}
	return adapter.OTLPSpan{
		Name:         s.GetName(),
		TraceID:      HexEncodeID(s.GetTraceId()),
		SpanID:       HexEncodeID(s.GetSpanId()),
		ParentSpanID: HexEncodeID(s.GetParentSpanId()),
		Kind:         int32(s.GetKind()),
		StartTime:    time.Unix(0, int64(s.GetStartTimeUnixNano())),
		EndTime:      time.Unix(0, int64(s.GetEndTimeUnixNano())),
		StatusCode:   statusCode,
		StatusMsg:    statusMsg,
		Attrs:        flattenKVs(s.GetAttributes()),
		Events:       evs,
	}
}

func decodeLog(l *logspb.LogRecord) adapter.OTLPLog {
	return adapter.OTLPLog{
		Name:           extractLogName(l),
		Timestamp:      time.Unix(0, int64(l.GetTimeUnixNano())),
		ObservedTime:   time.Unix(0, int64(l.GetObservedTimeUnixNano())),
		SeverityNumber: int32(l.GetSeverityNumber()),
		SeverityText:   l.GetSeverityText(),
		Body:           decodeAnyValue(l.GetBody()),
		Attrs:          flattenKVs(l.GetAttributes()),
		TraceID:        HexEncodeID(l.GetTraceId()),
		SpanID:         HexEncodeID(l.GetSpanId()),
	}
}

// extractLogName pulls the event name. Claude Code encodes the name as
// an attribute "event.name" per the OTel Events SDK; the proto LogRecord
// has a direct EventName field only in the newer spec. We fall back to
// that if event.name is absent.
func extractLogName(l *logspb.LogRecord) string {
	if name := l.GetEventName(); name != "" {
		return name
	}
	for _, kv := range l.GetAttributes() {
		if kv.GetKey() == "event.name" {
			if s, ok := kv.GetValue().GetValue().(*commonpb.AnyValue_StringValue); ok {
				return s.StringValue
			}
		}
	}
	return ""
}

// fanoutMetric dispatches the metric's data points into per-point
// intermediate metrics. Only the Point-type dimensions that wipnote
// cares about are fanned out; OTel's summary-of-summaries aggregations
// are currently ignored.
func fanoutMetric(d *Decoded, scope adapter.OTLPScope, m *metricspb.Metric) {
	name := m.GetName()
	unit := m.GetUnit()
	switch data := m.GetData().(type) {
	case *metricspb.Metric_Sum:
		for _, dp := range data.Sum.GetDataPoints() {
			d.Metrics = append(d.Metrics, ScopedMetric{
				Scope: scope,
				Metric: adapter.OTLPMetric{
					Name:      name,
					Kind:      adapter.MetricKindCounter,
					Unit:      unit,
					Timestamp: time.Unix(0, int64(dp.GetTimeUnixNano())),
					StartTime: time.Unix(0, int64(dp.GetStartTimeUnixNano())),
					Value:     numberValue(dp),
					Attrs:     flattenKVs(dp.GetAttributes()),
				},
			})
		}
	case *metricspb.Metric_Gauge:
		for _, dp := range data.Gauge.GetDataPoints() {
			d.Metrics = append(d.Metrics, ScopedMetric{
				Scope: scope,
				Metric: adapter.OTLPMetric{
					Name:      name,
					Kind:      adapter.MetricKindGauge,
					Unit:      unit,
					Timestamp: time.Unix(0, int64(dp.GetTimeUnixNano())),
					StartTime: time.Unix(0, int64(dp.GetStartTimeUnixNano())),
					Value:     numberValue(dp),
					Attrs:     flattenKVs(dp.GetAttributes()),
				},
			})
		}
	case *metricspb.Metric_Histogram:
		for _, dp := range data.Histogram.GetDataPoints() {
			d.Metrics = append(d.Metrics, ScopedMetric{
				Scope: scope,
				Metric: adapter.OTLPMetric{
					Name:      name,
					Kind:      adapter.MetricKindHistogram,
					Unit:      unit,
					Timestamp: time.Unix(0, int64(dp.GetTimeUnixNano())),
					StartTime: time.Unix(0, int64(dp.GetStartTimeUnixNano())),
					Value:     dp.GetSum(),
					Count:     dp.GetCount(),
					Attrs:     flattenKVs(dp.GetAttributes()),
				},
			})
		}
	}
}

func numberValue(dp *metricspb.NumberDataPoint) float64 {
	if dp == nil {
		return 0
	}
	switch v := dp.GetValue().(type) {
	case *metricspb.NumberDataPoint_AsDouble:
		return v.AsDouble
	case *metricspb.NumberDataPoint_AsInt:
		return float64(v.AsInt)
	}
	return 0
}

// flattenKVs converts a repeated KeyValue slice into the flat
// map[string]any adapters consume.
func flattenKVs(kvs []*commonpb.KeyValue) map[string]any {
	if len(kvs) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		out[kv.GetKey()] = decodeAnyValue(kv.GetValue())
	}
	return out
}

// decodeAnyValue converts OTel AnyValue oneof into a Go-native type.
// We preserve int/float/bool/string distinction because adapter helpers
// (AttrInt64, AttrFloat64) use type assertions. Nested arrays and
// key-value lists flatten recursively.
func decodeAnyValue(v *commonpb.AnyValue) any {
	if v == nil {
		return nil
	}
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_BoolValue:
		return x.BoolValue
	case *commonpb.AnyValue_IntValue:
		return x.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return x.DoubleValue
	case *commonpb.AnyValue_BytesValue:
		return x.BytesValue
	case *commonpb.AnyValue_ArrayValue:
		arr := x.ArrayValue.GetValues()
		out := make([]any, len(arr))
		for i, item := range arr {
			out[i] = decodeAnyValue(item)
		}
		return out
	case *commonpb.AnyValue_KvlistValue:
		return flattenKVs(x.KvlistValue.GetValues())
	}
	return nil
}

// Validate is a sanity check used by tests and the receiver's request
// handler to reject obviously-malformed payloads before they hit the
// writer.
func Validate(d Decoded) error {
	if d.Resource.Attrs == nil {
		return fmt.Errorf("nil resource attributes")
	}
	return nil
}
