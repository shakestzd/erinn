package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// transcriptHandler returns messages and tool calls for a session.
// Requires ?session=SESSION_ID. Supports ?limit=N (default 500).
func transcriptHandler(database *sql.DB, wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.URL.Query().Get("session")
		if sessionID == "" {
			http.Error(w, "session parameter required", http.StatusBadRequest)
			return
		}

		limit := 500
		if l := r.URL.Query().Get("limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 2000 {
				limit = n
			}
		}

		messages, err := dbpkg.ListMessages(database, sessionID, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		toolCalls, err := dbpkg.ListToolCalls(database, sessionID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Group tool calls by message ID for easy frontend consumption.
		toolsByMsg := map[int][]map[string]any{}
		for _, tc := range toolCalls {
			entry := map[string]any{
				"tool_name":           tc.ToolName,
				"category":            tc.Category,
				"tool_use_id":         tc.ToolUseID,
				"input_json":          tc.InputJSON,
				"subagent_session_id": tc.SubagentSessionID,
			}
			// For Agent tool calls, find the subagent's agent_id by matching the
			// Task event nearest in time to this message. The agent_id lets the
			// frontend query /api/events/subagent?agent_id=... reliably.
			if tc.ToolName == "Agent" {
				entry["subagent_agent_id"] = lookupSubagentAgentID(database, tc.MessageID)
			}
			toolsByMsg[tc.MessageID] = append(toolsByMsg[tc.MessageID], entry)
		}

		result := make([]map[string]any, 0, len(messages))
		for _, m := range messages {
			entry := map[string]any{
				"id":                m.ID,
				"ordinal":           m.Ordinal,
				"role":              m.Role,
				"content":           m.Content,
				"timestamp":         m.Timestamp.Format(time.RFC3339),
				"has_thinking":      m.HasThinking,
				"has_tool_use":      m.HasToolUse,
				"content_length":    m.ContentLength,
				"model":             m.Model,
				"input_tokens":      m.InputTokens,
				"output_tokens":     m.OutputTokens,
				"cache_read_tokens": m.CacheReadTokens,
				"stop_reason":       m.StopReason,
			}
			if tools, ok := toolsByMsg[m.ID]; ok {
				entry["tool_calls"] = tools
			}
			result = append(result, entry)
		}

		// Look up linked plan for this session (from plan chat).
		planID, planTitle := lookupSessionPlan(database, wipnoteDir, sessionID)

		// Collect distinct work item IDs attributed to this session so the
		// transcript stats row can render them as clickable badges next to
		// the Plan badge. Only includes items that actually appeared in
		// agent_events — empty strings filtered, order stable by id.
		featureIDs := lookupSessionFeatureIDs(database, sessionID)

		resp := map[string]any{
			"session_id":    sessionID,
			"message_count": len(messages),
			"tool_count":    len(toolCalls),
			"messages":      result,
			"feature_ids":   featureIDs,
		}
		if planID != "" {
			resp["plan_id"] = planID
			resp["plan_title"] = planTitle
		}
		respondJSON(w, resp)
	}
}

// lookupSessionFeatureIDs returns distinct feature_id values recorded for
// the given session in agent_events. Used by the transcript view to render
// session-level work item badges. Returns an empty slice (not nil) so the
// JSON response always serialises as an array for consistent frontend
// handling.
func lookupSessionFeatureIDs(database *sql.DB, sessionID string) []string {
	ids := []string{}
	rows, err := database.Query(`
		SELECT DISTINCT feature_id FROM agent_events
		WHERE session_id = ? AND COALESCE(feature_id, '') != ''
		ORDER BY feature_id`, sessionID)
	if err != nil {
		return ids
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil && id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// lookupSessionPlan queries plan_feedback for a session→plan link and returns
// (planID, planTitle). Returns ("", "") when no link exists.
func lookupSessionPlan(database *sql.DB, wipnoteDir, sessionID string) (string, string) {
	var planID string
	database.QueryRow(
		`SELECT plan_id FROM plan_feedback WHERE action = 'session_id' AND value = ? LIMIT 1`,
		sessionID,
	).Scan(&planID) //nolint:errcheck
	if planID == "" {
		return "", ""
	}
	// Resolve title from the plan HTML file.
	planTitle := resolvePlanTitle(wipnoteDir, planID)
	return planID, planTitle
}

// resolvePlanTitle reads the plan HTML file and extracts the h1 title.
// Returns planID as fallback when the file is missing or unparseable.
func resolvePlanTitle(wipnoteDir, planID string) string {
	if wipnoteDir == "" {
		return planID
	}
	planPath := filepath.Join(wipnoteDir, "plans", planID+".html")
	f, err := os.Open(planPath)
	if err != nil {
		return planID
	}
	defer f.Close()

	// Minimal scan: look for first <h1>…</h1> without pulling in goquery.
	data, err := io.ReadAll(f)
	if err != nil {
		return planID
	}
	content := string(data)
	start := strings.Index(content, "<h1>")
	end := strings.Index(content, "</h1>")
	if start < 0 || end <= start+4 {
		return planID
	}
	title := strings.TrimSpace(content[start+4 : end])
	if title == "" {
		return planID
	}
	return title
}

// subagentEventsHandler returns agent_events for a subagent.
// Accepts ?agent_id=XXX (preferred) or ?parent_event_id=XXX (legacy fallback).
// GET /api/events/subagent?agent_id=XXX
// GET /api/events/subagent?parent_event_id=XXX
func subagentEventsHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Preferred: direct lookup by agent_id — no join required.
		if agentID := r.URL.Query().Get("agent_id"); agentID != "" {
			events := queryEventsByAgentID(database, agentID)
			if events == nil {
				events = []map[string]any{}
			}
			respondJSON(w, events)
			return
		}

		parentID := r.URL.Query().Get("parent_event_id")
		if parentID == "" {
			http.Error(w, "agent_id or parent_event_id required", http.StatusBadRequest)
			return
		}

		// Legacy path: try direct children first (handles cases where
		// parent_event_id is the Task event's event_id directly).
		events := querySubagentEvents(database, parentID)
		if len(events) == 0 {
			// The caller passed an Agent event ID. Find the Task child, then
			// return its children (the actual subagent tool events).
			var taskEventID string
			database.QueryRow(`
				SELECT event_id FROM agent_events
				WHERE parent_event_id = ? AND tool_name = 'Task'
				LIMIT 1`, parentID).Scan(&taskEventID) //nolint:errcheck
			if taskEventID != "" {
				events = querySubagentEvents(database, taskEventID)
			}
		}

		if events == nil {
			events = []map[string]any{}
		}
		respondJSON(w, events)
	}
}

// queryEventsByAgentID returns all agent_events that belong to the given
// subagent (identified by its agent_id), excluding top-level UserQuery rows,
// ordered chronologically.
func queryEventsByAgentID(database *sql.DB, agentID string) []map[string]any {
	rows, err := database.Query(`
		SELECT event_id, agent_id, event_type, timestamp, COALESCE(tool_name, ''),
		       COALESCE(input_summary, ''), COALESCE(output_summary, ''),
		       session_id, COALESCE(status, ''), COALESCE(subagent_type, '')
		FROM agent_events
		WHERE agent_id = ? AND COALESCE(tool_name, '') != 'UserQuery'
		ORDER BY timestamp ASC
		LIMIT 100`, agentID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var events []map[string]any
	for rows.Next() {
		var eid, aid, etype, ts, tool, inputSum, outputSum, sid, status, subType string
		if err := rows.Scan(&eid, &aid, &etype, &ts, &tool,
			&inputSum, &outputSum, &sid, &status, &subType); err != nil {
			continue
		}
		events = append(events, map[string]any{
			"event_id":       eid,
			"agent_id":       aid,
			"event_type":     etype,
			"timestamp":      ts,
			"tool_name":      tool,
			"input_summary":  inputSum,
			"output_summary": outputSum,
			"session_id":     sid,
			"status":         status,
			"subagent_type":  subType,
		})
	}
	return events
}

// lookupSubagentAgentID finds the agent_id for the subagent launched by the
// Agent tool call in the given message. It matches the Task event in
// agent_events whose timestamp is closest to the message timestamp (within
// a 120 s window) and extracts the id from the input_summary field which
// contains "Subagent started: type=<t> id=<agent_id>".
func lookupSubagentAgentID(database *sql.DB, messageID int) string {
	var msgTs string
	if err := database.QueryRow(
		`SELECT timestamp FROM messages WHERE id = ?`, messageID,
	).Scan(&msgTs); err != nil || msgTs == "" {
		return ""
	}

	// Normalize T-separator: SQLite datetime() outputs 'YYYY-MM-DD HH:MM:SS'
	// but stored timestamps use ISO 8601 'YYYY-MM-DDTHH:MM:SSZ'. REPLACE
	// ensures both sides use the same format for BETWEEN comparison.
	normTs := strings.Replace(strings.TrimSuffix(msgTs, "Z"), "T", " ", 1)

	var inputSummary string
	database.QueryRow(`
		SELECT input_summary FROM agent_events
		WHERE tool_name = 'Task'
		  AND input_summary LIKE 'Subagent started:%'
		  AND REPLACE(REPLACE(timestamp,'T',' '),'Z','') BETWEEN datetime(?, '-120 seconds') AND datetime(?, '+120 seconds')
		ORDER BY ABS(julianday(REPLACE(REPLACE(timestamp,'T',' '),'Z','')) - julianday(?))
		LIMIT 1`, normTs, normTs, normTs).Scan(&inputSummary) //nolint:errcheck

	// Extract agent_id from "Subagent started: type=xxx id=YYY"
	if idx := strings.Index(inputSummary, " id="); idx >= 0 {
		return inputSummary[idx+4:]
	}
	return ""
}

// querySubagentEvents returns all agent_events that are direct children of
// parentID, ordered by timestamp ascending.
func querySubagentEvents(database *sql.DB, parentID string) []map[string]any {
	rows, err := database.Query(`
		SELECT event_id, agent_id, event_type, timestamp, COALESCE(tool_name, ''),
		       COALESCE(input_summary, ''), COALESCE(output_summary, ''),
		       session_id, COALESCE(status, ''), COALESCE(subagent_type, '')
		FROM agent_events
		WHERE parent_event_id = ?
		ORDER BY timestamp ASC`, parentID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var events []map[string]any
	for rows.Next() {
		var eid, aid, etype, ts, tool, inputSum, outputSum, sid, status, subType string
		if err := rows.Scan(&eid, &aid, &etype, &ts, &tool,
			&inputSum, &outputSum, &sid, &status, &subType); err != nil {
			continue
		}
		events = append(events, map[string]any{
			"event_id":       eid,
			"agent_id":       aid,
			"event_type":     etype,
			"timestamp":      ts,
			"tool_name":      tool,
			"input_summary":  inputSum,
			"output_summary": outputSum,
			"session_id":     sid,
			"status":         status,
			"subagent_type":  subType,
		})
	}
	return events
}

// sseHandler streams new agent_events rows as Server-Sent Events.
// Polls SQLite every 2 s for rows with a rowid greater than last seen.
func sseHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		// Track the highest rowid seen so far.
		var lastRowID int64
		database.QueryRow(
			`SELECT COALESCE(MAX(rowid), 0) FROM agent_events`).Scan(&lastRowID)

		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				// No wal_checkpoint here: the dashboard mux handle is
				// read-only (bug-74a7bda7) so it cannot drive a checkpoint,
				// and the checkpoint is a no-op under DELETE journal mode
				// anyway. The writable handles that commit own
				// checkpointing; in WAL mode a read connection still sees
				// committed writes without an explicit checkpoint.
				rows, err := database.Query(`
					SELECT rowid, event_id, agent_id, event_type, timestamp,
					       tool_name, COALESCE(input_summary, ''),
					       COALESCE(output_summary, ''), session_id,
					       COALESCE(feature_id, ''), status
					FROM agent_events
					WHERE rowid > ?
					ORDER BY rowid ASC
					LIMIT 20`, lastRowID)
				if err != nil {
					continue
				}

				for rows.Next() {
					var rowid int64
					var eid, aid, etype, ts, tool, inputSum, outputSum, sid, fid, status string
					if err := rows.Scan(&rowid, &eid, &aid, &etype, &ts,
						&tool, &inputSum, &outputSum, &sid, &fid, &status); err != nil {
						continue
					}
					payload, _ := json.Marshal(map[string]string{
						"event_id":       eid,
						"agent_id":       aid,
						"event_type":     etype,
						"timestamp":      ts,
						"tool_name":      tool,
						"input_summary":  inputSum,
						"output_summary": outputSum,
						"session_id":     sid,
						"feature_id":     fid,
						"status":         status,
					})
					fmt.Fprintf(w, "data: %s\n\n", payload)
					lastRowID = rowid
				}
				rows.Close()
				flusher.Flush()
			}
		}
	}
}
