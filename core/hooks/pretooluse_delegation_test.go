package hooks

// Regression tests for Finding 266 (roborev bug-60107613):
// hasPriorDelegationEvidence must NOT treat Codex's generic TaskStarted
// checkpoint as sidecar/delegation evidence; only real spawn events
// (Task, Agent, multi_agent.*) should suppress the research advisory.

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// TestTaskStarted_DoesNotSuppressResearchAdvisory asserts that a prior
// TaskStarted event (a Codex generic checkpoint) does NOT suppress the
// orchestration research advisory. TaskStarted is a progress marker, not
// a sidecar spawn, so the advisory must still fire.
func TestTaskStarted_DoesNotSuppressResearchAdvisory(t *testing.T) {
	projectDir := t.TempDir()
	database := makeSessionDB(t, "sess-task-started-nosupp", projectDir)
	now := time.Now().UTC()

	if err := db.InsertEvent(database, &models.AgentEvent{
		EventID:   "ev-task-started-nosupp",
		AgentID:   "codex",
		EventType: models.EventToolCall,
		Timestamp: now,
		ToolName:  "TaskStarted",
		SessionID: "sess-task-started-nosupp",
		Status:    "started",
		Source:    "hook",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("InsertEvent TaskStarted: %v", err)
	}

	got := checkOrchestratorResearchDelegationAdvisory(
		&CloudEvent{ToolName: "web.search_query"},
		&toolUseContext{SessionID: "sess-task-started-nosupp"},
		database,
	)
	if got == "" {
		t.Fatal("expected advisory after TaskStarted (Codex checkpoint — not delegation evidence): got empty")
	}
}

// TestTaskTool_SuppressesResearchAdvisory asserts that a prior Task tool-call
// (a genuine sidecar spawn) DOES suppress the orchestration research advisory.
func TestTaskTool_SuppressesResearchAdvisory(t *testing.T) {
	projectDir := t.TempDir()
	database := makeSessionDB(t, "sess-task-tool-supp", projectDir)
	now := time.Now().UTC()

	if err := db.InsertEvent(database, &models.AgentEvent{
		EventID:   "ev-task-tool",
		AgentID:   "claude-code",
		EventType: models.EventToolCall,
		Timestamp: now,
		ToolName:  "Task",
		SessionID: "sess-task-tool-supp",
		Status:    "completed",
		Source:    "hook",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("InsertEvent Task: %v", err)
	}

	got := checkOrchestratorResearchDelegationAdvisory(
		&CloudEvent{ToolName: "web.search_query"},
		&toolUseContext{SessionID: "sess-task-tool-supp"},
		database,
	)
	if got != "" {
		t.Fatalf("expected no advisory after Task spawn evidence, got %q", got)
	}
}

// TestAgentTool_SuppressesResearchAdvisory asserts that a prior Agent tool-call
// suppresses the orchestration research advisory (genuine delegation evidence).
func TestAgentTool_SuppressesResearchAdvisory(t *testing.T) {
	projectDir := t.TempDir()
	database := makeSessionDB(t, "sess-agent-tool-supp", projectDir)
	now := time.Now().UTC()

	if err := db.InsertEvent(database, &models.AgentEvent{
		EventID:   "ev-agent-tool",
		AgentID:   "claude-code",
		EventType: models.EventToolCall,
		Timestamp: now,
		ToolName:  "Agent",
		SessionID: "sess-agent-tool-supp",
		Status:    "completed",
		Source:    "hook",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("InsertEvent Agent: %v", err)
	}

	got := checkOrchestratorResearchDelegationAdvisory(
		&CloudEvent{ToolName: "web.search_query"},
		&toolUseContext{SessionID: "sess-agent-tool-supp"},
		database,
	)
	if got != "" {
		t.Fatalf("expected no advisory after Agent spawn evidence, got %q", got)
	}
}

// TestMultiAgentSpawn_SuppressesResearchAdvisory asserts that multi_agent.*
// spawn evidence suppresses the orchestration research advisory.
func TestMultiAgentSpawn_SuppressesResearchAdvisory(t *testing.T) {
	projectDir := t.TempDir()
	database := makeSessionDB(t, "sess-multi-agent-supp", projectDir)
	now := time.Now().UTC()

	if err := db.InsertEvent(database, &models.AgentEvent{
		EventID:   "ev-multi-agent-spawn",
		AgentID:   "claude-code",
		EventType: models.EventToolCall,
		Timestamp: now,
		ToolName:  "multi_agent.spawn",
		SessionID: "sess-multi-agent-supp",
		Status:    "completed",
		Source:    "hook",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("InsertEvent multi_agent.spawn: %v", err)
	}

	got := checkOrchestratorResearchDelegationAdvisory(
		&CloudEvent{ToolName: "web.search_query"},
		&toolUseContext{SessionID: "sess-multi-agent-supp"},
		database,
	)
	if got != "" {
		t.Fatalf("expected no advisory after multi_agent.spawn evidence, got %q", got)
	}
}
