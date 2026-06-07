package receiver_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/internal/otel/receiver"
	sqls "github.com/shakestzd/wipnote/internal/otel/sink/sqlite"

	"google.golang.org/protobuf/proto"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// TestReceiver_EndToEndLifecycle exercises the full receiver lifecycle:
// New → Start (bind ephemeral port) → POST signals over real HTTP →
// assert persistence → Stop. Proves the Config.HTTPPort listener path,
// not just the mux-mounted path exercised by http_test.go.
func TestReceiver_EndToEndLifecycle(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "otel-e2e.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	d.Close()

	// Reserve an ephemeral port by briefly binding then closing.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("port reserve: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	w, err := receiver.NewWriter(dbPath)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	rec, err := receiver.New(receiver.Config{
		Enabled:  true,
		BindHost: "127.0.0.1",
		HTTPPort: port,
	}, sqls.New(w))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := rec.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { rec.Stop(context.Background()) })

	// Wait for the listener to be accepting connections.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond); err == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Post a real Claude api_request log event.
	body := makeClaudeLogPayload(t, "sess-e2e", "prompt-e2e",
		"claude-haiku-4-5-20251001", 10, 577, 23276, 2261, "0.00804885")
	req, _ := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/v1/logs", port),
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}

	var cost float64
	err = w.DB().QueryRow(
		`SELECT cost_usd FROM otel_signals WHERE session_id='sess-e2e'`,
	).Scan(&cost)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if cost != 0.00804885 {
		t.Errorf("cost = %v, want 0.00804885", cost)
	}
}

// TestReceiver_Burst verifies the writer handles a sustained burst of
// concurrent OTLP requests without SQLITE_BUSY errors. Uses 5 parallel
// clients posting 40 logs each (200 total) — this is the lower end of
// Phase 1's stated throughput target and catches any regression in the
// MaxOpenConns=1 + BEGIN IMMEDIATE contract.
func TestReceiver_Burst(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping burst test in short mode")
	}
	dbPath := filepath.Join(t.TempDir(), "otel-burst.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	d.Close()

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	w, err := receiver.NewWriter(dbPath)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	rec, err := receiver.New(receiver.Config{
		Enabled: true, BindHost: "127.0.0.1", HTTPPort: port,
	}, sqls.New(w))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec.Start(context.Background())
	t.Cleanup(func() { rec.Stop(context.Background()) })
	time.Sleep(100 * time.Millisecond) // listener warmup

	const clients = 5
	const perClient = 40
	client := &http.Client{Timeout: 10 * time.Second}
	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for c := 0; c < clients; c++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()
			for i := 0; i < perClient; i++ {
				// Unique session/prompt per (client, i) so rows don't dedup.
				session := fmt.Sprintf("sess-burst-%d-%d", clientID, i)
				body := makeClaudeLogPayload(t, session, "p",
					"claude-haiku-4-5-20251001", 10, 10, 0, 0, "0.00001")
				req, _ := http.NewRequest(http.MethodPost,
					fmt.Sprintf("http://127.0.0.1:%d/v1/logs", port),
					bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/x-protobuf")
				resp, err := client.Do(req)
				if err != nil {
					errs <- err
					return
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					errs <- fmt.Errorf("status %d", resp.StatusCode)
					return
				}
			}
		}(c)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("burst client error: %v", e)
	}

	var count int
	if err := w.DB().QueryRow(
		`SELECT COUNT(*) FROM otel_signals WHERE session_id LIKE 'sess-burst-%'`,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != clients*perClient {
		t.Errorf("inserted = %d, want %d", count, clients*perClient)
	}
}

// TestLoadConfigFromEnv walks the env-var surface to ensure defaults
// and overrides behave as documented.
func TestLoadConfigFromEnv(t *testing.T) {
	// Default: enabled (default-on semantics).
	t.Setenv("WIPNOTE_OTEL_ENABLED", "")
	t.Setenv("WIPNOTE_OTEL_HTTP_PORT", "")
	t.Setenv("WIPNOTE_OTEL_BIND", "")
	t.Setenv("WIPNOTE_PROJECT_DIR", "")
	cfg := receiver.LoadConfigFromEnv("/tmp/x", "")
	if !cfg.Enabled {
		t.Error("default Enabled should be true (default-on)")
	}
	if cfg.HTTPPort != 4318 {
		t.Errorf("default HTTPPort (no project dir) = %d, want 4318", cfg.HTTPPort)
	}
	if cfg.BindHost != "127.0.0.1" {
		t.Errorf("default BindHost = %q", cfg.BindHost)
	}

	// Explicit WIPNOTE_OTEL_ENABLED=0 → disabled.
	t.Setenv("WIPNOTE_OTEL_ENABLED", "0")
	cfg = receiver.LoadConfigFromEnv("/tmp/x", "")
	if cfg.Enabled {
		t.Error("WIPNOTE_OTEL_ENABLED=0 should set Enabled=false")
	}

	// Custom port via env var wins over project hash.
	t.Setenv("WIPNOTE_OTEL_ENABLED", "1")
	t.Setenv("WIPNOTE_OTEL_HTTP_PORT", "14318")
	t.Setenv("WIPNOTE_OTEL_BIND", "0.0.0.0")
	cfg = receiver.LoadConfigFromEnv("/tmp/x", "/some/project")
	if !cfg.Enabled || cfg.HTTPPort != 14318 || cfg.BindHost != "0.0.0.0" {
		t.Errorf("envs not applied: %+v", cfg)
	}
}

// TestLoadConfigFromEnv_DefaultPort verifies that when no explicit port env is
// set, LoadConfigFromEnv returns the OTel default port 4318. The per-project
// hash-based port derivation has been removed — per-session collectors use
// ephemeral ports.
func TestLoadConfigFromEnv_DefaultPort(t *testing.T) {
	t.Setenv("WIPNOTE_OTEL_HTTP_PORT", "")
	t.Setenv("WIPNOTE_OTEL_ENABLED", "")
	t.Setenv("WIPNOTE_OTEL_BIND", "")

	cfg := receiver.LoadConfigFromEnv("", "/home/user/project-alpha")
	if cfg.HTTPPort != 4318 {
		t.Errorf("default port should be 4318, got %d", cfg.HTTPPort)
	}
}

// TestLoadConfigFromEnv_EnvOverride verifies WIPNOTE_OTEL_HTTP_PORT wins
// over the default port.
func TestLoadConfigFromEnv_EnvOverride(t *testing.T) {
	t.Setenv("WIPNOTE_OTEL_HTTP_PORT", "5000")
	t.Setenv("WIPNOTE_OTEL_ENABLED", "")

	cfg := receiver.LoadConfigFromEnv("", "/home/user/any-project")
	if cfg.HTTPPort != 5000 {
		t.Errorf("WIPNOTE_OTEL_HTTP_PORT=5000 should win, got %d", cfg.HTTPPort)
	}

	t.Cleanup(func() { t.Setenv("WIPNOTE_OTEL_HTTP_PORT", "") })
}

// makeClaudeLogPayload builds a marshalled LogsData byte slice that
// mirrors a Claude Code api_request log event.
func makeClaudeLogPayload(t *testing.T,
	session, prompt, model string,
	input, output, cacheRead, cacheCreation int64,
	costUSD string,
) []byte {
	t.Helper()
	kv := func(k, v string) *commonpb.KeyValue {
		return &commonpb.KeyValue{Key: k,
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
	}
	kvi := func(k string, v int64) *commonpb.KeyValue {
		return &commonpb.KeyValue{Key: k,
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
	}
	logs := &logspb.LogsData{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				kv("service.name", "claude-code"),
			}},
			ScopeLogs: []*logspb.ScopeLogs{{
				Scope: &commonpb.InstrumentationScope{Name: "com.anthropic.claude_code"},
				LogRecords: []*logspb.LogRecord{{
					TimeUnixNano: uint64(time.Now().UnixNano()),
					Attributes: []*commonpb.KeyValue{
						kv("event.name", "api_request"),
						kv("session.id", session),
						kv("prompt.id", prompt),
						kv("model", model),
						kvi("input_tokens", input),
						kvi("output_tokens", output),
						kvi("cache_read_tokens", cacheRead),
						kvi("cache_creation_tokens", cacheCreation),
						kv("cost_usd", costUSD),
					},
				}},
			}},
		}},
	}
	b, err := proto.Marshal(logs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
