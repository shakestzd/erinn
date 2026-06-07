package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/internal/ingest"
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

type otelPromptAnchor struct {
	SignalID    string
	Harness     string
	SessionID   string
	TraceID     string
	PromptID    string
	FeatureID   string
	TSMicros    int64
	RawTSMicros int64
	AttrsRaw    string
	PromptText  string
	FeatureName string
}

// hookOtelDedupWindowMicros is the timestamp window used to consider a
// hook UserQuery row "anchored" by an OTel interaction span when the hook
// row has no step_id to match exactly. Five seconds is generous compared
// to the actual sub-second offset between matching events.
const hookOtelDedupWindowMicros int64 = 5_000_000

// eventColumns is the shared SELECT column list for agent_events (aliased as e).
const eventColumns = `e.event_id,
	COALESCE(NULLIF(e.agent_id, ''), (SELECT s.agent_assigned FROM sessions s WHERE s.session_id = e.session_id LIMIT 1), ''),
	e.event_type, e.timestamp, e.tool_name,
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

		turns, err := buildEventTree(database, limit)
		if err != nil {
			http.Error(w, fmt.Sprintf("query event tree: %v", err), http.StatusInternalServerError)
			return
		}
		respondJSON(w, turns)
	}
}

// buildEventTree merges OTel interaction-span turns with hook-based UserQuery
// turns that have no OTel anchor, then returns the most recent `limit` by
// timestamp. Mixed databases (sessions partially anchored in OTel, older
// sessions purely hook-driven) are rendered together rather than the
// older sessions being silently dropped once any OTel span is present.
func buildEventTree(database *sql.DB, limit int) ([]turn, error) {
	otelTurns, err := buildEventTreeOtel(database, limit)
	if err != nil {
		return nil, err
	}
	otelLogTurns, err := buildEventTreeOtelLogFallback(database, limit)
	if err != nil {
		return nil, err
	}
	hookTurns, err := buildEventTreeHookUnanchored(database, limit)
	if err != nil {
		return nil, err
	}

	merged := append(otelTurns, otelLogTurns...)
	merged = append(merged, hookTurns...)
	if len(merged) == 0 {
		return []turn{}, nil
	}

	// Source-agnostic post-merge dedup: collapse rows that represent the same
	// user turn but arrived from different data sources (e.g. gemini_cli span
	// vs gemini log in the same session). The existing SQL NOT EXISTS anti-joins
	// correlate by session_id, which is unreliable across sources. This pass
	// is a safety-net layer that operates purely on the merged Go slice.
	merged = collapseDuplicateTurns(merged)

	// Post-dedup: nest task-notification wake-turns under the row that
	// contains the originating background tool call, matched by tool-use-id.
	// This must run AFTER collapseDuplicateTurns so only canonical rows remain.
	merged = nestTaskNotificationTurns(merged)

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
	return merged, nil
}

// collapseBucketSecs is the coarse time bucket for same-turn dedup.
// 10 seconds is wide enough to absorb the typical ~1 s emission gap between a
// hook event and its twin OTel span (or between gemini/gemini_cli log vs span),
// yet tight enough that two genuinely distinct user prompts seconds apart
// (rapid-fire Q&A) are not collapsed. Values below 5 s are too narrow (OTel
// pipeline latency) and above 60 s risk false-collapsing distinct prompts that
// share a short repeated text like "continue" or "/resume".
const collapseBucketSecs = 10

// normPrompt returns a dedup key for a raw prompt string: lowercase, collapsed
// whitespace, first 200 characters. Returns "" when the input is trivially
// empty (empty/whitespace-only), signalling that the row should not be
// collapsed.
func normPrompt(raw string) string {
	// collapse internal whitespace and trim
	out := make([]byte, 0, len(raw))
	prevSpace := true
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if !prevSpace {
				out = append(out, ' ')
			}
			prevSpace = true
		} else {
			// lowercase ASCII letters
			if c >= 'A' && c <= 'Z' {
				c += 32
			}
			out = append(out, c)
			prevSpace = false
		}
	}
	// trim trailing space
	if len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	s := string(out)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// turnRichness returns a sortable richness score for a turn: higher is richer.
// Richness order (descending priority):
//  1. has harness icon (agent_id is a known OTel harness source, not empty)
//  2. higher tool count
//  3. higher token count (not available in the turn struct itself; zero for now)
//  4. has non-empty feature_id
//
// We prefer OTel interaction turns over hook-only or log-only turns, which
// matches what the user expects to see (full cost/token data).
func turnRichness(t turn) int {
	score := 0
	agentID, _ := t.UserQuery["agent_id"].(string)
	if agentID != "" {
		score += 1_000_000
	}
	score += t.Stats.ToolCount * 10
	featureID, _ := t.UserQuery["feature_id"].(string)
	if featureID != "" {
		score += 1
	}
	return score
}

// collapseDuplicateTurns removes duplicate turns that represent the same user
// prompt submitted in the same coarse time window. This is a source-agnostic
// safety net on top of the SQL NOT EXISTS anti-joins: those correlate by
// session_id which is unreliable across OTel/hook data sources, so duplicates
// can slip through when a Gemini (or other) session emits the same prompt via
// two different signal paths (e.g. gemini_cli interaction span + gemini log).
//
// Dedup key: (normalized prompt text, floor(unix_seconds / collapseBucketSecs)).
// Within each group the richest row survives; the rest are dropped.
// Groups of size 1 are untouched — unanchored hook-only rows with no OTel twin
// are intentionally preserved.
func collapseDuplicateTurns(turns []turn) []turn {
	type bucketKey struct {
		norm   string
		bucket int64
	}
	type group struct {
		best  int // index into turns slice
		score int
	}

	groups := make(map[bucketKey]*group, len(turns))
	order := make([]bucketKey, 0, len(turns))

	for i, t := range turns {
		raw, _ := t.UserQuery["input_summary"].(string)
		norm := normPrompt(raw)
		if norm == "" {
			// Empty/whitespace prompt — do not collapse; keep as-is.
			// Use a unique sentinel key so it lands in its own singleton group.
			sentinel := bucketKey{norm: "\x00" + string(rune(i)), bucket: 0}
			groups[sentinel] = &group{best: i, score: 0}
			order = append(order, sentinel)
			continue
		}

		ts, _ := t.UserQuery["timestamp"].(string)
		var bucket int64
		if ts != "" {
			if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
				bucket = parsed.Unix() / collapseBucketSecs
			}
		}

		key := bucketKey{norm: norm, bucket: bucket}
		score := turnRichness(t)

		if g, exists := groups[key]; exists {
			if score > g.score {
				g.best = i
				g.score = score
			}
		} else {
			groups[key] = &group{best: i, score: score}
			order = append(order, key)
		}
	}

	// Reconstruct in original encounter order so the subsequent sort is stable.
	out := make([]turn, 0, len(groups))
	for _, key := range order {
		g := groups[key]
		out = append(out, turns[g.best])
	}
	return out
}

// buildEventTreeOtel builds the turn list using OTel interaction spans as
// anchors. Each interaction span corresponds to one user prompt turn; its
// trace_id links all child spans. The user_query shape is synthesized to
// match the fields the frontend event-tree.js expects so no frontend
// changes are required.
func buildEventTreeOtel(database *sql.DB, limit int) ([]turn, error) {
	rows, err := database.Query(`
		SELECT s.signal_id, s.trace_id, COALESCE(s.span_id, ''),
		       s.session_id,
		       COALESCE(NULLIF(s.harness, ''), (SELECT sess.agent_assigned FROM sessions sess WHERE sess.session_id = s.session_id LIMIT 1), ''),
		       s.ts_micros, COALESCE(s.duration_ms, 0),
		       COALESCE(s.attrs_json, '{}'),
		       COALESCE(s.feature_id, '')
		FROM otel_signals s
		WHERE s.kind = 'span' AND s.canonical = 'interaction'
		ORDER BY s.ts_micros DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var turns []turn
	for rows.Next() {
		var signalID, traceID, spanID, sessionID, harness string
		var tsMicros, durationMs int64
		var attrsRaw, featureID string

		if err := rows.Scan(&signalID, &traceID, &spanID, &sessionID,
			&harness, &tsMicros, &durationMs, &attrsRaw, &featureID); err != nil {
			continue
		}

		// Convert ts_micros to RFC3339 for the frontend.
		ts := time.UnixMicro(tsMicros).UTC().Format(time.RFC3339)

		// Extract prompt text from attrs_json (attrs.user_prompt, attrs.prompt).
		promptText := displayPromptText(extractPromptText(attrsRaw))

		// If attrs_json didn't carry the prompt, look for a user_prompt log
		// record in the same trace.
		if promptText == "" && traceID != "" {
			promptText = displayPromptText(fetchPromptFromTrace(database, traceID))
		}
		if ingest.IsSystemMessage(promptText) {
			continue
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
			"agent_id":        harness,
			"event_type":      "tool_call",
			"output_summary":  "",
			"status":          "recorded",
			"parent_event_id": "",
			"subagent_type":   "",
			"tool_use_id":     "",
			"model":           "",
			// otel_backed signals that this row renders from OTel spans
			// rather than hook-derived Children. nestTaskNotificationTurns
			// uses this flag to avoid nesting wake-turns under parents that
			// won't render their Children field (which would silently drop
			// the wake-turn from the feed entirely).
			"otel_backed": true,
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

	return turns, nil
}

// buildEventTreeOtelLogFallback synthesizes turn rows from OTel user_prompt logs
// when no interaction span anchors the prompt. This covers Codex sessions that
// currently emit prompt/tool activity as logs only.
func buildEventTreeOtelLogFallback(database *sql.DB, limit int) ([]turn, error) {
	rows, err := database.Query(`
		SELECT s.signal_id, COALESCE(s.harness, ''), COALESCE(s.session_id, ''), COALESCE(s.trace_id, ''),
		       COALESCE(s.prompt_id, ''), COALESCE(s.feature_id, ''),
		       s.ts_micros, COALESCE(s.attrs_json, '{}'),
		       COALESCE((SELECT f.title FROM features f WHERE f.id = s.feature_id LIMIT 1), '')
		FROM otel_signals s
		WHERE s.kind = 'log' AND s.canonical = 'user_prompt'
		  AND NOT EXISTS (
		    SELECT 1 FROM otel_signals i
		    WHERE i.kind = 'span' AND i.canonical = 'interaction'
		      AND i.session_id = s.session_id
		      AND ABS(i.ts_micros - s.ts_micros) < ?
		  )`, hookOtelDedupWindowMicros)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var anchors []otelPromptAnchor
	for rows.Next() {
		var anchor otelPromptAnchor
		var attrsRaw string
		if err := rows.Scan(
			&anchor.SignalID, &anchor.Harness, &anchor.SessionID, &anchor.TraceID,
			&anchor.PromptID, &anchor.FeatureID, &anchor.RawTSMicros,
			&attrsRaw, &anchor.FeatureName,
		); err != nil {
			continue
		}
		anchor.AttrsRaw = attrsRaw
		anchor.PromptText = displayPromptText(extractPromptText(attrsRaw))
		if ingest.IsSystemMessage(anchor.PromptText) {
			continue
		}
		_, anchor.TSMicros = treeTimestampFromOtel(anchor.RawTSMicros, anchor.AttrsRaw)
		if anchor.TSMicros == 0 {
			continue
		}
		anchors = append(anchors, anchor)
	}

	if len(anchors) == 0 {
		return []turn{}, nil
	}

	anchors = filterAnchorsAgainstInteractionSpans(database, anchors)
	if len(anchors) == 0 {
		return []turn{}, nil
	}

	sort.SliceStable(anchors, func(i, j int) bool {
		return anchors[i].TSMicros > anchors[j].TSMicros
	})
	if len(anchors) > limit {
		anchors = anchors[:limit]
	}

	sessionAnchors := make(map[string][]otelPromptAnchor)
	for _, anchor := range anchors {
		sessionAnchors[anchor.SessionID] = append(sessionAnchors[anchor.SessionID], anchor)
	}
	for sessionID := range sessionAnchors {
		sort.Slice(sessionAnchors[sessionID], func(i, j int) bool {
			return sessionAnchors[sessionID][i].TSMicros < sessionAnchors[sessionID][j].TSMicros
		})
	}

	turns := make([]turn, 0, len(anchors))
	for _, anchor := range anchors {
		windowEnd := nextPromptBoundary(sessionAnchors[anchor.SessionID], anchor.SignalID)
		children := fetchOtelLogChildren(database, anchor, windowEnd)
		stats := computeStats(children)
		timestamp, _ := treeTimestampFromOtel(anchor.RawTSMicros, anchor.AttrsRaw)

		turns = append(turns, turn{
			SessionID: anchor.SessionID,
			UserQuery: map[string]any{
				"event_id":        anchor.SignalID,
				"agent_id":        anchor.Harness,
				"event_type":      "tool_call",
				"timestamp":       timestamp,
				"tool_name":       "UserQuery",
				"input_summary":   anchor.PromptText,
				"output_summary":  "",
				"session_id":      anchor.SessionID,
				"feature_id":      anchor.FeatureID,
				"feature_title":   anchor.FeatureName,
				"status":          "recorded",
				"parent_event_id": "",
				"subagent_type":   "",
				"tool_use_id":     anchor.PromptID,
				"model":           "",
				// otel_backed: see comment in buildEventTreeOtel.
				"otel_backed": true,
			},
			Children: children,
			Stats:    stats,
		})
	}

	return turns, nil
}

// filterAnchorsAgainstInteractionSpans removes anchors whose effective timestamp
// falls within hookOtelDedupWindowMicros of an interaction span. This handles
// the case where a prompt log has ts_micros=0 but a valid attrs_json timestamp,
// which the SQL NOT EXISTS check misses because it compares against raw ts_micros.
//
// For anchors with TSMicros==0 (neither raw ts_micros nor event.timestamp could
// be resolved), timestamp-based dedup is impossible. Instead, we perform a
// content match: if any interaction span in the same session stores the same
// prompt text (checked via json_extract across the keys extractPromptText uses),
// the anchor is a duplicate of that span row and is dropped.
func filterAnchorsAgainstInteractionSpans(database *sql.DB, anchors []otelPromptAnchor) []otelPromptAnchor {
	out := anchors[:0:len(anchors)]
	for _, a := range anchors {
		if a.SessionID == "" {
			out = append(out, a)
			continue
		}
		if a.TSMicros == 0 {
			// Cannot dedup by timestamp; fall back to prompt-text content match.
			if anchorHasMatchingInteractionSpan(database, a) {
				continue // duplicate of the interaction-span row — drop it
			}
			out = append(out, a)
			continue
		}
		var count int
		_ = database.QueryRow(`
			SELECT COUNT(*) FROM otel_signals
			WHERE kind = 'span' AND canonical = 'interaction'
			  AND session_id = ? AND ABS(ts_micros - ?) < ?`,
			a.SessionID, a.TSMicros, hookOtelDedupWindowMicros).Scan(&count)
		if count == 0 {
			out = append(out, a)
		}
	}
	return out
}

// anchorHasMatchingInteractionSpan reports whether any interaction span in the
// same session stores the same prompt text as the anchor. Interaction spans may
// store the prompt under "user_prompt", "prompt", or "text" (the same keys
// extractPromptText checks); we test all three so the match is key-agnostic.
func anchorHasMatchingInteractionSpan(database *sql.DB, a otelPromptAnchor) bool {
	if a.PromptText == "" {
		return false // empty prompt — cannot content-match; keep the anchor
	}
	var count int
	_ = database.QueryRow(`
		SELECT COUNT(*) FROM otel_signals
		WHERE kind = 'span' AND canonical = 'interaction'
		  AND session_id = ?
		  AND (
		    json_extract(attrs_json, '$.user_prompt') = ?
		    OR json_extract(attrs_json, '$.prompt') = ?
		    OR json_extract(attrs_json, '$.text') = ?
		  )`,
		a.SessionID, a.PromptText, a.PromptText, a.PromptText).Scan(&count)
	return count > 0
}

func treeTimestampFromOtel(tsMicros int64, attrsRaw string) (string, int64) {
	if tsMicros > 0 {
		return time.UnixMicro(tsMicros).UTC().Format(time.RFC3339), tsMicros
	}

	var attrs map[string]any
	if err := json.Unmarshal([]byte(attrsRaw), &attrs); err == nil {
		if raw, ok := attrs["event.timestamp"]; ok {
			if parsed, ok := parseTreeEventTimestamp(raw); ok {
				return parsed.UTC().Format(time.RFC3339), parsed.UnixMicro()
			}
		}
	}

	return time.UnixMicro(tsMicros).UTC().Format(time.RFC3339), tsMicros
}

func parseTreeEventTimestamp(v any) (time.Time, bool) {
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

// buildEventTreeHookUnanchored returns hook-based UserQuery turns that do
// NOT correspond to an OTel interaction span. Anchoring is detected with
// two complementary checks so deduplication holds even when hook events
// arrive without a step_id:
//
//  1. step_id match — when the hook UserQuery carries the OTel trace_id,
//     skip if any interaction span shares it.
//  2. session+timestamp window — when step_id is empty, skip if any
//     interaction span exists in the same session within
//     hookOtelDedupWindowMicros of the hook event's timestamp.
//
// Buildings before OTel emission and pure hook-only sessions still pass
// both checks and remain visible in /api/events/tree.
func buildEventTreeHookUnanchored(database *sql.DB, limit int) ([]turn, error) {
	rows, err := database.Query(`
		SELECT `+eventColumns+`
		FROM agent_events e
		WHERE e.tool_name = 'UserQuery'
		  AND (e.agent_id = '' OR e.agent_id = 'claude-code')
		  AND NOT EXISTS (
		    SELECT 1 FROM otel_signals s
		    WHERE s.kind = 'span' AND s.canonical = 'interaction'
		      AND s.session_id = e.session_id
		      AND (
		        (e.step_id IS NOT NULL AND e.step_id != '' AND s.trace_id = e.step_id)
		        OR ABS(s.ts_micros - CAST(strftime('%s', e.timestamp) AS INTEGER) * 1000000) < ?
		      )
		  )
		ORDER BY e.timestamp DESC
		LIMIT ?`, hookOtelDedupWindowMicros, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var turns []turn
	for rows.Next() {
		evt := scanEvent(rows)
		if evt == nil {
			continue
		}
		if inputSummary, _ := evt["input_summary"].(string); ingest.IsSystemMessage(inputSummary) {
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
		return []turn{}, nil
	}
	return turns, nil
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

// displayPromptText keeps the stored OTel prompt intact while removing the
// repeated orchestrator preamble from Codex-spawned agent prompts. Without this,
// parallel subagents all render as "Work item: ... Start by running ..." and
// look duplicated even when their actual task bodies differ.
func displayPromptText(raw string) string {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, "Work item:") {
		return s
	}

	if idx := strings.Index(s, "."); idx >= 0 {
		s = strings.TrimSpace(s[idx+1:])
	}
	if strings.HasPrefix(s, "Start by running") {
		if idx := strings.Index(s, "`."); idx >= 0 {
			s = strings.TrimSpace(s[idx+2:])
		} else if idx := strings.Index(s, "."); idx >= 0 {
			s = strings.TrimSpace(s[idx+1:])
		}
	}
	if s == "" {
		return strings.TrimSpace(raw)
	}
	return s
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

func nextPromptBoundary(anchors []otelPromptAnchor, signalID string) int64 {
	for i, anchor := range anchors {
		if anchor.SignalID != signalID {
			continue
		}
		if i+1 < len(anchors) {
			return anchors[i+1].TSMicros
		}
		break
	}
	return 0
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

func fetchOtelLogChildren(database *sql.DB, anchor otelPromptAnchor, windowEnd int64) []map[string]any {
	query := `
		SELECT signal_id, COALESCE(harness, ''), COALESCE(canonical, ''), COALESCE(tool_name, ''),
		       COALESCE(model, ''), ts_micros, COALESCE(duration_ms, 0),
		       success, COALESCE(decision, ''), COALESCE(feature_id, ''),
		       COALESCE(prompt_id, ''), COALESCE(attrs_json, '{}'),
		       COALESCE((SELECT f.title FROM features f WHERE f.id = s.feature_id LIMIT 1), '')
		FROM otel_signals s
		WHERE s.kind = 'log'
		  AND s.session_id = ?
		  AND s.canonical IN ('api_request', 'tool_result', 'tool_decision', 'api_error')`
	args := []any{anchor.SessionID}
	if anchor.PromptID != "" {
		query += ` AND (s.prompt_id = ? OR s.prompt_id = '')`
		args = append(args, anchor.PromptID)
	}
	query += ` ORDER BY s.ts_micros DESC`

	rows, err := database.Query(query, args...)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()

	type otelLogChild struct {
		tsMicros int64
		event    map[string]any
	}
	var children []otelLogChild
	for rows.Next() {
		var signalID, harness, canonical, toolName, model string
		var tsMicros, durationMs int64
		var successVal any
		var decision, featureID, promptID, attrsRaw, featureTitle string

		if err := rows.Scan(
			&signalID, &harness, &canonical, &toolName, &model,
			&tsMicros, &durationMs, &successVal, &decision, &featureID,
			&promptID, &attrsRaw, &featureTitle,
		); err != nil {
			continue
		}

		timestamp, effectiveTSMicros := feedTimestampFromOtel(tsMicros, attrsRaw)
		if effectiveTSMicros < anchor.TSMicros {
			continue
		}
		if windowEnd > 0 && effectiveTSMicros >= windowEnd {
			continue
		}

		summary := otelSummary(canonical, toolName, model, 0, 0, attrsRaw)
		if canonical == "tool_decision" && decision != "" {
			summary = decisionSummary(toolName, decision)
		}
		if canonical == "api_error" && summary == canonical {
			summary = "API error"
		}

		status := "recorded"
		if b, ok := decodeFeedSuccess(successVal); ok && !b {
			status = "failed"
		}
		if canonical == "api_error" {
			status = "failed"
		}

		displayTool := toolName
		if displayTool == "" {
			displayTool = canonical
		}

		children = append(children, otelLogChild{
			tsMicros: effectiveTSMicros,
			event: map[string]any{
				"event_id":        signalID,
				"agent_id":        harness,
				"event_type":      otelLogEventType(canonical, status),
				"timestamp":       timestamp,
				"tool_name":       displayTool,
				"input_summary":   summary,
				"output_summary":  "",
				"session_id":      anchor.SessionID,
				"feature_id":      featureID,
				"feature_title":   featureTitle,
				"status":          status,
				"parent_event_id": anchor.SignalID,
				"subagent_type":   "",
				"tool_use_id":     promptID,
				"model":           model,
				"children":        []map[string]any{},
			},
		})
	}

	if children == nil {
		return []map[string]any{}
	}

	sort.SliceStable(children, func(i, j int) bool {
		return children[i].tsMicros > children[j].tsMicros
	})

	out := make([]map[string]any, 0, len(children))
	for _, child := range children {
		out = append(out, child.event)
	}
	return out
}

func decisionSummary(toolName, decision string) string {
	if toolName == "" {
		return decision
	}
	return toolName + ": " + decision
}

func otelLogEventType(canonical, status string) string {
	if canonical == "api_error" || status == "failed" {
		return "error"
	}
	return "tool_call"
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
		"event_id":        eventID,
		"agent_id":        agentID,
		"event_type":      eventType,
		"timestamp":       ts,
		"tool_name":       toolName,
		"input_summary":   inputSum,
		"output_summary":  outputSum,
		"session_id":      sessionID,
		"feature_id":      featureID,
		"feature_title":   featureTitle,
		"status":          status,
		"parent_event_id": parentEvtID,
		"subagent_type":   subagentType,
		"tool_use_id":     stepID,
		"model":           model,
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

// parseTaskNotification extracts the tool-use-id and task-id from a
// <task-notification> wake message. Returns empty strings when the text does
// not look like a task-notification message.
//
// Example payload (abbreviated):
//
//	<task-notification><task-id>bt8yi6rhf</task-id><tool-use-id>toolu_01XYZ</tool-use-id>...</task-notification>
func parseTaskNotification(text string) (toolUseID, taskID string) {
	if !containsStr(text, "<task-notification>") {
		return "", ""
	}
	toolUseID = extractXMLTag(text, "tool-use-id")
	taskID = extractXMLTag(text, "task-id")
	return toolUseID, taskID
}

// extractXMLTag returns the first inner text of <tag>…</tag> in s.
// It is intentionally minimal — it does not handle nested tags or attributes.
func extractXMLTag(s, tag string) string {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	start := indexStr(s, open)
	if start < 0 {
		return ""
	}
	start += len(open)
	end := indexStr(s[start:], close)
	if end < 0 {
		return ""
	}
	return s[start : start+end]
}

// containsStr reports whether s contains substr (avoids importing strings).
func containsStr(s, substr string) bool {
	return indexStr(s, substr) >= 0
}

// indexStr returns the index of the first instance of substr in s, or -1.
func indexStr(s, substr string) int {
	n := len(substr)
	if n == 0 {
		return 0
	}
	if n > len(s) {
		return -1
	}
	for i := 0; i <= len(s)-n; i++ {
		if s[i:i+n] == substr {
			return i
		}
	}
	return -1
}

// taskNotificationTitle returns a clean, non-XML title for a wake turn.
// Format: "↩ resumed: background task <taskID> completed"
// If taskID is empty, it returns "↩ resumed: background task completed".
func taskNotificationTitle(taskID string) string {
	if taskID != "" {
		return "↩ resumed: background task " + taskID + " completed"
	}
	return "↩ resumed: background task completed"
}

// findToolUseInChildren walks the children tree of a turn and returns true
// when any child event has tool_use_id == id.
func findToolUseInChildren(children []map[string]any, id string) bool {
	for _, c := range children {
		if tid, _ := c["tool_use_id"].(string); tid == id {
			return true
		}
		if sub, ok := c["children"].([]map[string]any); ok {
			if findToolUseInChildren(sub, id) {
				return true
			}
		}
	}
	return false
}

// nestTaskNotificationTurns is a post-dedup pass that moves task-notification
// wake turns out of the top-level list and nests them as children of the turn
// that contains the originating background tool call (matched by tool-use-id).
//
// Matching strategy:
//  1. Parse <tool-use-id> from the notification text → primary key.
//  2. Walk every non-notification turn's children tree looking for that id.
//  3. When found: append the notification turn's children to the originating
//     row's children, relabel its input_summary to a clean title, then attach
//     it as a child of the originating turn's user_query node.
//  4. Fallback (no match): relabel the notification's input_summary to the
//     clean title and leave it as a top-level row — never display raw XML.
//
// This function is additive and orthogonal to collapseDuplicateTurns.
// It must be called AFTER collapseDuplicateTurns and BEFORE the final sort.
func nestTaskNotificationTurns(turns []turn) []turn {
	// Separate notification turns from normal turns.
	type notifTurn struct {
		t         turn
		toolUseID string
		taskID    string
	}

	var normal []turn
	var notifs []notifTurn

	for _, t := range turns {
		raw, _ := t.UserQuery["input_summary"].(string)
		toolUseID, taskID := parseTaskNotification(raw)
		if toolUseID != "" || taskID != "" {
			notifs = append(notifs, notifTurn{t: t, toolUseID: toolUseID, taskID: taskID})
		} else {
			normal = append(normal, t)
		}
	}

	if len(notifs) == 0 {
		return turns // fast path — nothing to do
	}

	// For each notification, try to find the originating turn in normal.
	// Build an index: tool_use_id → index in normal slice.
	// We walk children trees, so this index maps an id to a normal-turn index.
	type matchResult struct {
		normalIdx int
		found     bool
	}

	matchNotif := func(n notifTurn) matchResult {
		if n.toolUseID != "" {
			for i := range normal {
				if findToolUseInChildren(normal[i].Children, n.toolUseID) {
					return matchResult{i, true}
				}
			}
		}
		// Fallback: task-id match not implemented (too ambiguous without tool_use_id).
		return matchResult{-1, false}
	}

	// Track which normal turns have received a nested notification (for
	// children merge ordering).
	for _, n := range notifs {
		m := matchNotif(n)
		// Build a clean child node from the notification turn.
		cleanTitle := taskNotificationTitle(n.taskID)

		// Determine whether the matching parent will render its Children
		// field in the frontend. OTel-backed rows prefer OTel spans from
		// /api/otel/spans over hook-derived turn.Children (see event-tree.js
		// renderTurn: spans take priority, Children only used as fallback).
		// Nesting a wake-turn under an OTel-backed parent appends it to
		// Children, which the frontend will never render — the wake-turn
		// silently vanishes. Use the otel_backed flag set by
		// buildEventTreeOtel/buildEventTreeOtelLogFallback to detect this.
		parentIsOtelBacked := m.found && func() bool {
			v, _ := normal[m.normalIdx].UserQuery["otel_backed"].(bool)
			return v
		}()

		if m.found && !parentIsOtelBacked {
			// Safe to nest: the parent renders Children (hook-derived row).
			// Attach a summary child representing the wake event itself, with
			// the notification's own children nested beneath it.
			wakeNode := map[string]any{
				"event_id":        n.t.UserQuery["event_id"],
				"agent_id":        n.t.UserQuery["agent_id"],
				"event_type":      "tool_call",
				"timestamp":       n.t.UserQuery["timestamp"],
				"tool_name":       "UserQuery",
				"input_summary":   cleanTitle,
				"output_summary":  "",
				"session_id":      n.t.UserQuery["session_id"],
				"feature_id":      n.t.UserQuery["feature_id"],
				"feature_title":   n.t.UserQuery["feature_title"],
				"status":          "recorded",
				"parent_event_id": "",
				"subagent_type":   "",
				"tool_use_id":     "",
				"model":           "",
				"children":        n.t.Children,
			}
			normal[m.normalIdx].Children = append(normal[m.normalIdx].Children, wakeNode)
			// Recompute parent stats so tool/error counts reflect the new child.
			normal[m.normalIdx].Stats = computeStats(normal[m.normalIdx].Children)
		} else {
			// Either no originating row matched, or the matched parent is
			// OTel-backed (renders spans, not Children). Keep the wake-turn
			// top-level with a clean, non-XML title. This is the safe
			// fallback: the wake-turn is never silently dropped, and no raw
			// XML is ever shown as a visible title.
			notifCopy := n.t
			notifCopy.UserQuery = make(map[string]any, len(n.t.UserQuery))
			for k, v := range n.t.UserQuery {
				notifCopy.UserQuery[k] = v
			}
			notifCopy.UserQuery["input_summary"] = cleanTitle
			normal = append(normal, notifCopy)
		}
	}

	return normal
}
