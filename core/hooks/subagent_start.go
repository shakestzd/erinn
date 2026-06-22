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
		// roborev-473 finding 3: the OTLP receiver reads this pending row the
		// instant the subagent's first span arrives, so an ENQUEUE-ONLY write
		// opens a miss window (the row isn't applied yet when the receiver looks).
		// This is a once-per-subagent, low-frequency write whose consumer is
		// coupled to it, so CORRECTNESS WINS over the <1s hot-hook target here:
		// route it APPLIED-ACK via apply.RouteSQL so the row is durably committed
		// (and therefore visible) before SubagentStart returns. apply.RouteSQL is
		// bounded by CLISubmitBudget (~2s) — acceptable for a once-per-subagent
		// write. On a daemon miss (RouteSQL returns false) fall back to the direct
		// applied write db.UpsertPendingSubagentStart so the row is still committed
		// synchronously; canonical NDJSON + reindex remain the durability backstop.
		upSQL, upArgs := db.UpsertPendingSubagentStartStmt(pending)
		if !routeSQLApplied(projectDir, upSQL, upArgs...) {
			if err := db.UpsertPendingSubagentStart(database, pending); err != nil {
				debugLog(projectDir, "[warn] handler=subagent-start session=%s: applied-ack pending upsert fallback: %v",
					sessionID[:minSessionLen(sessionID)], err)
			}
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
	//
	// Route the UPDATE through the daemon-first enqueue-only seam (bug-c9ec25a4)
	// so it never opens a direct contended writable handle when the daemon is
	// reachable. FIFO single-writer ordering guarantees the already-enqueued
	// synthetic-sessions INSERT above applies before this UPDATE of that row.
	// Best-effort/advisory like the prior db.BackfillParentSession.
	bfSQL, bfArgs := db.BackfillParentSessionStmt(agentID, parentSessionID)
	_ = RouteHookWrite("subagent-start", projectDir, parentSessionID, bfSQL, bfArgs...)

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
	// Route the lineage-trace INSERT through the same enqueue-only seam. The
	// builder's args are JSON-transport-safe (nullableStr → nil/string, RFC3339
	// time string, int depth, JSON path string) so the daemon can re-bind them.
	// json.Marshal(trace.Path) can fail — if it does, skip routing entirely
	// (never enqueue a half-built statement) and log; canonical NDJSON + reindex
	// recover the row. Best-effort/advisory like the prior db.InsertLineageTrace.
	if ltSQL, ltArgs, err := db.InsertLineageTraceStmt(trace); err != nil {
		debugLog(projectDir, "[warn] handler=subagent-start session=%s: build lineage trace stmt: %v",
			parentSessionID[:minSessionLen(parentSessionID)], err)
	} else {
		_ = RouteHookWrite("subagent-start", projectDir, parentSessionID, ltSQL, ltArgs...)
	}
}

// closeSubagentLineage marks the lineage row completed, keyed on
// trace_id = agentID (see insertSubagentLineage).
//
// roborev-473 finding 4: this close is routed ENQUEUE-ONLY through the daemon
// (RouteHookWrite) instead of a DIRECT Exec on the passed handle. The matching
// SubagentStart lineage INSERT is ALSO enqueue-only (insertSubagentLineage), and
// the daemon applies ops on a single writer in FIFO order. Because SubagentStart
// always fires before SubagentStop, the start insert is enqueued first and this
// close UPDATE applies AFTER it — so a fast stop can no longer update 0 rows and
// leave an orphaned `active` lineage row that the later-landing insert created.
// The enqueue-only ack carries no RowsAffected, so the prior "no open lineage
// trace" diagnostic is dropped; the daemon applies the UPDATE in FIFO order and
// canonical NDJSON + reindex remain the durability backstop.
//
// Routing through RouteHookWrite (which opens its OWN bounded writable handle on a
// daemon miss) also means the close no longer needs the passed `database` handle
// to be writable — a prerequisite for switching the Stop dispatch to a read-only
// handle (roborev-473 finding 1).
func closeSubagentLineage(database *sql.DB, agentID, projectDir, sessionID string) {
	_ = database // reads happen elsewhere in SubagentStop; the close routes via the daemon
	clSQL, clArgs := db.CloseLineageTraceByTraceIDStmt(agentID)
	_ = RouteHookWrite("subagent-stop", projectDir, sessionID, clSQL, clArgs...)
}
