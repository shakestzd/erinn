package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// OpenEphemeralProjection opens a private, process-local compatibility
// projection. It never accepts a path and therefore never creates a project DB,
// WAL/SHM sidecar, or cache directory.
func OpenEphemeralProjection() (*sql.DB, error) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open ephemeral projection: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	database.SetConnMaxLifetime(0)
	if err := ApplyPragmas(database, EphemeralPragmas()); err != nil {
		database.Close()
		return nil, fmt.Errorf("open ephemeral projection: apply pragmas: %w", err)
	}
	if err := RunMigrations(database); err != nil {
		database.Close()
		return nil, fmt.Errorf("open ephemeral projection: migrate schema: %w", err)
	}
	if err := VerifyEphemeralPragmas(database); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

func EphemeralPragmas() map[string]string {
	return map[string]string{
		"journal_mode": "MEMORY",
		"synchronous":  "OFF",
		"foreign_keys": "ON",
		"temp_store":   "MEMORY",
		"mmap_size":    "0",
	}
}

func VerifyEphemeralPragmas(db *sql.DB) error {
	checks := map[string]func(string) bool{
		"journal_mode": func(v string) bool { return strings.EqualFold(v, "memory") },
		"temp_store":   func(v string) bool { return v == "2" || strings.EqualFold(v, "memory") },
		"synchronous":  func(v string) bool { return v == "0" || strings.EqualFold(v, "off") },
		"foreign_keys": func(v string) bool { return v == "1" || strings.EqualFold(v, "on") },
		"mmap_size":    func(v string) bool { return v == "0" },
	}
	for pragma, ok := range checks {
		var got string
		if err := db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			if pragma == "mmap_size" && errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return fmt.Errorf("verify ephemeral PRAGMA %s: %w", pragma, err)
		}
		if !ok(got) {
			return fmt.Errorf("verify ephemeral PRAGMA %s: got %q", pragma, got)
		}
	}
	return nil
}
