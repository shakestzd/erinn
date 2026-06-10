package ndjson_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/observe/otel"
	"github.com/shakestzd/wipnote/observe/otel/sink/ndjson"
)

func makeSignal(kind otel.Kind, id, sessionID string) otel.UnifiedSignal {
	return otel.UnifiedSignal{
		Harness:       otel.HarnessClaude,
		SignalID:      id,
		Kind:          kind,
		CanonicalName: "test_event",
		NativeName:    "test.event",
		Timestamp:     time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC),
		SessionID:     sessionID,
		RawAttrs:      map[string]any{"key": "val"},
	}
}

func TestNDJSONSink_OneLinePerSignal(t *testing.T) {
	dir := t.TempDir()
	sid := "ndjson-test-sess"
	sessDir := filepath.Join(dir, ".wipnote", "sessions", sid)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	s, err := ndjson.New(dir, sid)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	signals := []otel.UnifiedSignal{
		makeSignal(otel.KindSpan, "id-span", sid),
		makeSignal(otel.KindMetric, "id-metric", sid),
		makeSignal(otel.KindLog, "id-log", sid),
	}
	if err := s.WriteBatch(context.Background(), otel.HarnessClaude, nil, signals); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	// Close flushes the bufio buffer and syncs before we read the file.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.Open(filepath.Join(sessDir, "events.ndjson"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	var rows []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Errorf("bad JSON: %v | line: %s", err, sc.Text())
			continue
		}
		rows = append(rows, m)
	}
	if sc.Err() != nil {
		t.Fatal(sc.Err())
	}

	if len(rows) != 3 {
		t.Fatalf("want 3 lines, got %d", len(rows))
	}

	wantKinds := []string{"span", "metric", "log"}
	for i, row := range rows {
		k, _ := row["kind"].(string)
		if k != wantKinds[i] {
			t.Errorf("row %d: want kind=%q got %q", i, wantKinds[i], k)
		}
		if _, ok := row["ts"]; !ok {
			t.Errorf("row %d: missing ts", i)
		}
		if _, ok := row["harness"]; !ok {
			t.Errorf("row %d: missing harness", i)
		}
		if _, ok := row["signal_id"]; !ok {
			t.Errorf("row %d: missing signal_id", i)
		}
	}
}

func TestNDJSONSink_ValidJSON(t *testing.T) {
	dir := t.TempDir()
	sid := "json-valid-sess"
	sessDir := filepath.Join(dir, ".wipnote", "sessions", sid)
	os.MkdirAll(sessDir, 0o755)

	s, _ := ndjson.New(dir, sid)

	signals := []otel.UnifiedSignal{makeSignal(otel.KindSpan, "id-1", sid)}
	s.WriteBatch(context.Background(), otel.HarnessClaude, map[string]any{"env": "test"}, signals)
	// Close flushes the bufio buffer and syncs before we read the file.
	s.Close()

	data, err := os.ReadFile(filepath.Join(sessDir, "events.ndjson"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data[:len(data)-1], &m); err != nil { // strip trailing newline
		t.Fatalf("not valid JSON: %v\nline: %s", err, data)
	}
}

func TestNDJSONSink_EmptyBatchIsNoOp(t *testing.T) {
	dir := t.TempDir()
	sid := "empty-batch-sess"
	sessDir := filepath.Join(dir, ".wipnote", "sessions", sid)
	os.MkdirAll(sessDir, 0o755)

	s, _ := ndjson.New(dir, sid)
	defer s.Close()

	if err := s.WriteBatch(context.Background(), otel.HarnessClaude, nil, nil); err != nil {
		t.Fatalf("empty WriteBatch returned error: %v", err)
	}

	// New now opens the file eagerly (O_CREATE), so the file exists but has 0 lines.
	ndjsonPath := filepath.Join(sessDir, "events.ndjson")
	f, err := os.Open(ndjsonPath)
	if err != nil {
		t.Fatalf("expected file to exist after New: %v", err)
	}
	sc := bufio.NewScanner(f)
	lineCount := 0
	for sc.Scan() {
		lineCount++
	}
	f.Close()
	if lineCount != 0 {
		t.Errorf("empty batch: expected 0 lines, got %d", lineCount)
	}
}

func TestNDJSONSink_CloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	sid := "close-idem-sess"
	sessDir := filepath.Join(dir, ".wipnote", "sessions", sid)
	os.MkdirAll(sessDir, 0o755)

	s, err := ndjson.New(dir, sid)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestNDJSONSink_PeriodicFlush verifies that events written to the sink are
// flushed and synced to disk within SyncInterval+1s without an explicit Close.
// This guards against data loss on abrupt process termination (host sleep,
// devcontainer disconnect, SIGKILL).
func TestNDJSONSink_PeriodicFlush(t *testing.T) {
	dir := t.TempDir()
	sid := "periodic-flush-sess"
	sessDir := filepath.Join(dir, ".wipnote", "sessions", sid)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	s, err := ndjson.New(dir, sid)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Do NOT call Close — simulate an abrupt stop. The goroutine cleanup is
	// handled by the test process exit; no leak in tests.
	defer s.Close()

	signals := []otel.UnifiedSignal{
		makeSignal(otel.KindSpan, "flush-1", sid),
		makeSignal(otel.KindLog, "flush-2", sid),
		makeSignal(otel.KindMetric, "flush-3", sid),
		makeSignal(otel.KindSpan, "flush-4", sid),
		makeSignal(otel.KindLog, "flush-5", sid),
	}
	if err := s.WriteBatch(context.Background(), otel.HarnessClaude, nil, signals); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}

	ndjsonPath := filepath.Join(sessDir, "events.ndjson")

	// Poll until the periodic ticker fires and syncs the data. Budget: SyncInterval + 1s.
	deadline := time.Now().Add(ndjson.SyncInterval + time.Second)
	for time.Now().Before(deadline) {
		info, err := os.Stat(ndjsonPath)
		if err == nil && info.Size() > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	data, err := os.ReadFile(ndjsonPath)
	if err != nil {
		t.Fatalf("file not readable after periodic flush window: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("file is still empty after SyncInterval+1s — periodic flush did not run")
	}

	// Count lines to confirm all 5 events are present.
	lineCount := 0
	for _, b := range data {
		if b == '\n' {
			lineCount++
		}
	}
	if lineCount != 5 {
		t.Errorf("want 5 lines after periodic flush, got %d", lineCount)
	}
}

// TestNDJSONSink_AppendOnReopen verifies that when a second Sink is opened for
// the same session path (simulating a collector restart after host sleep), it
// appends to the existing log rather than truncating it.
func TestNDJSONSink_AppendOnReopen(t *testing.T) {
	dir := t.TempDir()
	sid := "append-reopen-sess"
	sessDir := filepath.Join(dir, ".wipnote", "sessions", sid)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// First sink — write 3 events, then "crash" (abandon without Close / goroutine runs).
	s1, err := ndjson.New(dir, sid)
	if err != nil {
		t.Fatalf("New (first): %v", err)
	}
	batch1 := []otel.UnifiedSignal{
		makeSignal(otel.KindSpan, "reopen-a1", sid),
		makeSignal(otel.KindLog, "reopen-a2", sid),
		makeSignal(otel.KindMetric, "reopen-a3", sid),
	}
	if err := s1.WriteBatch(context.Background(), otel.HarnessClaude, nil, batch1); err != nil {
		t.Fatalf("WriteBatch (first): %v", err)
	}
	// Force flush to disk before "crashing".
	if err := s1.Close(); err != nil {
		t.Fatalf("Close (first): %v", err)
	}

	// Confirm 3 lines on disk.
	ndjsonPath := filepath.Join(sessDir, "events.ndjson")
	countLines := func() int {
		data, err := os.ReadFile(ndjsonPath)
		if err != nil {
			return -1
		}
		n := 0
		for _, b := range data {
			if b == '\n' {
				n++
			}
		}
		return n
	}
	if got := countLines(); got != 3 {
		t.Fatalf("after first sink: want 3 lines, got %d", got)
	}

	// Second sink — simulates a replacement collector opening the same session file.
	s2, err := ndjson.New(dir, sid)
	if err != nil {
		t.Fatalf("New (second): %v", err)
	}
	defer s2.Close()

	batch2 := []otel.UnifiedSignal{
		makeSignal(otel.KindSpan, "reopen-b1", sid),
		makeSignal(otel.KindLog, "reopen-b2", sid),
	}
	if err := s2.WriteBatch(context.Background(), otel.HarnessClaude, nil, batch2); err != nil {
		t.Fatalf("WriteBatch (second): %v", err)
	}
	if err := s2.Close(); err != nil {
		t.Fatalf("Close (second): %v", err)
	}

	// File must contain all 5 events (3 original + 2 new).
	if got := countLines(); got != 5 {
		t.Errorf("after reopen: want 5 lines (3+2), got %d", got)
	}
}
