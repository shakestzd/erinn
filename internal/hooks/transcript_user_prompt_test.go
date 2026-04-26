package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readNDJSONSignals reads .htmlgraph/sessions/<sid>/events.ndjson and
// returns each line parsed as a map. Used by hook tests to assert
// what NDJSON signals the hook emitted (replacing the old direct-INSERT
// SQLite assertion path).
func readNDJSONSignals(t *testing.T, projectDir, sessionID string) []map[string]any {
	t.Helper()
	path := filepath.Join(projectDir, ".htmlgraph", "sessions", sessionID, "events.ndjson")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read events.ndjson: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("parse line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// findSignal returns the first NDJSON entry matching the predicate, or nil.
func findSignal(signals []map[string]any, pred func(map[string]any) bool) map[string]any {
	for _, s := range signals {
		if pred(s) {
			return s
		}
	}
	return nil
}

// makeUserPromptLine returns a JSONL line for a user record with legacy string content.
func makeUserPromptLine(uuid, parentUUID, sessionID, text string, isSidechain bool) string {
	rec := map[string]any{
		"type":        "user",
		"uuid":        uuid,
		"parentUuid":  parentUUID,
		"sessionId":   sessionID,
		"requestId":   "req_" + uuid,
		"timestamp":   "2026-04-20T10:00:00.000Z",
		"isSidechain": isSidechain,
		"message": map[string]any{
			"role":    "user",
			"content": text, // legacy: plain string
		},
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

// makeUserPromptLineModern returns a JSONL line for a user record with modern array content.
func makeUserPromptLineModern(uuid, parentUUID, sessionID, text string, isSidechain bool) string {
	rec := map[string]any{
		"type":        "user",
		"uuid":        uuid,
		"parentUuid":  parentUUID,
		"sessionId":   sessionID,
		"requestId":   "req_" + uuid,
		"timestamp":   "2026-04-20T10:00:00.000Z",
		"isSidechain": isSidechain,
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

// makeToolResultLine returns a JSONL line for a user record that is a tool_result (not a human prompt).
func makeToolResultLine(uuid, sessionID string) string {
	rec := map[string]any{
		"type":        "user",
		"uuid":        uuid,
		"parentUuid":  "parent-" + uuid,
		"sessionId":   sessionID,
		"timestamp":   "2026-04-20T10:00:00.000Z",
		"isSidechain": false,
		"message": map[string]any{
			"role": "user",
			"content": []map[string]any{
				{
					"type":        "tool_result",
					"tool_use_id": "tool-abc",
					"content":     "tool output here",
				},
			},
		},
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

// makeImageOnlyLine returns a JSONL line for a user record with only image blocks.
func makeImageOnlyLine(uuid, sessionID string) string {
	rec := map[string]any{
		"type":        "user",
		"uuid":        uuid,
		"parentUuid":  "parent-" + uuid,
		"sessionId":   sessionID,
		"timestamp":   "2026-04-20T10:00:00.000Z",
		"isSidechain": false,
		"message": map[string]any{
			"role": "user",
			"content": []map[string]any{
				{
					"type":   "image",
					"source": map[string]any{"type": "base64", "media_type": "image/png", "data": "abc"},
				},
			},
		},
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

// writeUserTranscript writes lines to a temp file and returns its path.
func writeUserTranscript(t *testing.T, lines []string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "user-transcript-*.jsonl")
	if err != nil {
		t.Fatalf("create temp transcript: %v", err)
	}
	defer f.Close()
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
	return f.Name()
}

// --- backfillMissedUserPrompts tests ---

// TestBackfillMissedUserPrompts_LegacyStringFormat verifies the happy path with
// legacy string content: a plain text prompt is correctly extracted and emitted
// as a UnifiedSignal NDJSON line.
func TestBackfillMissedUserPrompts_LegacyStringFormat(t *testing.T) {
	td := setupTestDB(t)
	sessionID := "test-sess"
	projectDir := t.TempDir()

	path := writeUserTranscript(t, []string{
		makeUserPromptLine("u1", "", sessionID, "what's the plan?", false),
	})

	n, err := backfillMissedUserPrompts(td.DB, projectDir, sessionID, path)
	if err != nil {
		t.Fatalf("backfillMissedUserPrompts: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 emitted signal, got %d", n)
	}

	signals := readNDJSONSignals(t, projectDir, sessionID)
	if len(signals) != 1 {
		t.Fatalf("want 1 NDJSON line, got %d", len(signals))
	}
	s := signals[0]
	if s["canonical"] != "user_prompt" {
		t.Errorf("canonical = %v, want user_prompt", s["canonical"])
	}
	if s["span_id"] != "u1" {
		t.Errorf("span_id = %v, want u1", s["span_id"])
	}
	attrs, _ := s["attrs"].(map[string]any)
	if attrs["text"] != "what's the plan?" {
		t.Errorf("attrs.text = %v, want %q", attrs["text"], "what's the plan?")
	}
	if attrs["source"] != "transcript_backfill" {
		t.Errorf("attrs.source = %v, want transcript_backfill", attrs["source"])
	}
}

// TestBackfillMissedUserPrompts_ModernTextBlockFormat verifies the happy path with
// modern array content: the first text block is extracted and emitted.
func TestBackfillMissedUserPrompts_ModernTextBlockFormat(t *testing.T) {
	td := setupTestDB(t)
	sessionID := "test-sess"
	projectDir := t.TempDir()

	path := writeUserTranscript(t, []string{
		makeUserPromptLineModern("u2", "parent-u2", sessionID, "next step?", false),
	})

	n, err := backfillMissedUserPrompts(td.DB, projectDir, sessionID, path)
	if err != nil {
		t.Fatalf("backfillMissedUserPrompts: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 emitted signal, got %d", n)
	}

	signals := readNDJSONSignals(t, projectDir, sessionID)
	s := findSignal(signals, func(m map[string]any) bool {
		return m["canonical"] == "user_prompt" && m["span_id"] == "u2"
	})
	if s == nil {
		t.Fatalf("user_prompt signal with span_id=u2 not found in NDJSON; got %d signals", len(signals))
	}
	if s["parent_span"] != "parent-u2" {
		t.Errorf("parent_span = %v, want parent-u2", s["parent_span"])
	}
	attrs, _ := s["attrs"].(map[string]any)
	if attrs["text"] != "next step?" {
		t.Errorf("attrs.text = %v, want %q", attrs["text"], "next step?")
	}
}

// TestBackfillMissedUserPrompts_SkipToolResult verifies that user records whose
// content array starts with a tool_result block are not inserted.
func TestBackfillMissedUserPrompts_SkipToolResult(t *testing.T) {
	td := setupTestDB(t)
	sessionID := "test-sess"
	projectDir := t.TempDir()

	path := writeUserTranscript(t, []string{
		makeToolResultLine("tool-result-uuid", sessionID),
	})

	n, err := backfillMissedUserPrompts(td.DB, projectDir, sessionID, path)
	if err != nil {
		t.Fatalf("backfillMissedUserPrompts: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 emitted rows for tool_result, got %d", n)
	}
	if got := readNDJSONSignals(t, projectDir, sessionID); len(got) != 0 {
		t.Errorf("expected 0 NDJSON lines for tool_result, got %d", len(got))
	}
}

// TestBackfillMissedUserPrompts_SkipSidechain verifies that user records with
// isSidechain=true are skipped (subagent internals, not main-thread prompts).
func TestBackfillMissedUserPrompts_SkipSidechain(t *testing.T) {
	td := setupTestDB(t)
	sessionID := "test-sess"
	projectDir := t.TempDir()

	path := writeUserTranscript(t, []string{
		makeUserPromptLine("sidechain-uuid", "", sessionID, "sidechain prompt", true),
	})

	n, err := backfillMissedUserPrompts(td.DB, projectDir, sessionID, path)
	if err != nil {
		t.Fatalf("backfillMissedUserPrompts: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 rows for sidechain record, got %d", n)
	}
}

// TestBackfillMissedUserPrompts_SkipImageOnly verifies that user records with
// only image blocks (no text) are skipped.
func TestBackfillMissedUserPrompts_SkipImageOnly(t *testing.T) {
	td := setupTestDB(t)
	sessionID := "test-sess"
	projectDir := t.TempDir()

	path := writeUserTranscript(t, []string{
		makeImageOnlyLine("image-uuid", sessionID),
	})

	n, err := backfillMissedUserPrompts(td.DB, projectDir, sessionID, path)
	if err != nil {
		t.Fatalf("backfillMissedUserPrompts: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 rows for image-only record, got %d", n)
	}
}

// TestBackfillMissedUserPrompts_Idempotent verifies that the hook emits the
// same signal_id on every backfill pass. End-to-end idempotency at the
// otel_signals layer is provided by the indexer's INSERT OR IGNORE
// (covered by TestIndexer_IdempotentReplay in internal/otel/indexer/);
// here we only verify the hook side: signal_id is stable per uuid, and
// running twice does not produce diverging signal_ids.
func TestBackfillMissedUserPrompts_Idempotent(t *testing.T) {
	td := setupTestDB(t)
	sessionID := "test-sess"
	projectDir := t.TempDir()

	path := writeUserTranscript(t, []string{
		makeUserPromptLine("idem-uuid-1", "", sessionID, "first prompt", false),
		makeUserPromptLine("idem-uuid-2", "idem-uuid-1", sessionID, "second prompt", false),
	})

	n1, err := backfillMissedUserPrompts(td.DB, projectDir, sessionID, path)
	if err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	if n1 != 2 {
		t.Errorf("first run: expected 2 emitted, got %d", n1)
	}

	n2, err := backfillMissedUserPrompts(td.DB, projectDir, sessionID, path)
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if n2 != 2 {
		t.Errorf("second run: expected 2 emitted (indexer dedups by signal_id), got %d", n2)
	}

	signals := readNDJSONSignals(t, projectDir, sessionID)
	if len(signals) != 4 {
		t.Fatalf("expected 4 NDJSON lines after two runs, got %d", len(signals))
	}
	wantID1 := userPromptSignalID("idem-uuid-1")
	wantID2 := userPromptSignalID("idem-uuid-2")
	id1Count, id2Count := 0, 0
	for _, s := range signals {
		switch s["signal_id"] {
		case wantID1:
			id1Count++
		case wantID2:
			id2Count++
		}
	}
	if id1Count != 2 || id2Count != 2 {
		t.Errorf("signal_id stability: id1=%d, id2=%d (want 2 each)", id1Count, id2Count)
	}
}

// TestBackfillMissedUserPrompts_MissingTranscriptFile verifies that a missing
// transcript path returns no error and inserts no rows (graceful degrade).
func TestBackfillMissedUserPrompts_MissingTranscriptFile(t *testing.T) {
	td := setupTestDB(t)
	sessionID := "test-sess"
	projectDir := t.TempDir()

	n, err := backfillMissedUserPrompts(td.DB, projectDir, sessionID, filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 emitted rows for missing file, got %d", n)
	}
	if got := readNDJSONSignals(t, projectDir, sessionID); len(got) != 0 {
		t.Errorf("expected 0 NDJSON lines for missing transcript, got %d", len(got))
	}
}
