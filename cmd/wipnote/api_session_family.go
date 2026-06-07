package main

import (
	"database/sql"
	"fmt"
	"net/http"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// familyTreeHandler returns the event tree for all sessions in a given family.
// GET /api/sessions/family?family_id=<id>&limit=50
//
// The response is identical in shape to /api/events/tree (a []turn slice) but
// filtered to only turns that belong to sessions in the requested family. This
// preserves per-session drilldown: the session_id field on each turn lets the
// caller filter to a specific session within the family.
//
// When family_id is not provided or not found, an empty array is returned.
func familyTreeHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		familyID := r.URL.Query().Get("family_id")
		if familyID == "" {
			respondJSON(w, []turn{})
			return
		}

		limit := 50
		if l := r.URL.Query().Get("limit"); l != "" {
			var n int
			if _, err := fmt.Sscanf(l, "%d", &n); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}

		// Fetch all session IDs belonging to this family.
		sessionIDs, err := dbpkg.GetSessionsByFamily(database, familyID)
		if err != nil {
			http.Error(w, fmt.Sprintf("get family sessions: %v", err), http.StatusInternalServerError)
			return
		}
		if len(sessionIDs) == 0 {
			respondJSON(w, []turn{})
			return
		}

		// Build a set of family session IDs for O(1) lookup.
		inFamily := make(map[string]bool, len(sessionIDs))
		for _, sid := range sessionIDs {
			inFamily[sid] = true
		}

		// buildEventTree applies a GLOBAL newest-first limit before any family
		// filtering is possible. If recent, unrelated sessions fill that global
		// window, a family whose turns are older returns empty/incomplete. To
		// fetch family-specific turns before the requested limit bites, grow the
		// global fetch budget until the family is fully represented (>= limit
		// family turns) or a hard ceiling is reached. The ceiling bounds work on
		// pathological histories while still scanning far enough back to surface
		// an older family's turns.
		const familyFetchCeiling = 5000
		fetch := limit * len(sessionIDs)
		if fetch < limit {
			fetch = limit
		}
		var familyTurns []turn
		for {
			allTurns, err := buildEventTree(database, fetch)
			if err != nil {
				http.Error(w, fmt.Sprintf("build event tree: %v", err), http.StatusInternalServerError)
				return
			}

			familyTurns = familyTurns[:0]
			for _, t := range allTurns {
				if inFamily[t.SessionID] {
					familyTurns = append(familyTurns, t)
				}
			}

			// Stop when we have enough family turns to satisfy the requested
			// limit, when the underlying scan is exhausted (fewer rows returned
			// than requested means there is no older data to find), or when the
			// hard ceiling is hit.
			if len(familyTurns) >= limit || len(allTurns) < fetch || fetch >= familyFetchCeiling {
				break
			}
			fetch *= 2
			if fetch > familyFetchCeiling {
				fetch = familyFetchCeiling
			}
		}

		if len(familyTurns) > limit {
			familyTurns = familyTurns[:limit]
		}
		if familyTurns == nil {
			familyTurns = []turn{}
		}
		respondJSON(w, familyTurns)
	}
}

// sessionFamilyHandler returns the family ID and member session IDs for a
// given session. GET /api/sessions/<id>/family
// Used by the dashboard to offer a "view full conversation family" link
// while preserving per-session drilldown as the default view.
func sessionFamilyHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.URL.Query().Get("session_id")
		if sessionID == "" {
			http.Error(w, "session_id required", http.StatusBadRequest)
			return
		}

		// Look up this session's family_id.
		var familyID sql.NullString
		err := database.QueryRow(
			`SELECT session_family_id FROM sessions WHERE session_id = ?`, sessionID,
		).Scan(&familyID)
		if err == sql.ErrNoRows {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, fmt.Sprintf("query session family: %v", err), http.StatusInternalServerError)
			return
		}

		// If no family recorded, return self as the family.
		fid := familyID.String
		if fid == "" {
			fid = sessionID
		}

		members, err := dbpkg.GetSessionsByFamily(database, fid)
		if err != nil {
			http.Error(w, fmt.Sprintf("get family members: %v", err), http.StatusInternalServerError)
			return
		}

		respondJSON(w, map[string]any{
			"session_id":        sessionID,
			"session_family_id": fid,
			"members":           members,
			"member_count":      len(members),
		})
	}
}
