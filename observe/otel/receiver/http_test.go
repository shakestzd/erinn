package receiver_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/observe/otel/adapter"
	"github.com/shakestzd/wipnote/observe/otel/receiver"
	sqls "github.com/shakestzd/wipnote/observe/otel/sink/sqlite"

	"google.golang.org/protobuf/proto"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
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

// buildTestReceiver constructs a receiver with a real writer pointed at
// an in-temp DB plus a Claude adapter. The handler is returned so the
// test can drive requests through httptest.
func buildTestReceiver(t *testing.T) (*receiver.HTTPHandler, *receiver.Writer, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "otel-http.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	d.Close()
	w, err := receiver.NewWriter(dbPath)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	reg := adapter.NewRegistry()
	reg.Register(adapter.NewClaudeAdapter())
	h := receiver.NewHTTPHandler(reg, sqls.New(w))
	return h, w, dbPath
}

// TestHTTPHandler_TracesRoundTrip marshals a TracesData proto, posts
// it to /v1/traces via httptest, and asserts the writer persisted the
// expected span rows. Mirrors the wire format produced by Claude Code's
// OTLP/HTTP exporter.
func TestHTTPHandler_TracesRoundTrip(t *testing.T) {
	h, w, _ := buildTestReceiver(t)

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	now := time.Now().UnixNano()
	traces := &tracepb.TracesData{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				kvString("service.name", "claude-code"),
				kvString("service.version", "2.1.42"),
			}},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: "com.anthropic.claude_code"},
				Spans: []*tracepb.Span{{
					TraceId:           bytes.Repeat([]byte{0xab}, 16),
					SpanId:            bytes.Repeat([]byte{0xcd}, 8),
					Name:              "claude_code.interaction",
					StartTimeUnixNano: uint64(now),
					EndTimeUnixNano:   uint64(now + 25_000_000_000),
					Attributes: []*commonpb.KeyValue{
						kvString("session.id", "sess-http-1"),
					},
				}},
			}},
		}},
	}

	body, err := proto.Marshal(traces)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/traces", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// Verify persistence: a span row landed with the session_id and canonical name.
	var count int
	if err := w.DB().QueryRow(
		`SELECT COUNT(*) FROM otel_signals
		 WHERE session_id='sess-http-1' AND kind='span' AND canonical='interaction'`,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("got %d interaction spans, want 1", count)
	}
}

// TestHTTPHandler_LogsRoundTrip exercises the /v1/logs path with an
// api_request event payload that mirrors a real Claude Code emission,
// asserting the writer extracts token/cost/model correctly.
func TestHTTPHandler_LogsRoundTrip(t *testing.T) {
	h, w, _ := buildTestReceiver(t)

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	now := time.Now().UnixNano()
	logs := &logspb.LogsData{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				kvString("service.name", "claude-code"),
			}},
			ScopeLogs: []*logspb.ScopeLogs{{
				Scope: &commonpb.InstrumentationScope{Name: "com.anthropic.claude_code"},
				LogRecords: []*logspb.LogRecord{{
					TimeUnixNano: uint64(now),
					Attributes: []*commonpb.KeyValue{
						kvString("event.name", "api_request"),
						kvString("session.id", "sess-log-1"),
						kvString("prompt.id", "prompt-1"),
						kvString("model", "claude-haiku-4-5-20251001"),
						kvInt("input_tokens", 10),
						kvInt("output_tokens", 577),
						kvInt("cache_read_tokens", 23276),
						kvInt("cache_creation_tokens", 2261),
						kvString("cost_usd", "0.00804885"),
						kvInt("duration_ms", 5835),
					},
				}},
			}},
		}},
	}

	body, _ := proto.Marshal(logs)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/logs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var tokensIn, tokensOut, cacheRead int64
	var cost float64
	if err := w.DB().QueryRow(
		`SELECT tokens_in, tokens_out, tokens_cache_read, cost_usd
		 FROM otel_signals WHERE session_id='sess-log-1' AND canonical='api_request'`,
	).Scan(&tokensIn, &tokensOut, &cacheRead, &cost); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if tokensIn != 10 || tokensOut != 577 || cacheRead != 23276 {
		t.Errorf("tokens = (%d, %d, %d)", tokensIn, tokensOut, cacheRead)
	}
	if cost != 0.00804885 {
		t.Errorf("cost = %v, want 0.00804885", cost)
	}
}

func TestHTTPHandler_RejectsNonPost(t *testing.T) {
	h, _, _ := buildTestReceiver(t)
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/v1/traces")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", resp.StatusCode)
	}
}

func TestHTTPHandler_RejectsBadContentType(t *testing.T) {
	h, _, _ := buildTestReceiver(t)
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/traces", bytes.NewReader([]byte("bogus")))
	req.Header.Set("Content-Type", "text/plain")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad content-type status = %d, want 400", resp.StatusCode)
	}
}

// TestReceiver_DisabledIsNoop proves the opt-in posture: with
// WIPNOTE_OTEL_ENABLED unset, Start returns nil without binding
// a port or opening a DB.
func TestReceiver_DisabledIsNoop(t *testing.T) {
	r, err := receiver.New(receiver.Config{Enabled: false}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Errorf("disabled Start returned %v, want nil", err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Errorf("disabled Stop returned %v", err)
	}
}
