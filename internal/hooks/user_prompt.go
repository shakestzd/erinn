package hooks

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/models"
)

// UserPrompt handles the UserPromptSubmit Claude Code hook event.
// It inserts a UserQuery agent_event, classifies the prompt intent,
// and returns combined CIGS attribution + classification guidance.
func UserPrompt(event *CloudEvent, database *sql.DB) (*HookResult, error) {
	sessionID := resolveSessionIDWithHarness(event)
	if sessionID == "" || event.Prompt == "" {
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

	// Build attribution block (open work items listing).
	attributionBlock := buildAttributionGuidance(database, sessionID, featureID)

	// Combine classification guidance with attribution.
	guidance := GenerateGuidance(intent, featureID, activeWorkType, attributionBlock)

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
const compactCLIRef = `**htmlgraph CLI** — feature|bug|spike|track|plan [create|show|start|complete|list|add-step|delete] · find <q> · wip · status · snapshot · link [add|remove|list] · session [list|show] · analytics [summary|velocity] · check · health · spec|tdd|review|compliance <id> · batch [apply|export] · ingest · reindex · yolo --feature <id>
**Required flags:** feature/bug create need --track <id> --description "…"`

// buildAttributionGuidance returns a compact CIGS attribution block listing
// open work items so Claude can call htmlgraph feature start for the right item.
func buildAttributionGuidance(database *sql.DB, sessionID, activeFeatureID string) string {
	open := listOpenWorkItems(database)
	if len(open) == 0 {
		return ""
	}

	activeContext := buildActiveFeatureContext(database, activeFeatureID)

	lines := []string{
		"## Work Item Attribution (CIGS)",
		"",
	}

	if activeContext != "" {
		lines = append(lines, activeContext, "")
	} else {
		lines = append(lines, "**ACTIVE**: none", "")
	}

	lines = append(lines, "**Open work items** — run `htmlgraph feature start <id>`:")
	for _, item := range open {
		if item.id == activeFeatureID {
			continue // already shown in active context above
		}
		lines = append(lines, fmt.Sprintf("  `%s` — %s [%s]", item.id, item.title, item.status))
	}
	lines = append(lines, "", compactCLIRef)
	return joinLines(lines)
}

// buildActiveFeatureContext returns a rich context block for the active feature.
// Returns empty string if no active feature or feature not found.
func buildActiveFeatureContext(database *sql.DB, featureID string) string {
	if featureID == "" {
		return ""
	}

	var title, description, status, trackID sql.NullString
	var stepsTotal, stepsCompleted int
	err := database.QueryRow(`
		SELECT title, description, status, track_id, steps_total, steps_completed
		FROM features WHERE id = ?`, featureID,
	).Scan(&title, &description, &status, &trackID, &stepsTotal, &stepsCompleted)
	if err != nil {
		return "**ACTIVE**: " + featureID
	}

	lines := []string{
		fmt.Sprintf("**ACTIVE**: %s — %s", featureID, title.String),
	}

	if description.Valid && description.String != "" {
		desc := description.String
		if len(desc) > activeDescMaxLen {
			desc = desc[:activeDescMaxLen] + "…"
		}
		lines = append(lines, fmt.Sprintf("  Description: %s", desc))
	}

	if stepsTotal > 0 {
		lines = append(lines, fmt.Sprintf("  Steps: %d/%d complete", stepsCompleted, stepsTotal))
	} else {
		lines = append(lines, "  Steps: none defined — add with `htmlgraph feature add-step`")
	}

	blockers := queryBlockedBy(database, featureID)
	if len(blockers) > 0 {
		var parts []string
		for _, b := range blockers {
			marker := "○"
			if b.status == "done" {
				marker = "✓"
			}
			parts = append(parts, fmt.Sprintf("%s %s", b.id, marker))
		}
		lines = append(lines, fmt.Sprintf("  Blocked by: %s", strings.Join(parts, ", ")))
	}

	if trackID.Valid && trackID.String != "" {
		if trackInfo := queryTrackProgress(database, trackID.String); trackInfo != "" {
			lines = append(lines, fmt.Sprintf("  Track: %s", trackInfo))
		}
	}

	var transcriptPath sql.NullString
	_ = database.QueryRow(`
		SELECT s.transcript_path FROM sessions s
		JOIN agent_events ae ON ae.session_id = s.session_id
		WHERE ae.feature_id = ?
		ORDER BY ae.timestamp ASC LIMIT 1`, featureID,
	).Scan(&transcriptPath)
	if transcriptPath.Valid && transcriptPath.String != "" {
		lines = append(lines, fmt.Sprintf("  Created in: %s", transcriptPath.String))
	}

	return strings.Join(lines, "\n")
}

type blockerInfo struct {
	id     string
	title  string
	status string
}

func queryBlockedBy(database *sql.DB, featureID string) []blockerInfo {
	rows, err := database.Query(`
		SELECT ge.to_node_id, COALESCE(f.title, ''), COALESCE(f.status, 'unknown')
		FROM graph_edges ge
		LEFT JOIN features f ON ge.to_node_id = f.id
		WHERE ge.from_node_id = ? AND ge.relationship_type = 'blocked_by'`,
		featureID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var blockers []blockerInfo
	for rows.Next() {
		var b blockerInfo
		if rows.Scan(&b.id, &b.title, &b.status) == nil {
			blockers = append(blockers, b)
		}
	}
	return blockers
}

func queryTrackProgress(database *sql.DB, trackID string) string {
	var trackTitle sql.NullString
	_ = database.QueryRow(`SELECT title FROM tracks WHERE id = ?`, trackID).Scan(&trackTitle)

	var total int
	var done sql.NullInt64
	_ = database.QueryRow(`
		SELECT COUNT(*), SUM(CASE WHEN f.status = 'done' THEN 1 ELSE 0 END)
		FROM features f
		WHERE f.track_id = ?`, trackID).Scan(&total, &done)

	label := trackID
	if trackTitle.Valid && trackTitle.String != "" {
		label = fmt.Sprintf("%s \"%s\"", trackID, trackTitle.String)
	}
	if total > 0 {
		return fmt.Sprintf("%s (%d/%d done)", label, done.Int64, total)
	}
	return label
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

func activeFeatureOrNone(id string) string {
	if id == "" {
		return "none"
	}
	return id
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
	for _, line := range strings.Split(s, "\n") {
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
