package hooks

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/paths"
	"github.com/shakestzd/wipnote/core/worktree"
)

// worktreePathResolver is the resolver function passed to
// paths.NormalizeWithResolver for worktree path normalization.  It receives
// a directory and must return the repo root anchor for that directory, or ""
// when the directory is not inside any wipnote-aware git repo.
//
// Production value: paths.ResolveWipnoteAnchorForDir (git-based resolver).
// Tests override this var with a stub that returns a synthetic repo root
// without shelling to git.
var worktreePathResolver = paths.ResolveWipnoteAnchorForDir

// normalizeWorktreePath converts an absolute WorktreePath to a
// repo-relative, forward-slash form using the paths.NormalizeWithResolver
// contract.  Already-relative paths pass through unchanged.  Foreign paths
// (outside any wipnote repo) receive the "unresolved:" prefix.  Empty input
// is returned as-is (no-op / no panic).
//
// The resolver is invoked directly on filepath.Dir(worktreePath) so that
// worktrees that have already been removed from disk (WorktreeRemove case)
// can still be normalized — we never need the path to exist on disk.
func normalizeWorktreePath(worktreePath string) string {
	if worktreePath == "" {
		return ""
	}
	if !filepath.IsAbs(worktreePath) {
		return worktreePath
	}
	// Resolve the repo root from the parent directory of the worktree path.
	// We call the resolver explicitly rather than relying on NormalizeWithResolver's
	// internal nearestExistingDir walk so that already-removed worktree directories
	// (not on disk) are still normalized correctly.
	repoRoot := worktreePathResolver(filepath.Dir(worktreePath))
	norm, _ := paths.NormalizeWithResolver(worktreePath, repoRoot, worktreePathResolver)
	return norm
}

// recordSimpleEvent is a shared helper for hook handlers that record a single
// agent_event and always return Continue. It resolves the session and feature
// IDs from the event/database, builds the AgentEvent, and inserts it
// non-fatally. Returns Continue on missing session ID.
func recordSimpleEvent(
	eventType models.EventType,
	toolName, inputSummary, status string,
	event *CloudEvent,
	database *sql.DB,
) (*HookResult, error) {
	return recordSimpleEventWithOutput(eventType, toolName, inputSummary, "", status, event, database)
}

// recordSimpleEventWithOutput is recordSimpleEvent with an additional
// outputSummary field, used by handlers that want to persist structured detail
// (e.g. PreCompact recording the JSON-encoded context_stats) alongside the
// human-readable input summary. outputSummary may be empty.
func recordSimpleEventWithOutput(
	eventType models.EventType,
	toolName, inputSummary, outputSummary, status string,
	event *CloudEvent,
	database *sql.DB,
) (*HookResult, error) {
	sessionID := resolveSessionIDWithHarness(event)
	if sessionID == "" {
		// For non-Claude harnesses where the session_id was missing,
		// try the env var fallback as last resort.
		sessionID = EnvSessionID(event.SessionID)
	}
	if sessionID == "" {
		return &HookResult{Continue: true}, nil
	}

	featureID := cachedGetActiveFeatureID(database, sessionID)
	now := time.Now().UTC()

	ev := &models.AgentEvent{
		EventID:       uuid.New().String(),
		AgentID:       resolveEventAgentID(event),
		EventType:     eventType,
		Timestamp:     now,
		ToolName:      toolName,
		InputSummary:  inputSummary,
		OutputSummary: outputSummary,
		SessionID:     sessionID,
		FeatureID:     featureID,
		Status:        status,
		Source:        "hook",
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := db.InsertEvent(database, ev); err != nil {
		projectDir := ResolveProjectDir(event.CWD, event.SessionID)
		debugLog(projectDir, "[error] handler=%s session=%s: insert event: %v", toolName, sessionID[:minSessionLen(sessionID)], err)
	}

	return &HookResult{Continue: true}, nil
}

// recordSimpleEventRouted is recordSimpleEvent for the hot Stop hook: it builds
// the same single agent_event row but routes the INSERT through the daemon-first
// enqueue path (plan-2390966a slice-4) instead of the direct db.InsertEvent.
// Only the Stop handler uses it — the shared recordSimpleEvent stays on the
// direct path for the many cold observational hooks (PreCompact, WorktreeRemove,
// …) that are out of this slice's scope. handler labels the fallback metrics;
// projectRoot (parent of .wipnote/) feeds RouteHookWrite's daemon + DBPath
// resolution. Best-effort: it never blocks and always returns Continue.
func recordSimpleEventRouted(
	handler string,
	eventType models.EventType,
	toolName, inputSummary, status string,
	projectRoot, sessionID string,
	event *CloudEvent,
	database *sql.DB,
) (*HookResult, error) {
	if sessionID == "" {
		return &HookResult{Continue: true}, nil
	}
	now := time.Now().UTC()
	ev := &models.AgentEvent{
		EventID:      uuid.New().String(),
		AgentID:      resolveEventAgentID(event),
		EventType:    eventType,
		Timestamp:    now,
		ToolName:     toolName,
		InputSummary: inputSummary,
		SessionID:    sessionID,
		FeatureID:    cachedGetActiveFeatureID(database, sessionID),
		Status:       status,
		Source:       "hook",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	_ = RouteInsertEvent(handler, projectRoot, sessionID, ev, database)
	return &HookResult{Continue: true}, nil
}

// Stop handles the Stop Claude Code hook event (agent/session stopped).
// Records a checkpoint event and captures the last assistant message as output.
// Also reads the transcript JSONL to persist the assistant reply text as an
// assistant_text otel_signals row so the dashboard can render text-only turns.
func Stop(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	summary := "Agent stopped"
	if event.LastAssistantMessage != "" {
		msg := event.LastAssistantMessage
		if len(msg) > debugMsgMaxLen {
			msg = msg[:debugMsgMaxLen] + "…"
		}
		summary = fmt.Sprintf("Agent stopped: %s", msg)
	}

	// Read the transcript and persist the last assistant text turn as an
	// otel_signals row with canonical='assistant_text'. This is a fast
	// synchronous file read — no network calls. Errors are logged to
	// debug.log only and never block the Stop response.
	sessionID := resolveSessionIDWithHarness(event)
	if sessionID == "" {
		sessionID = EnvSessionID(event.SessionID)
	}
	if sessionID != "" {
		projectDir := ResolveProjectDir(event.CWD, event.SessionID)
		if !insertAssistantTextSignalFromHookPayload(database, projectDir, sessionID, event, event.LastAssistantMessage, "hook_payload") {
			insertAssistantTextSignal(database, projectDir, sessionID, event.TranscriptPath)
		}
		// Backfill any user prompts missed by the live UserPromptSubmit hook path.
		// Non-fatal: errors are logged to debug.log and never block the Stop response.
		if n, err := backfillMissedUserPrompts(database, projectDir, sessionID, event.TranscriptPath); err != nil {
			debugLog(projectDir, "[user-prompt-backfill] stop hook: %v", err)
		} else if n > 0 {
			debugLog(projectDir, "[user-prompt-backfill] stop: %d prompts recovered (session=%s)", n, sessionID[:minSessionLen(sessionID)])
		}
		// Finalize session HTML so the event-count badge is accurate for harnesses
		// (e.g. Codex) that map their task-complete event to this Stop handler
		// rather than to SessionEnd. Non-fatal: errors are silently logged inside.
		var evtCount int
		_ = database.QueryRow(`SELECT COUNT(*) FROM agent_events WHERE session_id = ?`, sessionID).Scan(&evtCount)
		FinalizeSessionHTML(projectDir, sessionID, time.Now().UTC().Format(time.RFC3339), "completed", evtCount)
	}

	// Session-exit reconciliation (slice-5, feat-f93fe770).
	//
	// AMENDMENT to the historical no-block contract: this handler previously
	// NEVER blocked (the only exit-2 wiring was TaskCompleted via
	// task_completion_gate.go). It now blocks (BlockExit2Error) — but ONLY for
	// harness=="claude" AND ONLY on ambiguous generator-drift. This narrow,
	// intentional amendment is scoped here: Gemini/Codex never block (a durable
	// warning is persisted and surfaced at the next SessionStart instead).
	//
	// skipPortDrift=true: Stop fires on EVERY model response (per-turn), not
	// just at true session boundaries. Running the port-drift check here cost
	// 8-22s/turn across 3 full regenerations. Port-drift enforcement is now
	// handled at commit-time by checkPortDriftCommitGuard (commit_portdrift_guard.go),
	// which fires only when generator-input files are staged — so correctness is
	// preserved at zero per-turn cost. The cheap orphan/uncommitted reconcile
	// classes still run on every Stop.
	if sessionID != "" {
		projectDir := ResolveProjectDir(event.CWD, event.SessionID)
		if err := runSessionExitReconcile(database, projectDir,
			currentHarness().String(), sessionID, true); err != nil {
			return nil, err
		}
	}

	// Route the terminal Stop agent_events INSERT through the daemon-first
	// enqueue path (plan-2390966a slice-4). Stop fires on EVERY model response,
	// so this is a hot write: routing it keeps the handler under 1s even when an
	// external writer holds the lock. Best-effort — canonical NDJSON + reindex
	// recover the row if the write degrades.
	return recordSimpleEventRouted("stop", models.EventEnd, "Stop", summary, "recorded",
		ResolveProjectDir(event.CWD, event.SessionID), resolveStopSessionID(event), event, database)
}

// resolveStopSessionID mirrors the Stop handler's session-id resolution
// (resolveSessionIDWithHarness with the EnvSessionID fallback) so the routed
// terminal-event recorder labels and routes under the same session ID the rest
// of the Stop handler used. Returns "" when no session can be resolved (the
// routed recorder then no-ops with Continue, matching recordSimpleEvent).
func resolveStopSessionID(event *CloudEvent) string {
	sessionID := resolveSessionIDWithHarness(event)
	if sessionID == "" {
		sessionID = EnvSessionID(event.SessionID)
	}
	return sessionID
}

// currentHarness resolves the active harness inside a hook handler. Handlers
// do not receive the parsed Harness value, but DetectHarness's env-based
// branches (CLAUDE_CODE_ENTRYPOINT, WIPNOTE_AGENT_ID) are authoritative and
// payload-independent — passing nil short-circuits payload parsing and yields
// the env-derived harness, which is exactly the discriminator slice-5 needs.
func currentHarness() Harness {
	return DetectHarness(nil)
}

// AfterAgent handles the Gemini CLI AfterAgent hook event. Gemini exposes the
// model's final text response directly in prompt_response, so capture it from
// the hook payload instead of trying to infer it from a harness transcript.
func AfterAgent(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	sessionID := resolveSessionIDWithHarness(event)
	if sessionID == "" {
		sessionID = EnvSessionID(event.SessionID)
	}
	if sessionID == "" {
		return &HookResult{Continue: true}, nil
	}

	projectDir := ResolveProjectDir(event.CWD, event.SessionID)
	insertAssistantTextSignalFromHookPayload(database, projectDir, sessionID, event, event.PromptResponse, "hook_payload")

	return &HookResult{Continue: true}, nil
}

// PreCompact handles the PreCompact Claude Code hook event.
//
// CONTROLLING CONTRACT (AdditiveControlling): returning
// {decision:"block", reason:"…"} PREVENTS the compaction. wipnote does NOT
// block today — there is no current trigger or use-case for refusing a
// compaction — so this handler is observe-only by behaviour. It does, however,
// now DECODE and RECORD the controlling-payload fields (compaction_trigger and
// context_stats), closing the MISMATCH-payload observability gap: the timeline
// can distinguish manual vs auto compactions and retains CC's pre-compaction
// context accounting.
//
// The handler is structured so that adding a future block condition is a
// one-line change: compute a `block` bool from event.CompactionTrigger /
// event.ContextStats and, when true, return
// &HookResult{Decision: "block", Reason: "…"} before the recordSimpleEvent
// call below. No structural rework required.
func PreCompact(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	trigger := event.CompactionTrigger
	if trigger == "" {
		trigger = "unspecified"
	}
	summary := fmt.Sprintf("Conversation compaction triggered (trigger=%s)", trigger)

	// Record CC's pre-compaction context accounting verbatim for observability.
	// Shape is harness-defined, so persist the JSON encoding in OutputSummary.
	var contextStatsJSON string
	if len(event.ContextStats) > 0 {
		if b, err := json.Marshal(event.ContextStats); err == nil {
			contextStatsJSON = string(b)
		}
	}

	// FUTURE BLOCK HOOK (intentionally inert today): to refuse a compaction,
	// set `block` from trigger/context_stats and return a block HookResult here.
	//   if block { return &HookResult{Decision: "block", Reason: "…"}, nil }

	return recordSimpleEventWithOutput(models.EventCheckPoint, "PreCompact", summary, contextStatsJSON, "recorded", event, database)
}

// PostCompact handles the PostCompact Claude Code hook event.
// Records a checkpoint after conversation context compaction completes, so
// subsequent re-reads of already-seen files are explainable in the timeline.
func PostCompact(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	return recordSimpleEvent(models.EventCheckPoint, "PostCompact", "Conversation compaction completed", "recorded", event, database)
}

// TeammateIdle handles the TeammateIdle Claude Code hook event.
// Records a teammate_idle event when a teammate agent goes idle.
//
// CONTROLLING CAPABILITY (AdditiveControlling, per hookEventContractSpecs): CC
// reads a JSON HookResult here, so this handler COULD return
// {decision:"block", reason:"…"} to keep a teammate busy. It is kept
// observe-only by design — there is no current use-case to override CC's idle
// handling — but the HookResult return path is already in place, so adding a
// future block condition is a one-line change.
func TeammateIdle(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	summary := "Teammate agent went idle"
	if event.TeammateName != "" {
		summary = fmt.Sprintf("Teammate %s went idle", event.TeammateName)
	}
	if event.IdleReason != "" {
		summary += fmt.Sprintf(" (reason: %s)", event.IdleReason)
	}
	return recordSimpleEvent(models.EventTeammateIdle, "TeammateIdle", summary, "recorded", event, database)
}

// TaskCreated handles the TaskCreated Claude Code hook event.
// Mirrors Claude Code tasks into wipnote as steps on the active feature,
// making wipnote the durable task tracking surface that survives session
// termination and is visible to other sessions.
func TaskCreated(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	sessionID := EnvSessionID(event.SessionID)
	if sessionID == "" {
		return &HookResult{Continue: true}, nil
	}

	featureID := cachedGetActiveFeatureID(database, sessionID)
	subject := event.TaskSubject
	if subject == "" {
		subject, _ = event.TaskData["subject"].(string)
	}
	description := event.TaskDescription
	if description == "" {
		description, _ = event.TaskData["description"].(string)
	}
	taskID := event.TaskID

	summary := "Task created"
	if subject != "" {
		summary = fmt.Sprintf("Task created: %s", subject)
	} else if taskID != "" {
		summary = fmt.Sprintf("Task created: task_id=%s", taskID)
	}

	now := time.Now().UTC()
	ev := &models.AgentEvent{
		EventID:       uuid.New().String(),
		AgentID:       resolveEventAgentID(event),
		EventType:     models.EventTaskCreated,
		Timestamp:     now,
		ToolName:      "TaskCreate",
		InputSummary:  summary,
		OutputSummary: description,
		SessionID:     sessionID,
		FeatureID:     featureID,
		Status:        "recorded",
		Source:        "hook",
		ClaudeTaskID:  taskID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := db.InsertEvent(database, ev); err != nil {
		projectDir := ResolveProjectDir(event.CWD, event.SessionID)
		debugLog(projectDir, "[error] handler=TaskCreated session=%s: insert event: %v", sessionID[:minSessionLen(sessionID)], err)
	}

	// Mirror as a step on the active feature so the task survives session end.
	if featureID != "" && taskID != "" {
		addTaskStep(database, sessionID, featureID, taskID, subject, event.TeammateName)
	}

	return &HookResult{Continue: true}, nil
}

// TaskCompleted handles the TaskCompleted Claude Code hook event.
// Marks the corresponding wipnote step as completed and records a
// task_completed agent_event for the timeline.
func TaskCompleted(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	sessionID := EnvSessionID(event.SessionID)
	if sessionID == "" {
		return &HookResult{Continue: true}, nil
	}

	featureID := cachedGetActiveFeatureID(database, sessionID)
	taskID := event.TaskID
	subject := event.TaskSubject
	if subject == "" {
		subject, _ = event.TaskData["subject"].(string)
	}

	summary := "Task completed"
	if subject != "" {
		summary = fmt.Sprintf("Task completed: %s", subject)
	} else if taskID != "" {
		summary = fmt.Sprintf("Task completed: task_id=%s", taskID)
	}

	now := time.Now().UTC()
	ev := &models.AgentEvent{
		EventID:      uuid.New().String(),
		AgentID:      resolveEventAgentID(event),
		EventType:    models.EventTaskCompleted,
		Timestamp:    now,
		ToolName:     "TaskComplete",
		InputSummary: summary,
		SessionID:    sessionID,
		FeatureID:    featureID,
		Status:       "completed",
		Source:       "hook",
		ClaudeTaskID: taskID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := db.InsertEvent(database, ev); err != nil {
		projectDir := ResolveProjectDir(event.CWD, event.SessionID)
		debugLog(projectDir, "[error] handler=TaskCompleted session=%s: insert event: %v", sessionID[:minSessionLen(sessionID)], err)
	}

	// quality gate only runs when a feature is actively claimed
	if featureID != "" {
		// Opt-in quality gate: run build/test before allowing task completion.
		projectDir := ResolveProjectDir(event.CWD, event.SessionID)
		blockOnFailure := readTaskCompletionConfig(projectDir)
		gate := runTaskCompletionGate(projectDir)
		if !gate.Passed {
			// Record the failure as an event regardless of blocking mode.
			recordSimpleEvent(models.EventQualityGate, "TaskCompletionGate",
				fmt.Sprintf("Quality gate failed: %s", gate.GateName), "failed", event, database)

			if blockOnFailure {
				msg := fmt.Sprintf("Quality gate %q failed. "+
					"To complete this task manually after fixing: wipnote feature complete %s",
					gate.GateName, featureID)
				return nil, &BlockExit2Error{Message: msg}
			}
		}
	}

	// Mark the step as completed on the feature HTML.
	if featureID != "" && taskID != "" {
		completeTaskStep(database, sessionID, featureID, taskID, event.TeammateName)
	}

	return &HookResult{Continue: true}, nil
}

// InstructionsLoaded handles the InstructionsLoaded Claude Code hook event.
//
// Observational event (stdout ignored by CC). Records a checkpoint when an
// instruction file (CLAUDE.md, AGENTS.md, a memory file, a glob-matched
// ruleset, …) is loaded. feat-cd937fc9 decodes and records the payload fields
// file_path / load_reason / memory_type / globs, closing the MISMATCH-payload
// observability gap so the timeline shows which instruction sources were loaded
// and why.
func InstructionsLoaded(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	summary := "Instruction files loaded (CLAUDE.md etc.)"
	if event.FilePath != "" {
		summary = fmt.Sprintf("Instructions loaded: %s", event.FilePath)
		if event.LoadReason != "" {
			summary += fmt.Sprintf(" (reason=%s)", event.LoadReason)
		}
		if event.MemoryType != "" {
			summary += fmt.Sprintf(" [memory=%s]", event.MemoryType)
		}
	}

	// Record the structured load detail (path, reason, memory type, globs) as
	// JSON in OutputSummary for the timeline.
	var detailJSON string
	detail := map[string]any{}
	if event.FilePath != "" {
		detail["file_path"] = event.FilePath
	}
	if event.LoadReason != "" {
		detail["load_reason"] = event.LoadReason
	}
	if event.MemoryType != "" {
		detail["memory_type"] = event.MemoryType
	}
	if len(event.Globs) > 0 {
		detail["globs"] = event.Globs
	}
	if len(detail) > 0 {
		if b, err := json.Marshal(detail); err == nil {
			detailJSON = string(b)
		}
	}

	return recordSimpleEventWithOutput(models.EventCheckPoint, "InstructionsLoaded", summary, detailJSON, "recorded", event, database)
}

// PermissionRequest handles the PermissionRequest Claude Code hook event.
//
// CONTROLLING CONTRACT (AdditiveControlling, feat-b396bd33): CC reads
// hookSpecificOutput.decision.behavior ("allow" | "deny") to decide the
// permission outcome. wipnote uses this CONSERVATIVELY:
//   - It auto-ALLOWS only the tight, read-only allowlist in
//     permission_allowlist.go (and only when risk_level is low/absent).
//   - For EVERYTHING ELSE it emits NO decision, so CC falls through to its
//     normal interactive prompt. wipnote NEVER auto-DENIES.
//
// The checkpoint event is always recorded first (observability is unchanged);
// the auto-allow decision is layered on top of that recorded result.
func PermissionRequest(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	summary := "Permission requested"
	if event.ToolName != "" {
		summary = fmt.Sprintf("Permission requested for tool: %s", event.ToolName)
	}

	result, err := recordSimpleEvent(models.EventCheckPoint, "PermissionRequest", summary, "recorded", event, database)
	if err != nil || result == nil {
		return result, err
	}

	// Conservative auto-allow: only a demonstrably read-only, allowlisted op
	// at low/absent risk gets a behavior:"allow"; all other requests fall
	// through to CC's normal prompt (no decision emitted).
	if permissionAutoAllow(event) {
		result.HookSpecificOutput = &HookSpecificOutput{
			HookEventName: "PermissionRequest",
			Decision: &PermissionDecision{
				Behavior: "allow",
				Message:  "Auto-approved: read-only wipnote query (wipnote allowlist).",
			},
		}
	}

	return result, nil
}

// ConfigChange handles the ConfigChange Claude Code hook event.
//
// It records the session's permission_mode durably so YOLO detection has a
// posture to fall back on when a later event carries no permission_mode of its
// own (isYoloFromRecordedMode). This used to UPDATE sessions.metadata in the
// per-project read index; that index is gone, so the record now lives in the
// session's own directory under .wipnote/ (yolo_guard.go).
//
// database is accepted for hook call-shape compatibility and unused.
func ConfigChange(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	_ = database
	if event.PermissionMode == "" {
		return &HookResult{Continue: true}, nil
	}
	sessionID := EnvSessionID(event.SessionID)
	if sessionID == "" {
		return &HookResult{Continue: true}, nil
	}
	projectDir := ResolveProjectDir(event.CWD, event.SessionID)
	if projectDir == "" {
		return &HookResult{Continue: true}, nil
	}
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	if err := RecordSessionPermissionMode(wipnoteDir, sessionID, event.PermissionMode); err != nil {
		debugLog(projectDir, "[error] handler=config-change session=%s: record permission mode: %v",
			sessionID[:minSessionLen(sessionID)], err)
	}
	return &HookResult{Continue: true}, nil
}

// WorktreeCreate handles the WorktreeCreate Claude Code hook event.
//
// REPLACEMENT hook: Claude Code expects the hook process to create the
// worktree and print only the created directory path on stdout. Callers must
// bypass the JSON HookResult response layer.
func WorktreeCreate(event *CloudEvent, database *sql.DB) (string, error) {
	if event == nil {
		return "", errors.New("missing WorktreeCreate event")
	}
	if event.WorktreePath != "" {
		if event.WorktreeBasePath == "" {
			event.WorktreeBasePath = filepath.Dir(event.WorktreePath)
		}
		if event.WorktreeName == "" {
			event.WorktreeName = filepath.Base(event.WorktreePath)
		}
	}
	if event.WorktreeBasePath == "" && event.WorktreeName != "" && event.CWD != "" {
		// Claude Code payloads (observed 2026-06-11) carry only "name"; default
		// to the harness's conventional location under the project directory.
		event.WorktreeBasePath = filepath.Join(event.CWD, ".claude", "worktrees")
	}
	if event.WorktreeBasePath == "" {
		return "", errors.New("missing worktree_base_path")
	}
	if event.WorktreeName == "" {
		return "", errors.New("missing worktree_name")
	}

	worktreePath := event.WorktreePath
	if worktreePath == "" {
		worktreePath = filepath.Join(event.WorktreeBasePath, event.WorktreeName)
	}
	createdPath, err := worktree.CreateForClaudeHook(event.CWD, worktreePath, event.WorktreeName, io.Discard)
	if err != nil {
		return "", err
	}

	// Checkpoint recording is best-effort and canonical-first: a nil database
	// means the derived-index open was unavailable (writer_unavailable), but
	// the worktree itself was created successfully, so we MUST still return the
	// bare path (#119 stdout contract). Skip the derived-index write when the
	// handle is nil; reindex recovers it on the next serve cycle.
	if database != nil {
		summary := fmt.Sprintf("Worktree created: %s", normalizeWorktreePath(createdPath))
		if _, err := recordSimpleEvent(models.EventCheckPoint, "WorktreeCreate", summary, "recorded", event, database); err != nil {
			projectDir := ResolveProjectDir(event.CWD, event.SessionID)
			debugLog(projectDir, "[worktree-create] checkpoint record failed: %v", err)
		}
	}

	return createdPath, nil
}

// WorktreeRemove handles the WorktreeRemove Claude Code hook event.
//
// OBSERVATIONAL event: CC ignores this hook's stdout entirely (see
// hookEventContractSpecs). Records when a git worktree is removed after work is
// complete and auto-completes any in-progress work items associated with the
// worktree branch.
//
// feat-cd937fc9 cleanup: a previous version returned an additionalContext
// message here. Because WorktreeRemove is Observational, CC discarded it — it
// never reached the agent. The dead return has been removed; the checkpoint
// recording and branch auto-completion (the real side effects) are retained.
func WorktreeRemove(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	summary := "Worktree removed"
	if event.WorktreePath != "" {
		normPath := normalizeWorktreePath(event.WorktreePath)
		summary = fmt.Sprintf("Worktree removed: %s", normPath)
	}

	result, err := recordSimpleEvent(models.EventCheckPoint, "WorktreeRemove", summary, "recorded", event, database)
	if err != nil || result == nil {
		return result, err
	}

	// Auto-complete in-progress work items for the removed worktree's branch.
	// The branch name is typically the last path component of the worktree path
	// (e.g. /path/to/worktrees/trk-abc12345 → "trk-abc12345").
	branch := extractBranchFromWorktreePath(event.WorktreePath)
	if branch != "" {
		completedItems := autoCompleteByBranch(branch, database)
		if len(completedItems) > 0 {
			projectDir := ResolveProjectDir(event.CWD, event.SessionID)
			debugLog(projectDir, "[worktree-remove] auto-completed %s (branch=%s)", strings.Join(completedItems, ", "), branch)
		}
	}

	return result, nil
}

// extractBranchFromWorktreePath extracts the branch name from a worktree path.
// Claude Code typically names worktrees after the branch, so the last path
// component is the branch name (e.g. "/repo/.claude/worktrees/trk-abc12345").
func extractBranchFromWorktreePath(worktreePath string) string {
	if worktreePath == "" {
		return ""
	}
	return filepath.Base(worktreePath)
}

// PostToolUseFailure handles the PostToolUseFailure Claude Code hook event.
// Records a tool crash/exception as an error event with details from ToolResult.
// This handler is kept separate because it extracts error info from ToolResult.
//
// CONTROLLING CAPABILITY (AdditiveControlling, per hookEventContractSpecs): CC
// reads a JSON HookResult here, so this handler COULD return a controlling
// shape (e.g. {decision:"block"} to halt after a tool failure). It is kept
// observe-only by design — no current use-case to alter CC's failure handling
// — but the HookResult return path is already in place for a future addition.
func PostToolUseFailure(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	sessionID := EnvSessionID(event.SessionID)
	if sessionID == "" {
		return &HookResult{Continue: true}, nil
	}

	featureID := cachedGetActiveFeatureID(database, sessionID)
	errorSummary := summariseOutput(event.ToolResult)
	if errorSummary == "" {
		errorSummary = fmt.Sprintf("tool %q crashed or threw exception", event.ToolName)
	}

	now := time.Now().UTC()
	ev := &models.AgentEvent{
		EventID:      uuid.New().String(),
		AgentID:      resolveEventAgentID(event),
		EventType:    models.EventError,
		Timestamp:    now,
		ToolName:     event.ToolName,
		InputSummary: fmt.Sprintf("PostToolUseFailure: %s", errorSummary),
		SessionID:    sessionID,
		FeatureID:    featureID,
		Status:       "failed",
		Source:       "hook",
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := db.InsertEvent(database, ev); err != nil {
		projectDir := ResolveProjectDir(event.CWD, event.SessionID)
		debugLog(projectDir, "[error] handler=posttooluse-failure session=%s: insert event: %v", sessionID[:minSessionLen(sessionID)], err)
	}

	return &HookResult{Continue: true}, nil
}
