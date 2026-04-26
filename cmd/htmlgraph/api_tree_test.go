package main

import (
	"database/sql"
	"testing"
	"time"

	"github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/models"
)

// openTreeTestDB creates an in-memory database with schema and a test session.
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

	turns := buildEventTree(database, 50)
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

	turns := buildEventTree(database, 50)

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

	turns := buildEventTree(database, 50)
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
		"uq-hook", "claude-code", "tool_call", ts, "UserQuery", "sess-test", "recorded")

	turns := buildEventTree(database, 50)
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}

	uq := turns[0].UserQuery
	if uq["event_id"] != "uq-hook" {
		t.Errorf("event_id = %v, want uq-hook (hook fallback)", uq["event_id"])
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

	turns := buildEventTree(database, 50)
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}

	if turns[0].UserQuery["input_summary"] != "Hello world" {
		t.Errorf("input_summary = %v, want 'Hello world' from trace user_prompt log",
			turns[0].UserQuery["input_summary"])
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
