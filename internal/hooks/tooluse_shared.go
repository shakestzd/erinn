package hooks

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	"github.com/shakestzd/htmlgraph/internal/agent"
	"github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/paths"
)

// featureIDCacheEntry holds the single cached result of GetActiveFeatureID for
// the lifetime of this process invocation. Each hook binary invocation handles
// exactly one CloudEvent, so the (sessionID → featureID) mapping is constant.
// No sync needed — hook handlers run in a single goroutine.
type featureIDCacheEntry struct {
	sessionID string
	featureID string
	populated bool
}

var featureIDCache featureIDCacheEntry

// cachedGetActiveFeatureID returns the active feature ID for sessionID,
// querying the database at most once per process invocation.
func cachedGetActiveFeatureID(database *sql.DB, sessionID string) string {
	if featureIDCache.populated && featureIDCache.sessionID == sessionID {
		return featureIDCache.featureID
	}
	featureID := GetActiveFeatureID(database, sessionID)
	featureIDCache = featureIDCacheEntry{
		sessionID: sessionID,
		featureID: featureID,
		populated: true,
	}
	return featureID
}

// toolUseContext holds resolved identifiers shared by PreToolUse and PostToolUse.
type toolUseContext struct {
	SessionID       string
	FeatureID       string
	AgentID         string
	AgentType       string
	IsSubagent      bool
	ProjectDir      string
	HgDir           string
	IsYoloMode      bool
	ParentEventID   string
	ParentSessionID string // parent session ID for subagent context lookups
	SessionCreatedAt time.Time // used for subagent grace period
	ClaimedItem     string // work_item_id of agent's active claim, or ""
}

// resolveToolUseContext resolves session, feature, agent identifiers, project
// directory, YOLO mode, and parent event ID from a CloudEvent and database.
// Returns nil when no active session is found, indicating the caller should
// skip all DB operations.
//
// Item 1 (feat-8b6fdf86): replaces 3 separate queries (GetSession,
// GetActiveFeatureID, HasActiveClaimByAgent) with a single SQL join via
// db.GetToolUseContext. The YOLO conditional queries remain separate since
// they only run in YOLO mode.
func resolveToolUseContext(event *CloudEvent, database *sql.DB) *toolUseContext {
	start := time.Now()

	sessionID := resolveSessionIDWithHarness(event)
	if sessionID == "" {
		return nil
	}

	agentID := resolveAgentID(event)
	isSubagent := isSubagentEvent(event)

	// Batch fetch: session row + active claim in one query (Item 1).
	var (
		featureID        string
		parentSessionID  string
		sessionCreatedAt time.Time
		claimedItem      string
	)
	if row, err := db.GetToolUseContext(database, sessionID, agentID); err == nil && row != nil {
		featureID = row.ActiveFeatureID
		parentSessionID = row.ParentSessionID
		sessionCreatedAt = row.CreatedAt
		claimedItem = row.ClaimedItem
		// Keep the process-level cache warm for other callers (missing_events etc.)
		featureIDCache = featureIDCacheEntry{
			sessionID: sessionID,
			featureID: featureID,
			populated: true,
		}
	} else {
		// Fallback: session not in DB yet (race during session-start).
		featureID = cachedGetActiveFeatureID(database, sessionID)
	}

	agentType := event.AgentType
	if agentType == "" {
		agentType = os.Getenv("HTMLGRAPH_AGENT_TYPE")
	}

	projectDir := ResolveProjectDir(event.CWD, event.SessionID)
	hgDir := filepath.Join(projectDir, ".htmlgraph")
	yolo := isYoloFromEvent(event, hgDir)
	parentEventID := resolveParentEventID(database, sessionID, agentID, isSubagent)

	LogTimed(projectDir, "pretooluse", map[string]string{
		"phase":   "resolve-context",
		"session": sessionID[:minSessionLen(sessionID)],
		"tool":    event.ToolName,
	}, start, "context resolved")

	return &toolUseContext{
		SessionID:        sessionID,
		FeatureID:        featureID,
		AgentID:          agentID,
		AgentType:        agentType,
		IsSubagent:       isSubagent,
		ProjectDir:       projectDir,
		HgDir:            hgDir,
		IsYoloMode:       yolo,
		ParentEventID:    parentEventID,
		ParentSessionID:  parentSessionID,
		SessionCreatedAt: sessionCreatedAt,
		ClaimedItem:      claimedItem,
	}
}

// isSubagentEvent returns true when the event originates from a subagent.
// Claude Code sets a non-empty agent_id (not "claude-code") for subagent hooks.
func isSubagentEvent(event *CloudEvent) bool {
	return event.AgentID != "" && event.AgentID != "claude-code"
}

// resolveAgentID returns the effective agent ID: the CloudEvent agent_id when
// present (subagent case), falling back to the per-subagent hint file
// (written by SubagentStart when CLAUDE_ENV_FILE is unset), then to the
// detected agent identity.
func resolveAgentID(event *CloudEvent) string {
	if event.AgentID != "" {
		return event.AgentID
	}
	// Check HTMLGRAPH_AGENT_ID env var (written to CLAUDE_ENV_FILE by SubagentStart
	// when CLAUDE_ENV_FILE is set).
	if id := os.Getenv("HTMLGRAPH_AGENT_ID"); id != "" {
		return id
	}
	// Fall back to the per-subagent hint file (written when CLAUDE_ENV_FILE is unset).
	sessionID := resolveSessionIDWithHarness(event)
	if sessionID != "" {
		if hint := paths.ReadSubagentHint(sessionID); hint.AgentID != "" {
			return hint.AgentID
		}
	}
	return agent.Detect().ID
}

// resolveEventAgentID returns the agent ID from the CloudEvent, falling back
// to the detected agent identity. Use this for non-tooluse handlers
// (Stop, TrackEvent, etc.) that receive a raw CloudEvent.
func resolveEventAgentID(event *CloudEvent) string {
	if event.AgentID != "" {
		return event.AgentID
	}
	return agent.Detect().ID
}

// resolveEventAgentType returns the agent type from the CloudEvent, falling
// back to the HTMLGRAPH_AGENT_TYPE env var.
func resolveEventAgentType(event *CloudEvent) string {
	if event.AgentType != "" {
		return event.AgentType
	}
	return os.Getenv("HTMLGRAPH_AGENT_TYPE")
}

// resolveParentEventID finds the parent event using a multi-step fallback that
// mirrors the Python event_tracker.py logic:
//  1. Env var HTMLGRAPH_PARENT_PROMPT_EVENT (written by PreToolUse at tool-call time)
//  2. Env var HTMLGRAPH_PARENT_EVENT (written by SubagentStart when CLAUDE_ENV_FILE set)
//  3. Per-subagent hint file parent_event_id (written when CLAUDE_ENV_FILE unset)
//  4. For subagents: task_delegation row matching our agent_id (Method 0.5)
//  5. Most recent UserQuery in this session (orchestrator default)
func resolveParentEventID(database *sql.DB, sessionID, agentID string, isSubagent bool) string {
	// HTMLGRAPH_PARENT_PROMPT_EVENT is written to CLAUDE_ENV_FILE by PreToolUse at
	// the moment the tool starts (before the tool executes). Reading it here — in
	// both PreToolUse and PostToolUse — eliminates the race where a new UserQuery
	// arrives while a long-running tool is executing. Without this, the
	// LatestEventByTool("UserQuery") fallback at the bottom of this chain would
	// return the *newer* UserQuery (wrong parent) when PostToolUse fires.
	//
	// Validate that the env-var event ID actually exists in the current DB before
	// returning it. In tests, multiple hook invocations share a process and an
	// earlier PreToolUse may have set this env var for a different DB instance —
	// using a stale ID that doesn't exist in the current DB causes FK violations
	// and silent InsertEvent failures.
	if v := os.Getenv("HTMLGRAPH_PARENT_PROMPT_EVENT"); v != "" {
		if db.EventExists(database, v) {
			return v
		}
	}

	// TODO(bug-cb4918d8): remove HTMLGRAPH_PARENT_EVENT read after lineage
	// wiring verified end-to-end — this env var is never set in subagent
	// hook contexts; the subagent-hint file and DB fallback carry the load.
	parentEventID := os.Getenv("HTMLGRAPH_PARENT_EVENT")

	if parentEventID == "" && sessionID != "" {
		// Check per-subagent hint file (fallback for CLAUDE_ENV_FILE-unset case).
		if hint := paths.ReadSubagentHint(sessionID); hint.AgentID == agentID && hint.ParentEventID != "" {
			parentEventID = hint.ParentEventID
		}
	}

	if parentEventID == "" && (isSubagent || agentID != agent.Detect().ID) {
		parentEventID, _ = db.FindDelegationByAgent(database, sessionID, agentID)
	}

	if parentEventID == "" {
		parentEventID, _ = db.LatestEventByTool(database, sessionID, "UserQuery")
	}

	return parentEventID
}
