package hooks

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// insertAgentEventFull inserts an agent_events row with explicit agent_id and
// event_type, complementing the package-level insertAgentEvent helper that
// hard-codes agent_id = "agent-test" and event_type = "tool_call".
func insertAgentEventFull(t *testing.T, tdb *testDB, eventID, sessionID, agentID, eventType, toolName, inputSummary string) {
	t.Helper()
	now := time.Now().UTC()
	insertAgentEventFullWithTime(t, tdb, eventID, sessionID, agentID, eventType, toolName, inputSummary, now)
}

// insertAgentEventFullWithTime inserts an agent_events row with explicit agent_id, event_type, and created_at.
func insertAgentEventFullWithTime(t *testing.T, tdb *testDB, eventID, sessionID, agentID, eventType, toolName, inputSummary string, createdAt time.Time) {
	t.Helper()
	e := &models.AgentEvent{
		EventID:      eventID,
		AgentID:      agentID,
		EventType:    models.EventType(eventType),
		Timestamp:    createdAt,
		ToolName:     toolName,
		InputSummary: inputSummary,
		SessionID:    sessionID,
		Status:       "recorded",
		Source:       "test",
		CreatedAt:    createdAt,
		UpdatedAt:    createdAt,
	}
	if err := db.InsertEvent(tdb.DB, e); err != nil {
		t.Fatalf("InsertEvent(%s): %v", eventID, err)
	}
}

// insertResearchTestSessionWithProject inserts a sessions row with optional parent and project_dir.
func insertResearchTestSessionWithProject(t *testing.T, tdb *testDB, sessionID, parentSessionID, projectDir string) {
	t.Helper()
	now := time.Now().UTC()
	sess := &models.Session{
		SessionID:       sessionID,
		AgentAssigned:   "claude-code",
		CreatedAt:       now,
		Status:          "active",
		ParentSessionID: parentSessionID,
		ProjectDir:      projectDir,
	}
	if err := db.InsertSession(tdb.DB, sess); err != nil {
		t.Fatalf("InsertSession(%s): %v", sessionID, err)
	}
}

// insertLineageTrace inserts an agent_lineage_trace row for sub-agent lineage.
func insertResearchLineageTrace(t *testing.T, tdb *testDB, traceID, rootSessionID, sessionID string) {
	t.Helper()
	trace := &models.LineageTrace{
		TraceID:       traceID,
		RootSessionID: rootSessionID,
		SessionID:     sessionID,
		AgentName:     "test-subagent",
		Depth:         1,
		Path:          []string{"test-subagent"},
		StartedAt:     time.Now().UTC(),
		Status:        "active",
	}
	if err := db.InsertLineageTrace(tdb.DB, trace); err != nil {
		t.Fatalf("InsertLineageTrace(%s): %v", traceID, err)
	}
}

// TestHasRecentResearch_FindsByAgentID verifies failure mode 1: sub-agent Reads
// are stored under the orchestrator's session_id with the sub-agent's agent_id.
// hasRecentResearch must find them via agentID even when the session walk misses.
func TestHasRecentResearch_FindsByAgentID(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()

	const (
		orchestratorSID = "orch-session-0001"
		subagentID      = "subagent-af3d0b93"
		subagentSID     = "sub-session-0001"
	)

	// Insert sessions rows. orchestratorSID needs a row so the FK is satisfied
	// when we insert an event under it; subagentSID is orphaned (no parent).
	insertResearchTestSessionWithProject(t, tdb, orchestratorSID, "", ".")
	insertResearchTestSessionWithProject(t, tdb, subagentSID, "", ".")

	// Read event stored under the orchestrator's session but with the sub-agent's agent_id.
	insertAgentEventFull(t, tdb, "evt-read-001", orchestratorSID, subagentID, "tool_call", "Read", "internal/foo.go")

	// Sub-agent also has a SessionStart event under its own session (enough to
	// prevent the old totalEvents==0 path, but tool_call count is 0 under subagentSID).
	insertAgentEventFull(t, tdb, "evt-start-001", subagentSID, subagentID, "start", "", "")

	// Call with sub-agent's session ID and agent ID. Must return true.
	if !hasRecentResearch(tdb.DB, subagentSID, subagentID, "") {
		t.Error("expected hasRecentResearch=true: Read stored under orchestrator session should be found via agentID")
	}
}

// TestHasRecentResearch_WalksFullLineageChain verifies that a 3-level parent
// chain root → mid → leaf is walked and Reads under root are found when
// called with leaf's session ID.
func TestHasRecentResearch_WalksFullLineageChain(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()

	const (
		rootSID = "chain-root-0001"
		midSID  = "chain-mid-0001"
		leafSID = "chain-leaf-0001"
		agentID = "chain-agent"
	)

	// sessions: leaf → mid → root
	insertResearchTestSessionWithProject(t, tdb, rootSID, "", ".")
	insertResearchTestSessionWithProject(t, tdb, midSID, rootSID, ".")
	insertResearchTestSessionWithProject(t, tdb, leafSID, midSID, ".")

	// Read event under the root session.
	insertAgentEventFull(t, tdb, "evt-chain-read", rootSID, agentID, "tool_call", "Read", "cmd/main.go")

	// Call with leaf session and a non-generic agentID. Must walk up and find it.
	if !hasRecentResearch(tdb.DB, leafSID, agentID, "") {
		t.Error("expected hasRecentResearch=true: Read under root should be found via 3-level parent walk")
	}
}

// TestHasRecentResearch_UsesLineageTraceFallback verifies that when
// sessions.parent_session_id is NULL but an agent_lineage_trace row points to
// a root that has Reads, hasRecentResearch returns true.
func TestHasRecentResearch_UsesLineageTraceFallback(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()

	const (
		rootSID = "trace-root-0001"
		subSID  = "trace-sub-0001"
		traceID = "trace-id-0001"
		agentID = "trace-agent"
	)

	// Root session with a Read event.
	insertResearchTestSessionWithProject(t, tdb, rootSID, "", ".")
	insertAgentEventFull(t, tdb, "evt-trace-read", rootSID, agentID, "tool_call", "Read", "internal/bar.go")

	// Sub-agent session with NULL parent (orphaned from session perspective).
	insertResearchTestSessionWithProject(t, tdb, subSID, "", ".")

	// Lineage trace row points sub → root.
	insertResearchLineageTrace(t, tdb, traceID, rootSID, subSID)

	// A tool_call under sub so the fail-open path does NOT trigger.
	insertAgentEventFull(t, tdb, "evt-trace-bash", subSID, agentID, "tool_call", "Bash", "echo check")

	// Call with sub's session. Must follow trace to root and find the Read.
	if !hasRecentResearch(tdb.DB, subSID, agentID, "") {
		t.Error("expected hasRecentResearch=true: Read under root should be found via lineage trace fallback")
	}
}

// TestHasRecentResearch_FailsOpenOnRecordingGap verifies that when the only
// events recorded are non-tool_call events (e.g. SessionStart), the function
// returns true (fail-open) instead of blocking.
func TestHasRecentResearch_FailsOpenOnRecordingGap(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()

	const (
		sessID  = "gap-session-0001"
		agentID = "gap-agent"
	)

	insertResearchTestSessionWithProject(t, tdb, sessID, "", ".")

	// Insert only a SessionStart event (event_type = 'start', not 'tool_call').
	insertAgentEventFull(t, tdb, "evt-gap-start", sessID, agentID, string(models.EventStart), "", "")

	// tool_call count is 0 → recording gap → must fail-open.
	if !hasRecentResearch(tdb.DB, sessID, agentID, "") {
		t.Error("expected hasRecentResearch=true (fail-open): only a SessionStart event recorded, recording pipeline may be broken")
	}
}

// TestHasRecentResearch_BlocksWhenNoResearchButOtherToolsRan verifies the
// normal blocking case: tool_call events exist but none are research-y.
func TestHasRecentResearch_BlocksWhenNoResearchButOtherToolsRan(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()

	const (
		sessID  = "noread-session-0001"
		agentID = "noread-agent"
	)

	insertResearchTestSessionWithProject(t, tdb, sessID, "", ".")

	// A non-research Bash command: echo hi is not on the read-only list.
	insertAgentEventFull(t, tdb, "evt-noread-bash", sessID, agentID, "tool_call", "Bash", "echo hi")

	// tool_call events exist but none are research → must block.
	if hasRecentResearch(tdb.DB, sessID, agentID, "") {
		t.Error("expected hasRecentResearch=false: Bash 'echo hi' is not research")
	}
}

// TestHasRecentResearch_WebToolsSatisfyResearch verifies that Claude Code's
// web research tools (WebSearch / WebFetch) count as research — consulting
// official docs, GitHub issues, or the web is first-class research and must not
// be penalized by the guard. See cmd/wipnote/prompts/research-routing.md.
func TestHasRecentResearch_WebToolsSatisfyResearch(t *testing.T) {
	cases := []struct {
		name    string
		sessID  string
		agentID string
		evtID   string
		tool    string
	}{
		{"WebSearch", "websearch-session-0001", "websearch-agent", "evt-websearch", "WebSearch"},
		{"WebFetch", "webfetch-session-0001", "webfetch-agent", "evt-webfetch", "WebFetch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tdb := setupTestDB(t)
			defer tdb.DB.Close()

			insertResearchTestSessionWithProject(t, tdb, tc.sessID, "", ".")
			insertAgentEventFull(t, tdb, tc.evtID, tc.sessID, tc.agentID, "tool_call", tc.tool, "https://example.com")

			if !hasRecentResearch(tdb.DB, tc.sessID, tc.agentID, "") {
				t.Errorf("expected hasRecentResearch=true: %s should count as research", tc.tool)
			}
		})
	}
}

// TestHasRecentResearch_GenericAgentIDNotCrossSession verifies that generic
// harness-level agent IDs do not bridge unrelated sessions.
func TestHasRecentResearch_GenericAgentIDNotCrossSession(t *testing.T) {
	for _, genericID := range []string{"claude-code", "claude", "codex", "gemini", "antigravity"} {
		t.Run(genericID, func(t *testing.T) {
			tdb := setupTestDB(t)
			defer tdb.DB.Close()

			sessionA := "generic-sess-a-" + genericID
			sessionB := "generic-sess-b-" + genericID

			// Session A with a Read event tagged with the generic agent_id.
			insertResearchTestSessionWithProject(t, tdb, sessionA, "", ".")
			insertAgentEventFull(t, tdb, "evt-generic-read-"+genericID, sessionA, genericID, "tool_call", "Read", "README.md")

			// Session B is unrelated (no parent, no lineage trace to A), has a non-research tool call.
			insertResearchTestSessionWithProject(t, tdb, sessionB, "", ".")
			insertAgentEventFull(t, tdb, "evt-generic-write-"+genericID, sessionB, genericID, "tool_call", "Write", "out.txt")

			// Calling from session B with the generic ID must NOT find session A's Read.
			if hasRecentResearch(tdb.DB, sessionB, genericID, "") {
				t.Errorf("expected hasRecentResearch=false: generic agent_id %q must not bridge unrelated sessions", genericID)
			}
		})
	}
}

// TestHasRecentResearch_AgentIDDoesNotCrossProjects verifies that the agent_id fallback
// is scoped to the same project. Events from the same agent in a different project
// must not count toward research.
func TestHasRecentResearch_AgentIDDoesNotCrossProjects(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()

	const (
		sessionA = "proj-a-sess"
		sessionB = "proj-b-sess"
		agentID  = "shared-agent-xyz"
		projectA = "."
		projectB = "other-project"
	)

	// Session A in project A with a Read event.
	insertResearchTestSessionWithProject(t, tdb, sessionA, "", projectA)
	insertAgentEventFull(t, tdb, "evt-proj-a-read", sessionA, agentID, "tool_call", "Read", "file.go")

	// Session B in project B with the same agent_id (but different project).
	insertResearchTestSessionWithProject(t, tdb, sessionB, "", projectB)
	insertAgentEventFull(t, tdb, "evt-proj-b-bash", sessionB, agentID, "tool_call", "Bash", "echo test")

	// Calling from session B in project B with that agent_id must NOT find session A's Read
	// (even though the agent_id matches), because the project_dir is different.
	if hasRecentResearch(tdb.DB, sessionB, agentID, projectB) {
		t.Error("expected hasRecentResearch=false: agent_id fallback must not cross projects")
	}
}

// TestHasRecentResearch_AgentIDIgnoresOldEvents verifies that the agent_id fallback
// is scoped to the last 24 hours. Events older than 24 hours must not count.
func TestHasRecentResearch_AgentIDIgnoresOldEvents(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()

	const (
		sessID  = "old-sess"
		agentID = "time-scoped-agent"
	)

	now := time.Now().UTC()
	oldTime := now.Add(-48 * time.Hour) // 48 hours ago

	// Session with project_dir set.
	insertResearchTestSessionWithProject(t, tdb, sessID, "", ".")

	// Insert a Read event 48 hours ago (outside the 24-hour window).
	insertAgentEventFullWithTime(t, tdb, "evt-old-read", sessID, agentID, "tool_call", "Read", "old.go", oldTime)

	// Also insert a non-research tool_call so the fail-open path doesn't trigger.
	insertAgentEventFull(t, tdb, "evt-bash", sessID, agentID, "tool_call", "Bash", "echo hi")

	// Calling with this agent_id should return false because the Read is stale (> 24h old).
	if hasRecentResearch(tdb.DB, sessID, agentID, ".") {
		t.Error("expected hasRecentResearch=false: agent_id fallback must ignore events older than 24 hours")
	}
}

// TestHasRecentResearch_AgentIDFallbackStillWorksRecent verifies that the agent_id fallback
// still works correctly when events are recent and in the same project.
func TestHasRecentResearch_AgentIDFallbackStillWorksRecent(t *testing.T) {
	tdb := setupTestDB(t)
	defer tdb.DB.Close()

	const (
		sessID  = "recent-sess"
		agentID = "recent-agent"
	)

	now := time.Now().UTC()

	// Session with project_dir set.
	insertResearchTestSessionWithProject(t, tdb, sessID, "", ".")

	// Insert a recent Read event (now).
	insertAgentEventFullWithTime(t, tdb, "evt-recent-read", sessID, agentID, "tool_call", "Read", "recent.go", now)

	// Calling with this agent_id should return true because the Read is recent and in the same project.
	if !hasRecentResearch(tdb.DB, sessID, agentID, ".") {
		t.Error("expected hasRecentResearch=true: agent_id fallback should find recent events in the same project")
	}
}
