package main

import (
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/shakestzd/htmlgraph/internal/otel/adapter"
	"github.com/shakestzd/htmlgraph/internal/otel/convert"
	"github.com/shakestzd/htmlgraph/internal/otel/otlp"
	"github.com/shakestzd/htmlgraph/internal/otel/sink/ndjson"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// collectorHandler is a minimal OTLP/HTTP handler for the per-session collector.
// It decodes OTLP requests, converts via the adapter pipeline, and writes to NDJSON.
type collectorHandler struct {
	registry     *adapter.Registry
	sink         *ndjson.Sink
	lastActivity *atomic.Int64
}

func (h *collectorHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	h.lastActivity.Store(time.Now().UnixMilli())

	const maxBodySize = 4 * 1024 * 1024 // 4MB
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var decoded []otlp.Decoded
	switch r.URL.Path {
	case "/v1/traces":
		decoded, err = h.decodeTraces(body, r.Header.Get("Content-Type"))
	case "/v1/metrics":
		decoded, err = h.decodeMetrics(body, r.Header.Get("Content-Type"))
	case "/v1/logs":
		decoded, err = h.decodeLogs(body, r.Header.Get("Content-Type"))
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.persist(r, decoded); err != nil {
		http.Error(w, "persist failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(nil)
}

func (h *collectorHandler) persist(r *http.Request, decoded []otlp.Decoded) error {
	for _, d := range decoded {
		a := h.registry.Resolve(d.Resource)
		if a == nil {
			continue
		}
		signals := convert.ConvertAll(a, d)
		if len(signals) == 0 {
			continue
		}
		if err := h.sink.WriteBatch(r.Context(), a.Name(), d.Resource.Attrs, signals); err != nil {
			return err
		}
	}
	return nil
}

func (h *collectorHandler) decodeTraces(body []byte, ct string) ([]otlp.Decoded, error) {
	var req tracepb.TracesData
	if err := unmarshalOTLP(body, ct, &req); err != nil {
		return nil, err
	}
	return otlp.DecodeTraces(req.GetResourceSpans()), nil
}

func (h *collectorHandler) decodeMetrics(body []byte, ct string) ([]otlp.Decoded, error) {
	var req metricspb.MetricsData
	if err := unmarshalOTLP(body, ct, &req); err != nil {
		return nil, err
	}
	return otlp.DecodeMetrics(req.GetResourceMetrics()), nil
}

func (h *collectorHandler) decodeLogs(body []byte, ct string) ([]otlp.Decoded, error) {
	var req logspb.LogsData
	if err := unmarshalOTLP(body, ct, &req); err != nil {
		return nil, err
	}
	return otlp.DecodeLogs(req.GetResourceLogs()), nil
}

func unmarshalOTLP(body []byte, ct string, out proto.Message) error {
	switch {
	case ct == "" || ct == "application/x-protobuf":
		return proto.Unmarshal(body, out)
	case ct == "application/json":
		return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(body, out)
	default:
		return fmt.Errorf("unsupported Content-Type: %s", ct)
	}
}
