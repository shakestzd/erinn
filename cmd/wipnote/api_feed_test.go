package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/db"
)

func seedFeedEventsDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "feed.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	_, err = database.Exec(`INSERT INTO sessions (session_id, agent_assigned) VALUES (?, ?)`, "sess-feed-1", "codex-cli")
	if err != nil {
		t.Fatal(err)
	}

	mustInsert := func(args ...any) {
		t.Helper()
		_, err := database.Exec(`
			INSERT INTO otel_signals (
				signal_id, harness, session_id, kind, canonical, native, ts_micros,
				tool_name, model, duration_ms, success, decision, attrs_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			args...,
		)
		if err != nil {
			t.Fatalf("insert otel_signals: %v", err)
		}
	}

	mustInsert("sig-span", "codex", "sess-feed-1", "span", "api_request", "codex.llm_request", int64(1000), "", "gpt-5.1", int64(42), nil, "", `{"model":"gpt-5.1"}`)
	mustInsert("sig-log", "codex", "sess-feed-1", "log", "user_prompt", "codex.user_prompt", int64(0), "", "", int64(0), nil, "", `{"event.name":"user_prompt","event.timestamp":"1970-01-01T00:00:02Z"}`)

	return database
}

func TestEventsFeedHandler_IncludesCodexLogs(t *testing.T) {
	database := seedFeedEventsDB(t)

	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM otel_signals`).Scan(&count); err != nil {
		t.Fatalf("count otel_signals: %v", err)
	}
	if count != 2 {
		t.Fatalf("seeded rows = %d, want 2", count)
	}

	if err := database.QueryRow(`
		SELECT COUNT(*) FROM otel_signals s
		WHERE (
			(s.kind = 'span' AND s.canonical IN (
		      'interaction', 'api_request', 'tool_result',
		      'tool_execution', 'tool_blocked_on_user', 'subagent_invocation'
		    ))
			OR
			(s.kind = 'log' AND s.canonical IN (
		      'user_prompt', 'api_request', 'tool_result', 'tool_decision'
		    ))
		)`).Scan(&count); err != nil {
		t.Fatalf("count feed rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("feed rows = %d, want 2", count)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/events/feed?limit=10", nil)
	rec := httptest.NewRecorder()
	eventsFeedHandler(database).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Events []feedEvent `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(payload.Events))
	}
	if payload.Events[0].Type != "user_prompt" || payload.Events[0].Source != "otel" {
		t.Fatalf("first event = %+v, want codex log user_prompt", payload.Events[0])
	}
	if payload.Events[0].Timestamp != "1970-01-01T00:00:02Z" {
		t.Fatalf("first event timestamp = %q, want parsed attrs_json timestamp", payload.Events[0].Timestamp)
	}
	if payload.Events[1].Type != "api_request" || payload.Events[1].Source != "otel" {
		t.Fatalf("second event = %+v, want span api_request", payload.Events[1])
	}
}

func TestEventsFeedHandler_IncludesAssistantMessages(t *testing.T) {
	database := seedFeedEventsDB(t)
	_, err := database.Exec(`
		INSERT INTO messages (session_id, ordinal, role, content, timestamp, model)
		VALUES (?, ?, 'assistant', ?, ?, ?)`,
		"sess-feed-1", 1, "Done with the implementation", "1970-01-01T00:00:03Z", "gpt-5.5")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/events/feed?limit=10", nil)
	rec := httptest.NewRecorder()
	eventsFeedHandler(database).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Events []feedEvent `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Events) != 3 {
		t.Fatalf("events = %d, want 3", len(payload.Events))
	}
	if payload.Events[0].Type != "assistant_text" || payload.Events[0].Source != "message" {
		t.Fatalf("first event = %+v, want assistant message", payload.Events[0])
	}
	if payload.Events[0].Harness != "codex-cli" {
		t.Fatalf("harness = %q, want session fallback", payload.Events[0].Harness)
	}
	if payload.Events[0].Summary != "Done with the implementation" {
		t.Fatalf("summary = %q", payload.Events[0].Summary)
	}
}

func TestEventsFeedHandler_FiltersHookSystemPrompt(t *testing.T) {
	database := seedFeedEventsDB(t)
	_, err := database.Exec(`
		INSERT INTO agent_events (
			event_id, agent_id, event_type, timestamp, tool_name, input_summary, session_id, status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"uq-review", "claude-code", "tool_call", "1970-01-01T00:00:01Z", "UserQuery",
		"You are a code reviewer. Review the code changes shown below and provide feedback.",
		"sess-feed-1", "recorded")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/events/feed?limit=10", nil)
	rec := httptest.NewRecorder()
	eventsFeedHandler(database).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Events []feedEvent `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, ev := range payload.Events {
		if ev.ID == "uq-review" {
			t.Fatalf("reviewer prompt leaked into feed: %+v", ev)
		}
	}
}

func TestDeduplicateUserPromptLogs(t *testing.T) {
	tests := []struct {
		name     string
		events   []feedEvent
		wantLen  int
		wantType map[int]string // index -> type, only for preserved events
	}{
		{
			name: "gemini: any otel event triggers suppression",
			events: []feedEvent{
				{
					ID:        "ev1",
					Type:      "api_request",
					Source:    "otel",
					Harness:   "gemini_cli",
					SessionID: "sess-1",
					tsMicros:  1000,
				},
				{
					ID:        "ev2",
					Type:      "user_prompt",
					Source:    "otel",
					Harness:   "gemini_cli",
					SessionID: "sess-1",
					tsMicros:  1001,
				},
			},
			wantLen: 1,
			wantType: map[int]string{
				0: "api_request",
			},
		},
		{
			name: "codex: no gemini otel, user_prompt preserved",
			events: []feedEvent{
				{
					ID:        "ev1",
					Type:      "user_prompt",
					Source:    "otel",
					Harness:   "codex",
					SessionID: "sess-2",
					tsMicros:  2000,
				},
			},
			wantLen: 1,
			wantType: map[int]string{
				0: "user_prompt",
			},
		},
		{
			name: "antigravity: any otel event triggers suppression",
			events: []feedEvent{
				{
					ID:        "ev1",
					Type:      "api_request",
					Source:    "otel",
					Harness:   "antigravity_cli",
					SessionID: "sess-antigravity",
					tsMicros:  2500,
				},
				{
					ID:        "ev2",
					Type:      "user_prompt",
					Source:    "otel",
					Harness:   "antigravity_cli",
					SessionID: "sess-antigravity",
					tsMicros:  2501,
				},
			},
			wantLen: 1,
			wantType: map[int]string{
				0: "api_request",
			},
		},
		{
			name: "mixed: only gemini session affected",
			events: []feedEvent{
				{
					ID:        "ev1",
					Type:      "api_request",
					Source:    "otel",
					Harness:   "gemini_cli",
					SessionID: "sess-gemini",
					tsMicros:  3000,
				},
				{
					ID:        "ev2",
					Type:      "user_prompt",
					Source:    "otel",
					Harness:   "gemini_cli",
					SessionID: "sess-gemini",
					tsMicros:  3001,
				},
				{
					ID:        "ev3",
					Type:      "user_prompt",
					Source:    "otel",
					Harness:   "codex",
					SessionID: "sess-codex",
					tsMicros:  3002,
				},
			},
			wantLen: 2,
			wantType: map[int]string{
				0: "api_request", // preserved in original order
				1: "user_prompt", // codex event preserved (gemini user_prompt suppressed)
			},
		},
		{
			name: "gemini: user_prompt-only session preserved",
			events: []feedEvent{
				{
					ID:        "ev1",
					Type:      "user_prompt",
					Source:    "otel",
					Harness:   "gemini_cli",
					SessionID: "sess-prompt-only",
					tsMicros:  4000,
				},
			},
			wantLen: 1,
			wantType: map[int]string{
				0: "user_prompt",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := deduplicateUserPromptLogs(tt.events)
			if len(result) != tt.wantLen {
				t.Fatalf("got %d events, want %d", len(result), tt.wantLen)
			}
			for idx, wantEventType := range tt.wantType {
				if result[idx].Type != wantEventType {
					t.Fatalf("event[%d].Type = %q, want %q", idx, result[idx].Type, wantEventType)
				}
			}
		})
	}
}

func TestEventsFeedHandler_FiltersInternalPrompts(t *testing.T) {
	database := seedFeedEventsDB(t)

	mustInsert := func(args ...any) {
		t.Helper()
		_, err := database.Exec(`
			INSERT INTO otel_signals (
				signal_id, harness, session_id, kind, canonical, native, ts_micros,
				tool_name, model, duration_ms, success, decision, attrs_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			args...,
		)
		if err != nil {
			t.Fatalf("insert otel_signals: %v", err)
		}
	}

	mustInsert("sig-resume", "codex", "sess-feed-1", "log", "user_prompt", "codex.user_prompt", int64(3000), "", "", int64(0), nil, "", `{"text":"↩ resumed: background task bt8yi6rhf completed"}`)
	mustInsert("sig-reviewer", "codex", "sess-feed-1", "span", "interaction", "codex.interaction", int64(4000), "", "", int64(0), nil, "", `{"user_prompt":"You are a code reviewer. Review the code changes shown below and provide feedback."}`)

	req := httptest.NewRequest(http.MethodGet, "/api/events/feed?limit=10", nil)
	rec := httptest.NewRecorder()
	eventsFeedHandler(database).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Events []feedEvent `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, ev := range payload.Events {
		if ev.ID == "sig-resume" || ev.ID == "sig-reviewer" {
			t.Fatalf("unexpected internal event in feed: %+v", ev)
		}
	}
}
