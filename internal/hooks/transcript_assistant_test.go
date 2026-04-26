package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTranscriptLine returns a JSONL line for an assistant record.
func makeTranscriptLine(uuid, parentUUID, sessionID, stopReason, text string, isSidechain bool) string {
	rec := map[string]any{
		"type":        "assistant",
		"uuid":        uuid,
		"parentUuid":  parentUUID,
		"sessionId":   sessionID,
		"requestId":   "req_" + uuid,
		"timestamp":   "2026-04-20T10:00:00.000Z",
		"isSidechain": isSidechain,
		"message": map[string]any{
			"role":        "assistant",
			"stop_reason": stopReason,
			"content": []map[string]any{
				{"type": "text", "text": text},
			},
		},
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

// makeThinkingLine returns a JSONL line for an assistant record with only a thinking block.
func makeThinkingLine(uuid, sessionID string) string {
	rec := map[string]any{
		"type":        "assistant",
		"uuid":        uuid,
		"parentUuid":  "parent-" + uuid,
		"sessionId":   sessionID,
		"requestId":   "req_" + uuid,
		"timestamp":   "2026-04-20T10:00:00.000Z",
		"isSidechain": false,
		"message": map[string]any{
			"role":        "assistant",
			"stop_reason": "end_turn",
			"content": []map[string]any{
				{"type": "thinking", "thinking": "some internal reasoning"},
			},
		},
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

// makeUserLine returns a JSONL line for a user record.
func makeUserLine(uuid, sessionID, text string) string {
	rec := map[string]any{
		"type":      "user",
		"uuid":      uuid,
		"sessionId": sessionID,
		"timestamp": "2026-04-20T09:59:00.000Z",
		"message": map[string]any{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": text},
			},
		},
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

// makeAssistantToolUseRecord returns a JSONL line for an assistant record with tool_use.
func makeAssistantToolUseRecord(uuid, parentUUID, sessionID string) string {
	rec := map[string]any{
		"type":        "assistant",
		"uuid":        uuid,
		"parentUuid":  parentUUID,
		"sessionId":   sessionID,
		"requestId":   "req_" + uuid,
		"timestamp":   "2026-04-20T10:00:00.000Z",
		"isSidechain": false,
		"message": map[string]any{
			"role":        "assistant",
			"stop_reason": "tool_use",
			"content": []map[string]any{
				{"type": "tool_use", "id": "tool_" + uuid, "name": "test_tool", "input": map[string]any{}},
			},
		},
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

// makeUserToolResultRecord returns a JSONL line for a user record with tool_result.
func makeUserToolResultRecord(uuid, parentUUID, sessionID, toolUseID string) string {
	rec := map[string]any{
		"type":      "user",
		"uuid":      uuid,
		"parentUuid": parentUUID,
		"sessionId": sessionID,
		"timestamp": "2026-04-20T09:59:00.000Z",
		"message": map[string]any{
			"role": "user",
			"content": []map[string]any{
				{"type": "tool_result", "tool_use_id": toolUseID, "content": "Tool returned: success"},
			},
		},
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

// writeTempTranscript writes lines to a temp file and returns its path.
func writeTempTranscript(t *testing.T, lines []string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "transcript-*.jsonl")
	if err != nil {
		t.Fatalf("create temp transcript: %v", err)
	}
	defer f.Close()
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
	return f.Name()
}

// --- readLastAssistantRecord tests ---

// TestReadLastAssistantRecord_HappyPath verifies that the most recent
// non-sidechain assistant text record is returned.
func TestReadLastAssistantRecord_HappyPath(t *testing.T) {
	path := writeTempTranscript(t, []string{
		makeUserLine("user-1", "sess-1", "Hello"),
		makeTranscriptLine("asst-1", "user-1", "sess-1", "end_turn", "Hi there!", false),
	})

	rec, err := readLastAssistantRecord(path)
	if err != nil {
		t.Fatalf("readLastAssistantRecord: %v", err)
	}
	if rec == nil {
		t.Fatal("expected non-nil record")
	}
	if rec.UUID != "asst-1" {
		t.Errorf("UUID = %q, want %q", rec.UUID, "asst-1")
	}
	if extractAssistantText(rec) != "Hi there!" {
		t.Errorf("text = %q, want %q", extractAssistantText(rec), "Hi there!")
	}
	if rec.Message.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q, want %q", rec.Message.StopReason, "end_turn")
	}
}

// TestReadLastAssistantRecord_SidechainFilter verifies that sidechain assistant
// records are skipped and we fall back to the most recent non-sidechain record.
func TestReadLastAssistantRecord_SidechainFilter(t *testing.T) {
	path := writeTempTranscript(t, []string{
		makeTranscriptLine("asst-main", "user-1", "sess-1", "end_turn", "Main reply", false),
		makeTranscriptLine("asst-side", "user-1", "sess-1", "end_turn", "Sidechain reply", true),
	})

	rec, err := readLastAssistantRecord(path)
	if err != nil {
		t.Fatalf("readLastAssistantRecord: %v", err)
	}
	if rec == nil {
		t.Fatal("expected non-nil record (should find the non-sidechain one)")
	}
	if rec.UUID != "asst-main" {
		t.Errorf("UUID = %q, want %q (should skip sidechain)", rec.UUID, "asst-main")
	}
}

// TestReadLastAssistantRecord_ThinkingOnly verifies that a record with only
// thinking blocks (no text blocks) is skipped.
func TestReadLastAssistantRecord_ThinkingOnly(t *testing.T) {
	path := writeTempTranscript(t, []string{
		makeThinkingLine("asst-thinking", "sess-1"),
	})

	rec, err := readLastAssistantRecord(path)
	if err != nil {
		t.Fatalf("readLastAssistantRecord: %v", err)
	}
	if rec != nil {
		t.Errorf("expected nil (thinking-only), got UUID=%q", rec.UUID)
	}
}

// TestReadLastAssistantRecord_Interrupted verifies that stop_reason != end_turn
// is preserved on the returned record.
func TestReadLastAssistantRecord_Interrupted(t *testing.T) {
	path := writeTempTranscript(t, []string{
		makeTranscriptLine("asst-trunc", "user-1", "sess-1", "max_tokens", "Partial...", false),
	})

	rec, err := readLastAssistantRecord(path)
	if err != nil {
		t.Fatalf("readLastAssistantRecord: %v", err)
	}
	if rec == nil {
		t.Fatal("expected non-nil record")
	}
	if rec.Message.StopReason != "max_tokens" {
		t.Errorf("stop_reason = %q, want %q", rec.Message.StopReason, "max_tokens")
	}
}

// TestReadLastAssistantRecord_MultiTurn verifies the MOST RECENT record is
// returned when a multi-turn transcript is present.
func TestReadLastAssistantRecord_MultiTurn(t *testing.T) {
	path := writeTempTranscript(t, []string{
		makeUserLine("user-1", "sess-1", "First question"),
		makeTranscriptLine("asst-1", "user-1", "sess-1", "end_turn", "First answer", false),
		makeUserLine("user-2", "sess-1", "Second question"),
		makeTranscriptLine("asst-2", "user-2", "sess-1", "end_turn", "Second answer", false),
	})

	rec, err := readLastAssistantRecord(path)
	if err != nil {
		t.Fatalf("readLastAssistantRecord: %v", err)
	}
	if rec == nil {
		t.Fatal("expected non-nil record")
	}
	if rec.UUID != "asst-2" {
		t.Errorf("UUID = %q, want %q (should be the most recent)", rec.UUID, "asst-2")
	}
}

// TestReadLastAssistantRecord_MissingFile verifies that a missing transcript
// path returns nil without error (non-fatal).
func TestReadLastAssistantRecord_MissingFile(t *testing.T) {
	rec, err := readLastAssistantRecord(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if rec != nil {
		t.Errorf("expected nil record for missing file, got UUID=%q", rec.UUID)
	}
}

// --- insertAssistantTextSignal tests ---

// TestInsertAssistantTextSignal_HappyPath verifies the full happy path:
// transcript is written, Stop hook reads it, and a UnifiedSignal NDJSON
// line is emitted with the correct canonical and attrs.
func TestInsertAssistantTextSignal_HappyPath(t *testing.T) {
	td := setupTestDB(t)
	sessionID := "test-sess"
	projectDir := t.TempDir()

	path := writeTempTranscript(t, []string{
		makeTranscriptLine("asst-uuid-1", "user-uuid-1", sessionID, "end_turn", "Hello world!", false),
	})

	insertAssistantTextSignal(td.DB, projectDir, sessionID, path)

	signals := readNDJSONSignals(t, projectDir, sessionID)
	if len(signals) != 1 {
		t.Fatalf("want 1 NDJSON line, got %d", len(signals))
	}
	s := signals[0]
	if s["canonical"] != "assistant_text" {
		t.Errorf("canonical = %v, want assistant_text", s["canonical"])
	}
	if s["kind"] != "log" {
		t.Errorf("kind = %v, want log", s["kind"])
	}
	if s["span_id"] != "asst-uuid-1" {
		t.Errorf("span_id = %v, want asst-uuid-1", s["span_id"])
	}
	if s["parent_span"] != "user-uuid-1" {
		t.Errorf("parent_span = %v, want user-uuid-1", s["parent_span"])
	}
	attrs, _ := s["attrs"].(map[string]any)
	if attrs["text"] != "Hello world!" {
		t.Errorf("attrs.text = %v, want %q", attrs["text"], "Hello world!")
	}
	if attrs["stop_reason"] != "end_turn" {
		t.Errorf("attrs.stop_reason = %v, want end_turn", attrs["stop_reason"])
	}
}

// TestInsertAssistantTextSignal_Idempotent verifies hook-side signal_id
// stability — calling twice produces two NDJSON lines with the same
// signal_id, which the indexer's INSERT OR IGNORE collapses to one
// otel_signals row (covered by TestIndexer_IdempotentReplay).
func TestInsertAssistantTextSignal_Idempotent(t *testing.T) {
	td := setupTestDB(t)
	sessionID := "test-sess"
	projectDir := t.TempDir()

	path := writeTempTranscript(t, []string{
		makeTranscriptLine("asst-idem", "user-idem", sessionID, "end_turn", "Same reply", false),
	})

	insertAssistantTextSignal(td.DB, projectDir, sessionID, path)
	insertAssistantTextSignal(td.DB, projectDir, sessionID, path)

	signals := readNDJSONSignals(t, projectDir, sessionID)
	if len(signals) != 2 {
		t.Fatalf("want 2 NDJSON lines, got %d", len(signals))
	}
	if signals[0]["signal_id"] != signals[1]["signal_id"] {
		t.Errorf("signal_id stability: line0=%v, line1=%v (want equal — indexer dedups)",
			signals[0]["signal_id"], signals[1]["signal_id"])
	}
}

// TestInsertAssistantTextSignal_MissingFile verifies that a missing transcript
// does not produce a row or an error (silent skip).
func TestInsertAssistantTextSignal_MissingFile(t *testing.T) {
	td := setupTestDB(t)
	sessionID := "test-sess"
	projectDir := t.TempDir()

	insertAssistantTextSignal(td.DB, projectDir, sessionID, filepath.Join(t.TempDir(), "no-such.jsonl"))

	if got := readNDJSONSignals(t, projectDir, sessionID); len(got) != 0 {
		t.Errorf("expected 0 NDJSON lines for missing transcript, got %d", len(got))
	}
}

// TestInsertAssistantTextSignal_Interrupted verifies that an interrupted turn
// (stop_reason != end_turn) includes the interrupted flag in attrs_json.
func TestInsertAssistantTextSignal_Interrupted(t *testing.T) {
	td := setupTestDB(t)
	sessionID := "test-sess"
	projectDir := t.TempDir()

	path := writeTempTranscript(t, []string{
		makeTranscriptLine("asst-trunc-1", "user-trunc-1", sessionID, "max_tokens", "Partial text...", false),
	})

	insertAssistantTextSignal(td.DB, projectDir, sessionID, path)

	signals := readNDJSONSignals(t, projectDir, sessionID)
	if len(signals) != 1 {
		t.Fatalf("want 1 NDJSON line, got %d", len(signals))
	}
	attrs, _ := signals[0]["attrs"].(map[string]any)
	if attrs["interrupted"] != true {
		t.Errorf("expected attrs.interrupted=true for max_tokens turn, got %v", attrs["interrupted"])
	}
	if attrs["stop_reason"] != "max_tokens" {
		t.Errorf("attrs.stop_reason = %v, want max_tokens", attrs["stop_reason"])
	}
}

// TestInsertAssistantTextSignal_NoTranscriptPath verifies that an empty
// transcript_path skips all processing without error.
func TestInsertAssistantTextSignal_NoTranscriptPath(t *testing.T) {
	td := setupTestDB(t)
	sessionID := "test-sess"
	projectDir := t.TempDir()

	insertAssistantTextSignal(td.DB, projectDir, sessionID, "")

	if got := readNDJSONSignals(t, projectDir, sessionID); len(got) != 0 {
		t.Errorf("expected 0 NDJSON lines when transcript_path empty, got %d", len(got))
	}
}

// TestAssistantText_ParentSpanWalksPastToolResult verifies that when an assistant
// text reply comes after a tool call, parent_span correctly walks the chain up to
// find the original human text prompt, not the tool_result.
//
// Chain: user_text(A1) → assistant_tool_use(A2) → user_tool_result(A3) → assistant_text(A4)
// Expected: A4.parent_span = A1 (human prompt), NOT A3 (tool result).
func TestAssistantText_ParentSpanWalksPastToolResult(t *testing.T) {
	td := setupTestDB(t)
	sessionID := "test-sess"
	projectDir := t.TempDir()

	userPromptUUID := "user-1"
	assistantToolUseUUID := "asst-2"
	userToolResultUUID := "user-3"
	assistantTextUUID := "asst-4"

	path := writeTempTranscript(t, []string{
		makeUserLine(userPromptUUID, sessionID, "Can you call a tool for me?"),
		makeAssistantToolUseRecord(assistantToolUseUUID, userPromptUUID, sessionID),
		makeUserToolResultRecord(userToolResultUUID, assistantToolUseUUID, sessionID, "tool_asst-2"),
		makeTranscriptLine(assistantTextUUID, userToolResultUUID, sessionID, "end_turn", "Tool executed successfully!", false),
	})

	insertAssistantTextSignal(td.DB, projectDir, sessionID, path)

	signals := readNDJSONSignals(t, projectDir, sessionID)
	s := findSignal(signals, func(m map[string]any) bool {
		return m["canonical"] == "assistant_text" && m["span_id"] == assistantTextUUID
	})
	if s == nil {
		t.Fatalf("assistant_text signal not found in NDJSON; got %d signals", len(signals))
	}
	if s["parent_span"] != userPromptUUID {
		t.Errorf("parent_span = %v, want %q (should walk past tool_result)", s["parent_span"], userPromptUUID)
	}
}

// TestExtractAssistantText_MultipleBlocks verifies that multiple text blocks
// are concatenated in order.
func TestExtractAssistantText_MultipleBlocks(t *testing.T) {
	rec := &transcriptRecord{}
	rec.Message.Content = []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{
		{Type: "text", Text: "Part one. "},
		{Type: "thinking", Text: "should be skipped"},
		{Type: "text", Text: "Part two."},
	}
	got := extractAssistantText(rec)
	want := "Part one. Part two."
	if got != want {
		t.Errorf("extractAssistantText = %q, want %q", got, want)
	}
}

// TestAssistantTextSignalID_Deterministic verifies that the signal ID is
// deterministic (same UUID always produces the same signal_id).
func TestAssistantTextSignalID_Deterministic(t *testing.T) {
	id1 := assistantTextSignalID("some-uuid-1234")
	id2 := assistantTextSignalID("some-uuid-1234")
	if id1 != id2 {
		t.Errorf("signal IDs differ for same input: %q vs %q", id1, id2)
	}
	// Different UUIDs should produce different signal_ids.
	id3 := assistantTextSignalID("other-uuid-5678")
	if id1 == id3 {
		t.Errorf("expected different signal IDs for different UUIDs, both = %q", id1)
	}
	// Verify length is 32 hex chars.
	if len(id1) != 32 {
		t.Errorf("signal_id length = %d, want 32", len(id1))
	}
	// Verify it's hex.
	validHex := "0123456789abcdef"
	for _, c := range id1 {
		if !strings.ContainsRune(validHex, c) {
			t.Errorf("signal_id contains non-hex char %q", c)
			break
		}
	}
}
