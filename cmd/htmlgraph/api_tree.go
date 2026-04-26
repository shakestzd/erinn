package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// turnStats holds per-turn aggregate counts.
type turnStats struct {
	ToolCount  int      `json:"tool_count"`
	ErrorCount int      `json:"error_count"`
	Models     []string `json:"models"`
}

// turn groups a UserQuery with its child events and stats.
type turn struct {
	SessionID string           `json:"session_id"`
	UserQuery map[string]any   `json:"user_query"`
	Children  []map[string]any `json:"children"`
	Stats     turnStats        `json:"stats"`
}

// eventColumns is the shared SELECT column list for agent_events (aliased as e).
const eventColumns = `e.event_id, e.agent_id, e.event_type, e.timestamp, e.tool_name,
	COALESCE(e.input_summary, ''), COALESCE(e.output_summary, ''),
	e.session_id, COALESCE(e.feature_id, ''), e.status,
	COALESCE(e.parent_event_id, ''), COALESCE(e.subagent_type, ''),
	COALESCE(e.model, ''), COALESCE(e.step_id, ''),
	COALESCE((SELECT f.title FROM features f WHERE f.id = e.feature_id LIMIT 1), '')`

// treeHandler returns hierarchical event data grouped by UserQuery turns.
// GET /api/events/tree?limit=50
func treeHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if l := r.URL.Query().Get("limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
				limit = n
			}
		}

		turns := buildEventTree(database, limit)
		respondJSON(w, turns)
	}
}

// buildEventTree merges OTel interaction-span turns with hook-based UserQuery
// turns that have no OTel anchor, then returns the most recent `limit` by
// timestamp. Mixed databases (sessions partially anchored in OTel, older
// sessions purely hook-driven) are rendered together rather than the
// older sessions being silently dropped once any OTel span is present.
func buildEventTree(database *sql.DB, limit int) []turn {
	otelTurns := buildEventTreeOtel(database, limit)
	hookTurns := buildEventTreeHookUnanchored(database, limit)

	merged := append(otelTurns, hookTurns...)
	if len(merged) == 0 {
		return []turn{}
	}

	// Sort DESC by timestamp (RFC3339 strings are lexicographically sortable
	// in ascending chronological order; reverse for newest-first).
	sort.SliceStable(merged, func(i, j int) bool {
		ti, _ := merged[i].UserQuery["timestamp"].(string)
		tj, _ := merged[j].UserQuery["timestamp"].(string)
		return ti > tj
	})

	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

// buildEventTreeOtel builds the turn list using OTel interaction spans as
// anchors. Each interaction span corresponds to one user prompt turn; its
// trace_id links all child spans. The user_query shape is synthesized to
// match the fields the frontend event-tree.js expects so no frontend
// changes are required.
func buildEventTreeOtel(database *sql.DB, limit int) []turn {
	rows, err := database.Query(`
		SELECT s.signal_id, s.trace_id, COALESCE(s.span_id, ''),
		       s.session_id,
		       s.ts_micros, COALESCE(s.duration_ms, 0),
		       COALESCE(s.attrs_json, '{}'),
		       COALESCE(s.feature_id, '')
		FROM otel_signals s
		WHERE s.kind = 'span' AND s.canonical = 'interaction'
		ORDER BY s.ts_micros DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var turns []turn
	for rows.Next() {
		var signalID, traceID, spanID, sessionID string
		var tsMicros, durationMs int64
		var attrsRaw, featureID string

		if err := rows.Scan(&signalID, &traceID, &spanID, &sessionID,
			&tsMicros, &durationMs, &attrsRaw, &featureID); err != nil {
			continue
		}

		// Convert ts_micros to RFC3339 for the frontend.
		ts := time.UnixMicro(tsMicros).UTC().Format(time.RFC3339)

		// Extract prompt text from attrs_json (attrs.user_prompt, attrs.prompt).
		promptText := extractPromptText(attrsRaw)

		// If attrs_json didn't carry the prompt, look for a user_prompt log
		// record in the same trace.
		if promptText == "" && traceID != "" {
			promptText = fetchPromptFromTrace(database, traceID)
		}

		// Look up feature title when we have a feature_id.
		featureTitle := ""
		if featureID != "" {
			_ = database.QueryRow(
				`SELECT COALESCE(title, '') FROM features WHERE id = ? LIMIT 1`,
				featureID,
			).Scan(&featureTitle)
		}

		// Build the user_query map with the same shape the frontend expects.
		userQuery := map[string]any{
			"event_id":      signalID,
			"timestamp":     ts,
			"tool_name":     "UserQuery",
			"input_summary": promptText,
			"session_id":    sessionID,
			"feature_id":    featureID,
			"feature_title": featureTitle,
			// Fields not available from OTel interaction spans; set to
			// zero-values so the frontend can still render gracefully.
			"agent_id":        "",
			"event_type":      "tool_call",
			"output_summary":  "",
			"status":          "recorded",
			"parent_event_id": "",
			"subagent_type":   "",
			"tool_use_id":     "",
			"model":           "",
		}

		// Fetch hook-based children: the frontend already renders OTel spans
		// independently via /api/otel/spans; hook children provide the
		// fallback tree for sessions that have both data sources.
		children := fetchChildrenForOtelTurn(database, traceID, sessionID)
		stats := computeOtelStats(database, traceID)

		turns = append(turns, turn{
			SessionID: sessionID,
			UserQuery: userQuery,
			Children:  children,
			Stats:     stats,
		})
	}

	return turns
}

// buildEventTreeHookUnanchored returns hook-based UserQuery turns whose
// step_id does NOT match an OTel interaction span. New sessions that emit
// both an OTel anchor AND a hook UserQuery are surfaced through
// buildEventTreeOtel; this function captures the older / hook-only
// sessions so they are not dropped when mixed with OTel data.
func buildEventTreeHookUnanchored(database *sql.DB, limit int) []turn {
	rows, err := database.Query(`
		SELECT `+eventColumns+`
		FROM agent_events e
		WHERE e.tool_name = 'UserQuery'
		  AND (
		    e.step_id IS NULL OR e.step_id = ''
		    OR NOT EXISTS (
		      SELECT 1 FROM otel_signals s
		      WHERE s.kind = 'span' AND s.canonical = 'interaction'
		        AND s.trace_id = e.step_id
		    )
		  )
		ORDER BY e.timestamp DESC
		LIMIT ?`, limit)
	if err != nil {
		return []turn{}
	}
	defer rows.Close()

	var turns []turn
	for rows.Next() {
		evt := scanEvent(rows)
		if evt == nil {
			continue
		}

		sessionID, _ := evt["session_id"].(string)
		eventID, _ := evt["event_id"].(string)

		children := fetchChildren(database, eventID, sessionID, 1)
		stats := computeStats(children)

		turns = append(turns, turn{
			SessionID: sessionID,
			UserQuery: evt,
			Children:  children,
			Stats:     stats,
		})
	}

	if turns == nil {
		return []turn{}
	}
	return turns
}

// extractPromptText pulls the user prompt string from an interaction span's
// attrs_json. Tries attrs.user_prompt then attrs.prompt.
func extractPromptText(attrsRaw string) string {
	if attrsRaw == "" || attrsRaw == "{}" {
		return ""
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(attrsRaw), &attrs); err != nil {
		return ""
	}
	for _, key := range []string{"user_prompt", "prompt", "text"} {
		if v, ok := attrs[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// fetchPromptFromTrace looks up the user_prompt log record that shares the
// given trace_id and extracts the prompt text from its attrs_json.
func fetchPromptFromTrace(database *sql.DB, traceID string) string {
	var attrsRaw string
	err := database.QueryRow(`
		SELECT COALESCE(attrs_json, '{}')
		FROM otel_signals
		WHERE trace_id = ? AND canonical = 'user_prompt' AND kind = 'log'
		LIMIT 1`, traceID).Scan(&attrsRaw)
	if err != nil {
		return ""
	}
	return extractPromptText(attrsRaw)
}

// fetchChildrenForOtelTurn returns the hook-based children for a turn
// identified by its OTel trace_id. It looks for the matching UserQuery
// hook event in the same session whose step_id matches the trace, or
// falls back to finding the nearest UserQuery by session.
//
// When no matching hook event exists (pure OTel session), returns an
// empty slice — the frontend renders OTel spans via /api/otel/spans.
func fetchChildrenForOtelTurn(database *sql.DB, traceID, sessionID string) []map[string]any {
	// Try to find the UserQuery hook event for this trace.
	var hookEventID string
	_ = database.QueryRow(`
		SELECT event_id FROM agent_events
		WHERE session_id = ? AND tool_name = 'UserQuery'
		  AND (step_id = ? OR step_id IS NULL OR step_id = '')
		LIMIT 1`, sessionID, traceID).Scan(&hookEventID)

	if hookEventID == "" {
		return []map[string]any{}
	}
	return fetchChildren(database, hookEventID, sessionID, 1)
}

// computeOtelStats aggregates per-turn stats from OTel tool spans in the
// same trace. Falls back gracefully to zero values when no spans exist.
func computeOtelStats(database *sql.DB, traceID string) turnStats {
	var toolCount, errorCount int
	_ = database.QueryRow(`
		SELECT
			COUNT(*) AS tool_count,
			SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END) AS error_count
		FROM otel_signals
		WHERE trace_id = ? AND kind = 'span'
		  AND canonical IN ('tool_result', 'tool_execution', 'subagent_invocation')`,
		traceID).Scan(&toolCount, &errorCount)

	return turnStats{
		ToolCount:  toolCount,
		ErrorCount: errorCount,
		Models:     []string{},
	}
}

// fetchChildren recursively fetches child events up to maxDepth=4 (depth 0-3).
func fetchChildren(database *sql.DB, parentID, sessionID string, depth int) []map[string]any {
	if depth > 3 {
		return nil
	}

	rows, err := database.Query(`
		SELECT `+eventColumns+`
		FROM agent_events e
		WHERE e.parent_event_id = ?
		ORDER BY e.timestamp DESC`, parentID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var children []map[string]any
	for rows.Next() {
		evt := scanEvent(rows)
		if evt == nil {
			continue
		}

		eventID, _ := evt["event_id"].(string)

		// Recurse for direct children.
		evt["children"] = fetchChildren(database, eventID, sessionID, depth+1)

		children = append(children, evt)
	}

	// Suppress duplicate tool_call/Agent rows when a sibling task_delegation exists.
	hasDelegation := false
	for _, c := range children {
		if et, _ := c["event_type"].(string); et == "task_delegation" {
			hasDelegation = true
			break
		}
	}
	if hasDelegation {
		filtered := children[:0]
		for _, c := range children {
			et, _ := c["event_type"].(string)
			tn, _ := c["tool_name"].(string)
			if et == "tool_call" && tn == "Agent" {
				continue // suppress — task_delegation is the canonical row
			}
			filtered = append(filtered, c)
		}
		children = filtered
	}

	return children
}

// scanEvent reads one row from the standard eventColumns projection.
func scanEvent(rows *sql.Rows) map[string]any {
	var eventID, agentID, eventType, ts, toolName string
	var inputSum, outputSum, sessionID, featureID, status string
	var parentEvtID, subagentType, model, stepID, featureTitle string

	if err := rows.Scan(
		&eventID, &agentID, &eventType, &ts, &toolName,
		&inputSum, &outputSum, &sessionID, &featureID, &status,
		&parentEvtID, &subagentType, &model, &stepID, &featureTitle,
	); err != nil {
		return nil
	}

	return map[string]any{
		"event_id":      eventID,
		"agent_id":      agentID,
		"event_type":    eventType,
		"timestamp":     ts,
		"tool_name":     toolName,
		"input_summary": inputSum,
		"output_summary": outputSum,
		"session_id":    sessionID,
		"feature_id":    featureID,
		"feature_title": featureTitle,
		"status":        status,
		"parent_event_id": parentEvtID,
		"subagent_type": subagentType,
		"tool_use_id":   stepID,
		"model":         model,
	}
}

// computeStats aggregates tool_count, error_count, and distinct models
// from a flat walk of the children tree.
func computeStats(children []map[string]any) turnStats {
	var stats turnStats
	modelSet := make(map[string]bool)
	walkChildren(children, &stats, modelSet)
	for m := range modelSet {
		if m != "" {
			stats.Models = append(stats.Models, m)
		}
	}
	if stats.Models == nil {
		stats.Models = []string{}
	}
	return stats
}

func walkChildren(children []map[string]any, stats *turnStats, models map[string]bool) {
	for _, evt := range children {
		stats.ToolCount++
		evtType, _ := evt["event_type"].(string)
		status, _ := evt["status"].(string)
		if evtType == "error" || status == "failed" {
			stats.ErrorCount++
		}
		if m, ok := evt["model"].(string); ok && m != "" {
			models[m] = true
		}
		if sub, ok := evt["children"].([]map[string]any); ok {
			walkChildren(sub, stats, models)
		}
	}
}

