package hooks

import (
	"encoding/json"
	"testing"
)

func TestCloudEvent_AgentTeamsPayload(t *testing.T) {
	payload := `{
		"session_id": "sess-001",
		"task_id": "task-001",
		"teammate_name": "implementer",
		"team_name": "my-team",
		"idle_reason": "waiting",
		"task_subject": "Build widget",
		"task_description": "Build the widget component"
	}`

	var ev CloudEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if ev.TeammateName != "implementer" {
		t.Errorf("TeammateName = %q, want %q", ev.TeammateName, "implementer")
	}
	if ev.TeamName != "my-team" {
		t.Errorf("TeamName = %q, want %q", ev.TeamName, "my-team")
	}
	if ev.IdleReason != "waiting" {
		t.Errorf("IdleReason = %q, want %q", ev.IdleReason, "waiting")
	}
	if ev.TaskSubject != "Build widget" {
		t.Errorf("TaskSubject = %q, want %q", ev.TaskSubject, "Build widget")
	}
	if ev.TaskDescription != "Build the widget component" {
		t.Errorf("TaskDescription = %q, want %q", ev.TaskDescription, "Build the widget component")
	}
}

func TestCloudEvent_LegacyPayload(t *testing.T) {
	payload := `{
		"session_id": "sess-002",
		"task_id": "task-002",
		"task": {"subject": "Run tests", "description": "Run all tests"}
	}`

	var ev CloudEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if ev.TeammateName != "" {
		t.Errorf("TeammateName = %q, want empty", ev.TeammateName)
	}
	if ev.TeamName != "" {
		t.Errorf("TeamName = %q, want empty", ev.TeamName)
	}
	if ev.IdleReason != "" {
		t.Errorf("IdleReason = %q, want empty", ev.IdleReason)
	}
	if ev.TaskSubject != "" {
		t.Errorf("TaskSubject = %q, want empty", ev.TaskSubject)
	}
	if ev.TaskDescription != "" {
		t.Errorf("TaskDescription = %q, want empty", ev.TaskDescription)
	}

	// TaskData should still be populated.
	if ev.TaskData["subject"] != "Run tests" {
		t.Errorf("TaskData[subject] = %v, want %q", ev.TaskData["subject"], "Run tests")
	}
}
