package main

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/harness"
)

// mergeWorkBoardSignals enriches each feature map (produced by featuresFromDB /
// featuresFromHTML, already carrying claim_* attribution) with READ-ONLY
// execution-visibility signals for the Kanban work board (Tier 4, feat-885ec940):
//
//   - active_session / active_owner_harness / active_session_live: the work
//     owner derived from active_work_items joined to sessions, with honest
//     heartbeat-derived liveness (NOT sessions.status — folds bug-6c3e8252).
//   - last_activity_at / last_activity_age_seconds / last_tool: newest
//     agent_event attributed to the feature.
//   - last_touched_file: newest feature_files row for the feature.
//   - file_conflict: live cross-session file overlap on any file this owner
//     touched, computed via dbpkg.FindLiveFileOverlaps — the SAME source
//     `wipnote who` and the PreToolUse ⚠ advisory consume (NOT duplicated).
//   - moved_recently / reassigned_recently: derived from agent_events of the
//     status-change / claim kinds within the recency window.
//   - step_tracking_supported / step_tracking_detail: the canonical
//     per-harness step-tracking truth from resolveTaskTrackingInfo. This is
//     the honest "steps not live for this harness" signal — the dashboard MUST
//     NOT imply live step tracking for a harness that cannot emit step events.
//
// wipnoteDir is the .wipnote/ directory; its parent is the project root used
// for the liveness/overlap config lookups. All queries are read-only.
func mergeWorkBoardSignals(features []map[string]any, database *sql.DB, wipnoteDir string) {
	if database == nil {
		return
	}
	projectRoot := filepath.Dir(wipnoteDir)
	window := dbpkg.FileOverlapWindow(projectRoot)
	liveness := dbpkg.LivenessStalenessThreshold(projectRoot)
	now := time.Now().UTC()

	for i := range features {
		id, _ := features[i]["id"].(string)
		if id == "" {
			continue
		}

		// Active work owner from active_work_items joined to sessions. The
		// most-recently-claimed row wins. Liveness is heartbeat-derived so a
		// stale 'active' ghost never shows as live.
		var ownerSession, ownerAgent, ownerModel string
		_ = database.QueryRow(`
			SELECT awi.session_id,
			       COALESCE(s.agent_assigned, awi.agent_id),
			       COALESCE(s.model, '')
			FROM active_work_items awi
			LEFT JOIN sessions s ON s.session_id = awi.session_id
			WHERE awi.work_item_id = ?
			ORDER BY awi.claimed_at DESC
			LIMIT 1`, id).Scan(&ownerSession, &ownerAgent, &ownerModel)
		features[i]["active_session"] = ownerSession
		features[i]["active_owner_harness"] = harness.NormalizeDisplayName(ownerAgent)
		features[i]["active_owner_model"] = ownerModel
		features[i]["active_session_live"] =
			ownerSession != "" && dbpkg.SessionLivenessByHeartbeat(database, ownerSession, liveness)

		// Last activity (newest event attributed to the feature).
		var lastTS, lastTool string
		_ = database.QueryRow(`
			SELECT timestamp, COALESCE(tool_name, '')
			FROM agent_events
			WHERE feature_id = ?
			ORDER BY timestamp DESC
			LIMIT 1`, id).Scan(&lastTS, &lastTool)
		features[i]["last_activity_at"] = lastTS
		features[i]["last_tool"] = lastTool
		features[i]["last_activity_age_seconds"] = ageSeconds(lastTS, now)

		// Last touched file.
		var lastFile string
		_ = database.QueryRow(`
			SELECT file_path FROM feature_files
			WHERE feature_id = ?
			ORDER BY last_seen DESC
			LIMIT 1`, id).Scan(&lastFile)
		features[i]["last_touched_file"] = lastFile

		// Recently-moved / reassigned markers. There is no agent_events
		// "status_change" type — board moves/reassignments are visible as
		// claim ownership churn in the recency window (the canonical
		// agent_events claim vocabulary). claim.handoff == reassigned;
		// any claim ownership change (claimed/handoff/abandoned/expired)
		// == the card moved on the board. COALESCE because SUM() over zero
		// matching rows returns NULL.
		var moved, reassigned int
		_ = database.QueryRow(`
			SELECT
			  COALESCE(SUM(CASE WHEN event_type IN
			    ('claim.claimed','claim.handoff','claim.abandoned','claim.expired','claim.completed')
			    THEN 1 ELSE 0 END), 0),
			  COALESCE(SUM(CASE WHEN event_type = 'claim.handoff' THEN 1 ELSE 0 END), 0)
			FROM agent_events
			WHERE feature_id = ?
			  AND timestamp >= datetime('now', ?)`,
			id, recencyModifier(window)).Scan(&moved, &reassigned)
		features[i]["moved_recently"] = moved > 0
		features[i]["reassigned_recently"] = reassigned > 0

		// File-conflict signal — SAME source as `wipnote who` ⚠. A conflict
		// exists when the claim collides (already merged as claim_collision)
		// OR a live cross-session overlap exists on a file the owner touched.
		conflict, _ := features[i]["claim_collision"].(bool)
		if !conflict && ownerSession != "" {
			conflict = ownerHasLiveFileOverlap(database, id, ownerSession, window, liveness)
		}
		features[i]["file_conflict"] = conflict

		// Per-harness step-tracking honesty. Owner harness drives the signal;
		// fall back to the claim harness, then claude-code default.
		dh := harness.NormalizeDisplayName(ownerAgent)
		if dh == "" {
			dh, _ = features[i]["claim_harness"].(string)
			dh = harness.NormalizeDisplayName(dh)
		}
		if dh == "" {
			dh = "claude-code"
		}
		ti := resolveTaskTrackingInfo(dh)
		features[i]["step_tracking_supported"] = ti.Supported
		features[i]["step_tracking_detail"] = ti.Detail
	}
}

// ownerHasLiveFileOverlap reports whether any file the owner session touched
// for this feature has a LIVE overlap with another session, reusing
// dbpkg.FindLiveFileOverlaps (the canonical `wipnote who` ⚠ source). It scans
// at most a handful of recent files per feature; all reads, zero writes.
func ownerHasLiveFileOverlap(database *sql.DB, featureID, ownerSession string,
	window, liveness time.Duration) bool {
	rows, err := database.Query(`
		SELECT DISTINCT file_path FROM feature_files
		WHERE feature_id = ? AND session_id = ?
		ORDER BY last_seen DESC
		LIMIT 25`, featureID, ownerSession)
	if err != nil {
		return false
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil && p != "" {
			paths = append(paths, p)
		}
	}
	for _, p := range paths {
		ov, oerr := dbpkg.FindLiveFileOverlaps(database, p, ownerSession, window, liveness)
		if oerr == nil && len(ov) > 0 {
			return true
		}
	}
	return false
}

// ageSeconds parses a SQLite/RFC3339 timestamp and returns its age in whole
// seconds relative to now. Returns -1 for an empty/unparseable timestamp so
// the dashboard can render "no activity" distinctly from "0s ago".
func ageSeconds(ts string, now time.Time) int64 {
	if ts == "" {
		return -1
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, ts); err == nil {
			d := now.Sub(t.UTC())
			if d < 0 {
				return 0
			}
			return int64(d.Seconds())
		}
	}
	return -1
}

// recencyModifier renders a SQLite datetime('now', ?) negative-minutes
// modifier for the configured overlap/recency window.
func recencyModifier(window time.Duration) string {
	m := int64(window / time.Minute)
	if m <= 0 {
		m = 15
	}
	return "-" + strconv.FormatInt(m, 10) + " minutes"
}

// featureActivityHandler returns a timeline of all agent_events attributed to
// a feature, plus a summary of files edited and sessions involved.
// Route: /api/features/{id}/activity   (id extracted from URL path)
func featureActivityHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Extract feature ID from URL path: /api/features/{id}/activity
		path := strings.TrimPrefix(r.URL.Path, "/api/features/")
		path = strings.TrimSuffix(path, "/activity")
		featureID := strings.TrimSpace(path)
		if featureID == "" {
			http.Error(w, "feature id required", http.StatusBadRequest)
			return
		}

		// Look up feature title from DB (graceful fallback to empty string).
		var featureTitle string
		database.QueryRow(`SELECT COALESCE(title,'') FROM features WHERE id = ?`, featureID).Scan(&featureTitle)

		// Query events attributed to this feature.
		rows, err := database.Query(`
			SELECT event_id, timestamp, COALESCE(tool_name,''), COALESCE(input_summary,''),
			       COALESCE(status,''), session_id
			FROM agent_events
			WHERE feature_id = ?
			ORDER BY timestamp DESC
			LIMIT 200`, featureID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type eventRow struct {
			EventID      string `json:"event_id"`
			Timestamp    string `json:"timestamp"`
			ToolName     string `json:"tool_name"`
			InputSummary string `json:"input_summary"`
			Status       string `json:"status"`
			SessionID    string `json:"session_id"`
		}

		events := make([]eventRow, 0, 100)
		sessionSet := make(map[string]struct{})
		for rows.Next() {
			var ev eventRow
			if err := rows.Scan(&ev.EventID, &ev.Timestamp, &ev.ToolName,
				&ev.InputSummary, &ev.Status, &ev.SessionID); err != nil {
				continue
			}
			events = append(events, ev)
			if ev.SessionID != "" {
				sessionSet[ev.SessionID] = struct{}{}
			}
		}

		// Query file edits grouped by file path.
		fileRows, err := database.Query(`
			SELECT file_path, COUNT(*) AS edit_count, MAX(last_seen) AS last_edit
			FROM feature_files
			WHERE feature_id = ?
			GROUP BY file_path
			ORDER BY last_edit DESC`, featureID)

		type fileEdit struct {
			FilePath  string `json:"file_path"`
			EditCount int    `json:"edit_count"`
			LastEdit  string `json:"last_edit"`
		}

		fileEdits := make([]fileEdit, 0, 20)
		if err == nil {
			defer fileRows.Close()
			for fileRows.Next() {
				var fe fileEdit
				if err := fileRows.Scan(&fe.FilePath, &fe.EditCount, &fe.LastEdit); err != nil {
					continue
				}
				fileEdits = append(fileEdits, fe)
			}
		}

		// Query git commits linked to this feature.
		commitRows, err := database.Query(`
			SELECT commit_hash, COALESCE(message,''), COALESCE(timestamp,'')
			FROM git_commits
			WHERE feature_id = ?
			ORDER BY timestamp DESC
			LIMIT 50`, featureID)

		type commitRow struct {
			SHA       string `json:"sha"`
			Subject   string `json:"subject"`
			Timestamp string `json:"timestamp"`
		}

		commits := make([]commitRow, 0, 10)
		if err == nil {
			defer commitRows.Close()
			for commitRows.Next() {
				var cr commitRow
				var fullMsg string
				if err := commitRows.Scan(&cr.SHA, &fullMsg, &cr.Timestamp); err != nil {
					continue
				}
				// Use first line of commit message as subject.
				if nl := strings.IndexByte(fullMsg, '\n'); nl >= 0 {
					cr.Subject = fullMsg[:nl]
				} else {
					cr.Subject = fullMsg
				}
				commits = append(commits, cr)
			}
		}

		// Build unique session list preserving discovery order.
		sessions := make([]string, 0, len(sessionSet))
		seen := make(map[string]bool)
		for _, ev := range events {
			if ev.SessionID != "" && !seen[ev.SessionID] {
				sessions = append(sessions, ev.SessionID)
				seen[ev.SessionID] = true
			}
		}

		respondJSON(w, map[string]any{
			"feature_id":    featureID,
			"feature_title": featureTitle,
			"total_events":  len(events),
			"events":        events,
			"file_edits":    fileEdits,
			"commits":       commits,
			"sessions":      sessions,
		})
	}
}

// featureActivityRouter dispatches /api/features/ sub-routes.
// It handles both /api/features/detail and /api/features/{id}/activity,
// delegating unknown paths to a 404.
func featureActivityRouter(database *sql.DB, wipnoteDir string) http.HandlerFunc {
	detailHandler := featureDetailHandler(wipnoteDir)
	relatedHandler := relatedFeaturesHandler(database)
	activityHandler := featureActivityHandler(database)

	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == "/api/features/detail":
			detailHandler(w, r)
		case path == "/api/features/related":
			relatedHandler(w, r)
		case strings.HasSuffix(path, "/activity"):
			activityHandler(w, r)
		default:
			http.NotFound(w, r)
		}
	}
}
