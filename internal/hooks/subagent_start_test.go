package hooks

import (
	"os"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// TestSubagentStart_WritesLineageRows is the bug-cb4918d8 regression test for
// the subagent-start write path. Given a CloudEvent with agent_id and
// agent_type populated (the empirically-verified discriminator fields from
// /tmp/wipnote-hook-trace.jsonl), the handler must:
//
//  1. Insert a synthetic sessions row keyed by agent_id with
//     parent_session_id = event.SessionID and is_subagent = 1.
//  2. Insert an agent_lineage_trace row with trace_id = agent_id,
//     root_session_id = event.SessionID, and agent_name = event.AgentType.
func TestSubagentStart_WritesLineageRows(t *testing.T) {
	database, projectDir := setupLifecycleDB(t)
	parentSessionID := "parent-session-cb4918d8"
	subagentID := "subagent-abc123"
	agentType := "wipnote:patch-coder"

	// Isolate from the dev environment.
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)
	t.Setenv("WIPNOTE_SESSION_ID", parentSessionID)
	t.Setenv("WIPNOTE_AGENT_ID", "claude-code")
	t.Setenv("WIPNOTE_PARENT_EVENT", "")

	// Seed the parent session so downstream queries don't trip FK issues.
	if err := db.InsertSession(database, &models.Session{
		SessionID:     parentSessionID,
		AgentAssigned: "claude-code",
		Status:        "active",
	}); err != nil {
		t.Fatalf("InsertSession parent: %v", err)
	}

	event := &CloudEvent{
		SessionID: parentSessionID,
		CWD:       projectDir,
		AgentID:   subagentID,
		AgentType: agentType,
	}
	if _, err := SubagentStart(event, database); err != nil {
		t.Fatalf("SubagentStart: %v", err)
	}

	// Assertion 1: synthetic sessions row keyed by agent_id.
	childSess, err := db.GetSession(database, subagentID)
	if err != nil || childSess == nil {
		t.Fatalf("GetSession subagent: sess=%v err=%v", childSess, err)
	}
	if childSess.ParentSessionID != parentSessionID {
		t.Errorf("parent_session_id: got %q, want %q", childSess.ParentSessionID, parentSessionID)
	}
	if !childSess.IsSubagent {
		t.Error("is_subagent: got false, want true")
	}

	// Assertion 2: lineage trace row.
	trace, err := db.GetLineageBySession(database, subagentID)
	if err != nil {
		t.Fatalf("GetLineageBySession: %v", err)
	}
	if trace == nil {
		t.Fatal("expected lineage trace, got nil")
	}
	if trace.TraceID != subagentID {
		t.Errorf("trace_id: got %q, want %q", trace.TraceID, subagentID)
	}
	if trace.RootSessionID != parentSessionID {
		t.Errorf("root_session_id: got %q, want %q", trace.RootSessionID, parentSessionID)
	}
	if trace.AgentName != agentType {
		t.Errorf("agent_name: got %q, want %q", trace.AgentName, agentType)
	}
	if trace.Status != "active" {
		t.Errorf("status: got %q, want %q", trace.Status, "active")
	}
}

// TestSubagentStart_Idempotent asserts re-delivery of the same start event
// is safe — the INSERT OR IGNORE path on sessions plus a duplicate-PK warn
// on agent_lineage_trace must not fail the hook.
func TestSubagentStart_Idempotent(t *testing.T) {
	database, projectDir := setupLifecycleDB(t)
	parentSessionID := "parent-idempotent"
	subagentID := "subagent-idempotent"

	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)
	t.Setenv("WIPNOTE_SESSION_ID", parentSessionID)

	if err := db.InsertSession(database, &models.Session{
		SessionID: parentSessionID, AgentAssigned: "claude-code", Status: "active",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	event := &CloudEvent{
		SessionID: parentSessionID,
		CWD:       projectDir,
		AgentID:   subagentID,
		AgentType: "general-purpose",
	}
	if _, err := SubagentStart(event, database); err != nil {
		t.Fatalf("SubagentStart first: %v", err)
	}
	if _, err := SubagentStart(event, database); err != nil {
		t.Fatalf("SubagentStart re-delivery: %v", err)
	}
}

// TestSubagentStop_ClosesLineage asserts that SubagentStop updates the lineage
// row for the matching agent_id, setting status=completed and completed_at.
func TestSubagentStop_ClosesLineage(t *testing.T) {
	database, projectDir := setupLifecycleDB(t)
	parentSessionID := "parent-stop-test"
	subagentID := "subagent-stop-test"

	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)
	t.Setenv("WIPNOTE_SESSION_ID", parentSessionID)

	if err := db.InsertSession(database, &models.Session{
		SessionID: parentSessionID, AgentAssigned: "claude-code", Status: "active",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	startEvent := &CloudEvent{
		SessionID: parentSessionID,
		CWD:       projectDir,
		AgentID:   subagentID,
		AgentType: "general-purpose",
	}
	if _, err := SubagentStart(startEvent, database); err != nil {
		t.Fatalf("SubagentStart: %v", err)
	}

	stopEvent := &CloudEvent{
		SessionID:            parentSessionID,
		CWD:                  projectDir,
		AgentID:              subagentID,
		LastAssistantMessage: "all done",
	}
	if _, err := SubagentStop(stopEvent, database); err != nil {
		t.Fatalf("SubagentStop: %v", err)
	}

	trace, err := db.GetLineageBySession(database, subagentID)
	if err != nil {
		t.Fatalf("GetLineageBySession: %v", err)
	}
	if trace == nil {
		t.Fatal("expected lineage row, got nil")
	}
	if trace.Status != "completed" {
		t.Errorf("status: got %q, want %q", trace.Status, "completed")
	}
	if trace.CompletedAt == nil {
		t.Error("completed_at: got nil, want non-nil")
	}
}

// TestSubagentStart_WritesPendingSubagentRow asserts that SubagentStart inserts
// a row into pending_subagent_starts so the OTLP receiver can synthesise a
// placeholder otel_signals row before the real Agent span arrives.
func TestSubagentStart_WritesPendingSubagentRow(t *testing.T) {
	database, projectDir := setupLifecycleDB(t)
	parentSessionID := "parent-pending-test"
	subagentID := "subagent-pending-123"
	agentType := "wipnote:patch-coder"

	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)
	t.Setenv("WIPNOTE_SESSION_ID", parentSessionID)
	t.Setenv("WIPNOTE_AGENT_ID", "claude-code")
	t.Setenv("WIPNOTE_PARENT_EVENT", "")
	t.Setenv("CLAUDE_ENV_FILE", "")

	if err := db.InsertSession(database, &models.Session{
		SessionID:     parentSessionID,
		AgentAssigned: "claude-code",
		Status:        "active",
	}); err != nil {
		t.Fatalf("InsertSession parent: %v", err)
	}

	event := &CloudEvent{
		SessionID: parentSessionID,
		CWD:       projectDir,
		AgentID:   subagentID,
		AgentType: agentType,
	}
	if _, err := SubagentStart(event, database); err != nil {
		t.Fatalf("SubagentStart: %v", err)
	}

	pending, err := db.GetPendingSubagentStart(database, subagentID)
	if err != nil {
		t.Fatalf("GetPendingSubagentStart: %v", err)
	}
	if pending == nil {
		t.Fatal("expected pending_subagent_starts row, got nil")
	}
	if pending.AgentID != subagentID {
		t.Errorf("agent_id: got %q, want %q", pending.AgentID, subagentID)
	}
	if pending.AgentType != agentType {
		t.Errorf("agent_type: got %q, want %q", pending.AgentType, agentType)
	}
	if pending.SessionID != parentSessionID {
		t.Errorf("session_id: got %q, want %q", pending.SessionID, parentSessionID)
	}
	if pending.CreatedAt == 0 {
		t.Error("created_at: got 0, want non-zero microseconds")
	}
}

// TestSubagentStart_WritesPendingSubagentRowIdempotent asserts that a re-delivered
// SubagentStart event overwrites the pending row without error (INSERT OR REPLACE).
func TestSubagentStart_WritesPendingSubagentRowIdempotent(t *testing.T) {
	database, projectDir := setupLifecycleDB(t)
	parentSessionID := "parent-pending-idem"
	subagentID := "subagent-pending-idem"

	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)
	t.Setenv("WIPNOTE_SESSION_ID", parentSessionID)
	t.Setenv("WIPNOTE_AGENT_ID", "claude-code")
	t.Setenv("CLAUDE_ENV_FILE", "")

	if err := db.InsertSession(database, &models.Session{
		SessionID:     parentSessionID,
		AgentAssigned: "claude-code",
		Status:        "active",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	event := &CloudEvent{
		SessionID: parentSessionID,
		CWD:       projectDir,
		AgentID:   subagentID,
		AgentType: "general-purpose",
	}
	// First delivery.
	if _, err := SubagentStart(event, database); err != nil {
		t.Fatalf("SubagentStart first: %v", err)
	}
	// Re-delivery must not fail.
	if _, err := SubagentStart(event, database); err != nil {
		t.Fatalf("SubagentStart re-delivery: %v", err)
	}

	// Exactly one row should exist.
	var count int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM pending_subagent_starts WHERE agent_id = ?`, subagentID,
	).Scan(&count); err != nil {
		t.Fatalf("count pending rows: %v", err)
	}
	if count != 1 {
		t.Errorf("pending row count = %d, want 1", count)
	}
}

// TestSubagentStart_WritesOTELResourceAttributes asserts that writeSubagentEnvVars
// appends OTEL_RESOURCE_ATTRIBUTES=wipnote.agent_id=<id> to CLAUDE_ENV_FILE,
// merging with any existing value so pre-existing attributes are preserved.
func TestSubagentStart_WritesOTELResourceAttributes(t *testing.T) {
	database, projectDir := setupLifecycleDB(t)
	parentSessionID := "parent-otel-ra"
	subagentID := "subagent-otel-ra-456"

	// Create a temp env file that CLAUDE_ENV_FILE points to.
	// writeSubagentEnvVars uses O_APPEND|O_WRONLY (no O_CREATE), so the file
	// must already exist — it's normally pre-created by writeEnvVars in SessionStart.
	envFile := t.TempDir() + "/claude.env"
	if err := os.WriteFile(envFile, nil, 0o644); err != nil {
		t.Fatalf("create CLAUDE_ENV_FILE: %v", err)
	}
	t.Setenv("CLAUDE_ENV_FILE", envFile)
	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)
	t.Setenv("WIPNOTE_SESSION_ID", parentSessionID)
	t.Setenv("WIPNOTE_AGENT_ID", "claude-code")
	t.Setenv("WIPNOTE_PARENT_EVENT", "")
	// Pre-set an existing OTEL_RESOURCE_ATTRIBUTES value to verify merge.
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.name=test,service.version=1.0")

	if err := db.InsertSession(database, &models.Session{
		SessionID:     parentSessionID,
		AgentAssigned: "claude-code",
		Status:        "active",
	}); err != nil {
		t.Fatalf("InsertSession parent: %v", err)
	}

	event := &CloudEvent{
		SessionID: parentSessionID,
		CWD:       projectDir,
		AgentID:   subagentID,
		AgentType: "general-purpose",
	}
	if _, err := SubagentStart(event, database); err != nil {
		t.Fatalf("SubagentStart: %v", err)
	}

	// Read env file contents and look for OTEL_RESOURCE_ATTRIBUTES.
	content, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read CLAUDE_ENV_FILE: %v", err)
	}
	fileStr := string(content)

	expectedSubstr := "wipnote.agent_id=" + subagentID
	if !strings.Contains(fileStr, expectedSubstr) {
		t.Errorf("CLAUDE_ENV_FILE does not contain %q\n\nfile contents:\n%s", expectedSubstr, fileStr)
	}
	// The existing attrs should be preserved (merged, not clobbered).
	if !strings.Contains(fileStr, "service.name=test") {
		t.Errorf("CLAUDE_ENV_FILE lost pre-existing OTEL_RESOURCE_ATTRIBUTES; file:\n%s", fileStr)
	}
}

// TestSubagentStart_PreservesClaudeCodeAgentIdentity is the regression test for
// bug-1b095c09: when WIPNOTE_PARENT_AGENT=codex leaks from a parent shell and
// CLAUDE_CODE_ENTRYPOINT is set (real Claude Code session), the handler must
// store the agent_id and agent_type from the raw payload unchanged.
// Before the fix, parseCodexEvent was invoked (because Claude Code's payload
// contains hook_event_name), which hardcoded AgentID="codex" and defaulted
// AgentType="general-purpose", stomping wipnote:* agent attribution.
func TestSubagentStart_PreservesClaudeCodeAgentIdentity(t *testing.T) {
	database, projectDir := setupLifecycleDB(t)
	parentSessionID := "parent-session-1b095c09"
	subagentID := "task-uuid-xyz"
	agentType := "wipnote:researcher"

	// Simulate leaked Codex env — must NOT override the payload's agent_id.
	t.Setenv("WIPNOTE_PARENT_AGENT", "codex")
	// Simulate real Claude Code hook environment.
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")

	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)
	t.Setenv("WIPNOTE_SESSION_ID", parentSessionID)
	t.Setenv("WIPNOTE_AGENT_ID", "claude-code")
	t.Setenv("WIPNOTE_PARENT_EVENT", "")
	t.Setenv("CLAUDE_ENV_FILE", "")

	if err := db.InsertSession(database, &models.Session{
		SessionID:     parentSessionID,
		AgentAssigned: "claude-code",
		Status:        "active",
	}); err != nil {
		t.Fatalf("InsertSession parent: %v", err)
	}

	// The CloudEvent that SubagentStart receives after ParseEventForHarness.
	// With the fix, HarnessClaude is selected (CLAUDE_CODE_ENTRYPOINT wins),
	// so agent_id and agent_type come through unchanged from the raw payload.
	event := &CloudEvent{
		SessionID: parentSessionID,
		CWD:       projectDir,
		AgentID:   subagentID,
		AgentType: agentType,
	}
	if _, err := SubagentStart(event, database); err != nil {
		t.Fatalf("SubagentStart: %v", err)
	}

	// The stored sessions row must use the payload's agent_id, not "codex".
	childSess, err := db.GetSession(database, subagentID)
	if err != nil || childSess == nil {
		t.Fatalf("GetSession(subagentID=%q): sess=%v err=%v", subagentID, childSess, err)
	}
	if childSess.AgentAssigned != agentType {
		t.Errorf("sessions.agent_assigned = %q, want %q (not 'codex' or 'general-purpose')",
			childSess.AgentAssigned, agentType)
	}
	if childSess.ParentSessionID != parentSessionID {
		t.Errorf("sessions.parent_session_id = %q, want %q", childSess.ParentSessionID, parentSessionID)
	}
	if !childSess.IsSubagent {
		t.Error("sessions.is_subagent = false, want true")
	}

	// The agent_events row must also preserve the real agent_id.
	var storedAgentID, storedSubagentType string
	err = database.QueryRow(
		`SELECT agent_id, subagent_type FROM agent_events WHERE agent_id = ? LIMIT 1`, subagentID,
	).Scan(&storedAgentID, &storedSubagentType)
	if err != nil {
		t.Fatalf("query agent_events: %v", err)
	}
	if storedAgentID != subagentID {
		t.Errorf("agent_events.agent_id = %q, want %q (must not be clobbered to 'codex')",
			storedAgentID, subagentID)
	}
	if storedSubagentType != agentType {
		t.Errorf("agent_events.subagent_type = %q, want %q (must not default to 'general-purpose')",
			storedSubagentType, agentType)
	}

	// Lineage trace must also preserve the real agent_type.
	trace, err := db.GetLineageBySession(database, subagentID)
	if err != nil {
		t.Fatalf("GetLineageBySession: %v", err)
	}
	if trace == nil {
		t.Fatal("expected lineage trace, got nil")
	}
	if trace.AgentName != agentType {
		t.Errorf("lineage.agent_name = %q, want %q", trace.AgentName, agentType)
	}
}

// TestSubagentStop_MissingTraceIsNonFatal asserts a stop event with no
// matching start row does not return an error (log-and-continue semantics).
func TestSubagentStop_MissingTraceIsNonFatal(t *testing.T) {
	database, projectDir := setupLifecycleDB(t)
	parentSessionID := "parent-no-trace"

	t.Setenv("WIPNOTE_PROJECT_DIR", projectDir)
	t.Setenv("WIPNOTE_SESSION_ID", parentSessionID)

	if err := db.InsertSession(database, &models.Session{
		SessionID: parentSessionID, AgentAssigned: "claude-code", Status: "active",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	stopEvent := &CloudEvent{
		SessionID: parentSessionID,
		CWD:       projectDir,
		AgentID:   "subagent-never-started",
	}
	if _, err := SubagentStop(stopEvent, database); err != nil {
		t.Fatalf("SubagentStop (missing trace) should not error: %v", err)
	}
}
