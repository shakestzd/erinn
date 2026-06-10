package sink_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/observe/otel"
	"github.com/shakestzd/wipnote/observe/otel/sink"
	ndj "github.com/shakestzd/wipnote/observe/otel/sink/ndjson"
	sqls "github.com/shakestzd/wipnote/observe/otel/sink/sqlite"
)

func newTestSignals(sessionID string) []otel.UnifiedSignal {
	t := time.Date(2026, 4, 24, 19, 0, 0, 0, time.UTC)
	tr := true
	return []otel.UnifiedSignal{
		{
			Harness:       otel.HarnessClaude,
			SignalID:      "sig-span-1",
			Kind:          otel.KindSpan,
			CanonicalName: otel.CanonicalAPIRequest,
			NativeName:    "claude_code.api.request",
			Timestamp:     t,
			SessionID:     sessionID,
			SpanID:        "span-abc",
			Success:       &tr,
			RawAttrs:      map[string]any{"model": "claude-3"},
		},
		{
			Harness:       otel.HarnessClaude,
			SignalID:      "sig-metric-1",
			Kind:          otel.KindMetric,
			CanonicalName: otel.CanonicalTokenUsage,
			NativeName:    "claude_code.token_usage",
			Timestamp:     t,
			SessionID:     sessionID,
			Tokens:        otel.TokenCounts{Input: 100, Output: 50},
			RawAttrs:      map[string]any{},
		},
		{
			Harness:       otel.HarnessClaude,
			SignalID:      "sig-log-1",
			Kind:          otel.KindLog,
			CanonicalName: otel.CanonicalSessionStart,
			NativeName:    "claude_code.session.start",
			Timestamp:     t,
			SessionID:     sessionID,
			RawAttrs:      map[string]any{},
		},
	}
}

// fakeWriter satisfies sqlite.WriterCloser without importing receiver.
type fakeWriter struct {
	batches int
	signals int
}

func (f *fakeWriter) WriteBatch(_ context.Context, _ otel.Harness, _ map[string]any, signals []otel.UnifiedSignal) (int, error) {
	f.batches++
	f.signals += len(signals)
	return len(signals), nil
}

func (f *fakeWriter) Close() error { return nil }

func TestSQLiteSink_WriteBatch(t *testing.T) {
	fw := &fakeWriter{}
	var s sink.SignalSink = sqls.New(fw)
	t.Cleanup(func() { s.Close() })

	signals := newTestSignals("sink-sqlite-sess")
	if err := s.WriteBatch(context.Background(), otel.HarnessClaude, map[string]any{}, signals); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	if fw.signals != 3 {
		t.Errorf("want 3 signals delegated, got %d", fw.signals)
	}
}

func TestNDJSONSink_WriteBatch(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, ".wipnote", "sessions", "sess-ndjson")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	s, err := ndj.New(dir, "sess-ndjson")
	if err != nil {
		t.Fatalf("ndj.New: %v", err)
	}

	signals := newTestSignals("sess-ndjson")
	if err := s.WriteBatch(context.Background(), otel.HarnessClaude, map[string]any{"foo": "bar"}, signals); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	// Close flushes the bufio buffer and syncs before we read.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ndjsonPath := filepath.Join(sessionDir, "events.ndjson")
	f, err := os.Open(ndjsonPath)
	if err != nil {
		t.Fatalf("open ndjson: %v", err)
	}
	defer f.Close()

	var lines []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Errorf("invalid JSON line: %v", err)
			continue
		}
		lines = append(lines, m)
	}
	if sc.Err() != nil {
		t.Fatalf("scan: %v", sc.Err())
	}

	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d", len(lines))
	}
	kinds := []string{"span", "metric", "log"}
	for i, line := range lines {
		if k, ok := line["kind"].(string); !ok || k != kinds[i] {
			t.Errorf("line %d: want kind=%q, got %v", i, kinds[i], line["kind"])
		}
		if _, ok := line["ts"]; !ok {
			t.Errorf("line %d: missing ts field", i)
		}
		if _, ok := line["harness"]; !ok {
			t.Errorf("line %d: missing harness field", i)
		}
		if _, ok := line["signal_id"]; !ok {
			t.Errorf("line %d: missing signal_id field", i)
		}
	}
}

func TestNDJSONSink_NoDBConnection(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, ".wipnote", "sessions", "sess-nodb")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	s, err := ndj.New(dir, "sess-nodb")
	if err != nil {
		t.Fatalf("ndj.New: %v", err)
	}
	defer s.Close()

	signals := newTestSignals("sess-nodb")
	if err := s.WriteBatch(context.Background(), otel.HarnessClaude, nil, signals); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	entries, _ := os.ReadDir(sessionDir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".db" {
			t.Errorf("unexpected .db file found: %s", e.Name())
		}
	}
}

func TestNDJSONSink_Append(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, ".wipnote", "sessions", "sess-append")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	s, err := ndj.New(dir, "sess-append")
	if err != nil {
		t.Fatalf("ndj.New: %v", err)
	}

	signals := newTestSignals("sess-append")
	if err := s.WriteBatch(context.Background(), otel.HarnessClaude, nil, signals[:1]); err != nil {
		t.Fatalf("first WriteBatch: %v", err)
	}
	if err := s.WriteBatch(context.Background(), otel.HarnessClaude, nil, signals[1:2]); err != nil {
		t.Fatalf("second WriteBatch: %v", err)
	}
	// Close flushes the bufio buffer and syncs before we read.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ndjsonPath := filepath.Join(sessionDir, "events.ndjson")
	f, _ := os.Open(ndjsonPath)
	defer f.Close()

	var count int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		count++
	}
	if count != 2 {
		t.Errorf("want 2 lines after 2 WriteBatch calls, got %d", count)
	}
}
