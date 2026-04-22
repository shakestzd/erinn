// Package db provides SQLite database operations for HtmlGraph.
//
// Uses modernc.org/sqlite (pure Go, no CGo) for maximum portability.
package db

import (
	"database/sql"
	"fmt"
	"log"
)

// Pragmas mirrors the Python PRAGMA_SETTINGS from pragmas.py.
var Pragmas = map[string]string{
	"journal_mode": "DELETE",
	"synchronous":  "NORMAL",
	"foreign_keys": "1",
	"busy_timeout": "5000",
	"cache_size":   "-64000",
	"temp_store":   "MEMORY",
	"mmap_size":    "0",
}

// ApplyPragmas sets all performance PRAGMAs on a database connection.
// Some PRAGMAs (busy_timeout, cache_size) are best-effort and may not apply
// to all backing stores (e.g., in-memory SQLite); failures are logged at debug
// level and don't block the Open. Other PRAGMAs are required.
func ApplyPragmas(db *sql.DB) error {
	// PRAGMAs that are REQUIRED — fail Open if these don't apply.
	required := []string{"journal_mode", "synchronous", "foreign_keys", "temp_store", "mmap_size"}
	// PRAGMAs that are best-effort — failure is logged at debug level
	// and doesn't block Open (some drivers/backing stores reject these).
	optional := []string{"busy_timeout", "cache_size"}

	for _, pragma := range required {
		value, ok := Pragmas[pragma]
		if !ok {
			continue
		}
		_, err := db.Exec(fmt.Sprintf("PRAGMA %s = %s", pragma, value))
		if err != nil {
			return fmt.Errorf("applying PRAGMA %s: %w", pragma, err)
		}
	}

	for _, pragma := range optional {
		value, ok := Pragmas[pragma]
		if !ok {
			continue
		}
		_, err := db.Exec(fmt.Sprintf("PRAGMA %s = %s", pragma, value))
		if err != nil {
			// Best-effort: log at debug, continue. In-memory DBs in tests
			// may reject busy_timeout / cache_size; that's fine because
			// they aren't subject to the contention these PRAGMAs protect
			// against.
			log.Printf("debug: skipping PRAGMA %s (not supported on this backing): %v", pragma, err)
		}
	}
	return nil
}

// RunOptimize executes PRAGMA optimize for planner/statistics upkeep.
func RunOptimize(db *sql.DB) error {
	_, err := db.Exec("PRAGMA optimize")
	return err
}

// CheckIntegrity runs integrity_check and foreign_key_check.
// Returns true if the database passes both checks.
func CheckIntegrity(db *sql.DB) (bool, error) {
	row := db.QueryRow("PRAGMA integrity_check")
	var result string
	if err := row.Scan(&result); err != nil {
		return false, fmt.Errorf("integrity_check: %w", err)
	}
	if result != "ok" {
		return false, nil
	}

	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return false, fmt.Errorf("foreign_key_check: %w", err)
	}
	defer rows.Close()

	if rows.Next() {
		// Any row means a violation exists.
		return false, nil
	}
	return true, rows.Err()
}
