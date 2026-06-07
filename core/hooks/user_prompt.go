package hooks

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/ingest"
	"github.com/shakestzd/wipnote/core/models"
)

// UserPrompt handles the UserPromptSubmit Claude Code hook event.
// It inserts a UserQuery agent_event, classifies the prompt intent,
// and returns combined CIGS attribution + classification guidance.
func UserPrompt(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	sessionID := resolveSessionIDWithHarness(event)
	if sessionID == "" || event.Prompt == "" {
		return &HookResult{Continue: true}, nil
	}

	// Skip subagent-dispatched prompts. Subagent prompts are already captured via
	// the SubagentStart handler's task_delegation event, which links to the
	// orchestrator's UserQuery as parent. Recording a separate UserQuery row for
	// the subagent would create a duplicate top-level turn. The task_delegation
	// and subsequent subagent tool calls provide full lineage and prompt context.
	if isSubagentEvent(event) {
		return &HookResult{Continue: true}, nil
	}

	// Backfill: ensure this session has a row in SQLite. The SessionStart hook
	// may not have fired (session started before plugin loaded, or hook failed).
	// This is idempotent — INSERT OR IGNORE won't overwrite existing rows.
	ensureSessionExists(database, sessionID, event)

	featureID := cachedGetActiveFeatureID(database, sessionID)

	promptSummary := sanitizePrompt(event.Prompt)
	if promptSummary == "" {
		return &HookResult{Continue: true}, nil
	}
	if ingest.IsSystemMessage(promptSummary) {
		return &HookResult{Continue: true}, nil
	}
	if len(promptSummary) > promptSummaryMaxLen {
		promptSummary = promptSummary[:promptSummaryMaxLen] + "…"
	}

	// Dedup: skip if identical UserQuery was recorded in last 5 seconds.
	recentCount, _ := db.CountRecentDuplicates(database, sessionID, "UserQuery", promptSummary, 5)
	if recentCount > 0 {
		return &HookResult{Continue: true}, nil
	}

	ev := &models.AgentEvent{
		EventID:      uuid.New().String(),
		AgentID:      resolveEventAgentID(event),
		EventType:    models.EventToolCall,
		Timestamp:    time.Now().UTC(),
		ToolName:     "UserQuery",
		InputSummary: promptSummary,
		SessionID:    sessionID,
		FeatureID:    featureID,
		Status:       "recorded",
		Source:       "hook",
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}

	if err := db.InsertEvent(database, ev); err != nil {
		debugLog(ResolveProjectDir(event.CWD, event.SessionID), "[error] handler=user-prompt session=%s: insert event: %v", sessionID[:minSessionLen(sessionID)], err)
	}

	// Update session last_user_query fields.
	updateLastQuery(database, sessionID, event.Prompt)

	// Classify the prompt intent for CIGS guidance.
	intent := ClassifyPrompt(event.Prompt)

	// Look up active work item type for intent-specific directives.
	activeWorkType := getActiveWorkItemType(database, featureID)

	// Build terse active item one-liner (only when active item exists).
	activeItemHint := buildActiveItemOneLiner(database, featureID)

	// Combine classification guidance with terse active item hint.
	guidance := GenerateGuidance(intent, featureID, activeWorkType, activeItemHint)

	result := &HookResult{}
	if guidance != "" {
		result.AdditionalContext = guidance
	} else {
		result.Continue = true
	}
	return result, nil
}

// ensureSessionExists creates a minimal session row if one doesn't exist.
// This backfills sessions that started before the plugin was loaded or when
// the SessionStart hook failed. The INSERT OR IGNORE is idempotent.
// agent_assigned is set from the incoming event so that Codex/Gemini sessions
// are correctly attributed (not hardcoded to 'claude-code').
func ensureSessionExists(database *sql.DB, sessionID string, event *CloudEvent) {
	if sessionID == "" || database == nil {
		return
	}
	var exists int
	database.QueryRow("SELECT 1 FROM sessions WHERE session_id = ?", sessionID).Scan(&exists) //nolint:errcheck
	if exists == 1 {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	agentID := resolveEventAgentID(event)
	_, _ = database.Exec(`
		INSERT OR IGNORE INTO sessions (session_id, agent_assigned, status, created_at, project_dir)
		VALUES (?, ?, 'active', ?, ?)`,
		sessionID, agentID, now, ResolveProjectDir(event.CWD, event.SessionID))
}

// updateLastQuery refreshes last_user_query_at and last_user_query on the session.
func updateLastQuery(database *sql.DB, sessionID, prompt string) {
	summary := prompt
	if len(summary) > sessionQueryMaxLen {
		summary = summary[:sessionQueryMaxLen] + "…"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = database.Exec(`
		UPDATE sessions
		SET last_user_query_at = ?,
		    last_user_query = ?
		WHERE session_id = ?`,
		now, summary, sessionID,
	)
}

// compactCLIRef is a per-turn CLI quick-reference injected into CIGS guidance.
// Keep in sync with the constant in help.go.
const compactCLIRef = `**wipnote CLI** — feature|bug|spike|track|plan [create|show|start|complete|list|add-step|delete] · find <q> · wip · status · snapshot · link [add|remove|list] · session [list|show] · analytics [summary|velocity] · check · health · spec|tdd|review|compliance <id> · batch [apply|export] · ingest · reindex · yolo --feature <id>
**Required flags:** feature/bug create need --track <id> --description "…"`

// buildActiveItemOneLiner returns a terse "ACTIVE: <id> — <title>" string when
// an active item is set, or empty string when none. Used per-turn in UserPromptSubmit.
func buildActiveItemOneLiner(database *sql.DB, featureID string) string {
	if featureID == "" {
		return ""
	}

	var title sql.NullString
	err := database.QueryRow(
		`SELECT title FROM features WHERE id = ?`, featureID,
	).Scan(&title)
	if err != nil || !title.Valid || title.String == "" {
		return fmt.Sprintf("ACTIVE: %s", featureID)
	}
	return fmt.Sprintf("ACTIVE: %s — %s", featureID, title.String)
}

type workItemRow struct {
	id     string
	title  string
	status string
	itype  string
}

// listOpenWorkItems returns in-progress and todo features/bugs/spikes.
func listOpenWorkItems(database *sql.DB) []workItemRow {
	rows, err := database.Query(`
		SELECT id, title, status, type
		FROM features
		WHERE status IN ('in-progress', 'todo', 'active')
		ORDER BY
			CASE status WHEN 'in-progress' THEN 0 ELSE 1 END,
			CASE type WHEN 'feature' THEN 0 WHEN 'bug' THEN 1 ELSE 2 END,
			created_at DESC
		LIMIT ?`,
		maxOpenWorkItemsDisplay,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var items []workItemRow
	for rows.Next() {
		var r workItemRow
		if err := rows.Scan(&r.id, &r.title, &r.status, &r.itype); err == nil {
			items = append(items, r)
		}
	}
	return items
}

// getActiveWorkItemType returns the type ("feature", "bug", "spike") of the
// active work item, or "" if no active item or lookup fails.
func getActiveWorkItemType(database *sql.DB, featureID string) string {
	if featureID == "" {
		return ""
	}
	var itemType sql.NullString
	_ = database.QueryRow(
		`SELECT type FROM features WHERE id = ?`, featureID,
	).Scan(&itemType)
	return itemType.String
}

// sanitizePrompt strips XML notification/reminder blocks from prompt text.
func sanitizePrompt(s string) string {
	for _, tag := range []string{"task-notification", "system-reminder", "command-message", "local-command-caveat"} {
		open := "<" + tag + ">"
		close := "</" + tag + ">"
		for {
			i := strings.Index(s, open)
			if i == -1 {
				break
			}
			j := strings.Index(s[i:], close)
			if j == -1 {
				s = s[:i]
				break
			}
			s = s[:i] + s[i+j+len(close):]
		}
	}
	// Strip lines that are just notification artifacts
	var cleaned []string
	for line := range strings.SplitSeq(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "Full transcript available at:") {
			continue
		}
		if strings.HasPrefix(trimmed, "Read the output file to retrieve") {
			continue
		}
		cleaned = append(cleaned, trimmed)
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

func joinLines(lines []string) string {
	result := ""
	for i, l := range lines {
		result += l
		if i < len(lines)-1 {
			result += "\n"
		}
	}
	return result
}
