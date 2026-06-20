package main

import (
	"database/sql"
	"net/http"
	"path/filepath"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

func resumableSessionsHandler(database *sql.DB, wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rows, err := dbpkg.ListResumableSessions(database, dbpkg.LivenessStalenessThreshold(filepath.Dir(wipnoteDir)))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		respondJSON(w, map[string]any{
			"sessions": rows,
			"count":    len(rows),
		})
	}
}
