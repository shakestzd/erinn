package main

import (
	"database/sql"
	"net/http"
	"os"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/ingest"
)

// sessionIngestHandler handles requests under /api/sessions/{id}/.
// It dispatches to the appropriate sub-handler based on the URL suffix:
//   - /ingest  → existing ingest logic (POST)
//   - /preview → new preview handler (GET)
// sessionIngestHandler routes /api/sessions/{id}/preview (read-only) and
// POST /api/sessions/{id}/ingest (writes via ingestSession). bug-74a7bda7:
// the preview sub-route runs on the read-only mux handle; the ingest write
// path runs on the dedicated writable handle (writeDB). writeDB may be nil
// in read-only unit tests — the POST path is only reached when the /ingest
// route is hit, so a nil writeDB never affects preview coverage.
func sessionIngestHandler(database, writeDB *sql.DB) http.HandlerFunc {
	preview := previewHandler(database)
	return func(w http.ResponseWriter, r *http.Request) {
		_, suffix := extractSessionIDWithSuffix(r.URL.Path)
		if suffix == "/preview" {
			preview.ServeHTTP(w, r)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Extract session ID from URL: /api/sessions/{id}/ingest
		sessionID := extractSessionID(r.URL.Path)
		if sessionID == "" {
			http.Error(w, "missing session ID", http.StatusBadRequest)
			return
		}

		// Manual ingest mutates sessions/messages/tool_calls — use the
		// writable handle, never the read-only mux handle (bug-74a7bda7).
		msgCount, toolCount, err := ingestSession(writeDB, sessionID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				respondJSON(w, map[string]any{
					"session_id": sessionID,
					"status":     "not_found",
					"messages":   0,
					"tool_calls": 0,
				})
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		respondJSON(w, map[string]any{
			"session_id": sessionID,
			"status":     "ok",
			"messages":   msgCount,
			"tool_calls": toolCount,
		})
	}
}

// extractSessionIDWithSuffix pulls the session UUID and the action suffix from
// a URL path like /api/sessions/{id}/ingest or /api/sessions/{id}/preview.
// The suffix is returned as "/ingest" or "/preview" (with leading slash).
// Returns ("", "") if the path does not match the expected pattern.
func extractSessionIDWithSuffix(path string) (sessionID, suffix string) {
	path = strings.TrimSuffix(path, "/")
	const prefix = "/api/sessions/"
	if !strings.HasPrefix(path, prefix) {
		return "", ""
	}
	rest := path[len(prefix):]
	// rest must be "{id}/ingest" or "{id}/preview"
	for _, sfx := range []string{"/ingest", "/preview"} {
		if strings.HasSuffix(rest, sfx) {
			id := rest[:len(rest)-len(sfx)]
			if id == "" {
				return "", ""
			}
			return id, sfx
		}
	}
	return "", ""
}

// extractSessionID pulls the session UUID from a URL path like
// /api/sessions/{id}/ingest. Returns empty string if not found.
func extractSessionID(path string) string {
	id, suffix := extractSessionIDWithSuffix(path)
	if suffix != "/ingest" {
		return ""
	}
	return id
}

// ingestSession finds the JSONL file for sessionID, checks whether a
// re-ingest is needed, and stores messages/tool calls. Returns counts of
// newly stored rows. Returns (0, 0, nil) when the session is already
// up-to-date (idempotent skip).
func ingestSession(database *sql.DB, sessionID string) (int, int, error) {
	files, err := ingest.DiscoverSessions("")
	if err != nil {
		return 0, 0, err
	}

	for _, sf := range files {
		if sf.SessionID != sessionID {
			continue
		}

		// Skip re-ingest if file hasn't changed since last sync.
		count, _ := dbpkg.CountMessages(database, sessionID)
		if count > 0 {
			var syncedAt string
			database.QueryRow(
				`SELECT COALESCE(transcript_synced, '') FROM sessions WHERE session_id = ?`,
				sessionID).Scan(&syncedAt)
			if syncedAt != "" {
				if info, statErr := os.Stat(sf.Path); statErr == nil {
					synced, parseErr := time.Parse(time.RFC3339, syncedAt)
					if parseErr == nil && !info.ModTime().After(synced) {
						// Already up-to-date
						return count, 0, nil
					}
				}
			}
			// File changed — clear old messages before re-ingest
			_ = dbpkg.DeleteSessionMessages(database, sessionID)
		}

		result, parseErr := ingest.ParseFile(sf.Path)
		if parseErr != nil {
			return 0, 0, parseErr
		}
		if len(result.Messages) == 0 {
			return 0, 0, nil
		}

		ensureSession(database, sessionID, result, decodeProjectDirFromSessionFile(sf))
		msgCount, toolCount := storeParseResult(database, sessionID, "", result)
		_ = dbpkg.UpdateTranscriptSync(database, sessionID, sf.Path)
		return msgCount, toolCount, nil
	}

	return 0, 0, &notFoundError{sessionID}
}

type notFoundError struct{ id string }

func (e *notFoundError) Error() string {
	return "session " + e.id + " not found"
}
