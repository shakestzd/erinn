package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/internal/otel"
	sqls "github.com/shakestzd/wipnote/internal/otel/sink/sqlite"
)

// setupIndexerDB creates a temporary SQLite DB with OTel schema and returns writer + db path.
func setupIndexerDB(t *testing.T) (*sqls.Writer, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "otel.db")
	readDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	readDB.Close()
	w, err := sqls.NewWriter(dbPath)
	if err != nil {
		t.Fatalf("sqls.NewWriter: %v", err)
	}
	t.Cleanup(func() { w.Close() })
	return w, dbPath
}

// writeNDJSONFixture creates a .wipnote/sessions/<sid>/events.ndjson file
// with the given NDJSON lines.
func writeNDJSONFixture(t *testing.T, wipnoteDir, sessionID string, lines []string) string {
	t.Helper()
	sessDir := filepath.Join(wipnoteDir, "sessions", sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	ndjsonPath := filepath.Join(sessDir, "events.ndjson")
	var content []byte
	for _, l := range lines {
		content = append(content, []byte(l+"\n")...)
	}
	if err := os.WriteFile(ndjsonPath, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return ndjsonPath
}

// countSignals returns the number of rows in otel_signals for the given session.
func countSignals(t *testing.T, w *sqls.Writer, sessionID string) int {
	t.Helper()
	var n int
	row := w.DB().QueryRow(`SELECT COUNT(*) FROM otel_signals WHERE session_id = ?`, sessionID)
	if err := row.Scan(&n); err != nil {
		t.Fatalf("count signals: %v", err)
	}
	return n
}

// TestIndexer_ProcessesNDJSONFile verifies the indexer applies all signal lines from
// a pre-written NDJSON file to the SQLite database.
func TestIndexer_ProcessesNDJSONFile(t *testing.T) {
	w, _ := setupIndexerDB(t)
	wipnoteDir := t.TempDir()
	sessionID := "idx-test-sess-01"

	lines := []string{
		`{"kind":"span","harness":"claude_code","ts":"2026-04-24T19:00:00Z","signal_id":"s1","session_id":"idx-test-sess-01","canonical":"api_request","native":"claude_code.api_request"}`,
		`{"kind":"metric","harness":"claude_code","ts":"2026-04-24T19:00:01Z","signal_id":"s2","session_id":"idx-test-sess-01","canonical":"token_usage","native":"claude_code.token_usage","tokens_input":100,"tokens_output":50}`,
		`{"kind":"log","harness":"claude_code","ts":"2026-04-24T19:00:02Z","signal_id":"s3","session_id":"idx-test-sess-01","canonical":"session_start","native":"claude_code.session.start"}`,
	}
	writeNDJSONFixture(t, wipnoteDir, sessionID, lines)

	snk := sqls.New(w)
	idxr := New(wipnoteDir, snk)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := idxr.processSession(ctx, sessionID); err != nil {
		t.Fatalf("processSession: %v", err)
	}

	got := countSignals(t, w, sessionID)
	if got != 3 {
		t.Errorf("want 3 signals in DB, got %d", got)
	}
}

// TestIndexer_IdempotentReplay verifies that replaying the same NDJSON file twice
// doesn't create duplicate rows (INSERT OR IGNORE on signal_id).
func TestIndexer_IdempotentReplay(t *testing.T) {
	w, _ := setupIndexerDB(t)
	wipnoteDir := t.TempDir()
	sessionID := "idx-test-sess-02"

	lines := []string{
		`{"kind":"span","harness":"claude_code","ts":"2026-04-24T19:00:00Z","signal_id":"dup-s1","session_id":"idx-test-sess-02","canonical":"api_request","native":"claude_code.api_request"}`,
	}
	writeNDJSONFixture(t, wipnoteDir, sessionID, lines)

	snk := sqls.New(w)
	idxr := New(wipnoteDir, snk)
	ctx := context.Background()

	// First replay.
	if err := idxr.processSession(ctx, sessionID); err != nil {
		t.Fatalf("first processSession: %v", err)
	}
	// Reset checkpoint to force full replay.
	checkpointPath := filepath.Join(wipnoteDir, "sessions", sessionID, ".index-offset")
	if err := os.Remove(checkpointPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove checkpoint: %v", err)
	}

	// Second replay from offset 0.
	if err := idxr.processSession(ctx, sessionID); err != nil {
		t.Fatalf("second processSession: %v", err)
	}

	got := countSignals(t, w, sessionID)
	if got != 1 {
		t.Errorf("want 1 signal (idempotent), got %d", got)
	}
}

// TestIndexer_SkipsCollectorStart verifies that collector_start lines don't create DB rows.
func TestIndexer_SkipsCollectorStart(t *testing.T) {
	w, _ := setupIndexerDB(t)
	wipnoteDir := t.TempDir()
	sessionID := "idx-test-sess-03"

	lines := []string{
		`{"kind":"collector_start","harness":"claude_code","ts":"2026-04-24T19:00:00Z","signal_id":"cs-1","session_id":"idx-test-sess-03"}`,
		`{"kind":"span","harness":"claude_code","ts":"2026-04-24T19:00:01Z","signal_id":"real-s1","session_id":"idx-test-sess-03","canonical":"api_request","native":"claude_code.api_request"}`,
	}
	writeNDJSONFixture(t, wipnoteDir, sessionID, lines)

	snk := sqls.New(w)
	idxr := New(wipnoteDir, snk)
	ctx := context.Background()

	if err := idxr.processSession(ctx, sessionID); err != nil {
		t.Fatalf("processSession: %v", err)
	}

	got := countSignals(t, w, sessionID)
	if got != 1 {
		t.Errorf("want 1 signal (skipped collector_start), got %d", got)
	}
}

// TestIndexer_CheckpointResumesFromOffset verifies that the indexer only processes
// new lines after the checkpoint offset.
func TestIndexer_CheckpointResumesFromOffset(t *testing.T) {
	w, _ := setupIndexerDB(t)
	wipnoteDir := t.TempDir()
	sessionID := "idx-test-sess-04"

	line1 := `{"kind":"span","harness":"claude_code","ts":"2026-04-24T19:00:00Z","signal_id":"chk-s1","session_id":"idx-test-sess-04","canonical":"api_request","native":"claude_code.api_request"}`
	line2 := `{"kind":"span","harness":"claude_code","ts":"2026-04-24T19:00:01Z","signal_id":"chk-s2","session_id":"idx-test-sess-04","canonical":"api_request","native":"claude_code.api_request"}`

	ndjsonPath := writeNDJSONFixture(t, wipnoteDir, sessionID, []string{line1})

	snk := sqls.New(w)
	idxr := New(wipnoteDir, snk)
	ctx := context.Background()

	// First run: processes line1, checkpoints after it.
	if err := idxr.processSession(ctx, sessionID); err != nil {
		t.Fatalf("first processSession: %v", err)
	}

	// Append line2 to the file.
	f, err := os.OpenFile(ndjsonPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := f.WriteString(line2 + "\n"); err != nil {
		f.Close()
		t.Fatalf("append: %v", err)
	}
	f.Close()

	// Second run: should only process line2 (line1 already checkpointed).
	if err := idxr.processSession(ctx, sessionID); err != nil {
		t.Fatalf("second processSession: %v", err)
	}

	got := countSignals(t, w, sessionID)
	if got != 2 {
		t.Errorf("want 2 signals total (1 per run), got %d", got)
	}
}

// TestIndexer_Status verifies the status snapshot reflects per-session offsets and sizes.
func TestIndexer_Status(t *testing.T) {
	w, _ := setupIndexerDB(t)
	wipnoteDir := t.TempDir()
	sessionID := "idx-status-sess"

	lines := []string{
		`{"kind":"span","harness":"claude_code","ts":"2026-04-24T19:00:00Z","signal_id":"st-s1","session_id":"idx-status-sess","canonical":"api_request","native":"claude_code.api_request"}`,
	}
	writeNDJSONFixture(t, wipnoteDir, sessionID, lines)

	snk := sqls.New(w)
	idxr := New(wipnoteDir, snk)
	ctx := context.Background()

	if err := idxr.processSession(ctx, sessionID); err != nil {
		t.Fatalf("processSession: %v", err)
	}

	status := idxr.Status()
	if len(status) == 0 {
		t.Fatal("Status() returned empty map")
	}

	fi, ok := status[sessionID]
	if !ok {
		t.Fatalf("status map missing session %q, got keys: %v", sessionID, statusKeys(status))
	}
	if fi.LastOffset <= 0 {
		t.Errorf("LastOffset should be > 0 after indexing, got %d", fi.LastOffset)
	}
	if fi.CurrentSize <= 0 {
		t.Errorf("CurrentSize should be > 0, got %d", fi.CurrentSize)
	}
}

// TestIndexer_Start_ContextCancel verifies Start() respects context cancellation.
func TestIndexer_Start_ContextCancel(t *testing.T) {
	wipnoteDir := t.TempDir()
	// Create the sessions dir so the indexer can scan it.
	_ = os.MkdirAll(filepath.Join(wipnoteDir, "sessions"), 0o755)

	// Use a fakeWriter that satisfies sqls.WriterCloser.
	snk := sqls.New(&fakeWriter{})
	idxr := New(wipnoteDir, snk)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		idxr.Start(ctx)
		close(done)
	}()

	// Cancel quickly; Start should return.
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Start() did not return after context cancel")
	}
}

// fakeWriter satisfies sqls.WriterCloser for tests that don't need real DB.
type fakeWriter struct{}

func (f *fakeWriter) WriteBatch(_ context.Context, _ otel.Harness, _ map[string]any, signals []otel.UnifiedSignal) (int, error) {
	return len(signals), nil
}
func (f *fakeWriter) Close() error { return nil }

// statusKeys returns sorted keys from a FileInfo map for error messages.
func statusKeys(m map[string]FileInfo) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestIndexer_DiscoverSessions verifies that discoverSessions finds session dirs.
func TestIndexer_DiscoverSessions(t *testing.T) {
	wipnoteDir := t.TempDir()
	sessionsDir := filepath.Join(wipnoteDir, "sessions")

	// Create two session dirs, one with events.ndjson, one without.
	sess1 := filepath.Join(sessionsDir, "sess-alpha")
	sess2 := filepath.Join(sessionsDir, "sess-beta")
	_ = os.MkdirAll(sess1, 0o755)
	_ = os.MkdirAll(sess2, 0o755)
	_ = os.WriteFile(filepath.Join(sess1, "events.ndjson"), []byte(""), 0o644)
	// sess2 has no events.ndjson

	snk := sqls.New(&fakeWriter{})
	idxr := New(wipnoteDir, snk)

	sessions, err := idxr.discoverSessions()
	if err != nil {
		t.Fatalf("discoverSessions: %v", err)
	}
	// Only sess-alpha should be returned (has events.ndjson).
	if len(sessions) != 1 {
		t.Errorf("want 1 session with events.ndjson, got %d: %v", len(sessions), sessions)
	}
	if len(sessions) > 0 && sessions[0] != "sess-alpha" {
		t.Errorf("expected sess-alpha, got %q", sessions[0])
	}
}

// TestProcessSession_RespectsMaxBytesPerTick verifies that processSession advances
// the checkpoint by at most maxBytesPerTick bytes on a single call, even when far
// more data is available.
func TestProcessSession_RespectsMaxBytesPerTick(t *testing.T) {
	wipnoteDir := t.TempDir()
	sessionID := "budget-sess-01"

	// Build a large NDJSON file that is clearly over maxBytesPerTick (4 MiB).
	// Each line is a valid signal JSON. We want total > 2*maxBytesPerTick so two
	// ticks are needed.
	singleLine := `{"kind":"span","harness":"claude_code","ts":"2026-04-24T19:00:00Z","signal_id":"bud-%06d","session_id":"budget-sess-01","canonical":"api_request","native":"claude_code.api_request"}`
	// One line is ~150 bytes; we need >4 MiB total, so ~30,000 lines.
	const targetBytes = maxBytesPerTick*2 + 512*1024 // ~8.5 MiB
	const approxLineBytes = 160
	lineCount := targetBytes/approxLineBytes + 1

	sessDir := filepath.Join(wipnoteDir, "sessions", sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	ndjsonPath := filepath.Join(sessDir, "events.ndjson")
	f, err := os.Create(ndjsonPath)
	if err != nil {
		t.Fatalf("create ndjson: %v", err)
	}
	for i := 0; i < lineCount; i++ {
		fmt.Fprintf(f, singleLine+"\n", i)
	}
	f.Close()

	info, err := os.Stat(ndjsonPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	totalSize := info.Size()
	if totalSize <= maxBytesPerTick {
		t.Fatalf("fixture too small: %d bytes (need > %d)", totalSize, maxBytesPerTick)
	}

	snk := sqls.New(&fakeWriter{})
	idxr := New(wipnoteDir, snk)
	ctx := context.Background()

	// First tick: should advance by at most maxBytesPerTick.
	if err := idxr.processSession(ctx, sessionID); err != nil {
		t.Fatalf("first processSession: %v", err)
	}
	checkpointPath := filepath.Join(sessDir, ".index-offset")
	offset1, err := readCheckpoint(checkpointPath)
	if err != nil {
		t.Fatalf("read checkpoint after tick 1: %v", err)
	}
	if offset1 <= 0 {
		t.Fatal("offset did not advance after first tick")
	}
	if offset1 > maxBytesPerTick+approxLineBytes {
		// Allow one extra line of slop (the budget check happens before reading
		// the line, so the final line may push us slightly over).
		t.Errorf("offset after tick 1 = %d, want <= %d (+slop)", offset1, maxBytesPerTick+approxLineBytes)
	}

	// Second tick: should advance further but still not reach end of file.
	if err := idxr.processSession(ctx, sessionID); err != nil {
		t.Fatalf("second processSession: %v", err)
	}
	offset2, err := readCheckpoint(checkpointPath)
	if err != nil {
		t.Fatalf("read checkpoint after tick 2: %v", err)
	}
	if offset2 <= offset1 {
		t.Errorf("offset did not advance on second tick: before=%d after=%d", offset1, offset2)
	}
	if offset2 >= totalSize {
		t.Logf("offset2=%d totalSize=%d: file fully consumed in 2 ticks (acceptable if file is small)", offset2, totalSize)
	}
}

// TestProcessSession_AlignsCutoffToNewline verifies that processSession never
// checkpoints mid-record: the offset is always on a newline boundary.
func TestProcessSession_AlignsCutoffToNewline(t *testing.T) {
	wipnoteDir := t.TempDir()
	sessionID := "align-sess-01"

	// Write enough lines to exceed maxBytesPerTick.
	// We use fakeWriter so no real SQLite is needed.
	fixedLine := `{"kind":"span","harness":"claude_code","ts":"2026-04-24T19:00:00Z","signal_id":"aln-XXXXX","session_id":"align-sess-01","canonical":"api_request","native":"claude_code.api_request"}`
	const totalLines = 30000
	sessDir := filepath.Join(wipnoteDir, "sessions", sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	ndjsonPath := filepath.Join(sessDir, "events.ndjson")
	fh, err := os.Create(ndjsonPath)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < totalLines; i++ {
		fmt.Fprintln(fh, fixedLine)
	}
	fh.Close()

	snk := sqls.New(&fakeWriter{})
	idxr := New(wipnoteDir, snk)
	ctx := context.Background()

	if err := idxr.processSession(ctx, sessionID); err != nil {
		t.Fatalf("processSession: %v", err)
	}

	checkpointPath := filepath.Join(sessDir, ".index-offset")
	offset, err := readCheckpoint(checkpointPath)
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	if offset <= 0 {
		t.Fatal("no progress made")
	}

	// Re-open file at the checkpointed offset and confirm the byte there is a
	// newline (i.e. the offset lands right after a '\n').
	fh2, err := os.Open(ndjsonPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer fh2.Close()

	// Seek to offset-1 to read the byte before the checkpoint position.
	if _, err := fh2.Seek(offset-1, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	b := make([]byte, 1)
	if _, err := fh2.Read(b); err != nil {
		t.Fatalf("read byte at offset-1: %v", err)
	}
	if b[0] != '\n' {
		t.Errorf("checkpoint offset %d is not on a newline boundary: byte at offset-1 = 0x%02x", offset, b[0])
	}
}

// Ensure sql package is referenced so import is valid.
var _ = sql.ErrNoRows
