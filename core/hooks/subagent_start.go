package hooks

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/paths"
)

// SubagentStart handles the SubagentStart Claude Code hook event.
// It records a task_delegation agent_event, links it to the current UserQuery,
// and writes env vars so the subagent's hooks know their parent and identity.
func SubagentStart(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	sessionID := resolveSessionIDWithHarness(event)
	if sessionID == "" {
		return &HookResult{Continue: true}, nil
	}

	projectDir := ResolveProjectDir(event.CWD, event.SessionID)
	featureID := cachedGetActiveFeatureID(database, sessionID)
	eventID := uuid.New().String()
	agentType := event.AgentType
	if agentType == "" {
		agentType = "general-purpose"
	}

	// Populate the lineage write path: insert a synthetic sessions row keyed
	// by the subagent's agent_id (guaranteed distinct per subagent, empirically
	// verified via /tmp/wipnote-hook-trace.jsonl) and a matching
	// agent_lineage_trace row so downstream queries can walk the subagent tree.
	// bug-cb4918d8: Claude Code delivers the SAME session_id to orchestrator
	// and subagent hook events, so agent_id is the only discriminator.
	if event.AgentID != "" {
		insertSubagentLineage(database, sessionID, event.AgentID, agentType, featureID, projectDir)
	}

	// Link delegation to the most recent UserQuery in this session.
	parentEventID, _ := db.LatestEventByTool(database, sessionID, "UserQuery")

	ev := &models.AgentEvent{
		EventID:       eventID,
		AgentID:       event.AgentID,
		EventType:     models.EventTaskDelegation,
		Timestamp:     time.Now().UTC(),
		ToolName:      "Task",
		InputSummary:  fmt.Sprintf("Subagent started: type=%s id=%s", agentType, event.AgentID),
		SessionID:     sessionID,
		FeatureID:     featureID,
		ParentEventID: parentEventID,
		SubagentType:  agentType,
		Status:        "started",
		Source:        "hook",
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}

	// Route the task_delegation agent_events INSERT through the daemon-first
	// enqueue path (plan-2390966a slice-4): no direct writable handle when the
	// daemon is reachable, <1s bounded fallback otherwise. Best-effort like the
	// prior db.InsertEvent — canonical NDJSON + reindex recover the row.
	_ = RouteInsertEvent("subagent-start", projectDir, sessionID, ev, database)

	// Write traceparent so the subagent's session-start can claim it.
	writeTraceparent(sessionID, eventID)

	// Write env vars so subagent hooks know their parent and identity.
	writeSubagentEnvVars(eventID, event.AgentID, agentType, projectDir, sessionID)

	// Register a pending row so the OTLP receiver can synthesize a placeholder
	// otel_signals row as soon as the first subagent span arrives — eliminating
	// the visible "flash" where orphan tool-call spans render without a parent.
	if event.AgentID != "" {
		pending := &db.PendingSubagentStart{
			AgentID:   event.AgentID,
			AgentType: agentType,
			SessionID: sessionID,
			// Normalize CWD to repo-relative so pending rows remain stable
			// across worktrees and machines (same policy as sessions.project_dir).
			CWD:           paths.NormalizeProjectDir(projectDir),
			ParentAgentID: os.Getenv("WIPNOTE_AGENT_ID"),
			CreatedAt:     time.Now().UnixMicro(),
		}
		if err := db.UpsertPendingSubagentStart(database, pending); err != nil {
			debugLog(projectDir, "[warn] handler=subagent-start session=%s: upsert pending_subagent_starts: %v",
				sessionID[:minSessionLen(sessionID)], err)
		}
	}

	return &HookResult{Continue: true}, nil
}

// SubagentStop handles the SubagentStop Claude Code hook event.
// It marks the task_delegation for this specific agent as completed and
// stores the last assistant message as the output summary.
//
// CONTROLLING CAPABILITY (AdditiveControlling, per hookEventContractSpecs): CC
// reads a JSON HookResult here, so returning {decision:"block", reason:"…"}
// would PREVENT the subagent from stopping. This handler is kept observe-only
// by design — there is no current use-case to keep a subagent running — but the
// HookResult return path is already wired, so a future block condition is a
// one-line addition.
func SubagentStop(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	sessionID := resolveSessionIDWithHarness(event)
	if sessionID == "" {
		return &HookResult{Continue: true}, nil
	}

	outputSummary := event.LastAssistantMessage
	if len(outputSummary) > outputSummaryMaxLen {
		outputSummary = outputSummary[:outputSummaryMaxLen] + "…"
	}

	// Prefer agent_id-scoped lookup to avoid matching the wrong delegation
	// in concurrent multi-agent scenarios.
	var eventID string
	if event.AgentID != "" {
		eventID, _ = db.FindStartedDelegationByAgent(database, sessionID, event.AgentID)
	}

	// Fallback: most recent started delegation in this session.
	if eventID == "" {
		var err error
		eventID, err = db.FindStartedDelegation(database, sessionID)
		if err != nil {
			return &HookResult{Continue: true}, nil
		}
	}

	if err := db.UpdateEventFields(database, eventID, "completed", outputSummary); err != nil {
		projectDir := ResolveProjectDir(event.CWD, event.SessionID)
		debugLog(projectDir, "[error] handler=subagent-stop session=%s: update event fields: %v", sessionID[:minSessionLen(sessionID)], err)
	}

	// Clean up per-subagent hint file written by SubagentStart.
	if event.AgentID != "" {
		paths.CleanupSubagentHint(sessionID, event.AgentID)
	}

	// Close the lineage trace row opened by SubagentStart. Keyed on
	// trace_id = event.AgentID (see insertSubagentLineage).
	// bug-cb4918d8: wire the subagent-stop completion path.
	if event.AgentID != "" {
		closeSubagentLineage(database, event.AgentID, ResolveProjectDir(event.CWD, event.SessionID), sessionID)
	}

	return &HookResult{Continue: true}, nil
}

// insertSubagentLineage writes the two lineage rows the dashboard / lineage
// queries need on every subagent dispatch:
//
//  1. sessions row keyed by agentID (synthetic PK) with parent_session_id set
//     to the orchestrator's session UUID. This is how "is_subagent=1 with
//     populated parent_session_id" finally becomes true for live data.
//  2. agent_lineage_trace row with trace_id = agentID, root_session_id =
//     parent session UUID, session_id = agentID (the synthetic row above),
//     agent_name = agentType. Depth defaults to 1 — top-level subagents only;
//     we don't chase nested lineage here (correctness-when-simple).
//
// Both writes are idempotent (INSERT OR IGNORE) so redelivered start events
// don't fail the hook. Errors are logged but never block the hook.
func insertSubagentLineage(database *sql.DB, parentSessionID, agentID, agentType, featureID, projectDir string) {
	now := time.Now().UTC().Format(time.RFC3339)
	metadata := fmt.Sprintf(`{"agent_type":%q,"created_via":"subagent-start-hook"}`, agentType)

	// Route the synthetic subagent sessions INSERT through the daemon-first
	// enqueue path (plan-2390966a slice-4) so it never opens a direct writable
	// handle when the daemon is reachable and degrades to a <1s bounded fallback
	// otherwise. Best-effort: a degraded write is recovered by reindex, so we no
	// longer abort the remaining lineage backfill on a write failure (the prior
	// direct Exec returned early on error). The subsequent BackfillParentSession /
	// InsertLineageTrace are independently idempotent.
	_ = routeHookWriteVia("subagent-start", projectDir, parentSessionID, database, `
		INSERT OR IGNORE INTO sessions
			(session_id, agent_assigned, parent_session_id, created_at,
			 status, is_subagent, metadata)
		VALUES (?, ?, ?, ?, 'active', 1, ?)`,
		agentID, agentType, parentSessionID, now, metadata,
	)
	// Out-of-order attribution: if the child session row already existed
	// (created by a prior hook before SubagentStart fired), backfill the
	// parent_session_id so the lineage chain is complete regardless of
	// arrival order. BackfillParentSession is idempotent (no-op when already set).
	if err := db.BackfillParentSession(database, agentID, parentSessionID); err != nil {
		debugLog(projectDir, "[warn] handler=subagent-start session=%s: backfill parent: %v",
			parentSessionID[:minSessionLen(parentSessionID)], err)
	}

	trace := &models.LineageTrace{
		TraceID:       agentID,
		RootSessionID: parentSessionID,
		SessionID:     agentID,
		AgentName:     agentType,
		Depth:         1,
		Path:          []string{agentType},
		FeatureID:     featureID,
		StartedAt:     time.Now().UTC(),
		Status:        "active",
	}
	if err := db.InsertLineageTrace(database, trace); err != nil {
		// Duplicate-PK on re-delivered events is expected; only warn.
		debugLog(projectDir, "[warn] handler=subagent-start session=%s: insert lineage trace: %v",
			parentSessionID[:minSessionLen(parentSessionID)], err)
	}
}

// closeSubagentLineage marks the lineage row completed. Missing rows (e.g.
// hook started firing mid-session) log a warn and return — never fail.
func closeSubagentLineage(database *sql.DB, agentID, projectDir, sessionID string) {
	res, err := database.Exec(`
		UPDATE agent_lineage_trace
		   SET completed_at = ?, status = 'completed'
		 WHERE trace_id = ? AND completed_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339), agentID,
	)
	if err != nil {
		debugLog(projectDir, "[warn] handler=subagent-stop session=%s: close lineage trace: %v",
			sessionID[:minSessionLen(sessionID)], err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		debugLog(projectDir, "[warn] handler=subagent-stop session=%s: no open lineage trace for agent_id=%s",
			sessionID[:minSessionLen(sessionID)], agentID)
	}
}
