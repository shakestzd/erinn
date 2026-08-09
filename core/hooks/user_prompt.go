package hooks

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shakestzd/wipnote/core/daemon"
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

	// Resolve the project root once for daemon-first write routing (plan-2390966a
	// slice-4). It is the parent of .wipnote/ and feeds RouteHookWrite's daemon
	// enqueue + bounded-fallback DBPath resolution.
	projectDir := ResolveProjectDir(event.CWD, event.SessionID)

	// Backfill: ensure this session has a row in SQLite. The SessionStart hook
	// may not have fired (session started before plugin loaded, or hook failed).
	// This is idempotent — INSERT OR IGNORE won't overwrite existing rows.
	ensureSessionExists(database, projectDir, sessionID, event)

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

	// Route the UserQuery agent_events INSERT through the daemon-first enqueue
	// path (plan-2390966a slice-4): no direct writable handle when the daemon is
	// reachable, <1s bounded fallback otherwise. Best-effort like the prior
	// db.InsertEvent — the canonical NDJSON + reindex backstop recovers the row.
	_ = RouteInsertEvent("user-prompt", projectDir, sessionID, ev, database)

	// Update session last_user_query fields.
	updateLastQuery(database, projectDir, sessionID, event.Prompt)

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
//
// The existence probe is a READ (no lock contention); the backfill INSERT is
// routed through the daemon-first enqueue path (plan-2390966a slice-4) so it
// never opens a direct writable handle when the daemon is reachable and degrades
// to a <1s bounded fallback otherwise.
func ensureSessionExists(database *sql.DB, projectDir, sessionID string, event *CloudEvent) {
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
	_ = routeHookWriteVia("user-prompt", projectDir, sessionID, database, `
		INSERT OR IGNORE INTO sessions (session_id, agent_assigned, status, created_at, project_dir)
		VALUES (?, ?, 'active', ?, ?)`,
		sessionID, agentID, now, ResolveProjectDir(event.CWD, event.SessionID))
}

// updateLastQuery refreshes last_user_query_at and last_user_query on the
// session. The UPDATE is routed through the daemon-first enqueue path
// (plan-2390966a slice-4) — best-effort, never blocking the hook.
func updateLastQuery(database *sql.DB, projectDir, sessionID, prompt string) {
	summary := prompt
	if len(summary) > sessionQueryMaxLen {
		summary = summary[:sessionQueryMaxLen] + "…"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_ = routeHookWriteVia("user-prompt", projectDir, sessionID, database, `
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
	item, found := LookupWorkItem(database, featureID)
	if !found || item.Title == "" {
		return fmt.Sprintf("ACTIVE: %s", featureID)
	}
	return fmt.Sprintf("ACTIVE: %s — %s", featureID, item.Title)
}

type workItemRow struct {
	id     string
	title  string
	status string
	itype  string
}

// listOpenWorkItems returns in-progress and todo features/bugs/spikes.
func listOpenWorkItems(database *sql.DB) []workItemRow {
	found := ListWorkItems(database, daemon.WorkItemListArgs{
		Statuses: []string{"in-progress", "todo", "active"},
		Limit:    maxOpenWorkItemsDisplay,
	})
	items := make([]workItemRow, 0, len(found))
	for _, it := range found {
		items = append(items, workItemRow{id: it.ID, title: it.Title, status: it.Status, itype: it.Type})
	}
	return items
}

// getActiveWorkItemType returns the type ("feature", "bug", "spike") of the
// active work item, or "" if no active item or lookup fails.
func getActiveWorkItemType(database *sql.DB, featureID string) string {
	if featureID == "" {
		return ""
	}
	item, _ := LookupWorkItem(database, featureID)
	return item.Type
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
