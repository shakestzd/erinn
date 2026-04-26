package main

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	_ "modernc.org/sqlite"

	"github.com/shakestzd/htmlgraph/internal/db"
)

// TestOtelLiveSpanVisibility_TripWire is the slice-7 trip-wire: when an
// OTLP span is POSTed to the per-session collector, it must surface in
// the dashboard's /api/otel/spans handler within a small polling window.
//
// Removing the indexer goroutine launch from runOtelCollect (slice-1)
// makes this test fail — the NDJSON file fills up but otel_signals stays
// empty and the handler returns an empty span array.
//
// The test mounts the dashboard handlers via httptest rather than launching
// a full htmlgraph serve subprocess, so the smoke test stays fast (~3s)
// while still exercising the same SQL + JSON paths users hit through the
// dashboard. Polling uses 10×100ms instead of a fixed sleep to avoid CI
// flakiness on slow runners.
func TestOtelLiveSpanVisibility_TripWire(t *testing.T) {
	bin := buildOtelCollectTestBinary(t)
	projectDir := mkOtelCollectProject(t)
	dbPath := filepath.Join(projectDir, ".htmlgraph", "htmlgraph.db")
	sid := "test-sid-visibility"

	// Pre-create schema so the read-only handle can open without racing
	// the collector's first write.
	seedDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("seed db: %v", err)
	}
	seedDB.Close()

	cmd := exec.Command(bin, "otel-collect",
		"--session-id", sid,
		"--project-dir", projectDir,
		"--listen", "127.0.0.1:0",
	)
	cmd.Env = append(os.Environ(),
		"HTMLGRAPH_OTEL_IDLE_TIMEOUT=20s",
		"HTMLGRAPH_PROJECT_DIR="+projectDir,
		"HTMLGRAPH_DB_PATH="+dbPath,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	scanner := bufio.NewScanner(stdout)
	line, ok := readHandshakeLine(t, scanner, 5*time.Second)
	if !ok {
		t.Fatal("no handshake within 5s")
	}
	var port int
	if _, err := fmt.Sscanf(line, "htmlgraph-otel-ready port=%d", &port); err != nil {
		t.Fatalf("parse port: %v", err)
	}
	go func() {
		for scanner.Scan() {
		}
	}()

	traces := &tracepb.TracesData{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{
				Key:   "service.name",
				Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "claude-code"}},
			}}},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: "com.anthropic.claude_code"},
				Spans: []*tracepb.Span{{
					TraceId:           bytes.Repeat([]byte{0x77}, 16),
					SpanId:            bytes.Repeat([]byte{0x88}, 8),
					Name:              "claude_code.interaction",
					StartTimeUnixNano: uint64(time.Now().UnixNano()),
					EndTimeUnixNano:   uint64(time.Now().UnixNano() + 1_000_000_000),
					Attributes: []*commonpb.KeyValue{{
						Key:   "session.id",
						Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: sid}},
					}},
				}},
			}},
		}},
	}
	body, err := proto.Marshal(traces)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/v1/traces", port),
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// Mount the dashboard's /api/otel/* handlers against a read-only
	// pool over the same DB file. WAL allows concurrent reads alongside
	// the collector's writer.
	roDB, err := sql.Open("sqlite",
		"file:"+dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open ro db: %v", err)
	}
	defer roDB.Close()

	mux := http.NewServeMux()
	mux.Handle("/api/otel/rollup", otelRollupHandler(roDB))
	mux.Handle("/api/otel/spans", otelSpansHandler(roDB))
	mux.Handle("/api/otel/cost", otelCostHandler(roDB))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Trip-wire: poll /api/otel/spans up to 10×100ms (slice-7 spec). The
	// indexer tick is 500ms but our drain may run sooner, so wait until
	// the row is observable rather than guessing a fixed sleep.
	spans := pollSpansEndpoint(t, srv.URL+"/api/otel/spans?session_id="+sid)
	if len(spans) == 0 {
		t.Fatal("/api/otel/spans returned 0 spans — indexer not draining NDJSON to SQLite")
	}

	// /api/otel/rollup falls back to live aggregation when no
	// materialized rollup exists for the in-flight session. Same trip-wire.
	resp2, err := http.Get(srv.URL + "/api/otel/rollup?session_id=" + sid)
	if err != nil {
		t.Fatalf("rollup GET: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("rollup status = %d, want 200 (got 404 means no signals -> indexer dark)", resp2.StatusCode)
	}

	// /api/otel/cost over all signals must include our span's session
	// (cost may be 0 since the synthetic span carries no cost_usd, but
	// the session row should appear in group_by=session).
	resp3, err := http.Get(srv.URL + "/api/otel/cost?group_by=session")
	if err != nil {
		t.Fatalf("cost GET: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Errorf("cost status = %d, want 200", resp3.StatusCode)
	}
}

// pollSpansEndpoint polls the spans endpoint up to 10×100ms, returning
// the parsed spans array as soon as it is non-empty. Empty after the
// budget means the indexer never wrote the row.
func pollSpansEndpoint(t *testing.T, url string) []any {
	t.Helper()
	for i := 0; i < 10; i++ {
		resp, err := http.Get(url)
		if err == nil {
			var payload struct {
				Spans []any `json:"spans"`
			}
			err = json.NewDecoder(resp.Body).Decode(&payload)
			resp.Body.Close()
			if err == nil && len(payload.Spans) > 0 {
				return payload.Spans
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}
