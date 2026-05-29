package hooks

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shakestzd/wipnote/internal/db"
	"github.com/shakestzd/wipnote/internal/models"
	"github.com/shakestzd/wipnote/internal/paths"
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
		EventID:      uuid.New().String(),
		AgentID:      resolveEventAgentID(event),
		EventType:    eventType,
		Timestamp:    now,
		ToolName:     toolName,
		InputSummary: inputSummary,
		SessionID:    sessionID,
		FeatureID:    featureID,
		Status:       status,
		Source:       "hook",
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := db.InsertEvent(database, ev); err != nil {
		projectDir := ResolveProjectDir(event.CWD, event.SessionID)
		debugLog(projectDir, "[error] handler=%s session=%s: insert event: %v", toolName, sessionID[:minSessionLen(sessionID)], err)
	}

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
	// just at true session boundaries. Running pluginbuild.CheckPorts here cost
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

	return recordSimpleEvent(models.EventEnd, "Stop", summary, "recorded", event, database)
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
// Records a checkpoint before conversation context compaction.
func PreCompact(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	return recordSimpleEvent(models.EventCheckPoint, "PreCompact", "Conversation compaction triggered", "recorded", event, database)
}

// PostCompact handles the PostCompact Claude Code hook event.
// Records a checkpoint after conversation context compaction completes, so
// subsequent re-reads of already-seen files are explainable in the timeline.
func PostCompact(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	return recordSimpleEvent(models.EventCheckPoint, "PostCompact", "Conversation compaction completed", "recorded", event, database)
}

// TeammateIdle handles the TeammateIdle Claude Code hook event.
// Records a teammate_idle event when a teammate agent goes idle.
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
// Records a checkpoint when CLAUDE.md or other instruction files are loaded.
func InstructionsLoaded(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	return recordSimpleEvent(models.EventCheckPoint, "InstructionsLoaded", "Instruction files loaded (CLAUDE.md etc.)", "recorded", event, database)
}

// PermissionRequest handles the PermissionRequest Claude Code hook event.
// Records a checkpoint when Claude requests a permission prompt.
func PermissionRequest(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	summary := "Permission requested"
	if event.ToolName != "" {
		summary = fmt.Sprintf("Permission requested for tool: %s", event.ToolName)
	}
	return recordSimpleEvent(models.EventCheckPoint, "PermissionRequest", summary, "recorded", event, database)
}

// ConfigChange handles the ConfigChange Claude Code hook event.
// Upserts the session's permission_mode into the sessions.metadata JSON column
// so that YOLO detection can use a DB lookup instead of the .launch-mode file.
func ConfigChange(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	if event.PermissionMode == "" {
		return &HookResult{Continue: true}, nil
	}
	sessionID := EnvSessionID(event.SessionID)
	if sessionID == "" {
		return &HookResult{Continue: true}, nil
	}
	_, err := database.Exec(
		`UPDATE sessions SET metadata = json_set(COALESCE(metadata, '{}'), '$.permission_mode', ?) WHERE session_id = ?`,
		event.PermissionMode, sessionID,
	)
	if err != nil {
		projectDir := ResolveProjectDir(event.CWD, event.SessionID)
		debugLog(projectDir, "[error] handler=config-change session=%s: update metadata: %v", sessionID[:minSessionLen(sessionID)], err)
	}
	return &HookResult{Continue: true}, nil
}

// WorktreeCreate handles the WorktreeCreate Claude Code hook event.
// Records when a git worktree is created for isolated work.
func WorktreeCreate(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	summary := "Worktree created"
	if event.WorktreePath != "" {
		normPath := normalizeWorktreePath(event.WorktreePath)
		summary = fmt.Sprintf("Worktree created: %s", normPath)
	}
	return recordSimpleEvent(models.EventCheckPoint, "WorktreeCreate", summary, "recorded", event, database)
}

// WorktreeRemove handles the WorktreeRemove Claude Code hook event.
// Records when a git worktree is removed after work is complete.
// Auto-completes any in-progress work items associated with the worktree branch.
// Also injects additionalContext to redirect the agent back to the project root
// so it can run final checks even though its CWD no longer exists.
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
	var completedItems []string
	if branch != "" {
		completedItems = autoCompleteByBranch(branch, database)
		if len(completedItems) > 0 {
			projectDir := ResolveProjectDir(event.CWD, event.SessionID)
			debugLog(projectDir, "[worktree-remove] auto-completed %s (branch=%s)", strings.Join(completedItems, ", "), branch)
		}
	}

	// Inject guidance so the agent can complete post-worktree steps.
	// The worktree directory no longer exists — any Bash command using the
	// old CWD will fail. Tell the agent to switch to the project root.
	projectRoot := ResolveProjectDir(event.CWD, event.SessionID)
	if projectRoot != "" {
		msg := fmt.Sprintf(
			"WORKTREE REMOVED: Your working directory (%s) no longer exists. "+
				"All subsequent Bash commands must use absolute paths or cd to the project root first. "+
				"Project root: %s — use this for any remaining steps (marking feature done, final checks, etc.).",
			event.WorktreePath, projectRoot,
		)
		if len(completedItems) > 0 {
			msg += fmt.Sprintf("\nAuto-completed work items: %s", strings.Join(completedItems, ", "))
		}
		result.AdditionalContext = msg
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
