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

// seedOtelSignals creates a session and two prompts' worth of
// api_request + tool_result + api_error signals mirroring the
// empirical Claude fixtures used in the materializer tests.
func seedOtelSignals(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "api-otel.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	sessionID := "sess-api-1"
	database.Exec(`INSERT INTO sessions (session_id, agent_assigned) VALUES (?, ?)`, sessionID, "claude-code")

	insert := func(id, prompt, canonical, kind string, ts int64, tokIn, tokOut, cr, cc int64, cost float64, dur int64, attempt int) {
		t.Helper()
		_, err := database.Exec(`
			INSERT INTO otel_signals (
				signal_id, harness, session_id, prompt_id, kind, canonical, native,
				ts_micros, model, tokens_in, tokens_out,
				tokens_cache_read, tokens_cache_creation,
				cost_usd, cost_source, duration_ms, attempt, attrs_json
			) VALUES (?, 'claude_code', ?, ?, ?, ?, 'claude_code.'||?, ?, 'claude-haiku-4-5', ?, ?, ?, ?, ?, 'vendor', ?, ?, '{}')`,
			id, sessionID, prompt, kind, canonical, canonical,
			ts, tokIn, tokOut, cr, cc, cost, dur, attempt)
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	insert("s1", "prompt-A", "api_request", "log", 1, 10, 577, 23276, 2261, 0.00804885, 5835, 1)
	insert("s2", "prompt-A", "tool_result", "log", 2, 0, 0, 0, 0, 0, 100, 0)
	insert("s3", "prompt-B", "api_request", "log", 3, 3, 87, 0, 16623, 0.02121675, 1635, 1)
	insert("s4", "prompt-B", "api_error", "log", 4, 0, 0, 0, 0, 0, 30000, 11)
	return database
}

func TestOtelRollupHandler_LivePath(t *testing.T) {
	database := seedOtelSignals(t)

	req := httptest.NewRequest(http.MethodGet, "/api/otel/rollup?session_id=sess-api-1", nil)
	rec := httptest.NewRecorder()
	otelRollupHandler(database).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got rollupJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SessionID != "sess-api-1" {
		t.Errorf("SessionID = %q", got.SessionID)
	}
	if !got.Live {
		t.Errorf("Live = false, want true (no materialized row)")
	}
	wantCost := 0.00804885 + 0.02121675
	if got.TotalCostUSD < wantCost-1e-9 || got.TotalCostUSD > wantCost+1e-9 {
		t.Errorf("TotalCostUSD = %v, want %v", got.TotalCostUSD, wantCost)
	}
	if got.TotalAPIErrors != 1 {
		t.Errorf("TotalAPIErrors = %d", got.TotalAPIErrors)
	}
	if got.MaxAttempt != 11 {
		t.Errorf("MaxAttempt = %d", got.MaxAttempt)
	}
}

func TestOtelRollupHandler_MaterializedPath(t *testing.T) {
	database := seedOtelSignals(t)
	// Pre-write a materialized row so the handler hits the fast path.
	_, err := database.Exec(`
		INSERT INTO otel_session_rollup (
			session_id, harness, total_cost_usd,
			total_tokens_in, total_tokens_out,
			total_tokens_cache_read, total_tokens_cache_creation,
			total_tokens_thought, total_tokens_tool, total_tokens_reasoning,
			total_turns, total_tool_calls, total_api_calls, total_api_errors,
			max_attempt, materialized_at
		) VALUES ('sess-api-1', 'claude_code', 0.0325479,
			18, 664, 23276, 18884, 0, 0, 0,
			2, 1, 2, 1,
			11, 0)`)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/otel/rollup?session_id=sess-api-1", nil)
	rec := httptest.NewRecorder()
	otelRollupHandler(database).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got rollupJSON
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Live {
		t.Error("Live = true, want false (materialized row present)")
	}
	if got.TotalTokensIn != 18 {
		t.Errorf("TotalTokensIn = %d, want 18 (materialized value)", got.TotalTokensIn)
	}
}

func TestOtelRollupHandler_404ForMissingSession(t *testing.T) {
	database := seedOtelSignals(t)
	req := httptest.NewRequest(http.MethodGet, "/api/otel/rollup?session_id=nonexistent", nil)
	rec := httptest.NewRecorder()
	otelRollupHandler(database).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestOtelRollupHandler_400ForMissingParam(t *testing.T) {
	database := seedOtelSignals(t)
	req := httptest.NewRequest(http.MethodGet, "/api/otel/rollup", nil)
	rec := httptest.NewRecorder()
	otelRollupHandler(database).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestOtelPromptsHandler(t *testing.T) {
	database := seedOtelSignals(t)
	req := httptest.NewRequest(http.MethodGet, "/api/otel/prompts?session_id=sess-api-1", nil)
	rec := httptest.NewRecorder()
	otelPromptsHandler(database).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Prompts []promptJSON `json:"prompts"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Prompts) != 2 {
		t.Fatalf("got %d prompts, want 2", len(body.Prompts))
	}
	if body.Prompts[0].PromptID != "prompt-A" {
		t.Errorf("first prompt = %q, want prompt-A", body.Prompts[0].PromptID)
	}
	// prompt-B has the api_error; it should surface on the breakdown.
	if body.Prompts[1].APIErrors != 1 {
		t.Errorf("prompt-B APIErrors = %d, want 1", body.Prompts[1].APIErrors)
	}
}

func TestOtelLogsHandler_IncludesAssistantMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("drives otel handler integration flow")
	}

	database := seedOtelSignals(t)
	_, err := database.Exec(`
		INSERT INTO messages (
			session_id, ordinal, role, content, timestamp, model, uuid, parent_uuid
		) VALUES (?, ?, 'assistant', ?, ?, ?, ?, ?)`,
		"sess-api-1", 1, "Here is the answer", "2026-05-08T10:00:01.123Z",
		"claude-sonnet-4-6", "asst-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/otel/logs?session_id=sess-api-1", nil)
	rec := httptest.NewRecorder()
	otelLogsHandler(database).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Logs []struct {
			SignalID   string `json:"signal_id"`
			SpanID     string `json:"span_id"`
			ParentSpan string `json:"parent_span"`
			Canonical  string `json:"canonical"`
			AttrsJSON  string `json:"attrs_json"`
		} `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Logs) != 1 {
		t.Fatalf("logs = %d, want 1", len(body.Logs))
	}
	if body.Logs[0].Canonical != "assistant_text" {
		t.Fatalf("canonical = %q, want assistant_text", body.Logs[0].Canonical)
	}
	if body.Logs[0].SpanID != "asst-1" || body.Logs[0].ParentSpan != "user-1" {
		t.Fatalf("ids = %+v, want transcript UUID linkage", body.Logs[0])
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(body.Logs[0].AttrsJSON), &attrs); err != nil {
		t.Fatalf("attrs decode: %v", err)
	}
	if attrs["text"] != "Here is the answer" || attrs["source"] != "messages" {
		t.Fatalf("attrs = %+v", attrs)
	}
}

func TestOtelCostHandler_GroupByModel(t *testing.T) {
	database := seedOtelSignals(t)
	req := httptest.NewRequest(http.MethodGet, "/api/otel/cost?group_by=model", nil)
	rec := httptest.NewRecorder()
	otelCostHandler(database).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		GroupBy string `json:"group_by"`
		Buckets []struct {
			Key       string  `json:"key"`
			TotalCost float64 `json:"total_cost_usd"`
		} `json:"buckets"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.GroupBy != "model" {
		t.Errorf("GroupBy = %q", body.GroupBy)
	}
	if len(body.Buckets) != 1 {
		t.Fatalf("got %d buckets, want 1", len(body.Buckets))
	}
	if body.Buckets[0].Key != "claude-haiku-4-5" {
		t.Errorf("Key = %q", body.Buckets[0].Key)
	}
}

func TestOtelCostHandler_GroupBySession(t *testing.T) {
	database := seedOtelSignals(t)
	req := httptest.NewRequest(http.MethodGet, "/api/otel/cost?group_by=session", nil)
	rec := httptest.NewRecorder()
	otelCostHandler(database).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestOtelCostHandler_BadGroupBy(t *testing.T) {
	database := seedOtelSignals(t)
	req := httptest.NewRequest(http.MethodGet, "/api/otel/cost?group_by=bogus", nil)
	rec := httptest.NewRecorder()
	otelCostHandler(database).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestOtelHandlers_RejectNonGet(t *testing.T) {
	database := seedOtelSignals(t)
	for _, h := range []http.HandlerFunc{
		otelRollupHandler(database),
		otelPromptsHandler(database),
		otelCostHandler(database),
		otelSpansHandler(database),
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/otel/x?session_id=sess-api-1", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST status = %d, want 405", rec.Code)
		}
	}
}

// TestOtelSpansHandler inserts two spans with a parent/child link and
// verifies the endpoint returns them with trace_id / parent_span / span_id
// preserved so the client-side tree builder has every field it needs.
func TestOtelSpansHandler(t *testing.T) {
	if testing.Short() {
		t.Skip("drives otel handler integration flow")
	}

	database := seedOtelSignals(t)
	// seedOtelSignals doesn't add spans — insert two here under sess-api-1.
	_, err := database.Exec(`
		INSERT INTO otel_signals (
			signal_id, harness, session_id, kind, canonical, native,
			ts_micros, trace_id, span_id, parent_span,
			tool_name, duration_ms, attrs_json
		) VALUES
		('span-root', 'claude_code', 'sess-api-1', 'span', 'interaction', 'claude_code.interaction',
			100, 'trace-1', 'span-root-id', '', '', 25000, '{}'),
		('span-tool', 'claude_code', 'sess-api-1', 'span', 'tool_result', 'claude_code.tool',
			200, 'trace-1', 'span-tool-id', 'span-root-id', 'Bash', 6799, '{}')`)
	if err != nil {
		t.Fatalf("seed spans: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/otel/spans?session_id=sess-api-1", nil)
	rec := httptest.NewRecorder()
	otelSpansHandler(database).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Spans []spanJSON `json:"spans"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(body.Spans))
	}
	if body.Spans[0].SpanID != "span-root-id" || body.Spans[0].ParentSpan != "" {
		t.Errorf("root span wrong: %+v", body.Spans[0])
	}
	if body.Spans[1].ParentSpan != "span-root-id" {
		t.Errorf("child ParentSpan = %q, want span-root-id", body.Spans[1].ParentSpan)
	}
	if body.Spans[1].ToolName != "Bash" {
		t.Errorf("child ToolName = %q", body.Spans[1].ToolName)
	}
}

func TestOtelSpansHandler_400ForMissingParam(t *testing.T) {
	database := seedOtelSignals(t)
	req := httptest.NewRequest(http.MethodGet, "/api/otel/spans", nil)
	rec := httptest.NewRecorder()
	otelSpansHandler(database).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestOtelSpansHandler_ConcurrentSubagentModelAttribution verifies that when
// two subagents run concurrently (patch-coder and feature-coder dispatched by
// an Opus orchestrator), the /api/otel/spans response exposes each span's OWN
// model field so the client-side _indexSpans absorption logic can attribute
// Task/Agent rows to the dispatching orchestrator rather than a concurrent
// subagent's mis-parented api_request.
//
// Regression for bug-5d7220f3: haiku subagent api_requests were leaking into
// the orchestrator's Task row because the OTel receiver's strategy-B
// re-attribution is skipped when two subagents run concurrently (ambiguous
// overlap), leaving haiku's api_requests as siblings of the orchestrator's
// Task spans in the shared interaction span. The client-side fix uses
// per-span model fields to detect and reject cross-agent absorptions.
func TestOtelSpansHandler_ConcurrentSubagentModelAttribution(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concurrent-subagent.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	sessionID := "sess-concurrent"
	database.Exec(`INSERT INTO sessions (session_id, agent_assigned) VALUES (?, ?)`, sessionID, "claude-code")

	// Orchestrator interaction span (Opus).
	_, err = database.Exec(`
		INSERT INTO otel_signals (
			signal_id, harness, session_id, kind, canonical, native,
			ts_micros, trace_id, span_id, parent_span, tool_name, model, attrs_json
		) VALUES
		('orch-interact', 'claude_code', ?, 'span', 'interaction', 'claude_code.interaction',
			1000, 'trace-A', 'orch-root', '', '', NULL, '{}')`,
		sessionID)
	if err != nil {
		t.Fatalf("seed interaction: %v", err)
	}

	// Orchestrator (Opus) api_request that dispatches both subagents.
	_, err = database.Exec(`
		INSERT INTO otel_signals (
			signal_id, harness, session_id, kind, canonical, native,
			ts_micros, trace_id, span_id, parent_span, model, tokens_in, tokens_out, attrs_json
		) VALUES
		('orch-api-req', 'claude_code', ?, 'span', 'api_request', 'claude_code.llm_request',
			1100, 'trace-A', 'orch-api-span', 'orch-root', 'claude-opus-4-7', 5000, 200, '{}')`,
		sessionID)
	if err != nil {
		t.Fatalf("seed orch api_request: %v", err)
	}

	// Orchestrator Bash call (proves Opus is the orchestrator model).
	_, err = database.Exec(`
		INSERT INTO otel_signals (
			signal_id, harness, session_id, kind, canonical, native,
			ts_micros, trace_id, span_id, parent_span, tool_name, attrs_json
		) VALUES
		('orch-bash', 'claude_code', ?, 'span', 'tool_result', 'claude_code.tool',
			1200, 'trace-A', 'orch-bash-span', 'orch-root', 'Bash', '{}')`,
		sessionID)
	if err != nil {
		t.Fatalf("seed orch bash: %v", err)
	}

	// Orchestrator api_request that triggers Task dispatch.
	_, err = database.Exec(`
		INSERT INTO otel_signals (
			signal_id, harness, session_id, kind, canonical, native,
			ts_micros, trace_id, span_id, parent_span, model, tokens_in, tokens_out, attrs_json
		) VALUES
		('orch-api-req-2', 'claude_code', ?, 'span', 'api_request', 'claude_code.llm_request',
			1300, 'trace-A', 'orch-api-span-2', 'orch-root', 'claude-opus-4-7', 4800, 180, '{}')`,
		sessionID)
	if err != nil {
		t.Fatalf("seed orch api_request 2: %v", err)
	}

	// Haiku subagent's api_request — mis-parented to orch-root (strategy B
	// ambiguity means re-attribution was skipped). This appears chronologically
	// between orch-api-req-2 and the Task span, which is the exact scenario
	// that causes the wrong _precedingApi absorption on the client side.
	_, err = database.Exec(`
		INSERT INTO otel_signals (
			signal_id, harness, session_id, kind, canonical, native,
			ts_micros, trace_id, span_id, parent_span, model, tokens_in, tokens_out, attrs_json
		) VALUES
		('haiku-api-req', 'claude_code', ?, 'span', 'api_request', 'claude_code.llm_request',
			1350, 'trace-A', 'haiku-api-span', 'orch-root', 'claude-haiku-4-5', 6, 6, '{}')`,
		sessionID)
	if err != nil {
		t.Fatalf("seed haiku api_request: %v", err)
	}

	// Task span dispatching feature-coder — this is what the Opus orchestrator spawns.
	_, err = database.Exec(`
		INSERT INTO otel_signals (
			signal_id, harness, session_id, kind, canonical, native,
			ts_micros, trace_id, span_id, parent_span, tool_name, model, attrs_json
		) VALUES
		('task-sonnet', 'claude_code', ?, 'span', 'subagent_invocation', 'claude_code.tool',
			1400, 'trace-A', 'task-sonnet-span', 'orch-root', 'Agent', NULL, '{"subagent_type":"wipnote:feature-coder"}')`,
		sessionID)
	if err != nil {
		t.Fatalf("seed Task sonnet span: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/otel/spans?session_id="+sessionID, nil)
	rec := httptest.NewRecorder()
	otelSpansHandler(database).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Spans []spanJSON `json:"spans"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Index spans by signal_id for easy lookup.
	byID := map[string]spanJSON{}
	for _, s := range body.Spans {
		byID[s.SignalID] = s
	}

	// The orchestrator's api_requests must carry the Opus model.
	for _, id := range []string{"orch-api-req", "orch-api-req-2"} {
		s, ok := byID[id]
		if !ok {
			t.Errorf("span %s missing from response", id)
			continue
		}
		if s.Model != "claude-opus-4-7" {
			t.Errorf("span %s model = %q, want claude-opus-4-7", id, s.Model)
		}
	}

	// The haiku subagent's api_request must carry the Haiku model, so the
	// client-side guard can detect and reject it as a mis-attributed span when
	// it appears between an orchestrator api_request and a Task span.
	haikuSpan, ok := byID["haiku-api-req"]
	if !ok {
		t.Fatal("haiku api_request span missing from response")
	}
	if haikuSpan.Model != "claude-haiku-4-5" {
		t.Errorf("haiku api_request model = %q, want claude-haiku-4-5", haikuSpan.Model)
	}

	// The Task/Agent span (dispatching feature-coder) must have NO model of its
	// own — the model badge comes from the _precedingApi on the client side, NOT
	// from this span's model field. If a model were stored here, the client would
	// show it regardless of the orchestrator-model guard logic.
	taskSpan, ok := byID["task-sonnet"]
	if !ok {
		t.Fatal("Task sonnet span missing from response")
	}
	if taskSpan.Model != "" {
		t.Errorf("Task span model = %q, want empty (model must come from _precedingApi on client)", taskSpan.Model)
	}

	// The Task span's parent_span must be orch-root so the client-side _indexSpans
	// places it in the same children array as orch-api-req-2. The client guard
	// then uses orchModel (derived from Bash→orch-api-req-1 pairing) to reject
	// haiku-api-req as _precedingApi for this Task span.
	if taskSpan.ParentSpan != "orch-root" {
		t.Errorf("Task span parent_span = %q, want orch-root", taskSpan.ParentSpan)
	}
	// haiku-api-req must also be parented to orch-root (the mis-parented scenario).
	if haikuSpan.ParentSpan != "orch-root" {
		t.Errorf("haiku api_request parent_span = %q, want orch-root (mis-parented scenario)", haikuSpan.ParentSpan)
	}
}

func TestOtelSpansHandler_CodexDetailsAndToolInput(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "codex-details.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	sessionID := "codex-sess"
	database.Exec(`INSERT INTO sessions (session_id, agent_assigned) VALUES (?, ?)`, sessionID, "codex")
	_, err = database.Exec(`
		INSERT INTO otel_signals (
			signal_id, harness, session_id, kind, canonical, native,
			ts_micros, trace_id, span_id, parent_span, tool_name, model,
			tokens_in, tokens_out, duration_ms, attrs_json
		) VALUES
		('codex-api', 'codex', ?, 'span', 'api_request', 'gen_ai.client.operation',
			100, 'trace-codex', 'api-span', '', '', 'gpt-5.1-codex',
			1234, 567, 890, '{"request.id":"req_abc","approval_policy":"on-request","event.kind":"turn"}'),
		('codex-bash', 'codex', ?, 'span', 'tool_result', 'mcp.tools.call',
			200, 'trace-codex', 'bash-span', 'api-span', 'Bash', '',
			0, 0, 42, '{}')`,
		sessionID, sessionID)
	if err != nil {
		t.Fatalf("seed spans: %v", err)
	}
	_, err = database.Exec(`
		INSERT INTO otel_signals (
			signal_id, harness, session_id, kind, canonical, native,
			ts_micros, trace_id, span_id, tool_name, attrs_json
		) VALUES
		('codex-bash-log', 'codex', ?, 'log', 'tool_result', 'codex.tool_result',
			210, 'trace-codex', 'bash-log', 'Bash',
			'{"tool_name":"Bash","tool_input":{"command":"go test ./cmd/wipnote","timeout":60000},"success":"true"}')`,
		sessionID)
	if err != nil {
		t.Fatalf("seed tool log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/otel/spans?session_id="+sessionID, nil)
	rec := httptest.NewRecorder()
	otelSpansHandler(database).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Spans []spanJSON `json:"spans"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]spanJSON{}
	for _, s := range body.Spans {
		byID[s.SignalID] = s
	}
	api := byID["codex-api"]
	if api.Model != "gpt-5.1-codex" || api.TokensIn != 1234 || api.TokensOut != 567 || api.DurationMs != 890 {
		t.Fatalf("api span core fields wrong: %+v", api)
	}
	if api.Details.RequestID != "req_abc" || api.Details.Mode != "on-request" || api.Details.CommandType != "turn" {
		t.Fatalf("api details = %+v", api.Details)
	}
	bash := byID["codex-bash"]
	if bash.Details.FullCommand != "go test ./cmd/wipnote" || bash.Details.Timeout != 60000 {
		t.Fatalf("bash details = %+v", bash.Details)
	}
	if bash.Details.ToolInput == nil || bash.Details.ToolInput["command"] != "go test ./cmd/wipnote" {
		t.Fatalf("tool input not preserved: %+v", bash.Details.ToolInput)
	}
}

// JS coverage gap (bug-1e5166ea): the _indexSpans second-pass fix that attaches
// _modelRef to Task/Agent spans when the orchestrator dispatches N Tasks in one LLM
// turn lives entirely in event-tree.js. There is no JS test runner in this project,
// so the following scenario is untested at the automated level:
//
//   children = [api_request(Opus), Task#1, Task#2, ...]
//
// After the first-pass absorption, Task#1 gets _precedingApi = api_request(Opus) and
// Task#2..N get neither _precedingApi nor _modelRef. The second pass walks backward
// through the original kids list and attaches _modelRef = api_request(Opus) to each
// orphan Task. The renderer then uses _modelRef.model for the model pill (read-only)
// while leaving cost/tokens blank on those rows to avoid double-counting.
//
// The Go-side invariant we CAN assert is that the server returns Task spans with no
// model field of their own (see TestOtelSpansHandler_ParallelSubagentAttribution above)
// so the client is forced to derive the model from the api_request reference — the
// fix only matters when that derivation reaches Task#2..N. The Go test verifies the
// input shape is correct; the rendering logic itself requires a browser or JS runtime
// to validate end-to-end.
