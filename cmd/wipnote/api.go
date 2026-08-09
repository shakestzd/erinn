package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
)

// respondJSON encodes v as JSON and writes it with status 200.
func respondJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, "encoding response", http.StatusInternalServerError)
	}
}

// initialStatsHandler returns the top-level counts the dashboard header uses.
// Matches /api/initial-stats that dashboard.html's loadInitialStats() calls.
func initialStatsHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var totalEvents, totalSessions int
		if err := database.QueryRow(`SELECT COUNT(*) FROM agent_events`).Scan(&totalEvents); err != nil && err != sql.ErrNoRows {
			http.Error(w, fmt.Sprintf("query agent_events count: %v", err), http.StatusInternalServerError)
			return
		}
		if err := database.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&totalSessions); err != nil && err != sql.ErrNoRows {
			http.Error(w, fmt.Sprintf("query sessions count: %v", err), http.StatusInternalServerError)
			return
		}

		rows, err := database.Query(
			`SELECT DISTINCT agent_id FROM agent_events ORDER BY agent_id`)
		agents := []string{}
		if err != nil {
			http.Error(w, fmt.Sprintf("query agents: %v", err), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var a string
			if rows.Scan(&a) == nil {
				agents = append(agents, a)
			}
		}

		respondJSON(w, map[string]any{
			"total_events":   totalEvents,
			"total_sessions": totalSessions,
			"agents":         agents,
		})
	}
}

// recentEventsHandler returns events ordered by timestamp DESC.
// Supports ?limit=N (default 50, max 200).
func recentEventsHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

		merged := merge(otelEvents, hookEvents, limit)
		events := make([]map[string]any, 0, len(merged))
		for _, ev := range merged {
			events = append(events, legacyEventFromFeed(ev))
		}

		respondJSON(w, events)
	}
}

// sessionsHandler returns the 20 most recent sessions.
func sessionsHandler(database *sql.DB, projectDir, wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nodes, err := dbpkg.LoadSessionAdherenceNodes(wipnoteDir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Scope to the current project. Legacy rows with empty project_dir
		// (ingested from Claude Code transcripts without attribution) are
		// hidden from the per-project dashboard. Tracked separately as a
		// data-attribution bug — the fix here is purely display-side.
		// The DB is per-project (path hashed by project root), so '.' is
		// unambiguous within it. SessionStart-hook sessions store project_dir
		// as '.' (repo-relative, via core/paths.NormalizeProjectDir), while
		// sessions ingested from other paths may use the absolute project dir.
		// Match both so active sessions are not excluded (bug-fc3b5559).
		rows, err := database.Query(`
			SELECT s.session_id, s.agent_assigned, s.status, s.created_at,
			       COALESCE(s.completed_at, ''), s.total_events,
			       COALESCE(
			           (SELECT w.work_item_id FROM active_work_items w
			            WHERE w.session_id = s.session_id AND w.agent_id = '__root__'
			            LIMIT 1),
			           s.active_feature_id, ''), COALESCE(s.model, ''),
			       COALESCE(s.title, '') AS title,
			       COALESCE((SELECT SUBSTR(m.content, 1, 120)
			                 FROM messages m
			                 WHERE m.session_id = s.session_id AND m.role = 'user'
			                 ORDER BY m.ordinal LIMIT 1), '') AS first_msg,
			       COALESCE((SELECT COUNT(*) FROM messages m2
			                 WHERE m2.session_id = s.session_id), 0) AS msg_count,
			       COALESCE(json_extract(s.metadata, '$.launch_mode'), '') AS launch_mode,
			       COALESCE((SELECT pf.plan_id FROM plan_feedback pf
			                 WHERE pf.action = 'session_id' AND pf.value = s.session_id
			                 LIMIT 1), '') AS plan_id,
			       COALESCE(s.session_family_id, s.session_id) AS family_id,
			       COALESCE(s.project_dir, '') AS canonical_project,
			       COALESCE(s.parent_session_id, '') AS parent_session_id
			FROM sessions s
			WHERE s.project_dir IN (?, '.')
			  AND (s.total_events > 0
			   OR EXISTS (SELECT 1 FROM messages m WHERE m.session_id = s.session_id)
			   OR s.status = 'active')
			  AND s.is_subagent = FALSE
			  AND COALESCE(s.title, '') NOT LIKE '[wipnote-titler]%'
			  AND COALESCE((SELECT SUBSTR(m4.content, 1, 30)
			       FROM messages m4
			       WHERE m4.session_id = s.session_id AND m4.role = 'user'
			       ORDER BY m4.ordinal LIMIT 1), '') NOT LIKE '[wipnote-titler]%'
			  AND (s.status = 'active' OR (SELECT COUNT(*) FROM messages m3
			       WHERE m3.session_id = s.session_id) >= 5)
			ORDER BY s.created_at DESC
			LIMIT 20`, projectDir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		sessions := make([]map[string]any, 0, 20)
		for rows.Next() {
			var sid, agent, status, created, completed string
			var totalEvents int
			var featureID, model, title, firstMsg string
			var msgCount int
			var sessionLaunchMode, sessionPlanID string
			var familyID, canonicalProject, parentSessionID string
			if err := rows.Scan(&sid, &agent, &status, &created, &completed,
				&totalEvents, &featureID, &model, &title, &firstMsg, &msgCount,
				&sessionLaunchMode, &sessionPlanID,
				&familyID, &canonicalProject, &parentSessionID); err != nil {
				continue
			}
			adherence, err := dbpkg.DeriveSessionAdherence(database, sid, nodes)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			// exec_root: root execution context. Subagents use parent; root sessions use self.
			execRoot := sid
			if parentSessionID != "" {
				execRoot = parentSessionID
			}
			// Claim/collision — best-effort, never fatal. Stable plan-c3bbb1ed contract.
			claimCollision := false
			claimStatus := ""
			claimSessionFamily := familyID
			if featureID != "" {
				if coll, cerr := dbpkg.DetectCollaboration(database, featureID); cerr == nil {
					claimCollision = coll.HasCollision
					if len(coll.Claimants) > 0 {
						claimStatus = string(coll.Claimants[0].Status)
					}
				}
			}
			if id, cerr := dbpkg.GetClaimIdentity(database, sid); cerr == nil && id != nil && id.SessionFamilyID != "" {
				claimSessionFamily = id.SessionFamilyID
			}
			// Files touched by this session — best-effort, never fatal.
			// Merge claimed (feature_files) + claimless (session_files) touches.
			sessionFiles, _ := dbpkg.ListFilesBySession(database, sid)
			if claimless, cerr := dbpkg.ListClaimlessFilesBySession(database, sid); cerr == nil {
				sessionFiles = append(sessionFiles, claimless...)
			}
			if sessionFiles == nil {
				sessionFiles = []dbpkg.SessionFile{}
			}
			// Honest liveness: derived from claim-heartbeat recency, NOT
			// sessions.status. A stale status='active' ghost row reports
			// live=false (folds bug-6c3e8252). projectDir is the canonical
			// project root for the config-tunable staleness threshold.
			live := dbpkg.SessionLivenessByHeartbeat(
				database, sid, dbpkg.LivenessStalenessThreshold(projectDir))
			sessions = append(sessions, map[string]any{
				"session_id":    sid,
				"agent":         agent,
				"harness":       agent,
				"status":        status,
				"created_at":    created,
				"completed_at":  completed,
				"total_events":  totalEvents,
				"feature_id":    featureID,
				"model":         model,
				"title":         title,
				"first_message": firstMsg,
				"message_count": msgCount,
				"launch_mode":   sessionLaunchMode,
				"plan_id":       sessionPlanID,
				"adherence":     adherence,
				// Isolation-visibility fields (slice-7). Stable plan-c3bbb1ed contract.
				"session_family_id":    familyID,
				"canonical_project":    canonicalProject,
				"exec_root":            execRoot,
				"claim_collision":      claimCollision,
				"claim_status":         claimStatus,
				"claim_session_family": claimSessionFamily,
				// Files this session touched (feat-f1c6f92e Tier 0).
				"files": sessionFiles,
				// Honest liveness from heartbeat recency (feat-793844bd).
				"live": live,
			})
		}

		respondJSON(w, sessions)
	}
}

func sessionAdherenceTrendHandler(database *sql.DB, projectDir, wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nodes, err := dbpkg.LoadSessionAdherenceNodes(wipnoteDir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		points, err := dbpkg.ListSessionAdherenceTrend(database, projectDir, nodes, 24)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		respondJSON(w, map[string]any{"points": points})
	}
}

// featuresHandler returns up to 50 features, in-progress first.
// Falls back to scanning HTML files when SQLite features table is empty.
// Provenance fields (created_by_agent, created_by_model, created_by_role,
// created_by_cli_ver) are merged in from HTML files for DB-backed results.
func featuresHandler(database *sql.DB, projectDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		features := featuresFromDB(database)
		if len(features) == 0 {
			features = featuresFromHTML(projectDir)
		} else {
			mergeProvenanceFromHTML(features, projectDir)
		}
		mergeClaimAttributionFromDB(features, database)
		mergeWorkBoardSignals(features, database, projectDir)
		respondJSON(w, features)
	}
}

func featuresFromDB(database *sql.DB) []map[string]any {
	rows, err := database.Query(`
		SELECT f.id, f.type, f.title, f.status, f.priority,
		       COALESCE(f.track_id, ''), f.created_at,
		       f.steps_total, f.steps_completed,
		       COALESCE(t.title, '') AS track_title
		-- Tracks live in their own table. This join used to target features,
		-- matching a trk- id against the work-item primary key, so it never
		-- produced a row and track_title was empty for every feature in
		-- production — invisibly, because an empty title renders as nothing
		-- rather than erroring (bug-0fe9485f).
		FROM features f
		LEFT JOIN tracks t ON t.id = f.track_id
		ORDER BY
		    CASE f.status WHEN 'in-progress' THEN 0 WHEN 'todo' THEN 1 ELSE 2 END,
		    f.created_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	features := make([]map[string]any, 0, 200)
	for rows.Next() {
		var id, ftype, title, status, priority, trackID, created, trackTitle string
		var stepsTotal, stepsCompleted int
		if err := rows.Scan(&id, &ftype, &title, &status, &priority, &trackID,
			&created, &stepsTotal, &stepsCompleted, &trackTitle); err != nil {
			continue
		}
		features = append(features, map[string]any{
			"id":              id,
			"type":            ftype,
			"title":           title,
			"status":          status,
			"priority":        priority,
			"track_id":        trackID,
			"track_title":     trackTitle,
			"created_at":      created,
			"steps_total":     stepsTotal,
			"steps_completed": stepsCompleted,
			"edges":           map[string]any{},
		})
	}
	return features
}

// mergeProvenanceFromHTML reads provenance attrs from HTML work-item files and
// merges them into the feature maps returned by featuresFromDB. Items without
// provenance are left unchanged (backward-compatible). Called by featuresHandler.
func mergeProvenanceFromHTML(features []map[string]any, projectDir string) {
	// Build a lookup: id -> provenance fields from HTML.
	type provFields struct{ agent, model, role, cliVer string }
	lookup := make(map[string]provFields, len(features))
	for _, subdir := range []string{"features", "bugs", "spikes", "tracks"} {
		pattern := filepath.Join(projectDir, subdir, "*.html")
		files, _ := filepath.Glob(pattern)
		for _, f := range files {
			node, err := htmlparse.ParseFile(f)
			if err != nil || node == nil || node.ID == "" {
				continue
			}
			if node.CreatedByAgent == "" && node.CreatedByModel == "" && node.CreatedByRole == "" && node.CreatedByCLIVersion == "" {
				continue // nothing to merge
			}
			lookup[node.ID] = provFields{
				agent:  node.CreatedByAgent,
				model:  node.CreatedByModel,
				role:   node.CreatedByRole,
				cliVer: node.CreatedByCLIVersion,
			}
		}
	}
	for i := range features {
		id, _ := features[i]["id"].(string)
		if pf, ok := lookup[id]; ok {
			features[i]["created_by_agent"] = pf.agent
			features[i]["created_by_model"] = pf.model
			features[i]["created_by_role"] = pf.role
			features[i]["created_by_cli_ver"] = pf.cliVer
		}
	}
}

// mergeClaimAttributionFromDB merges claim ownership, collaboration state,
// session family, and harness fields into features returned by featuresFromDB or
// featuresFromHTML. This exposes identity fields to /api/features consumers
// (Kanban cards, dashboard session/file panels, plan-c3bbb1ed). Best-effort:
// missing claims leave the feature unchanged (backward-compatible).
func mergeClaimAttributionFromDB(features []map[string]any, database *sql.DB) {
	for i := range features {
		id, _ := features[i]["id"].(string)
		if id == "" {
			continue
		}
		coll, err := dbpkg.DetectCollaboration(database, id)
		if err != nil {
			continue
		}
		if len(coll.Claimants) == 0 {
			features[i]["claim_owner"] = ""
			features[i]["claim_session_family"] = ""
			features[i]["claim_harness"] = ""
			features[i]["claim_status"] = ""
			features[i]["claim_collision"] = false
			continue
		}
		// Primary claimant is the oldest (first by created_at).
		primary := coll.Claimants[0]
		features[i]["claim_owner"] = primary.OwnerSessionID
		features[i]["claim_harness"] = primary.OwnerAgent
		features[i]["claim_status"] = string(primary.Status)
		features[i]["claim_collision"] = coll.HasCollision
		// Session family for the primary claimant.
		var familyID string
		if err := database.QueryRow(
			`SELECT COALESCE(session_family_id, session_id) FROM sessions WHERE session_id = ?`,
			primary.OwnerSessionID,
		).Scan(&familyID); err == nil {
			features[i]["claim_session_family"] = familyID
		} else {
			features[i]["claim_session_family"] = primary.OwnerSessionID
		}
		// Claimant list for dashboard collision panels.
		claimants := make([]map[string]any, 0, len(coll.Claimants))
		for _, c := range coll.Claimants {
			claimants = append(claimants, map[string]any{
				"claim_id":   c.ClaimID,
				"session_id": c.OwnerSessionID,
				"harness":    c.OwnerAgent,
				"status":     string(c.Status),
				"claimed_at": c.LeasedAt.UTC().Format(time.RFC3339),
			})
		}
		features[i]["claimants"] = claimants
	}
}

// featuresFromHTML scans .wipnote/features/*.html, .wipnote/bugs/*.html,
// .wipnote/spikes/*.html, .wipnote/tracks/*.html and parses each file.
func featuresFromHTML(projectDir string) []map[string]any {
	// Build track title lookup from tracks/*.html first.
	trackTitles := buildTrackTitles(projectDir)

	features := make([]map[string]any, 0, 100)
	for _, subdir := range []string{"features", "bugs", "spikes", "tracks"} {
		pattern := filepath.Join(projectDir, subdir, "*.html")
		files, _ := filepath.Glob(pattern)
		for _, f := range files {
			node, err := htmlparse.ParseFile(f)
			if err != nil || node == nil {
				continue
			}
			completed := 0
			for _, s := range node.Steps {
				if s.Completed {
					completed++
				}
			}
			edges := node.Edges
			if edges == nil {
				edges = map[string][]models.Edge{}
			}
			trackTitle := trackTitles[node.TrackID]
			features = append(features, map[string]any{
				"id":                 node.ID,
				"type":               node.Type,
				"title":              node.Title,
				"status":             string(node.Status),
				"priority":           string(node.Priority),
				"track_id":           node.TrackID,
				"track_title":        trackTitle,
				"created_at":         node.CreatedAt.Format(time.RFC3339),
				"steps_total":        len(node.Steps),
				"steps_completed":    completed,
				"edges":              edges,
				"created_by_agent":   node.CreatedByAgent,
				"created_by_model":   node.CreatedByModel,
				"created_by_role":    node.CreatedByRole,
				"created_by_cli_ver": node.CreatedByCLIVersion,
			})
		}
	}
	return features
}

// buildTrackTitles parses tracks/*.html and returns a map of track ID -> title.
func buildTrackTitles(projectDir string) map[string]string {
	titles := make(map[string]string)
	pattern := filepath.Join(projectDir, "tracks", "*.html")
	files, _ := filepath.Glob(pattern)
	for _, f := range files {
		node, err := htmlparse.ParseFile(f)
		if err != nil || node == nil {
			continue
		}
		if node.ID != "" {
			titles[node.ID] = node.Title
		}
	}
	return titles
}

// statsHandler returns a summary of counts from the database.
// Falls back to HTML files for feature counts when SQLite features table is empty.
func statsHandler(database *sql.DB, projectDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var total, inProgress, done, todo int
		var activeSessions, totalEvents int

		if err := database.QueryRow(`SELECT COUNT(*) FROM features`).Scan(&total); err != nil && err != sql.ErrNoRows {
			http.Error(w, fmt.Sprintf("query features count: %v", err), http.StatusInternalServerError)
			return
		}

		if total == 0 {
			items := featuresFromHTML(projectDir)
			total = len(items)
			for _, item := range items {
				switch item["status"] {
				case "in-progress":
					inProgress++
				case "done":
					done++
				case "todo":
					todo++
				}
			}
		} else {
			if err := database.QueryRow(`SELECT COUNT(*) FROM features WHERE status='in-progress'`).Scan(&inProgress); err != nil && err != sql.ErrNoRows {
				http.Error(w, fmt.Sprintf("query in-progress count: %v", err), http.StatusInternalServerError)
				return
			}
			if err := database.QueryRow(`SELECT COUNT(*) FROM features WHERE status='done'`).Scan(&done); err != nil && err != sql.ErrNoRows {
				http.Error(w, fmt.Sprintf("query done count: %v", err), http.StatusInternalServerError)
				return
			}
			if err := database.QueryRow(`SELECT COUNT(*) FROM features WHERE status='todo'`).Scan(&todo); err != nil && err != sql.ErrNoRows {
				http.Error(w, fmt.Sprintf("query todo count: %v", err), http.StatusInternalServerError)
				return
			}
		}

		if err := database.QueryRow(`SELECT COUNT(*) FROM sessions WHERE status='active'`).Scan(&activeSessions); err != nil && err != sql.ErrNoRows {
			http.Error(w, fmt.Sprintf("query active sessions: %v", err), http.StatusInternalServerError)
			return
		}
		if err := database.QueryRow(`SELECT COUNT(*) FROM agent_events`).Scan(&totalEvents); err != nil && err != sql.ErrNoRows {
			http.Error(w, fmt.Sprintf("query total events: %v", err), http.StatusInternalServerError)
			return
		}

		// Live sessions: active with event in last 5 minutes
		var liveSessions int
		if err := database.QueryRow(`
			SELECT COUNT(DISTINCT s.session_id) FROM sessions s
			WHERE s.status = 'active' AND s.is_subagent = FALSE
			  AND EXISTS (SELECT 1 FROM agent_events ae
			    WHERE ae.session_id = s.session_id
			      AND ae.timestamp > datetime('now', '-5 minutes'))`).Scan(&liveSessions); err != nil && err != sql.ErrNoRows {
			http.Error(w, fmt.Sprintf("query live sessions: %v", err), http.StatusInternalServerError)
			return
		}

		// Done today
		var doneToday int
		if err := database.QueryRow(`SELECT COUNT(*) FROM features WHERE status='done'
			AND updated_at > datetime('now', '-24 hours')`).Scan(&doneToday); err != nil && err != sql.ErrNoRows {
			http.Error(w, fmt.Sprintf("query done today: %v", err), http.StatusInternalServerError)
			return
		}

		// Errors today
		var errorsToday int
		if err := database.QueryRow(`SELECT COUNT(*) FROM agent_events
			WHERE event_type = 'error'
			AND timestamp > datetime('now', '-24 hours')`).Scan(&errorsToday); err != nil && err != sql.ErrNoRows {
			http.Error(w, fmt.Sprintf("query errors today: %v", err), http.StatusInternalServerError)
			return
		}

		// Cost estimate today (input_tokens * rate + cache * rate + output * rate per model)
		var costToday float64
		if err := database.QueryRow(`
			SELECT COALESCE(SUM(
				CASE
					WHEN model LIKE '%opus%' THEN (input_tokens * 15.0 + cache_read_tokens * 1.50 + output_tokens * 75.0) / 1000000.0
					WHEN model LIKE '%sonnet%' THEN (input_tokens * 3.0 + cache_read_tokens * 0.30 + output_tokens * 15.0) / 1000000.0
					WHEN model LIKE '%haiku%' THEN (input_tokens * 0.80 + cache_read_tokens * 0.08 + output_tokens * 4.0) / 1000000.0
					ELSE (input_tokens * 3.0 + cache_read_tokens * 0.30 + output_tokens * 15.0) / 1000000.0
				END
			), 0) FROM messages WHERE timestamp > datetime('now', '-24 hours')`).Scan(&costToday); err != nil && err != sql.ErrNoRows {
			http.Error(w, fmt.Sprintf("query cost today: %v", err), http.StatusInternalServerError)
			return
		}

		launchMode := ""
		launchTimestamp := ""
		if data, err := os.ReadFile(filepath.Join(projectDir, ".launch-mode")); err == nil {
			content := string(data)
			if strings.Contains(content, `"yolo`) {
				launchMode = "yolo"
			}
			if idx := strings.Index(content, `"timestamp":"`); idx >= 0 {
				rest := content[idx+13:]
				if end := strings.Index(rest, `"`); end >= 0 {
					launchTimestamp = rest[:end]
				}
			}
		}

		respondJSON(w, map[string]any{
			"features_total":       total,
			"features_in_progress": inProgress,
			"features_done":        done,
			"features_todo":        todo,
			"active_sessions":      activeSessions,
			"live_sessions":        liveSessions,
			"done_today":           doneToday,
			"errors_today":         errorsToday,
			"cost_today":           costToday,
			"total_events":         totalEvents,
			"launch_mode":          launchMode,
			"launch_timestamp":     launchTimestamp,
		})
	}
}

// featureDetailHandler returns a single work item parsed from its HTML file.
// Requires ?id=ITEM_ID (e.g. feat-xxx, bug-xxx, spk-xxx, trk-xxx).
func featureDetailHandler(projectDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id parameter required", http.StatusBadRequest)
			return
		}
		for _, subdir := range []string{"features", "bugs", "spikes", "tracks"} {
			path := filepath.Join(projectDir, subdir, id+".html")
			node, err := htmlparse.ParseFile(path)
			if err != nil || node == nil {
				continue
			}
			respondJSON(w, node)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// relatedFeaturesHandler returns features that share files with a given feature.
// Requires ?feature_id=FEATURE_ID. Returns a JSON array of RelatedFeature objects.
func relatedFeaturesHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		featureID := r.URL.Query().Get("feature_id")
		if featureID == "" {
			http.Error(w, "feature_id query parameter required", http.StatusBadRequest)
			return
		}
		related, err := dbpkg.FindRelatedFeatures(database, featureID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if related == nil {
			related = []dbpkg.RelatedFeature{}
		}
		respondJSON(w, related)
	}
}
