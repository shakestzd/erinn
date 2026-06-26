package hooks

import (
	"encoding/json"
	"testing"
)

// TestEnvSessionID_HarnessAware verifies that EnvSessionID picks the
// harness-native live session ID over a stale WIPNOTE_SESSION_ID when
// WIPNOTE_HARNESS indicates a non-Claude harness (issue #144).
func TestEnvSessionID_HarnessAware(t *testing.T) {
	// Clear any leaked vars from the ambient test environment.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("WIPNOTE_PARENT_SESSION", "")
	t.Setenv("NESTING_DEPTH", "")

	stale := "stale-wipnote-session"

	tests := []struct {
		name         string
		harness      string
		codexThread  string
		geminiSess   string
		antigravSess string
		wipnoteID    string
		explicitArg  string
		want         string
	}{
		{
			name:        "Codex: CODEX_THREAD_ID wins over stale WIPNOTE_SESSION_ID",
			harness:     "codex",
			codexThread: "codex-thread-live-abc123",
			wipnoteID:   stale,
			want:        "codex-thread-live-abc123",
		},
		{
			name:       "Gemini: GEMINI_SESSION_ID wins over stale WIPNOTE_SESSION_ID",
			harness:    "gemini",
			geminiSess: "gemini-live-session-xyz",
			wipnoteID:  stale,
			want:       "gemini-live-session-xyz",
		},
		{
			name:         "Antigravity: ANTIGRAVITY_SESSION_ID wins over stale WIPNOTE_SESSION_ID",
			harness:      "antigravity",
			antigravSess: "antigravity-live-456",
			wipnoteID:    stale,
			want:         "antigravity-live-456",
		},
		{
			name:      "Claude (no harness marker): falls through to WIPNOTE_SESSION_ID",
			harness:   "claude",
			wipnoteID: "wipnote-claude-session",
			want:      "wipnote-claude-session",
		},
		{
			name:      "No harness set: falls through to WIPNOTE_SESSION_ID",
			harness:   "",
			wipnoteID: "wipnote-only-session",
			want:      "wipnote-only-session",
		},
		{
			name:        "Explicit arg always wins (hook-path invariant)",
			harness:     "codex",
			codexThread: "codex-thread-live",
			wipnoteID:   stale,
			explicitArg: "explicit-event-session",
			want:        "explicit-event-session",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WIPNOTE_HARNESS", tc.harness)
			t.Setenv("CODEX_THREAD_ID", tc.codexThread)
			t.Setenv("GEMINI_SESSION_ID", tc.geminiSess)
			t.Setenv("ANTIGRAVITY_SESSION_ID", tc.antigravSess)
			t.Setenv("WIPNOTE_SESSION_ID", tc.wipnoteID)

			got := EnvSessionID(tc.explicitArg)
			if got != tc.want {
				t.Errorf("EnvSessionID(%q) = %q, want %q", tc.explicitArg, got, tc.want)
			}
		})
	}
}

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
