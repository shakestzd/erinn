package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/ingest"
)

// feedEvent is the unified wire shape for /api/events/feed.
// source is "otel" for OTel-derived events, "hook" for agent_events fallback.
type feedEvent struct {
	ID           string  `json:"id"`
	Source       string  `json:"source"`
	Type         string  `json:"type"`
	Harness      string  `json:"harness,omitempty"`
	ToolName     string  `json:"tool_name,omitempty"`
	Model        string  `json:"model,omitempty"`
	Timestamp    string  `json:"timestamp"`
	DurationMs   int64   `json:"duration_ms,omitempty"`
	TokensIn     int64   `json:"tokens_in,omitempty"`
	TokensOut    int64   `json:"tokens_out,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	Success      *bool   `json:"success,omitempty"`
	Decision     string  `json:"decision,omitempty"`
	SessionID    string  `json:"session_id,omitempty"`
	FeatureID    string  `json:"feature_id,omitempty"`
	TraceID      string  `json:"trace_id,omitempty"`
	ParentSpan   string  `json:"parent_span,omitempty"`
	Summary      string  `json:"summary,omitempty"`
	FeatureTitle string  `json:"feature_title,omitempty"`
	// SessionFamilyID groups this event's session with continuation sessions.
	// Exposed so feed consumers (plan-c3bbb1ed, wipnote who) can group by family.
	SessionFamilyID string `json:"session_family_id,omitempty"`
	// tsMicros is used internally for sorting and is not serialised.
	tsMicros int64
}

// eventsFeedHandler returns a unified activity feed combining OTel signals
// (primary) and hook-only agent_events (fallback for event types with no OTel
// equivalent). Results are merged and sorted by timestamp DESC.
//
// GET /api/events/feed?limit=N  (default 50, max 200)
func eventsFeedHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		limit := 50
		if l := r.URL.Query().Get("limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}

		otelEvents, err := queryOtelFeedEvents(database, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		hookEvents, err := queryHookFeedEvents(database, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		messageEvents, err := queryMessageFeedEvents(database, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Deduplicate assistant_text entries within otelEvents that have the same
		// session_id and timestamps within a 30-second window. This prevents duplicate
		// assistant responses from appearing (e.g., one from ingest pipeline, one from Stop hook).
		otelEvents = deduplicateOtelAssistantText(otelEvents)

		// Deduplicate user_prompt log events for sessions that already have interaction
		// span coverage. Gemini emits both an interaction span (which shows the user's
		// query as its summary) and a separate user_prompt log for the same turn, causing
		// the prompt text to appear twice in the feed.
		otelEvents = deduplicateUserPromptLogs(otelEvents)

		// Deduplicate messageEvents against otelEvents to avoid showing
		// the same assistant response twice (once from otel_signals, once from messages).
		deduped := deduplicateMessageEvents(otelEvents, messageEvents)
		merged := merge(append(otelEvents, deduped...), hookEvents, limit)
		respondJSON(w, map[string]any{"events": merged})
	}
}

// queryOtelFeedEvents fetches relevant OTel spans and returns them as feedEvents.
// The query is split into two UNION ALL subqueries (one for spans, one for logs),
// each with its own ORDER BY + LIMIT to avoid scanning unbounded rows and holding
// a SHARED lock for an extended period. The limit is applied generously per half
// to leave headroom for Go-side dedup/merge/sort.
func queryOtelFeedEvents(database *sql.DB, limit int) ([]feedEvent, error) {
	// Use a generous per-half limit (3x requested + floor) to handle dedup/merge
	perHalfLimit := limit*3 + 100
	if perHalfLimit > 500 {
		perHalfLimit = 500 // cap to avoid excessive memory
	}

	rows, err := database.Query(`
		WITH span_signals AS (
			SELECT signal_id, harness, COALESCE(trace_id, '') AS trace_id, COALESCE(parent_span, '') AS parent_span, canonical, tool_name, model, ts_micros, duration_ms, COALESCE(tokens_in, 0) AS tokens_in, COALESCE(tokens_out, 0) AS tokens_out, COALESCE(cost_usd, 0.0) AS cost_usd, success, decision, session_id, COALESCE(feature_id, '') AS feature_id, attrs_json
			FROM otel_signals
			WHERE kind = 'span' AND canonical IN (
				'interaction', 'api_request', 'tool_result',
				'tool_execution', 'tool_blocked_on_user', 'subagent_invocation'
			)
			ORDER BY ts_micros DESC
			LIMIT ?
		),
		log_signals AS (
			SELECT signal_id, harness, COALESCE(trace_id, '') AS trace_id, COALESCE(parent_span, '') AS parent_span, canonical, tool_name, model, ts_micros, duration_ms, COALESCE(tokens_in, 0) AS tokens_in, COALESCE(tokens_out, 0) AS tokens_out, COALESCE(cost_usd, 0.0) AS cost_usd, success, decision, session_id, COALESCE(feature_id, '') AS feature_id, attrs_json
			FROM otel_signals
			WHERE kind = 'log' AND canonical IN (
				'user_prompt', 'assistant_text', 'api_request', 'tool_result', 'tool_decision'
			)
			ORDER BY ts_micros DESC
			LIMIT ?
		)
		SELECT signal_id, harness, trace_id, parent_span, canonical, tool_name, model, ts_micros, duration_ms, tokens_in, tokens_out, cost_usd, success, decision, session_id, feature_id, attrs_json
		FROM span_signals
		UNION ALL
		SELECT signal_id, harness, trace_id, parent_span, canonical, tool_name, model, ts_micros, duration_ms, tokens_in, tokens_out, cost_usd, success, decision, session_id, feature_id, attrs_json
		FROM log_signals
		ORDER BY ts_micros DESC`, perHalfLimit, perHalfLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Collect rows and the set of feature_id and session_id for batch lookups
	var out []feedEvent
	featureIDs := make(map[string]bool)
	sessionIDs := make(map[string]bool)

	for rows.Next() {
		var ev feedEvent
		var successVal any
		var attrsRaw string
		var tsMicros int64

		if err := rows.Scan(
			&ev.ID, &ev.Harness, &ev.TraceID, &ev.ParentSpan,
			&ev.Type, &ev.ToolName, &ev.Model,
			&tsMicros, &ev.DurationMs,
			&ev.TokensIn, &ev.TokensOut, &ev.CostUSD,
			&successVal, &ev.Decision,
			&ev.SessionID, &ev.FeatureID, &attrsRaw,
		); err != nil {
			continue
		}

		if b, ok := decodeFeedSuccess(successVal); ok {
			ev.Success = &b
		}

		ev.Source = "otel"
		ev.Timestamp, ev.tsMicros = feedTimestampFromOtel(tsMicros, attrsRaw)
		if (ev.Type == "interaction" || ev.Type == "user_prompt") && ingest.IsSystemMessage(extractPromptText(attrsRaw)) {
			continue
		}
		ev.Summary = otelSummary(ev.Type, ev.ToolName, ev.Model, ev.TokensIn, ev.TokensOut, attrsRaw)

		// Track IDs for batch lookup after closing rows
		if ev.FeatureID != "" {
			featureIDs[ev.FeatureID] = true
		}
		if ev.SessionID != "" {
			sessionIDs[ev.SessionID] = true
		}

		// Zero out empty optional fields to keep JSON tidy.
		if ev.Model == "" {
			ev.Model = ""
		}
		out = append(out, ev)
	}
	// Close rows to release the SHARED lock before doing batch lookups
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Convert sets to slices for batch queries
	var featureIDList []string
	for fid := range featureIDs {
		featureIDList = append(featureIDList, fid)
	}
	var sessionIDList []string
	for sid := range sessionIDs {
		sessionIDList = append(sessionIDList, sid)
	}

	// Batch fetch feature titles
	featureTitles := make(map[string]string)
	if len(featureIDList) > 0 {
		placeholders := make([]string, len(featureIDList))
		args := make([]any, len(featureIDList))
		for i, id := range featureIDList {
			placeholders[i] = "?"
			args[i] = id
		}
		ftRows, err := database.Query(
			"SELECT id, title FROM features WHERE id IN ("+strings.Join(placeholders, ",")+")",
			args...,
		)
		if err == nil {
			defer ftRows.Close()
			for ftRows.Next() {
				var fid, title string
				if err := ftRows.Scan(&fid, &title); err == nil {
					featureTitles[fid] = title
				}
			}
		}
	}

	// Batch fetch session_family_ids
	sessionFamilyIDs := make(map[string]string)
	if len(sessionIDList) > 0 {
		placeholders := make([]string, len(sessionIDList))
		args := make([]any, len(sessionIDList))
		for i, id := range sessionIDList {
			placeholders[i] = "?"
			args[i] = id
		}
		sessRows, err := database.Query(
			"SELECT session_id, session_family_id FROM sessions WHERE session_id IN ("+strings.Join(placeholders, ",")+")",
			args...,
		)
		if err == nil {
			defer sessRows.Close()
			for sessRows.Next() {
				var sid, sfid string
				if err := sessRows.Scan(&sid, &sfid); err == nil {
					sessionFamilyIDs[sid] = sfid
				}
			}
		}
	}

	// Populate feature_title and session_family_id from the maps
	for i := range out {
		out[i].FeatureTitle = featureTitles[out[i].FeatureID]
		out[i].SessionFamilyID = sessionFamilyIDs[out[i].SessionID]
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].tsMicros == out[j].tsMicros {
			return out[i].ID > out[j].ID
		}
		return out[i].tsMicros > out[j].tsMicros
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func queryMessageFeedEvents(database *sql.DB, limit int) ([]feedEvent, error) {
	rows, err := database.Query(`
		SELECT m.id,
			COALESCE(NULLIF(m.agent_id, ''), s.agent_assigned, ''),
			COALESCE(m.timestamp, ''),
			COALESCE(m.content, ''),
			COALESCE(m.model, ''),
			m.session_id
		FROM messages m
		LEFT JOIN sessions s ON s.session_id = m.session_id
		WHERE m.role = 'assistant' AND TRIM(m.content) != ''
		ORDER BY m.timestamp DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []feedEvent{}
	for rows.Next() {
		var id int64
		var harness, tsRaw, content, model, sessionID string
		if err := rows.Scan(&id, &harness, &tsRaw, &content, &model, &sessionID); err != nil {
			return nil, err
		}
		tsMicros := timestampStringToMicros(tsRaw)
		if tsMicros == 0 {
			continue
		}
		out = append(out, feedEvent{
			ID:        "message:" + strconv.FormatInt(id, 10),
			Source:    "message",
			Type:      "assistant_text",
			Harness:   harness,
			Model:     model,
			Timestamp: time.UnixMicro(tsMicros).UTC().Format(time.RFC3339),
			SessionID: sessionID,
			Summary:   truncateFeedText(content, 200),
			tsMicros:  tsMicros,
		})
	}
	return out, rows.Err()
}

func feedTimestampFromOtel(tsMicros int64, attrsRaw string) (string, int64) {
	if tsMicros > 0 {
		return time.UnixMicro(tsMicros).UTC().Format(time.RFC3339), tsMicros
	}

	var attrs map[string]any
	if err := json.Unmarshal([]byte(attrsRaw), &attrs); err == nil {
		if raw, ok := attrs["event.timestamp"]; ok {
			if parsed, ok := parseFeedEventTimestamp(raw); ok {
				return parsed.UTC().Format(time.RFC3339), parsed.UnixMicro()
			}
		}
	}

	return time.UnixMicro(tsMicros).UTC().Format(time.RFC3339), tsMicros
}

func parseFeedEventTimestamp(v any) (time.Time, bool) {
	switch x := v.(type) {
	case string:
		if t, err := time.Parse(time.RFC3339, x); err == nil {
			return t.UTC(), true
		}
		if t, err := time.Parse("2006-01-02T15:04:05.999999999Z07:00", x); err == nil {
			return t.UTC(), true
		}
	case float64:
		return time.UnixMicro(int64(x)), true
	case int64:
		return time.UnixMicro(x), true
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return time.UnixMicro(n), true
		}
	}
	return time.Time{}, false
}

func decodeFeedSuccess(v any) (bool, bool) {
	switch x := v.(type) {
	case nil:
		return false, false
	case int64:
		return x == 1, true
	case int:
		return x == 1, true
	case bool:
		return x, true
	case []byte:
		switch strings.ToLower(string(x)) {
		case "1", "true":
			return true, true
		case "0", "false":
			return false, true
		}
	case string:
		switch strings.ToLower(x) {
		case "1", "true":
			return true, true
		case "0", "false":
			return false, true
		}
	}
	return false, false
}

// queryHookFeedEvents fetches hook-only event types from agent_events.
func queryHookFeedEvents(database *sql.DB, limit int) ([]feedEvent, error) {
	rows, err := database.Query(`
		SELECT e.event_id, e.event_type, e.timestamp,
		       COALESCE(e.tool_name, '') AS tool_name,
		       COALESCE(e.input_summary, '') AS input_summary,
		       COALESCE(e.output_summary, '') AS output_summary,
		       COALESCE(e.session_id, '') AS session_id,
		       COALESCE(e.feature_id, '') AS feature_id,
		       COALESCE(e.parent_event_id, '') AS parent_event_id,
		       COALESCE((SELECT f.title FROM features f WHERE f.id = e.feature_id LIMIT 1), '') AS feature_title
		FROM agent_events e
		WHERE e.event_type IN ('start', 'end', 'check_point', 'error', 'tool_call')
		ORDER BY e.timestamp DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []feedEvent
	for rows.Next() {
		var ev feedEvent
		var ts, inputSum, outputSum, parentEvtID string

		if err := rows.Scan(
			&ev.ID, &ev.Type, &ts,
			&ev.ToolName, &inputSum, &outputSum,
			&ev.SessionID, &ev.FeatureID, &parentEvtID, &ev.FeatureTitle,
		); err != nil {
			continue
		}
		if ingest.IsSystemMessage(inputSum) {
			continue
		}

		ev.Source = "hook"
		ev.Timestamp = ts
		ev.Summary = hookSummary(outputSum, inputSum)

		// Parse timestamp for sorting; tolerate missing values.
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			ev.tsMicros = t.UnixMicro()
		} else if t, err := time.Parse("2006-01-02T15:04:05.999999999Z07:00", ts); err == nil {
			ev.tsMicros = t.UnixMicro()
		}

		out = append(out, ev)
	}
	return out, nil
}

// otelSummary produces a human-readable one-liner for an OTel span.
func otelSummary(canonical, toolName, model string, tokensIn, tokensOut int64, attrsRaw string) string {
	// Parse attrs once — only if we need content-specific details.
	var attrs map[string]any
	if attrsRaw != "" && attrsRaw != "{}" {
		_ = json.Unmarshal([]byte(attrsRaw), &attrs)
	}

	pull := func(k string) string {
		if attrs == nil {
			return ""
		}
		if v, ok := attrs[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}

	truncate := func(s string, n int) string {
		if len(s) <= n {
			return s
		}
		return s[:n] + "..."
	}

	switch canonical {
	case "tool_result", "tool_execution", "subagent_invocation":
		switch strings.ToLower(toolName) {
		case "bash":
			if cmd := pull("full_command"); cmd != "" {
				return truncate(cmd, 200)
			}
			if cmd := pull("bash_command"); cmd != "" {
				return truncate(cmd, 200)
			}
			if input := pull("input"); input != "" {
				return truncate(input, 200)
			}
			return "Bash"
		case "read", "edit", "write", "notebookedit":
			if fp := pull("file_path"); fp != "" {
				return fp
			}
			return toolName
		case "task", "agent":
			if desc := pull("description"); desc != "" {
				return truncate(desc, 200)
			}
			if prompt := pull("prompt"); prompt != "" {
				return truncate(prompt, 200)
			}
			return toolName
		default:
			if toolName != "" {
				return toolName
			}
			return canonical
		}
	case "api_request":
		if model != "" {
			if tokensIn > 0 || tokensOut > 0 {
				return model + " — " + formatTokens(tokensIn, tokensOut)
			}
			return model
		}
		return "API request"
	case "interaction":
		if turn := pull("turn"); turn != "" {
			return "User prompt (turn " + turn + ")"
		}
		return "User prompt"
	case "assistant_text":
		if text := pull("text"); text != "" {
			return truncateFeedText(text, 200)
		}
		return "Assistant response"
	case "tool_blocked_on_user":
		return "Permission request"
	default:
		return canonical
	}
}

// hookSummary picks the best human-readable text from hook event fields.
func hookSummary(outputSummary, inputSummary string) string {
	if outputSummary != "" {
		return outputSummary
	}
	return inputSummary
}

func truncateFeedText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// formatTokens returns a concise token count string like "1.2k in / 512 out".
func formatTokens(in, out int64) string {
	return formatNum(in) + " in / " + formatNum(out) + " out"
}

func formatNum(n int64) string {
	if n >= 1000 {
		f := float64(n) / 1000.0
		// One decimal place only when meaningful.
		s := strconv.FormatFloat(f, 'f', 1, 64)
		// Strip trailing ".0"
		s = strings.TrimSuffix(s, ".0")
		return s + "k"
	}
	return strconv.FormatInt(n, 10)
}

// merge combines two slices of feedEvent, sorts by tsMicros DESC, and
// returns at most limit items.
func merge(a, b []feedEvent, limit int) []feedEvent {
	combined := make([]feedEvent, 0, len(a)+len(b))
	combined = append(combined, a...)
	combined = append(combined, b...)
	sort.Slice(combined, func(i, j int) bool {
		return combined[i].tsMicros > combined[j].tsMicros
	})
	if len(combined) > limit {
		combined = combined[:limit]
	}
	return combined
}

func legacyEventFromFeed(ev feedEvent) map[string]any {
	toolName := ev.ToolName
	if toolName == "" && ev.Type == "interaction" {
		toolName = "UserQuery"
	}

	status := "recorded"
	if ev.Type == "tool_blocked_on_user" {
		status = "blocked"
	} else if ev.Success != nil {
		if *ev.Success {
			status = "completed"
		} else {
			status = "failed"
		}
	}

	return map[string]any{
		"event_id":        ev.ID,
		"agent_id":        legacyAgentID(ev),
		"event_type":      ev.Type,
		"timestamp":       ev.Timestamp,
		"tool_name":       toolName,
		"input_summary":   ev.Summary,
		"output_summary":  ev.Summary,
		"session_id":      ev.SessionID,
		"feature_id":      ev.FeatureID,
		"feature_title":   ev.FeatureTitle,
		"parent_event_id": ev.ParentSpan,
		"status":          status,
	}
}

func legacyAgentID(ev feedEvent) string {
	switch ev.Harness {
	case "claude_code":
		return "claude-code"
	case "gemini_cli":
		return "gemini"
	case "codex", "wipnote":
		return ev.Harness
	}
	switch ev.Source {
	case "otel":
		return "otel"
	case "hook":
		return "hook"
	default:
		return ""
	}
}

// deduplicateOtelAssistantText removes duplicate assistant_text entries within
// otelEvents where two entries have the same session_id and timestamps within a
// 30-second window. This prevents the same assistant response from appearing twice
// (e.g., once from the OTel ingest pipeline, once from the Stop hook's custom insert).
//
// The function keeps non-assistant_text events unchanged and returns all events
// re-sorted by tsMicros DESC.
func deduplicateOtelAssistantText(events []feedEvent) []feedEvent {
	const windowMicros int64 = 30_000_000 // 30 seconds in microseconds

	// Separate assistant_text from other event types.
	var assistantEvents []feedEvent
	var otherEvents []feedEvent

	for _, ev := range events {
		if ev.Type == "assistant_text" {
			assistantEvents = append(assistantEvents, ev)
		} else {
			otherEvents = append(otherEvents, ev)
		}
	}

	// Sort assistant_text events by session_id, then by tsMicros (ascending for bucketing).
	sort.Slice(assistantEvents, func(i, j int) bool {
		if assistantEvents[i].SessionID != assistantEvents[j].SessionID {
			return assistantEvents[i].SessionID < assistantEvents[j].SessionID
		}
		return assistantEvents[i].tsMicros < assistantEvents[j].tsMicros
	})

	// Deduplicate: keep only one entry per (session_id, 30-second bucket).
	// For each session, iterate through sorted timestamps and keep the first event
	// in each 30-second bucket.
	var deduped []feedEvent
	var lastSessionID string
	var lastBucketStart int64

	for _, ev := range assistantEvents {
		if ev.SessionID != lastSessionID {
			// New session; reset bucket tracking.
			lastSessionID = ev.SessionID
			lastBucketStart = ev.tsMicros
			deduped = append(deduped, ev)
		} else {
			// Same session; check if this event is in a new 30-second bucket.
			if ev.tsMicros-lastBucketStart >= windowMicros {
				// New bucket; keep this event and update the bucket start.
				lastBucketStart = ev.tsMicros
				deduped = append(deduped, ev)
			}
			// else: within the same bucket; skip this event (keep the first one).
		}
	}

	// Combine deduplicated assistant events with non-assistant events.
	combined := append(deduped, otherEvents...)

	// Re-sort by tsMicros DESC to match the expected feed order.
	sort.Slice(combined, func(i, j int) bool {
		if combined[i].tsMicros == combined[j].tsMicros {
			return combined[i].ID > combined[j].ID
		}
		return combined[i].tsMicros > combined[j].tsMicros
	})

	return combined
}

// deduplicateMessageEvents filters out message table entries that have
// corresponding otel_signals assistant_text coverage. If a session has ANY
// otel assistant_text event, suppress ALL message-path assistant_text entries
// for that session regardless of timestamp divergence. This mirrors the
// hook-coverage gate pattern and handles Gemini's large timestamp skew.
func deduplicateMessageEvents(otelEvents, messageEvents []feedEvent) []feedEvent {
	// Build set of session IDs that have otel assistant_text coverage.
	coveredSessions := make(map[string]bool)
	for _, ev := range otelEvents {
		if ev.Type == "assistant_text" && ev.SessionID != "" {
			coveredSessions[ev.SessionID] = true
		}
	}

	var deduped []feedEvent
	for _, msg := range messageEvents {
		// Suppress assistant_text for covered sessions; keep everything else.
		if msg.Type == "assistant_text" && coveredSessions[msg.SessionID] {
			continue
		}
		deduped = append(deduped, msg)
	}

	return deduped
}

// deduplicateUserPromptLogs suppresses user_prompt OTel log events for
// Gemini-compatible sessions that have other OTel coverage (api_request,
// assistant_text, tool_result, etc.). Gemini emits both:
//  1. hook check_point events that render the user's prompt with tool children
//  2. a gemini_cli.user_prompt OTel log with the same prompt text and 0 tools
//
// If any non-user_prompt Gemini-compatible OTel signal exists for a session, the hook
// path already covers that turn and the user_prompt log is redundant.
// Excluding user_prompt from the coverage map prevents a prompt-only session
// from suppressing its own only record.
//
// Suppression is gated to Gemini-compatible harnesses only — Codex and other
// harnesses keep their user_prompt logs intact.
func deduplicateUserPromptLogs(events []feedEvent) []feedEvent {
	// Collect sessions that have any Gemini-compatible OTel signal OTHER than user_prompt.
	geminiLikeOtelSessions := make(map[string]bool)
	for _, ev := range events {
		if ev.Source == "otel" && isGeminiLikeFeedHarness(ev.Harness) && ev.Type != "user_prompt" {
			geminiLikeOtelSessions[ev.SessionID] = true
		}
	}
	if len(geminiLikeOtelSessions) == 0 {
		return events
	}
	out := make([]feedEvent, 0, len(events))
	for _, ev := range events {
		if ev.Type == "user_prompt" && isGeminiLikeFeedHarness(ev.Harness) && geminiLikeOtelSessions[ev.SessionID] {
			continue // hook check_point already shows this turn's prompt
		}
		out = append(out, ev)
	}
	return out
}

func isGeminiLikeFeedHarness(harness string) bool {
	return harness == "gemini_cli" || harness == "antigravity_cli"
}
