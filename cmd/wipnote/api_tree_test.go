package main

import (
	"database/sql"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// openTreeTestDB creates an in-memory database with schema and a test session.
//
// Shared-cache in-memory DSN (NOT plain ":memory:"): buildEventTree issues nested
// queries while an outer *sql.Rows is still open (e.g. inside a rows.Next() loop),
// so database/sql may open a second connection. "file::memory:?cache=shared" makes
// every connection share ONE in-memory DB; plain ":memory:" would give the second
// connection a private empty DB and drop rows.
//
// SERIAL-ONLY: this DSN names a PROCESS-GLOBAL shared in-memory DB and is the
// IDENTICAL name openGraphTestDB uses, so it is safe only while every caller runs
// serially (no t.Parallel) and closes before the next opens. Before slice-3
// (feat-6ce99108) adds t.Parallel, give each test a UNIQUE shared-cache name,
// e.g. fmt.Sprintf("file:treetest_%s?mode=memory&cache=shared", t.Name()).
func openTreeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	now := time.Now().UTC()
	sess := &models.Session{
		SessionID:     "sess-test",
		AgentAssigned: "claude-code",
		CreatedAt:     now,
		Status:        "active",
	}
	if err := db.InsertSession(database, sess); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	return database
}

func mustExec(t *testing.T, database *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := database.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func TestBuildEventTree_SuppressesDuplicateAgentRows(t *testing.T) {
	database := openTreeTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	ts := now.Format(time.RFC3339)

	// Insert UserQuery anchor.
	mustExec(t, database,
		`INSERT INTO agent_events (event_id, agent_id, event_type, timestamp, tool_name, session_id, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"uq-1", "claude-code", "tool_call", ts, "UserQuery", "sess-test", "recorded")

	// Insert task_delegation/Task as child of UserQuery.
	mustExec(t, database,
		`INSERT INTO agent_events (event_id, agent_id, event_type, timestamp, tool_name, session_id, status, parent_event_id, subagent_type)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"td-1", "claude-code", "task_delegation", ts, "Task", "sess-test", "recorded", "uq-1", "researcher")

	// Insert duplicate tool_call/Agent as sibling of task_delegation.
	mustExec(t, database,
		`INSERT INTO agent_events (event_id, agent_id, event_type, timestamp, tool_name, session_id, status, parent_event_id, subagent_type)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"tc-dup", "claude-code", "tool_call", ts, "Agent", "sess-test", "recorded", "uq-1", "researcher")

	// Insert child Bash/Read/Edit under task_delegation.
	for i, tool := range []string{"Bash", "Read", "Edit"} {
		mustExec(t, database,
			`INSERT INTO agent_events (event_id, agent_id, event_type, timestamp, tool_name, session_id, status, parent_event_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			"child-"+string(rune('a'+i)), "claude-code", "tool_call", ts, tool, "sess-test", "recorded", "td-1")
	}

	turns, err := buildEventTree(database, 50)
	if err != nil {
		t.Fatalf("buildEventTree: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}

	children := turns[0].Children
	// Should have 1 child (task_delegation) — the tool_call/Agent duplicate is suppressed.
	if len(children) != 1 {
		t.Fatalf("got %d children, want 1 (duplicate Agent row should be suppressed)", len(children))
	}

	td := children[0]
	if td["event_type"] != "task_delegation" {
		t.Errorf("child event_type = %v, want task_delegation", td["event_type"])
	}
	if td["tool_name"] != "Task" {
		t.Errorf("child tool_name = %v, want Task", td["tool_name"])
	}

	// task_delegation should have 3 nested children (Bash, Read, Edit).
	nested, ok := td["children"].([]map[string]any)
	if !ok {
		t.Fatalf("task_delegation children type = %T, want []map[string]any", td["children"])
	}
	if len(nested) != 3 {
		t.Fatalf("got %d nested children, want 3", len(nested))
	}
	tools := map[string]bool{}
	for _, c := range nested {
		tn, _ := c["tool_name"].(string)
		tools[tn] = true
	}
	for _, want := range []string{"Bash", "Read", "Edit"} {
		if !tools[want] {
			t.Errorf("missing nested child tool_name %q", want)
		}
	}
}

func TestBuildEventTree_NoDelegation_KeepsAgentRows(t *testing.T) {
	database := openTreeTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	ts := now.Format(time.RFC3339)

	// Insert UserQuery.
	mustExec(t, database,
		`INSERT INTO agent_events (event_id, agent_id, event_type, timestamp, tool_name, session_id, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"uq-2", "claude-code", "tool_call", ts, "UserQuery", "sess-test", "recorded")

	// Insert tool_call/Agent without any sibling task_delegation — should be kept.
	mustExec(t, database,
		`INSERT INTO agent_events (event_id, agent_id, event_type, timestamp, tool_name, session_id, status, parent_event_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"tc-agent", "claude-code", "tool_call", ts, "Agent", "sess-test", "recorded", "uq-2")

	turns, err := buildEventTree(database, 50)
	if err != nil {
		t.Fatalf("buildEventTree: %v", err)
	}

	// Find the turn for uq-2.
	var found *turn
	for i := range turns {
		if uq, ok := turns[i].UserQuery["event_id"].(string); ok && uq == "uq-2" {
			found = &turns[i]
			break
		}
	}
	if found == nil {
		t.Fatal("turn for uq-2 not found")
	}
	if len(found.Children) != 1 {
		t.Fatalf("got %d children, want 1 (Agent row should be kept when no delegation sibling)", len(found.Children))
	}
	if found.Children[0]["tool_name"] != "Agent" {
		t.Errorf("child tool_name = %v, want Agent", found.Children[0]["tool_name"])
	}
}

func TestBuildEventTree_OtelPrimary(t *testing.T) {
	database := openTreeTestDB(t)
	defer database.Close()

	tsMicros := time.Now().UTC().UnixMicro()

	// Insert an interaction span — OTel turn anchor.
	mustExec(t, database,
		`INSERT INTO otel_signals
		 (signal_id, harness, session_id, kind, canonical, native, ts_micros, trace_id, span_id, attrs_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sig-interaction-1", "claude-code", "sess-test",
		"span", "interaction", "interaction",
		tsMicros, "trace-abc", "span-interaction-1",
		`{"user_prompt":"What is the capital of France?"}`)

	// Insert a tool_result span in the same trace.
	mustExec(t, database,
		`INSERT INTO otel_signals
		 (signal_id, harness, session_id, kind, canonical, native, ts_micros, trace_id, span_id, parent_span, tool_name, attrs_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sig-tool-1", "claude-code", "sess-test",
		"span", "tool_result", "tool_result",
		tsMicros+1000, "trace-abc", "span-tool-1", "span-interaction-1",
		"Bash", "{}")

	turns, err := buildEventTree(database, 50)
	if err != nil {
		t.Fatalf("buildEventTree: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}

	uq := turns[0].UserQuery
	if uq["event_id"] != "sig-interaction-1" {
		t.Errorf("event_id = %v, want sig-interaction-1", uq["event_id"])
	}
	if uq["tool_name"] != "UserQuery" {
		t.Errorf("tool_name = %v, want UserQuery", uq["tool_name"])
	}
	if uq["input_summary"] != "What is the capital of France?" {
		t.Errorf("input_summary = %v, want prompt text", uq["input_summary"])
	}
	if uq["session_id"] != "sess-test" {
		t.Errorf("session_id = %v, want sess-test", uq["session_id"])
	}
	if turns[0].Stats.ToolCount != 1 {
		t.Errorf("stats.tool_count = %d, want 1", turns[0].Stats.ToolCount)
	}
}

func TestBuildEventTree_OtelFallsBackToHook(t *testing.T) {
	database := openTreeTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	ts := now.Format(time.RFC3339)

	// Insert only a hook UserQuery — no OTel interaction spans.
	mustExec(t, database,
		`INSERT INTO agent_events (event_id, agent_id, event_type, timestamp, tool_name, session_id, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"uq-hook", "", "tool_call", ts, "UserQuery", "sess-test", "recorded")

	turns, err := buildEventTree(database, 50)
	if err != nil {
		t.Fatalf("buildEventTree: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}

	uq := turns[0].UserQuery
	if uq["event_id"] != "uq-hook" {
		t.Errorf("event_id = %v, want uq-hook (hook fallback)", uq["event_id"])
	}
	if uq["agent_id"] != "claude-code" {
		t.Errorf("agent_id = %v, want claude-code from session fallback", uq["agent_id"])
	}
}

func TestBuildEventTree_OtelPromptFromTrace(t *testing.T) {
	database := openTreeTestDB(t)
	defer database.Close()

	tsMicros := time.Now().UTC().UnixMicro()

	// Interaction span with no prompt in attrs_json.
	mustExec(t, database,
		`INSERT INTO otel_signals
		 (signal_id, harness, session_id, kind, canonical, native, ts_micros, trace_id, span_id, attrs_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sig-interaction-2", "claude-code", "sess-test",
		"span", "interaction", "interaction",
		tsMicros, "trace-xyz", "span-i2", `{}`)

	// user_prompt log in the same trace.
	mustExec(t, database,
		`INSERT INTO otel_signals
		 (signal_id, harness, session_id, kind, canonical, native, ts_micros, trace_id, span_id, attrs_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sig-prompt-2", "claude-code", "sess-test",
		"log", "user_prompt", "user_prompt",
		tsMicros-500, "trace-xyz", "span-prompt-2", `{"prompt":"Hello world"}`)

	turns, err := buildEventTree(database, 50)
	if err != nil {
		t.Fatalf("buildEventTree: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}

	if turns[0].UserQuery["input_summary"] != "Hello world" {
		t.Errorf("input_summary = %v, want 'Hello world' from trace user_prompt log",
			turns[0].UserQuery["input_summary"])
	}
	if turns[0].UserQuery["agent_id"] != "claude-code" {
		t.Errorf("agent_id = %v, want claude-code from OTel harness", turns[0].UserQuery["agent_id"])
	}
}

func TestBuildEventTree_OtelLogOnlyCodexTurn(t *testing.T) {
	database := openTreeTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	codexSess := &models.Session{
		SessionID:     "sess-codex-log-only",
		AgentAssigned: "codex",
		CreatedAt:     now,
		Status:        "active",
	}
	if err := db.InsertSession(database, codexSess); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	promptTS := now.UnixMicro()
	apiTS := now.Add(150 * time.Millisecond).UnixMicro()
	decisionTS := now.Add(300 * time.Millisecond).UnixMicro()
	toolTS := now.Add(450 * time.Millisecond).UnixMicro()

	mustExec(t, database,
		`INSERT INTO otel_signals
		 (signal_id, harness, session_id, prompt_id, kind, canonical, native, ts_micros, trace_id, attrs_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sig-codex-prompt", "codex", "sess-codex-log-only", "prompt-1",
		"log", "user_prompt", "codex.user_prompt", 0, "trace-codex-1",
		`{"prompt":"fix the dashboard activity page","event.timestamp":"`+time.UnixMicro(promptTS).UTC().Format(time.RFC3339)+`"}`)
	mustExec(t, database,
		`INSERT INTO otel_signals
		 (signal_id, harness, session_id, prompt_id, kind, canonical, native, ts_micros, trace_id, model, attrs_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sig-codex-api", "codex", "sess-codex-log-only", "prompt-1",
		"log", "api_request", "codex.api_request", 0, "trace-codex-1", "gpt-5.1",
		`{"model":"gpt-5.1","input_token_count":123,"output_token_count":45,"event.timestamp":"`+time.UnixMicro(apiTS).UTC().Format(time.RFC3339Nano)+`"}`)
	mustExec(t, database,
		`INSERT INTO otel_signals
		 (signal_id, harness, session_id, prompt_id, kind, canonical, native, ts_micros, trace_id, tool_name, decision, attrs_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sig-codex-decision", "codex", "sess-codex-log-only", "prompt-1",
		"log", "tool_decision", "codex.tool_decision", 0, "trace-codex-1", "Bash", "allowed",
		`{"tool_name":"Bash","decision":"allowed","event.timestamp":"`+time.UnixMicro(decisionTS).UTC().Format(time.RFC3339Nano)+`"}`)
	mustExec(t, database,
		`INSERT INTO otel_signals
		 (signal_id, harness, session_id, prompt_id, kind, canonical, native, ts_micros, trace_id, tool_name, success, attrs_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sig-codex-tool", "codex", "sess-codex-log-only", "prompt-1",
		"log", "tool_result", "codex.tool_result", 0, "trace-codex-1", "Bash", 1,
		`{"tool_name":"Bash","full_command":"go test ./cmd/wipnote","event.timestamp":"`+time.UnixMicro(toolTS).UTC().Format(time.RFC3339Nano)+`"}`)

	turns, err := buildEventTree(database, 50)
	if err != nil {
		t.Fatalf("buildEventTree: %v", err)
	}

	var codexTurn *turn
	for i := range turns {
		if id, _ := turns[i].UserQuery["event_id"].(string); id == "sig-codex-prompt" {
			codexTurn = &turns[i]
			break
		}
	}
	if codexTurn == nil {
		t.Fatalf("Codex log-only prompt missing from event tree: %+v", turns)
	}

	if got := codexTurn.UserQuery["input_summary"]; got != "fix the dashboard activity page" {
		t.Fatalf("input_summary = %v, want prompt text", got)
	}
	if got := codexTurn.UserQuery["timestamp"]; got != time.UnixMicro(promptTS).UTC().Format(time.RFC3339) {
		t.Fatalf("timestamp = %v, want event timestamp", got)
	}
	if codexTurn.Stats.ToolCount != 3 {
		t.Fatalf("stats.tool_count = %d, want 3", codexTurn.Stats.ToolCount)
	}
	if len(codexTurn.Children) != 3 {
		t.Fatalf("children = %d, want 3 log-derived rows", len(codexTurn.Children))
	}

	if codexTurn.Children[0]["event_id"] != "sig-codex-tool" {
		t.Fatalf("first child = %v, want newest tool_result row", codexTurn.Children[0]["event_id"])
	}
	if codexTurn.Children[1]["tool_name"] != "Bash" {
		t.Fatalf("second child tool_name = %v, want Bash tool_decision row", codexTurn.Children[1]["tool_name"])
	}
	if codexTurn.Children[2]["tool_name"] != "api_request" {
		t.Fatalf("third child tool_name = %v, want api_request row", codexTurn.Children[2]["tool_name"])
	}
}

func TestBuildEventTree_OtelLogOnlyCodexTurn_BoundsChildrenByPromptInterval(t *testing.T) {
	database := openTreeTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	codexSess := &models.Session{
		SessionID:     "sess-codex-log-window",
		AgentAssigned: "codex",
		CreatedAt:     now,
		Status:        "active",
	}
	if err := db.InsertSession(database, codexSess); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	prompt1TS := now.Add(-2 * time.Minute)
	child1TS := prompt1TS.Add(200 * time.Millisecond)
	prompt2TS := now.Add(-1 * time.Minute)
	child2TS := prompt2TS.Add(200 * time.Millisecond)
	child2ErrTS := prompt2TS.Add(400 * time.Millisecond)

	mustExec(t, database,
		`INSERT INTO otel_signals
		 (signal_id, harness, session_id, prompt_id, kind, canonical, native, ts_micros, trace_id, attrs_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sig-codex-prompt-1", "codex", "sess-codex-log-window", "prompt-1",
		"log", "user_prompt", "codex.user_prompt", 0, "trace-codex-window-1",
		`{"prompt":"please run the dashboard server","event.timestamp":"`+prompt1TS.Format(time.RFC3339)+`"}`)
	mustExec(t, database,
		`INSERT INTO otel_signals
		 (signal_id, harness, session_id, prompt_id, kind, canonical, native, ts_micros, trace_id, tool_name, success, attrs_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sig-codex-tool-1", "codex", "sess-codex-log-window", "",
		"log", "tool_result", "codex.tool_result", 0, "trace-codex-window-1", "Bash", 1,
		`{"tool_name":"Bash","full_command":"wipnote serve --port 8088","event.timestamp":"`+child1TS.Format(time.RFC3339Nano)+`"}`)

	mustExec(t, database,
		`INSERT INTO otel_signals
		 (signal_id, harness, session_id, prompt_id, kind, canonical, native, ts_micros, trace_id, attrs_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sig-codex-prompt-2", "codex", "sess-codex-log-window", "prompt-2",
		"log", "user_prompt", "codex.user_prompt", 0, "trace-codex-window-2",
		`{"prompt":"codex events are still not showing","event.timestamp":"`+prompt2TS.Format(time.RFC3339)+`"}`)
	mustExec(t, database,
		`INSERT INTO otel_signals
		 (signal_id, harness, session_id, prompt_id, kind, canonical, native, ts_micros, trace_id, tool_name, success, attrs_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sig-codex-tool-2", "codex", "sess-codex-log-window", "",
		"log", "tool_result", "codex.tool_result", 0, "trace-codex-window-2", "Read", 0,
		`{"tool_name":"Read","target":"cmd/wipnote/api_tree.go","event.timestamp":"`+child2TS.Format(time.RFC3339Nano)+`"}`)
	mustExec(t, database,
		`INSERT INTO otel_signals
		 (signal_id, harness, session_id, prompt_id, kind, canonical, native, ts_micros, trace_id, attrs_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sig-codex-api-error-2", "codex", "sess-codex-log-window", "",
		"log", "api_error", "codex.api_error", 0, "trace-codex-window-2",
		`{"message":"dashboard query failed","event.timestamp":"`+child2ErrTS.Format(time.RFC3339Nano)+`"}`)

	turns, err := buildEventTree(database, 50)
	if err != nil {
		t.Fatalf("buildEventTree: %v", err)
	}

	var prompt1Turn, prompt2Turn *turn
	for i := range turns {
		switch turns[i].UserQuery["event_id"] {
		case "sig-codex-prompt-1":
			prompt1Turn = &turns[i]
		case "sig-codex-prompt-2":
			prompt2Turn = &turns[i]
		}
	}
	if prompt1Turn == nil || prompt2Turn == nil {
		t.Fatalf("expected both Codex prompt turns, got %+v", turns)
	}

	if len(prompt1Turn.Children) != 1 {
		t.Fatalf("prompt1 children = %d, want 1", len(prompt1Turn.Children))
	}
	if got := prompt1Turn.Children[0]["event_id"]; got != "sig-codex-tool-1" {
		t.Fatalf("prompt1 child event_id = %v, want sig-codex-tool-1", got)
	}
	if prompt1Turn.Stats.ToolCount != 1 || prompt1Turn.Stats.ErrorCount != 0 {
		t.Fatalf("prompt1 stats = %+v, want tool_count=1 error_count=0", prompt1Turn.Stats)
	}

	if len(prompt2Turn.Children) != 2 {
		t.Fatalf("prompt2 children = %d, want 2", len(prompt2Turn.Children))
	}
	if got := prompt2Turn.Children[0]["event_id"]; got != "sig-codex-api-error-2" {
		t.Fatalf("prompt2 newest child event_id = %v, want sig-codex-api-error-2", got)
	}
	if got := prompt2Turn.Children[1]["event_id"]; got != "sig-codex-tool-2" {
		t.Fatalf("prompt2 second child event_id = %v, want sig-codex-tool-2", got)
	}
	if prompt2Turn.Stats.ToolCount != 2 || prompt2Turn.Stats.ErrorCount != 2 {
		t.Fatalf("prompt2 stats = %+v, want tool_count=2 error_count=2", prompt2Turn.Stats)
	}
}

func TestBuildEventTree_OtelLogOnlyCodexTurn_SortsZeroMicrosPromptsByEventTimestamp(t *testing.T) {
	database := openTreeTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	codexSess := &models.Session{
		SessionID:     "sess-codex-zero-sort",
		AgentAssigned: "codex",
		CreatedAt:     now,
		Status:        "active",
	}
	if err := db.InsertSession(database, codexSess); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	for i := 0; i < 4; i++ {
		ts := now.Add(time.Duration(i) * time.Minute)
		mustExec(t, database,
			`INSERT INTO otel_signals
			 (signal_id, harness, session_id, prompt_id, kind, canonical, native, ts_micros, trace_id, attrs_json)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("sig-codex-zero-sort-%d", i), "codex", "sess-codex-zero-sort", fmt.Sprintf("prompt-%d", i),
			"log", "user_prompt", "codex.user_prompt", 0, fmt.Sprintf("trace-codex-zero-sort-%d", i),
			`{"prompt":"prompt `+strconv.Itoa(i)+`","event.timestamp":"`+ts.Format(time.RFC3339Nano)+`"}`)
	}

	turns, err := buildEventTree(database, 2)
	if err != nil {
		t.Fatalf("buildEventTree: %v", err)
	}

	var prompts []string
	for _, turn := range turns {
		if turn.SessionID == "sess-codex-zero-sort" {
			if prompt, _ := turn.UserQuery["input_summary"].(string); prompt != "" {
				prompts = append(prompts, prompt)
			}
		}
	}
	if len(prompts) != 2 {
		t.Fatalf("zero-sort prompts = %v, want 2 newest prompts", prompts)
	}
	if prompts[0] != "prompt 3" || prompts[1] != "prompt 2" {
		t.Fatalf("zero-sort prompts = %v, want [prompt 3 prompt 2]", prompts)
	}
}

// TestBuildEventTree_MixedOtelAndHook verifies the merge logic: OTel-anchored
// sessions, hook-only sessions, and the regression case where a session emits
// both an OTel interaction span AND a hook UserQuery row with empty step_id
// (which used to surface the hook row as a duplicate prompt).
func TestBuildEventTree_MixedOtelAndHook(t *testing.T) {
	database := openTreeTestDB(t)
	defer database.Close()

	// Second session for the hook-only case.
	now := time.Now().UTC()
	hookOnlySess := &models.Session{
		SessionID:     "sess-hook-only",
		AgentAssigned: "claude-code",
		CreatedAt:     now,
		Status:        "active",
	}
	if err := db.InsertSession(database, hookOnlySess); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	// Three turns at distinct timestamps so DESC sort is unambiguous.
	otelTs := now.Add(-30 * time.Second)
	hookOnlyTs := now.Add(-20 * time.Second)
	dupHookTs := now.Add(-10 * time.Second)

	// 1. OTel interaction span with a paired hook UserQuery whose step_id
	//    matches the trace_id (the well-formed case).
	mustExec(t, database,
		`INSERT INTO otel_signals
		 (signal_id, harness, session_id, kind, canonical, native, ts_micros, trace_id, span_id, attrs_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sig-i1", "claude-code", "sess-test",
		"span", "interaction", "interaction",
		otelTs.UnixMicro(), "trace-otel-1", "span-i1",
		`{"user_prompt":"first prompt"}`)
	mustExec(t, database,
		`INSERT INTO agent_events (event_id, agent_id, event_type, timestamp, tool_name, session_id, status, step_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"uq-otel-1", "claude-code", "tool_call", otelTs.Format(time.RFC3339),
		"UserQuery", "sess-test", "recorded", "trace-otel-1")

	// 2. Hook-only session — no OTel anywhere. Must be retained.
	mustExec(t, database,
		`INSERT INTO agent_events (event_id, agent_id, event_type, timestamp, tool_name, session_id, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"uq-hook-only", "claude-code", "tool_call", hookOnlyTs.Format(time.RFC3339),
		"UserQuery", "sess-hook-only", "recorded")

	// 3. Regression: a session that has an OTel interaction span AND a hook
	//    UserQuery row with EMPTY step_id near the same timestamp. The
	//    session+timestamp window check must dedup it.
	mustExec(t, database,
		`INSERT INTO otel_signals
		 (signal_id, harness, session_id, kind, canonical, native, ts_micros, trace_id, span_id, attrs_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"sig-i2", "claude-code", "sess-test",
		"span", "interaction", "interaction",
		dupHookTs.UnixMicro(), "trace-otel-2", "span-i2",
		`{"user_prompt":"third prompt"}`)
	mustExec(t, database,
		`INSERT INTO agent_events (event_id, agent_id, event_type, timestamp, tool_name, session_id, status, step_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"uq-dup-empty-step", "claude-code", "tool_call", dupHookTs.Format(time.RFC3339),
		"UserQuery", "sess-test", "recorded", "")

	turns, err := buildEventTree(database, 50)
	if err != nil {
		t.Fatalf("buildEventTree: %v", err)
	}

	// Expected: two OTel turns (sig-i1, sig-i2) + one hook-only turn (uq-hook-only).
	// The empty-step hook row (uq-dup-empty-step) must be deduped.
	if len(turns) != 3 {
		var ids []string
		for _, t := range turns {
			if id, ok := t.UserQuery["event_id"].(string); ok {
				ids = append(ids, id)
			}
		}
		t.Fatalf("got %d turns, want 3 (event_ids=%v)", len(turns), ids)
	}

	for _, tn := range turns {
		if id, _ := tn.UserQuery["event_id"].(string); id == "uq-dup-empty-step" {
			t.Errorf("hook UserQuery with empty step_id was not deduped against the same-session OTel interaction span")
		}
	}

	// Sanity: hook-only session's turn is present.
	foundHookOnly := false
	for _, tn := range turns {
		if id, _ := tn.UserQuery["event_id"].(string); id == "uq-hook-only" {
			foundHookOnly = true
		}
	}
	if !foundHookOnly {
		t.Error("hook-only session's UserQuery turn missing — merge dropped unanchored hook turns")
	}
}

// TestFilterAnchorsAgainstInteractionSpans_ZeroTS covers the three cases for
// zero-ts anchors: duplicate (has matching span → filtered), orphan (no matching
// span → kept), and the regression guard for normal non-zero-ts anchors.
func TestFilterAnchorsAgainstInteractionSpans_ZeroTS(t *testing.T) {
	database := openTreeTestDB(t)
	defer database.Close()

	// Seed an interaction span for sess-test using the "user_prompt" key.
	mustExec(t, database,
		`INSERT INTO otel_signals
		 (signal_id, harness, session_id, kind, canonical, native, ts_micros, trace_id, span_id, attrs_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"span-filter-1", "gemini_cli", "sess-test",
		"span", "interaction", "interaction",
		time.Now().UnixMicro(), "trace-filter-1", "span-filter-1",
		`{"user_prompt":"hello from gemini"}`)

	// Case 1: zero-ts anchor WITH a matching interaction span → must be filtered OUT.
	anchorsWithMatch := []otelPromptAnchor{
		{
			SignalID:   "log-dup-1",
			SessionID:  "sess-test",
			TSMicros:   0,
			PromptText: "hello from gemini",
		},
	}
	got := filterAnchorsAgainstInteractionSpans(database, anchorsWithMatch)
	if len(got) != 0 {
		t.Errorf("case 1 (zero-ts with matching span): got %d anchors, want 0 (should be filtered)", len(got))
	}

	// Case 2: zero-ts anchor with NO matching interaction span → must be KEPT.
	anchorsNoMatch := []otelPromptAnchor{
		{
			SignalID:   "log-orphan-1",
			SessionID:  "sess-test",
			TSMicros:   0,
			PromptText: "this prompt has no interaction span",
		},
	}
	got = filterAnchorsAgainstInteractionSpans(database, anchorsNoMatch)
	if len(got) != 1 {
		t.Errorf("case 2 (zero-ts with no matching span): got %d anchors, want 1 (should be kept)", len(got))
	}

	// Case 3: normal non-zero-ts anchor with a matching interaction span →
	// existing timestamp-based dedup still filters it (regression guard).
	spanTS := time.Now().UnixMicro()
	mustExec(t, database,
		`INSERT INTO otel_signals
		 (signal_id, harness, session_id, kind, canonical, native, ts_micros, trace_id, span_id, attrs_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"span-filter-2", "claude-code", "sess-test",
		"span", "interaction", "interaction",
		spanTS, "trace-filter-2", "span-filter-2",
		`{"user_prompt":"normal ts prompt"}`)

	anchorsNormalTS := []otelPromptAnchor{
		{
			SignalID:   "log-normal-1",
			SessionID:  "sess-test",
			TSMicros:   spanTS + 100, // within the dedup window
			PromptText: "normal ts prompt",
		},
	}
	got = filterAnchorsAgainstInteractionSpans(database, anchorsNormalTS)
	if len(got) != 0 {
		t.Errorf("case 3 (non-zero ts within window of span): got %d anchors, want 0 (timestamp dedup should filter)", len(got))
	}
}

// TestCollapseDuplicateTurns_RichestSurvives: two rows, same normalized prompt,
// 1 s apart, different session IDs (simulating gemini_cli vs gemini log source),
// one with a harness agent_id (rich) and one without (poor). Only the rich one
// must survive.
func TestCollapseDuplicateTurns_RichestSurvives(t *testing.T) {
	// Anchor base to a collapseBucketSecs boundary so the ~1s-apart rows always
	// land in the same dedup bucket. Truncating only to the second is flaky:
	// it fails ~10% of runs when wall-clock places base at a bucket edge
	// (bucket = unix/collapseBucketSecs, api_tree.go). bug-18d09db3.
	base := time.Now().UTC().Truncate(time.Duration(collapseBucketSecs) * time.Second)
	rich := turn{
		SessionID: "sess-rich",
		UserQuery: map[string]any{
			"input_summary": "should i rename wipnote to wipcli?",
			"agent_id":      "gemini_cli",
			"timestamp":     base.Format(time.RFC3339),
			"feature_id":    "",
		},
		Stats: turnStats{ToolCount: 25},
	}
	poor := turn{
		SessionID: "sess-poor",
		UserQuery: map[string]any{
			"input_summary": "should i rename wipnote to wipcli?",
			"agent_id":      "",
			"timestamp":     base.Add(1 * time.Second).Format(time.RFC3339),
			"feature_id":    "",
		},
		Stats: turnStats{ToolCount: 0},
	}

	got := collapseDuplicateTurns([]turn{rich, poor})
	if len(got) != 1 {
		t.Fatalf("got %d turns, want 1 (poor duplicate should be dropped)", len(got))
	}
	if got[0].Stats.ToolCount != 25 {
		t.Errorf("surviving turn tool_count = %d, want 25 (rich row should survive)", got[0].Stats.ToolCount)
	}
	if got[0].SessionID != "sess-rich" {
		t.Errorf("surviving session_id = %s, want sess-rich", got[0].SessionID)
	}
}

// TestCollapseDuplicateTurns_FarApartBothSurvive: same prompt text but
// timestamps more than collapseBucketSecs apart → both rows must survive.
func TestCollapseDuplicateTurns_FarApartBothSurvive(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	t1 := turn{
		SessionID: "sess-a",
		UserQuery: map[string]any{
			"input_summary": "continue",
			"agent_id":      "claude_code",
			"timestamp":     base.Add(-5 * time.Minute).Format(time.RFC3339),
		},
		Stats: turnStats{ToolCount: 10},
	}
	t2 := turn{
		SessionID: "sess-a",
		UserQuery: map[string]any{
			"input_summary": "continue",
			"agent_id":      "claude_code",
			"timestamp":     base.Add(-2 * time.Minute).Format(time.RFC3339),
		},
		Stats: turnStats{ToolCount: 15},
	}

	got := collapseDuplicateTurns([]turn{t1, t2})
	if len(got) != 2 {
		t.Fatalf("got %d turns, want 2 (far-apart same-prompt rows must not be collapsed)", len(got))
	}
}

// TestCollapseDuplicateTurns_SingleUnanchoredSurvives: a lone hook row with
// no OTel twin must pass through untouched.
func TestCollapseDuplicateTurns_SingleUnanchoredSurvives(t *testing.T) {
	base := time.Now().UTC()
	sole := turn{
		SessionID: "sess-hook-only",
		UserQuery: map[string]any{
			"input_summary": "run go test",
			"agent_id":      "",
			"timestamp":     base.Format(time.RFC3339),
		},
		Stats: turnStats{ToolCount: 3},
	}

	got := collapseDuplicateTurns([]turn{sole})
	if len(got) != 1 {
		t.Fatalf("got %d turns, want 1 (single unanchored row must survive)", len(got))
	}
}

// TestCollapseDuplicateTurns_DifferentPromptsSameBucketBothSurvive: two
// different prompts that land in the same time bucket must not be collapsed.
func TestCollapseDuplicateTurns_DifferentPromptsSameBucketBothSurvive(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	t1 := turn{
		SessionID: "sess-b",
		UserQuery: map[string]any{
			"input_summary": "what is the capital of france?",
			"agent_id":      "claude_code",
			"timestamp":     base.Format(time.RFC3339),
		},
		Stats: turnStats{ToolCount: 1},
	}
	t2 := turn{
		SessionID: "sess-b",
		UserQuery: map[string]any{
			"input_summary": "what is the population of paris?",
			"agent_id":      "claude_code",
			"timestamp":     base.Add(2 * time.Second).Format(time.RFC3339),
		},
		Stats: turnStats{ToolCount: 2},
	}

	got := collapseDuplicateTurns([]turn{t1, t2})
	if len(got) != 2 {
		t.Fatalf("got %d turns, want 2 (different prompts in same bucket must both survive)", len(got))
	}
}

// TestCollapseDuplicateTurns_EmptyPromptNotCollapsed: rows with empty or
// whitespace-only prompts must never be collapsed with each other.
func TestCollapseDuplicateTurns_EmptyPromptNotCollapsed(t *testing.T) {
	base := time.Now().UTC()
	e1 := turn{
		SessionID: "sess-c",
		UserQuery: map[string]any{
			"input_summary": "",
			"agent_id":      "claude_code",
			"timestamp":     base.Format(time.RFC3339),
		},
		Stats: turnStats{ToolCount: 1},
	}
	e2 := turn{
		SessionID: "sess-c",
		UserQuery: map[string]any{
			"input_summary": "   ",
			"agent_id":      "claude_code",
			"timestamp":     base.Add(1 * time.Second).Format(time.RFC3339),
		},
		Stats: turnStats{ToolCount: 2},
	}

	got := collapseDuplicateTurns([]turn{e1, e2})
	if len(got) != 2 {
		t.Fatalf("got %d turns, want 2 (empty/whitespace prompts must not be collapsed)", len(got))
	}
}

// --- nestTaskNotificationTurns tests ---

// TestNestTaskNotificationTurns_NestedUnderOriginatingRow verifies that a
// notification turn whose text contains <tool-use-id>T</tool-use-id> is
// removed from the top-level list and attached as a child of the turn
// containing tool_use_id T, and its title contains no raw angle-bracket XML.
func TestNestTaskNotificationTurns_NestedUnderOriginatingRow(t *testing.T) {
	base := time.Now().UTC()
	origChild := map[string]any{
		"event_id":    "bash-child-1",
		"tool_name":   "Bash",
		"tool_use_id": "toolu_ORIGINATING",
		"children":    []map[string]any{},
	}
	origTurn := turn{
		SessionID: "sess-bg",
		UserQuery: map[string]any{
			"event_id":      "uq-1",
			"input_summary": "run the server in background",
			"timestamp":     base.Add(-2 * time.Minute).Format(time.RFC3339),
		},
		Children: []map[string]any{origChild},
	}
	notifTurn := turn{
		SessionID: "sess-bg",
		UserQuery: map[string]any{
			"event_id":      "uq-notif",
			"input_summary": "<task-notification><task-id>bt8yi6rhf</task-id><tool-use-id>toolu_ORIGINATING</tool-use-id></task-notification>",
			"timestamp":     base.Format(time.RFC3339),
		},
		Children: []map[string]any{
			{"event_id": "notif-child-bash", "tool_name": "Bash", "children": []map[string]any{}},
		},
	}

	result := nestTaskNotificationTurns([]turn{origTurn, notifTurn})

	// Notification must not be a top-level row.
	if len(result) != 1 {
		t.Fatalf("got %d top-level turns, want 1 (notification must be nested)", len(result))
	}
	remaining := result[0]
	if id, _ := remaining.UserQuery["event_id"].(string); id != "uq-1" {
		t.Fatalf("remaining top-level turn event_id = %v, want uq-1", id)
	}

	// Originating turn must have 2 children: the original bash child + the wake node.
	if len(remaining.Children) != 2 {
		t.Fatalf("originating turn children = %d, want 2 (original + wake node)", len(remaining.Children))
	}

	// Find the wake node (the newly appended child).
	var wakeNode map[string]any
	for _, c := range remaining.Children {
		if id, _ := c["event_id"].(string); id == "uq-notif" {
			wakeNode = c
		}
	}
	if wakeNode == nil {
		t.Fatal("wake node not found in originating turn children")
	}

	// Wake node title must not contain angle-bracket XML.
	title, _ := wakeNode["input_summary"].(string)
	if containsStr(title, "<") {
		t.Errorf("wake node title contains raw XML angle brackets: %q", title)
	}
	if !containsStr(title, "bt8yi6rhf") {
		t.Errorf("wake node title does not contain task-id: %q", title)
	}

	// Wake node must have the notification's own children attached.
	wakeChildren, _ := wakeNode["children"].([]map[string]any)
	if len(wakeChildren) != 1 || wakeChildren[0]["event_id"] != "notif-child-bash" {
		t.Errorf("wake node children = %v, want the notification's original children", wakeChildren)
	}
}

// TestNestTaskNotificationTurns_FallbackRelabelsWhenNoMatch verifies that a
// notification turn with no matching originating row stays top-level but has
// its title relabeled to clean text (no raw XML).
func TestNestTaskNotificationTurns_FallbackRelabelsWhenNoMatch(t *testing.T) {
	base := time.Now().UTC()
	notifTurn := turn{
		SessionID: "sess-orphan",
		UserQuery: map[string]any{
			"event_id":      "uq-orphan-notif",
			"input_summary": "<task-notification><task-id>xyz123</task-id><tool-use-id>toolu_MISSING</tool-use-id></task-notification>",
			"timestamp":     base.Format(time.RFC3339),
		},
		Children: []map[string]any{},
	}
	normalTurn := turn{
		SessionID: "sess-orphan",
		UserQuery: map[string]any{
			"event_id":      "uq-normal",
			"input_summary": "some normal prompt",
			"timestamp":     base.Add(-1 * time.Minute).Format(time.RFC3339),
		},
		Children: []map[string]any{},
	}

	result := nestTaskNotificationTurns([]turn{normalTurn, notifTurn})

	// Both rows should remain — the notification cannot be nested.
	if len(result) != 2 {
		t.Fatalf("got %d top-level turns, want 2 (orphan stays top-level)", len(result))
	}

	// Find the notification row.
	var notifResult turn
	found := false
	for _, r := range result {
		if id, _ := r.UserQuery["event_id"].(string); id == "uq-orphan-notif" {
			notifResult = r
			found = true
		}
	}
	if !found {
		t.Fatal("notification turn missing from result")
	}

	title, _ := notifResult.UserQuery["input_summary"].(string)
	if containsStr(title, "<") {
		t.Errorf("fallback title contains raw XML: %q", title)
	}
	if title == "" {
		t.Errorf("fallback title is empty, want descriptive text")
	}
}

// TestNestTaskNotificationTurns_NormalTurnUntouched verifies that a normal
// user prompt turn (no <task-notification>) is not modified.
func TestNestTaskNotificationTurns_NormalTurnUntouched(t *testing.T) {
	base := time.Now().UTC()
	normal := turn{
		SessionID: "sess-normal",
		UserQuery: map[string]any{
			"event_id":      "uq-regular",
			"input_summary": "please implement the feature",
			"timestamp":     base.Format(time.RFC3339),
		},
		Children: []map[string]any{
			{"event_id": "child-1", "tool_name": "Read", "children": []map[string]any{}},
		},
	}

	result := nestTaskNotificationTurns([]turn{normal})

	if len(result) != 1 {
		t.Fatalf("got %d turns, want 1 (normal turn untouched)", len(result))
	}
	if id, _ := result[0].UserQuery["event_id"].(string); id != "uq-regular" {
		t.Errorf("turn event_id = %v, want uq-regular", id)
	}
	if summary, _ := result[0].UserQuery["input_summary"].(string); summary != "please implement the feature" {
		t.Errorf("input_summary changed: %q", summary)
	}
	if len(result[0].Children) != 1 {
		t.Errorf("children length changed: %d", len(result[0].Children))
	}
}

// TestNestTaskNotificationTurns_DeduplicateThenNest verifies that dedup
// (collapseDuplicateTurns) followed by nestTaskNotificationTurns correctly
// collapses a duplicate pair AND nests a notification turn.
func TestNestTaskNotificationTurns_DeduplicateThenNest(t *testing.T) {
	// Anchor base to a collapseBucketSecs boundary so the rich(-30s)/poor(-29s)
	// duplicate pair always shares a dedup bucket. Truncating only to the second
	// is flaky: it fails ~10% of runs when wall-clock places base at a bucket
	// edge (bucket = unix/collapseBucketSecs, api_tree.go). bug-18d09db3.
	base := time.Now().UTC().Truncate(time.Duration(collapseBucketSecs) * time.Second)

	// Two duplicates of the same prompt (rich + poor).
	richTurn := turn{
		SessionID: "sess-rich",
		UserQuery: map[string]any{
			"event_id":      "uq-rich",
			"input_summary": "run tests in background",
			"agent_id":      "claude_code",
			"timestamp":     base.Add(-30 * time.Second).Format(time.RFC3339),
		},
		Children: []map[string]any{
			{
				"event_id":    "bash-bg",
				"tool_name":   "Bash",
				"tool_use_id": "toolu_BG_TASK",
				"children":    []map[string]any{},
			},
		},
		Stats: turnStats{ToolCount: 5},
	}
	poorTurn := turn{
		SessionID: "sess-poor",
		UserQuery: map[string]any{
			"event_id":      "uq-poor",
			"input_summary": "run tests in background",
			"agent_id":      "",
			"timestamp":     base.Add(-29 * time.Second).Format(time.RFC3339),
		},
		Children: []map[string]any{},
		Stats:    turnStats{ToolCount: 0},
	}

	// A notification wake turn matching the rich turn's tool_use_id.
	notifTurn := turn{
		SessionID: "sess-rich",
		UserQuery: map[string]any{
			"event_id":      "uq-wake",
			"input_summary": "<task-notification><task-id>taskABC</task-id><tool-use-id>toolu_BG_TASK</tool-use-id></task-notification>",
			"agent_id":      "claude_code",
			"timestamp":     base.Format(time.RFC3339),
		},
		Children: []map[string]any{},
	}

	// Step 1: dedup.
	afterDedup := collapseDuplicateTurns([]turn{richTurn, poorTurn, notifTurn})
	// Should have 2 rows: rich (dedup winner) + notif (unique prompt text).
	if len(afterDedup) != 2 {
		t.Fatalf("after dedup: got %d turns, want 2", len(afterDedup))
	}

	// Step 2: nest.
	afterNest := nestTaskNotificationTurns(afterDedup)
	// Notification nested → only 1 top-level row remaining.
	if len(afterNest) != 1 {
		t.Fatalf("after nest: got %d top-level turns, want 1", len(afterNest))
	}

	// The surviving row must be the rich dedup winner.
	if id, _ := afterNest[0].UserQuery["event_id"].(string); id != "uq-rich" {
		t.Errorf("surviving turn event_id = %v, want uq-rich", id)
	}

	// It must now have 2 children: original bash-bg + wake node.
	if len(afterNest[0].Children) != 2 {
		t.Fatalf("originating turn children = %d, want 2", len(afterNest[0].Children))
	}
}

// TestNestTaskNotificationTurns_OtelBackedParentFallsBackToRelabel verifies
// that when the originating row is OTel-span-rendered (otel_backed=true), the
// wake-turn is NOT nested (it would vanish because the frontend renders OTel
// spans, not Children). Instead it stays top-level with a clean relabeled title.
// Invariants: no raw XML visible anywhere, wake-turn is never silently dropped.
func TestNestTaskNotificationTurns_OtelBackedParentFallsBackToRelabel(t *testing.T) {
	base := time.Now().UTC()
	// OTel-backed originating turn: frontend renders spans, not turn.Children.
	otelTurn := turn{
		SessionID: "sess-otel",
		UserQuery: map[string]any{
			"event_id":      "otel-uq-1",
			"input_summary": "run background task via otel session",
			"timestamp":     base.Add(-2 * time.Minute).Format(time.RFC3339),
			"otel_backed":   true, // key discriminator: frontend uses spans, not Children
		},
		Children: []map[string]any{
			{
				"event_id":    "bash-child-otel",
				"tool_name":   "Bash",
				"tool_use_id": "toolu_OTEL_BG",
				"children":    []map[string]any{},
			},
		},
	}
	notifTurn := turn{
		SessionID: "sess-otel",
		UserQuery: map[string]any{
			"event_id":      "uq-otel-notif",
			"input_summary": "<task-notification><task-id>otel-task-abc</task-id><tool-use-id>toolu_OTEL_BG</tool-use-id></task-notification>",
			"timestamp":     base.Format(time.RFC3339),
		},
		Children: []map[string]any{
			{"event_id": "wake-child-1", "tool_name": "Bash", "children": []map[string]any{}},
		},
	}

	result := nestTaskNotificationTurns([]turn{otelTurn, notifTurn})

	// Both rows must remain top-level: nesting under an OTel-backed parent
	// would silently drop the wake-turn from the feed.
	if len(result) != 2 {
		t.Fatalf("got %d top-level turns, want 2 (wake-turn must NOT be nested under OTel-backed parent)", len(result))
	}

	// The OTel-backed parent must be untouched — no children were appended.
	var otelResult turn
	for _, r := range result {
		if id, _ := r.UserQuery["event_id"].(string); id == "otel-uq-1" {
			otelResult = r
		}
	}
	if len(otelResult.Children) != 1 {
		t.Errorf("OTel-backed parent children = %d, want 1 (wake-turn must not be appended)", len(otelResult.Children))
	}

	// The wake-turn must appear top-level, relabeled with clean (non-XML) title.
	var wakeResult turn
	found := false
	for _, r := range result {
		if id, _ := r.UserQuery["event_id"].(string); id == "uq-otel-notif" {
			wakeResult = r
			found = true
		}
	}
	if !found {
		t.Fatal("wake-turn missing from result — it was silently dropped (data-loss bug)")
	}
	title, _ := wakeResult.UserQuery["input_summary"].(string)
	if containsStr(title, "<") {
		t.Errorf("wake-turn title contains raw XML: %q", title)
	}
	if title == "" {
		t.Errorf("wake-turn title is empty, want a descriptive relabeled title")
	}
	if !containsStr(title, "otel-task-abc") {
		t.Errorf("wake-turn title does not contain task-id %q: %q", "otel-task-abc", title)
	}
}

// TestNestTaskNotificationTurns_ParentStatsRecomputedAfterNest verifies that
// after a wake-turn is nested under a hook-derived parent, the parent's Stats
// are recomputed to include the new child (LOW finding from roborev 2749).
func TestNestTaskNotificationTurns_ParentStatsRecomputedAfterNest(t *testing.T) {
	base := time.Now().UTC()
	origChild := map[string]any{
		"event_id":    "bash-stats-child",
		"tool_name":   "Bash",
		"tool_use_id": "toolu_STATS_TEST",
		"event_type":  "tool_call",
		"status":      "recorded",
		"children":    []map[string]any{},
	}
	// Hook-derived parent with 1 existing child and stats reflecting that.
	origTurn := turn{
		SessionID: "sess-stats",
		UserQuery: map[string]any{
			"event_id":      "uq-stats-1",
			"input_summary": "start background work",
			"timestamp":     base.Add(-2 * time.Minute).Format(time.RFC3339),
			// No otel_backed key → hook-derived, renders Children.
		},
		Children: []map[string]any{origChild},
		Stats:    turnStats{ToolCount: 1}, // pre-nest stats: 1 tool
	}
	notifTurn := turn{
		SessionID: "sess-stats",
		UserQuery: map[string]any{
			"event_id":      "uq-stats-notif",
			"input_summary": "<task-notification><task-id>stats-task-xyz</task-id><tool-use-id>toolu_STATS_TEST</tool-use-id></task-notification>",
			"timestamp":     base.Format(time.RFC3339),
		},
		Children: []map[string]any{
			{
				"event_id":   "wake-tool-child",
				"tool_name":  "Read",
				"event_type": "tool_call",
				"status":     "recorded",
				"children":   []map[string]any{},
			},
		},
	}

	result := nestTaskNotificationTurns([]turn{origTurn, notifTurn})

	if len(result) != 1 {
		t.Fatalf("got %d top-level turns, want 1 after nesting", len(result))
	}

	parent := result[0]
	// After nesting: parent has 2 children (original bash + wake node).
	// Wake node itself has 1 child (wake-tool-child).
	// computeStats walks all children recursively → ToolCount = 2 + 1 = 3.
	// (origChild=1, wakeNode=1, wake-tool-child=1)
	if parent.Stats.ToolCount < 2 {
		t.Errorf("parent Stats.ToolCount = %d after nesting, want ≥ 2 (stale stats not recomputed)", parent.Stats.ToolCount)
	}
}

func TestComputeStats_CountsNestedChildren(t *testing.T) {
	children := []map[string]any{
		{
			"event_type": "task_delegation",
			"tool_name":  "Task",
			"status":     "recorded",
			"model":      "claude-sonnet",
			"children": []map[string]any{
				{"event_type": "tool_call", "tool_name": "Bash", "status": "recorded", "model": "claude-sonnet"},
				{"event_type": "error", "tool_name": "Read", "status": "failed", "model": "claude-sonnet"},
			},
		},
	}

	stats := computeStats(children)
	if stats.ToolCount != 3 {
		t.Errorf("ToolCount = %d, want 3", stats.ToolCount)
	}
	if stats.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", stats.ErrorCount)
	}
}

// TestBuildEventTree_SubagentUserQueryNotTopLevel verifies that subagent-dispatched
// UserQuery rows (agent_id != "claude-code") do not appear as top-level turns.
// They are captured via the Task/task_delegation/SubagentStart path, so the
// redundant UserQuery row should not create a duplicate turn.
func TestBuildEventTree_SubagentUserQueryNotTopLevel(t *testing.T) {
	database := openTreeTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	ts := now.Format(time.RFC3339)

	// Insert orchestrator UserQuery (agent_id = "claude-code").
	mustExec(t, database,
		`INSERT INTO agent_events (event_id, agent_id, event_type, timestamp, tool_name, session_id, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"uq-orch", "claude-code", "tool_call", ts, "UserQuery", "sess-test", "recorded")

	// Insert task_delegation/Task as child of orchestrator UserQuery.
	mustExec(t, database,
		`INSERT INTO agent_events (event_id, agent_id, event_type, timestamp, tool_name, session_id, status, parent_event_id, subagent_type)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"td-sub", "claude-code", "task_delegation", ts, "Task", "sess-test", "recorded", "uq-orch", "researcher")

	// Insert subagent UserQuery (agent_id = subagent UUID, not "claude-code").
	// This mimics the redundant UserQuery that the bug causes.
	mustExec(t, database,
		`INSERT INTO agent_events (event_id, agent_id, event_type, timestamp, tool_name, session_id, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"uq-sub", "sub-agent-uuid-123", "tool_call", ts, "UserQuery", "sess-test", "recorded")

	turns, err := buildEventTree(database, 50)
	if err != nil {
		t.Fatalf("buildEventTree: %v", err)
	}

	// Should have exactly 1 top-level turn (the orchestrator's UserQuery).
	// The subagent's UserQuery should NOT appear as a second top-level turn.
	if len(turns) != 1 {
		t.Fatalf("got %d top-level turns, want 1 (subagent UserQuery should not be top-level)", len(turns))
	}

	// Verify the turn is the orchestrator's UserQuery.
	topTurn := turns[0]
	if eventID, _ := topTurn.UserQuery["event_id"].(string); eventID != "uq-orch" {
		t.Errorf("top-level UserQuery event_id = %q, want uq-orch", eventID)
	}
	if agentID, _ := topTurn.UserQuery["agent_id"].(string); agentID != "claude-code" {
		t.Errorf("top-level UserQuery agent_id = %q, want claude-code", agentID)
	}
}

func TestBuildEventTree_FiltersHookSystemPrompt(t *testing.T) {
	database := openTreeTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	ts := now.Format(time.RFC3339)

	mustExec(t, database,
		`INSERT INTO agent_events (event_id, agent_id, event_type, timestamp, tool_name, session_id, status, input_summary)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"uq-review", "claude-code", "tool_call", ts, "UserQuery", "sess-test", "recorded",
		"You are a code reviewer. Review the code changes shown below and provide feedback.")
	mustExec(t, database,
		`INSERT INTO agent_events (event_id, agent_id, event_type, timestamp, tool_name, session_id, status, parent_event_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"child-review", "claude-code", "tool_call", ts, "Bash", "sess-test", "recorded", "uq-review")

	turns, err := buildEventTree(database, 50)
	if err != nil {
		t.Fatalf("buildEventTree: %v", err)
	}
	if len(turns) != 0 {
		t.Fatalf("got %d turns, want 0 (system reviewer prompt should be filtered)", len(turns))
	}
}

func TestBuildEventTree_FiltersInternalOtelPrompts(t *testing.T) {
	database := openTreeTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	ts := now.UnixMicro()

	mustExec(t, database,
		`INSERT INTO otel_signals (signal_id, harness, session_id, kind, canonical, native, trace_id, ts_micros, attrs_json)
		 VALUES (?, ?, ?, 'span', 'interaction', ?, ?, ?, ?)`,
		"sig-reviewer", "codex", "sess-test", "codex.interaction", "trace-reviewer", ts,
		`{"user_prompt":"You are a code reviewer. Review the code changes shown below and provide feedback."}`)
	mustExec(t, database,
		`INSERT INTO otel_signals (signal_id, harness, session_id, kind, canonical, native, trace_id, ts_micros, attrs_json)
		 VALUES (?, ?, ?, 'span', 'interaction', ?, ?, ?, ?)`,
		"sig-real", "codex", "sess-test", "codex.interaction", "trace-real", ts+1_000_000,
		`{"user_prompt":"please fix the dashboard feed"}`)

	turns, err := buildEventTree(database, 50)
	if err != nil {
		t.Fatalf("buildEventTree: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}
	if got := turns[0].UserQuery["input_summary"]; got != "please fix the dashboard feed" {
		t.Fatalf("input_summary = %v, want real user prompt", got)
	}
}

func TestBuildEventTree_FiltersResumeNotificationPromptLogs(t *testing.T) {
	database := openTreeTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	ts := now.UnixMicro()

	mustExec(t, database,
		`INSERT INTO otel_signals (signal_id, harness, session_id, kind, canonical, native, ts_micros, attrs_json)
		 VALUES (?, ?, ?, 'log', 'user_prompt', ?, ?, ?)`,
		"sig-resume", "codex", "sess-test", "codex.user_prompt", ts,
		`{"text":"↩ resumed: background task bt8yi6rhf completed"}`)
	mustExec(t, database,
		`INSERT INTO otel_signals (signal_id, harness, session_id, kind, canonical, native, ts_micros, attrs_json)
		 VALUES (?, ?, ?, 'log', 'user_prompt', ?, ?, ?)`,
		"sig-real", "codex", "sess-test", "codex.user_prompt", ts+1_000_000,
		`{"text":"please fix the dashboard feed"}`)

	turns, err := buildEventTree(database, 50)
	if err != nil {
		t.Fatalf("buildEventTree: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}
	if got := turns[0].UserQuery["event_id"]; got != "sig-real" {
		t.Fatalf("event_id = %v, want sig-real", got)
	}
}

func TestBuildEventTree_TrimsCodexSubagentWorkItemPreamble(t *testing.T) {
	database := openTreeTestDB(t)
	defer database.Close()

	now := time.Now().UTC()
	ts := now.UnixMicro()
	prompt := "Work item: bug-774ee9c8. Start by running `wipnote bug start bug-774ee9c8`. Investigate runtime SQLITE_BUSY under DELETE journal."

	mustExec(t, database,
		`INSERT INTO otel_signals (signal_id, harness, session_id, kind, canonical, native, ts_micros, attrs_json)
		 VALUES (?, ?, ?, 'log', 'user_prompt', ?, ?, ?)`,
		"sig-subagent", "codex", "sess-test", "codex.user_prompt", ts,
		`{"text":`+strconv.Quote(prompt)+`}`)

	turns, err := buildEventTree(database, 50)
	if err != nil {
		t.Fatalf("buildEventTree: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}
	if got := turns[0].UserQuery["input_summary"]; got != "Investigate runtime SQLITE_BUSY under DELETE journal." {
		t.Fatalf("input_summary = %v", got)
	}
}
