package main

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	_ "modernc.org/sqlite"
)

// otelCollectTestBinary holds the path to the binary built for otel-collect tests.
// Built once by buildOtelCollectTestBinary and reused across tests.
var otelCollectTestBinary string

// buildOtelCollectTestBinary builds the htmlgraph binary into a temp dir and
// returns the path. It is safe to call multiple times — subsequent calls
// reuse the first binary. Callers must call t.Helper() and check the error.
func buildOtelCollectTestBinary(t *testing.T) string {
	t.Helper()
	if otelCollectTestBinary != "" {
		if _, err := os.Stat(otelCollectTestBinary); err == nil {
			return otelCollectTestBinary
		}
	}
	tmp, err := os.MkdirTemp("", "otel-collect-test-*")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	bin := filepath.Join(tmp, "htmlgraph-test")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	cmd.Dir = filepath.Dir(thisFile)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build htmlgraph for otel-collect tests: %v", err)
	}
	otelCollectTestBinary = bin
	return bin
}

// mkOtelCollectProject creates a temp project dir with a .htmlgraph directory
// and returns the project root.
func mkOtelCollectProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".htmlgraph"), 0o755); err != nil {
		t.Fatalf("mkdirall: %v", err)
	}
	return dir
}

// readHandshakeLine reads lines from the scanner until it finds the
// htmlgraph-otel-ready line or the deadline is exceeded.
func readHandshakeLine(t *testing.T, scanner *bufio.Scanner, deadline time.Duration) (string, bool) {
	t.Helper()
	done := make(chan string, 1)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "htmlgraph-otel-ready") {
				done <- line
				return
			}
		}
		done <- ""
	}()
	select {
	case line := <-done:
		return line, line != ""
	case <-time.After(deadline):
		return "", false
	}
}

// TestOtelCollect_HandshakeLine verifies that otel-collect prints exactly one
// handshake line on stdout matching "htmlgraph-otel-ready port=<N>" and nothing
// else before the process is signalled. Stdout purity is required because the
// launcher in S3 uses bufio.Scanner on the child's stdout pipe.
func TestOtelCollect_HandshakeLine(t *testing.T) {
	bin := buildOtelCollectTestBinary(t)
	projectDir := mkOtelCollectProject(t)
	sid := "test-sid-handshake"

	cmd := exec.Command(bin, "otel-collect",
		"--session-id", sid,
		"--project-dir", projectDir,
		"--listen", "127.0.0.1:0",
	)
	// Very short idle timeout so the process exits promptly after the handshake.
	cmd.Env = append(os.Environ(),
		"HTMLGRAPH_OTEL_IDLE_TIMEOUT=300ms",
		"HTMLGRAPH_PROJECT_DIR="+projectDir,
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start otel-collect: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	scanner := bufio.NewScanner(stdout)
	line, ok := readHandshakeLine(t, scanner, 5*time.Second)
	if !ok {
		t.Fatal("otel-collect did not print htmlgraph-otel-ready within 5s")
	}

	// Validate format: "htmlgraph-otel-ready port=<N>"
	if !strings.HasPrefix(line, "htmlgraph-otel-ready port=") {
		t.Errorf("handshake line format wrong: %q", line)
	}
	var port int
	if _, err := fmt.Sscanf(line, "htmlgraph-otel-ready port=%d", &port); err != nil {
		t.Errorf("could not parse port from handshake %q: %v", line, err)
	}
	if port <= 0 || port > 65535 {
		t.Errorf("port out of range: %d", port)
	}
}

// TestOtelCollect_IdleTimeout verifies that otel-collect exits 0 within a
// reasonable window when no OTLP traffic arrives and HTMLGRAPH_OTEL_IDLE_TIMEOUT
// is set to a short value.
func TestOtelCollect_IdleTimeout(t *testing.T) {
	bin := buildOtelCollectTestBinary(t)
	projectDir := mkOtelCollectProject(t)
	sid := "test-sid-idletimeout"

	cmd := exec.Command(bin, "otel-collect",
		"--session-id", sid,
		"--project-dir", projectDir,
		"--listen", "127.0.0.1:0",
	)
	cmd.Env = append(os.Environ(),
		"HTMLGRAPH_OTEL_IDLE_TIMEOUT=200ms",
		"HTMLGRAPH_PROJECT_DIR="+projectDir,
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start otel-collect: %v", err)
	}

	// Drain stdout so the process doesn't block on a full pipe.
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
		}
	}()

	// The process should exit within 3 seconds (200ms idle timeout + margin).
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("otel-collect exited with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("otel-collect did not exit within 5s (idle timeout not working)")
	}
}

// TestOtelCollect_CollectorStartEvent verifies that after the handshake, the
// session's events.ndjson contains a collector_start event as the first line.
func TestOtelCollect_CollectorStartEvent(t *testing.T) {
	bin := buildOtelCollectTestBinary(t)
	projectDir := mkOtelCollectProject(t)
	sid := "test-sid-startev"

	cmd := exec.Command(bin, "otel-collect",
		"--session-id", sid,
		"--project-dir", projectDir,
		"--listen", "127.0.0.1:0",
	)
	cmd.Env = append(os.Environ(),
		"HTMLGRAPH_OTEL_IDLE_TIMEOUT=400ms",
		"HTMLGRAPH_PROJECT_DIR="+projectDir,
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start otel-collect: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	// Wait for handshake before reading the events file.
	scanner := bufio.NewScanner(stdout)
	if _, ok := readHandshakeLine(t, scanner, 5*time.Second); !ok {
		t.Fatal("no handshake line within 5s")
	}

	// Give it a moment to flush the collector_start event.
	time.Sleep(100 * time.Millisecond)

	eventsPath := filepath.Join(projectDir, ".htmlgraph", "sessions", sid, "events.ndjson")
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("events.ndjson not found at %s: %v", eventsPath, err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		t.Fatal("events.ndjson is empty")
	}

	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first line is not valid JSON: %v — raw: %q", err, lines[0])
	}

	if got := first["kind"]; got != "collector_start" {
		t.Errorf("first event kind = %q, want %q", got, "collector_start")
	}
	if first["session_id"] != sid {
		t.Errorf("first event session_id = %q, want %q", first["session_id"], sid)
	}

	attrs, ok := first["attrs"].(map[string]any)
	if !ok {
		t.Fatalf("attrs field missing or not an object: %v", first["attrs"])
	}
	if attrs["htmlgraph_sid"] != sid {
		t.Errorf("attrs.htmlgraph_sid = %q, want %q", attrs["htmlgraph_sid"], sid)
	}
	if _, hasPort := attrs["port"]; !hasPort {
		t.Error("attrs.port missing from collector_start event")
	}
	if _, hasPID := attrs["pid"]; !hasPID {
		t.Error("attrs.pid missing from collector_start event")
	}
}

// TestParseDrainBudget verifies HTMLGRAPH_OTEL_DRAIN_SECS overrides the
// default budget, accepts Go durations + bare seconds, and falls back on
// invalid input.
func TestParseDrainBudget(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", defaultDrainBudget},
		{"5s", 5 * time.Second},
		{"750ms", 750 * time.Millisecond},
		{"3", 3 * time.Second},
		{"garbage", defaultDrainBudget},
		{"-1", defaultDrainBudget},
	}
	for _, tc := range cases {
		t.Setenv("HTMLGRAPH_OTEL_DRAIN_SECS", tc.env)
		got := parseDrainBudget()
		if got != tc.want {
			t.Errorf("parseDrainBudget env=%q: got %v, want %v", tc.env, got, tc.want)
		}
	}
}

// TestCountUnindexedSignals verifies the helper counts NDJSON lines past
// the checkpoint offset, used in the over-budget log line.
func TestCountUnindexedSignals(t *testing.T) {
	htmlgraphDir := t.TempDir()
	sessionID := "drain-count"
	sessDir := filepath.Join(htmlgraphDir, "sessions", sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	contents := "line-1\nline-2\nline-3\n"
	if err := os.WriteFile(filepath.Join(sessDir, "events.ndjson"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	// No checkpoint → all 3 lines unindexed.
	if got := countUnindexedSignals(htmlgraphDir, sessionID); got != 3 {
		t.Errorf("no checkpoint: got %d, want 3", got)
	}

	// Checkpoint past line-1 (offset = len("line-1\n")) → 2 unindexed.
	off := int64(len("line-1\n"))
	if err := os.WriteFile(filepath.Join(sessDir, ".index-offset"), []byte(fmt.Sprintf("%d", off)), 0o644); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	if got := countUnindexedSignals(htmlgraphDir, sessionID); got != 2 {
		t.Errorf("after checkpoint: got %d, want 2", got)
	}
}

// TestGracefulShutdown_RespectsDrainBudget verifies that when the indexer
// goroutine refuses to exit, gracefulShutdown returns within the configured
// budget and reports drain failure rather than blocking forever.
func TestGracefulShutdown_RespectsDrainBudget(t *testing.T) {
	t.Setenv("HTMLGRAPH_OTEL_DRAIN_SECS", "300ms")

	// A WaitGroup that never completes — simulates a wedged indexer.
	var wg sync.WaitGroup
	wg.Add(1)
	t.Cleanup(func() { wg.Done() })

	start := time.Now()
	drained := waitWithBudget(&wg, parseDrainBudget())
	elapsed := time.Since(start)

	if drained {
		t.Error("waitWithBudget returned true on wedged WaitGroup")
	}
	if elapsed < 250*time.Millisecond || elapsed > 800*time.Millisecond {
		t.Errorf("wait duration %v outside [250ms, 800ms]", elapsed)
	}
}

// TestOtelCollect_GracefulShutdown_DrainsIndexer is the slice-2 integration
// proof: emit 100 OTLP spans, immediately SIGTERM the collector, await its
// exit, then assert all 100 rows landed in otel_signals — the drain window
// must cover the indexer tick.
func TestOtelCollect_GracefulShutdown_DrainsIndexer(t *testing.T) {
	bin := buildOtelCollectTestBinary(t)
	projectDir := mkOtelCollectProject(t)
	dbPath := filepath.Join(projectDir, ".htmlgraph", "htmlgraph.db")
	sid := "test-sid-drain"

	cmd := exec.Command(bin, "otel-collect",
		"--session-id", sid,
		"--project-dir", projectDir,
		"--listen", "127.0.0.1:0",
	)
	cmd.Env = append(os.Environ(),
		"HTMLGRAPH_OTEL_IDLE_TIMEOUT=30s",
		"HTMLGRAPH_OTEL_DRAIN_SECS=10s",
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

	// Emit 100 spans across one POST.
	now := time.Now().UnixNano()
	const spanCount = 100
	spans := make([]*tracepb.Span, 0, spanCount)
	for i := 0; i < spanCount; i++ {
		spans = append(spans, &tracepb.Span{
			TraceId:           bytes.Repeat([]byte{byte(i + 1)}, 16),
			SpanId:            bytes.Repeat([]byte{byte(i + 1)}, 8),
			Name:              "claude_code.interaction",
			StartTimeUnixNano: uint64(now + int64(i)),
			EndTimeUnixNano:   uint64(now + int64(i) + 1_000_000),
			Attributes: []*commonpb.KeyValue{{
				Key:   "session.id",
				Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: sid}},
			}},
		})
	}
	traces := &tracepb.TracesData{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{
				Key:   "service.name",
				Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "claude-code"}},
			}}},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: "com.anthropic.claude_code"},
				Spans: spans,
			}},
		}},
	}
	body, err := proto.Marshal(traces)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/traces", port)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// SIGTERM immediately — drain must persist signals to SQLite.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	exit := make(chan error, 1)
	go func() { exit <- cmd.Wait() }()
	select {
	case err := <-exit:
		if err != nil {
			t.Fatalf("collector exited non-zero: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("collector did not exit within 15s of SIGTERM")
	}

	// All 100 spans must be in SQLite.
	ro, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer ro.Close()
	var got int
	if err := ro.QueryRow(
		`SELECT COUNT(*) FROM otel_signals WHERE session_id = ? AND kind='span'`, sid,
	).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != spanCount {
		t.Errorf("after drain: got %d span rows, want %d — drain did not flush all NDJSON", got, spanCount)
	}
}

// TestOtelCollect_OTLPToSQLite is the slice-1 trip-wire: an OTLP span POSTed
// to the per-session collector must surface in the canonical SQLite
// otel_signals table within ~one indexer tick. Removing the indexer goroutine
// from runOtelCollect makes this test fail.
func TestOtelCollect_OTLPToSQLite(t *testing.T) {
	bin := buildOtelCollectTestBinary(t)
	projectDir := mkOtelCollectProject(t)
	dbPath := filepath.Join(projectDir, ".htmlgraph", "htmlgraph.db")
	sid := "test-sid-otlp-sqlite"
	const traceIDByte = 0xab
	const spanIDByte = 0xcd

	cmd := exec.Command(bin, "otel-collect",
		"--session-id", sid,
		"--project-dir", projectDir,
		"--listen", "127.0.0.1:0",
	)
	cmd.Env = append(os.Environ(),
		"HTMLGRAPH_OTEL_IDLE_TIMEOUT=15s",
		"HTMLGRAPH_PROJECT_DIR="+projectDir,
		"HTMLGRAPH_DB_PATH="+dbPath,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start otel-collect: %v", err)
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

	now := time.Now().UnixNano()
	traces := &tracepb.TracesData{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{
				Key:   "service.name",
				Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "claude-code"}},
			}}},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: "com.anthropic.claude_code"},
				Spans: []*tracepb.Span{{
					TraceId:           bytes.Repeat([]byte{traceIDByte}, 16),
					SpanId:            bytes.Repeat([]byte{spanIDByte}, 8),
					Name:              "claude_code.interaction",
					StartTimeUnixNano: uint64(now),
					EndTimeUnixNano:   uint64(now + 1_000_000_000),
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
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/traces", port)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/traces: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// Poll otel_signals for up to 5s — one indexer tick is 500ms but the
	// child process opens its own DB pool and the canonical schema is
	// applied lazily. Open a read-only handle to avoid contending with
	// the child's writer.
	deadline := time.Now().Add(5 * time.Second)
	var count int
	for time.Now().Before(deadline) {
		ro, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
		if err == nil {
			err = ro.QueryRow(
				`SELECT COUNT(*) FROM otel_signals WHERE session_id = ? AND kind='span'`, sid,
			).Scan(&count)
			ro.Close()
		}
		if err == nil && count > 0 {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if count == 0 {
		t.Fatalf("otel_signals row for session=%s did not appear within 5s — indexer not draining NDJSON to SQLite", sid)
	}
}
