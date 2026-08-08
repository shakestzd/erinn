package receiver

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/shakestzd/wipnote/observe/otel"
	"github.com/shakestzd/wipnote/observe/otel/adapter"
	"github.com/shakestzd/wipnote/observe/otel/otlp"
	"github.com/shakestzd/wipnote/observe/otel/sink"

	// TracesData/MetricsData/LogsData are wire-compatible wrappers with
	// identical field numbers to ExportTraceServiceRequest etc. We use
	// them instead of the collector/*/v1 subpackages, which would pull
	// in gRPC + grpc-gateway dependencies we don't otherwise need.
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// HTTPHandler implements the OTLP/HTTP wire protocol for traces,
// metrics, and logs. One handler instance serves all three paths;
// register with a net/http mux at /v1/traces, /v1/metrics, /v1/logs.
//
// Content-type multiplex:
//
//	application/x-protobuf  → binary (proto.Unmarshal)
//	application/json        → protojson.Unmarshal (handles OTLP/JSON
//	                          hex-encoded trace_id/span_id correctly)
//	anything else           → 415 Unsupported Media Type
//
// The response body is an empty Export*ServiceResponse per the OTel
// spec — partial-success fields are populated when the writer rejects
// some rows (wrong harness, invalid IDs, etc.) but none currently do
// so since we silently drop. Future work: surface drop counts.
type HTTPHandler struct {
	Registry *adapter.Registry
	Sink     sink.SignalSink
	Logger   *log.Logger
}

// NewHTTPHandler constructs a handler with a default stdlib logger.
// Callers wire the registry + sink and mount the three routes.
func NewHTTPHandler(reg *adapter.Registry, s sink.SignalSink) *HTTPHandler {
	return &HTTPHandler{
		Registry: reg,
		Sink:     s,
		Logger:   log.Default(),
	}
}

// Register mounts /v1/traces, /v1/metrics, and /v1/logs on mux.
func (h *HTTPHandler) Register(mux *http.ServeMux) {
	mux.Handle("/v1/traces", h.handlerFor(h.handleTraces))
	mux.Handle("/v1/metrics", h.handlerFor(h.handleMetrics))
	mux.Handle("/v1/logs", h.handlerFor(h.handleLogs))
}

// handlerFor wraps an OTLP signal handler with POST-only enforcement
// and common error paths. Every OTLP endpoint accepts POST only per
// spec.
func (h *HTTPHandler) handlerFor(fn func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		fn(w, r)
	}
}

func (h *HTTPHandler) handleTraces(w http.ResponseWriter, r *http.Request) {
	var req tracepb.TracesData
	if err := h.readProtobufOrJSON(r, &req); err != nil {
		h.Logger.Printf("otel/http: traces decode: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	decoded := otlp.DecodeTraces(req.GetResourceSpans())
	if err := h.persist(r.Context(), decoded); err != nil {
		h.Logger.Printf("otel/http: traces persist: %v", err)
		http.Error(w, "persist failed", http.StatusInternalServerError)
		return
	}
	h.respondEmpty(w)
}

func (h *HTTPHandler) handleMetrics(w http.ResponseWriter, r *http.Request) {
	var req metricspb.MetricsData
	if err := h.readProtobufOrJSON(r, &req); err != nil {
		h.Logger.Printf("otel/http: metrics decode: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	decoded := otlp.DecodeMetrics(req.GetResourceMetrics())
	if err := h.persist(r.Context(), decoded); err != nil {
		h.Logger.Printf("otel/http: metrics persist: %v", err)
		http.Error(w, "persist failed", http.StatusInternalServerError)
		return
	}
	h.respondEmpty(w)
}

func (h *HTTPHandler) handleLogs(w http.ResponseWriter, r *http.Request) {
	var req logspb.LogsData
	if err := h.readProtobufOrJSON(r, &req); err != nil {
		h.Logger.Printf("otel/http: logs decode: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	decoded := otlp.DecodeLogs(req.GetResourceLogs())
	if err := h.persist(r.Context(), decoded); err != nil {
		h.Logger.Printf("otel/http: logs persist: %v", err)
		http.Error(w, "persist failed", http.StatusInternalServerError)
		return
	}
	h.respondEmpty(w)
}

// readProtobufOrJSON reads the request body and decodes it based on
// Content-Type. protojson handles the OTLP/JSON spec deviation where
// trace_id/span_id are hex-encoded strings — stock encoding/json on
// the proto types would reject them.
func (h *HTTPHandler) readProtobufOrJSON(r *http.Request, out proto.Message) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	defer r.Body.Close()

	ct := r.Header.Get("Content-Type")
	switch {
	case ct == "" || ct == "application/x-protobuf":
		return proto.Unmarshal(body, out)
	case ct == "application/json":
		return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(body, out)
	default:
		return fmt.Errorf("unsupported Content-Type: %s", ct)
	}
}

// persist fans each Decoded into the adapter pipeline and writes the
// resulting UnifiedSignals. One Decoded corresponds to one resource;
// the writer batches all signals from that resource in a single tx.
func (h *HTTPHandler) persist(ctx context.Context, decoded []otlp.Decoded) error {
	for _, d := range decoded {
		a := h.Registry.Resolve(d.Resource)
		if a == nil {
			// Unknown harness (service.name). Drop silently — a future
			// observability-of-observability counter could surface this.
			continue
		}
		signals := ConvertAll(a, d)
		if len(signals) == 0 {
			continue
		}
		if err := h.Sink.WriteBatch(ctx, a.Name(), d.Resource.Attrs, signals); err != nil {
			return err
		}
	}
	return nil
}

// ConvertAll runs every signal in the decoded batch through the
// adapter and assigns a stable SignalID. The resulting slice is ready
// for SignalSink.WriteBatch.
func ConvertAll(a adapter.Adapter, d otlp.Decoded) []otel.UnifiedSignal {
	out := make([]otel.UnifiedSignal, 0, len(d.Metrics)+len(d.Logs)+len(d.Spans))

	for _, sm := range d.Metrics {
		for _, sig := range a.ConvertMetric(d.Resource, sm.Scope, sm.Metric) {
			sig.SignalID = DeriveSignalID(d.Resource, sm.Scope, sm.Metric.Name,
				sm.Metric.Timestamp.UnixNano(), sm.Metric.Attrs)
			out = append(out, sig)
		}
	}
	for _, sl := range d.Logs {
		for _, sig := range a.ConvertLog(d.Resource, sl.Scope, sl.Log) {
			sig.SignalID = DeriveSignalID(d.Resource, sl.Scope, sl.Log.Name,
				sl.Log.Timestamp.UnixNano(), sl.Log.Attrs)
			out = append(out, sig)
		}
	}
	for _, ss := range d.Spans {
		for _, sig := range a.ConvertSpan(d.Resource, ss.Scope, ss.Span) {
			sig.SignalID = DeriveSignalID(d.Resource, ss.Scope, ss.Span.Name,
				ss.Span.StartTime.UnixNano(), ss.Span.Attrs)
			out = append(out, sig)
		}
	}
	// bug-a0143c2c: adapters that need to reconcile two signals from the same
	// batch (e.g. Claude's beta tool.execution span vs. its stable
	// tool_result log — see ClaudeAdapter.CrossValidateToolOutcomes) get one
	// pass over the full converted batch here, harness-agnostically via a
	// type assertion so the shared Adapter interface stays untouched for
	// adapters that don't need it.
	if cv, ok := a.(interface {
		CrossValidateToolOutcomes([]otel.UnifiedSignal) []otel.UnifiedSignal
	}); ok {
		out = cv.CrossValidateToolOutcomes(out)
	}
	return out
}

// respondEmpty writes an empty Export*ServiceResponse. Per the OTLP
// spec, an empty body with 200 OK means full success. OTel SDKs
// accept this response shape even when expecting the fully-qualified
// Export*ServiceResponse message because empty bytes decode as a
// message with all fields unset (which is what full success looks like).
func (h *HTTPHandler) respondEmpty(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	// Zero bytes is valid protobuf for a message with no populated fields.
	_, _ = w.Write(nil)
}
