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
		"type":       "user",
		"uuid":       uuid,
		"parentUuid": parentUUID,
		"sessionId":  sessionID,
		"timestamp":  "2026-04-20T09:59:00.000Z",
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
// transcript is written, Stop hook reads it, and an otel_signals row is
// inserted with the correct canonical and attrs.
func TestInsertAssistantTextSignal_HappyPath(t *testing.T) {
	td := setupTestDB(t)
	sessionID := "test-sess"
	projectDir := t.TempDir()

	path := writeTempTranscript(t, []string{
		makeTranscriptLine("asst-uuid-1", "user-uuid-1", sessionID, "end_turn", "Hello world!", false),
	})

	insertAssistantTextSignal(td.DB, projectDir, sessionID, path)

	var canonical, kindVal, spanID, parentSpan, attrsRaw string
	err := td.DB.QueryRow(`
		SELECT canonical, kind, COALESCE(span_id,''), COALESCE(parent_span,''), attrs_json
		FROM otel_signals
		WHERE session_id = ? AND canonical = 'assistant_text'`,
		sessionID,
	).Scan(&canonical, &kindVal, &spanID, &parentSpan, &attrsRaw)
	if err != nil {
		t.Fatalf("query otel_signals: %v", err)
	}
	if canonical != "assistant_text" {
		t.Errorf("canonical = %q, want %q", canonical, "assistant_text")
	}
	if kindVal != "log" {
		t.Errorf("kind = %q, want %q", kindVal, "log")
	}
	if spanID != "asst-uuid-1" {
		t.Errorf("span_id = %q, want %q", spanID, "asst-uuid-1")
	}
	if parentSpan != "user-uuid-1" {
		t.Errorf("parent_span = %q, want %q", parentSpan, "user-uuid-1")
	}

	var attrs map[string]any
	if err := json.Unmarshal([]byte(attrsRaw), &attrs); err != nil {
		t.Fatalf("unmarshal attrs: %v", err)
	}
	if attrs["text"] != "Hello world!" {
		t.Errorf("attrs[text] = %q, want %q", attrs["text"], "Hello world!")
	}
	if attrs["stop_reason"] != "end_turn" {
		t.Errorf("attrs[stop_reason] = %q, want %q", attrs["stop_reason"], "end_turn")
	}
}

// TestInsertAssistantTextSignal_Idempotent verifies that calling the function
// twice on the same transcript produces exactly one otel_signals row (INSERT OR IGNORE).
func TestInsertAssistantTextSignal_Idempotent(t *testing.T) {
	td := setupTestDB(t)
	sessionID := "test-sess"
	projectDir := t.TempDir()

	path := writeTempTranscript(t, []string{
		makeTranscriptLine("asst-idem", "user-idem", sessionID, "end_turn", "Same reply", false),
	})

	insertAssistantTextSignal(td.DB, projectDir, sessionID, path)
	insertAssistantTextSignal(td.DB, projectDir, sessionID, path)

	var count int
	if err := td.DB.QueryRow(`
		SELECT COUNT(*) FROM otel_signals WHERE session_id = ? AND canonical = 'assistant_text'`,
		sessionID,
	).Scan(&count); err != nil {
		t.Fatalf("query count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 row after two calls, got %d", count)
	}
}

func TestInsertAssistantTextSignalFromHookPayload_CodexStop(t *testing.T) {
	td := setupTestDB(t)
	sessionID := "test-sess"
	projectDir := t.TempDir()

	event := &CloudEvent{
		AgentID:              "codex",
		SessionID:            sessionID,
		TurnID:               "turn-1",
		Model:                "gpt-5.4",
		Timestamp:            "2026-05-08T10:00:00Z",
		LastAssistantMessage: "Codex answer",
		StopReason:           "end_turn",
	}

	if !insertAssistantTextSignalFromHookPayload(td.DB, projectDir, sessionID, event, event.LastAssistantMessage, "hook_payload") {
		t.Fatal("expected hook payload assistant_text insert to succeed")
	}
	if !insertAssistantTextSignalFromHookPayload(td.DB, projectDir, sessionID, event, event.LastAssistantMessage, "hook_payload") {
		t.Fatal("expected duplicate hook payload assistant_text insert to be treated as success")
	}

	var count int
	if err := td.DB.QueryRow(`
		SELECT COUNT(*) FROM otel_signals
		WHERE session_id = ? AND canonical = 'assistant_text'`,
		sessionID,
	).Scan(&count); err != nil {
		t.Fatalf("query count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one idempotent assistant_text row, got %d", count)
	}

	var harness, native, spanID, parentSpan, attrsRaw string
	var tsMicros int64
	if err := td.DB.QueryRow(`
		SELECT harness, native, COALESCE(span_id,''), COALESCE(parent_span,''), ts_micros, attrs_json
		FROM otel_signals
		WHERE session_id = ? AND canonical = 'assistant_text'`,
		sessionID,
	).Scan(&harness, &native, &spanID, &parentSpan, &tsMicros, &attrsRaw); err != nil {
		t.Fatalf("query assistant_text: %v", err)
	}
	if harness != "codex" {
		t.Errorf("harness = %q, want codex", harness)
	}
	if native != "codex.assistant_turn" {
		t.Errorf("native = %q, want codex.assistant_turn", native)
	}
	if spanID != "assistant:turn-1" {
		t.Errorf("span_id = %q, want assistant:turn-1", spanID)
	}
	if parentSpan != "turn-1" {
		t.Errorf("parent_span = %q, want turn-1", parentSpan)
	}
	if tsMicros != 1778234400000000 {
		t.Errorf("ts_micros = %d, want 1778234400000000", tsMicros)
	}

	var attrs map[string]any
	if err := json.Unmarshal([]byte(attrsRaw), &attrs); err != nil {
		t.Fatalf("unmarshal attrs: %v", err)
	}
	if attrs["text"] != "Codex answer" {
		t.Errorf("attrs[text] = %q, want Codex answer", attrs["text"])
	}
	if attrs["source"] != "hook_payload" {
		t.Errorf("attrs[source] = %q, want hook_payload", attrs["source"])
	}
	if attrs["model"] != "gpt-5.4" {
		t.Errorf("attrs[model] = %q, want gpt-5.4", attrs["model"])
	}
	if attrs["turn_id"] != "turn-1" {
		t.Errorf("attrs[turn_id] = %q, want turn-1", attrs["turn_id"])
	}
}

// TestInsertAssistantTextSignal_MissingFile verifies that a missing transcript
// does not produce a row or an error (silent skip).
func TestInsertAssistantTextSignal_MissingFile(t *testing.T) {
	td := setupTestDB(t)
	sessionID := "test-sess"
	projectDir := t.TempDir()

	insertAssistantTextSignal(td.DB, projectDir, sessionID, filepath.Join(t.TempDir(), "no-such.jsonl"))

	var count int
	if err := td.DB.QueryRow(`SELECT COUNT(*) FROM otel_signals WHERE session_id = ?`, sessionID).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows for missing transcript, got %d", count)
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

	var attrsRaw string
	err := td.DB.QueryRow(`SELECT attrs_json FROM otel_signals WHERE session_id = ? AND canonical = 'assistant_text'`, sessionID).Scan(&attrsRaw)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	var attrs map[string]any
	if err := json.Unmarshal([]byte(attrsRaw), &attrs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if attrs["interrupted"] != true {
		t.Errorf("expected attrs[interrupted]=true for max_tokens turn, got %v", attrs["interrupted"])
	}
	if attrs["stop_reason"] != "max_tokens" {
		t.Errorf("attrs[stop_reason] = %v, want max_tokens", attrs["stop_reason"])
	}
}

// TestInsertAssistantTextSignal_NoTranscriptPath verifies that an empty
// transcript_path skips all processing without error.
func TestInsertAssistantTextSignal_NoTranscriptPath(t *testing.T) {
	td := setupTestDB(t)
	sessionID := "test-sess"

	// Should not panic or return an error — just a silent skip.
	insertAssistantTextSignal(td.DB, t.TempDir(), sessionID, "")

	var count int
	if err := td.DB.QueryRow(`SELECT COUNT(*) FROM otel_signals WHERE session_id = ?`, sessionID).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows when transcript_path is empty, got %d", count)
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

	// Build the 4-record chain.
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

	var parentSpan string
	err := td.DB.QueryRow(`
		SELECT COALESCE(parent_span, '') FROM otel_signals
		WHERE session_id = ? AND canonical = 'assistant_text'`,
		sessionID,
	).Scan(&parentSpan)
	if err != nil {
		t.Fatalf("query otel_signals: %v", err)
	}

	if parentSpan != userPromptUUID {
		t.Errorf("parent_span = %q, want %q (should walk past tool_result)", parentSpan, userPromptUUID)
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
