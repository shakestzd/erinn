package main

import (
	"database/sql"
	"net/http"
	"sort"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// parallelSessionIdentity holds the per-session identity fields exposed to
// the dashboard and plan-c3bbb1ed (Kanban rendering). This struct is the
// STABLE API contract — do not remove fields without a deprecation cycle.
type parallelSessionIdentity struct {
	SessionID       string `json:"session_id"`
	Harness         string `json:"harness"`
	Model           string `json:"model"`
	Status          string `json:"status"`
	CreatedAt       string `json:"created_at"`
	SessionFamilyID string `json:"session_family_id"`
	// ExecRoot is the parent_session_id when the session is a subagent, or
	// the session_id itself for root sessions. Derived from the claim identity
	// when available; falls back to session_id.
	ExecRoot string `json:"exec_root"`
	// CanonicalProject is the sessions.project_dir value — the canonical git
	// worktree root (or project dir) that the session is operating on.
	CanonicalProject string `json:"canonical_project"`
	// WorkItemID is the active work item claimed by this session (if any).
	WorkItemID string `json:"work_item_id"`
	// ClaimCollision is true when two or more distinct root sessions hold
	// concurrent active claims on the same work item.
	ClaimCollision bool `json:"claim_collision"`
	// ClaimStatus is the claim status string (e.g. "in_progress", "claimed",
	// "proposed"). Empty when no active claim exists.
	ClaimStatus string `json:"claim_status"`
}

// sessionFamilyGroup groups sessions that share a session_family_id.
// Exposed as Level 2 in the canonical→family→session grouping hierarchy.
type sessionFamilyGroup struct {
	SessionFamilyID string                    `json:"session_family_id"`
	Sessions        []parallelSessionIdentity `json:"sessions"`
	// HasCollision is true when any session in this family has a claim collision.
	HasCollision bool `json:"has_collision"`
}

// projectGroup groups session families by their canonical project root.
// Exposed as Level 1 in the canonical→family→session grouping hierarchy.
type projectGroup struct {
	CanonicalProject string               `json:"canonical_project"`
	Families         []sessionFamilyGroup `json:"families"`
	SessionCount     int                  `json:"session_count"`
	// HasCollision is true when any family in this project has a collision.
	HasCollision bool `json:"has_collision"`
}

// parallelSessionsHandler returns active sessions grouped by:
//
//	Level 1: canonical_project  (sessions.project_dir)
//	Level 2: session_family_id
//	Level 3: individual session with full identity fields
//
// This is the DOCUMENTED default grouping for multi-harness parallel-CLI
// visibility. plan-c3bbb1ed Kanban rendering consumes this endpoint.
//
// GET /api/sessions/parallel
func parallelSessionsHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		rows, err := database.Query(`
			SELECT s.session_id,
			       COALESCE(s.agent_assigned, '') AS harness,
			       COALESCE(s.model, '') AS model,
			       s.status,
			       s.created_at,
			       COALESCE(s.session_family_id, s.session_id) AS family_id,
			       COALESCE(s.project_dir, '') AS project_dir,
			       COALESCE(s.parent_session_id, '') AS parent_session_id,
			       COALESCE(
			           (SELECT work_item_id FROM active_work_items
			            WHERE session_id = s.session_id
			            ORDER BY claimed_at DESC LIMIT 1),
			           s.active_feature_id, '') AS work_item_id
			FROM sessions s
			WHERE s.status = 'active'
			ORDER BY s.project_dir, s.session_family_id, s.created_at`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type rawSession struct {
			parallelSessionIdentity
			parentSessionID string
		}
		var rawSessions []rawSession
		for rows.Next() {
			var rs rawSession
			var createdAt time.Time
			if err := rows.Scan(
				&rs.SessionID, &rs.Harness, &rs.Model, &rs.Status, &createdAt,
				&rs.SessionFamilyID, &rs.CanonicalProject,
				&rs.parentSessionID, &rs.WorkItemID,
			); err != nil {
				continue
			}
			rs.CreatedAt = createdAt.UTC().Format(time.RFC3339)
			// ExecRoot: use parent_session_id when present (subagent case),
			// otherwise the session itself is the root execution context.
			if rs.parentSessionID != "" {
				rs.ExecRoot = rs.parentSessionID
			} else {
				rs.ExecRoot = rs.SessionID
			}
			rawSessions = append(rawSessions, rs)
		}

		// Enrich with claim/collision data — best-effort, never fatal.
		for i := range rawSessions {
			if rawSessions[i].WorkItemID == "" {
				continue
			}
			coll, err := dbpkg.DetectCollaboration(database, rawSessions[i].WorkItemID)
			if err != nil {
				continue
			}
			rawSessions[i].ClaimCollision = coll.HasCollision
			if len(coll.Claimants) > 0 {
				rawSessions[i].ClaimStatus = string(coll.Claimants[0].Status)
			}
		}

		// Group: canonical_project -> session_family_id -> sessions.
		projectIndex := make(map[string]int) // project_dir -> index in projects slice
		var projects []projectGroup

		for _, rs := range rawSessions {
			pi, ok := projectIndex[rs.CanonicalProject]
			if !ok {
				pi = len(projects)
				projectIndex[rs.CanonicalProject] = pi
				projects = append(projects, projectGroup{
					CanonicalProject: rs.CanonicalProject,
				})
			}

			pg := &projects[pi]

			// Find or create the family group within this project.
			familyIdx := -1
			for fi := range pg.Families {
				if pg.Families[fi].SessionFamilyID == rs.SessionFamilyID {
					familyIdx = fi
					break
				}
			}
			if familyIdx < 0 {
				familyIdx = len(pg.Families)
				pg.Families = append(pg.Families, sessionFamilyGroup{
					SessionFamilyID: rs.SessionFamilyID,
				})
			}

			fg := &pg.Families[familyIdx]
			fg.Sessions = append(fg.Sessions, rs.parallelSessionIdentity)
			if rs.ClaimCollision {
				fg.HasCollision = true
				pg.HasCollision = true
			}
			pg.SessionCount++
		}

		// Sort projects by canonical_project for stable output.
		sort.Slice(projects, func(i, j int) bool {
			return projects[i].CanonicalProject < projects[j].CanonicalProject
		})

		respondJSON(w, map[string]any{
			"groups":       projects,
			"active_count": len(rawSessions),
			// Documented grouping order for dashboard and plan-c3bbb1ed consumers.
			"grouping": "canonical_project -> session_family_id -> session",
		})
	}
}
