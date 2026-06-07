package hooks

import (
	"testing"
)

// TestCodexTaskStarted_NoFalseStepState proves the cross-harness step contract
// (feat-885ec940, Tier 4): a Codex TaskStarted event is routed to TrackEvent
// (a generic checkpoint agent_event), and MUST NOT create a hook-driven
// "task-<id>" step on the active feature. Only Claude TaskCreated/TaskCompleted
// drive addTaskStep/completeTaskStep. If Codex ever silently started ticking
// steps, the dashboard's "steps not live for this harness" state would become
// a lie — this test is the regression guard for that invariant.
func TestCodexTaskStarted_NoFalseStepState(t *testing.T) {
	td, sessionID := setupMissingEventsDB(t)

	// Active feature claimed by the session so cachedGetActiveFeatureID
	// resolves — this is the exact condition under which a buggy mapping
	// would create a phantom step.
	if _, err := td.DB.Exec(
		`INSERT INTO features (id, type, title, status) VALUES ('feat-cx-contract', 'feature', 'Codex Contract', 'in-progress')`,
	); err != nil {
		t.Fatalf("insert feature: %v", err)
	}
	if _, err := td.DB.Exec(
		`INSERT INTO active_work_items (session_id, agent_id, work_item_id) VALUES (?, 'codex', 'feat-cx-contract')`,
		sessionID,
	); err != nil {
		t.Fatalf("insert active_work_items: %v", err)
	}

	// Codex TaskStarted → TrackEvent (the manifest.json wiring).
	result, err := TrackEvent("TaskStarted", &CloudEvent{
		SessionID: sessionID,
		CWD:       t.TempDir(),
		TaskID:    "task-codex-1",
	}, td.DB)
	if err != nil {
		t.Fatalf("TrackEvent(TaskStarted): %v", err)
	}
	if result == nil || !result.Continue {
		t.Fatalf("expected Continue=true from TrackEvent")
	}

	// A generic checkpoint event IS recorded (telemetry is fine).
	var checkpoints int
	if err := td.DB.QueryRow(
		`SELECT COUNT(*) FROM agent_events WHERE session_id = ? AND tool_name = 'TaskStarted'`,
		sessionID,
	).Scan(&checkpoints); err != nil {
		t.Fatalf("query checkpoint: %v", err)
	}
	if checkpoints != 1 {
		t.Errorf("expected 1 TaskStarted checkpoint event, got %d", checkpoints)
	}

	// But NO hook-driven step command ran: there must be no task_created /
	// task_completed event, which addTaskStep/completeTaskStep paths produce.
	var stepEvents int
	if err := td.DB.QueryRow(
		`SELECT COUNT(*) FROM agent_events
		 WHERE session_id = ? AND event_type IN ('task_created','task_completed')`,
		sessionID,
	).Scan(&stepEvents); err != nil {
		t.Fatalf("query step events: %v", err)
	}
	if stepEvents != 0 {
		t.Fatalf("Codex TaskStarted created %d false step event(s); contract requires ZERO "+
			"(step tracking is documented-unsupported for codex)", stepEvents)
	}
}
