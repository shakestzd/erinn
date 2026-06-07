package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

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
// legacy string content: a plain text prompt is correctly extracted and inserted.
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
		t.Errorf("expected 1 inserted row, got %d", n)
	}

	var canonical, spanID, attrsRaw string
	err = td.DB.QueryRow(`
		SELECT canonical, COALESCE(span_id,''), attrs_json
		FROM otel_signals
		WHERE session_id = ? AND canonical = 'user_prompt'`,
		sessionID,
	).Scan(&canonical, &spanID, &attrsRaw)
	if err != nil {
		t.Fatalf("query otel_signals: %v", err)
	}
	if canonical != "user_prompt" {
		t.Errorf("canonical = %q, want %q", canonical, "user_prompt")
	}
	if spanID != "u1" {
		t.Errorf("span_id = %q, want %q", spanID, "u1")
	}

	var attrs map[string]any
	if err := json.Unmarshal([]byte(attrsRaw), &attrs); err != nil {
		t.Fatalf("unmarshal attrs: %v", err)
	}
	if attrs["text"] != "what's the plan?" {
		t.Errorf("attrs[text] = %q, want %q", attrs["text"], "what's the plan?")
	}
	if attrs["source"] != "transcript_backfill" {
		t.Errorf("attrs[source] = %q, want %q", attrs["source"], "transcript_backfill")
	}
}

// TestBackfillMissedUserPrompts_ModernTextBlockFormat verifies the happy path with
// modern array content: the first text block is extracted and inserted.
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
		t.Errorf("expected 1 inserted row, got %d", n)
	}

	var attrsRaw, parentSpan string
	err = td.DB.QueryRow(`
		SELECT attrs_json, COALESCE(parent_span,'')
		FROM otel_signals
		WHERE session_id = ? AND canonical = 'user_prompt' AND span_id = 'u2'`,
		sessionID,
	).Scan(&attrsRaw, &parentSpan)
	if err != nil {
		t.Fatalf("query otel_signals: %v", err)
	}

	var attrs map[string]any
	if err := json.Unmarshal([]byte(attrsRaw), &attrs); err != nil {
		t.Fatalf("unmarshal attrs: %v", err)
	}
	if attrs["text"] != "next step?" {
		t.Errorf("attrs[text] = %q, want %q", attrs["text"], "next step?")
	}
	if parentSpan != "parent-u2" {
		t.Errorf("parent_span = %q, want %q", parentSpan, "parent-u2")
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
		t.Errorf("expected 0 inserted rows for tool_result, got %d", n)
	}

	var count int
	if err := td.DB.QueryRow(`SELECT COUNT(*) FROM otel_signals WHERE session_id = ?`, sessionID).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows in otel_signals for tool_result record, got %d", count)
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

// TestBackfillMissedUserPrompts_Idempotent verifies that running backfill twice
// on the same transcript produces exactly one row per uuid (INSERT OR IGNORE).
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
		t.Errorf("first run: expected 2 inserted, got %d", n1)
	}

	n2, err := backfillMissedUserPrompts(td.DB, projectDir, sessionID, path)
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second run: expected 0 new inserts (idempotent), got %d", n2)
	}

	var count int
	if err := td.DB.QueryRow(`SELECT COUNT(*) FROM otel_signals WHERE session_id = ? AND canonical = 'user_prompt'`, sessionID).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 2 {
		t.Errorf("expected exactly 2 rows after two runs, got %d", count)
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
		t.Errorf("expected 0 rows for missing file, got %d", n)
	}

	var count int
	if err := td.DB.QueryRow(`SELECT COUNT(*) FROM otel_signals WHERE session_id = ?`, sessionID).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows in DB for missing transcript, got %d", count)
	}
}
