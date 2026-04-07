package hooks

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/models"
)

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
	sessionID := EnvSessionID(event.SessionID)
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
func Stop(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	summary := "Agent stopped"
	if event.LastAssistantMessage != "" {
		msg := event.LastAssistantMessage
		if len(msg) > debugMsgMaxLen {
			msg = msg[:debugMsgMaxLen] + "…"
		}
		summary = fmt.Sprintf("Agent stopped: %s", msg)
	}
	return recordSimpleEvent(models.EventEnd, "Stop", summary, "recorded", event, database)
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
	return recordSimpleEvent(models.EventTeammateIdle, "TeammateIdle", "Teammate agent went idle", "recorded", event, database)
}

// TaskCreated handles the TaskCreated Claude Code hook event.
// Mirrors Claude Code tasks into HtmlGraph as steps on the active feature,
// making HtmlGraph the durable task tracking surface that survives session
// termination and is visible to other sessions.
func TaskCreated(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	sessionID := EnvSessionID(event.SessionID)
	if sessionID == "" {
		return &HookResult{Continue: true}, nil
	}

	featureID := cachedGetActiveFeatureID(database, sessionID)
	subject, _ := event.TaskData["subject"].(string)
	description, _ := event.TaskData["description"].(string)
	taskID := event.TaskID

	summary := "Task created"
	if subject != "" {
		summary = fmt.Sprintf("Task created: %s", subject)
	} else if taskID != "" {
		summary = fmt.Sprintf("Task created: task_id=%s", taskID)
	}

	now := time.Now().UTC()
	ev := &models.AgentEvent{
		EventID:      uuid.New().String(),
		AgentID:      resolveEventAgentID(event),
		EventType:    models.EventCheckPoint,
		Timestamp:    now,
		ToolName:     "TaskCreate",
		InputSummary: summary,
		OutputSummary: description,
		SessionID:    sessionID,
		FeatureID:    featureID,
		Status:       "recorded",
		Source:       "hook",
		ClaudeTaskID: taskID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := db.InsertEvent(database, ev); err != nil {
		projectDir := ResolveProjectDir(event.CWD, event.SessionID)
		debugLog(projectDir, "[error] handler=TaskCreated session=%s: insert event: %v", sessionID[:minSessionLen(sessionID)], err)
	}

	// Mirror as a step on the active feature so the task survives session end.
	if featureID != "" && taskID != "" {
		addTaskStep(database, sessionID, featureID, taskID, subject)
	}



	return &HookResult{Continue: true}, nil
}

// TaskCompleted handles the TaskCompleted Claude Code hook event.
// Marks the corresponding HtmlGraph step as completed and records a
// task_completed agent_event for the timeline.
func TaskCompleted(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	sessionID := EnvSessionID(event.SessionID)
	if sessionID == "" {
		return &HookResult{Continue: true}, nil
	}

	featureID := cachedGetActiveFeatureID(database, sessionID)
	taskID := event.TaskID
	subject, _ := event.TaskData["subject"].(string)

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

	// Mark the step as completed on the feature HTML.
	if featureID != "" && taskID != "" {
		completeTaskStep(database, sessionID, featureID, taskID)
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

// WorktreeCreate handles the WorktreeCreate Claude Code hook event.
// Records when a git worktree is created for isolated work.
func WorktreeCreate(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	summary := "Worktree created"
	if event.WorktreePath != "" {
		summary = fmt.Sprintf("Worktree created: %s", event.WorktreePath)
	}
	return recordSimpleEvent(models.EventCheckPoint, "WorktreeCreate", summary, "recorded", event, database)
}

// worktreeItemRe matches work item IDs (feat-, bug-, spk-) with an 8-char hex suffix.
var worktreeItemRe = regexp.MustCompile(`^(feat|bug|spk)-[0-9a-f]{8}$`)

// extractWorktreeItemID extracts a closable work item ID (feat-, bug-, spk-)
// from a worktree path. The convention is that feature worktrees are created at
// .claude/worktrees/<itemID>, so the basename of the path is the item ID.
// Returns "" when the path doesn't contain a closable item ID.
func extractWorktreeItemID(worktreePath string) string {
	if worktreePath == "" {
		return ""
	}
	// Strip trailing slash before extracting basename.
	base := filepath.Base(strings.TrimRight(worktreePath, "/"))
	if worktreeItemRe.MatchString(base) {
		return base
	}
	return ""
}

// WorktreeRemove handles the WorktreeRemove Claude Code hook event.
// Records when a git worktree is removed after work is complete.
// Auto-completes any work item whose ID matches the worktree directory name,
// then injects additionalContext to redirect the agent back to the project root.
func WorktreeRemove(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	summary := "Worktree removed"
	if event.WorktreePath != "" {
		summary = fmt.Sprintf("Worktree removed: %s", event.WorktreePath)
	}

	result, err := recordSimpleEvent(models.EventCheckPoint, "WorktreeRemove", summary, "recorded", event, database)
	if err != nil || result == nil {
		return result, err
	}

	// Auto-complete the work item embedded in the worktree path.
	// Feature worktrees are at .claude/worktrees/<feat-xxx> — when the
	// worktree is removed, the feature should be marked done automatically.
	var autoCompleted string
	if itemID := extractWorktreeItemID(event.WorktreePath); itemID != "" {
		if completeIfInProgress(itemID, database) {
			autoCompleted = itemID
			projectDir := ResolveProjectDir(event.CWD, event.SessionID)
			debugLog(projectDir, "[worktree-remove] auto-completed %s on worktree removal", itemID)
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
				"Project root: %s",
			event.WorktreePath, projectRoot,
		)
		if autoCompleted != "" {
			msg += fmt.Sprintf(" — Auto-completed %s on worktree removal.", autoCompleted)
		}
		result.AdditionalContext = msg
	}

	return result, nil
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
